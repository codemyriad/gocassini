#!/usr/bin/env python3
"""Bounded, CUDA-only Voxtral offline benchmark over the pinned fixture set."""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import os
import re
import sys
import time
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent.parent / "scripts"
sys.path.insert(0, str(SCRIPT_DIR))

from benchlib import (  # noqa: E402
    atomic_json_write,
    load_manifest,
    read_wav_info,
    select_fixture_names,
    sha256_file,
    verify_fixtures,
)
from voxtral_support import (  # noqa: E402
    ResultTelemetry,
    VOXTRAL_OFFLINE_SNAPSHOT,
    environment_requests_offline,
    resolve_model_snapshot,
)


DEFAULT_MODEL_ID = VOXTRAL_OFFLINE_SNAPSHOT.model_id
DEFAULT_MODEL_REVISION = VOXTRAL_OFFLINE_SNAPSHOT.revision
BASELINE_FIXTURES = (
    "2026-03-06_0742.wav",
    "2026-03-06_0961.wav",
    "mix.wav",
    "okay-mix.wav",
)


def _normalized_key(value: str) -> str:
    return re.sub(r"[^a-z0-9]", "", value.casefold())


def _mapping_value(mapping: dict[str, Any], *names: str) -> Any:
    wanted = {_normalized_key(name) for name in names}
    for key, value in mapping.items():
        if isinstance(key, str) and _normalized_key(key) in wanted:
            return value
    return None


def extract_json_object(text: str) -> tuple[dict[str, Any] | None, str | None]:
    """Extract the first JSON object from plain, fenced, or prefaced model output.

    ``JSONDecoder.raw_decode`` handles braces inside JSON strings and lets us
    accept harmless prose or Markdown around the requested compact object. We
    intentionally do not repair malformed JSON: a parse failure must remain a
    visible benchmark failure rather than silently changing the model answer.
    """

    decoder = json.JSONDecoder()
    candidates = [text.strip()]
    candidates.extend(
        match.group(1).strip()
        for match in re.finditer(r"```(?:json)?\s*(.*?)```", text, flags=re.IGNORECASE | re.DOTALL)
    )
    errors: list[str] = []
    for candidate in candidates:
        if not candidate:
            continue
        starts = [index for index, character in enumerate(candidate) if character == "{"]
        for start in starts:
            try:
                value, _ = decoder.raw_decode(candidate[start:])
            except json.JSONDecodeError as error:
                errors.append(error.msg)
                continue
            if isinstance(value, dict):
                return value, None
    detail = errors[-1] if errors else "no JSON object found"
    return None, f"could not parse attribution JSON: {detail}"


def _direct_speech_observation(value: Any) -> bool | None:
    if isinstance(value, bool):
        return value
    if isinstance(value, list):
        return bool(value)
    if isinstance(value, dict):
        explicit = _mapping_value(value, "direct_speech", "directSpeech", "is_direct_speech")
        if isinstance(explicit, bool):
            return explicit
        intervals = _mapping_value(
            value,
            "intervals",
            "direct_speech_intervals",
            "directSpeechIntervals",
            "segments",
        )
        if isinstance(intervals, list):
            return bool(intervals)
    return None


