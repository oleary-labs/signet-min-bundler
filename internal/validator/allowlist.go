package validator

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

// Validator enforces policy checks on UserOperations.
// All ops must have an approved paymaster. Gas caps provide DoS protection.
type Validator struct {
	allowedPaymasters  map[common.Address]struct{}
	maxVerificationGas uint64
	maxCallGas         uint64
}

// New creates a Validator from config values.
func New(
	allowedPaymasters []common.Address,
	maxVerificationGas uint64,
	maxCallGas uint64,
) *Validator {
	paymasters := make(map[common.Address]struct{}, len(allowedPaymasters))
	for _, p := range allowedPaymasters {
		paymasters[p] = struct{}{}
	}
	return &Validator{
		allowedPaymasters:  paymasters,
		maxVerificationGas: maxVerificationGas,
		maxCallGas:         maxCallGas,
	}
}

// Validate runs the full validation sequence.
func (v *Validator) Validate(op *core.PackedUserOp) error {
	if err := v.checkGasLimits(op); err != nil {
		return err
	}
	if err := v.checkPaymaster(op); err != nil {
		return err
	}
	return nil
}

func (v *Validator) checkGasLimits(op *core.PackedUserOp) error {
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

func (v *Validator) checkPaymaster(op *core.PackedUserOp) error {
	if len(op.PaymasterAndData) < 20 {
		return fmt.Errorf("paymaster required")
	}
	pm := common.BytesToAddress(op.PaymasterAndData[:20])
	if _, ok := v.allowedPaymasters[pm]; ok {
		return nil
	}
	return fmt.Errorf("paymaster %s not in allowed list", pm.Hex())
}
