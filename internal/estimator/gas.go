package estimator

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

// VerificationGasLimit is derived analytically from the Signet wallet architecture.
// See design spec §11.2 for the component breakdown.
// Must be validated empirically against a test fork before production.
const VerificationGasLimit = 31_000

// GasEstimate holds the three gas values returned by eth_estimateUserOperationGas.
type GasEstimate struct {
	PreVerificationGas   *big.Int
	VerificationGasLimit *big.Int
	CallGasLimit         *big.Int
}

// EthClient is the subset of ethclient.Client needed for gas estimation.
type EthClient interface {
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
}

// Estimator implements eth_estimateUserOperationGas without EVM simulation.
type Estimator struct {
	client EthClient
}

// New creates an Estimator.
func New(client EthClient) *Estimator {
	return &Estimator{client: client}
}

// Estimate computes gas values for a UserOperation.
func (e *Estimator) Estimate(ctx context.Context, op *core.PackedUserOp) (*GasEstimate, error) {
	pvg := CalcPreVerificationGas(op)

	callGas, err := e.estimateCallGas(ctx, op)
	if err != nil {
		return nil, fmt.Errorf("estimateCallGas: %w", err)
	}

	return &GasEstimate{
		PreVerificationGas:   pvg,
		VerificationGasLimit: big.NewInt(VerificationGasLimit),
		CallGasLimit:         callGas,
	}, nil
}

// CalcPreVerificationGas computes preVerificationGas statically from calldata costs.
// EIP-2028: 4 gas per zero byte, 16 gas per nonzero byte.
func CalcPreVerificationGas(op *core.PackedUserOp) *big.Int {
	packed := abiEncodePackedOp(op)
	var calldataCost uint64
	for _, b := range packed {
		if b == 0 {
			calldataCost += 4
		} else {
			calldataCost += 16
		}
	}
	// Fixed overhead: base tx share (21000) + EntryPoint loop per-op (11000)
	return new(big.Int).SetUint64(calldataCost + 21_000 + 11_000)
}

// estimateCallGas estimates the inner call gas via eth_estimateGas.
func (e *Estimator) estimateCallGas(ctx context.Context, op *core.PackedUserOp) (*big.Int, error) {
	target, value, innerData, err := core.DecodeSignetExecute(op.CallData)
	if err != nil {
		return nil, err
	}

	msg := ethereum.CallMsg{
		From:  op.Sender, // msg.sender for access control checks (fix R4)
		To:    &target,
		Value: value,
		Data:  innerData,
	}

	gas, err := e.client.EstimateGas(ctx, msg)
	if err != nil {
		return nil, err
	}
	// 30% buffer for Kernel dispatch wrapper overhead
	return new(big.Int).SetUint64(gas * 130 / 100), nil
}

// abiEncodePackedOp encodes the PackedUserOp as it appears in handleOps calldata
// for preVerificationGas calculation.
func abiEncodePackedOp(op *core.PackedUserOp) []byte {
	// Approximate the ABI encoding: sender(32) + nonce(32) + initCode hash(32) +
	// callData hash(32) + accountGasLimits(32) + preVerificationGas(32) +
	// gasFees(32) + paymasterAndData hash(32) + signature length prefix(32) + signature
	//
	// For calldata cost, we use the actual bytes rather than hashes since
	// the raw fields appear in the handleOps calldata.
	var buf []byte
	buf = append(buf, core.PadLeft32(op.Sender.Bytes())...)
	buf = append(buf, core.PadLeft32(op.Nonce.Bytes())...)
	buf = append(buf, op.InitCode...)
	buf = append(buf, op.CallData...)
	buf = append(buf, op.AccountGasLimits[:]...)
	buf = append(buf, core.PadLeft32(op.PreVerificationGas.Bytes())...)
	buf = append(buf, op.GasFees[:]...)
	buf = append(buf, op.PaymasterAndData...)
	buf = append(buf, op.Signature...)
	return buf
}
