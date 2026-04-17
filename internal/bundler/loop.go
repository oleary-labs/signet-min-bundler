package bundler

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"go.uber.org/zap"
)

// handleOps(PackedUserOperation[],address) selector
var handleOpsSelector = core.Keccak256([]byte("handleOps((address,uint256,bytes,bytes,bytes32,uint256,bytes32,bytes,bytes)[],address)"))[:4]

// BundleClient is the subset of ethclient.Client needed by the bundling loop.
type BundleClient interface {
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
}

// Signer abstracts the bundle signer for testability.
type Signer interface {
	Address() common.Address
	CheckBalance(ctx context.Context) error
	SignAndSubmit(ctx context.Context, to common.Address, data []byte, gasLimit uint64) (common.Hash, error)
}

// BundlingLoop periodically claims pending ops and submits them as handleOps bundles.
type BundlingLoop struct {
	repo       mempool.Repository
	signer     Signer
	client     BundleClient
	entryPoint common.Address
	maxBundle  int
	tick       time.Duration
	log        *zap.Logger
}

// NewLoop creates a BundlingLoop.
func NewLoop(
	repo mempool.Repository,
	signer Signer,
	client BundleClient,
	entryPoint common.Address,
	maxBundleSize int,
	tickInterval time.Duration,
	log *zap.Logger,
) *BundlingLoop {
	return &BundlingLoop{
		repo:       repo,
		signer:     signer,
		client:     client,
		entryPoint: entryPoint,
		maxBundle:  maxBundleSize,
		tick:       tickInterval,
		log:        log,
	}
}

// Run starts the bundling loop, ticking at the configured interval.
func (l *BundlingLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.Tick(ctx); err != nil {
				l.log.Error("bundling tick failed", zap.String("error", err.Error()))
			}
		}
	}
}

// Tick executes a single bundling cycle. Exported for testing.
func (l *BundlingLoop) Tick(ctx context.Context) error {
	// 1. Check signer balance before doing any work.
	if err := l.signer.CheckBalance(ctx); err != nil {
		return err
	}

	// 2. Fetch up to maxBundleSize pending ops (FIFO by received_at).
	ops, err := l.repo.GetPending(l.maxBundle)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}
	if len(ops) == 0 {
		return nil
	}

	// 3. Claim ops atomically.
	hashes := make([]common.Hash, len(ops))
	for i, op := range ops {
		hashes[i] = op.Hash
	}
	if err := l.repo.ClaimForBundling(hashes); err != nil {
		return fmt.Errorf("claim for bundling: %w", err)
	}

	// 4. Build handleOps calldata.
	opHashes := make([]string, len(ops))
	for i, op := range ops {
		opHashes[i] = op.Hash.Hex()[:10]
	}
	l.log.Debug("building bundle",
		zap.Strings("ops", opHashes),
		zap.Int("count", len(ops)))

	calldata := BuildHandleOpsCalldata(ops, l.signer.Address())

	// 5. Estimate gas for the bundle tx.
	callMsg := ethereum.CallMsg{
		From: l.signer.Address(),
		To:   &l.entryPoint,
		Data: calldata,
	}
	gasLimit, err := l.client.EstimateGas(ctx, callMsg)
	if err != nil {
		revertReason := l.extractRevertReason(ctx, callMsg)

		if isEntryPointRevert(err, revertReason) {
			// EntryPoint FailedOp errors (AA13, AA21, AA24, etc.) are permanent —
			// the op is structurally invalid and retrying won't help.
			l.failOps(ops, revertReason)
			l.log.Error("ops failed permanently",
				zap.String("reason", revertReason),
				zap.Int("count", len(ops)))
		} else {
			// Network errors, RPC timeouts, etc. are transient — retry later.
			l.resetToPending(ops, "estimate gas failed")
		}

		// Log calldata for debugging with cast:
		//   cast call <entrypoint> <calldata> --from <bundler> --rpc-url http://localhost:8545 --trace
		l.log.Error("handleOps failed, debug with cast call",
			zap.String("entrypoint", l.entryPoint.Hex()),
			zap.String("from", l.signer.Address().Hex()),
			zap.String("calldata", "0x"+hex.EncodeToString(calldata)))

		return fmt.Errorf("estimate gas: %w", err)
	}
	gasLimit = gasLimit * 12 / 10 // 20% buffer

	// 6. Submit.
	txHash, err := l.signer.SignAndSubmit(ctx, l.entryPoint, calldata, gasLimit)
	if err != nil {
		// Fix R5: reset ops to pending on submission failure.
		l.resetToPending(ops, "submit failed")
		return fmt.Errorf("submit bundle: %w", err)
	}

	// 7. Update status for all ops in the bundle.
	now := time.Now().UnixMilli()
	for i, op := range ops {
		idx := i
		l.repo.UpdateStatus(op.Hash, mempool.StatusSubmitted, &mempool.StatusExtra{
			BundleTxHash: &txHash,
			BundleIndex:  &idx,
			SubmittedAt:  &now,
		})
	}

	l.log.Info("bundle submitted",
		zap.String("tx", txHash.Hex()),
		zap.Int("op_count", len(ops)))

	return nil
}

