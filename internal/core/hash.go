package core

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// ComputeUserOpHash computes the ERC-4337 v0.7 userOpHash:
//
//	keccak256(abi.encode(
//	    keccak256(abi.encode(
//	        sender, nonce,
//	        keccak256(initCode), keccak256(callData),
//	        accountGasLimits, preVerificationGas,
//	        gasFees, keccak256(paymasterAndData)
//	    )),
//	    entryPoint, chainId
//	))
//
// All dynamic fields are pre-hashed so both encodes contain only static
// 32-byte values — no offsets needed.
//
// Note: the outer encode field order is innerHash, entryPoint, chainId —
// confirmed from the send-userop source.
func ComputeUserOpHash(op *PackedUserOp, entryPoint common.Address, chainID uint64) common.Hash {
	chainIDBig := new(big.Int).SetUint64(chainID)

	inner := make([]byte, 256) // 8 × 32 bytes

	copy(inner[12:32], op.Sender[:])
	copy(inner[32:64], PadLeft32(op.Nonce.Bytes()))
	copy(inner[64:96], Keccak256(op.InitCode))
	copy(inner[96:128], Keccak256(op.CallData))
	copy(inner[128:160], op.AccountGasLimits[:])
	copy(inner[160:192], PadLeft32(op.PreVerificationGas.Bytes()))
	copy(inner[192:224], op.GasFees[:])
	copy(inner[224:256], Keccak256(op.PaymasterAndData))

	var innerHash [32]byte
	copy(innerHash[:], Keccak256(inner))

	// Outer: abi.encode(innerHash, address(entryPoint), chainId)
	outer := make([]byte, 96)
	copy(outer[0:32], innerHash[:])
	copy(outer[44:64], entryPoint[:]) // address left-padded: [32:44] zeros, [44:64] address
	copy(outer[64:96], PadLeft32(chainIDBig.Bytes()))

	var h common.Hash
	copy(h[:], Keccak256(outer))
	return h
}
