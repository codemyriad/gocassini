#!/usr/bin/env python3
"""Verify every private audio fixture against the immutable manifest."""

from __future__ import annotations

import argparse
import json
from dataclasses import asdict
from pathlib import Path

from benchlib import verify_fixtures


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--audio-dir", type=Path, required=True)
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    verified = verify_fixtures(args.manifest, args.audio_dir)
    summary = [
        {"name": item["name"], "path": item["path"], **asdict(item["wav"])}
        for item in verified
    ]
    if args.json:
        print(json.dumps(summary, indent=2))
    else:
        seconds = sum(item["wav"].duration_seconds for item in verified)
        print(f"verified {len(verified)} fixtures ({seconds:.3f} aggregate audio seconds)")


if __name__ == "__main__":
    main()

