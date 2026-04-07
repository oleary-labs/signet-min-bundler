package core

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

// packUint128s packs two uint64 values into a bytes32 as uint128 hi || uint128 lo.
// Used for accountGasLimits: verificationGasLimit (hi) || callGasLimit (lo).
func PackUint128s(hi, lo uint64) [32]byte {
	var b [32]byte
	binary.BigEndian.PutUint64(b[8:16], hi)  // hi in upper half, right-aligned
	binary.BigEndian.PutUint64(b[24:32], lo) // lo in lower half, right-aligned
	return b
}

// packBigInts packs two *big.Int values into a bytes32 as uint128 hi || uint128 lo.
// Used for gasFees: maxPriorityFeePerGas (hi) || maxFeePerGas (lo).
func PackBigInts(hi, lo *big.Int) [32]byte {
	var b [32]byte
	into128 := func(dst []byte, v *big.Int) {
		if v == nil {
			return
		}
		vb := v.Bytes()
		if len(vb) > 16 {
			vb = vb[len(vb)-16:]
		}
		copy(dst[16-len(vb):16], vb) // right-align within the 16-byte half
	}
	into128(b[0:16], hi)
	into128(b[16:32], lo)
	return b
}

// Keccak256 computes the Ethereum keccak256 hash of the concatenation of inputs.
func Keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, d := range data {
		h.Write(d)
	}
	return h.Sum(nil)
}

// PadLeft32 left-pads b with zeros to exactly 32 bytes.
func PadLeft32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// DecodeHex decodes a 0x-prefixed hex string to bytes.
func DecodeHex(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(s)
}

// BytesToHex encodes bytes as a 0x-prefixed hex string; nil/empty → "0x".
func BytesToHex(b []byte) string {
	if len(b) == 0 {
		return "0x"
	}
	return "0x" + hex.EncodeToString(b)
}

// BigToHex encodes a *big.Int as a 0x-prefixed minimal hex string.
func BigToHex(n *big.Int) string {
	if n == nil || n.Sign() == 0 {
		return "0x0"
	}
	return "0x" + n.Text(16)
}

// HexToBigInt decodes a 0x-prefixed hex string to a *big.Int.
func HexToBigInt(s string) (*big.Int, error) {
	s = strings.TrimPrefix(s, "0x")
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex integer: %q", s)
	}
	return n, nil
}

// ParseBigHex is an alias for HexToBigInt.
func ParseBigHex(s string) (*big.Int, error) {
	return HexToBigInt(s)
}
