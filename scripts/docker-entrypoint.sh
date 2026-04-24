#!/bin/sh
set -e

# Decode keystore from env var to disk if not already present.
if [ -n "$BUNDLER_KEYSTORE_B64" ] && [ ! -f /data/keystore.json ]; then
    mkdir -p /data
    echo "$BUNDLER_KEYSTORE_B64" | base64 -d > /data/keystore.json
fi

exec /app/bundler "$@"
