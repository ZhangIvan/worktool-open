#!/usr/bin/env bash
# Offline candidate. Python validates all arguments before nonce or resource I/O.
# No EXIT trap: only the supervisor that acquires owned state can clean it.
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$SCRIPT_DIR/authority-pg-ci.py" "$@"
