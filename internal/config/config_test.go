package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValid(t *testing.T) {
	content := `
rpcUrl  = "https://mainnet.infura.io/v3/test"
chainId = 1

entryPoints = [
  "0x0000000071727De22E5E9d8BAf0edAc6f37da032"
]

allowedTargets = [
  "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
]

maxVerificationGas = 50000
maxCallGas         = 500000
`
	path := writeTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.RpcURL != "https://mainnet.infura.io/v3/test" {
		t.Errorf("rpcUrl = %s", cfg.RpcURL)
	}
	if cfg.ChainID != 1 {
		t.Errorf("chainId = %d", cfg.ChainID)
	}
	if len(cfg.EntryPoints) != 1 {
		t.Fatalf("entryPoints len = %d", len(cfg.EntryPoints))
	}
	if len(cfg.AllowedTargets) != 1 {
		t.Errorf("allowedTargets len = %d", len(cfg.AllowedTargets))
	}

	// Defaults
	if cfg.MaxBundleSize != 10 {
		t.Errorf("maxBundleSize default = %d, want 10", cfg.MaxBundleSize)
	}
	if cfg.ListenAddr != ":4337" {
		t.Errorf("listenAddr default = %s, want :4337", cfg.ListenAddr)
	}
	if cfg.TickIntervalMs != 12_000 {
		t.Errorf("tickIntervalMs default = %d, want 12000", cfg.TickIntervalMs)
	}
	if cfg.PendingTtlMs != 1_800_000 {
		t.Errorf("pendingTtlMs default = %d", cfg.PendingTtlMs)
	}
	if cfg.RetentionMs != 604_800_000 {
		t.Errorf("retentionMs default = %d", cfg.RetentionMs)
	}
}

func TestLoadMissingRpcURL(t *testing.T) {
	content := `
chainId = 1
entryPoints = ["0x0000000071727De22E5E9d8BAf0edAc6f37da032"]
maxVerificationGas = 50000
maxCallGas = 500000
`
	path := writeTempConfig(t, content)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing rpcUrl")
	}
}

func TestLoadMissingChainID(t *testing.T) {
	content := `
rpcUrl = "http://localhost:8545"
entryPoints = ["0x0000000071727De22E5E9d8BAf0edAc6f37da032"]
maxVerificationGas = 50000
maxCallGas = 500000
`
	path := writeTempConfig(t, content)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing chainId")
	}
}

func TestLoadMissingEntryPoints(t *testing.T) {
	content := `
rpcUrl = "http://localhost:8545"
chainId = 1
maxVerificationGas = 50000
maxCallGas = 500000
`
	path := writeTempConfig(t, content)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing entryPoints")
	}
}

func TestIsAllowedEntryPoint(t *testing.T) {
	content := `
rpcUrl = "http://localhost:8545"
chainId = 1
entryPoints = [
  "0x0000000071727De22E5E9d8BAf0edAc6f37da032",
  "0x1111111111111111111111111111111111111111"
]
maxVerificationGas = 50000
maxCallGas = 500000
`
	path := writeTempConfig(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.IsAllowedEntryPoint(cfg.EntryPoints[0]) {
		t.Error("first entry point should be allowed")
	}
	if !cfg.IsAllowedEntryPoint(cfg.EntryPoints[1]) {
		t.Error("second entry point should be allowed")
	}

	unknown := cfg.EntryPoints[0]
	unknown[0] = 0xff
	if cfg.IsAllowedEntryPoint(unknown) {
		t.Error("unknown entry point should not be allowed")
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := ExpandPath("~/test/file.db")
	want := home + "/test/file.db"
	if got != want {
		t.Errorf("ExpandPath = %s, want %s", got, want)
	}

	abs := "/absolute/path"
	if ExpandPath(abs) != abs {
		t.Error("absolute path should pass through unchanged")
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bundler.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
