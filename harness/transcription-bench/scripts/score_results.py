#!/usr/bin/env python3
"""Score ASR results against public controls and optional private ground truth."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from benchlib import (
    aligned_edit_metrics,
    atomic_json_write,
    iter_result_cases,
    load_manifest,
    normalize_text,
    phrase_present,
)

SUPPORTED_RESULT_SCHEMAS = {
    "gocassini.openrouter-stt-benchmark.v1",
    "gocassini.hotword-benchmark.v1",
    "gocassini.parakeet-hotword-matrix.v1",
    "gocassini.voxtral-benchmark.v1",
    "gocassini.voxtral-realtime-benchmark.v1",
    "gocassini.voxtral-offline-benchmark.v2",
    "gocassini.voxtral-realtime-benchmark.v2",
}


def discover_result_paths(direct: list[Path], directories: list[Path]) -> list[Path]:
    """Return explicit and recursively discovered supported result payloads."""

    selected: dict[Path, Path] = {}
    for path in direct:
        resolved = path.resolve()
        selected[resolved] = path
    for directory in directories:
        if directory.is_symlink() or not directory.is_dir():
            raise ValueError(f"result directory is not a real directory: {directory}")
        for path in sorted(directory.rglob("*.json")):
            if path.is_symlink() or not path.is_file():
                continue
            try:
                payload = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
                raise ValueError(f"invalid JSON in result directory: {path}: {error}") from error
            if isinstance(payload, dict) and payload.get("schema") in SUPPORTED_RESULT_SCHEMAS:
                selected[path.resolve()] = path
    return [selected[key] for key in sorted(selected, key=str)]


def observed_direct_speech(result_case: dict, transcript: str) -> bool | None:
    explicit = result_case.get("directSpeech")
    if isinstance(explicit, bool):
        return explicit
    # Ordinary isolated-track transcripts can use non-empty speech as a
    # fallback observation. Structured multi-track challenges must not turn a
    # parse failure plus empty transcript into an accidental "no speech"
    # success.
    if result_case.get("challengeCase") is not None:
        return None
    return bool(normalize_text(transcript))


def load_ground_truth(path: Path, manifest: dict) -> dict:
    value = json.loads(path.read_text(encoding="utf-8"))
    if value.get("schema") != "gocassini.transcription-ground-truth.v1":
        raise ValueError(f"{path}: unsupported ground-truth schema")
    if value.get("fixtureSet") != manifest["fixtureSet"]:
        raise ValueError(f"{path}: fixtureSet does not match manifest")
    entries = value.get("fixtures")
    if not isinstance(entries, dict) or not entries:
        raise ValueError(f"{path}: fixtures must be a non-empty object")
    manifest_by_name = {fixture["name"]: fixture for fixture in manifest["fixtures"]}
    for name, entry in entries.items():
        if name not in manifest_by_name or not isinstance(entry, dict):
            raise ValueError(f"{path}: unknown or invalid fixture {name!r}")
        if not isinstance(entry.get("referenceTranscript"), str):
            raise ValueError(f"{path}: {name} requires a referenceTranscript string")
        if "overlapCount" in entry and (
            isinstance(entry["overlapCount"], bool)
            or not isinstance(entry["overlapCount"], int)
            or entry["overlapCount"] < 0
        ):
            raise ValueError(f"{path}: {name} overlapCount must be a non-negative integer")
        if "directSpeech" in entry and not isinstance(entry["directSpeech"], bool):
            raise ValueError(f"{path}: {name} directSpeech must be boolean")
        manifest_direct = manifest_by_name[name].get("expectedDirectSpeech")
        if manifest_direct is not None and entry.get("directSpeech", manifest_direct) != manifest_direct:
            raise ValueError(f"{path}: {name} directSpeech conflicts with manifest")
    return entries


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--result", type=Path, action="append", default=[])
    parser.add_argument(
        "--result-dir",
        type=Path,
        action="append",
        default=[],
        help="recursively discover supported benchmark result schemas",
    )
    parser.add_argument("--output", type=Path)
    parser.add_argument("--ground-truth", type=Path)
    parser.add_argument("--fail-on-forbidden", action="store_true")
    parser.add_argument("--fail-on-false-speech", action="store_true")
    args = parser.parse_args()

    manifest = load_manifest(args.manifest)
    try:
        result_paths = discover_result_paths(args.result, args.result_dir)
    except ValueError as error:
        parser.error(str(error))
    if not result_paths:
        parser.error("no supported benchmark result JSON was found")
    fixtures = {fixture["name"]: fixture for fixture in manifest["fixtures"]}
    global_forbidden = manifest.get("globalForbiddenTerms", [])
    ground_truth = load_ground_truth(args.ground_truth, manifest) if args.ground_truth else {}
    cases = []
    total_expected = 0
    total_found = 0
    total_injections = 0
    total_false_speech = 0
    total_reference_tokens = 0
    total_edit_distance = 0
    total_aligned_tokens = 0
    overlap_compared = 0
    overlap_exact = 0
    direct_compared = 0
    direct_correct = 0
    for result_path in result_paths:
        payload = json.loads(result_path.read_text(encoding="utf-8"))
        for result_case in iter_result_cases(payload):
            name = result_case["fixture"]
            label = result_case["label"]
            transcript = result_case["text"]
            fixture = fixtures.get(name)
            if fixture is None:
                continue
            expected = fixture.get("expectedTerms", [])
            forbidden = list(dict.fromkeys(global_forbidden + fixture.get("forbiddenTerms", [])))
            found = [term for term in expected if phrase_present(transcript, term)]
            injections = [term for term in forbidden if phrase_present(transcript, term)]
            truth = ground_truth.get(name)
            expected_direct = (
                truth.get("directSpeech") if truth and "directSpeech" in truth
                else fixture.get("expectedDirectSpeech")
            )
            observed_direct = observed_direct_speech(result_case, transcript)
            false_speech = expected_direct is False and observed_direct is True
            reference_metrics = None
            overlap_score = None
            direct_score = None
            if truth is not None:
                reference_metrics = aligned_edit_metrics(truth["referenceTranscript"], transcript)
                total_reference_tokens += reference_metrics["referenceTokens"]
                total_edit_distance += reference_metrics["tokenEditDistance"]
                total_aligned_tokens += reference_metrics["alignedTokens"]
                if "overlapCount" in truth:
                    observed_overlap = result_case.get("overlapCount")
                    if not isinstance(observed_overlap, int) and isinstance(
                        result_case.get("overlapIntervals"), list
                    ):
                        observed_overlap = len(result_case["overlapIntervals"])
                    overlap_score = {
                        "expected": truth["overlapCount"],
                        "observed": observed_overlap if isinstance(observed_overlap, int) else None,
                        "exact": observed_overlap == truth["overlapCount"]
                        if isinstance(observed_overlap, int) else None,
                    }
                    if isinstance(observed_overlap, int):
                        overlap_compared += 1
                        overlap_exact += int(observed_overlap == truth["overlapCount"])
                if "directSpeech" in truth:
                    direct_score = {
                        "expected": truth["directSpeech"],
                        "observed": observed_direct,
                        "correct": observed_direct == truth["directSpeech"],
                    }
                    direct_compared += 1
                    direct_correct += int(observed_direct == truth["directSpeech"])
            total_expected += len(expected)
            total_found += len(found)
            total_injections += len(injections)
            total_false_speech += int(false_speech)
            cases.append(
                {
                    "result": str(result_path),
                    "fixture": name,
                    "label": label,
                    "challengeCase": result_case.get("challengeCase"),
                    "sourceAudio": result_case.get("sourceAudio"),
                    "directSpeechTrack": result_case.get("directSpeechTrack"),
                    "structuredParseError": result_case.get("structuredParseError"),
                    "structuredValidationErrors": result_case.get(
                        "structuredValidationErrors"
                    ),
                    "expectedTerms": expected,
                    "foundExpectedTerms": found,
                    "missingExpectedTerms": [term for term in expected if term not in found],
                    "forbiddenInjections": injections,
                    "falseSpeechAttribution": false_speech,
                    "termRecall": len(found) / len(expected) if expected else None,
                    "reference": reference_metrics,
                    "overlap": overlap_score,
                    "directSpeech": direct_score,
                }
            )

    report = {
        "schema": "gocassini.transcription-score.v2",
        "groundTruth": args.ground_truth.name if args.ground_truth else None,
        "summary": {
            "cases": len(cases),
            "expectedTerms": total_expected,
            "foundExpectedTerms": total_found,
            "termRecall": total_found / total_expected if total_expected else None,
            "forbiddenInjections": total_injections,
            "falseSpeechAttributions": total_false_speech,
            "wordErrorRate": total_edit_distance / total_reference_tokens
            if total_reference_tokens else None,
            "alignedTokenRecall": total_aligned_tokens / total_reference_tokens
            if total_reference_tokens else None,
            "overlapExact": overlap_exact,
            "overlapCompared": overlap_compared,
            "directSpeechAccuracy": direct_correct / direct_compared if direct_compared else None,
        },
        "cases": cases,
    }
    if args.output:
        atomic_json_write(args.output, report)
    print(json.dumps(report, indent=2))
    if args.fail_on_forbidden and total_injections:
        raise SystemExit(3)
    if args.fail_on_false_speech and total_false_speech:
        raise SystemExit(4)


if __name__ == "__main__":
    main()
