package paymaster

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

type mockSigner struct {
	addr common.Address
}

func (s *mockSigner) Address() common.Address { return s.addr }
func (s *mockSigner) SignHash(hash []byte) ([]byte, error) {
	sig := make([]byte, 65)
	sig[0] = 0xaa // non-zero so we can distinguish from stub
	sig[64] = 27
	return sig, nil
}

var (
	signerAddr     = common.HexToAddress("0x1111111111111111111111111111111111111111")
	paymasterAddr  = common.HexToAddress("0x2222222222222222222222222222222222222222")
)

func testOp() *core.PackedUserOp {
	return &core.PackedUserOp{
		Sender:             common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Nonce:              big.NewInt(0),
		CallData:           []byte{0x01, 0x02, 0x03, 0x04},
		AccountGasLimits:   core.PackUint128s(50_000, 100_000),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            core.PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		Signature:          make([]byte, 65),
	}
}

func TestGetStubData(t *testing.T) {
	svc := New(&mockSigner{addr: signerAddr}, paymasterAddr, 1)
	result := svc.GetStubData(testOp())

	if result.Paymaster != paymasterAddr {
		t.Errorf("paymaster = %s, want %s", result.Paymaster.Hex(), paymasterAddr.Hex())
	}
	// paymasterData = 64 (abi.encode(validUntil, validAfter)) + 65 (dummy sig) = 129 bytes
	if len(result.PaymasterData) != 129 {
		t.Errorf("paymasterData len = %d, want 129", len(result.PaymasterData))
	}
	// Stub signature should be all zeros.
	for i := 64; i < 129; i++ {
		if result.PaymasterData[i] != 0 {
			t.Errorf("stub sig byte %d = 0x%02x, want 0x00", i, result.PaymasterData[i])
			break
		}
	}
	if result.PaymasterVerificationGasLimit != DefaultVerificationGasLimit {
		t.Errorf("verificationGasLimit = %d, want %d", result.PaymasterVerificationGasLimit, DefaultVerificationGasLimit)
	}
}

func TestGetPaymasterData(t *testing.T) {
	svc := New(&mockSigner{addr: signerAddr}, paymasterAddr, 1)
	result, err := svc.GetPaymasterData(testOp())
	if err != nil {
		t.Fatalf("GetPaymasterData: %v", err)
	}

	if result.Paymaster != paymasterAddr {
		t.Errorf("paymaster = %s, want %s", result.Paymaster.Hex(), paymasterAddr.Hex())
	}
	if len(result.PaymasterData) != 129 {
		t.Errorf("paymasterData len = %d, want 129", len(result.PaymasterData))
	}
	// Real signature should be non-zero (first byte = 0xaa from mock).
	if result.PaymasterData[64] != 0xaa {
		t.Errorf("sig first byte = 0x%02x, want 0xaa", result.PaymasterData[64])
	}
}

func TestGetHashDeterministic(t *testing.T) {
	svc := New(&mockSigner{addr: signerAddr}, paymasterAddr, 1)
	op := testOp()

	h1 := svc.getHash(op, 1000, 500)
	h2 := svc.getHash(op, 1000, 500)

	if string(h1) != string(h2) {
		t.Error("getHash should be deterministic")
	}

	// Different validity window should produce different hash.
	h3 := svc.getHash(op, 2000, 500)
	if string(h1) == string(h3) {
		t.Error("different validUntil should produce different hash")
	}
}
