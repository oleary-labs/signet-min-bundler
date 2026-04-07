package mempool

import (
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"go.uber.org/zap"
)

func testRepo(t *testing.T) *SQLiteRepo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	repo, err := Open(path, zap.NewNop())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func testOp(sender common.Address, nonce int64) (*core.PackedUserOp, common.Hash) {
	op := &core.PackedUserOp{
		Sender:             sender,
		Nonce:              big.NewInt(nonce),
		InitCode:           nil,
		CallData:           []byte{0xb6, 0x1d, 0x27, 0xf6},
		AccountGasLimits:   core.PackUint128s(31000, 50000),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		PaymasterAndData:   nil,
		Signature:          make([]byte, 65),
	}
	hash := core.ComputeUserOpHash(op, common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032"), 1)
	return op, hash
}

func TestInsertAndGetByHash(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)

	if err := repo.Insert(op, hash); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByHash(hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.Hash != hash {
		t.Errorf("hash mismatch")
	}
	if got.Status != StatusPending {
		t.Errorf("status = %s, want pending", got.Status)
	}
	if got.Sender != sender {
		t.Errorf("sender = %s, want %s", got.Sender.Hex(), sender.Hex())
	}
	if got.Nonce.Int64() != 0 {
		t.Errorf("nonce = %d, want 0", got.Nonce.Int64())
	}
}

func TestInsertDuplicateSenderNonce(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op1, hash1 := testOp(sender, 0)

	if err := repo.Insert(op1, hash1); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Same sender+nonce, different hash — should fail due to unique index.
	op2 := *op1
	op2.Signature = []byte{0xff, 0xfe} // different sig to get different hash
	op2.Signature = append(op2.Signature, make([]byte, 63)...)
	hash2 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	err := repo.Insert(&op2, hash2)
	if err == nil {
		t.Error("expected error for duplicate sender+nonce")
	}
}

func TestDuplicateSenderNonceAllowedAfterTerminal(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op1, hash1 := testOp(sender, 0)

	if err := repo.Insert(op1, hash1); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Mark as failed (terminal) — should free the sender+nonce slot.
	reason := "test"
	if err := repo.UpdateStatus(hash1, StatusFailed, &StatusExtra{RevertReason: &reason}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// New op with same sender+nonce should now succeed.
	hash2 := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	if err := repo.Insert(op1, hash2); err != nil {
		t.Errorf("Insert after terminal should succeed: %v", err)
	}
}

func TestGetPendingFIFO(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	var hashes []common.Hash
	for i := int64(0); i < 5; i++ {
		op, hash := testOp(sender, i)
		if err := repo.Insert(op, hash); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		hashes = append(hashes, hash)
		time.Sleep(time.Millisecond) // ensure ordering
	}

	got, err := repo.GetPending(3)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d ops, want 3", len(got))
	}
	for i, op := range got {
		if op.Hash != hashes[i] {
			t.Errorf("op[%d] hash mismatch: got %s, want %s", i, op.Hash.Hex(), hashes[i].Hex())
		}
	}
}

func TestClaimForBundling(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)

	if err := repo.Insert(op, hash); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := repo.ClaimForBundling([]common.Hash{hash}); err != nil {
		t.Fatalf("ClaimForBundling: %v", err)
	}

	got, err := repo.GetByHash(hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.Status != StatusBundling {
		t.Errorf("status = %s, want bundling", got.Status)
	}

	// Should not appear in pending anymore.
	pending, err := repo.GetPending(10)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("got %d pending, want 0", len(pending))
	}
}

func TestClaimNotPendingFails(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)

	if err := repo.Insert(op, hash); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Claim once — succeeds.
	if err := repo.ClaimForBundling([]common.Hash{hash}); err != nil {
		t.Fatalf("first claim: %v", err)
	}

	// Claim again — should fail (already bundling).
	err := repo.ClaimForBundling([]common.Hash{hash})
	if err == nil {
		t.Error("expected error for double-claim")
	}
}

