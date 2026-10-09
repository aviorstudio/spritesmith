#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
test -z "$(gofmt -l cmd internal)" || { echo 'Go formatting differs'; exit 1; }
go vet ./...
# The public client must install without any private source dependency.
python3 - <<'PY'
from pathlib import Path
text = Path('go.mod').read_text()
if 'spritesmith-be' in text or 'cazper-core' in text or 'replace ' in text:
    raise SystemExit('Public client depends on private source')
PY
