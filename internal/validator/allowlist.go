package validator

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

// AllowlistValidator enforces static policy checks on UserOperations.
// All checks are synchronous and stateless — no I/O.
type AllowlistValidator struct {
	allowedTargets     map[common.Address]struct{}
	allowedPaymasters  map[common.Address]struct{}
	maxVerificationGas uint64
	maxCallGas         uint64
}

// New creates an AllowlistValidator from config values.
func New(
	allowedTargets []common.Address,
	allowedPaymasters []common.Address,
	maxVerificationGas uint64,
	maxCallGas uint64,
) *AllowlistValidator {
	targets := make(map[common.Address]struct{}, len(allowedTargets))
	for _, t := range allowedTargets {
		targets[t] = struct{}{}
	}
	paymasters := make(map[common.Address]struct{}, len(allowedPaymasters))
	for _, p := range allowedPaymasters {
		paymasters[p] = struct{}{}
	}
	return &AllowlistValidator{
		allowedTargets:     targets,
		allowedPaymasters:  paymasters,
		maxVerificationGas: maxVerificationGas,
		maxCallGas:         maxCallGas,
	}
}

// Validate runs the full allowlist validation sequence.
func (v *AllowlistValidator) Validate(op *core.PackedUserOp) error {
	if err := v.checkGasLimits(op); err != nil {
		return err
	}
	if err := v.checkPaymaster(op); err != nil {
		return err
	}
	if err := v.checkTargets(op); err != nil {
		return err
	}
	return nil
}

func (v *AllowlistValidator) checkGasLimits(op *core.PackedUserOp) error {
	verifGas := new(big.Int).SetBytes(op.AccountGasLimits[0:16])
	callGas := new(big.Int).SetBytes(op.AccountGasLimits[16:32])

	if verifGas.Uint64() > v.maxVerificationGas {
		return fmt.Errorf("verificationGasLimit %d exceeds max %d",
			verifGas.Uint64(), v.maxVerificationGas)
	}
	if callGas.Uint64() > v.maxCallGas {
		return fmt.Errorf("callGasLimit %d exceeds max %d",
			callGas.Uint64(), v.maxCallGas)
	}
	return nil
}

func (v *AllowlistValidator) checkPaymaster(op *core.PackedUserOp) error {
	if len(op.PaymasterAndData) < 20 {
		return nil // no paymaster — always accepted
	}
	pm := common.BytesToAddress(op.PaymasterAndData[:20])
	if _, ok := v.allowedPaymasters[pm]; ok {
		return nil
	}
	return fmt.Errorf("paymaster %s not in allowed list", pm.Hex())
}

// execute(address,uint256,bytes) selector
var executeSelector = core.Keccak256([]byte("execute(address,uint256,bytes)"))[:4]

func (v *AllowlistValidator) checkTargets(op *core.PackedUserOp) error {
	if len(op.CallData) < 4+32 {
		return fmt.Errorf("callData too short")
	}
	if !bytes.Equal(op.CallData[:4], executeSelector) {
		return fmt.Errorf("unrecognised selector %x", op.CallData[:4])
	}

	// address is the last 20 bytes of the first 32-byte ABI word
	target := common.BytesToAddress(op.CallData[4+12 : 4+32])

	// Self-calls always allowed (key rotation, validator management)
	if target == op.Sender {
		return nil
	}

	if _, ok := v.allowedTargets[target]; ok {
		return nil
	}
	return fmt.Errorf("target %s not in allowed list", target.Hex())
}
