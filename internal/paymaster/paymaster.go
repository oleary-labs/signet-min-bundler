package paymaster

import (
	"fmt"
	"math/big"
	"time"

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

// Service handles paymaster sponsorship for ERC-7677.
type Service struct {
	signer           PaymasterSigner
	paymasterAddress common.Address
	chainID          uint64
}

// New creates a paymaster Service.
func New(signer PaymasterSigner, paymasterAddress common.Address, chainID uint64) *Service {
	return &Service{
		signer:           signer,
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
func (s *Service) GetPaymasterData(op *core.PackedUserOp) (*SponsorResult, error) {
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

// toEthSignedMessageHash applies the Ethereum signed message prefix:
//
//	keccak256("\x19Ethereum Signed Message:\n32" || hash)
func toEthSignedMessageHash(hash []byte) []byte {
	prefix := []byte("\x19Ethereum Signed Message:\n32")
	return core.Keccak256(prefix, hash)
}
