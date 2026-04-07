// keygen generates a new secp256k1 key, encrypts it as an EIP-55 keystore v3
// file, and writes it to disk. Run once; never store the raw key.
//
// Usage:
//
//	keygen --out ~/.bundler/keystore.json
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"golang.org/x/term"
)

func main() {
	outPath := flag.String("out", "keystore.json", "Output path for the encrypted keystore file")
	flag.Parse()

	if err := run(*outPath); err != nil {
		fmt.Fprintf(os.Stderr, "keygen: %v\n", err)
		os.Exit(1)
	}
}

func run(outPath string) error {
	// Generate key.
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	address := crypto.PubkeyToAddress(privateKey.PublicKey)

	// Prompt for password.
	fmt.Fprint(os.Stderr, "Set keystore password: ")
	pass1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	fmt.Fprint(os.Stderr, "Confirm password:      ")
	pass2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	if string(pass1) != string(pass2) {
		return fmt.Errorf("passwords do not match")
	}
	if len(pass1) == 0 {
		return fmt.Errorf("password cannot be empty")
	}

	// Build keystore key struct.
	id, _ := uuid.NewRandom()
	key := &keystore.Key{
		Id:         id,
		Address:    address,
		PrivateKey: privateKey,
	}

	// Encrypt with scrypt N=1<<17 (standard strength).
	encrypted, err := keystore.EncryptKey(key, string(pass1), keystore.StandardScryptN, keystore.StandardScryptP)
	if err != nil {
		return fmt.Errorf("encrypt key: %w", err)
	}

	// Ensure output directory exists.
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Write with restrictive permissions.
	if err := os.WriteFile(outPath, encrypted, 0600); err != nil {
		return fmt.Errorf("write keystore: %w", err)
	}

	// Wipe private key from memory.
	_, _ = rand.Read(privateKey.D.Bytes())

	fmt.Fprintf(os.Stderr, "Bundler address: %s\n", address.Hex())
	fmt.Fprintf(os.Stderr, "Keystore written to: %s\n", outPath)
	fmt.Fprintln(os.Stderr, "Fund this address before starting the bundler.")
	return nil
}
