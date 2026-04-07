package rpc

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
	"github.com/oleary-labs/signet-min-bundler/internal/estimator"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"github.com/oleary-labs/signet-min-bundler/internal/validator"
	"go.uber.org/zap"
)

// Methods holds the dependencies for JSON-RPC method handlers.
type Methods struct {
	cfg       MethodsConfig
	validator *validator.AllowlistValidator
	repo      mempool.Repository
	estimator *estimator.Estimator
	log       *zap.Logger
}

// MethodsConfig holds the subset of config needed by RPC methods.
type MethodsConfig struct {
	EntryPoints []common.Address
	ChainID     uint64
}

// NewMethods creates a Methods handler.
func NewMethods(
	cfg MethodsConfig,
	v *validator.AllowlistValidator,
	repo mempool.Repository,
	est *estimator.Estimator,
	log *zap.Logger,
) *Methods {
	return &Methods{
		cfg:       cfg,
		validator: v,
		repo:      repo,
		estimator: est,
		log:       log,
	}
}

func (m *Methods) handleSendUserOperation(params []json.RawMessage) (any, *RpcError) {
	if len(params) < 2 {
		return nil, ErrInvalidParams("expected [userOp, entryPoint]")
	}

	var op core.UserOperationRPC
	if err := json.Unmarshal(params[0], &op); err != nil {
		return nil, ErrInvalidParams("invalid userOp: " + err.Error())
	}

	var entryPointHex string
	if err := json.Unmarshal(params[1], &entryPointHex); err != nil {
		return nil, ErrInvalidParams("invalid entryPoint: " + err.Error())
	}

	// 1. Validate entry point.
	entryPoint := common.HexToAddress(entryPointHex)
	if !m.isAllowedEntryPoint(entryPoint) {
		return nil, ErrOpRejected("unsupported entry point")
	}

	// 2. Convert wire format to packed struct.
	packed, err := core.FromRPC(op)
	if err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// 3. Validate signature length.
	if len(packed.Signature) != 65 {
		return nil, ErrOpRejected("signature must be 65 bytes")
	}

	// 4. Allowlist validation.
	if err := m.validator.Validate(packed); err != nil {
		return nil, ErrOpRejected(err.Error())
	}

	// 5. Compute hash.
	hash := core.ComputeUserOpHash(packed, entryPoint, m.cfg.ChainID)

	// 6. Insert into mempool.
	if err := m.repo.Insert(packed, hash); err != nil {
		return nil, ErrOpRejected(err.Error())
	}

	m.log.Info("op received",
		zap.String("hash", hash.Hex()),
		zap.String("sender", packed.Sender.Hex()))

	return hash.Hex(), nil
}

func (m *Methods) handleEstimateUserOperationGas(ctx context.Context, params []json.RawMessage) (any, *RpcError) {
	if len(params) < 2 {
		return nil, ErrInvalidParams("expected [userOp, entryPoint]")
	}

	var op core.UserOperationRPC
	if err := json.Unmarshal(params[0], &op); err != nil {
		return nil, ErrInvalidParams("invalid userOp: " + err.Error())
	}

	var entryPointHex string
	if err := json.Unmarshal(params[1], &entryPointHex); err != nil {
		return nil, ErrInvalidParams("invalid entryPoint: " + err.Error())
	}

	entryPoint := common.HexToAddress(entryPointHex)
	if !m.isAllowedEntryPoint(entryPoint) {
		return nil, ErrOpRejected("unsupported entry point")
	}

	packed, err := core.FromRPC(op)
	if err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	est, err := m.estimator.Estimate(ctx, packed)
	if err != nil {
		return nil, ErrInvalidParams("gas estimation failed: " + err.Error())
	}

	return map[string]string{
		"preVerificationGas":   core.BigToHex(est.PreVerificationGas),
		"verificationGasLimit": core.BigToHex(est.VerificationGasLimit),
		"callGasLimit":         core.BigToHex(est.CallGasLimit),
	}, nil
}

