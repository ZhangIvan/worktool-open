#!/usr/bin/env bash
# Candidate only. No set -x; never echo environment/DSNs.
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
case "${1:-}" in
  --selftest) exec python3 "$SCRIPT_DIR/pg-integration-ci.py" --selftest ;;
  cleanup) exec python3 "$SCRIPT_DIR/pg-integration-ci.py" cleanup ;;
  run)
    trap 'python3 "$SCRIPT_DIR/pg-integration-ci.py" cleanup || exit 1' EXIT
    cd "$SCRIPT_DIR/../backend-go"
    python3 "$SCRIPT_DIR/pg-integration-ci.py" run
    ;;
  *) printf '%s\n' 'Expected run, cleanup or --selftest' >&2; exit 2 ;;
esac
