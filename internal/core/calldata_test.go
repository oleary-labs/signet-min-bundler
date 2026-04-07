package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestDecodeSignetExecuteTarget(t *testing.T) {
	target := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	calldata := BuildExecuteCalldata(target, big.NewInt(0), []byte{})

	got, err := DecodeSignetExecuteTarget(calldata)
	if err != nil {
		t.Fatalf("DecodeSignetExecuteTarget: %v", err)
	}
	if got != target {
		t.Errorf("target = %s, want %s", got.Hex(), target.Hex())
	}
}

func TestDecodeSignetExecuteTargetTooShort(t *testing.T) {
	_, err := DecodeSignetExecuteTarget([]byte{0x01, 0x02})
	if err == nil {
		t.Error("expected error for short calldata")
	}
}

func TestDecodeSignetExecuteTargetWrongSelector(t *testing.T) {
	data := make([]byte, 4+32)
	copy(data[:4], []byte{0xde, 0xad, 0xbe, 0xef})
	_, err := DecodeSignetExecuteTarget(data)
	if err == nil {
		t.Error("expected error for wrong selector")
	}
}

func TestDecodeSignetExecuteRoundTrip(t *testing.T) {
	target := common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")
	value := big.NewInt(1_000_000)
	innerData := []byte{0xaa, 0xbb, 0xcc, 0xdd}

	calldata := BuildExecuteCalldata(target, value, innerData)

	gotTarget, gotValue, gotData, err := DecodeSignetExecute(calldata)
	if err != nil {
		t.Fatalf("DecodeSignetExecute: %v", err)
	}
	if gotTarget != target {
		t.Errorf("target = %s, want %s", gotTarget.Hex(), target.Hex())
	}
	if gotValue.Cmp(value) != 0 {
		t.Errorf("value = %s, want %s", gotValue, value)
	}
	if len(gotData) != len(innerData) {
		t.Fatalf("data len = %d, want %d", len(gotData), len(innerData))
	}
	for i := range innerData {
		if gotData[i] != innerData[i] {
			t.Errorf("data[%d] = %x, want %x", i, gotData[i], innerData[i])
		}
	}
}

func TestDecodeSignetExecuteEmptyData(t *testing.T) {
	target := common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	calldata := BuildExecuteCalldata(target, big.NewInt(0), []byte{})

	_, _, gotData, err := DecodeSignetExecute(calldata)
	if err != nil {
		t.Fatalf("DecodeSignetExecute: %v", err)
	}
	if len(gotData) != 0 {
		t.Errorf("expected empty data, got %d bytes", len(gotData))
	}
}

func TestBuildExecuteCalldataSelector(t *testing.T) {
	calldata := BuildExecuteCalldata(common.Address{}, big.NewInt(0), []byte{})
	// execute(address,uint256,bytes) selector = 0xb61d27f6
	if calldata[0] != 0xb6 || calldata[1] != 0x1d || calldata[2] != 0x27 || calldata[3] != 0xf6 {
		t.Errorf("selector = %x, want b61d27f6", calldata[:4])
	}
}
