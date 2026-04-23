// Package sponsor manages invite codes and sender whitelisting for
// paymaster sponsorship gating.
package sponsor

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Store manages invite codes and whitelisted senders in SQLite.
type Store struct {
	db *sql.DB
}

// Open creates a Store using an existing database connection.
func Open(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate sponsor tables: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS invite_codes (
			code       TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			used_by    TEXT,
			used_at    INTEGER
		);
		CREATE TABLE IF NOT EXISTS whitelisted_senders (
			sender     TEXT PRIMARY KEY,
			invite_code TEXT,
			created_at INTEGER NOT NULL
		);
	`)
	return err
}

// GenerateCodes creates n new invite codes and returns them.
func (s *Store) GenerateCodes(n int) ([]string, error) {
	codes := make([]string, n)
	now := time.Now().UnixMilli()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO invite_codes (code, created_at) VALUES (?, ?)")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	for i := 0; i < n; i++ {
		code := generateCode()
		if _, err := stmt.Exec(code, now); err != nil {
			return nil, err
		}
		codes[i] = code
	}

	return codes, tx.Commit()
}

// CheckSender returns true if the sender is whitelisted.
func (s *Store) CheckSender(sender common.Address) bool {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM whitelisted_senders WHERE sender = ?",
		strings.ToLower(sender.Hex()),
	).Scan(&count)
	return err == nil && count > 0
}

// RedeemCode validates an invite code and whitelists the sender.
// Returns nil on success, error if the code is invalid or already used.
func (s *Store) RedeemCode(code string, sender common.Address) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Check if sender is already whitelisted.
	var count int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM whitelisted_senders WHERE sender = ?",
		strings.ToLower(sender.Hex()),
	).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // already whitelisted
	}

	// Check the invite code.
	var usedBy sql.NullString
	err = tx.QueryRow(
		"SELECT used_by FROM invite_codes WHERE code = ?", code,
	).Scan(&usedBy)
	if err == sql.ErrNoRows {
		return fmt.Errorf("invalid invite code")
	}
	if err != nil {
		return err
	}
	if usedBy.Valid {
		return fmt.Errorf("invite code already used")
	}

	// Redeem: mark code as used and whitelist sender.
	now := time.Now().UnixMilli()
	senderHex := strings.ToLower(sender.Hex())

	if _, err := tx.Exec(
		"UPDATE invite_codes SET used_by = ?, used_at = ? WHERE code = ?",
		senderHex, now, code,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(
		"INSERT INTO whitelisted_senders (sender, invite_code, created_at) VALUES (?, ?, ?)",
		senderHex, code, now,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// generateCode returns a random 8-character hex string (e.g. "a3f1b2c4").
func generateCode() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}
