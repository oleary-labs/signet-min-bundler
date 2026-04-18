package paymaster

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/oleary-labs/signet-min-bundler/internal/core"
)

// DefaultValidityWindow is how long a paymaster sponsorship is valid.
const DefaultValidityWindow = 10 * time.Minute

// DefaultVerificationGasLimit is the gas for validatePaymasterUserOp.
const DefaultVerificationGasLimit = 50_000

// DefaultPostOpGasLimit is the gas for postOp (unused by VerifyingPaymaster).
const DefaultPostOpGasLimit = 0

// PaymasterSigner abstracts the signing key for testability.
type PaymasterSigner interface {
	Address() common.Address
	SignHash(hash []byte) ([]byte, error)
}

// EthCaller is the subset of ethclient.Client needed for shouldSponsor checks.
type EthCaller interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// Service handles paymaster sponsorship for ERC-7677.
type Service struct {
	signer           PaymasterSigner
	client           EthCaller
	paymasterAddress common.Address
	chainID          uint64
}

// New creates a paymaster Service.
func New(signer PaymasterSigner, client EthCaller, paymasterAddress common.Address, chainID uint64) *Service {
	return &Service{
		signer:           signer,
		client:           client,
		paymasterAddress: paymasterAddress,
		chainID:          chainID,
	}
}

// SponsorResult contains the paymaster fields for a UserOp.
type SponsorResult struct {
	Paymaster                     common.Address
	PaymasterData                 []byte
	PaymasterVerificationGasLimit uint64
	PaymasterPostOpGasLimit       uint64
}

// GetStubData returns stub paymaster data for gas estimation.
// The signature is zeroed out but the correct length so gas estimation is accurate.
func (s *Service) GetStubData(op *core.PackedUserOp) *SponsorResult {
	validUntil, validAfter := s.validityWindow()

	// Stub paymasterData: abi.encode(validUntil, validAfter) + 65 zero bytes (dummy sig)
	data := encodeValidityAndSig(validUntil, validAfter, make([]byte, 65))

	return &SponsorResult{
		Paymaster:                     s.paymasterAddress,
		PaymasterData:                 data,
		PaymasterVerificationGasLimit: DefaultVerificationGasLimit,
		PaymasterPostOpGasLimit:       DefaultPostOpGasLimit,
	}
}

// GetPaymasterData returns signed paymaster data for submission.
// Calls the on-chain shouldSponsor check before signing.
func (s *Service) GetPaymasterData(ctx context.Context, op *core.PackedUserOp) (*SponsorResult, error) {
	// Check on-chain sponsorship policy before signing.
	if err := s.checkShouldSponsor(ctx, op); err != nil {
		return nil, err
	}

	validUntil, validAfter := s.validityWindow()

	hash := s.getHash(op, validUntil, validAfter)

	// VerifyingPaymaster uses toEthSignedMessageHash.
	prefixed := toEthSignedMessageHash(hash)
	sig, err := s.signer.SignHash(prefixed)
	if err != nil {
		return nil, fmt.Errorf("sign paymaster data: %w", err)
	}

	data := encodeValidityAndSig(validUntil, validAfter, sig)

	return &SponsorResult{
		Paymaster:                     s.paymasterAddress,
		PaymasterData:                 data,
		PaymasterVerificationGasLimit: DefaultVerificationGasLimit,
		PaymasterPostOpGasLimit:       DefaultPostOpGasLimit,
	}, nil
}

// getHash computes the VerifyingPaymaster's getHash, matching the on-chain
// implementation exactly:
//
//	keccak256(abi.encode(
//	    sender, nonce,
//	    keccak256(initCode), keccak256(callData),
//	    accountGasLimits,
//	    paymasterAndData[20:52],  // packed paymaster gas limits
//	    preVerificationGas, gasFees,
//	    chainId, address(paymaster),
//	    validUntil, validAfter
//	))
func (s *Service) getHash(op *core.PackedUserOp, validUntil, validAfter uint64) []byte {
	buf := make([]byte, 12*32) // 12 fields × 32 bytes

	copy(buf[12:32], op.Sender[:])                                       // address sender
	copy(buf[32:64], core.PadLeft32(op.Nonce.Bytes()))                   // uint256 nonce
	copy(buf[64:96], core.Keccak256(op.InitCode))                        // keccak256(initCode)
	copy(buf[96:128], core.Keccak256(op.CallData))                       // keccak256(callData)
	copy(buf[128:160], op.AccountGasLimits[:])                            // bytes32 accountGasLimits
	copy(buf[160:192], s.paymasterGasLimits())                            // paymasterAndData[20:52]
	copy(buf[192:224], core.PadLeft32(op.PreVerificationGas.Bytes()))    // uint256 preVerificationGas
	copy(buf[224:256], op.GasFees[:])                                     // bytes32 gasFees
	copy(buf[256:288], core.PadLeft32(new(big.Int).SetUint64(s.chainID).Bytes())) // uint256 chainId
	copy(buf[300:320], s.paymasterAddress[:])                             // address paymaster
	copy(buf[320:352], core.PadLeft32(new(big.Int).SetUint64(validUntil).Bytes())) // uint48 validUntil
	copy(buf[352:384], core.PadLeft32(new(big.Int).SetUint64(validAfter).Bytes())) // uint48 validAfter

	return core.Keccak256(buf)
}

// paymasterGasLimits returns the 32-byte packed paymaster gas limits
// (verificationGasLimit(16) + postOpGasLimit(16)), matching paymasterAndData[20:52].
func (s *Service) paymasterGasLimits() []byte {
	packed := core.PackUint128s(DefaultVerificationGasLimit, DefaultPostOpGasLimit)
	return packed[:]
}

