#!/usr/bin/env python3
"""Bake the revision/checksum-pinned GGUF into the STT model root."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

spec = json.loads(Path(sys.argv[1]).read_text())
target = Path(sys.argv[2]) / "models" / spec["id"]
target.mkdir(parents=True, exist_ok=True)
path = target / spec["file"]
subprocess.run(["curl", "-fLSs", "--retry", "3", spec["url"], "-o", str(path)], check=True)
with path.open("rb") as source:
    hasher = hashlib.sha256()
    for chunk in iter(lambda: source.read(1024 * 1024), b""):
        hasher.update(chunk)
    digest = hasher.hexdigest()
if path.stat().st_size != spec["size"] or digest != spec["sha256"]:
    path.unlink()
    raise SystemExit("Bundled Ling model failed size/SHA-256 verification")
(target / "MODEL.json").write_text(json.dumps(spec, indent=2) + "\n")