def parse_attribution_response(text: str, direct_speech_track: str = "Ivan") -> dict[str, Any]:
    """Normalize the requested attribution JSON into scorer-facing observations."""

    parsed, parse_error = extract_json_object(text)
    observations: dict[str, Any] = {
        "transcript": "",
        "directSpeech": None,
        "directSpeechTrack": direct_speech_track,
        "overlapIntervals": None,
        "overlapCount": None,
        "confidence": None,
        "audioQuality": None,
        "structuredOutput": parsed,
        "structuredParseError": parse_error,
        "structuredValidationErrors": [],
    }
    if parsed is None:
        return observations

    # Some chat templates wrap the requested object in a conventional result
    # property. Accept that small variation while preserving the raw object.
    body = parsed
    if not any(
        _mapping_value(body, name) is not None
        for name in ("verbatim_transcript", "direct_speech_intervals_by_track", "overlap_intervals")
    ):
        nested = _mapping_value(body, "result", "output", "attribution")
        if isinstance(nested, dict):
            body = nested

    validation_errors: list[str] = observations["structuredValidationErrors"]
    transcript = _mapping_value(body, "verbatim_transcript", "verbatimTranscript", "transcript")
    if isinstance(transcript, str):
        observations["transcript"] = transcript.strip()
    else:
        validation_errors.append("verbatim_transcript must be a string")

    overlap_intervals = _mapping_value(body, "overlap_intervals", "overlapIntervals")
    if isinstance(overlap_intervals, list):
        observations["overlapIntervals"] = overlap_intervals
        observations["overlapCount"] = len(overlap_intervals)
    else:
        validation_errors.append("overlap_intervals must be an array")

    by_track = _mapping_value(
        body,
        "direct_speech_intervals_by_track",
        "directSpeechIntervalsByTrack",
        "direct_speech_by_track",
    )
    if isinstance(by_track, dict):
        wanted = _normalized_key(direct_speech_track)
        track_value = next(
            (
                value
                for key, value in by_track.items()
                if isinstance(key, str) and _normalized_key(key) == wanted
            ),
            None,
        )
        observations["directSpeech"] = _direct_speech_observation(track_value)
        if observations["directSpeech"] is None:
            validation_errors.append(
                f"direct-speech intervals for track {direct_speech_track!r} are missing or invalid"
            )
    else:
        validation_errors.append("direct_speech_intervals_by_track must be an object")

    confidence = _mapping_value(body, "confidence")
    if isinstance(confidence, (int, float)) and not isinstance(confidence, bool):
        observations["confidence"] = confidence
    else:
        validation_errors.append("confidence must be numeric")

    audio_quality = _mapping_value(body, "audio_quality", "audioQuality")
    if isinstance(audio_quality, dict):
        observations["audioQuality"] = audio_quality
    else:
        validation_errors.append("audio_quality must be an object")
    return observations


def generated_text(processor, model, inputs, max_new_tokens: int) -> tuple[str, float]:
    import torch

    inputs = inputs.to("cuda", dtype=torch.bfloat16)
    torch.cuda.synchronize()
    started = time.perf_counter()
    with torch.inference_mode():
        outputs = model.generate(
            **inputs,
            do_sample=False,
            max_new_tokens=max_new_tokens,
            use_cache=True,
        )
    torch.cuda.synchronize()
    elapsed = time.perf_counter() - started
    text = processor.batch_decode(
        outputs[:, inputs.input_ids.shape[1] :], skip_special_tokens=True
    )[0]
    return text.strip(), elapsed


def transcribe(processor, model, model_id: str, path: Path, max_new_tokens: int) -> dict:
    inputs = processor.apply_transcription_request(
        language="en", audio=str(path), model_id=model_id
    )
    text, elapsed = generated_text(processor, model, inputs, max_new_tokens)
    seconds = read_wav_info(path).duration_seconds
    return {
        "kind": "transcription",
        "label": f"transcription-{path.name}",
        "audio": [path.name],
        "audioSeconds": seconds,
        "elapsedSeconds": elapsed,
        "rtf": elapsed / seconds,
        "text": text,
    }


