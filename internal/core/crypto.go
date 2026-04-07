package core

import (
	"crypto/sha256"
	"fmt"
	"math/big"
)

// PubKeyToAddress derives the Ethereum signer address from a 33-byte compressed
// secp256k1 public key: keccak256(uncompressed_x || uncompressed_y)[12:].
//
// This matches the SignetAccountFactory._signerAddress logic exactly.
// TODO: is there a more standard library that can be used here?
func PubKeyToAddress(pubKey []byte) ([20]byte, error) {
	if len(pubKey) != 33 {
		return [20]byte{}, fmt.Errorf("pubKeyToAddress: need 33 bytes, got %d", len(pubKey))
	}
	prefix := pubKey[0]
	if prefix != 0x02 && prefix != 0x03 {
		return [20]byte{}, fmt.Errorf("pubKeyToAddress: invalid prefix 0x%02x", prefix)
	}
	// secp256k1 field prime P.
	P, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", 16)
	x := new(big.Int).SetBytes(pubKey[1:33])

	// y² = x³ + 7 mod P
	y2 := new(big.Int).Mul(x, x)
	y2.Mod(y2, P)
	y2.Mul(y2, x)
	y2.Mod(y2, P)
	y2.Add(y2, big.NewInt(7))
	y2.Mod(y2, P)

	// y = y²^((P+1)/4) mod P  (valid since P ≡ 3 mod 4)
	exp := new(big.Int).Add(P, big.NewInt(1))
	exp.Rsh(exp, 2)
	y := new(big.Int).Exp(y2, exp, P)

	// Select the root whose parity matches the prefix.
	if (y.Bit(0) == 1) != (prefix == 0x03) {
		y.Sub(P, y)
	}

	// keccak256(x_32 || y_32)[12:]
	hash := Keccak256(PadLeft32(x.Bytes()), PadLeft32(y.Bytes()))
	var addr [20]byte
	copy(addr[:], hash[12:])
	return addr, nil
}

// ExpandMessageXMD implements RFC 9380 expand_message_xmd with SHA-256.
// s_in_bytes (block size) = 64, b_in_bytes (output size) = 32.
func ExpandMessageXMD(msg, dstPrime []byte, outLen int) []byte {
	ell := (outLen + 31) / 32

	zPad := make([]byte, 64)
	lStr := []byte{byte(outLen >> 8), byte(outLen)} // I2OSP(outLen, 2)

	// b0 = SHA256(Z_pad || msg || I2OSP(outLen,2) || 0x00 || DST_prime)
	h := sha256.New()
	h.Write(zPad)
	h.Write(msg)
	h.Write(lStr)
	h.Write([]byte{0x00})
	h.Write(dstPrime)
	b0 := h.Sum(nil)

	// b1 = SHA256(b0 || 0x01 || DST_prime)
	h = sha256.New()
	h.Write(b0)
	h.Write([]byte{0x01})
	h.Write(dstPrime)
	b1 := h.Sum(nil)

	bs := [][]byte{b1}
	for i := 2; i <= ell; i++ {
		prev := bs[len(bs)-1]
		xorPrev := make([]byte, 32)
		for j := 0; j < 32; j++ {
			xorPrev[j] = prev[j] ^ b0[j]
		}
		h = sha256.New()
		h.Write(xorPrev)
		h.Write([]byte{byte(i)})
		h.Write(dstPrime)
		bs = append(bs, h.Sum(nil))
	}

	var uniform []byte
	for _, b := range bs {
		uniform = append(uniform, b...)
	}
	return uniform[:outLen]
}

// FrostChallenge computes the FROST RFC 9591 challenge:
//
//	c = int(expand_message_xmd(SHA-256, input, DST, 48)) mod N
//
// where DST = "FROST-secp256k1-SHA256-v1chal" and input = R_compressed || PK || message.
// This matches the on-chain FROSTVerifier._frostChallenge exactly.
func FrostChallenge(input []byte) *big.Int {
	dst := []byte("FROST-secp256k1-SHA256-v1chal")
	dstPrime := append(dst, byte(len(dst))) // DST || I2OSP(len(DST),1)

	uniform := ExpandMessageXMD(input, dstPrime, 48)

	N, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	c := new(big.Int).SetBytes(uniform)
	c.Mod(c, N)
	return c
}
