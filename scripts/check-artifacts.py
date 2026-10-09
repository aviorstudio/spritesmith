#!/usr/bin/env python3
"""Check distributable contents, checksums, revision and native CLI behavior."""
import hashlib
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile

root = Path('dist')
expected = {f'spritesmith_{os}_{arch}.{suffix}' for os, arch, suffix in (
    ('linux', 'amd64', 'tar.gz'), ('linux', 'arm64', 'tar.gz'),
    ('darwin', 'amd64', 'tar.gz'), ('darwin', 'arm64', 'tar.gz'), ('windows', 'amd64', 'zip'))}
checksums = {}
for line in (root/'checksums.txt').read_text().splitlines():
    digest, name = line.split()
    if name in checksums: raise SystemExit('duplicate archive identity')
    checksums[name] = digest
if set(checksums) != expected: raise SystemExit('wrong release artifact set')
for name, digest in checksums.items():
    if hashlib.sha256((root/name).read_bytes()).hexdigest() != digest:
        raise SystemExit('archive digest mismatch')
    binary = 'spritesmith.exe' if name.endswith('.zip') else 'spritesmith'
    if name.endswith('.zip'):
        with zipfile.ZipFile(root/name) as archive:
            names = archive.namelist()
    else:
        with tarfile.open(root/name) as archive:
            names = archive.getnames()
            if any(not item.isfile() for item in archive.getmembers()):
                raise SystemExit('non-file archive member')
    if sorted(names) != sorted([binary, 'README.md']): raise SystemExit('unexpected distributable content')
revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
release = json.loads((root/'release.json').read_text())
if release['revision'] != revision: raise SystemExit('artifacts belong to another source revision')
with tempfile.TemporaryDirectory() as temp:
    with tarfile.open(root/'spritesmith_linux_amd64.tar.gz') as archive:
        archive.extractall(temp, filter='data')
    output = subprocess.check_output([str(Path(temp)/'spritesmith'), '--version'], text=True)
    if revision not in output: raise SystemExit('binary revision differs')
print('Five platform archives, checksums and source identity verified')
