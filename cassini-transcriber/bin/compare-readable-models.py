#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import time
from pathlib import Path

from cassini_transcriber.llm import (
    OpenAICompatibleChatClient,
    OpenAICompatibleConfig,
    build_readable_records,
    plan_readable_record_batches,
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Compare OpenAI-compatible readable-transcript models on one transcript batch."
    )
    parser.add_argument("--transcript", required=True, type=Path, help="Source transcript.words.v1.json")
    parser.add_argument(
        "--base-url",
        default=os.getenv("CASSINI_API_BASE_URL")
        or os.getenv("OPENAI_BASE_URL")
        or ("https://openrouter.ai/api/v1" if os.getenv("OPENROUTER_API_KEY") else None),
        help="OpenAI-compatible API base URL",
    )
    parser.add_argument(
        "--api-key",
        default=os.getenv("CASSINI_API_KEY") or os.getenv("OPENROUTER_API_KEY") or os.getenv("OPENAI_API_KEY"),
        help="OpenAI-compatible API key",
    )
    parser.add_argument(
        "--models",
        nargs="+",
        default=[
            "openai/gpt-4o-mini",
            "google/gemini-2.5-flash",
            "anthropic/claude-3.7-sonnet",
            "qwen/qwen3-32b",
        ],
        help="Model ids to compare",
    )
    parser.add_argument("--batch-index", type=int, default=0, help="Readable batch index to compare")
    parser.add_argument("--max-batch-records", type=int, default=8, help="Maximum windows per batch")
    parser.add_argument("--max-batch-chars", type=int, default=3000, help="Maximum raw chars per batch")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if not args.base_url:
        raise SystemExit("--base-url is required")
    if not args.api_key:
        raise SystemExit("--api-key is required")

    transcript_payload = json.loads(args.transcript.read_text(encoding="utf-8"))
    records = build_readable_records(
        transcript_payload=transcript_payload,
        max_gap_ms=1800,
        max_window_ms=45_000,
        max_window_words=120,
    )
    batches = plan_readable_record_batches(
        records,
        max_batch_records=args.max_batch_records,
        max_batch_chars=args.max_batch_chars,
    )
    if args.batch_index < 0 or args.batch_index >= len(batches):
        raise SystemExit(f"--batch-index must be between 0 and {len(batches) - 1}")

    batch = batches[args.batch_index]
    for model in args.models:
        client = OpenAICompatibleChatClient(
            OpenAICompatibleConfig(
                base_url=args.base_url,
                api_key=args.api_key,
                model=model,
            )
        )
        started = time.monotonic()
        try:
            cleaned = client.rewrite_readable_records(batch)
        except Exception as exc:
            print(f"=== {model} ERROR {type(exc).__name__}: {exc}")
            continue
        elapsed = time.monotonic() - started
        print(f"=== {model} OK {elapsed:.2f}s")
        for record, text in zip(batch, cleaned, strict=True):
            print(f"{record.index}: {text}")
        print()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
