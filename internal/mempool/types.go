package mempool

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

// Status represents the state of a UserOperation in the mempool.
type Status string

const (
	StatusPending   Status = "pending"
	StatusBundling  Status = "bundling"
	StatusSubmitted Status = "submitted"
	StatusConfirmed Status = "confirmed"
	StatusFailed    Status = "failed"
	StatusReplaced  Status = "replaced"
)

// IsTerminal returns true if the status is a final state.
func (s Status) IsTerminal() bool {
	return s == StatusConfirmed || s == StatusFailed || s == StatusReplaced
}

// StoredOp is a UserOperation with mempool metadata.
type StoredOp struct {
	core.PackedUserOp
	Hash          common.Hash
	Status        Status
	BundleTxHash  *common.Hash
	BundleIndex   *int
	SubmittedAt   *int64
	BlockNumber   *uint64
	RevertReason  *string
	ActualGasCost *big.Int
	ReceivedAt    int64
	UpdatedAt     int64
}

// StatusExtra holds optional fields for status updates.
type StatusExtra struct {
	BundleTxHash  *common.Hash
	BundleIndex   *int
	SubmittedAt   *int64
	BlockNumber   *uint64
	RevertReason  *string
	ActualGasCost *big.Int
}

// Repository defines the mempool storage interface.
type Repository interface {
	Insert(op *core.PackedUserOp, hash common.Hash) error
	Replace(oldHash common.Hash, newOp *core.PackedUserOp, newHash common.Hash) error
	GetPending(limit int) ([]*StoredOp, error)
	ClaimForBundling(hashes []common.Hash) error
	UpdateStatus(hash common.Hash, status Status, extra *StatusExtra) error
	GetByHash(hash common.Hash) (*StoredOp, error)
	GetConfirmedByHash(hash common.Hash) (*StoredOp, error)
	GetByBundleTx(txHash common.Hash) ([]*StoredOp, error)
	GetByStatus(status Status) ([]*StoredOp, error)
	ResetBundlingToPending() (int, error)
	MarkTtlExpired(cutoffMs int64) (int, error)
	PruneHistory(cutoffMs int64) (int, error)
	Close() error
}
