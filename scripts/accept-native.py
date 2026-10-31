"""Require all native build identities before accepting current-run archives."""
import json
import os
from pathlib import Path
import shutil

root = Path(os.environ['NATIVE_CLI_ARCHIVES'])
expected = {(system,arch) for system,arch in [('Linux','x86_64'),('Linux','arm64'),('Darwin','x86_64'),('Darwin','arm64'),('Windows','x86_64')]}
names = set()
for system,arch in sorted(expected):
    prefix = f'spritesmith_{system}_{arch}'
    receipt = prefix+'.json'
    archive = prefix+('.zip' if system=='Windows' else '.tar.gz')
    assert json.loads((root/receipt).read_text()) == dict(revision=os.environ['COMMIT'],version=os.environ['VERSION'],platform=system,arch=arch), receipt
    assert not (root/archive).is_symlink() and (root/archive).is_file()
    shutil.copyfile(root/archive,Path('dist')/archive)
    names.update((receipt,archive))
assert {p.name for p in root.iterdir()} == names, 'Unexpected native build payload'
