package signer

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func generateTestKeystore(t *testing.T, password string) (string, common.Address) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "keystore.json")

	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	id, _ := uuid.NewRandom()
	ks := &keystore.Key{
		Id:         id,
		Address:    crypto.PubkeyToAddress(privateKey.PublicKey),
		PrivateKey: privateKey,
	}

	encrypted, err := keystore.EncryptKey(ks, password, keystore.LightScryptN, keystore.LightScryptP)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, encrypted, 0600); err != nil {
		t.Fatal(err)
	}

	return path, ks.Address
}

func TestLoadKeystore(t *testing.T) {
	password := "testpassword"
	path, expectedAddr := generateTestKeystore(t, password)

	t.Setenv("BUNDLER_KEYSTORE_PASSWORD", password)

	signer, err := Load(path, &expectedAddr, nil, 1, zap.NewNop())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if signer.Address() != expectedAddr {
		t.Errorf("address = %s, want %s", signer.Address().Hex(), expectedAddr.Hex())
	}
}

func TestLoadKeystoreWrongPassword(t *testing.T) {
	path, _ := generateTestKeystore(t, "correct")
	t.Setenv("BUNDLER_KEYSTORE_PASSWORD", "wrong")

	_, err := Load(path, nil, nil, 1, zap.NewNop())
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestLoadKeystoreAddressMismatch(t *testing.T) {
	password := "testpassword"
	path, _ := generateTestKeystore(t, password)
	t.Setenv("BUNDLER_KEYSTORE_PASSWORD", password)

	wrongAddr := common.HexToAddress("0xdeaddeaddeaddeaddeaddeaddeaddeaddeaddead")
	_, err := Load(path, &wrongAddr, nil, 1, zap.NewNop())
	if err == nil {
		t.Error("expected error for address mismatch")
	}
}

func TestLoadKeystoreNoPassword(t *testing.T) {
	path, _ := generateTestKeystore(t, "test")
	t.Setenv("BUNDLER_KEYSTORE_PASSWORD", "")

	_, err := Load(path, nil, nil, 1, zap.NewNop())
	if err == nil {
		t.Error("expected error when password env is empty")
	}
}

func TestWeiToEthString(t *testing.T) {
	// 1 ETH = 10^18 wei
	oneEth := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	got := weiToEthString(oneEth)
	if got != "1.000000" {
		t.Errorf("1 ETH = %s, want 1.000000", got)
	}

	// 0.05 ETH
	got = weiToEthString(warnThreshold)
	if got != "0.050000" {
		t.Errorf("0.05 ETH = %s, want 0.050000", got)
	}

	// 0
	got = weiToEthString(big.NewInt(0))
	if got != "0.000000" {
		t.Errorf("0 ETH = %s, want 0.000000", got)
	}
}

func TestIsNonceTooLow(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"nonce too low", true},
		{"Nonce Too Low", true},
		{"already known", true},
		{"some other error", false},
		{"insufficient funds", false},
	}
	for _, tc := range tests {
		got := isNonceTooLow(fmt.Errorf("%s", tc.msg))
		if got != tc.want {
			t.Errorf("isNonceTooLow(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
