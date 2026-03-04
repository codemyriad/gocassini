#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CRATE_DIR="$SCRIPT_DIR/compose-rs"
BIN="$CRATE_DIR/target/release/compose-rs"

if [[ ! -x "$BIN" || "$CRATE_DIR/Cargo.toml" -nt "$BIN" || "$CRATE_DIR/src/main.rs" -nt "$BIN" ]]; then
  cargo +stable build --release --manifest-path "$CRATE_DIR/Cargo.toml"
fi

exec "$BIN" "$@"