func (m *Methods) handleGetUserOperationByHash(params []json.RawMessage) (any, *RpcError) {
	if len(params) < 1 {
		return nil, ErrInvalidParams("expected [hash]")
	}

	var hashHex string
	if err := json.Unmarshal(params[0], &hashHex); err != nil {
		return nil, ErrInvalidParams("invalid hash: " + err.Error())
	}

	hash := common.HexToHash(hashHex)
	op, err := m.repo.GetByHash(hash)
	if err != nil {
		return nil, nil // not found → null
	}

	return storedOpToRPC(op), nil
}

func (m *Methods) handleGetUserOperationReceipt(params []json.RawMessage) (any, *RpcError) {
	if len(params) < 1 {
		return nil, ErrInvalidParams("expected [hash]")
	}

	var hashHex string
	if err := json.Unmarshal(params[0], &hashHex); err != nil {
		return nil, ErrInvalidParams("invalid hash: " + err.Error())
	}

	hash := common.HexToHash(hashHex)
	op, err := m.repo.GetConfirmedByHash(hash)
	if err != nil || op == nil {
		return nil, nil // not confirmed → null
	}

	receipt := map[string]any{
		"userOpHash":    op.Hash.Hex(),
		"sender":        op.Sender.Hex(),
		"nonce":         core.BigToHex(op.Nonce),
		"success":       op.Status == mempool.StatusConfirmed,
	}
	if op.ActualGasCost != nil {
		receipt["actualGasCost"] = core.BigToHex(op.ActualGasCost)
	}
	if op.BundleTxHash != nil {
		receipt["transactionHash"] = op.BundleTxHash.Hex()
	}
	if op.BlockNumber != nil {
		receipt["blockNumber"] = core.BigToHex(new(big.Int).SetUint64(*op.BlockNumber))
	}
	if op.RevertReason != nil {
		receipt["revertReason"] = *op.RevertReason
	}

	return receipt, nil
}

func (m *Methods) handleSupportedEntryPoints() (any, *RpcError) {
	eps := make([]string, len(m.cfg.EntryPoints))
	for i, ep := range m.cfg.EntryPoints {
		eps[i] = ep.Hex()
	}
	return eps, nil
}

func (m *Methods) handleChainId() (any, *RpcError) {
	return core.BigToHex(new(big.Int).SetUint64(m.cfg.ChainID)), nil
}

func (m *Methods) isAllowedEntryPoint(addr common.Address) bool {
	for _, ep := range m.cfg.EntryPoints {
		if ep == addr {
			return true
		}
	}
	return false
}

// storedOpToRPC converts a StoredOp back to the wire format with metadata.
func storedOpToRPC(op *mempool.StoredOp) map[string]any {
	verifGas := new(big.Int).SetBytes(op.AccountGasLimits[0:16])
	callGas := new(big.Int).SetBytes(op.AccountGasLimits[16:32])
	maxPrio := new(big.Int).SetBytes(op.GasFees[0:16])
	maxFee := new(big.Int).SetBytes(op.GasFees[16:32])

	result := map[string]any{
		"sender":               op.Sender.Hex(),
		"nonce":                core.BigToHex(op.Nonce),
		"callData":             core.BytesToHex(op.CallData),
		"callGasLimit":         core.BigToHex(callGas),
		"verificationGasLimit": core.BigToHex(verifGas),
		"preVerificationGas":   core.BigToHex(op.PreVerificationGas),
		"maxFeePerGas":         core.BigToHex(maxFee),
		"maxPriorityFeePerGas": core.BigToHex(maxPrio),
		"signature":            core.BytesToHex(op.Signature),
	}

	if len(op.InitCode) >= 20 {
		result["factory"] = common.BytesToAddress(op.InitCode[:20]).Hex()
		result["factoryData"] = core.BytesToHex(op.InitCode[20:])
	}
	if len(op.PaymasterAndData) >= 20 {
		result["paymaster"] = common.BytesToAddress(op.PaymasterAndData[:20]).Hex()
		result["paymasterData"] = core.BytesToHex(op.PaymasterAndData[20:])
	}

	// Metadata
	if op.BundleTxHash != nil {
		result["transactionHash"] = op.BundleTxHash.Hex()
	}
	if op.BlockNumber != nil {
		result["blockNumber"] = core.BigToHex(new(big.Int).SetUint64(*op.BlockNumber))
	}

	return result
}
