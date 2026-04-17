.PHONY: build build-all test vet devnet-setup devnet clean-devnet

# ── Build ────────────────────────────────────────────────────────────────

build:
	go build -o bin/bundler ./cmd/bundler
	go build -o bin/keygen  ./cmd/keygen

build-all:
	go build ./...

# ── Test / Lint ──────────────────────────────────────────────────────────

test:
	go test ./...

vet:
	go vet ./...

# ── Devnet ───────────────────────────────────────────────────────────────

DEVNET_DIR   := .devnet
DEVNET_PASS  := devnet-insecure

devnet-setup:
	@./scripts/devnet-setup.sh

devnet: devnet-setup
	BUNDLER_KEYSTORE_PASSWORD=$(DEVNET_PASS) BUNDLER_DEV=1 \
		go run ./cmd/bundler --config $(DEVNET_DIR)/bundler.toml

clean-devnet:
	rm -rf $(DEVNET_DIR)