// validityWindow returns (validUntil, validAfter) timestamps.
func (s *Service) validityWindow() (uint64, uint64) {
	now := uint64(time.Now().Unix())
	return now + uint64(DefaultValidityWindow.Seconds()), now
}

// encodeValidityAndSig builds paymasterData = abi.encode(validUntil, validAfter) + signature.
func encodeValidityAndSig(validUntil, validAfter uint64, sig []byte) []byte {
	// abi.encode(uint48 validUntil, uint48 validAfter) = 64 bytes
	encoded := make([]byte, 64+len(sig))
	copy(encoded[0:32], core.PadLeft32(new(big.Int).SetUint64(validUntil).Bytes()))
	copy(encoded[32:64], core.PadLeft32(new(big.Int).SetUint64(validAfter).Bytes()))
	copy(encoded[64:], sig)
	return encoded
}

// shouldSponsorSelector is the selector for shouldSponsor(PackedUserOperation).
// The PackedUserOperation is a tuple with dynamic fields, so this is:
// keccak256("shouldSponsor((address,uint256,bytes,bytes,bytes32,uint256,bytes32,bytes,bytes))")
var shouldSponsorSelector = core.Keccak256([]byte("shouldSponsor((address,uint256,bytes,bytes,bytes32,uint256,bytes32,bytes,bytes))"))[:4]

// checkShouldSponsor calls the paymaster's shouldSponsor view function via eth_call.
// Returns nil if the op should be sponsored, or an error with the rejection reason.
func (s *Service) checkShouldSponsor(ctx context.Context, op *core.PackedUserOp) error {
	// ABI-encode the call: shouldSponsor(PackedUserOperation)
	// The PackedUserOperation is a tuple passed as a single dynamic parameter.
	calldata := encodeShouldSponsorCall(op)

	msg := ethereum.CallMsg{
		To:   &s.paymasterAddress,
		Data: calldata,
	}

	result, err := s.client.CallContract(ctx, msg, nil)
	if err != nil {
		return fmt.Errorf("shouldSponsor call failed: %w", err)
	}

	// Result is abi-encoded bool (32 bytes, last byte is 0 or 1).
	if len(result) < 32 {
		return fmt.Errorf("shouldSponsor: unexpected response length %d", len(result))
	}
	if result[31] != 1 {
		return fmt.Errorf("sponsorship rejected by paymaster")
	}

	return nil
}

// encodeShouldSponsorCall ABI-encodes shouldSponsor(PackedUserOperation).
// Reuses the handleOps tuple encoding logic for a single PackedUserOperation.
func encodeShouldSponsorCall(op *core.PackedUserOp) []byte {
	var buf []byte
	buf = append(buf, shouldSponsorSelector...)

	// Single tuple parameter — offset to its data.
	buf = append(buf, core.PadLeft32(big.NewInt(32).Bytes())...) // offset = 32

	// Encode the PackedUserOperation tuple (same layout as handleOps elements).
	encoded := encodePackedUserOpForCall(op)
	buf = append(buf, encoded...)

	return buf
}

// encodePackedUserOpForCall ABI-encodes a PackedUserOperation tuple for an eth_call.
// Same layout as encodePackedUserOp in the bundler loop.
func encodePackedUserOpForCall(op *core.PackedUserOp) []byte {
	headSize := 9 * 32

	initCodeEnc := encodeBytes(op.InitCode)
	callDataEnc := encodeBytes(op.CallData)
	pmDataEnc := encodeBytes(op.PaymasterAndData)
	sigEnc := encodeBytes(op.Signature)

	totalSize := headSize + len(initCodeEnc) + len(callDataEnc) + len(pmDataEnc) + len(sigEnc)
	buf := make([]byte, totalSize)

	// Static fields.
	copy(buf[12:32], op.Sender[:])
	copy(buf[32:64], core.PadLeft32(op.Nonce.Bytes()))
	copy(buf[4*32:5*32], op.AccountGasLimits[:])
	copy(buf[5*32:6*32], core.PadLeft32(op.PreVerificationGas.Bytes()))
	copy(buf[6*32:7*32], op.GasFees[:])

	// Dynamic field offsets.
	tailOffset := headSize
	copy(buf[2*32:3*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes()))
	tailOffset += len(initCodeEnc)
	copy(buf[3*32:4*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes()))
	tailOffset += len(callDataEnc)
	copy(buf[7*32:8*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes()))
	tailOffset += len(pmDataEnc)
	copy(buf[8*32:9*32], core.PadLeft32(big.NewInt(int64(tailOffset)).Bytes()))

	// Dynamic data.
	pos := headSize
	copy(buf[pos:], initCodeEnc)
	pos += len(initCodeEnc)
	copy(buf[pos:], callDataEnc)
	pos += len(callDataEnc)
	copy(buf[pos:], pmDataEnc)
	pos += len(pmDataEnc)
	copy(buf[pos:], sigEnc)

	return buf
}

// encodeBytes ABI-encodes a bytes value: length(32) + data(padded to 32).
func encodeBytes(data []byte) []byte {
	paddedLen := ((len(data) + 31) / 32) * 32
	buf := make([]byte, 32+paddedLen)
	copy(buf[0:32], core.PadLeft32(big.NewInt(int64(len(data))).Bytes()))
	copy(buf[32:], data)
	return buf
}

// toEthSignedMessageHash applies the Ethereum signed message prefix:
//
//	keccak256("\x19Ethereum Signed Message:\n32" || hash)
func toEthSignedMessageHash(hash []byte) []byte {
	prefix := []byte("\x19Ethereum Signed Message:\n32")
	return core.Keccak256(prefix, hash)
}