func TestUpdateStatusSubmitted(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)
	repo.Insert(op, hash)
	repo.ClaimForBundling([]common.Hash{hash})

	txHash := common.HexToHash("0xabcd")
	idx := 0
	now := time.Now().UnixMilli()
	err := repo.UpdateStatus(hash, StatusSubmitted, &StatusExtra{
		BundleTxHash: &txHash,
		BundleIndex:  &idx,
		SubmittedAt:  &now,
	})
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, _ := repo.GetByHash(hash)
	if got.Status != StatusSubmitted {
		t.Errorf("status = %s", got.Status)
	}
	if got.BundleTxHash == nil || *got.BundleTxHash != txHash {
		t.Error("bundle_tx_hash mismatch")
	}
	if got.BundleIndex == nil || *got.BundleIndex != 0 {
		t.Error("bundle_index mismatch")
	}
}

func TestUpdateStatusConfirmed(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)
	repo.Insert(op, hash)

	blockNum := uint64(12345)
	gasCost := big.NewInt(100000)
	err := repo.UpdateStatus(hash, StatusConfirmed, &StatusExtra{
		BlockNumber:   &blockNum,
		ActualGasCost: gasCost,
	})
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, _ := repo.GetConfirmedByHash(hash)
	if got == nil {
		t.Fatal("GetConfirmedByHash returned nil")
	}
	if *got.BlockNumber != 12345 {
		t.Errorf("block_number = %d", *got.BlockNumber)
	}
	if got.ActualGasCost.Cmp(gasCost) != 0 {
		t.Errorf("actual_gas_cost = %s", got.ActualGasCost)
	}
}

func TestGetByBundleTx(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	txHash := common.HexToHash("0xbundle")

	for i := int64(0); i < 3; i++ {
		op, hash := testOp(sender, i)
		repo.Insert(op, hash)
		idx := int(i)
		now := time.Now().UnixMilli()
		repo.UpdateStatus(hash, StatusSubmitted, &StatusExtra{
			BundleTxHash: &txHash,
			BundleIndex:  &idx,
			SubmittedAt:  &now,
		})
	}

	ops, err := repo.GetByBundleTx(txHash)
	if err != nil {
		t.Fatalf("GetByBundleTx: %v", err)
	}
	if len(ops) != 3 {
		t.Errorf("got %d ops, want 3", len(ops))
	}
	// Should be ordered by bundle_index.
	for i, op := range ops {
		if *op.BundleIndex != i {
			t.Errorf("op[%d] bundle_index = %d", i, *op.BundleIndex)
		}
	}
}

func TestReplacePending(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op1, hash1 := testOp(sender, 0)
	repo.Insert(op1, hash1)

	// Create replacement with higher gas.
	op2 := *op1
	op2.GasFees = core.PackBigInts(big.NewInt(2_000_000_000), big.NewInt(100_000_000_000))
	hash2 := common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")

	if err := repo.Replace(hash1, &op2, hash2); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	old, _ := repo.GetByHash(hash1)
	if old.Status != StatusReplaced {
		t.Errorf("old status = %s, want replaced", old.Status)
	}

	new_, _ := repo.GetByHash(hash2)
	if new_.Status != StatusPending {
		t.Errorf("new status = %s, want pending", new_.Status)
	}
}

func TestReplaceBundlingFails(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op1, hash1 := testOp(sender, 0)
	repo.Insert(op1, hash1)
	repo.ClaimForBundling([]common.Hash{hash1})

	op2 := *op1
	hash2 := common.HexToHash("0x4444444444444444444444444444444444444444444444444444444444444444")
	err := repo.Replace(hash1, &op2, hash2)
	if err == nil {
		t.Error("expected error replacing bundling op (R3 fix)")
	}
}

func TestReplaceSubmittedFails(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op1, hash1 := testOp(sender, 0)
	repo.Insert(op1, hash1)
	repo.ClaimForBundling([]common.Hash{hash1})
	txH := common.HexToHash("0xaabb")
	idx := 0
	now := time.Now().UnixMilli()
	repo.UpdateStatus(hash1, StatusSubmitted, &StatusExtra{BundleTxHash: &txH, BundleIndex: &idx, SubmittedAt: &now})

	op2 := *op1
	hash2 := common.HexToHash("0x5555555555555555555555555555555555555555555555555555555555555555")
	err := repo.Replace(hash1, &op2, hash2)
	if err == nil {
		t.Error("expected error replacing submitted op (R3 fix)")
	}
}

