package validator

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

var (
	paymaster1 = common.HexToAddress("0x1111111111111111111111111111111111111111")
	sender     = common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	forbidden  = common.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
)

func makeOp(verifGas, callGas uint64) *core.PackedUserOp {
	return &core.PackedUserOp{
		Sender:             sender,
		Nonce:              big.NewInt(0),
		CallData:           []byte{0x01, 0x02, 0x03, 0x04},
		AccountGasLimits:   core.PackUint128s(verifGas, callGas),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		Signature:          make([]byte, 65),
	}
}

func withPaymaster(op *core.PackedUserOp, pm common.Address) *core.PackedUserOp {
	op.PaymasterAndData = make([]byte, 22)
	copy(op.PaymasterAndData[:20], pm[:])
	return op
}

func defaultValidator() *Validator {
	return New(
		[]common.Address{paymaster1},
		50_000,
		500_000,
	)
}

func TestValidateAcceptsAllowedPaymaster(t *testing.T) {
	v := defaultValidator()
	op := withPaymaster(makeOp(31000, 100000), paymaster1)
	if err := v.Validate(op); err != nil {
		t.Errorf("should accept allowed paymaster: %v", err)
	}
}

func TestValidateRejectsNoPaymaster(t *testing.T) {
	v := defaultValidator()
	op := makeOp(31000, 100000)
	op.PaymasterAndData = nil
	if err := v.Validate(op); err == nil {
		t.Error("should reject op without paymaster")
	}
}

func TestValidateRejectsShortPaymasterAndData(t *testing.T) {
	v := defaultValidator()
	op := makeOp(31000, 100000)
	op.PaymasterAndData = []byte{0x01, 0x02} // too short to be an address
	if err := v.Validate(op); err == nil {
		t.Error("should reject op with short PaymasterAndData")
	}
}

func TestValidateRejectsForbiddenPaymaster(t *testing.T) {
	v := defaultValidator()
	op := withPaymaster(makeOp(31000, 100000), forbidden)
	if err := v.Validate(op); err == nil {
		t.Error("should reject forbidden paymaster")
	}
}

func TestValidateRejectsHighVerificationGas(t *testing.T) {
	v := defaultValidator()
	op := withPaymaster(makeOp(60_000, 100_000), paymaster1) // exceeds max 50000
	if err := v.Validate(op); err == nil {
		t.Error("should reject high verificationGasLimit")
	}
}

func TestValidateRejectsHighCallGas(t *testing.T) {
	v := defaultValidator()
	op := withPaymaster(makeOp(31000, 600_000), paymaster1) // exceeds max 500000
	if err := v.Validate(op); err == nil {
		t.Error("should reject high callGasLimit")
	}
}

func TestValidateAcceptsAtMaxGas(t *testing.T) {
	v := defaultValidator()
	op := withPaymaster(makeOp(50_000, 500_000), paymaster1) // exactly at max
	if err := v.Validate(op); err != nil {
		t.Errorf("should accept gas at exact max: %v", err)
	}
}
