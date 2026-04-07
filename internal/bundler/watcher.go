package bundler

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"go.uber.org/zap"
)

// ReceiptWatcher polls for submitted bundle transaction receipts.
type ReceiptWatcher struct {
	repo       mempool.Repository
	client     BundleClient
	entryPoint common.Address
	interval   time.Duration
	log        *zap.Logger
}

// NewWatcher creates a ReceiptWatcher.
func NewWatcher(
	repo mempool.Repository,
	client BundleClient,
	entryPoint common.Address,
	interval time.Duration,
	log *zap.Logger,
) *ReceiptWatcher {
	return &ReceiptWatcher{
		repo:       repo,
		client:     client,
		entryPoint: entryPoint,
		interval:   interval,
		log:        log,
	}
}

// Run starts the receipt watcher loop.
func (w *ReceiptWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.Poll(ctx); err != nil {
				w.log.Error("receipt poll failed", zap.Error(err))
			}
		}
	}
}

// Poll checks receipts for all submitted ops. Exported for testing.
func (w *ReceiptWatcher) Poll(ctx context.Context) error {
	submitted, err := w.repo.GetByStatus(mempool.StatusSubmitted)
	if err != nil {
		return err
	}
	if len(submitted) == 0 {
		return nil
	}

	// Group ops by bundle tx hash to avoid redundant receipt fetches.
	byTx := map[common.Hash][]*mempool.StoredOp{}
	for _, op := range submitted {
		if op.BundleTxHash == nil {
			continue
		}
		byTx[*op.BundleTxHash] = append(byTx[*op.BundleTxHash], op)
	}

	for txHash, ops := range byTx {
		receipt, err := w.client.TransactionReceipt(ctx, txHash)
		if err != nil {
			if !isNotFound(err) {
				w.log.Warn("failed to fetch receipt",
					zap.String("tx", txHash.Hex()),
					zap.Error(err))
			}
			continue
		}

		if receipt.Status == 1 {
			// Bundle succeeded — parse per-op outcomes from logs.
			outcomes := ParseHandleOpsReceipt(receipt, w.entryPoint)
			for _, op := range ops {
				outcome, ok := outcomes[op.Hash]
				if !ok {
					// Op not found in logs — shouldn't happen, but leave as submitted.
					w.log.Warn("op not found in receipt logs",
						zap.String("hash", op.Hash.Hex()),
						zap.String("tx", txHash.Hex()))
					continue
				}

				status := mempool.StatusConfirmed
				if !outcome.Success {
					status = mempool.StatusFailed
				}

				blockNum := receipt.BlockNumber.Uint64()
				extra := &mempool.StatusExtra{
					BlockNumber:   &blockNum,
					ActualGasCost: outcome.ActualGasCost,
					RevertReason:  outcome.RevertReason,
				}

				if err := w.repo.UpdateStatus(op.Hash, status, extra); err != nil {
					w.log.Error("failed to update op status",
						zap.String("hash", op.Hash.Hex()),
						zap.Error(err))
					continue
				}

				if status == mempool.StatusConfirmed {
					w.log.Info("op confirmed",
						zap.String("hash", op.Hash.Hex()),
						zap.String("tx", txHash.Hex()),
						zap.Uint64("block", blockNum),
						zap.String("gas_cost", outcome.ActualGasCost.String()))
				} else {
					reason := ""
					if outcome.RevertReason != nil {
						reason = *outcome.RevertReason
					}
					w.log.Warn("op failed on-chain",
						zap.String("hash", op.Hash.Hex()),
						zap.String("tx", txHash.Hex()),
						zap.Uint64("block", blockNum),
						zap.String("reason", reason))
				}
			}
		} else {
			// Bundle tx reverted — handle FailedOp or reset all.
			// Note: receipt.RevertReason may not be populated by all clients.
			// We use the revert data if available.
			var revertData []byte
			// go-ethereum doesn't expose revert reason directly on receipt in all versions.
			// For now, pass empty and let HandleBundleRevert reset all ops.
			HandleBundleRevert(revertData, ops, w.repo, w.log)
		}
	}

	return nil
}