func TestResetBundlingToPending(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)
	repo.Insert(op, hash)
	repo.ClaimForBundling([]common.Hash{hash})

	n, err := repo.ResetBundlingToPending()
	if err != nil {
		t.Fatalf("ResetBundlingToPending: %v", err)
	}
	if n != 1 {
		t.Errorf("reset %d, want 1", n)
	}

	got, _ := repo.GetByHash(hash)
	if got.Status != StatusPending {
		t.Errorf("status = %s, want pending", got.Status)
	}
}

func TestMarkTtlExpired(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)
	repo.Insert(op, hash)

	// Use a cutoff in the future to expire everything.
	n, err := repo.MarkTtlExpired(time.Now().UnixMilli() + 60_000)
	if err != nil {
		t.Fatalf("MarkTtlExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("expired %d, want 1", n)
	}

	got, _ := repo.GetByHash(hash)
	if got.Status != StatusFailed {
		t.Errorf("status = %s, want failed", got.Status)
	}
	if got.RevertReason == nil || *got.RevertReason != "ttl_expired" {
		t.Error("revert_reason should be ttl_expired")
	}
}

func TestPruneHistory(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	op, hash := testOp(sender, 0)
	repo.Insert(op, hash)
	reason := "test"
	repo.UpdateStatus(hash, StatusFailed, &StatusExtra{RevertReason: &reason})

	// Cutoff in the future — should delete.
	n, err := repo.PruneHistory(time.Now().UnixMilli() + 60_000)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}

	got, err := repo.GetByHash(hash)
	if err == nil && got != nil {
		t.Error("op should be deleted after prune")
	}
}

func TestGetByStatus(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	op1, hash1 := testOp(sender, 0)
	repo.Insert(op1, hash1)

	op2, hash2 := testOp(sender, 1)
	repo.Insert(op2, hash2)
	repo.ClaimForBundling([]common.Hash{hash2})

	pending, _ := repo.GetByStatus(StatusPending)
	if len(pending) != 1 {
		t.Errorf("pending = %d, want 1", len(pending))
	}

	bundling, _ := repo.GetByStatus(StatusBundling)
	if len(bundling) != 1 {
		t.Errorf("bundling = %d, want 1", len(bundling))
	}
}

func TestFieldRoundTrip(t *testing.T) {
	repo := testRepo(t)
	sender := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	op := &core.PackedUserOp{
		Sender:             sender,
		Nonce:              big.NewInt(42),
		InitCode:           []byte{0x12, 0x34},
		CallData:           []byte{0xb6, 0x1d, 0x27, 0xf6, 0xaa, 0xbb},
		AccountGasLimits:   core.PackUint128s(31000, 50000),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		PaymasterAndData:   []byte{0xde, 0xad},
		Signature:          make([]byte, 65),
	}
	op.Signature[0] = 0xff
	hash := core.ComputeUserOpHash(op, common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032"), 1)

	repo.Insert(op, hash)

	got, err := repo.GetByHash(hash)
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}

	if got.Nonce.Int64() != 42 {
		t.Errorf("nonce = %d", got.Nonce.Int64())
	}
	if len(got.InitCode) != 2 || got.InitCode[0] != 0x12 {
		t.Errorf("initCode round-trip failed")
	}
	if got.PreVerificationGas.Int64() != 50000 {
		t.Errorf("preVerificationGas = %d", got.PreVerificationGas.Int64())
	}
	if got.AccountGasLimits != op.AccountGasLimits {
		t.Error("accountGasLimits round-trip failed")
	}
	if got.GasFees != op.GasFees {
		t.Error("gasFees round-trip failed")
	}
	if len(got.PaymasterAndData) != 2 || got.PaymasterAndData[0] != 0xde {
		t.Error("paymasterAndData round-trip failed")
	}
	if got.Signature[0] != 0xff {
		t.Error("signature round-trip failed")
	}
}
