"""Package only the executable built and exercised on this native runner."""
import json
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import zipfile

system = platform.system()
machine = platform.machine().lower()
arch = {'amd64':'x86_64','x86_64':'x86_64','aarch64':'arm64','arm64':'arm64'}[machine]
assert system in ('Linux','Darwin','Windows')
assert (system,arch) != ('Windows','arm64'), 'This product publishes five targets'
binary = 'spritesmith.exe' if system=='Windows' else 'spritesmith'
target_root = Path(os.environ.get('CARGO_TARGET_DIR','target'))
rust_target = os.environ.get('RUST_TARGET','')
target = (target_root/rust_target if rust_target else target_root)/'release'/binary
revision = os.environ['COMMIT']
version = os.environ.get('VERSION','dev')
assert subprocess.check_output([str(target), '--version'],text=True).strip() == f'spritesmith {version} ({revision})'
directory = Path(os.environ.get('NATIVE_PACKAGE_OUTPUT','dist'))
directory.mkdir(exist_ok=True,parents=True)
name = f'spritesmith_{system}_{arch}'
if system=='Windows':
    with zipfile.ZipFile(directory/(name+'.zip'),'w',zipfile.ZIP_DEFLATED) as archive:
        archive.write(target,binary)
        archive.write('README.md','README.md')
else:
    with tarfile.open(directory/(name+'.tar.gz'),'w:gz') as archive:
        archive.add(target,arcname=binary)
        archive.add('README.md',arcname='README.md')
(directory/(name+'.json')).write_text(json.dumps(dict(revision=revision,version=version,platform=system,arch=arch))+'\n')

if system == 'Linux' and (rust_target or version != 'dev'):
    assert rust_target.endswith('-linux-musl')
    assert 'INTERP' not in subprocess.check_output(['readelf','-l',str(target)],text=True)
    assert '(NEEDED)' not in subprocess.check_output(['readelf','-d',str(target)],text=True)
