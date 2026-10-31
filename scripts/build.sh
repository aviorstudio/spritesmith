#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export VERSION="${VERSION:-dev}"
export COMMIT="${COMMIT:-$(git rev-parse HEAD)}"
[[ "$VERSION" == dev || "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release version'; exit 1; }
test "$COMMIT" = "$(git rev-parse HEAD)"
mkdir -p dist
if [ -n "${NATIVE_CLI_ARCHIVES:-}" ]; then
  python3 scripts/accept-native.py
else
  cargo build --release --locked
  python3 scripts/package-native.py
fi
python3 scripts/record-artifacts.py
