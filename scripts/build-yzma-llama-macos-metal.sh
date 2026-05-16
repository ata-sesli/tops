#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ -x "./tps" ]]; then
  ./tps local build-yzma-libs --backend metal "$@"
else
  go run ./cmd/tops local build-yzma-libs --backend metal "$@"
fi
