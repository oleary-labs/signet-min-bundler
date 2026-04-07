# signet-min-bundler

Minimal ERC-4337 v0.7 bundler for the [Signet](https://github.com/oleary-labs/signet-wallet) system.

Accepts UserOperations from Signet wallets (Kernel v3 + FROSTValidator), validates them against a static allowlist, stores them in a local SQLite database, and submits them as `handleOps` bundles to the EntryPoint contract.

## Design constraints

- **No distributed mempool** -- operations are held in local SQLite only.
- **No EVM simulation** -- allowlist policy replaces the standard reputation/simulation system.
- **Single known wallet type** -- all wallets are Kernel v3 + FROSTValidator. Gas constants are derived analytically.
- **Single operator** -- one hot key submits all bundles.

See [`docs/signet-bundler-design.md`](docs/signet-bundler-design.md) for the full design specification.

## Quick start

### 1. Build

```bash
go build -o bundler ./cmd/bundler
go build -o keygen ./cmd/keygen
```

### 2. Generate a bundler key

```bash
./keygen --out ~/.bundler/keystore.json
```

This creates an encrypted keystore file. Fund the printed address with ETH before starting the bundler.

### 3. Configure

```bash
cp bundler.example.toml bundler.toml
```

Edit `bundler.toml` with your RPC URL, chain ID, allowed targets, and other settings. See [Configuration](#configuration) below for all options.

### 4. Run

```bash
export BUNDLER_KEYSTORE_PASSWORD="your-password"
./bundler --config bundler.toml
```

The bundler starts an HTTP JSON-RPC server on `:4337` (default).

## Configuration

Configuration is loaded from a TOML file. No secrets are stored in config.

| Field | Default | Description |
|---|---|---|
| `rpcUrl` | *(required)* | Ethereum JSON-RPC endpoint |
| `chainId` | *(required)* | Chain ID |
| `entryPoints` | *(required)* | ERC-4337 EntryPoint v0.7 addresses |
| `allowedTargets` | `[]` | Contract addresses ops may call via `execute()`. Self-calls are always allowed. |
| `allowedPaymasters` | `[]` | Accepted paymaster addresses. Empty = only ops with no paymaster. |
| `maxVerificationGas` | *(required)* | Max `verificationGasLimit` per op |
| `maxCallGas` | *(required)* | Max `callGasLimit` per op |
| `maxBundleSize` | `10` | Max ops per `handleOps` bundle |
| `pendingTtlMs` | `1800000` | Pending op TTL before expiry (30 min) |
| `retentionMs` | `604800000` | Terminal op retention before pruning (7 days) |
| `keystorePath` | `~/.bundler/keystore.json` | Path to encrypted keystore |
| `expectedAddress` | *(optional)* | If set, verified against keystore on startup |
| `dbPath` | `~/.bundler/bundler.db` | SQLite database path |
| `listenAddr` | `:4337` | HTTP listen address |
| `tickIntervalMs` | `12000` | Bundling loop interval (12s) |

### Environment variables

| Variable | Description |
|---|---|
| `BUNDLER_KEYSTORE_PASSWORD` | Keystore decryption password (required) |
| `BUNDLER_CONFIG` | Path to `bundler.toml` (default: `./bundler.toml`) |
| `BUNDLER_LOG_LEVEL` | `debug` \| `info` \| `warn` \| `error` (default: `info`) |
| `BUNDLER_DEV` | Set to `1` for human-readable log output |

## JSON-RPC methods

| Method | Description |
|---|---|
| `eth_sendUserOperation(op, entryPoint)` | Submit a UserOperation. Returns `userOpHash`. |
| `eth_estimateUserOperationGas(op, entryPoint)` | Returns `preVerificationGas`, `verificationGasLimit`, `callGasLimit`. |
| `eth_getUserOperationByHash(hash)` | Returns op + metadata, or `null`. |
| `eth_getUserOperationReceipt(hash)` | Returns receipt once confirmed, or `null`. |
| `eth_supportedEntryPoints()` | Returns configured entry point addresses. |
| `eth_chainId()` | Returns chain ID as hex string. |

## Architecture

```
cmd/
  bundler/       main entrypoint, startup wiring
  keygen/        one-shot keystore generation
internal/
  config/        TOML config loading and validation
  core/          ERC-4337 types, hashing, calldata, FROST crypto
  validator/     allowlist policy checks (targets, paymasters, gas caps)
  mempool/       SQLite-backed op store with state machine
  estimator/     gas estimation without EVM simulation
  signer/        hot key management, nonce tracking, balance monitoring
  bundler/       bundling loop, receipt watcher, startup reconciliation
  rpc/           JSON-RPC HTTP server
```

### UserOperation state machine

```
                         ┌─────────┐
         send ──────────>│ pending │──── TTL expiry ───> failed
                         └────┬────┘
                              │ claim
                         ┌────▼────┐
              ┌──────────│bundling │──── crash ────────> pending (reconcile)
              │          └────┬────┘
              │               │ submit
              │          ┌────▼─────┐
              │          │submitted │──── receipt ─────> confirmed
              │          └────┬─────┘        │
              │               │              └────────> failed
              │               │ revert
              │               └───────────────────────> pending (retry)
              │
              └── estimate/submit fail ───────────────> pending (R5 fix)
```

## Development

```bash
go test ./...       # run all tests (101 tests)
go vet ./...        # lint
go build ./...      # build all
```

## License

[MIT](LICENSE)
