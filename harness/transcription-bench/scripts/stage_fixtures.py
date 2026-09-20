#!/usr/bin/env python3
"""Copy or hard-link an already extracted private fixture set into the harness."""

from __future__ import annotations

import argparse
import os
import shutil
from pathlib import Path

from benchlib import load_manifest, verify_fixtures


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--source-dir", type=Path, required=True)
    parser.add_argument("--audio-dir", type=Path, required=True)
    parser.add_argument(
        "--hardlink",
        action="store_true",
        help="opt in to hard links; copying is safer when the source may be rewritten",
    )
    args = parser.parse_args()

    # Verify the source before creating any destination files.
    verify_fixtures(args.manifest, args.source_dir)
    manifest = load_manifest(args.manifest)
    args.audio_dir.mkdir(parents=True, exist_ok=True)
    for fixture in manifest["fixtures"]:
        source = args.source_dir / fixture["name"]
        destination = args.audio_dir / fixture["name"]
        temporary = destination.with_name(f".{destination.name}.tmp")
        if temporary.exists():
            temporary.unlink()
        if args.hardlink:
            try:
                os.link(source, temporary)
            except OSError:
                shutil.copyfile(source, temporary)
        else:
            shutil.copyfile(source, temporary)
        temporary.replace(destination)
    verify_fixtures(args.manifest, args.audio_dir)
    print(f"staged {len(manifest['fixtures'])} verified fixtures in {args.audio_dir}")


if __name__ == "__main__":
    main()
