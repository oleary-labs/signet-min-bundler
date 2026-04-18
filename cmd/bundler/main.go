// bundler is a minimal ERC-4337 v0.7 bundler for the Signet system.
//
// Usage:
//
//	bundler [--config bundler.toml]
//
// Environment variables:
//
//	BUNDLER_KEYSTORE_PASSWORD  Keystore decryption password (required)
//	BUNDLER_CONFIG             Path to bundler.toml (default: ./bundler.toml)
//	BUNDLER_LOG_LEVEL          debug|info|warn|error (default: info)
//	BUNDLER_DEV                Set to 1 for human-readable log output
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/oleary-labs/signet-min-bundler/internal/bundler"
	"github.com/oleary-labs/signet-min-bundler/internal/config"
	"github.com/oleary-labs/signet-min-bundler/internal/estimator"
	"github.com/oleary-labs/signet-min-bundler/internal/mempool"
	"github.com/oleary-labs/signet-min-bundler/internal/paymaster"
	"github.com/oleary-labs/signet-min-bundler/internal/rpc"
	"github.com/oleary-labs/signet-min-bundler/internal/signer"
	"github.com/oleary-labs/signet-min-bundler/internal/validator"
)

func main() {
	configPath := flag.String("config", "", "Path to bundler.toml")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "bundler: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	// 1. Resolve config path.
	if configPath == "" {
		configPath = os.Getenv("BUNDLER_CONFIG")
	}
	if configPath == "" {
		configPath = "./bundler.toml"
	}

	// 2. Load config.
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 3. Build logger.
	log, err := buildLogger()
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}
	defer log.Sync()

	log.Info("starting bundler",
		zap.String("listen", cfg.ListenAddr),
		zap.Uint64("chain_id", cfg.ChainID),
		zap.Int("entry_points", len(cfg.EntryPoints)))

	// 4. Open SQLite database.
	dbPath := config.ExpandPath(cfg.DbPath)
	repo, err := mempool.Open(dbPath, log.With(zap.String("component", "mempool")))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer repo.Close()

	// 5. Connect to Ethereum node.
	client, err := ethclient.Dial(cfg.RpcURL)
	if err != nil {
		return fmt.Errorf("connect to node: %w", err)
	}

	// 6. Load bundler key and check balance.
	keystorePath := config.ExpandPath(cfg.KeystorePath)
	signerLog := log.With(zap.String("component", "signer"))
	bSigner, err := signer.Load(keystorePath, cfg.ExpectedAddress, client, cfg.ChainID, signerLog)
	if err != nil {
		return fmt.Errorf("load signer: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := bSigner.CheckBalance(ctx); err != nil {
		return fmt.Errorf("balance check: %w", err)
	}

	// 7. Reconcile mempool (BEFORE nonce init).
	reconcileLog := log.With(zap.String("component", "reconcile"))
	if err := bundler.Reconcile(ctx, repo, client, cfg.EntryPoints[0], reconcileLog); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	// 8. Init signer nonce (AFTER reconcile).
	if err := bSigner.InitNonce(ctx); err != nil {
		return fmt.Errorf("init nonce: %w", err)
	}

	// 9. Build components.
	v := validator.New(
		cfg.AllowedPaymasters,
		cfg.MaxVerificationGas,
		cfg.MaxCallGas,
	)

	est := estimator.New(client)

	pm := paymaster.New(bSigner, client, cfg.AllowedPaymasters[0], cfg.ChainID)

	methods := rpc.NewMethods(
		rpc.MethodsConfig{
			EntryPoints: cfg.EntryPoints,
			ChainID:     cfg.ChainID,
		},
		v, repo, est, pm,
		log.With(zap.String("component", "rpc")),
	)
	rpcServer := rpc.NewServer(methods, log.With(zap.String("component", "rpc")))

	tickInterval := time.Duration(cfg.TickIntervalMs) * time.Millisecond
	// TODO: does this only really support one entry point?
	bundleLoop := bundler.NewLoop(
		repo, bSigner, client,
		cfg.EntryPoints[0],
		cfg.MaxBundleSize,
		tickInterval,
		log.With(zap.String("component", "bundler")),
	)

	receiptWatcher := bundler.NewWatcher(
		repo, client,
		cfg.EntryPoints[0],
		5*time.Second,
		log.With(zap.String("component", "watcher")),
	)

	pruner := mempool.NewPruner(
		repo, cfg.PendingTtlMs, cfg.RetentionMs,
		log.With(zap.String("component", "pruner")),
	)

	// 10. Start everything.
	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: rpcServer.Handler(),
	}

	go func() {
		log.Info("RPC server listening", zap.String("addr", cfg.ListenAddr))
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("RPC server error", zap.Error(err))
		}
	}()
	go bundleLoop.Run(ctx)
	go receiptWatcher.Run(ctx)
	go pruner.Run(ctx)

	// 11. Wait for shutdown signal.
	<-ctx.Done()
	log.Info("shutting down")

	// Graceful HTTP shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdownCtx)

	return nil
}

func buildLogger() (*zap.Logger, error) {
	dev := os.Getenv("BUNDLER_DEV") == "1"
	if dev {
		cfg := zap.NewDevelopmentConfig()
		// Only show stack traces on DPanic+, not every Warn/Error.
		return cfg.Build(zap.AddStacktrace(zapcore.DPanicLevel))
	}

	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	level := os.Getenv("BUNDLER_LOG_LEVEL")
	switch level {
	case "debug":
		cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "warn":
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		cfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	return cfg.Build()
}
