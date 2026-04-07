package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestComputeUserOpHashDeterministic(t *testing.T) {
	op := &PackedUserOp{
		Sender:             common.HexToAddress("0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c"),
		Nonce:              big.NewInt(0),
		InitCode:           nil,
		CallData:           []byte{0xb6, 0x1d, 0x27, 0xf6},
		AccountGasLimits:   PackUint128s(31000, 50000),
		PreVerificationGas: big.NewInt(50000),
		GasFees:            PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
		PaymasterAndData:   nil,
		Signature:          make([]byte, 65),
	}

	entryPoint := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")
	chainID := uint64(1)

	hash1 := ComputeUserOpHash(op, entryPoint, chainID)
	hash2 := ComputeUserOpHash(op, entryPoint, chainID)

	if hash1 != hash2 {
		t.Error("hash is not deterministic")
	}

	// Hash should be non-zero
	if hash1 == (common.Hash{}) {
		t.Error("hash should not be zero")
	}
}

func TestComputeUserOpHashDifferentChainID(t *testing.T) {
	op := &PackedUserOp{
		Sender:             common.HexToAddress("0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c"),
		Nonce:              big.NewInt(0),
		InitCode:           nil,
		CallData:           []byte{},
		AccountGasLimits:   [32]byte{},
		PreVerificationGas: big.NewInt(0),
		GasFees:            [32]byte{},
		PaymasterAndData:   nil,
		Signature:          make([]byte, 65),
	}
	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	h1 := ComputeUserOpHash(op, ep, 1)
	h2 := ComputeUserOpHash(op, ep, 137) // polygon

	if h1 == h2 {
		t.Error("different chain IDs should produce different hashes")
	}
}

func TestComputeUserOpHashDifferentEntryPoint(t *testing.T) {
	op := &PackedUserOp{
		Sender:             common.HexToAddress("0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c"),
		Nonce:              big.NewInt(0),
		InitCode:           nil,
		CallData:           []byte{},
		AccountGasLimits:   [32]byte{},
		PreVerificationGas: big.NewInt(0),
		GasFees:            [32]byte{},
		PaymasterAndData:   nil,
		Signature:          make([]byte, 65),
	}

	h1 := ComputeUserOpHash(op, common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032"), 1)
	h2 := ComputeUserOpHash(op, common.HexToAddress("0x1111111111111111111111111111111111111111"), 1)

	if h1 == h2 {
		t.Error("different entry points should produce different hashes")
	}
}

// TestComputeUserOpHashSignatureExcluded verifies that the signature is NOT
// part of the hash (as per ERC-4337 spec: signature is excluded from the hash).
func TestComputeUserOpHashSignatureExcluded(t *testing.T) {
	makeOp := func(sig byte) *PackedUserOp {
		s := make([]byte, 65)
		s[0] = sig
		return &PackedUserOp{
			Sender:             common.HexToAddress("0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c"),
			Nonce:              big.NewInt(0),
			InitCode:           nil,
			CallData:           []byte{0xb6, 0x1d, 0x27, 0xf6},
			AccountGasLimits:   PackUint128s(31000, 50000),
			PreVerificationGas: big.NewInt(50000),
			GasFees:            PackBigInts(big.NewInt(1_000_000_000), big.NewInt(50_000_000_000)),
			PaymasterAndData:   nil,
			Signature:          s,
		}
	}

	ep := common.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")
	h1 := ComputeUserOpHash(makeOp(0x00), ep, 1)
	h2 := ComputeUserOpHash(makeOp(0xff), ep, 1)

	if h1 != h2 {
		t.Error("signature should not affect the hash")
	}
}
