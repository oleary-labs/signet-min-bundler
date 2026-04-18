package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ethereum/go-ethereum/common"
)

// BundlerConfig holds all bundler configuration loaded from TOML.
type BundlerConfig struct {
	RpcURL             string           `toml:"rpcUrl"`
	ChainID            uint64           `toml:"chainId"`
	EntryPoints        []common.Address `toml:"entryPoints"`
	AllowedPaymasters  []common.Address `toml:"allowedPaymasters"`
	MaxVerificationGas uint64           `toml:"maxVerificationGas"`
	MaxCallGas         uint64           `toml:"maxCallGas"`
	MaxBundleSize      int              `toml:"maxBundleSize"`
	PendingTtlMs       int64            `toml:"pendingTtlMs"`
	RetentionMs        int64            `toml:"retentionMs"`
	KeystorePath       string           `toml:"keystorePath"`
	ExpectedAddress    *common.Address  `toml:"expectedAddress"`
	DbPath             string           `toml:"dbPath"`
	ListenAddr         string           `toml:"listenAddr"`
	TickIntervalMs     int64            `toml:"tickIntervalMs"`
}

// Load reads and validates a BundlerConfig from a TOML file.
func Load(path string) (*BundlerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg BundlerConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return &cfg, nil
}

func (c *BundlerConfig) applyDefaults() {
	if c.MaxBundleSize == 0 {
		c.MaxBundleSize = 10
	}
	if c.PendingTtlMs == 0 {
		c.PendingTtlMs = 1_800_000 // 30 minutes
	}
	if c.RetentionMs == 0 {
		c.RetentionMs = 604_800_000 // 7 days
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":4337"
	}
	if c.TickIntervalMs == 0 {
		c.TickIntervalMs = 12_000 // 12 seconds
	}
	if c.DbPath == "" {
		c.DbPath = "~/.bundler/bundler.db"
	}
	if c.KeystorePath == "" {
		c.KeystorePath = "~/.bundler/keystore.json"
	}
}

func (c *BundlerConfig) validate() error {
	if c.RpcURL == "" {
		return fmt.Errorf("rpcUrl is required")
	}
	if c.ChainID == 0 {
		return fmt.Errorf("chainId is required")
	}
	if len(c.EntryPoints) == 0 {
		return fmt.Errorf("at least one entryPoint is required")
	}
	if len(c.AllowedPaymasters) == 0 {
		return fmt.Errorf("at least one allowedPaymaster is required")
	}
	if c.MaxVerificationGas == 0 {
		return fmt.Errorf("maxVerificationGas is required")
	}
	if c.MaxCallGas == 0 {
		return fmt.Errorf("maxCallGas is required")
	}
	return nil
}

// IsAllowedEntryPoint returns true if addr is in the configured entry points.
func (c *BundlerConfig) IsAllowedEntryPoint(addr common.Address) bool {
	for _, ep := range c.EntryPoints {
		if ep == addr {
			return true
		}
	}
	return false
}

// ExpandPath replaces a leading ~ with the user's home directory.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home + path[1:]
	}
	return path
}
