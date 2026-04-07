package bundler

import (
	"bytes"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"go.uber.org/zap"
)

// EntryPoint v0.7 event topic signatures.
var (
	// UserOperationEvent(bytes32 indexed userOpHash, address indexed sender,
	//   address indexed paymaster, uint256 nonce, bool success,
	//   uint256 actualGasCost, uint256 actualGasUsed)
	userOperationEventTopic = common.BytesToHash(core.Keccak256(
		[]byte("UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)"),
	))

	// UserOperationRevertReason(bytes32 indexed userOpHash, address indexed sender,
	//   uint256 nonce, bytes revertReason)
	userOperationRevertReasonTopic = common.BytesToHash(core.Keccak256(
		[]byte("UserOperationRevertReason(bytes32,address,uint256,bytes)"),
	))

	// FailedOp(uint256 opIndex, string reason)
	failedOpSelector = core.Keccak256([]byte("FailedOp(uint256,string)"))[:4]
)

// OpOutcome holds the per-op result parsed from receipt logs.
type OpOutcome struct {
	Success       bool
	ActualGasCost *big.Int
	RevertReason  *string
}

// ParseHandleOpsReceipt parses UserOperationEvent and UserOperationRevertReason
// logs from a handleOps transaction receipt.
//
// Fix R1: UserOperationEvent has 3 indexed params (userOpHash, sender, paymaster)
// in Topics[1..3]. The remaining fields (nonce, success, actualGasCost,
// actualGasUsed) are ABI-encoded in log.Data.
//
// Fix R2: UserOperationRevertReason data is safely sliced with length checks.
func ParseHandleOpsReceipt(receipt *types.Receipt, entryPoint common.Address) map[common.Hash]*OpOutcome {
	outcomes := map[common.Hash]*OpOutcome{}

	for _, log := range receipt.Logs {
		if log.Address != entryPoint {
			continue
		}
		if len(log.Topics) == 0 {
			continue
		}

		switch log.Topics[0] {
		case userOperationEventTopic:
			if len(log.Topics) < 2 {
				continue
			}
			hash := log.Topics[1]

			// log.Data layout (ABI-encoded, non-indexed params):
			// [0:32]   uint256 nonce
			// [32:64]  bool success (uint256, 0 or 1)
			// [64:96]  uint256 actualGasCost
			// [96:128] uint256 actualGasUsed
			if len(log.Data) < 96 {
				continue
			}

			success := log.Data[63] == 1 // last byte of the bool word
			gasCost := new(big.Int).SetBytes(log.Data[64:96])

			outcomes[hash] = &OpOutcome{
				Success:       success,
				ActualGasCost: gasCost,
			}

		case userOperationRevertReasonTopic:
			if len(log.Topics) < 2 {
				continue
			}
			hash := log.Topics[1]

			// log.Data layout (ABI-encoded):
			// [0:32]   uint256 nonce
			// [32:64]  offset to bytes revertReason
			// [64:96]  length of revertReason bytes
			// [96+]    revertReason bytes
			if len(log.Data) < 96 {
				continue
			}
			reasonLen := new(big.Int).SetBytes(log.Data[64:96]).Int64()
			reasonEnd := 96 + int(reasonLen)
			if reasonEnd > len(log.Data) {
				reasonEnd = len(log.Data)
			}
			reason := string(log.Data[96:reasonEnd])

			if o, ok := outcomes[hash]; ok {
				o.RevertReason = &reason
			} else {
				outcomes[hash] = &OpOutcome{RevertReason: &reason}
			}
		}
	}

	return outcomes
}

// HandleBundleRevert processes a reverted bundle transaction.
// If the revert is a FailedOp, the offending op is marked failed and others reset.
// Otherwise all ops are reset to pending.
func HandleBundleRevert(revertData []byte, ops []*mempool.StoredOp, repo mempool.Repository, log *zap.Logger) {
	if len(revertData) >= 4 && bytes.Equal(revertData[:4], failedOpSelector) {
		opIndex, reason := DecodeFailedOp(revertData)
		log.Error("bundle reverted with FailedOp",
			zap.Int("failed_index", opIndex),
			zap.String("reason", reason),
			zap.Int("reset_count", len(ops)-1))

		for _, op := range ops {
			if op.BundleIndex != nil && *op.BundleIndex == opIndex {
				repo.UpdateStatus(op.Hash, mempool.StatusFailed,
					&mempool.StatusExtra{RevertReason: &reason})
			} else {
				repo.UpdateStatus(op.Hash, mempool.StatusPending, nil)
			}
		}
	} else {
		log.Error("bundle reverted unexpectedly",
			zap.String("revert_data", core.BytesToHex(revertData)))
		for _, op := range ops {
			repo.UpdateStatus(op.Hash, mempool.StatusPending, nil)
		}
	}
}

// DecodeFailedOp decodes FailedOp(uint256 opIndex, string reason) revert data.
func DecodeFailedOp(data []byte) (int, string) {
	if len(data) < 4+32 {
		return 0, "unknown"
	}
	opIndex := new(big.Int).SetBytes(data[4 : 4+32]).Int64()

	if len(data) < 4+32+32 {
		return int(opIndex), "unknown"
	}

	// String is ABI-encoded: offset(32) + length(32) + data
	// offset is at data[4+32:4+64], points relative to start of params
	offset := new(big.Int).SetBytes(data[4+32 : 4+64]).Int64()
	strStart := 4 + int(offset)

	if len(data) < strStart+32 {
		return int(opIndex), "unknown"
	}
	strLen := new(big.Int).SetBytes(data[strStart : strStart+32]).Int64()
	strDataStart := strStart + 32
	strDataEnd := strDataStart + int(strLen)
	if strDataEnd > len(data) {
		strDataEnd = len(data)
	}

	return int(opIndex), string(data[strDataStart:strDataEnd])
}
