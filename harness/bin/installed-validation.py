#!/usr/bin/env python3
"""Strict, fixture-testable installed-ExApp job/catalog/transcript assertions."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from urllib.parse import urljoin


class ValidationError(Exception):
    pass


def load_json(path: str):
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValidationError(f"cannot read JSON {path}: {exc}") from exc


def require_id(value, label: str) -> str:
    result = str(value or "").strip()
    if not result:
        raise ValidationError(f"{label} has no id")
    return result


def command_select_job(args: argparse.Namespace) -> int:
    before = load_json(args.before)
    current = load_json(args.current)
    if not isinstance(before, list) or not isinstance(current, list):
        raise ValidationError("job responses must be JSON arrays")
    before_ids = {
        require_id(job.get("id"), "baseline job")
        for job in before
        if isinstance(job, dict)
    }
    new_jobs = [
        job
        for job in current
        if isinstance(job, dict)
        and require_id(job.get("id"), "current job") not in before_ids
    ]
    if not new_jobs:
        print(json.dumps({"result": "pending", "reason": "no-new-job"}))
        return 10
    if len(new_jobs) != 1:
        ids = [require_id(job.get("id"), "new job") for job in new_jobs]
        raise ValidationError(f"ambiguous new jobs: {', '.join(ids)}")
    job = new_jobs[0]
    job_id = require_id(job.get("id"), "new job")
    state = str(job.get("state") or "").strip().lower()
    stage = str(job.get("stage") or "").strip()
    error = str(job.get("error") or "").strip()
    result = {"job_id": job_id, "state": state, "stage": stage, "error": error}
    print(json.dumps(result, separators=(",", ":")))
    if state == "succeeded":
        return 0
    if state in {"failed", "interrupted"}:
        return 3
    return 10


def command_catalog_entry(args: argparse.Namespace) -> int:
    catalog = load_json(args.catalog)
    if not isinstance(catalog, dict) or not isinstance(catalog.get("meetings"), list):
        raise ValidationError("catalog must contain a meetings array")
    matches = [
        meeting
        for meeting in catalog["meetings"]
        if isinstance(meeting, dict)
        and str(meeting.get("id") or "").strip() == args.job_id
    ]
    if not matches:
        print(json.dumps({"result": "pending", "reason": "missing-catalog-entry"}))
        return 10
    if len(matches) != 1:
        raise ValidationError(f"duplicate catalog entries for {args.job_id}")
    meeting = matches[0]
    base = args.proxy_url.rstrip("/") + "/published/"
    artifact_path = str(meeting.get("artifactPath") or "").strip()
    audio_path = str(meeting.get("audioPath") or "").strip()
    if bool(artifact_path) == bool(audio_path):
        raise ValidationError(
            f"catalog entry {args.job_id} must define exactly one of artifactPath or audioPath"
        )
    if audio_path:
        segment_count = meeting.get("segmentCount")
        if (
            isinstance(segment_count, bool)
            or not isinstance(segment_count, (int, float))
            or int(segment_count) != segment_count
            or segment_count <= 0
        ):
            raise ValidationError(
                f"portable catalog entry {args.job_id} has non-positive/integer segmentCount: {segment_count!r}"
            )
        result = {
            "kind": "portable",
            "job_id": args.job_id,
            "segment_count": int(segment_count),
            "artifact_url": urljoin(base, audio_path),
        }
    else:
        artifact_path = artifact_path.rstrip("/") + "/"
        result = {
            "kind": "directory",
            "job_id": args.job_id,
            "segment_count": meeting.get("segmentCount"),
            "artifact_url": urljoin(base, artifact_path + "transcript.display.v1.json"),
        }
    print(json.dumps(result, separators=(",", ":")))
    return 0


def count_words(doc) -> int:
    if not isinstance(doc, dict):
        raise ValidationError("word transcript must be a JSON object")
    segments = doc.get("segments")
    if not isinstance(segments, list):
        raise ValidationError("word transcript must contain a segments array")
    count = 0
    for segment in segments:
        if not isinstance(segment, dict):
            raise ValidationError("word transcript segment is not an object")
        words = segment.get("words")
        if not isinstance(words, list):
            raise ValidationError("word transcript segment has no words array")
        for word in words:
            if not isinstance(word, dict) or not str(word.get("text") or "").strip():
                raise ValidationError("word transcript contains a malformed/empty word")
            count += 1
    declared = doc.get("wordCount")
    if declared is not None and (isinstance(declared, bool) or not isinstance(declared, int)):
        raise ValidationError("wordCount must be an integer when present")
    if declared is not None and declared != count:
        raise ValidationError(f"wordCount mismatch: declared={declared} decoded={count}")
    if count <= 0:
        raise ValidationError("decoded word transcript contains zero words")
    return count


def summarize_display(doc) -> tuple[int, int]:
    if not isinstance(doc, dict) or not isinstance(doc.get("blocks"), list):
        raise ValidationError("display transcript must contain a blocks array")
    text_blocks = 0
    words = 0
    for block in doc["blocks"]:
        if not isinstance(block, dict):
            raise ValidationError("display transcript block is not an object")
        if str(block.get("text") or "").strip():
            text_blocks += 1
        for key in ("words", "sourceWords"):
            value = block.get(key)
            if value is not None and not isinstance(value, list):
                raise ValidationError(f"display transcript {key} is not an array")
            if isinstance(value, list):
                words += len(value)
    if text_blocks == 0 and words == 0:
        raise ValidationError("display transcript contains no text or words")
    return text_blocks, words


def command_transcript_summary(args: argparse.Namespace) -> int:
    doc = load_json(args.transcript)
    if args.kind == "words":
        result = {"word_count": count_words(doc)}
    else:
        text_blocks, words = summarize_display(doc)
        result = {"text_blocks": text_blocks, "word_count": words}
    print(json.dumps(result, separators=(",", ":")))
    return 0


def command_catalog_contains(args: argparse.Namespace) -> int:
    catalog = load_json(args.catalog)
    if not isinstance(catalog, dict) or not isinstance(catalog.get("meetings"), list):
        raise ValidationError("catalog must contain a meetings array")
    ids = {
        str(meeting.get("id") or "").strip()
        for meeting in catalog["meetings"]
        if isinstance(meeting, dict) and str(meeting.get("id") or "").strip()
    }
    missing = [expected for expected in args.id if expected not in ids]
    if missing:
        raise ValidationError("missing catalog ids: " + ", ".join(missing))
    print(json.dumps({"catalog_entries": len(ids)}, separators=(",", ":")))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)

    select = sub.add_parser("select-job")
    select.add_argument("--before", required=True)
    select.add_argument("--current", required=True)
    select.set_defaults(run=command_select_job)

    catalog = sub.add_parser("catalog-entry")
    catalog.add_argument("--catalog", required=True)
    catalog.add_argument("--job-id", required=True)
    catalog.add_argument("--proxy-url", required=True)
    catalog.set_defaults(run=command_catalog_entry)

    transcript = sub.add_parser("transcript-summary")
    transcript.add_argument("--transcript", required=True)
    transcript.add_argument("--kind", choices=("words", "display"), required=True)
    transcript.set_defaults(run=command_transcript_summary)

    contains = sub.add_parser("catalog-contains")
    contains.add_argument("--catalog", required=True)
    contains.add_argument("--id", action="append", required=True)
    contains.set_defaults(run=command_catalog_contains)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        return args.run(args)
    except ValidationError as exc:
        print(f"validation error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
