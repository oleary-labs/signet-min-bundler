package core

import (
	"math/big"
	"strings"
	"testing"
)

func TestFromRPCBasic(t *testing.T) {
	rpcOp := UserOperationRPC{
		Sender:               "0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c",
		Nonce:                "0x0",
		CallData:             "0xb61d27f6000000000000000000000000a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000000",
		CallGasLimit:         "0xc350",   // 50000
		VerificationGasLimit: "0x7918",   // 31000
		PreVerificationGas:   "0xc350",   // 50000
		MaxFeePerGas:         "0xba43b7400", // 50 gwei
		MaxPriorityFeePerGas: "0x3b9aca00",  // 1 gwei
		Signature:            "0x" + "aa" + "bb" + "cc" + // just need 65 bytes
			"0000000000000000000000000000000000000000000000000000000000" +
			"0000000000000000000000000000000000000000000000000000000000" +
			"0000000000",
	}

	packed, err := FromRPC(rpcOp)
	if err != nil {
		t.Fatalf("FromRPC: %v", err)
	}

	if !strings.EqualFold(packed.Sender.Hex(), "0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c") {
		t.Errorf("sender = %s", packed.Sender.Hex())
	}
	if packed.Nonce.Sign() != 0 {
		t.Errorf("nonce = %s, want 0", packed.Nonce)
	}
	if len(packed.InitCode) != 0 {
		t.Error("initCode should be empty when factory is omitted")
	}
	if len(packed.PaymasterAndData) != 0 {
		t.Error("paymasterAndData should be empty when paymaster is omitted")
	}

	// Verify gas unpacking round-trips
	verifGas := new(big.Int).SetBytes(packed.AccountGasLimits[0:16])
	callGas := new(big.Int).SetBytes(packed.AccountGasLimits[16:32])
	if verifGas.Int64() != 31000 {
		t.Errorf("verificationGasLimit = %d, want 31000", verifGas.Int64())
	}
	if callGas.Int64() != 50000 {
		t.Errorf("callGasLimit = %d, want 50000", callGas.Int64())
	}

	maxPrio := new(big.Int).SetBytes(packed.GasFees[0:16])
	maxFee := new(big.Int).SetBytes(packed.GasFees[16:32])
	if maxPrio.Int64() != 1_000_000_000 {
		t.Errorf("maxPriorityFeePerGas = %d, want 1000000000", maxPrio.Int64())
	}
	if maxFee.Int64() != 50_000_000_000 {
		t.Errorf("maxFeePerGas = %d, want 50000000000", maxFee.Int64())
	}
}

func TestFromRPCWithFactory(t *testing.T) {
	rpcOp := UserOperationRPC{
		Sender:               "0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c",
		Nonce:                "0x0",
		Factory:              "0x1234567890abcdef1234567890abcdef12345678",
		FactoryData:          "0xdeadbeef",
		CallData:             "0x",
		CallGasLimit:         "0x1",
		VerificationGasLimit: "0x1",
		PreVerificationGas:   "0x1",
		MaxFeePerGas:         "0x1",
		MaxPriorityFeePerGas: "0x1",
		Signature:            "0x" + "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" + "00",
	}

	packed, err := FromRPC(rpcOp)
	if err != nil {
		t.Fatalf("FromRPC: %v", err)
	}

	if len(packed.InitCode) != 24 { // 20 bytes factory + 4 bytes factoryData
		t.Errorf("initCode length = %d, want 24", len(packed.InitCode))
	}
}

func TestFromRPCWithPaymaster(t *testing.T) {
	rpcOp := UserOperationRPC{
		Sender:               "0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c",
		Nonce:                "0x0",
		Paymaster:            "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PaymasterData:        "0x1234",
		CallData:             "0x",
		CallGasLimit:         "0x1",
		VerificationGasLimit: "0x1",
		PreVerificationGas:   "0x1",
		MaxFeePerGas:         "0x1",
		MaxPriorityFeePerGas: "0x1",
		Signature:            "0x" + "00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000" + "00",
	}

	packed, err := FromRPC(rpcOp)
	if err != nil {
		t.Fatalf("FromRPC: %v", err)
	}

	if len(packed.PaymasterAndData) != 22 { // 20 bytes paymaster + 2 bytes data
		t.Errorf("paymasterAndData length = %d, want 22", len(packed.PaymasterAndData))
	}
}

func TestFromRPCInvalidHex(t *testing.T) {
	rpcOp := UserOperationRPC{
		Sender:               "0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c",
		Nonce:                "not_hex",
		CallData:             "0x",
		CallGasLimit:         "0x1",
		VerificationGasLimit: "0x1",
		PreVerificationGas:   "0x1",
		MaxFeePerGas:         "0x1",
		MaxPriorityFeePerGas: "0x1",
		Signature:            "0x00",
	}

	_, err := FromRPC(rpcOp)
	if err == nil {
		t.Error("expected error for invalid nonce hex")
	}
}
