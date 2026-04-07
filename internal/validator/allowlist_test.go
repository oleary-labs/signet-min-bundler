package validator

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

var (
	usdc       = common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	usdt       = common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	paymaster1 = common.HexToAddress("0x1111111111111111111111111111111111111111")
	sender     = common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	forbidden  = common.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
)

func makeOp(target common.Address, verifGas, callGas uint64) *core.PackedUserOp {
	calldata := core.BuildExecuteCalldata(target, big.NewInt(0), []byte{})
	return &core.PackedUserOp{
		Sender:             sender,
		Nonce:              big.NewInt(0),
		CallData:           calldata,
		AccountGasLimits:   core.PackUint128s(verifGas, callGas),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		Signature:          make([]byte, 65),
	}
}

func defaultValidator() *AllowlistValidator {
	return New(
		[]common.Address{usdc, usdt},
		[]common.Address{paymaster1},
		50_000,
		500_000,
	)
}

func TestValidateAcceptsAllowedTarget(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 100000)
	if err := v.Validate(op); err != nil {
		t.Errorf("should accept allowed target: %v", err)
	}
}

func TestValidateRejectsForbiddenTarget(t *testing.T) {
	v := defaultValidator()
	op := makeOp(forbidden, 31000, 100000)
	if err := v.Validate(op); err == nil {
		t.Error("should reject forbidden target")
	}
}

func TestValidateAllowsSelfCall(t *testing.T) {
	v := defaultValidator()
	op := makeOp(sender, 31000, 100000) // target == sender
	if err := v.Validate(op); err != nil {
		t.Errorf("should allow self-call: %v", err)
	}
}

func TestValidateRejectsHighVerificationGas(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 60_000, 100_000) // exceeds max 50000
	if err := v.Validate(op); err == nil {
		t.Error("should reject high verificationGasLimit")
	}
}

func TestValidateRejectsHighCallGas(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 600_000) // exceeds max 500000
	if err := v.Validate(op); err == nil {
		t.Error("should reject high callGasLimit")
	}
}

func TestValidateAcceptsAtMaxGas(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 50_000, 500_000) // exactly at max
	if err := v.Validate(op); err != nil {
		t.Errorf("should accept gas at exact max: %v", err)
	}
}

func TestValidateAcceptsNoPaymaster(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 100000)
	op.PaymasterAndData = nil // no paymaster
	if err := v.Validate(op); err != nil {
		t.Errorf("should accept no paymaster: %v", err)
	}
}

func TestValidateAcceptsAllowedPaymaster(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 100000)
	op.PaymasterAndData = make([]byte, 22)
	copy(op.PaymasterAndData[:20], paymaster1[:])
	if err := v.Validate(op); err != nil {
		t.Errorf("should accept allowed paymaster: %v", err)
	}
}

func TestValidateRejectsForbiddenPaymaster(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 100000)
	op.PaymasterAndData = make([]byte, 20)
	copy(op.PaymasterAndData[:20], forbidden[:])
	if err := v.Validate(op); err == nil {
		t.Error("should reject forbidden paymaster")
	}
}

func TestValidateRejectsShortCalldata(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 100000)
	op.CallData = []byte{0x01, 0x02} // too short
	if err := v.Validate(op); err == nil {
		t.Error("should reject short calldata")
	}
}

func TestValidateRejectsWrongSelector(t *testing.T) {
	v := defaultValidator()
	op := makeOp(usdc, 31000, 100000)
	op.CallData[0] = 0xde // corrupt selector
	op.CallData[1] = 0xad
	if err := v.Validate(op); err == nil {
		t.Error("should reject wrong selector")
	}
}

func TestValidateEmptyAllowedTargets(t *testing.T) {
	v := New(nil, nil, 50_000, 500_000)
	op := makeOp(usdc, 31000, 100000)
	// No targets allowed — should reject (except self-calls).
	if err := v.Validate(op); err == nil {
		t.Error("should reject when no targets allowed")
	}
	// But self-call should still work.
	selfOp := makeOp(sender, 31000, 100000)
	if err := v.Validate(selfOp); err != nil {
		t.Errorf("self-call should always work: %v", err)
	}
}

func TestValidateEmptyAllowedPaymastersRejectsAny(t *testing.T) {
	v := New([]common.Address{usdc}, nil, 50_000, 500_000)
	op := makeOp(usdc, 31000, 100000)
	op.PaymasterAndData = make([]byte, 20)
	copy(op.PaymasterAndData[:20], paymaster1[:])
	if err := v.Validate(op); err == nil {
		t.Error("should reject paymaster when none are allowed")
	}
}
