package mempool

import (
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"go.uber.org/zap"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS user_operations (
  hash                 TEXT PRIMARY KEY,
  sender               TEXT NOT NULL,
  nonce                TEXT NOT NULL,

  init_code            TEXT NOT NULL,
  call_data            TEXT NOT NULL,
  account_gas_limits   TEXT NOT NULL,
  pre_verification_gas TEXT NOT NULL,
  gas_fees             TEXT NOT NULL,
  paymaster_and_data   TEXT NOT NULL,
  signature            TEXT NOT NULL,

  status               TEXT NOT NULL DEFAULT 'pending',

  bundle_tx_hash       TEXT,
  bundle_index         INTEGER,
  submitted_at         INTEGER,
  block_number         INTEGER,
  revert_reason        TEXT,
  actual_gas_cost      TEXT,

  received_at          INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_userop_status ON user_operations(status);
CREATE INDEX IF NOT EXISTS idx_userop_sender ON user_operations(sender);
CREATE UNIQUE INDEX IF NOT EXISTS idx_userop_sender_nonce ON user_operations(sender, nonce)
  WHERE status NOT IN ('confirmed', 'failed', 'replaced');
`

// SQLiteRepo implements Repository backed by SQLite.
type SQLiteRepo struct {
	db  *sql.DB
	log *zap.Logger
}

// Open creates or opens a SQLite database at path and applies the schema.
func Open(path string, log *zap.Logger) (*SQLiteRepo, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// WAL mode for concurrent reads during bundling writes.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &SQLiteRepo{db: db, log: log}, nil
}

func (r *SQLiteRepo) Close() error {
	return r.db.Close()
}

func (r *SQLiteRepo) Insert(op *core.PackedUserOp, hash common.Hash) error {
	now := time.Now().UnixMilli()
	_, err := r.db.Exec(`
		INSERT INTO user_operations (
			hash, sender, nonce,
			init_code, call_data, account_gas_limits,
			pre_verification_gas, gas_fees, paymaster_and_data, signature,
			status, received_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		hash.Hex(),
		strings.ToLower(op.Sender.Hex()),
		core.BigToHex(op.Nonce),
		core.BytesToHex(op.InitCode),
		core.BytesToHex(op.CallData),
		core.BytesToHex(op.AccountGasLimits[:]),
		core.BigToHex(op.PreVerificationGas),
		core.BytesToHex(op.GasFees[:]),
		core.BytesToHex(op.PaymasterAndData),
		core.BytesToHex(op.Signature),
		now, now,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("duplicate sender+nonce: op already exists in mempool")
		}
		return fmt.Errorf("insert op: %w", err)
	}
	return nil
}