def audio_prompt(
    processor,
    model,
    paths: list[Path],
    prompt: str,
    label: str,
    max_new_tokens: int,
    challenge_case: str | None = None,
    direct_speech_track: str = "Ivan",
) -> dict:
    content = [{"type": "audio", "path": str(path)} for path in paths]
    content.append({"type": "text", "text": prompt})
    inputs = processor.apply_chat_template([{"role": "user", "content": content}])
    text, elapsed = generated_text(processor, model, inputs, max_new_tokens)
    seconds = sum(read_wav_info(path).duration_seconds for path in paths)
    result = {
        "kind": "audio_prompt",
        "label": label,
        "audio": [path.name for path in paths],
        "aggregateAudioSeconds": seconds,
        "elapsedSeconds": elapsed,
        "rtf": elapsed / seconds,
        "prompt": prompt,
        "text": text,
        "overlapCount": text.casefold().count("[overlap]"),
    }
    if challenge_case is not None:
        result.update(
            {
                "challengeCase": challenge_case,
                "mixFixture": paths[0].name,
                **parse_attribution_response(text, direct_speech_track),
            }
        )
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--audio-dir", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--model-id", default=DEFAULT_MODEL_ID)
    parser.add_argument("--revision", default=DEFAULT_MODEL_REVISION)
    parser.add_argument(
        "--snapshot-dir",
        type=Path,
        help="use this already-downloaded model directory without contacting Hugging Face",
    )
    parser.add_argument(
        "--model-cache-dir",
        type=Path,
        help="optional Hugging Face snapshot cache (HF_HOME remains supported)",
    )
    parser.add_argument(
        "--local-files-only",
        action="store_true",
        help="require the model revision to be present locally; never download it",
    )
    parser.add_argument(
        "--telemetry-dir",
        type=Path,
        help="first-result/progress sidecar directory (default: OUTPUT parent/.telemetry)",
    )
    parser.add_argument(
        "--suite",
        choices=("baseline", "vocabulary", "attribution", "prompted", "all"),
        default="all",
    )
    parser.add_argument(
        "--fixtures",
        default=",".join(BASELINE_FIXTURES),
        help="comma-separated manifest names for ordinary transcription, or all",
    )
    parser.add_argument("--approved-term", action="append", default=[])
    parser.add_argument("--max-new-tokens", type=int, default=384)
    args = parser.parse_args()
    if not 16 <= args.max_new_tokens <= 1024:
        raise SystemExit("--max-new-tokens must be in 16..1024")

    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    os.environ.setdefault("OMP_NUM_THREADS", "1")
    os.environ.setdefault("MKL_NUM_THREADS", "1")

    import torch
    from transformers import AutoProcessor, VoxtralForConditionalGeneration

    torch.set_num_threads(1)
    torch.set_num_interop_threads(1)
    if not torch.cuda.is_available():
        raise SystemExit("CUDA is unavailable; refusing CPU inference")
    if not torch.cuda.is_bf16_supported():
        raise SystemExit("BF16 is unavailable; refusing an unplanned fallback")

    verify_fixtures(args.manifest, args.audio_dir)
    manifest = load_manifest(args.manifest)
    selected = select_fixture_names(manifest, args.fixtures)
    paths = {fixture["name"]: args.audio_dir / fixture["name"] for fixture in manifest["fixtures"]}

    expected_results = 0
    if args.suite in ("baseline", "all"):
        expected_results += len(selected)
    if args.suite in ("vocabulary", "prompted", "all"):
        expected_results += sum(
            fixture["group"] == "vocabulary" and fixture["name"] in selected
            for fixture in manifest["fixtures"]
        )
    if args.suite in ("attribution", "prompted", "all"):
        expected_results += 2
    telemetry = ResultTelemetry(
        args.output,
        runner="voxtral-offline",
        model_id=args.model_id,
        revision=args.revision,
        expected_results=expected_results,
        telemetry_dir=args.telemetry_dir,
    )

    snapshot = resolve_model_snapshot(
        args.model_id,
        args.revision,
        VOXTRAL_OFFLINE_SNAPSHOT,
        snapshot_dir=args.snapshot_dir,
        cache_dir=args.model_cache_dir,
        local_files_only=args.local_files_only or environment_requests_offline(),
    )

    load_started = time.perf_counter()
    processor = AutoProcessor.from_pretrained(snapshot, local_files_only=True)
    model = VoxtralForConditionalGeneration.from_pretrained(
        snapshot,
        dtype=torch.bfloat16,
        device_map="cuda",
        low_cpu_mem_usage=True,
        local_files_only=True,
    )
    model.eval()
    if next(model.parameters()).device.type != "cuda":
        raise SystemExit("model is not on CUDA; refusing CPU inference")
    torch.cuda.synchronize()
    load_seconds = time.perf_counter() - load_started

    results: list[dict] = []
    if args.suite in ("baseline", "all"):
        for name in selected:
            result = transcribe(
                processor, model, args.model_id, paths[name], args.max_new_tokens
            )
            results.append(result)
            telemetry.record(result)

    if args.suite in ("vocabulary", "prompted", "all"):
        terms = list(args.approved_term)
        if not terms:
            for fixture in manifest["fixtures"]:
                terms.extend(fixture.get("expectedTerms", []))
        terms = list(dict.fromkeys(terms))
        vocabulary = (
            "Transcribe this meeting excerpt verbatim. Use these approved spellings only when "
            f"acoustically supported: {', '.join(terms)}. Do not insert a listed term merely "
            "because it was provided. Mark simultaneous speech as [overlap]. Return only the transcript."
        )
        for fixture in manifest["fixtures"]:
            if fixture["group"] == "vocabulary" and fixture["name"] in selected:
                path = paths[fixture["name"]]
                result = audio_prompt(
                    processor,
                    model,
                    [path],
                    vocabulary,
                    f"vocabulary-{path.name}",
                    args.max_new_tokens,
                )
                results.append(result)
                telemetry.record(result)

    if args.suite in ("attribution", "prompted", "all"):
        attribution_prompt = (
            "The first audio is the meeting mix, the second is Chima's isolated track, and the "
            "third is Ivan's isolated track; all start at the same instant. Determine whether Ivan "
            "speaks directly or only contains bleed/noise. Return only one compact JSON object "
            "with exactly this shape: {\"verbatim_transcript\":\"...\","
            "\"direct_speech_intervals_by_track\":{\"Chima\":[],\"Ivan\":[]},"
            "\"overlap_intervals\":[],\"confidence\":0.0,\"audio_quality\":{"
            "\"echo\":\"...\",\"clipping\":\"...\",\"rustling\":\"...\","
            "\"cross_track_bleed\":\"...\"}}. Each interval must have numeric start and end "
            "seconds relative to clip start. Keep Ivan's array empty when its track has only "
            "bleed/noise. Do not use Markdown and do not guess when evidence is absent."
        )
        for group, names in {
            "disputed-interjection": ["mix.wav", "chima.wav", "ivan.wav"],
            "short-okay": ["okay-mix.wav", "okay-chima.wav", "okay-ivan.wav"],
        }.items():
            result = audio_prompt(
                processor,
                model,
                [paths[name] for name in names],
                attribution_prompt,
                f"three-track-{group}",
                args.max_new_tokens,
                challenge_case=group,
                direct_speech_track="Ivan",
            )
            results.append(result)
            telemetry.record(result)

    payload = {
        "schema": "gocassini.voxtral-offline-benchmark.v2",
        "fixtureSet": manifest["fixtureSet"],
        "fixtureManifestSha256": sha256_file(args.manifest),
        "model": args.model_id,
        "modelRevision": args.revision,
        "suite": args.suite,
        "device": torch.cuda.get_device_name(0),
        "cuda": torch.version.cuda,
        "torch": torch.__version__,
        "transformers": importlib.metadata.version("transformers"),
        "loadSeconds": load_seconds,
        "peakAllocatedMiB": torch.cuda.max_memory_allocated() / 1024**2,
        "peakReservedMiB": torch.cuda.max_memory_reserved() / 1024**2,
        "results": results,
    }
    atomic_json_write(args.output, payload)
    telemetry.complete()
    print(args.output)


if __name__ == "__main__":
    main()
