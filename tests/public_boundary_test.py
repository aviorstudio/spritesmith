"""The public client must build without private implementation source."""
from pathlib import Path
text=Path('Cargo.toml').read_text()
if any(value in text for value in ['spritesmith-be','cazper-core','git =','path =']):
    raise AssertionError('Public client depends on private implementation source')
