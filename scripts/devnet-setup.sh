#!/usr/bin/env bash
# devnet-setup.sh — bootstrap a local bundler against Anvil at localhost:8545.
#
# Prerequisites: anvil running, cast + forge (Foundry) on PATH.
# Idempotent: safe to re-run.

set -euo pipefail

ANVIL_RPC="${ANVIL_RPC:-http://localhost:8545}"
ANVIL_CHAIN_ID=31337
ENTRYPOINT_V07="0x0000000071727De22E5E9d8BAf0edAc6f37da032"

# Anvil account #0 — used to fund the bundler and deploy contracts.
ANVIL_FUNDER="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
ANVIL_FUNDER_KEY="0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

DEVNET_DIR=".devnet"
KEYSTORE_PATH="${DEVNET_DIR}/keystore.json"
DB_PATH="${DEVNET_DIR}/bundler.db"
CONFIG_PATH="${DEVNET_DIR}/bundler.toml"
BUNDLER_PASSWORD="devnet-insecure"

# ── Helpers ──────────────────────────────────────────────────────────────

info()  { printf "\033[1;34m==> %s\033[0m\n" "$*"; }
ok()    { printf "\033[1;32m  ✓ %s\033[0m\n" "$*"; }
warn()  { printf "\033[1;33m  ⚠ %s\033[0m\n" "$*"; }
die()   { printf "\033[1;31mERROR: %s\033[0m\n" "$*" >&2; exit 1; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "'$1' not found on PATH. Install Foundry: https://getfoundry.sh"
}

# ── Preflight ────────────────────────────────────────────────────────────

info "Checking prerequisites"

require_cmd cast
require_cmd forge
require_cmd go

# Check Anvil is reachable.
if ! cast chain-id --rpc-url "$ANVIL_RPC" >/dev/null 2>&1; then
    die "Anvil not reachable at $ANVIL_RPC. Start it with:  anvil"
fi
ok "Anvil running at $ANVIL_RPC (chain $ANVIL_CHAIN_ID)"

# ── EntryPoint v0.7 ─────────────────────────────────────────────────────

info "Checking EntryPoint v0.7 at $ENTRYPOINT_V07"

# The EntryPoint v0.7 constructor deploys a SenderCreator helper at a
# deterministic address. When we stamp bytecode via anvil_setCode the
# constructor never runs, so we must copy both contracts.
SENDER_CREATOR="0xEFC2c1444eBCC4Db75e7613d20C6a62fF67A167C"

EP_CODE=$(cast code "$ENTRYPOINT_V07" --rpc-url "$ANVIL_RPC" 2>/dev/null || echo "0x")
SC_CODE=$(cast code "$SENDER_CREATOR" --rpc-url "$ANVIL_RPC" 2>/dev/null || echo "0x")

if [ "$EP_CODE" = "0x" ] || [ -z "$EP_CODE" ] || [ "$SC_CODE" = "0x" ] || [ -z "$SC_CODE" ]; then
    info "EntryPoint or SenderCreator missing — copying from testnet"

    TESTNET_RPC="${TESTNET_RPC:-}"
    if [ -z "$TESTNET_RPC" ]; then
        warn "EntryPoint v0.7 not fully deployed and TESTNET_RPC not set."
        echo ""
        echo "  Set a Sepolia RPC so this script can copy the bytecode:"
        echo "    TESTNET_RPC=https://eth-sepolia.g.alchemy.com/v2/YOUR_KEY make devnet-setup"
        echo ""
        die "Cannot continue without EntryPoint v0.7"
    fi

    info "Fetching EntryPoint + SenderCreator bytecode from testnet"
    EP_RUNTIME=$(cast code "$ENTRYPOINT_V07" --rpc-url "$TESTNET_RPC" 2>/dev/null || echo "")
    if [ -z "$EP_RUNTIME" ] || [ "$EP_RUNTIME" = "0x" ]; then
        die "Could not fetch EntryPoint bytecode from $TESTNET_RPC"
    fi
    SC_RUNTIME=$(cast code "$SENDER_CREATOR" --rpc-url "$TESTNET_RPC" 2>/dev/null || echo "")
    if [ -z "$SC_RUNTIME" ] || [ "$SC_RUNTIME" = "0x" ]; then
        die "Could not fetch SenderCreator bytecode from $TESTNET_RPC"
    fi

    cast rpc anvil_setCode "$ENTRYPOINT_V07" "$EP_RUNTIME" --rpc-url "$ANVIL_RPC" >/dev/null
    cast rpc anvil_setCode "$SENDER_CREATOR" "$SC_RUNTIME" --rpc-url "$ANVIL_RPC" >/dev/null
    ok "EntryPoint + SenderCreator bytecode copied from testnet"
