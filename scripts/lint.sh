#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
cargo fmt --check
cargo clippy --locked --all-targets -- -D warnings
# Public clients must remain independent of private implementation sources.
python3 - <<'CHECK'
from pathlib import Path
text = Path('Cargo.toml').read_text()
if 'spritesmith-be' in text or 'cazper-core' in text or 'git =' in text or 'path =' in text:
    raise SystemExit('Public client depends on private source')
CHECK
