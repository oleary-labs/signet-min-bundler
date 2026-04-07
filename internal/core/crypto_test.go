package core

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

func TestPubKeyToAddress(t *testing.T) {
	// Test vector from frost_vector.json:
	// groupPubKey: 0x03ba81688507e7e2e2f29c90aebe66cc05aef00ad25fb79a8f2989fa7aab81ba8f
	// signer:      0xd051c8072cc737d4f4ef99b0cc84a6e7c17f809c
	pubKeyHex := "03ba81688507e7e2e2f29c90aebe66cc05aef00ad25fb79a8f2989fa7aab81ba8f"
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		t.Fatal(err)
	}

	addr, err := PubKeyToAddress(pubKey)
	if err != nil {
		t.Fatalf("PubKeyToAddress: %v", err)
	}

	expected := "d051c8072cc737d4f4ef99b0cc84a6e7c17f809c"
	got := hex.EncodeToString(addr[:])
	if got != expected {
		t.Errorf("address = %s, want %s", got, expected)
	}
}

func TestPubKeyToAddressInvalidLength(t *testing.T) {
	_, err := PubKeyToAddress([]byte{0x02, 0x01})
	if err == nil {
		t.Error("expected error for short key")
	}
}

func TestPubKeyToAddressInvalidPrefix(t *testing.T) {
	key := make([]byte, 33)
	key[0] = 0x04 // uncompressed prefix, invalid for this function
	_, err := PubKeyToAddress(key)
	if err == nil {
		t.Error("expected error for invalid prefix")
	}
}

func TestFrostChallenge(t *testing.T) {
	// Smoke test: challenge should be non-zero and deterministic
	input := []byte("test input for frost challenge")
	c1 := FrostChallenge(input)
	c2 := FrostChallenge(input)

	if c1.Sign() == 0 {
		t.Error("challenge should be non-zero")
	}
	if c1.Cmp(c2) != 0 {
		t.Error("challenge is not deterministic")
	}

	// Different input should give different challenge
	c3 := FrostChallenge([]byte("different input"))
	if c1.Cmp(c3) == 0 {
		t.Error("different inputs should give different challenges")
	}
}

func TestExpandMessageXMD(t *testing.T) {
	// Basic smoke test: output length should be correct
	dst := []byte("FROST-secp256k1-SHA256-v1chal")
	dstPrime := append(dst, byte(len(dst)))
	result := ExpandMessageXMD([]byte("test"), dstPrime, 48)
	if len(result) != 48 {
		t.Errorf("output length = %d, want 48", len(result))
	}

	// Deterministic
	result2 := ExpandMessageXMD([]byte("test"), dstPrime, 48)
	if !bytesEqual(result, result2) {
		t.Error("not deterministic")
	}
}

func TestFrostChallengeWithVector(t *testing.T) {
	// From frost_vector.json, the msgHash was signed with groupPubKey.
	// We can verify the challenge computation is consistent by checking that
	// FrostChallenge(R_compressed || pubKey || msg) produces a valid scalar.
	sigRxHex := "b3480f95d8a8a830ddd23b41e758b8b27cb11d082ee0e466ababed3a357e1632"
	sigV := byte(1)
	pubKeyHex := "03ba81688507e7e2e2f29c90aebe66cc05aef00ad25fb79a8f2989fa7aab81ba8f"
	msgHashHex := "4badeece6c056bd51ae542637718a0c9ae9ea5cf5c3c6b5687ca9a3b77319067"

	sigRx, _ := hex.DecodeString(sigRxHex)
	pubKey, _ := hex.DecodeString(pubKeyHex)
	msgHash, _ := hex.DecodeString(msgHashHex)

	// Reconstruct compressed R
	rCompressed := make([]byte, 33)
	if sigV == 0 {
		rCompressed[0] = 0x02
	} else {
		rCompressed[0] = 0x03
	}
	copy(rCompressed[1:], sigRx)

	input := append(rCompressed, pubKey...)
	input = append(input, msgHash...)

	c := FrostChallenge(input)
	if c.Sign() == 0 {
		t.Error("challenge from real vector should be non-zero")
	}

	// The challenge should be less than the group order N
	N := hexMustBigInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141")
	if c.Cmp(N) >= 0 {
		t.Error("challenge should be less than group order")
	}
}

func hexMustBigInt(s string) *big.Int {
	n, _ := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	return n
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
