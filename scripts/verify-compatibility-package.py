#!/usr/bin/env python3
"""Bind the final signed package to the manifest that passed qualification."""
import hashlib
import json
from pathlib import Path
import sys
import tarfile

from nextcloud_compatibility import require, sha

package, evidence = map(Path, sys.argv[1:])
index = json.loads(evidence.read_text())
with tarfile.open(package) as archive:
    manifests = [m for m in archive.getmembers() if m.name.rstrip('/').endswith('/appinfo/info.xml')]
    require(len(manifests) == 1, 'package does not contain exactly one manifest')
    actual = hashlib.sha256(archive.extractfile(manifests[0]).read()).hexdigest()
require(actual == index['manifest_sha256'], 'packaged manifest differs from tested manifest')
print(json.dumps({'package': package.name, 'sha256': sha(package),
                  'evidence_sha256': sha(evidence), 'manifest_sha256': actual}, indent=2))
