package core

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// execute(address,uint256,bytes) selector
var executeSelector = Keccak256([]byte("execute(address,uint256,bytes)"))[:4]

// DecodeSignetExecuteTarget extracts the target address from
// SignetAccount.execute(address,uint256,bytes) calldata.
func DecodeSignetExecuteTarget(callData []byte) (common.Address, error) {
	if len(callData) < 4+32 {
		return common.Address{}, errors.New("callData too short")
	}
	if !bytes.Equal(callData[:4], executeSelector) {
		return common.Address{}, fmt.Errorf("unrecognised selector %x", callData[:4])
	}
	// address is the last 20 bytes of the first 32-byte ABI word
	return common.BytesToAddress(callData[4+12 : 4+32]), nil
}

// DecodeSignetExecute decodes the full execute(address,uint256,bytes) calldata
// into target, value, and inner call data.
func DecodeSignetExecute(callData []byte) (common.Address, *big.Int, []byte, error) {
	if len(callData) < 4+32+32+32 {
		return common.Address{}, nil, nil, errors.New("callData too short for execute")
	}
	if !bytes.Equal(callData[:4], executeSelector) {
		return common.Address{}, nil, nil, fmt.Errorf("unrecognised selector %x", callData[:4])
	}

	// [4:36]   target address (left-padded to 32)
	target := common.BytesToAddress(callData[4+12 : 4+32])

	// [36:68]  value (uint256)
	value := new(big.Int).SetBytes(callData[4+32 : 4+64])

	// [68:100] offset to bytes data (should be 96 = 0x60)
	offset := new(big.Int).SetBytes(callData[4+64 : 4+96])
	dataStart := 4 + int(offset.Int64())

	if len(callData) < dataStart+32 {
		return common.Address{}, nil, nil, errors.New("callData too short for data length")
	}

	// Read length
	dataLen := new(big.Int).SetBytes(callData[dataStart : dataStart+32])
	dataBegin := dataStart + 32
	dataEnd := dataBegin + int(dataLen.Int64())

	if len(callData) < dataEnd {
		return common.Address{}, nil, nil, errors.New("callData too short for data payload")
	}

	innerData := make([]byte, dataLen.Int64())
	copy(innerData, callData[dataBegin:dataEnd])

	return target, value, innerData, nil
}

// BuildExecuteCalldata ABI-encodes SignetAccount.execute(address,uint256,bytes).
func BuildExecuteCalldata(to common.Address, value *big.Int, data []byte) []byte {
	dataPaddedLen := ((len(data) + 31) / 32) * 32
	buf := make([]byte, 4+32+32+32+32+dataPaddedLen)

	copy(buf[0:4], executeSelector)
	copy(buf[4+12:4+32], to[:])                                            // to, left-padded
	copy(buf[4+32:4+64], PadLeft32(value.Bytes()))                         // value
	buf[4+64+31] = 0x60                                                    // offset = 96
	copy(buf[4+96:4+128], PadLeft32(big.NewInt(int64(len(data))).Bytes())) // data length
	copy(buf[4+128:], data)                                                // data, zero-padded by make

	return buf
}

// BuildInitCode builds the ERC-4337 initCode field for first-time deployment:
//
//	factory (20 bytes) || createAccount.selector (4 bytes) || abi.encode(entryPoint, groupPublicKey, salt)
func BuildInitCode(factory, entryPoint common.Address, groupPubKey []byte, salt *big.Int) []byte {
	sel := Keccak256([]byte("createAccount(address,bytes,uint256)"))[:4]
	args := ABIEncodeFactoryArgs(entryPoint, groupPubKey, salt)
	out := make([]byte, 20+4+len(args))
	copy(out[0:20], factory[:])
	copy(out[20:24], sel)
	copy(out[24:], args)
	return out
}

// ABIEncodeFactoryArgs ABI-encodes (address entryPoint, bytes groupPublicKey, uint256 salt).
func ABIEncodeFactoryArgs(entryPoint common.Address, groupPubKey []byte, salt *big.Int) []byte {
	dataPaddedLen := ((len(groupPubKey) + 31) / 32) * 32
	buf := make([]byte, 4*32+dataPaddedLen)

	copy(buf[12:32], entryPoint[:])                                              // address, left-padded
	buf[32+31] = 0x60                                                            // offset = 96
	copy(buf[64:96], PadLeft32(salt.Bytes()))                                    // salt
	copy(buf[96:128], PadLeft32(big.NewInt(int64(len(groupPubKey))).Bytes()))     // bytes length
	copy(buf[128:], groupPubKey)                                                 // bytes data

	return buf
}
