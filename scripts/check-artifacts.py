#!/usr/bin/env python3
"""Check distributable contents, checksums, revision and native CLI behavior."""
import hashlib
import json
import os
import platform
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile

root = Path('dist')
native_arch = {'x86_64':'x86_64','amd64':'x86_64','arm64':'arm64','aarch64':'arm64'}[platform.machine().lower()]
native_suffix = 'zip' if platform.system() == 'Windows' else 'tar.gz'
native_archive = f'spritesmith_{platform.system()}_{native_arch}.{native_suffix}'
native_binary = 'spritesmith.exe' if platform.system() == 'Windows' else 'spritesmith'
expected = {f'spritesmith_{os}_{arch}.{suffix}' for os, arch, suffix in (
    ('Linux', 'x86_64', 'tar.gz'), ('Linux', 'arm64', 'tar.gz'),
    ('Darwin', 'x86_64', 'tar.gz'), ('Darwin', 'arm64', 'tar.gz'), ('Windows', 'x86_64', 'zip'))}
if not os.environ.get('NATIVE_CLI_ARCHIVES'):
    expected = {native_archive}
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
    if native_suffix == 'zip':
        with zipfile.ZipFile(root/native_archive) as archive:
            archive.extractall(temp)
    else:
        with tarfile.open(root/native_archive) as archive:
            archive.extractall(temp, filter='data')
    output = subprocess.check_output([str(Path(temp)/native_binary), '--version'], text=True).strip()
    if output != f"spritesmith {release['version']} ({revision})":
        raise SystemExit('binary source/version differs')
print(f'{len(expected)} platform archives, checksums and source identity verified')
