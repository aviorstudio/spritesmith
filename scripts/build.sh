#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
VERSION="${VERSION:-dev}"
[[ "$VERSION" == dev || "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release version'; exit 1; }
revision="$(git rev-parse HEAD)"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  target_os="${target%/*}"
  target_arch="${target#*/}"
  binary=spritesmith
  [[ "$target_os" != windows ]] || binary=spritesmith.exe
  staging="$(mktemp -d)"
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath \
    -ldflags "-X main.version=$VERSION -X main.revision=$revision" -o "$staging/$binary" ./cmd/spritesmith
  cp README.md "$staging/README.md"
  archive="spritesmith_${target_os}_${target_arch}"
  if [[ "$target_os" == windows ]]; then
    python3 - "$staging" "dist/$archive.zip" <<'PY'
from pathlib import Path
import sys, zipfile
with zipfile.ZipFile(sys.argv[2], 'w', zipfile.ZIP_DEFLATED) as archive:
    for path in sorted(Path(sys.argv[1]).iterdir()):
        archive.write(path, path.name)
PY
  else
    tar -czf "dist/$archive.tar.gz" -C "$staging" "$binary" README.md
  fi
  rm -rf "$staging"
done
(cd dist && sha256sum -- *.tar.gz *.zip > checksums.txt)
python3 - "$revision" "$VERSION" <<'PY'
import json, sys
from pathlib import Path
Path('dist/release.json').write_text(json.dumps({'revision':sys.argv[1], 'version':sys.argv[2]}, indent=2)+'\n')
PY
