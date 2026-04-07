package core

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// UserOperationRPC is the ERC-4337 v0.7 JSON-RPC wire format.
// Fields are split (not packed) as sent by clients.
type UserOperationRPC struct {
	Sender               string `json:"sender"`
	Nonce                string `json:"nonce"`
	Factory              string `json:"factory,omitempty"`
	FactoryData          string `json:"factoryData,omitempty"`
	CallData             string `json:"callData"`
	CallGasLimit         string `json:"callGasLimit"`
	VerificationGasLimit string `json:"verificationGasLimit"`
	PreVerificationGas   string `json:"preVerificationGas"`
	MaxFeePerGas         string `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas"`
	Paymaster            string `json:"paymaster,omitempty"`
	PaymasterData        string `json:"paymasterData,omitempty"`
	Signature            string `json:"signature"`
}

// PackedUserOp is the ERC-4337 v0.7 on-chain struct.
// Used for hash computation, handleOps calldata, and DB storage.
type PackedUserOp struct {
	Sender             common.Address
	Nonce              *big.Int
	InitCode           []byte   // factory (20 bytes) + factoryData
	CallData           []byte
	AccountGasLimits   [32]byte // verificationGasLimit (hi 128) || callGasLimit (lo 128)
	PreVerificationGas *big.Int
	GasFees            [32]byte // maxPriorityFeePerGas (hi 128) || maxFeePerGas (lo 128)
	PaymasterAndData   []byte
	Signature          []byte // 65 bytes: R.x(32) || z(32) || v(1)
}

// FromRPC converts a wire-format UserOperationRPC into a PackedUserOp.
func FromRPC(op UserOperationRPC) (*PackedUserOp, error) {
	sender := common.HexToAddress(op.Sender)

	nonce, err := HexToBigInt(op.Nonce)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	callData, err := DecodeHex(op.CallData)
	if err != nil {
		return nil, fmt.Errorf("callData: %w", err)
	}

	callGasLimit, err := HexToBigInt(op.CallGasLimit)
	if err != nil {
		return nil, fmt.Errorf("callGasLimit: %w", err)
	}

	verificationGasLimit, err := HexToBigInt(op.VerificationGasLimit)
	if err != nil {
		return nil, fmt.Errorf("verificationGasLimit: %w", err)
	}

	preVerificationGas, err := HexToBigInt(op.PreVerificationGas)
	if err != nil {
		return nil, fmt.Errorf("preVerificationGas: %w", err)
	}

	maxFeePerGas, err := HexToBigInt(op.MaxFeePerGas)
	if err != nil {
		return nil, fmt.Errorf("maxFeePerGas: %w", err)
	}

	maxPriorityFeePerGas, err := HexToBigInt(op.MaxPriorityFeePerGas)
	if err != nil {
		return nil, fmt.Errorf("maxPriorityFeePerGas: %w", err)
	}

	sig, err := DecodeHex(op.Signature)
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}

	// Build initCode: factory (20 bytes) + factoryData
	var initCode []byte
	if op.Factory != "" {
		factory := common.HexToAddress(op.Factory)
		factoryData, err := DecodeHex(op.FactoryData)
		if err != nil {
			return nil, fmt.Errorf("factoryData: %w", err)
		}
		initCode = make([]byte, 20+len(factoryData))
		copy(initCode[0:20], factory.Bytes())
		copy(initCode[20:], factoryData)
	}

	// Build paymasterAndData: paymaster (20 bytes) + paymasterData
	var paymasterAndData []byte
	if op.Paymaster != "" {
		paymaster := common.HexToAddress(op.Paymaster)
		pmData, err := DecodeHex(op.PaymasterData)
		if err != nil {
			return nil, fmt.Errorf("paymasterData: %w", err)
		}
		paymasterAndData = make([]byte, 20+len(pmData))
		copy(paymasterAndData[0:20], paymaster.Bytes())
		copy(paymasterAndData[20:], pmData)
	}

	packed := &PackedUserOp{
		Sender:             sender,
		Nonce:              nonce,
		InitCode:           initCode,
		CallData:           callData,
		AccountGasLimits:   PackBigInts(verificationGasLimit, callGasLimit),
		PreVerificationGas: preVerificationGas,
		GasFees:            PackBigInts(maxPriorityFeePerGas, maxFeePerGas),
		PaymasterAndData:   paymasterAndData,
		Signature:          sig,
	}

	return packed, nil
}
