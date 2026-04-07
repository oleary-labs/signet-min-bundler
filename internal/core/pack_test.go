package core

import (
	"math/big"
	"testing"
)

func TestPackUint128s(t *testing.T) {
	result := PackUint128s(100_000, 500_000)
	// hi=100000 should be in bytes [8:16], lo=500000 in bytes [24:32]
	hi := new(big.Int).SetBytes(result[0:16])
	lo := new(big.Int).SetBytes(result[16:32])
	if hi.Uint64() != 100_000 {
		t.Errorf("hi = %d, want 100000", hi.Uint64())
	}
	if lo.Uint64() != 500_000 {
		t.Errorf("lo = %d, want 500000", lo.Uint64())
	}
}

func TestPackBigInts(t *testing.T) {
	hi := big.NewInt(1_000_000_000) // 1 gwei
	lo := big.NewInt(50_000_000_000) // 50 gwei
	result := PackBigInts(hi, lo)
	gotHi := new(big.Int).SetBytes(result[0:16])
	gotLo := new(big.Int).SetBytes(result[16:32])
	if gotHi.Cmp(hi) != 0 {
		t.Errorf("hi = %s, want %s", gotHi, hi)
	}
	if gotLo.Cmp(lo) != 0 {
		t.Errorf("lo = %s, want %s", gotLo, lo)
	}
}

func TestPackBigIntsNil(t *testing.T) {
	result := PackBigInts(nil, big.NewInt(42))
	gotHi := new(big.Int).SetBytes(result[0:16])
	gotLo := new(big.Int).SetBytes(result[16:32])
	if gotHi.Sign() != 0 {
		t.Errorf("hi should be zero for nil input, got %s", gotHi)
	}
	if gotLo.Int64() != 42 {
		t.Errorf("lo = %d, want 42", gotLo.Int64())
	}
}

func TestPadLeft32(t *testing.T) {
	short := []byte{0x01, 0x02}
	padded := PadLeft32(short)
	if len(padded) != 32 {
		t.Fatalf("len = %d, want 32", len(padded))
	}
	if padded[30] != 0x01 || padded[31] != 0x02 {
		t.Errorf("content mismatch: got %x", padded)
	}
	for i := 0; i < 30; i++ {
		if padded[i] != 0 {
			t.Errorf("byte %d = %d, want 0", i, padded[i])
		}
	}

	// Already 32 bytes — no padding needed
	full := make([]byte, 32)
	full[0] = 0xff
	result := PadLeft32(full)
	if len(result) != 32 || result[0] != 0xff {
		t.Errorf("32-byte input should pass through unchanged")
	}
}

func TestKeccak256(t *testing.T) {
	// keccak256("") = c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470
	empty := Keccak256([]byte{})
	expected := "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	got := BytesToHex(empty)[2:] // strip 0x
	if got != expected {
		t.Errorf("keccak256('') = %s, want %s", got, expected)
	}
}

func TestDecodeHex(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{"0x", "", false},
		{"0xff", "ff", false},
		{"0xdead", "dead", false},
		{"dead", "dead", false},
		{"0xgg", "", true},
	}
	for _, tc := range tests {
		b, err := DecodeHex(tc.input)
		if tc.err {
			if err == nil {
				t.Errorf("DecodeHex(%q) should fail", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("DecodeHex(%q) error: %v", tc.input, err)
			continue
		}
		got := ""
		if len(b) > 0 {
			got = BytesToHex(b)[2:]
		}
		if got != tc.want {
			t.Errorf("DecodeHex(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBytesToHex(t *testing.T) {
	if BytesToHex(nil) != "0x" {
		t.Error("nil should produce 0x")
	}
	if BytesToHex([]byte{}) != "0x" {
		t.Error("empty should produce 0x")
	}
	if BytesToHex([]byte{0xab, 0xcd}) != "0xabcd" {
		t.Errorf("got %s", BytesToHex([]byte{0xab, 0xcd}))
	}
}

func TestBigToHex(t *testing.T) {
	if BigToHex(nil) != "0x0" {
		t.Error("nil should produce 0x0")
	}
	if BigToHex(big.NewInt(0)) != "0x0" {
		t.Error("zero should produce 0x0")
	}
	if BigToHex(big.NewInt(255)) != "0xff" {
		t.Errorf("255 = %s, want 0xff", BigToHex(big.NewInt(255)))
	}
}

func TestHexToBigInt(t *testing.T) {
	n, err := HexToBigInt("0xff")
	if err != nil {
		t.Fatal(err)
	}
	if n.Int64() != 255 {
		t.Errorf("got %d, want 255", n.Int64())
	}

	_, err = HexToBigInt("0xnope")
	if err == nil {
		t.Error("should fail on invalid hex")
	}
}

func TestRoundTripBigHex(t *testing.T) {
	original := big.NewInt(123456789)
	encoded := BigToHex(original)
	decoded, err := HexToBigInt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if original.Cmp(decoded) != 0 {
		t.Errorf("round trip failed: %s -> %s -> %s", original, encoded, decoded)
	}
}