else
    ok "EntryPoint v0.7 + SenderCreator deployed"
fi

# ── Devnet directory ─────────────────────────────────────────────────────

mkdir -p "$DEVNET_DIR"

# ── Bundler keystore ─────────────────────────────────────────────────────

info "Setting up bundler keystore"

if [ -f "$KEYSTORE_PATH" ]; then
    ok "Keystore already exists at $KEYSTORE_PATH"
else
    export BUNDLER_KEYSTORE_PASSWORD="$BUNDLER_PASSWORD"
    go run ./cmd/keygen --out "$KEYSTORE_PATH"
    ok "Keystore created at $KEYSTORE_PATH"
fi

# Extract bundler address from keystore JSON.
BUNDLER_ADDR="0x$(cat "$KEYSTORE_PATH" | python3 -c "import sys,json; print(json.load(sys.stdin)['address'])" 2>/dev/null || echo "")"
if [ "$BUNDLER_ADDR" = "0x" ] || [ -z "$BUNDLER_ADDR" ]; then
    die "Could not extract address from keystore"
fi
ok "Bundler address: $BUNDLER_ADDR"

# ── Fund bundler ─────────────────────────────────────────────────────────

info "Funding bundler"

BALANCE=$(cast balance "$BUNDLER_ADDR" --rpc-url "$ANVIL_RPC" 2>/dev/null || echo "0")
# Fund with 100 ETH if balance is below 1 ETH.
MIN_BALANCE="1000000000000000000"  # 1 ETH in wei
if [ "$(echo "$BALANCE" | tr -d '[:space:]')" = "0" ] || [ "$(printf '%d' "$BALANCE" 2>/dev/null || echo 0)" -lt "$(printf '%d' "$MIN_BALANCE" 2>/dev/null || echo 0)" ]; then
    cast send "$BUNDLER_ADDR" --value 100ether \
        --from "$ANVIL_FUNDER" --private-key "$ANVIL_FUNDER_KEY" \
        --rpc-url "$ANVIL_RPC" >/dev/null
    ok "Funded with 100 ETH"
else
    ok "Already funded ($(cast from-wei "$BALANCE") ETH)"
fi

# ── SignetPaymaster ──────────────────────────────────────────────────────
#
# Note: this used to deploy the stock VerifyingPaymaster, but the bundler's
# paymaster service (internal/paymaster/paymaster.go) calls shouldSponsor()
# during ERC-7677 pm_getPaymasterData, and the stock contract doesn't have
# that function. SignetPaymaster wraps VerifyingPaymaster's signature logic
# and adds shouldSponsor() + target allowlisting tied to the factory.

info "Deploying SignetPaymaster"

# SignetPaymaster needs the signet-protocol SignetFactory address as its
# third constructor arg so shouldSponsor() can call factory.isGroup(target).
# Read it from the sibling repo's devnet env file.
SIGNET_PROTOCOL_ENV="${SIGNET_PROTOCOL_ENV:-../signet-protocol/devnet/.env}"
if [ ! -f "$SIGNET_PROTOCOL_ENV" ]; then
    die "signet-protocol devnet env not found at $SIGNET_PROTOCOL_ENV. Run devnet/start.sh in signet-protocol first, or set SIGNET_PROTOCOL_ENV=<path>."
fi
# shellcheck disable=SC1090
. "$SIGNET_PROTOCOL_ENV"
if [ -z "${FACTORY_ADDRESS:-}" ]; then
    die "FACTORY_ADDRESS not found in $SIGNET_PROTOCOL_ENV"
fi
ok "Using SignetFactory at $FACTORY_ADDRESS"

PAYMASTER_ADDR_FILE="${DEVNET_DIR}/paymaster.addr"
PAYMASTER_ADDR=""

# Check if we have a stored paymaster address that still has code.
if [ -f "$PAYMASTER_ADDR_FILE" ]; then
    STORED_PM=$(cat "$PAYMASTER_ADDR_FILE")
    PM_CODE=$(cast code "$STORED_PM" --rpc-url "$ANVIL_RPC" 2>/dev/null || echo "0x")
    if [ "$PM_CODE" != "0x" ] && [ -n "$PM_CODE" ]; then
        PAYMASTER_ADDR="$STORED_PM"
        ok "Paymaster already deployed at $PAYMASTER_ADDR"
    fi
