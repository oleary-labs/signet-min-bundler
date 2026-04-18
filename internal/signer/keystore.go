package signer

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"go.uber.org/zap"
)

var (
	// Balance thresholds in wei.
	warnThreshold     = new(big.Int).Mul(big.NewInt(50_000_000_000_000_000), big.NewInt(1))   // 0.05 ETH
	criticalThreshold = new(big.Int).Mul(big.NewInt(10_000_000_000_000_000), big.NewInt(1))   // 0.01 ETH
)

// BundlerSigner manages the hot key for bundle submission.
type BundlerSigner struct {
	key          *keystore.Key
	client       *ethclient.Client
	chainID      *big.Int
	mu           sync.Mutex
	pendingNonce uint64
	log          *zap.Logger
}

// Load decrypts the keystore and returns a BundlerSigner.
// Password is read from BUNDLER_KEYSTORE_PASSWORD env var, or from passwordReader if unset.
func Load(keystorePath string, expectedAddress *common.Address, client *ethclient.Client, chainID uint64, log *zap.Logger) (*BundlerSigner, error) {
	encrypted, err := os.ReadFile(keystorePath)
	if err != nil {
		return nil, fmt.Errorf("read keystore: %w", err)
	}

	password := os.Getenv("BUNDLER_KEYSTORE_PASSWORD")
	if password == "" {
		return nil, fmt.Errorf("BUNDLER_KEYSTORE_PASSWORD not set")
	}

	key, err := keystore.DecryptKey(encrypted, password)
	if err != nil {
		return nil, fmt.Errorf("keystore decrypt failed: %w", err)
	}

	if expectedAddress != nil && key.Address != *expectedAddress {
		return nil, fmt.Errorf("keystore address %s != expected %s",
			key.Address.Hex(), expectedAddress.Hex())
	}

	log.Info("loaded bundler key", zap.String("address", key.Address.Hex()))

	return &BundlerSigner{
		key:     key,
		client:  client,
		chainID: new(big.Int).SetUint64(chainID),
		log:     log,
	}, nil
}

// Address returns the bundler's Ethereum address.
func (s *BundlerSigner) Address() common.Address {
	return s.key.Address
}

// InitNonce fetches the pending nonce from the node.
// Must be called once after reconciliation, before the bundling loop.
func (s *BundlerSigner) InitNonce(ctx context.Context) error {
	n, err := s.client.PendingNonceAt(ctx, s.key.Address)
	if err != nil {
		return fmt.Errorf("fetch pending nonce: %w", err)
	}
	s.mu.Lock()
	s.pendingNonce = n
	s.mu.Unlock()
	s.log.Info("initialized nonce", zap.Uint64("nonce", n))
	return nil
}

// CheckBalance checks the bundler's ETH balance and returns an error if critically low.
func (s *BundlerSigner) CheckBalance(ctx context.Context) error {
	balance, err := s.client.BalanceAt(ctx, s.key.Address, nil)
	if err != nil {
		return fmt.Errorf("fetch balance: %w", err)
	}

	if balance.Cmp(criticalThreshold) < 0 {
		s.log.Error("bundler balance critically low",
			zap.String("address", s.key.Address.Hex()),
			zap.String("balance_eth", weiToEthString(balance)))
		return fmt.Errorf("bundler balance %s ETH below critical threshold 0.01 ETH",
			weiToEthString(balance))
	}

	if balance.Cmp(warnThreshold) < 0 {
		s.log.Warn("bundler balance low",
			zap.String("address", s.key.Address.Hex()),
			zap.String("balance_eth", weiToEthString(balance)),
			zap.String("threshold_eth", "0.05"))
	}

	return nil
}

// SignAndSubmit builds, signs, and submits a transaction.
// The mutex is held for the full duration to ensure nonce correctness.
func (s *BundlerSigner) SignAndSubmit(ctx context.Context, to common.Address, data []byte, gasLimit uint64) (common.Hash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Fetch current gas prices from the node (fix G3).
	gasTipCap, err := s.client.SuggestGasTipCap(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("suggest gas tip: %w", err)
	}

	head, err := s.client.HeaderByNumber(ctx, nil)
	if err != nil {
		return common.Hash{}, fmt.Errorf("fetch latest header: %w", err)
	}
	// gasFeeCap = 2 * baseFee + gasTipCap
	baseFee := head.BaseFee
	gasFeeCap := new(big.Int).Mul(baseFee, big.NewInt(2))
	gasFeeCap.Add(gasFeeCap, gasTipCap)

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   s.chainID,
		Nonce:     s.pendingNonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        &to,
		Data:      data,
	})

	signer := types.LatestSignerForChainID(s.chainID)
	signed, err := types.SignTx(tx, signer, s.key.PrivateKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sign tx: %w", err)
	}

	if err := s.client.SendTransaction(ctx, signed); err != nil {
		if isNonceTooLow(err) {
			s.log.Warn("nonce too low, resyncing", zap.Uint64("old_nonce", s.pendingNonce))
			// Resync nonce — release lock briefly is not needed since we hold it.
			if n, nerr := s.client.PendingNonceAt(ctx, s.key.Address); nerr == nil {
				s.pendingNonce = n
			}
		}
		return common.Hash{}, fmt.Errorf("send tx: %w", err)
	}

	txHash := signed.Hash()
	s.log.Info("submitted bundle tx",
		zap.String("tx", txHash.Hex()),
		zap.Uint64("nonce", s.pendingNonce),
		zap.Uint64("gas_limit", gasLimit))

	s.pendingNonce++
	return txHash, nil
}

// SignHash signs a 32-byte hash with the bundler's private key, returning
// a 65-byte signature (r(32) + s(32) + v(1)) with Ethereum's personal_sign
// prefix already applied by the caller if needed.
func (s *BundlerSigner) SignHash(hash []byte) ([]byte, error) {
	return ethSign(hash, s.key.PrivateKey)
}

func isNonceTooLow(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nonce too low") || strings.Contains(msg, "already known")
}

// ethSign signs a 32-byte hash with an ECDSA key, returning a 65-byte
// signature with v = 27 or 28 (Ethereum convention).
func ethSign(hash []byte, key *ecdsa.PrivateKey) ([]byte, error) {
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		return nil, err
	}
	// crypto.Sign returns v=0/1; Ethereum uses v=27/28.
	sig[64] += 27
	return sig, nil
}

func weiToEthString(wei *big.Int) string {
	eth := new(big.Float).SetInt(wei)
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	eth.Quo(eth, divisor)
	return eth.Text('f', 6)
}