// resetToPending resets all ops back to pending after a failed submission attempt (fix R5).
func (l *BundlingLoop) resetToPending(ops []*mempool.StoredOp, reason string) {
	for _, op := range ops {
		if err := l.repo.UpdateStatus(op.Hash, mempool.StatusPending, nil); err != nil {
			l.log.Error("failed to reset op to pending",
				zap.String("hash", op.Hash.Hex()),
				zap.Error(err))
		}
	}
	l.log.Warn("reset ops to pending after failure",
		zap.String("reason", reason),
		zap.Int("count", len(ops)))
}

// failOps marks all ops as permanently failed with the given reason.
func (l *BundlingLoop) failOps(ops []*mempool.StoredOp, reason string) {
	for _, op := range ops {
		if err := l.repo.UpdateStatus(op.Hash, mempool.StatusFailed, &mempool.StatusExtra{
			RevertReason: &reason,
		}); err != nil {
			l.log.Error("failed to mark op as failed",
				zap.String("hash", op.Hash.Hex()),
				zap.Error(err))
		}
	}
}

// extractRevertReason does an eth_call to get the revert reason string.
// Returns the error message from the revert, or a generic message if extraction fails.
func (l *BundlingLoop) extractRevertReason(ctx context.Context, msg ethereum.CallMsg) string {
	_, err := l.client.CallContract(ctx, msg, nil)
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// isEntryPointRevert returns true if the error indicates a permanent EntryPoint
// rejection (FailedOp, FailedOpWithRevert) rather than a transient failure.
func isEntryPointRevert(estimateErr error, revertReason string) bool {
	// EntryPoint FailedOp errors contain "AA" error codes (AA10-AA41).
	// These are permanent: the op itself is invalid.
	if strings.Contains(revertReason, "FailedOp") ||
		strings.Contains(revertReason, "FailedOpWithRevert") {
		return true
	}

	// go-ethereum surfaces the custom error selector in "execution reverted: custom error 0x..."
	// FailedOp = 0x220266b6, FailedOpWithRevert = 0x6fde1f52
	msg := estimateErr.Error()
	if strings.Contains(msg, "0x220266b6") || strings.Contains(msg, "0x6fde1f52") {
		return true
	}

	// "AA" prefixed reasons from decoded revert data.
	if strings.Contains(revertReason, " AA") {
		return true
	}

	return false
}

// BuildHandleOpsCalldata ABI-encodes handleOps(PackedUserOperation[], address beneficiary).
//
// ABI layout:
//
//	[0:4]     selector
//	[4:36]    offset to dynamic array = 64
//	[36:68]   beneficiary address
//	[68:100]  array length
//	[100+]    array elements (each is an ABI-encoded PackedUserOperation tuple)
func BuildHandleOpsCalldata(ops []*mempool.StoredOp, beneficiary common.Address) []byte {
	// Each PackedUserOperation has 9 fields. The struct contains both static
	// and dynamic fields. We encode per the ABI spec for a tuple[].
	//
	// For the handleOps call, the two params are:
	// 1. PackedUserOperation[] (dynamic)
	// 2. address beneficiary (static)
	//
	// Top-level ABI encoding:
	// [0:32]  offset to param 1 (array) = 64 (0x40)
	// [32:64] beneficiary

	var buf []byte
	buf = append(buf, handleOpsSelector...)

	// Offset to the array data (relative to start of params = after selector).
	buf = append(buf, core.PadLeft32(big.NewInt(64).Bytes())...) // offset = 64
	buf = append(buf, core.PadLeft32(beneficiary.Bytes())...)     // beneficiary

	// Array encoding: length + elements.
	// Each element is a tuple with dynamic fields, so we need offset-based encoding.
	arrayLen := len(ops)
	buf = append(buf, core.PadLeft32(big.NewInt(int64(arrayLen)).Bytes())...)

	// First pass: encode each op tuple and collect offsets.
	encodedOps := make([][]byte, arrayLen)
	for i, op := range ops {
		encodedOps[i] = encodePackedUserOp(&op.PackedUserOp)
	}

	// The array of tuples uses offset-based encoding since tuples contain dynamic fields.
	// offsets[i] = distance from start of array elements to element i's data.
	offsetBase := arrayLen * 32 // space for offset words
	currentOffset := offsetBase
	for i := range encodedOps {
		buf = append(buf, core.PadLeft32(big.NewInt(int64(currentOffset)).Bytes())...)
		currentOffset += len(encodedOps[i])
	}

	// Then append all encoded tuples.
	for _, enc := range encodedOps {
		buf = append(buf, enc...)
	}

	return buf
}

// encodePackedUserOp ABI-encodes a single PackedUserOperation tuple.
//
// PackedUserOperation fields:
//
//	address sender             (static)
//	uint256 nonce              (static)
//	bytes   initCode           (dynamic)
//	bytes   callData           (dynamic)
//	bytes32 accountGasLimits   (static)
//	uint256 preVerificationGas (static)
//	bytes32 gasFees            (static)
//	bytes   paymasterAndData   (dynamic)
//	bytes   signature          (dynamic)
func encodePackedUserOp(op *core.PackedUserOp) []byte {
	// Head: 9 x 32 bytes for field values or offsets.
	// For dynamic fields (initCode, callData, paymasterAndData, signature),
	// the head contains offsets. Static fields contain values directly.
	headSize := 9 * 32

	// Encode dynamic fields.
	initCodeEnc := encodeBytes(op.InitCode)
	callDataEnc := encodeBytes(op.CallData)
	pmDataEnc := encodeBytes(op.PaymasterAndData)
	sigEnc := encodeBytes(op.Signature)

	totalSize := headSize + len(initCodeEnc) + len(callDataEnc) + len(pmDataEnc) + len(sigEnc)
	buf := make([]byte, totalSize)

	// Static fields.
	copy(buf[12:32], op.Sender[:])                              // address sender
	copy(buf[32:64], core.PadLeft32(op.Nonce.Bytes()))          // uint256 nonce
	// Slots 2,3 are offsets (filled below)
	copy(buf[4*32:5*32], op.AccountGasLimits[:])                // bytes32 accountGasLimits
	copy(buf[5*32:6*32], core.PadLeft32(op.PreVerificationGas.Bytes())) // uint256 preVerificationGas
	copy(buf[6*32:7*32], op.GasFees[:])                         // bytes32 gasFees
	// Slots 7,8 are offsets (filled below)

	// Dynamic field offsets (relative to start of this tuple).
	tailOffset := headSize
	copy(buf[2*32:3*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes())) // initCode offset
	tailOffset += len(initCodeEnc)
	copy(buf[3*32:4*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes())) // callData offset
	tailOffset += len(callDataEnc)
	copy(buf[7*32:8*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes())) // paymasterAndData offset
	tailOffset += len(pmDataEnc)
	copy(buf[8*32:9*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes())) // signature offset

	// Append dynamic data.
	pos := headSize
	copy(buf[pos:], initCodeEnc)
	pos += len(initCodeEnc)
	copy(buf[pos:], callDataEnc)
	pos += len(callDataEnc)
	copy(buf[pos:], pmDataEnc)
	pos += len(pmDataEnc)
	copy(buf[pos:], sigEnc)

	return buf
}

// encodeBytes ABI-encodes a bytes value: length(32) + data(padded to 32).
func encodeBytes(data []byte) []byte {
	paddedLen := ((len(data) + 31) / 32) * 32
	buf := make([]byte, 32+paddedLen)
	copy(buf[0:32], core.PadLeft32(big.NewInt(int64(len(data))).Bytes()))
	copy(buf[32:], data)
	return buf
}