fi

if [ -z "$PAYMASTER_ADDR" ]; then
    # Build the contracts.
    info "Compiling contracts"
    forge build --root contracts --silent 2>&1

    # Deploy with bundler address as the verifying signer and the
    # signet-protocol factory as the allowlist source.
    # Constructor: SignetPaymaster(IEntryPoint, address verifyingSigner, ISignetFactory)
    #
    # Foundry 1.5.x footguns (all three bite this one call):
    #   1. --broadcast is now required; without it forge create is a dry-run
    #      and returns no `deployedTo` field, so the python parse below fails.
    #   2. --constructor-args is variadic and greedily consumes everything
    #      after it — including --private-key, --rpc-url, etc. It MUST be the
    #      last flag on the command line.
    #   3. With --root contracts, paths resolve relative to contracts/. The
    #      SignetPaymaster source lives at contracts/src/, referenced as
    #      src/... from that root.
    #
    # If this call fails, drop the `2>/dev/null` to see forge's real error.
    DEPLOY_OUTPUT=$(forge create \
        --rpc-url "$ANVIL_RPC" \
        --root contracts \
        --broadcast \
        --private-key "$ANVIL_FUNDER_KEY" \
        --json \
        src/SignetPaymaster.sol:SignetPaymaster \
        --constructor-args "$ENTRYPOINT_V07" "$BUNDLER_ADDR" "$FACTORY_ADDRESS" 2>/dev/null)

    PAYMASTER_ADDR=$(echo "$DEPLOY_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['deployedTo'])")
    if [ -z "$PAYMASTER_ADDR" ] || [ "$PAYMASTER_ADDR" = "None" ]; then
        die "Failed to deploy SignetPaymaster"
    fi

    echo "$PAYMASTER_ADDR" > "$PAYMASTER_ADDR_FILE"
    ok "Paymaster deployed at $PAYMASTER_ADDR"

    # Transfer ownership to the funder (BasePaymaster.transferOwnership).
    # The deployer is already the owner, so this is a no-op, but keeps it explicit.

    # Deposit ETH into EntryPoint for the paymaster to cover gas.
    cast send "$ENTRYPOINT_V07" "depositTo(address)" "$PAYMASTER_ADDR" \
        --value 100ether \
        --from "$ANVIL_FUNDER" --private-key "$ANVIL_FUNDER_KEY" \
        --rpc-url "$ANVIL_RPC" >/dev/null
    ok "Deposited 100 ETH for paymaster on EntryPoint"
fi

# ── Write devnet config ─────────────────────────────────────────────────

info "Writing devnet config"

cat > "$CONFIG_PATH" <<EOF
# Auto-generated devnet config — do not commit.
rpcUrl  = "$ANVIL_RPC"
chainId = $ANVIL_CHAIN_ID

entryPoints = ["$ENTRYPOINT_V07"]

# SignetPaymaster deployed by devnet-setup.sh.
# Bundler key is the verifying signer — signs all sponsorship requests.
allowedPaymasters = ["$PAYMASTER_ADDR"]

maxVerificationGas = 1000000
maxCallGas         = 5000000
maxBundleSize      = 10

keystorePath = "$KEYSTORE_PATH"
dbPath       = "$DB_PATH"
listenAddr   = ":4337"

# Anvil mines on demand, so tick fast.
tickIntervalMs = 2000
pendingTtlMs   = 1800000
retentionMs    = 604800000

# ZK proof API — uses jwt_auth circuit from signet-protocol.
circuitDir  = "../signet-protocol/circuits/jwt_auth"
proverApiKey = "devnet-insecure"
EOF

ok "Config written to $CONFIG_PATH"

# ── Summary ──────────────────────────────────────────────────────────────

echo ""
info "Devnet ready!"
echo ""
echo "  Start the bundler:"
echo "    BUNDLER_KEYSTORE_PASSWORD=$BUNDLER_PASSWORD BUNDLER_DEV=1 go run ./cmd/bundler --config $CONFIG_PATH"
echo ""
echo "  Or via make:"
echo "    make devnet"
echo ""
echo "  RPC endpoint:  http://localhost:4337"
echo "  Bundler addr:  $BUNDLER_ADDR"
echo "  Paymaster:     $PAYMASTER_ADDR"
echo ""
