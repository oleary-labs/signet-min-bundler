package bundler

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"go.uber.org/zap"
)

// Reconcile ensures no ops are left in intermediate states from a previous process run.
// Must be called on every startup, before the bundling loop begins.
func Reconcile(ctx context.Context, repo mempool.Repository, client BundleClient, entryPoint common.Address, log *zap.Logger) error {
	// Step 1: Reset all 'bundling' ops to 'pending'.
	// These were claimed but never submitted — process died in the gap.
	n, err := repo.ResetBundlingToPending()
	if err != nil {
		return err
	}
	if n > 0 {
		log.Warn("reconciled stuck ops", zap.Int("bundling_reset", n))
	}

	// Step 2: Check all 'submitted' ops against the node.
	submitted, err := repo.GetByStatus(mempool.StatusSubmitted)
	if err != nil {
		return err
	}
	if len(submitted) == 0 {
		log.Info("reconciliation complete", zap.Int("bundling_reset", n), zap.Int("submitted_checked", 0))
		return nil
	}

	// Group by bundle tx hash.
	byTx := map[common.Hash][]*mempool.StoredOp{}
	for _, op := range submitted {
		if op.BundleTxHash == nil {
			continue
		}
		byTx[*op.BundleTxHash] = append(byTx[*op.BundleTxHash], op)
	}

	checked := 0
	for txHash, ops := range byTx {
		receipt, err := client.TransactionReceipt(ctx, txHash)
		if err != nil {
			if !isNotFound(err) {
				log.Warn("failed to fetch receipt during reconciliation",
					zap.String("tx", txHash.Hex()),
					zap.Error(err))
			}
			continue
		}
		checked++

		if receipt.Status == 1 {
			// Bundle landed — parse logs for per-op outcomes.
			outcomes := ParseHandleOpsReceipt(receipt, entryPoint)
			for _, op := range ops {
				outcome, ok := outcomes[op.Hash]
				if !ok {
					continue
				}
				status := mempool.StatusConfirmed
				if !outcome.Success {
					status = mempool.StatusFailed
				}
				blockNum := receipt.BlockNumber.Uint64()
				repo.UpdateStatus(op.Hash, status, &mempool.StatusExtra{
					BlockNumber:   &blockNum,
					RevertReason:  outcome.RevertReason,
					ActualGasCost: outcome.ActualGasCost,
				})
			}
		} else {
			// Bundle tx reverted.
			HandleBundleRevert(nil, ops, repo, log)
		}
	}

	log.Info("reconciliation complete",
		zap.Int("bundling_reset", n),
		zap.Int("submitted_checked", checked))
	return nil
}
