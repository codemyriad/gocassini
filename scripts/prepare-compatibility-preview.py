#!/usr/bin/env python3
"""Prepare an explicitly non-release canary manifest, retaining the image version.

Canaries test the frozen released artifact, whose version must match AppAPI's
registration. The source manifest is therefore fetched from that release, not
silently relabelled as the current checkout's version.
"""
import json
from pathlib import Path
import re
import subprocess
import sys
import xml.etree.ElementTree as ET

from nextcloud_compatibility import ROOT, require

stack_path, output = map(Path, sys.argv[1:])
stack = json.loads(stack_path.read_text())
policy = json.loads((ROOT / 'ci/nextcloud-compatibility.json').read_text())
ref = policy['canary_image']
match = re.search(r':([^:@]+)@sha256:', ref)
require(match is not None, 'frozen canary image must retain its release tag alongside the digest')
release = match[1]
require(re.fullmatch(r'\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?', release), 'invalid canary release version')
manifest = subprocess.check_output(['gh', 'api', '-H', 'Accept: application/vnd.github.raw+json',
    f'repos/codemyriad/gocassini/contents/appinfo/info.xml?ref=v{release}'], text=True)
major = stack['nextcloud_version'].split('.')[0]
original = manifest
current_max = ET.fromstring(manifest).find('dependencies/nextcloud').get('max-version')
major = str(max(int(major), int(current_max.split('.')[0])))
manifest = re.sub(r'(<nextcloud\b[^>]*max-version=")[^"]+("[^>]*/>)',
                  lambda m: m[1] + major + m[2], manifest)
output.write_text(manifest)
output.with_suffix('.original.xml').write_text(original)
print(f'Canary manifest from v{release}: original maximum {current_max}, test maximum {major}')