func (r *SQLiteRepo) Replace(oldHash common.Hash, newOp *core.PackedUserOp, newHash common.Hash) error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Check old op exists and is in a replaceable state (fix R3: reject bundling/submitted).
	var oldStatus string
	err = tx.QueryRow("SELECT status FROM user_operations WHERE hash = ?", oldHash.Hex()).Scan(&oldStatus)
	if err != nil {
		return fmt.Errorf("old op not found: %w", err)
	}
	if oldStatus != string(StatusPending) {
		return fmt.Errorf("cannot replace op in %s status", oldStatus)
	}

	now := time.Now().UnixMilli()

	// Mark old op as replaced.
	_, err = tx.Exec("UPDATE user_operations SET status = 'replaced', updated_at = ? WHERE hash = ?",
		now, oldHash.Hex())
	if err != nil {
		return fmt.Errorf("mark replaced: %w", err)
	}

	// Insert new op.
	_, err = tx.Exec(`
		INSERT INTO user_operations (
			hash, sender, nonce,
			init_code, call_data, account_gas_limits,
			pre_verification_gas, gas_fees, paymaster_and_data, signature,
			status, received_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		newHash.Hex(),
		strings.ToLower(newOp.Sender.Hex()),
		core.BigToHex(newOp.Nonce),
		core.BytesToHex(newOp.InitCode),
		core.BytesToHex(newOp.CallData),
		core.BytesToHex(newOp.AccountGasLimits[:]),
		core.BigToHex(newOp.PreVerificationGas),
		core.BytesToHex(newOp.GasFees[:]),
		core.BytesToHex(newOp.PaymasterAndData),
		core.BytesToHex(newOp.Signature),
		now, now,
	)
	if err != nil {
		return fmt.Errorf("insert replacement: %w", err)
	}

	return tx.Commit()
}

func (r *SQLiteRepo) GetPending(limit int) ([]*StoredOp, error) {
	rows, err := r.db.Query(
		"SELECT * FROM user_operations WHERE status = 'pending' ORDER BY received_at ASC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOps(rows)
}

func (r *SQLiteRepo) ClaimForBundling(hashes []common.Hash) error {
	if len(hashes) == 0 {
		return nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	for _, h := range hashes {
		res, err := tx.Exec(
			"UPDATE user_operations SET status = 'bundling', updated_at = ? WHERE hash = ? AND status = 'pending'",
			now, h.Hex(),
		)
		if err != nil {
			return fmt.Errorf("claim %s: %w", h.Hex(), err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("claim %s: op not pending", h.Hex())
		}
	}

	return tx.Commit()
}

func (r *SQLiteRepo) UpdateStatus(hash common.Hash, status Status, extra *StatusExtra) error {
	now := time.Now().UnixMilli()

	sets := []string{"status = ?", "updated_at = ?"}
	args := []any{string(status), now}

	if extra != nil {
		if extra.BundleTxHash != nil {
			sets = append(sets, "bundle_tx_hash = ?")
			args = append(args, extra.BundleTxHash.Hex())
		}
		if extra.BundleIndex != nil {
			sets = append(sets, "bundle_index = ?")
			args = append(args, *extra.BundleIndex)
		}
		if extra.SubmittedAt != nil {
			sets = append(sets, "submitted_at = ?")
			args = append(args, *extra.SubmittedAt)
		}
		if extra.BlockNumber != nil {
			sets = append(sets, "block_number = ?")
			args = append(args, *extra.BlockNumber)
		}
		if extra.RevertReason != nil {
			sets = append(sets, "revert_reason = ?")
			args = append(args, *extra.RevertReason)
		}
		if extra.ActualGasCost != nil {
			sets = append(sets, "actual_gas_cost = ?")
			args = append(args, extra.ActualGasCost.String())
		}
	}

	args = append(args, hash.Hex())
	query := fmt.Sprintf("UPDATE user_operations SET %s WHERE hash = ?", strings.Join(sets, ", "))

	res, err := r.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("op %s not found", hash.Hex())
	}
	return nil
}

func (r *SQLiteRepo) GetByHash(hash common.Hash) (*StoredOp, error) {
	row := r.db.QueryRow("SELECT * FROM user_operations WHERE hash = ?", hash.Hex())
	return scanOp(row)
}

func (r *SQLiteRepo) GetConfirmedByHash(hash common.Hash) (*StoredOp, error) {
	row := r.db.QueryRow(
		"SELECT * FROM user_operations WHERE hash = ? AND status = 'confirmed'",
		hash.Hex(),
	)
	return scanOp(row)
}

func (r *SQLiteRepo) GetByBundleTx(txHash common.Hash) ([]*StoredOp, error) {
	rows, err := r.db.Query(
		"SELECT * FROM user_operations WHERE bundle_tx_hash = ? ORDER BY bundle_index ASC",
		txHash.Hex(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOps(rows)
}

func (r *SQLiteRepo) GetByStatus(status Status) ([]*StoredOp, error) {
	rows, err := r.db.Query(
		"SELECT * FROM user_operations WHERE status = ? ORDER BY received_at ASC",
		string(status),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOps(rows)
}

func (r *SQLiteRepo) ResetBundlingToPending() (int, error) {
	now := time.Now().UnixMilli()
	res, err := r.db.Exec(
		"UPDATE user_operations SET status = 'pending', updated_at = ? WHERE status = 'bundling'",
		now,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (r *SQLiteRepo) MarkTtlExpired(cutoffMs int64) (int, error) {
	now := time.Now().UnixMilli()
	res, err := r.db.Exec(
		"UPDATE user_operations SET status = 'failed', revert_reason = 'ttl_expired', updated_at = ? WHERE status = 'pending' AND received_at < ?",
		now, cutoffMs,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (r *SQLiteRepo) PruneHistory(cutoffMs int64) (int, error) {
	res, err := r.db.Exec(
		"DELETE FROM user_operations WHERE status IN ('confirmed', 'failed', 'replaced') AND updated_at < ?",
		cutoffMs,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// scanOp scans a single row into a StoredOp.
func scanOp(row *sql.Row) (*StoredOp, error) {
	var (
		hashHex, senderHex, nonceHex                         string
		initCodeHex, callDataHex, aglHex                     string
		pvgHex, gasFeeHex, pmDataHex, sigHex                 string
		statusStr                                            string
		bundleTxHash, revertReason, actualGasCostStr         sql.NullString
		bundleIndex                                          sql.NullInt64
		submittedAt, blockNumber                             sql.NullInt64
		receivedAt, updatedAt                                int64
	)

	err := row.Scan(
		&hashHex, &senderHex, &nonceHex,
		&initCodeHex, &callDataHex, &aglHex,
		&pvgHex, &gasFeeHex, &pmDataHex, &sigHex,
		&statusStr,
		&bundleTxHash, &bundleIndex, &submittedAt, &blockNumber,
		&revertReason, &actualGasCostStr,
		&receivedAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	return hydrateOp(
		hashHex, senderHex, nonceHex,
		initCodeHex, callDataHex, aglHex,
		pvgHex, gasFeeHex, pmDataHex, sigHex,
		statusStr,
		bundleTxHash, bundleIndex, submittedAt, blockNumber,
		revertReason, actualGasCostStr,
		receivedAt, updatedAt,
	)
}

// scanOps scans multiple rows into StoredOps.
func scanOps(rows *sql.Rows) ([]*StoredOp, error) {
	var ops []*StoredOp
	for rows.Next() {
		var (
			hashHex, senderHex, nonceHex                         string
			initCodeHex, callDataHex, aglHex                     string
			pvgHex, gasFeeHex, pmDataHex, sigHex                 string
			statusStr                                            string
			bundleTxHash, revertReason, actualGasCostStr         sql.NullString
			bundleIndex                                          sql.NullInt64
			submittedAt, blockNumber                             sql.NullInt64
			receivedAt, updatedAt                                int64
		)

		err := rows.Scan(
			&hashHex, &senderHex, &nonceHex,
			&initCodeHex, &callDataHex, &aglHex,
			&pvgHex, &gasFeeHex, &pmDataHex, &sigHex,
			&statusStr,
			&bundleTxHash, &bundleIndex, &submittedAt, &blockNumber,
			&revertReason, &actualGasCostStr,
			&receivedAt, &updatedAt,
		)
		if err != nil {
			return nil, err
		}

		op, err := hydrateOp(
			hashHex, senderHex, nonceHex,
			initCodeHex, callDataHex, aglHex,
			pvgHex, gasFeeHex, pmDataHex, sigHex,
			statusStr,
			bundleTxHash, bundleIndex, submittedAt, blockNumber,
			revertReason, actualGasCostStr,
			receivedAt, updatedAt,
		)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

func hydrateOp(
	hashHex, senderHex, nonceHex string,
	initCodeHex, callDataHex, aglHex string,
	pvgHex, gasFeeHex, pmDataHex, sigHex string,
	statusStr string,
	bundleTxHash sql.NullString, bundleIndex sql.NullInt64,
	submittedAt sql.NullInt64, blockNumber sql.NullInt64,
	revertReason sql.NullString, actualGasCostStr sql.NullString,
	receivedAt, updatedAt int64,
) (*StoredOp, error) {
	op := &StoredOp{
		Hash:       common.HexToHash(hashHex),
		Status:     Status(statusStr),
		ReceivedAt: receivedAt,
		UpdatedAt:  updatedAt,
	}

	op.Sender = common.HexToAddress(senderHex)

	nonce, err := core.HexToBigInt(nonceHex)
	if err != nil {
		return nil, fmt.Errorf("parse nonce: %w", err)
	}
	op.Nonce = nonce

	op.InitCode, _ = core.DecodeHex(initCodeHex)
	op.CallData, _ = core.DecodeHex(callDataHex)

	aglBytes, _ := core.DecodeHex(aglHex)
	if len(aglBytes) == 32 {
		copy(op.AccountGasLimits[:], aglBytes)
	}

	pvg, err := core.HexToBigInt(pvgHex)
	if err != nil {
		return nil, fmt.Errorf("parse preVerificationGas: %w", err)
	}
	op.PreVerificationGas = pvg

	gfBytes, _ := core.DecodeHex(gasFeeHex)
	if len(gfBytes) == 32 {
		copy(op.GasFees[:], gfBytes)
	}

	op.PaymasterAndData, _ = core.DecodeHex(pmDataHex)
	op.Signature, _ = core.DecodeHex(sigHex)

	if bundleTxHash.Valid {
		h := common.HexToHash(bundleTxHash.String)
		op.BundleTxHash = &h
	}
	if bundleIndex.Valid {
		idx := int(bundleIndex.Int64)
		op.BundleIndex = &idx
	}
	if submittedAt.Valid {
		v := submittedAt.Int64
		op.SubmittedAt = &v
	}
	if blockNumber.Valid {
		v := uint64(blockNumber.Int64)
		op.BlockNumber = &v
	}
	if revertReason.Valid {
		op.RevertReason = &revertReason.String
	}
	if actualGasCostStr.Valid {
		v, ok := new(big.Int).SetString(actualGasCostStr.String, 10)
		if ok {
			op.ActualGasCost = v
		}
	}

	return op, nil
}
