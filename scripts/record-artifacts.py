"""Record exact current-source archive checksums."""
import hashlib
import json
import os
from pathlib import Path

root=Path('dist')
archives=sorted([*root.glob('*.tar.gz'),*root.glob('*.zip')])
assert archives
(root/'checksums.txt').write_text(''.join(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.name+'\n' for path in archives))
(root/'release.json').write_text(json.dumps(dict(revision=os.environ['COMMIT'],version=os.environ['VERSION']))+'\n')
