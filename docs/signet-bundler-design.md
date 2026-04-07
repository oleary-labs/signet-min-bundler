# Signet Bundler — Design Specification

**Status:** Final draft — ready for implementation
**Version:** 1.0
**Date:** April 2026

| Field | Value |
|---|---|
| Language | Go |
| Protocol | ERC-4337 v0.7 (PackedUserOperation) |
| Wallet | oleary-labs/signet-wallet (Kernel v3 + FROSTValidator) |
| Scope | Single-operator bundler, no distributed mempool |

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [Configuration](#3-configuration)
4. [Core Types](#4-core-types)
5. [RPC Interface](#5-rpc-interface)
6. [Allowlist Validator](#6-allowlist-validator)
7. [Mempool](#7-mempool)
8. [Startup Reconciliation](#8-startup-reconciliation)
9. [Bundling Loop](#9-bundling-loop)
10. [Receipt Watcher](#10-receipt-watcher)
11. [Gas Estimation](#11-gas-estimation)
12. [Hot Key Management](#12-hot-key-management)
13. [Mempool Lifecycle](#13-mempool-lifecycle)
14. [Logging](#14-logging)
15. [Startup Sequence](#15-startup-sequence)
16. [Dependencies](#16-dependencies)
17. [Ported Code from send-userop](#17-ported-code-from-send-userop)
18. [Open Items](#18-open-items)

---

## 1. Overview

This document specifies a minimal ERC-4337 v0.7 bundler for the Signet system. The bundler accepts UserOperations from Signet wallets, validates them against a static allowlist, stores them in a local SQLite database, and submits them as `handleOps` bundles to the EntryPoint contract on behalf of a hot bundler EOA.

**Key design constraints:**

- No distributed mempool. Operations are held in local SQLite only.
- No EVM simulation. Allowlist policy replaces the standard reputation/simulation system.
- Single known wallet type. All wallets are Kernel v3 accounts authenticated by `FROSTValidator`. Gas constants are derived analytically rather than estimated per-op.
- Single operator. One hot key submits all bundles.

---

## 2. Architecture

### 2.1 Component overview

| Component | Package | Responsibility |
|---|---|---|
| RPC server | `internal/rpc` | JSON-RPC HTTP handler; decodes and dispatches the five required ERC-4337 methods |
| Allowlist validator | `internal/validator` | Stateless policy gate; checks entry point, targets, paymasters, and gas caps before mempool insertion |
| Mempool | `internal/mempool` | SQLite-backed op store; manages state transitions and dedup |
| Bundling loop | `internal/bundler` | Periodic tick; selects pending ops, builds `handleOps` calldata, submits via signer |
| Signer | `internal/signer` | Hot key management; nonce tracking, `eth_sendRawTransaction`, balance monitoring |
| Receipt watcher | `internal/bundler` | Polls for bundle tx receipts; parses `UserOperationEvent` and `FailedOp`; updates mempool |
| Gas estimator | `internal/estimator` | Implements `eth_estimateUserOperationGas` without simulation |
| Config | `internal/config` | TOML config loading and validation |

### 2.2 Request lifecycle

```
Client
  │
  │  eth_sendUserOperation(op, entryPoint)
  ▼
RPC handler
  │  1. Decode wire format (split fields → PackedUserOp)
  │  2. Validate entry point
  │  3. AllowlistValidator.Validate(op)  ← targets, paymaster, gas caps
  │  4. Dedup check against live mempool (sender+nonce)
  │  5. repo.Insert(op, hash)
  │  return userOpHash
  │
Bundling loop (every tick)
  │  1. repo.ClaimForBundling(hashes)  ← atomic status transition
  │  2. Build handleOps calldata
  │  3. eth_estimateGas on bundle tx
  │  4. signer.SignAndSubmit(calldata)
  │  5. repo.UpdateStatus(hashes, submitted, txHash)
  │
Receipt watcher
  │  1. Poll eth_getTransactionReceipt
  │  2. Parse UserOperationEvent logs
  │  3. repo.UpdateStatus per op (confirmed / failed)
  └─ On FailedOp revert: mark offending op failed, reset rest to pending
```

### 2.3 Package layout

```
bundler/
  cmd/
    bundler/main.go          startup, signal handling, wires components
    keygen/main.go           one-shot keystore generation tool
  internal/
    config/config.go         BundlerConfig struct, TOML loading
    core/
      userop.go              PackedUserOp, UserOperationRPC types
      hash.go                computeUserOpHash (ported from send-userop)
      calldata.go            decodeSignetExecute, target extraction
      pack.go                packUint128s, packBigInts, padLeft32, helpers
    validator/
      allowlist.go           AllowlistValidator
    mempool/
      db.go                  SQLite repository (schema, migrations)
      mempool.go             Insert, Replace, GetPending, ClaimForBundling
      reconcile.go           startup reconciliation
      prune.go               TTL expiry, history retention
    bundler/
      loop.go                bundling tick loop
      submitter.go           BundlerSigner: nonce management, submit
      receipt.go             UserOperationEvent parsing, FailedOp decoding
    estimator/
      gas.go                 eth_estimateUserOperationGas
    rpc/
      server.go              net/http JSON-RPC dispatcher
      methods.go             eth_sendUserOperation, eth_estimateUserOperationGas,
                             eth_getUserOperationByHash, eth_getUserOperationReceipt,
                             eth_supportedEntryPoints, eth_chainId
      types.go               wire-format structs, RpcError
    signer/
      keystore.go            keystore load/decrypt, BundlerSigner, balance check
```

---

## 3. Configuration

### 3.1 TOML schema

Configuration is loaded from a TOML file. No secrets are stored in config; the keystore password is sourced from the environment or stdin at startup.

```toml
# bundler.toml

# ── Network ───────────────────────────────────────────────────────────────
rpcUrl  = "https://mainnet.infura.io/v3/<key>"
chainId = 1

# ── Protocol ──────────────────────────────────────────────────────────────
entryPoints = [
  "0x0000000071727De22E5E9d8BAf0edAc6f37da032"  # EntryPoint v0.7
]

# ── Policy ────────────────────────────────────────────────────────────────
allowedTargets = [
  "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",  # USDC
  "0xdAC17F958D2ee523a2206206994597C13D831ec7",  # USDT
]

# Empty list = accept ops with no paymaster.
# Listed addresses = only those paymasters are accepted when a paymaster is present.
allowedPaymasters = [
  "0x...",
]

# ── Gas bounds (sanity caps, not simulation) ──────────────────────────────
maxVerificationGas = 50_000
maxCallGas         = 500_000
maxBundleSize      = 10

# ── Mempool lifecycle ─────────────────────────────────────────────────────
pendingTtlMs  = 1_800_000    # 30 minutes
retentionMs   = 604_800_000  # 7 days

# ── Hot key ───────────────────────────────────────────────────────────────
keystorePath    = "~/.bundler/keystore.json"
expectedAddress = "0x..."   # optional; verified on startup

# ── Storage ───────────────────────────────────────────────────────────────
dbPath = "~/.bundler/bundler.db"
```

### 3.2 Go config struct

```go
type BundlerConfig struct {
    RpcURL             string
    ChainID            uint64
    EntryPoints        []common.Address
    AllowedTargets     []common.Address
    AllowedPaymasters  []common.Address
    MaxVerificationGas uint64
    MaxCallGas         uint64
    MaxBundleSize      int
    PendingTtlMs       int64
    RetentionMs        int64
    KeystorePath       string
    ExpectedAddress    *common.Address  // nil = no check
    DbPath             string
}
```

### 3.3 Environment variables

| Variable | Purpose |
|---|---|
| `BUNDLER_KEYSTORE_PASSWORD` | Keystore decryption password. If unset, bundler prompts stdin at startup. |
| `BUNDLER_CONFIG` | Path to `bundler.toml`. Default: `./bundler.toml` |
| `BUNDLER_LOG_LEVEL` | `debug` \| `info` \| `warn` \| `error`. Default: `info` |
| `BUNDLER_DEV` | Set to `1` for human-readable log output (zap development mode) |

---

## 4. Core Types

### 4.1 UserOperation representations

Two structs represent a UserOperation at different stages of processing. Conversion between them is performed by `core.FromRPC()`.

#### UserOperationRPC — wire format (JSON-RPC input/output)

```go
// UserOperationRPC is the ERC-4337 v0.7 JSON-RPC wire format.
// Fields are split (not packed) as sent by clients.
type UserOperationRPC struct {
    Sender               string `json:"sender"`
    Nonce                string `json:"nonce"`
    Factory              string `json:"factory,omitempty"`
    FactoryData          string `json:"factoryData,omitempty"`
    CallData             string `json:"callData"`
    CallGasLimit         string `json:"callGasLimit"`
    VerificationGasLimit string `json:"verificationGasLimit"`
    PreVerificationGas   string `json:"preVerificationGas"`
    MaxFeePerGas         string `json:"maxFeePerGas"`
    MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas"`
    Paymaster            string `json:"paymaster,omitempty"`
    PaymasterData        string `json:"paymasterData,omitempty"`
    Signature            string `json:"signature"`
}
```

#### PackedUserOp — internal/on-chain format

```go
// PackedUserOp is the ERC-4337 v0.7 on-chain struct.
// Used for hash computation, handleOps calldata, and DB storage.
type PackedUserOp struct {
    Sender             common.Address
    Nonce              *big.Int
    InitCode           []byte   // factory (20 bytes) + factoryData
    CallData           []byte
    AccountGasLimits   [32]byte // verificationGasLimit (hi 128) || callGasLimit (lo 128)
    PreVerificationGas *big.Int
    GasFees            [32]byte // maxPriorityFeePerGas (hi 128) || maxFeePerGas (lo 128)
    PaymasterAndData   []byte
    Signature          []byte   // 65 bytes: R.x(32) || z(32) || v(1)
}
```

#### FromRPC conversion

`core.FromRPC(op UserOperationRPC) (*PackedUserOp, error)` re-assembles the packed fields:

- `AccountGasLimits`: `packBigInts(verificationGasLimit, callGasLimit)`
- `GasFees`: `packBigInts(maxPriorityFeePerGas, maxFeePerGas)`
- `InitCode`: `hex(factory) + hex(factoryData)` — empty if factory is omitted
- `PaymasterAndData`: `hex(paymaster) + hex(paymasterData)` — empty if paymaster is omitted

### 4.2 userOpHash computation

The hash implementation is ported directly from `send-userop/main.go computeUserOpHash`. This is the canonical implementation; the bundler must produce identical output.

> **Note:** The outer encode field order is `innerHash, entryPoint, chainId` — confirmed from the Go source. Double-check any port against the send-userop test vectors.

```
// Inner: 8 × 32-byte words, all dynamic fields pre-hashed
inner[0:32]   = leftPad32(op.Sender)           // address
inner[32:64]  = leftPad32(op.Nonce)             // uint256
inner[64:96]  = keccak256(op.InitCode)          // bytes32
inner[96:128] = keccak256(op.CallData)          // bytes32
inner[128:160]= op.AccountGasLimits             // bytes32 (already packed)
inner[160:192]= leftPad32(op.PreVerificationGas)
inner[192:224]= op.GasFees                      // bytes32 (already packed)
inner[224:256]= keccak256(op.PaymasterAndData)  // bytes32

innerHash = keccak256(inner)   // 256 bytes → 32 bytes

// Outer: 3 × 32-byte words
outer[0:32]  = innerHash
outer[32:64] = leftPad32(entryPoint)  // address: 12 zero bytes + 20 addr bytes
outer[64:96] = leftPad32(chainID)

userOpHash = keccak256(outer)
```

---

## 5. RPC Interface

### 5.1 Transport

Plain HTTP JSON-RPC on a configurable port (default `4337`). No WebSocket, no authentication. Intended for trusted internal network use only — the operator is responsible for network-level access control.

### 5.2 Required methods

| Method | Description |
|---|---|
| `eth_sendUserOperation(op, entryPoint)` | Primary intake. Returns `userOpHash` on success. |
| `eth_estimateUserOperationGas(op, entryPoint)` | Returns `preVerificationGas`, `verificationGasLimit`, `callGasLimit`. |
| `eth_getUserOperationByHash(hash)` | Returns op + metadata, or `null` if not found. |
| `eth_getUserOperationReceipt(hash)` | Returns receipt once confirmed; `null` if pending. |
| `eth_supportedEntryPoints()` | Returns `config.EntryPoints` as a hex string array. |
| `eth_chainId()` | Returns `config.ChainID` as a hex string. |

### 5.3 Error codes

| Code | Constant | When used |
|---|---|---|
| `-32600` | `ErrInvalidRequest` | Malformed JSON-RPC envelope |
| `-32601` | `ErrMethodNotFound` | Unknown method name |
| `-32602` | `ErrInvalidParams` | Missing or malformed fields (wrong type, bad hex, etc.) |
| `-32521` | `ErrOpRejected` | Allowlist rejection: target, paymaster, entry point, or gas cap violation |
| `-32521` | `ErrOpRejected` | Structural rejection: signature wrong length, nonce already used |

```go
type RpcError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
}

func (e *RpcError) Error() string { return e.Message }

var (
    ErrInvalidRequest = func(msg string) *RpcError { return &RpcError{-32600, msg} }
    ErrMethodNotFound = func(msg string) *RpcError { return &RpcError{-32601, msg} }
    ErrInvalidParams  = func(msg string) *RpcError { return &RpcError{-32602, msg} }
    ErrOpRejected     = func(msg string) *RpcError { return &RpcError{-32521, msg} }
)
```

### 5.4 eth_sendUserOperation flow

```go
func handleSendUserOperation(op UserOperationRPC, entryPoint string) (string, *RpcError) {
    // 1. Validate entry point is configured
    if !config.IsAllowedEntryPoint(entryPoint) {
        return "", ErrOpRejected("unsupported entry point")
    }

    // 2. Convert wire format to packed struct
    packed, err := core.FromRPC(op)
    if err != nil { return "", ErrInvalidParams(err.Error()) }

    // 3. Validate signature length (must be exactly 65 bytes)
    if len(packed.Signature) != 65 {
        return "", ErrOpRejected("signature must be 65 bytes")
    }

    // 4. Allowlist validation (synchronous, no I/O)
    if err := validator.Validate(packed); err != nil {
        return "", ErrOpRejected(err.Error())
    }

    // 5. Compute hash
    hash := core.ComputeUserOpHash(packed, config.EntryPoints[0], config.ChainID)

    // 6. Dedup / replacement check; insert into mempool
    if err := mempool.Insert(packed, hash); err != nil {
        return "", ErrOpRejected(err.Error())
    }

    return hash.Hex(), nil
}
```

---

## 6. Allowlist Validator

### 6.1 Validation sequence

All checks are synchronous and stateless. The full validation completes before the op touches the database.

```go
func (v *AllowlistValidator) Validate(op *core.PackedUserOp) error {
    if err := v.checkGasLimits(op); err != nil { return err }
    if err := v.checkPaymaster(op); err != nil { return err }
    if err := v.checkTargets(op);   err != nil { return err }
    return nil
}
```

### 6.2 Gas cap check

```go
func (v *AllowlistValidator) checkGasLimits(op *core.PackedUserOp) error {
    verifGas := new(big.Int).SetBytes(op.AccountGasLimits[0:16])
    callGas  := new(big.Int).SetBytes(op.AccountGasLimits[16:32])

    if verifGas.Uint64() > v.cfg.MaxVerificationGas {
        return fmt.Errorf("verificationGasLimit %d exceeds max %d",
            verifGas, v.cfg.MaxVerificationGas)
    }
    if callGas.Uint64() > v.cfg.MaxCallGas {
        return fmt.Errorf("callGasLimit %d exceeds max %d",
            callGas, v.cfg.MaxCallGas)
    }
    return nil
}
```

### 6.3 Paymaster check

If `PaymasterAndData` is empty or shorter than 20 bytes, the op has no paymaster and is accepted regardless of `config.AllowedPaymasters`. If a paymaster address is present, it must be in the allowlist.

```go
func (v *AllowlistValidator) checkPaymaster(op *core.PackedUserOp) error {
    if len(op.PaymasterAndData) < 20 {
        return nil  // no paymaster — always accepted
    }
    pm := common.BytesToAddress(op.PaymasterAndData[:20])
    for _, allowed := range v.cfg.AllowedPaymasters {
        if pm == allowed { return nil }
    }
    return fmt.Errorf("paymaster %s not in allowed list", pm.Hex())
}
```

### 6.4 Target extraction and check

`SignetAccount.execute` uses the ABI signature `execute(address,uint256,bytes)`. The target is the first argument. Self-calls (target == `op.Sender`) are always permitted — they are required for validator management and key rotation ops.

```go
// Selector: keccak256("execute(address,uint256,bytes)")[:4] = 0xb61d27f6
var executeSelector = []byte{0xb6, 0x1d, 0x27, 0xf6}

func decodeSignetExecuteTarget(callData []byte) (common.Address, error) {
    if len(callData) < 4+32 {
        return common.Address{}, errors.New("callData too short")
    }
    if !bytes.Equal(callData[:4], executeSelector) {
        return common.Address{}, fmt.Errorf("unrecognised selector %x", callData[:4])
    }
    // address is the last 20 bytes of the first 32-byte ABI word
    return common.BytesToAddress(callData[4+12 : 4+32]), nil
}

func (v *AllowlistValidator) checkTargets(op *core.PackedUserOp) error {
    target, err := decodeSignetExecuteTarget(op.CallData)
    if err != nil { return err }

    // Self-calls always allowed (key rotation, validator management)
    if target == op.Sender { return nil }

    for _, allowed := range v.cfg.AllowedTargets {
        if target == allowed { return nil }
    }
    return fmt.Errorf("target %s not in allowed list", target.Hex())
}
```

---

## 7. Mempool

### 7.1 SQLite schema

```sql
CREATE TABLE IF NOT EXISTS user_operations (
  -- identity
  hash                 TEXT PRIMARY KEY,   -- userOpHash hex, 0x-prefixed
  sender               TEXT NOT NULL,
  nonce                TEXT NOT NULL,       -- hex string (uint256 is too large for INTEGER)

  -- packed op fields (v0.7 PackedUserOperation)
  init_code            TEXT NOT NULL,       -- hex or '0x'
  call_data            TEXT NOT NULL,
  account_gas_limits   TEXT NOT NULL,       -- bytes32 hex
  pre_verification_gas TEXT NOT NULL,
  gas_fees             TEXT NOT NULL,       -- bytes32 hex
  paymaster_and_data   TEXT NOT NULL,
  signature            TEXT NOT NULL,       -- 65-byte FROST sig, hex

  -- state machine
  status               TEXT NOT NULL DEFAULT 'pending',
  -- pending | bundling | submitted | confirmed | failed | replaced

  -- submission tracking
  bundle_tx_hash       TEXT,
  bundle_index         INTEGER,    -- position in handleOps array; used for FailedOp matching
  submitted_at         INTEGER,    -- unix ms
  block_number         INTEGER,
  revert_reason        TEXT,
  actual_gas_cost      TEXT,       -- from UserOperationEvent; stored for calibration

  -- timestamps
  received_at          INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

CREATE INDEX idx_userop_status ON user_operations(status);
CREATE INDEX idx_userop_sender ON user_operations(sender);

-- Prevents two live ops with the same sender+nonce.
-- Terminal states are exempt so history is preserved.
CREATE UNIQUE INDEX idx_userop_sender_nonce ON user_operations(sender, nonce)
  WHERE status NOT IN ('confirmed', 'failed', 'replaced');
```

### 7.2 State machine

| Transition | Trigger | Notes |
|---|---|---|
| → `pending` | `eth_sendUserOperation` accepted | Initial state on insert |
| `pending` → `bundling` | Bundling loop claims op | Atomic; prevents double-claim |
| `bundling` → `submitted` | `eth_sendRawTransaction` succeeds | `bundle_tx_hash` and `bundle_index` set |
| `bundling` → `pending` | Startup reconciliation | Process died before submission |
| `submitted` → `confirmed` | Receipt watcher: `UserOperationEvent` `success=true` | `block_number` and `actual_gas_cost` set |
| `submitted` → `failed` | Receipt watcher: `UserOperationEvent` `success=false` | `revert_reason` set from `UserOperationRevertReason` log |
| `submitted` → `pending` | Bundle tx reverted (not `FailedOp`) | EntryPoint-level revert; all ops in bundle reset |
| `pending` → `failed` | `FailedOp` at this op's `bundle_index` | Offending op only; others reset to pending |
| `pending` → `failed` | TTL expiry | `revert_reason = 'ttl_expired'` |
| `pending` → `replaced` | Higher-fee op for same sender+nonce | Old op marked replaced atomically with new insert |

### 7.3 Repository interface

```go
type Repository interface {
    Insert(op *core.PackedUserOp, hash common.Hash) error
    Replace(oldHash common.Hash, newOp *core.PackedUserOp, newHash common.Hash) error
    GetPending(limit int) ([]*StoredOp, error)
    ClaimForBundling(hashes []common.Hash) error  // atomic status transition
    UpdateStatus(hash common.Hash, status Status, extra *StatusExtra) error
    GetByHash(hash common.Hash) (*StoredOp, error)
    GetConfirmedByHash(hash common.Hash) (*StoredOp, error)
    GetByBundleTx(txHash common.Hash) ([]*StoredOp, error)
    MarkTtlExpired(cutoffMs int64) (int, error)
    PruneHistory(cutoffMs int64) (int, error)
    Close() error
}

type StatusExtra struct {
    BundleTxHash  *common.Hash
    BundleIndex   *int
    SubmittedAt   *int64
    BlockNumber   *uint64
    RevertReason  *string
    ActualGasCost *big.Int
}
```

### 7.4 Replacement policy

When a new op arrives with the same sender+nonce as a live (non-terminal) op, it is treated as a replacement if and only if its `maxFeePerGas` is at least 10% higher than the existing op. The replacement is atomic: the old op is marked `replaced` and the new op is inserted in a single SQLite transaction.

```go
minReplacementFee := new(big.Int).Mul(existingMaxFee, big.NewInt(110))
minReplacementFee.Div(minReplacementFee, big.NewInt(100))
if newMaxFee.Cmp(minReplacementFee) < 0 {
    return ErrOpRejected("replacement fee too low: need 10% increase")
}
```

### 7.5 WAL mode

The SQLite database is opened with `journal_mode=WAL` and `foreign_keys=ON`. WAL mode allows concurrent reads from the RPC handler while the bundling loop holds a write transaction, which is important for low-latency receipt responses.

---

## 8. Startup Reconciliation

On every startup, before the bundling loop begins, the reconciler ensures no op is left in an intermediate state from a previous process run.

```go
func Reconcile(repo Repository, client *ethclient.Client, log *zap.Logger) error {

    // Step 1: Reset all 'bundling' ops to 'pending'.
    // These were claimed but never submitted — process died in the gap.
    n, err := repo.ResetBundlingToPending()
    if err != nil { return err }
    if n > 0 {
        log.Warn("reconciled stuck ops", zap.Int("bundling_reset", n))
    }

    // Step 2: Check all 'submitted' ops against the node.
    submitted, err := repo.GetByStatus(StatusSubmitted)
    for _, op := range submitted {
        receipt, err := client.TransactionReceipt(ctx, *op.BundleTxHash)
        if err != nil { continue }  // tx not found; leave as submitted

        if receipt.Status == 1 {
            // Bundle landed; parse logs for per-op outcomes
            outcomes := parseHandleOpsReceipt(receipt)
            for opHash, outcome := range outcomes {
                status := StatusConfirmed
                if !outcome.Success { status = StatusFailed }
                repo.UpdateStatus(opHash, status, &StatusExtra{
                    BlockNumber:   &receipt.BlockNumber,
                    RevertReason:  outcome.RevertReason,
                    ActualGasCost: outcome.ActualGasCost,
                })
            }
        } else {
            // Bundle tx reverted; decode FailedOp and reset appropriately
            bundleOps, _ := repo.GetByBundleTx(*op.BundleTxHash)
            handleBundleRevert(receipt, bundleOps, repo, log)
        }
    }
    return nil
}
```

---

## 9. Bundling Loop

### 9.1 Tick behaviour

The bundling loop runs on a configurable interval (default: every new block, polled via `eth_blockNumber`). Each tick is sequential — a new tick does not begin until the previous one completes, including bundle submission.

```go
func (l *BundlingLoop) Run(ctx context.Context) {
    ticker := time.NewTicker(l.cfg.TickInterval)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            if err := l.tick(ctx); err != nil {
                l.log.Error("bundling tick failed", zap.Error(err))
            }
        }
    }
}

func (l *BundlingLoop) tick(ctx context.Context) error {
    // 1. Check signer balance before doing any work
    if err := l.signer.CheckBalance(ctx); err != nil { return err }

    // 2. Fetch up to maxBundleSize pending ops (FIFO by received_at)
    ops, err := l.repo.GetPending(l.cfg.MaxBundleSize)
    if len(ops) == 0 { return nil }

    // 3. Claim ops atomically
    hashes := hashesOf(ops)
    if err := l.repo.ClaimForBundling(hashes); err != nil { return err }

    // 4. Build handleOps(ops[], beneficiary) calldata
    calldata, err := buildHandleOpsCalldata(ops, l.signer.Address())

    // 5. Estimate gas for the bundle tx
    gasLimit, err := l.client.EstimateGas(ctx, ethereum.CallMsg{
        To: &l.cfg.EntryPoint, Data: calldata,
    })
    gasLimit = gasLimit * 12 / 10  // 20% buffer

    // 6. Submit
    txHash, err := l.signer.SignAndSubmit(ctx, l.cfg.EntryPoint, calldata, gasLimit)

    // 7. Update status for all ops in the bundle
    now := time.Now().UnixMilli()
    for i, op := range ops {
        idx := i
        l.repo.UpdateStatus(op.Hash, StatusSubmitted, &StatusExtra{
            BundleTxHash: &txHash,
            BundleIndex:  &idx,
            SubmittedAt:  &now,
        })
    }
    return nil
}
```

### 9.2 FailedOp handling

When a bundle tx reverts, the revert data is decoded to determine whether it is a `FailedOp` error (one specific op caused the revert) or an unexpected revert (EntryPoint bug, gas exhaustion, etc.).

```go
// FailedOp(uint256 opIndex, string reason) selector
var failedOpSelector = crypto.Keccak256([]byte("FailedOp(uint256,string)"))[:4]

func handleBundleRevert(receipt *types.Receipt, ops []*StoredOp,
                        repo Repository, log *zap.Logger) {
    revertData := receipt.RevertReason

    if len(revertData) >= 4 && bytes.Equal(revertData[:4], failedOpSelector) {
        opIndex, reason := decodeFailedOp(revertData)
        log.Error("bundle reverted with FailedOp",
            zap.Int("failed_index", opIndex), zap.String("reason", reason))

        for _, op := range ops {
            if op.BundleIndex != nil && *op.BundleIndex == opIndex {
                repo.UpdateStatus(op.Hash, StatusFailed,
                    &StatusExtra{RevertReason: &reason})
            } else {
                repo.UpdateStatus(op.Hash, StatusPending, nil)
            }
        }
    } else {
        // Unknown revert — reset everything
        log.Error("bundle reverted unexpectedly",
            zap.String("revert_data", hexutil.Encode(revertData)))
        for _, op := range ops {
            repo.UpdateStatus(op.Hash, StatusPending, nil)
        }
    }
}
```

---

## 10. Receipt Watcher

### 10.1 Polling

The receipt watcher polls for submitted bundle transactions at a configurable interval (default 5 seconds). It fetches receipts for all ops in `submitted` state and processes them in a single pass.

### 10.2 UserOperationEvent parsing

The EntryPoint emits one event per op. Both event types must be decoded.

```go
// EntryPoint v0.7 events:
//
// UserOperationEvent(bytes32 userOpHash, address sender, address paymaster,
//   uint256 nonce, bool success, uint256 actualGasCost, uint256 actualGasUsed)
//
// UserOperationRevertReason(bytes32 userOpHash, address sender,
//   uint256 nonce, bytes revertReason)

func parseHandleOpsReceipt(receipt *types.Receipt) map[common.Hash]*OpOutcome {
    outcomes := map[common.Hash]*OpOutcome{}
    for _, log := range receipt.Logs {
        if log.Address != entryPointAddr { continue }
        switch log.Topics[0] {
        case userOperationEventTopic:
            hash    := log.Topics[1]
            success := log.Topics[4][31] == 1
            gasCost := new(big.Int).SetBytes(log.Data[0:32])
            outcomes[hash] = &OpOutcome{Success: success, ActualGasCost: gasCost}
        case userOperationRevertReasonTopic:
            hash   := log.Topics[1]
            reason := string(log.Data[64:])  // ABI-decoded bytes
            if o, ok := outcomes[hash]; ok { o.RevertReason = &reason }
        }
    }
    return outcomes
}
```

---

## 11. Gas Estimation

`eth_estimateUserOperationGas` is implemented without EVM simulation. All three returned values are computed analytically or via `eth_estimateGas` on the inner call only, not on the full EntryPoint flow.

### 11.1 preVerificationGas

Computed statically from the packed UserOperation's byte content. EIP-2028 calldata costs: 4 gas per zero byte, 16 gas per nonzero byte.

```go
func calcPreVerificationGas(op *core.PackedUserOp) *big.Int {
    packed := abiEncodePackedOp(op)  // as it appears in handleOps calldata
    var calldataCost uint64
    for _, b := range packed {
        if b == 0 {
            calldataCost += 4
        } else {
            calldataCost += 16
        }
    }
    // Fixed overhead: base tx share (21000) + EntryPoint loop per-op (11000)
    return new(big.Int).SetUint64(calldataCost + 21_000 + 11_000)
}
```

> **Note:** The FROST signature is always exactly 65 bytes: `R.x(32) || z(32) || v(1)`. Unlike ECDSA there is no length variance. Calldata cost is deterministic.

### 11.2 verificationGasLimit

Fixed constant derived analytically from the Signet wallet architecture. No simulation required.

| Component | Gas | Source |
|---|---|---|
| `FROSTValidator.validateUserOp` (ecrecover + storage reads) | 12,000 | Design doc upper bound |
| Kernel v3 nonce decode + validation routing overhead | 8,000 | Kernel v3 source |
| `TokenWhitelistHook.preCheck` (allowlist check + storage write) | 6,000 | Conservative estimate |
| EntryPoint `validateUserOp` wrapper | 5,000 | EntryPoint v0.7 source |
| **Total `verificationGasLimit` constant** | **31,000** | |

> **Note:** This constant must be validated empirically against a test deployment on a fork before going to production. Run a representative batch of ops and confirm actual verification gas stays below 31,000.

### 11.3 callGasLimit

Estimated via `eth_estimateGas` on the inner call, bypassing the EntryPoint. Kernel dispatch overhead is already captured in `verificationGasLimit`.

```go
func estimateCallGas(ctx context.Context, op *core.PackedUserOp,
                     client *ethclient.Client) (*big.Int, error) {
    target, value, innerData, err := core.DecodeSignetExecute(op.CallData)
    if err != nil { return nil, err }

    msg := ethereum.CallMsg{
        From:  op.Sender,  // msg.sender for access control checks
        To:    &target,
        Value: value,
        Data:  innerData,
    }

    // For counterfactual wallets (initCode set), inject proxy bytecode via state override.
    if len(op.InitCode) > 0 {
        return estimateCallGasCounterfactual(ctx, op, target, value, innerData)
    }

    gas, err := client.EstimateGas(ctx, msg)
    if err != nil { return nil, err }
    // 30% buffer for Kernel dispatch wrapper overhead
    return new(big.Int).SetUint64(gas * 130 / 100), nil
}
```

### 11.4 Counterfactual wallets

If `op.InitCode` is non-empty, the wallet is not yet deployed. `eth_estimateGas` from a nonexistent sender would fail. State overrides inject the Kernel v3 proxy bytecode at `op.Sender` so estimation runs as if the wallet exists. The proxy bytecode is deterministic from the factory and is known at compile time.

```go
// eth_call with state override — not available directly via ethclient.
// Must call the RPC method manually.
func estimateCallGasCounterfactual(ctx context.Context, op *core.PackedUserOp,
                                   target common.Address, value *big.Int,
                                   data []byte) (*big.Int, error) {
    overrides := map[string]interface{}{
        op.Sender.Hex(): map[string]interface{}{
            "code": hexutil.Encode(kernelV3ProxyBytecode),
        },
    }
    // raw eth_call with stateOverride parameter
    // ...
}
```

### 11.5 Dummy signature for estimation requests

Clients calling `eth_estimateUserOperationGas` send a dummy signature (65 zero bytes). The bundler accepts this during estimation — it does not verify the signature. The signature field must still be present and exactly 65 bytes for the struct to be well-formed.

---

## 12. Hot Key Management

### 12.1 Key generation

The `keygen` command generates a new secp256k1 key, encrypts it as an EIP-55 keystore v3 file (AES-128-CTR, scrypt KDF, `N=1<<17`), and writes it to disk with mode `0600`. Run once; never store the raw key.

```
$ bundler keygen --out ~/.bundler/keystore.json
Set keystore password: ****
Confirm password:      ****
Bundler address: 0x...
Keystore written to: /home/user/.bundler/keystore.json
Fund this address before starting the bundler.
```

Implementation uses go-ethereum's `accounts/keystore` package: `keystore.NewKey()` + `keystore.EncryptKey()`.

### 12.2 Keystore loading at startup

```go
func LoadBundlerKey(cfg *BundlerConfig, client *ethclient.Client) (*BundlerSigner, error) {
    encrypted, err := os.ReadFile(cfg.KeystorePath)

    // Password: env var for non-interactive (CI/systemd), stdin for interactive
    password := os.Getenv("BUNDLER_KEYSTORE_PASSWORD")
    if password == "" {
        password, err = promptPassword("Bundler keystore password: ")
    }

    key, err := keystore.DecryptKey(encrypted, password)
    if err != nil { return nil, fmt.Errorf("keystore decrypt failed: %w", err) }

    // Optional address verification against config
    if cfg.ExpectedAddress != nil && key.Address != *cfg.ExpectedAddress {
        return nil, fmt.Errorf("keystore address %s != expected %s",
            key.Address, *cfg.ExpectedAddress)
    }

    return &BundlerSigner{key: key, client: client}, nil
}
```

### 12.3 BundlerSigner

```go
type BundlerSigner struct {
    key          *keystore.Key
    client       *ethclient.Client
    mu           sync.Mutex
    pendingNonce uint64  // tracked locally; NOT fetched per submission
}

// InitNonce must be called once after reconciliation, before the bundling loop.
func (s *BundlerSigner) InitNonce(ctx context.Context) error {
    n, err := s.client.PendingNonceAt(ctx, s.key.Address)
    s.pendingNonce = n
    return err
}

// SignAndSubmit is single-flight: the mutex is held for the full duration of submission.
func (s *BundlerSigner) SignAndSubmit(ctx context.Context,
        to common.Address, data []byte, gasLimit uint64) (common.Hash, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    tx := types.NewTx(&types.DynamicFeeTx{
        ChainID:   s.chainID,
        Nonce:     s.pendingNonce,
        GasTipCap: maxPriorityFee,
        GasFeeCap: maxFee,
        Gas:       gasLimit,
        To:        &to,
        Data:      data,
    })
    signed, err := types.SignTx(tx, signer, s.key.PrivateKey)
    if err != nil { return common.Hash{}, err }

    if err := s.client.SendTransaction(ctx, signed); err != nil {
        if isNonceTooLow(err) { s.InitNonce(ctx) }  // resync on nonce error
        return common.Hash{}, err
    }

    s.pendingNonce++  // increment only after successful submission
    return signed.Hash(), nil
}
```

### 12.4 Balance monitoring

| Threshold | Action |
|---|---|
| < 0.05 ETH | Log `Warn` at the start of each bundling tick |
| < 0.01 ETH | Return error from `CheckBalance()`; bundling tick aborts; log `Error` |

### 12.5 Filesystem layout

```
~/.bundler/
  keystore.json     # encrypted, chmod 600, never commit to VCS
  bundler.db        # sqlite, chmod 600, never commit to VCS
  bundler.toml      # config, no secrets, safe to commit
```

---

## 13. Mempool Lifecycle

### 13.1 TTL pruning

A background goroutine runs every 5 minutes and performs two operations:

- Pending ops older than `pendingTtlMs` (default 30 minutes) are marked `failed` with `revert_reason = 'ttl_expired'`.
- Terminal ops (`confirmed`, `failed`, `replaced`) with `updated_at` older than `retentionMs` (default 7 days) are deleted.

```go
func (p *Pruner) Run(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            cutoff := time.Now().UnixMilli() - p.cfg.PendingTtlMs
            expired, _ := p.repo.MarkTtlExpired(cutoff)
            if expired > 0 {
                p.log.Info("expired pending ops", zap.Int("count", expired))
            }
            retentionCutoff := time.Now().UnixMilli() - p.cfg.RetentionMs
            deleted, _ := p.repo.PruneHistory(retentionCutoff)
            if deleted > 0 {
                p.log.Info("pruned history", zap.Int("count", deleted))
            }
        }
    }
}
```

---

## 14. Logging

### 14.1 Library

`go.uber.org/zap` v1.27+. Use `*zap.Logger` (strongly typed fields) throughout — not `SugaredLogger`. JSON output in production; human-readable console output in development (`BUNDLER_DEV=1`).

### 14.2 Logger construction

```go
func BuildLogger(dev bool) (*zap.Logger, error) {
    if dev { return zap.NewDevelopment() }
    cfg := zap.NewProductionConfig()
    cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder  // readable timestamps
    return cfg.Build()
}
```

Each component receives a child logger pre-scoped with its name. No global logger; pass explicitly.

```go
rpcLogger     := logger.With(zap.String("component", "rpc"))
mempoolLogger := logger.With(zap.String("component", "mempool"))
bundlerLogger := logger.With(zap.String("component", "bundler"))
signerLogger  := logger.With(zap.String("component", "signer"))
watcherLogger := logger.With(zap.String("component", "watcher"))
```

### 14.3 Standard field names

| Field key | Type | Used for |
|---|---|---|
| `hash` | string | userOpHash — primary identifier across all log sites |
| `sender` | string | `op.Sender.Hex()` |
| `tx` | string | bundle transaction hash |
| `block` | uint64 | block number |
| `status` | string | op status string |
| `count` | int | batch sizes, counts |
| `gas_cost` | string | `actualGasCost.String()` from `UserOperationEvent` |
| `reason` | string | revert reason or rejection message |
| `component` | string | set via `With()` at construction, not per call |

### 14.4 Required log sites

| Event | Level | Fields |
|---|---|---|
| Op received | `Info` | `hash`, `sender`, `paymaster` |
| Op rejected | `Warn` | `hash`, `reason` |
| Op replaced | `Info` | `old_hash`, `new_hash`, `sender` |
| Bundle submitted | `Info` | `tx`, `op_count`, `hashes[]` |
| Op confirmed | `Info` | `hash`, `tx`, `block`, `gas_cost` |
| Op failed on-chain | `Warn` | `hash`, `tx`, `block`, `reason` |
| Bundle `FailedOp` revert | `Error` | `tx`, `failed_index`, `reason`, `reset_count` |
| Bundle unknown revert | `Error` | `tx`, `revert_data` |
| Balance low | `Warn` | `address`, `balance_eth`, `threshold_eth` |
| Balance critical | `Error` | `address`, `balance_eth` |
| Reconciled stuck ops | `Warn` | `bundling_reset`, `submitted_checked` |
| Ops TTL expired | `Info` | `count` |
| History pruned | `Info` | `count` |

---

## 15. Startup Sequence

The `main` function wires components together in this order. The ordering is load-bearing: each step depends on the previous.

```go
func main() {
    // 1. Load config
    cfg, err := config.Load(configPath)

    // 2. Build logger
    log, err := BuildLogger(cfg.Dev)
    defer log.Sync()

    // 3. Open SQLite database
    repo, err := mempool.Open(cfg.DbPath, log)

    // 4. Connect to Ethereum node
    client, err := ethclient.Dial(cfg.RpcURL)

    // 5. Load bundler key and check balance
    signer, err := signer.Load(cfg, client, log)
    signer.CheckBalance(ctx)  // fatal if critically low

    // 6. Reconcile mempool (BEFORE nonce init)
    reconcile.Run(repo, client, log)

    // 7. Init signer nonce (AFTER reconcile, so pending tx count is accurate)
    signer.InitNonce(ctx)

    // 8. Build components
    validator      := validator.New(cfg)
    estimator      := estimator.New(cfg, client)
    rpcServer      := rpc.New(cfg, validator, repo, estimator, log)
    bundleLoop     := bundler.NewLoop(cfg, repo, signer, client, log)
    receiptWatcher := bundler.NewWatcher(repo, client, log)
    pruner         := mempool.NewPruner(cfg, repo, log)

    // 9. Start everything
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
    defer stop()

    go rpcServer.ListenAndServe(cfg.ListenAddr)
    go bundleLoop.Run(ctx)
    go receiptWatcher.Run(ctx)
    go pruner.Run(ctx)

    // 10. Wait for shutdown signal
    <-ctx.Done()
    log.Info("shutting down")
    bundleLoop.WaitForDrain(5 * time.Second)
    repo.Close()
    _ = log.Sync()  // explicit flush; ignore error (expected on stdout/stderr on Linux)
}
```

---

## 16. Dependencies

| Module | Version | Purpose |
|---|---|---|
| `github.com/ethereum/go-ethereum` | v1.15+ | `ethclient`, `accounts/keystore`, `types`, `crypto`, ABI encoding |
| `modernc.org/sqlite` | v1.34+ | Pure-Go SQLite driver (no cgo; enables static binaries) |
| `github.com/BurntSushi/toml` | v1.4+ | TOML config loading |
| `go.uber.org/zap` | v1.27+ | Structured leveled logging |

> `modernc.org/sqlite` is preferred over `mattn/go-sqlite3` to avoid cgo. If cgo is acceptable, `mattn/go-sqlite3` is also correct. WAL mode works on both.

---

## 17. Ported Code from send-userop

The following functions in `cmd/send-userop/main.go` are correct and tested. Port them verbatim into `internal/core/` without modification. Do not reimplement.

| Function | Destination | Notes |
|---|---|---|
| `computeUserOpHash` | `core/hash.go` | Canonical hash implementation. Ground truth. |
| `packUint128s` | `core/pack.go` | Pack two `uint64`s into `bytes32` |
| `packBigInts` | `core/pack.go` | Pack two `*big.Int` as `uint128 hi\|\|lo` |
| `keccak256` | `core/pack.go` | `sha3.NewLegacyKeccak256` wrapper |
| `padLeft32` | `core/pack.go` | Left-pad `[]byte` to 32 bytes |
| `decodeHex` | `core/pack.go` | `0x`-prefixed hex decode |
| `bytesToHex` | `core/pack.go` | Bytes to `0x`-prefixed hex |
| `bigToHex` | `core/pack.go` | `*big.Int` to `0x`-prefixed minimal hex |
| `hexToBigInt` | `core/pack.go` | `0x` hex to `*big.Int` |
| `parseBigHex` | `core/pack.go` | Alias of `hexToBigInt` |
| `buildExecuteCalldata` | `core/calldata.go` | ABI-encode `execute(address,uint256,bytes)` |
| `abiEncodeFactoryArgs` | `core/calldata.go` | Used in counterfactual gas estimation |
| `buildInitCode` | `core/calldata.go` | `factory \|\| createAccount selector \|\| args` |
| `pubKeyToAddress` | `core/crypto.go` | Derive Ethereum address from 33-byte compressed pubkey |
| `expandMessageXMD` | `core/crypto.go` | RFC 9380 `expand_message_xmd` (SHA-256) |
| `frostChallengeGo` | `core/crypto.go` | FROST RFC 9591 challenge hash |
| `verifyFrostSig` | `core/crypto.go` | Local FROST sig verification (useful for tests) |

> The target extraction function `decodeSignetExecuteTarget` (section 6.4) is new and not present in `send-userop`. It must be implemented for the allowlist validator.

---

## 18. Design Review Notes

The following issues were identified during review and must be addressed during implementation.

### 18.1 Bugs to fix

| # | Issue | Section | Severity |
|---|---|---|---|
| R1 | **`UserOperationEvent` topic parsing is wrong.** Only `userOpHash` is indexed (Topics[1]). `sender`, `paymaster`, `nonce`, `success`, `actualGasCost`, `actualGasUsed` are all ABI-encoded in `log.Data`, not in topics. The current `parseHandleOpsReceipt` will silently produce wrong results. | §10.2 | Critical |
| R2 | **`parseHandleOpsReceipt` revert reason slice.** `log.Data[64:]` assumes ABI-encoded bytes with offset+length prefix but does not read the actual length field — can over-read or panic on short data. | §10.2 | High |
| R3 | **Replacement race with `bundling` status.** §7.4 allows replacement of any non-terminal op, but `bundling` and `submitted` are non-terminal. A replacement arriving while an op is claimed for bundling would replace it mid-submission. `Replace()` must reject ops in `bundling` or `submitted` status. | §7.4 | High |
| R4 | **`eth_estimateGas` missing `From` field.** The `CallMsg` in the bundling loop (§9.1 step 5) omits `From: l.signer.Address()`. Without it, the estimate runs from the zero address, which may produce incorrect gas results or fail access-control checks on the EntryPoint. | §9.1 | Medium |
| R5 | **No recovery on bundle submission failure.** If `SignAndSubmit` fails (network error, not nonce), ops stay in `bundling` status until the next process restart triggers reconciliation. The tick should reset ops to `pending` on submission failure. | §9.1 | Medium |

### 18.2 Spec gaps to fill

| # | Gap | Section |
|---|---|---|
| G1 | **`ListenAddr` missing from config.** §15 references `cfg.ListenAddr` but it is absent from both the TOML schema (§3.1) and Go struct (§3.2). Add `listenAddr = ":4337"` with a default. | §3 |
| G2 | **`TickInterval` missing from config.** Used in the bundling loop but not in the TOML schema or Go struct. Open item #4 acknowledges this. Add `tickIntervalMs` to config. | §3, §9 |
| G3 | **Bundle tx gas pricing not sourced.** `SignAndSubmit` references `maxPriorityFee` and `maxFee` but they are not fetched or configured anywhere. Needs an `eth_maxPriorityFeePerGas` call or config fields. | §12.3 |
| G4 | **`DecodeSignetExecute` not defined.** §11.3 `estimateCallGas` calls `core.DecodeSignetExecute(op.CallData)` returning `(target, value, innerData)`, but only `decodeSignetExecuteTarget` (returns target only) is specified in §6.4. The full decoder is needed for gas estimation. | §6.4, §11.3 |

### 18.3 Nonce resync concern

`InitNonce` uses `PendingNonceAt` which includes unconfirmed txs. After a `nonce too low` error, the original tx may have landed. If the bundling loop immediately retries, it could double-submit the same ops. Consider: (a) adding a short backoff after nonce resync, and (b) checking whether the resynced nonce implies the previous tx succeeded before re-bundling.

---

## 19. Open Items

The following items require resolution before or shortly after production deployment.

| # | Item | Priority |
|---|---|---|
| 1 | **Validate `verificationGasLimit` constant (31,000) against a test fork.** Run a representative batch of ops and confirm actual verification gas stays below this value. Adjust the constant if not. | High — before production |
| 2 | **Confirm `SignetAccount.executeBatch` ABI signature.** Section 6.4 decodes `execute(address,uint256,bytes)`. If batch ops are needed, the batch selector and calldata layout must be confirmed from `SignetAccount.sol` source. | Medium — if batch ops needed |
| 3 | **Obtain `kernelV3ProxyBytecode` constant for counterfactual estimation.** Required only for ops with `initCode` set. Retrieve from the deployed `KernelFactory`. | Medium — if counterfactual ops needed |
| 4 | **Make `TickInterval` configurable.** The bundling loop currently assumes a 12-second block time. Consider triggering on new-block events via `eth_subscribe` for tighter latency. | Low |

---

## 20. Implementation Plan

Bottom-up by package dependency. Each phase produces testable code before the next begins.

### Phase 1 — Skeleton + core types

- `go mod init`, add dependencies (go-ethereum, modernc.org/sqlite, BurntSushi/toml, zap)
- `internal/config` — `BundlerConfig` struct, TOML loading, validation, defaults (including `ListenAddr`, `TickInterval`, gap G1/G2)
- `internal/core/pack.go` — port helpers verbatim from send-userop (`padLeft32`, `keccak256`, `packBigInts`, `decodeHex`, `bytesToHex`, etc.)
- `internal/core/userop.go` — `PackedUserOp`, `UserOperationRPC`, `FromRPC()` conversion
- `internal/core/hash.go` — `ComputeUserOpHash` (port verbatim)
- `internal/core/calldata.go` — `decodeSignetExecuteTarget` + full `DecodeSignetExecute` (fixes gap G4), `buildHandleOpsCalldata`
- Unit tests for hash computation against send-userop test vectors

### Phase 2 — Mempool (SQLite)

- `internal/mempool/db.go` — schema creation, WAL mode, `Open()`/`Close()`
- `internal/mempool/mempool.go` — full `Repository` implementation: `Insert`, `Replace` (fix R3: reject bundling/submitted), `GetPending`, `ClaimForBundling`, `UpdateStatus`, `GetByHash`, `GetByBundleTx`
- `internal/mempool/prune.go` — `MarkTtlExpired`, `PruneHistory`, `Pruner.Run()`
- Tests: insert/dedup, replacement policy, state transitions, TTL expiry

### Phase 3 — Validator + Estimator

- `internal/validator/allowlist.go` — `AllowlistValidator` (gas caps, paymaster, targets)
- `internal/estimator/gas.go` — `calcPreVerificationGas`, `estimateCallGas` (with `From` field, fix R4), verification gas constant
- Tests: allowlist accept/reject cases, preVerificationGas calculation

### Phase 4 — Signer

- `internal/signer/keystore.go` — `LoadBundlerKey`, `BundlerSigner`, `InitNonce`, `SignAndSubmit` (with gas price fetching, fix G3), `CheckBalance`
- `cmd/keygen/main.go` — one-shot keystore generation tool

### Phase 5 — Bundler loop + Receipt watcher

- `internal/bundler/loop.go` — bundling tick with submission failure recovery (fix R5: reset to pending on error)
- `internal/bundler/receipt.go` — corrected `parseHandleOpsReceipt` (fix R1: parse from `log.Data`, fix R2: safe slice with length check), `handleBundleRevert`
- `internal/bundler/submitter.go` — `buildHandleOpsCalldata`, nonce resync backoff (§18.3)
- `internal/mempool/reconcile.go` — startup reconciliation

### Phase 6 — RPC server

- `internal/rpc/types.go` — `RpcError`, error constructors
- `internal/rpc/server.go` — JSON-RPC dispatcher over `net/http`
- `internal/rpc/methods.go` — all 6 methods
- Integration test: submit op via RPC, verify hash returned

### Phase 7 — Main + integration

- `cmd/bundler/main.go` — full startup sequence (§15)
- End-to-end test against a local fork (Anvil/Hardhat) if feasible
- Validate verification gas constant (open item #1)
