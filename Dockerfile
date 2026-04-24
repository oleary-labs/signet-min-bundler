# ---- Build stage: Go binary + extract toolchain versions ----
FROM golang:1.26-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /app/bundler ./cmd/bundler

# Extract nargo and bb versions from the signet-circuits metadata so the
# tools stage installs exactly the versions the embedded artifacts expect.
RUN METADATA=$(find $(go env GOMODCACHE) -path '*/signet-circuits/packages/go@*/artifacts/jwt_auth/metadata.json' | head -1) && \
    grep -o '"nargo": *"[^"]*"' "$METADATA" | cut -d'"' -f4 > /tmp/nargo_version && \
    grep -o '"bb": *"[^"]*"' "$METADATA" | head -1 | cut -d'"' -f4 > /tmp/bb_version

# ---- Tools stage: install nargo + bb ----
FROM debian:bookworm-slim AS tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Versions are extracted from the signet-circuits module metadata in the build stage.
COPY --from=build /tmp/nargo_version /tmp/bb_version /tmp/

# Install nargo.
RUN NARGO_VERSION=$(cat /tmp/nargo_version) && \
    ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ]; then ARCH="aarch64"; else ARCH="x86_64"; fi && \
    curl -sL "https://github.com/noir-lang/noir/releases/download/v${NARGO_VERSION}/nargo-${ARCH}-unknown-linux-gnu.tar.gz" \
    | tar -xzC /usr/local/bin

# Install bb.
RUN BB_VERSION=$(cat /tmp/bb_version) && \
    ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then ARCH="arm64"; else ARCH="amd64"; fi && \
    curl -sL "https://github.com/AztecProtocol/aztec-packages/releases/download/v${BB_VERSION}/barretenberg-${ARCH}-linux.tar.gz" \
    | tar -xzC /usr/local/bin

# ---- Final runtime image ----
# bb 3.0+ requires GLIBC 2.38+ — bookworm only has 2.36.
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libstdc++6 libgomp1 git \
    && rm -rf /var/lib/apt/lists/*

# Copy nargo + bb from the tools stage.
COPY --from=tools /usr/local/bin/nargo /usr/local/bin/nargo
COPY --from=tools /usr/local/bin/bb /usr/local/bin/bb

COPY --from=build /app/bundler /app/bundler
COPY bundler.docker.toml /app/bundler.toml
COPY scripts/docker-entrypoint.sh /app/entrypoint.sh

WORKDIR /app
ENTRYPOINT ["/app/entrypoint.sh"]
