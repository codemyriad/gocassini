#!/usr/bin/env python3
"""Versioned, observational CI phase timings; never qualification evidence."""
import argparse
import json
from pathlib import Path
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    record = commands.add_parser("record")
    record.add_argument("path", type=Path)
    record.add_argument("--phase", required=True)
    record.add_argument("--clock", type=int, required=True)
    record.add_argument("--started", required=True)
    record.add_argument("--exit-code", type=int, required=True)
    summary = commands.add_parser("summary")
    summary.add_argument("path", type=Path)
    args = parser.parse_args()
    if args.command == "record":
        duration = round((time.monotonic_ns() - args.clock) / 1e9, 3)
        row = dict(schema_version=1, phase=args.phase, started_at=args.started,
                   duration_seconds=duration, exit_code=args.exit_code)
        with args.path.open("a") as output:
            output.write(json.dumps(row) + "\n")
        print(f"[phase] {args.phase}: {duration:.1f}s (exit {args.exit_code})")
    elif args.path.exists():
        print("\n| Scenario phase | Seconds | Exit |\n|---|---:|---:|")
        for line in args.path.read_text().splitlines():
            row = json.loads(line)
            print(f"| {row['phase']} | {row['duration_seconds']:.1f} | {row['exit_code']} |")
        print("\nTool preparation and Cassini image transfer/load have separate Actions step timings. "
              "Stack pull includes layer extraction. These timings do not qualify a release.")


if __name__ == "__main__":
    main()
