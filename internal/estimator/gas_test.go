package estimator

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

type mockClient struct {
	gasEstimate uint64
	err         error
}

func (m *mockClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	if m.err != nil {
		return 0, m.err
	}
	return m.gasEstimate, nil
}

func testOp() *core.PackedUserOp {
	target := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	calldata := core.BuildExecuteCalldata(target, big.NewInt(0), []byte{0xaa, 0xbb})
	return &core.PackedUserOp{
		Sender:             common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Nonce:              big.NewInt(0),
		InitCode:           nil,
		CallData:           calldata,
		AccountGasLimits:   core.PackUint128s(31000, 50000),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		PaymasterAndData:   nil,
		Signature:          make([]byte, 65),
	}
}

func TestCalcPreVerificationGas(t *testing.T) {
	op := testOp()
	pvg := CalcPreVerificationGas(op)

	// Must include base overhead (21000 + 11000 = 32000)
	if pvg.Uint64() < 32_000 {
		t.Errorf("preVerificationGas %d is less than fixed overhead 32000", pvg.Uint64())
	}

	// Should be deterministic
	pvg2 := CalcPreVerificationGas(op)
	if pvg.Cmp(pvg2) != 0 {
		t.Error("preVerificationGas is not deterministic")
	}
}

func TestCalcPreVerificationGasZeroBytesAreCheaper(t *testing.T) {
	// Op with mostly zero bytes should be cheaper than one with nonzero bytes
	opZero := testOp()
	opZero.CallData = make([]byte, 100) // all zeros
	copy(opZero.CallData[:4], core.Keccak256([]byte("execute(address,uint256,bytes)"))[:4])

	opNonzero := testOp()
	opNonzero.CallData = make([]byte, 100)
	copy(opNonzero.CallData[:4], core.Keccak256([]byte("execute(address,uint256,bytes)"))[:4])
	for i := 4; i < 100; i++ {
		opNonzero.CallData[i] = 0xff
	}

	pvgZero := CalcPreVerificationGas(opZero)
	pvgNonzero := CalcPreVerificationGas(opNonzero)

	if pvgZero.Cmp(pvgNonzero) >= 0 {
		t.Errorf("zero-byte op (%d) should be cheaper than nonzero (%d)", pvgZero.Uint64(), pvgNonzero.Uint64())
	}
}

func TestEstimate(t *testing.T) {
	client := &mockClient{gasEstimate: 50_000}
	est := New(client)
	op := testOp()

	result, err := est.Estimate(context.Background(), op)
	if err != nil {
		t.Fatalf("Estimate: %v", err)
	}

	if result.VerificationGasLimit.Int64() != VerificationGasLimit {
		t.Errorf("verificationGasLimit = %d, want %d", result.VerificationGasLimit.Int64(), VerificationGasLimit)
	}

	// callGasLimit should be 50000 * 130 / 100 = 65000
	expectedCallGas := uint64(50_000 * 130 / 100)
	if result.CallGasLimit.Uint64() != expectedCallGas {
		t.Errorf("callGasLimit = %d, want %d", result.CallGasLimit.Uint64(), expectedCallGas)
	}

	if result.PreVerificationGas.Uint64() < 32_000 {
		t.Errorf("preVerificationGas = %d, too low", result.PreVerificationGas.Uint64())
	}
}

func TestEstimateCallGasBuffer(t *testing.T) {
	// Verify the 30% buffer is applied correctly
	tests := []struct {
		rawGas   uint64
		expected uint64
	}{
		{100_000, 130_000},
		{10, 13},
		{1, 1}, // 1 * 130 / 100 = 1 (integer division)
	}

	for _, tc := range tests {
		client := &mockClient{gasEstimate: tc.rawGas}
		est := New(client)
		op := testOp()

		result, err := est.Estimate(context.Background(), op)
		if err != nil {
			t.Fatalf("Estimate(rawGas=%d): %v", tc.rawGas, err)
		}
		if result.CallGasLimit.Uint64() != tc.expected {
			t.Errorf("rawGas=%d: callGasLimit=%d, want %d",
				tc.rawGas, result.CallGasLimit.Uint64(), tc.expected)
		}
	}
}

func TestEstimateFromFieldSet(t *testing.T) {
	// Verify the mock receives From=op.Sender (fix R4)
	var capturedFrom common.Address
	client := &mockClient{gasEstimate: 50_000}
	origEstimate := client.EstimateGas
	_ = origEstimate

	// Use a wrapper to capture the From field
	wrapper := &capturingClient{
		inner:       client,
		capturedMsg: nil,
	}
	est := New(wrapper)
	op := testOp()
	est.Estimate(context.Background(), op)

	capturedFrom = wrapper.capturedMsg.From
	if capturedFrom != op.Sender {
		t.Errorf("From = %s, want %s (R4 fix)", capturedFrom.Hex(), op.Sender.Hex())
	}
}

type capturingClient struct {
	inner       *mockClient
	capturedMsg *ethereum.CallMsg
}

func (c *capturingClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	c.capturedMsg = &msg
	return c.inner.EstimateGas(ctx, msg)
}
