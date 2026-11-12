#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

# Keep the Go toolchain and module/build caches inside the project workspace.
export GOPATH="$ROOT/.gopath"
export GOMODCACHE="$ROOT/.gomodcache"
export GOCACHE="$ROOT/.gocache"
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.3}"

HOST="${HOST:-:8787}"
DB="${DB:-$ROOT/hornedviper.db}"

echo "Starting HornedViper on http://127.0.0.1${HOST#:} ..."
echo "Database: $DB"

exec go run ./cmd/hornedviper -serve "$HOST" -db "$DB"
