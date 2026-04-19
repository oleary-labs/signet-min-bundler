# ZK Prover API

## Overview

The bundler includes an optional ZK proof generation API at `POST /v1/prove`. This is **not a core ERC-4337 bundler function** — it is a convenience endpoint that co-locates server-side proof generation with the bundler to simplify the deployment topology.

The motivation is UX: generating ZK proofs client-side (in the browser via WASM) is slow and resource-intensive. By delegating proof generation to a trusted server, the UI gets faster auth flows with less client-side complexity. The bundler is already a trusted backend that the UI communicates with, so it's a natural place to host this.

This pattern is general-purpose — any application that needs server-side ZK proving can use this as a reference for how to wrap the Noir/Barretenberg toolchain in a Go HTTP API.

## What It Proves

The circuit proves that a JWT (from an OAuth provider like Google) is valid without revealing its full contents. Specifically, it proves:

1. The JWT's RSA-SHA256 signature is valid against the issuer's public key
2. The JWT contains specific claims (issuer, subject, audience, expiry, etc.)
3. The proof is bound to an ephemeral session public key (prevents replay)

This is the same proof the UI currently generates client-side. Moving it server-side doesn't change the trust model — the proof is still verified independently by signet-protocol nodes.

## API

### `POST /v1/prove`

Generates an UltraHonk ZK proof for a JWT.

**Headers:**
- `X-API-Key: <key>` — Required if `proverApiKey` is set in config.

**Request:**
```json
{
  "jwt": "eyJhbGciOiJSUzI1NiIs...",
  "session_pub": "02abcdef1234..."
}
```

| Field | Type | Description |
|-------|------|-------------|
| `jwt` | string | Raw JWT from OAuth provider (header.payload.signature) |
| `session_pub` | string | Hex-encoded 33-byte compressed secp256k1 public key |

**Response:**
```json
{
  "proof": "abcdef...",
  "sub": "user-123",
  "iss": "https://accounts.google.com",
  "exp": 1893456000,
  "aud": "app.example.com",
  "azp": "client-id",
  "jwks_modulus": "c4f2e8...",
  "session_pub": "02abcdef..."
}
```

| Field | Type | Description |
|-------|------|-------------|
| `proof` | string | Hex-encoded UltraHonk proof bytes (~2 KB) |
| `sub` | string | JWT subject claim |
| `iss` | string | JWT issuer |
| `exp` | number | JWT expiry (unix timestamp) |
| `aud` | string | JWT audience |
| `azp` | string | JWT authorized party |
| `jwks_modulus` | string | Hex-encoded RSA modulus from the issuer's JWKS |
| `session_pub` | string | Echoed back for convenience |

**Error response:**
```json
{
  "error": "proof generation failed: ..."
}
```

## How It Works

1. **Parse JWT** — extract header, payload (claims), and signature
2. **Fetch JWKS** — resolve the issuer's OIDC discovery endpoint, fetch the JSON Web Key Set, and find the RSA public key matching the JWT's `kid`
3. **Build witness** — encode the JWT data as circuit inputs:
   - RSA modulus, signature, and REDC parameter as 18 × 120-bit limbs
   - Signed data (`header.payload`) padded to 1024 bytes
   - Claims as BoundedVec byte arrays
   - Session public key (33 bytes)
4. **Generate witness** — run `nargo execute` to compute all intermediate wire values
5. **Generate proof** — run `bb prove` to produce the UltraHonk proof
6. **Return** proof bytes and public inputs

Steps 4–5 take ~2–3 seconds combined.

## Prerequisites

The prover requires two external tools:

- **[nargo](https://noir-lang.org/)** — Noir compiler and witness generator
- **[bb](https://github.com/AztecProtocol/aztec-packages)** — Barretenberg prover/verifier

Both are looked up on `PATH` first, then at their default install locations (`~/.nargo/bin/nargo` and `~/.bb/bb`).

The prover also requires a **pre-compiled circuit** — the `jwt_auth` circuit from the [signet-protocol](https://github.com/oleary-labs/signet-protocol) repository. The compiled artifact (`target/jwt_auth.json`) must exist at the configured `circuitDir`. To compile it:

```bash
cd ../signet-protocol/circuits/jwt_auth
nargo compile
```

## Configuration

Add to `bundler.toml`:

```toml
# Path to the jwt_auth circuit directory (must contain target/jwt_auth.json).
# If omitted, the prover API is disabled.
circuitDir = "../signet-protocol/circuits/jwt_auth"

# API key for the /v1/prove endpoint. If empty, no auth is required.
proverApiKey = "your-secret-key"
```

The prover is **opt-in** — if `circuitDir` is not set, the endpoint is not registered and the bundler operates as a pure ERC-4337 bundler.

## Limitations

- **Synchronous** — proof generation blocks the HTTP request for ~2–3 seconds. For high-throughput deployments, consider adding request queuing.
- **Requires nargo + bb** — these are external binaries, not embedded. The bundler logs a warning and disables the prover if they're not found.

## Concurrency

Each proof request uses a unique ID to isolate its files — per-request `Prover_<id>.toml`, witness (`target/<id>_witness.gz`), and proof output (`target/<id>_proof/`). All per-request files are cleaned up after the proof is generated (or on error). Multiple requests can run concurrently without interference.
