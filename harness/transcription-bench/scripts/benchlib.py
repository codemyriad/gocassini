#!/usr/bin/env python3
"""Dependency-free fixture and result helpers for the transcription benchmark."""

from __future__ import annotations

import hashlib
import json
import os
import re
import struct
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable


STREAMING_WAV_SENTINELS = {0x7FFFFFFF, 0xFFFFFFFF}
FIXTURE_NAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*\.wav$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class WavInfo:
    codec: str
    sample_rate_hz: int
    channels: int
    bits_per_sample: int
    block_align: int
    data_offset: int
    data_bytes: int
    declared_data_bytes: int
    frames: int
    duration_seconds: float


def read_wav_info(path: Path) -> WavInfo:
    """Read PCM WAV metadata without trusting a streaming data-size sentinel.

    FFmpeg pipe output may leave the data chunk size at 0x7fffffff or
    0xffffffff.  In that case the actual file length is authoritative.  This
    avoids the multi-hour phantom durations reported by Python's wave module.
    """

    file_size = path.stat().st_size
    with path.open("rb") as wav:
        header = wav.read(12)
        if len(header) != 12 or header[:4] != b"RIFF" or header[8:] != b"WAVE":
            raise ValueError(f"{path}: not a RIFF WAVE file")

        fmt: tuple[int, int, int, int, int] | None = None
        data_offset = -1
        declared_data_bytes = -1
        while wav.tell() + 8 <= file_size:
            chunk_header = wav.read(8)
            if len(chunk_header) != 8:
                break
            chunk_id, chunk_size = struct.unpack("<4sI", chunk_header)
            content_offset = wav.tell()
            if chunk_id == b"fmt ":
                raw = wav.read(min(chunk_size, 40))
                if len(raw) < 16:
                    raise ValueError(f"{path}: truncated fmt chunk")
                audio_format, channels, sample_rate, _, block_align, bits = struct.unpack(
                    "<HHIIHH", raw[:16]
                )
                fmt = (audio_format, channels, sample_rate, block_align, bits)
            elif chunk_id == b"data":
                data_offset = content_offset
                declared_data_bytes = chunk_size
                break

            next_offset = content_offset + chunk_size + (chunk_size & 1)
            if next_offset > file_size:
                raise ValueError(f"{path}: truncated {chunk_id!r} chunk")
            wav.seek(next_offset)

    if fmt is None or data_offset < 0:
        raise ValueError(f"{path}: missing fmt or data chunk")
    audio_format, channels, sample_rate, block_align, bits = fmt
    if audio_format != 1:
        raise ValueError(f"{path}: require integer PCM (format 1), got {audio_format}")
    if channels <= 0 or sample_rate <= 0 or block_align <= 0:
        raise ValueError(f"{path}: invalid WAV format values")

    available = file_size - data_offset
    if declared_data_bytes in STREAMING_WAV_SENTINELS:
        data_bytes = available
    else:
        if declared_data_bytes > available:
            raise ValueError(
                f"{path}: data chunk declares {declared_data_bytes} bytes, only {available} remain"
            )
        data_bytes = declared_data_bytes
    if data_bytes % block_align:
        raise ValueError(f"{path}: data size {data_bytes} is not frame-aligned")
    frames = data_bytes // block_align
    return WavInfo(
        codec=f"pcm_s{bits}le",
        sample_rate_hz=sample_rate,
        channels=channels,
        bits_per_sample=bits,
        block_align=block_align,
        data_offset=data_offset,
        data_bytes=data_bytes,
        declared_data_bytes=declared_data_bytes,
        frames=frames,
        duration_seconds=frames / sample_rate,
    )


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sha256_region(path: Path, offset: int, length: int) -> str:
    digest = hashlib.sha256()
    remaining = length
    with path.open("rb") as source:
        source.seek(offset)
        while remaining:
            chunk = source.read(min(1024 * 1024, remaining))
            if not chunk:
                raise ValueError(f"{path}: truncated while hashing PCM payload")
            digest.update(chunk)
            remaining -= len(chunk)
    return digest.hexdigest()


def load_manifest(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if value.get("schema") != "gocassini.transcription-fixtures.v1":
        raise ValueError(f"{path}: unsupported manifest schema {value.get('schema')!r}")
    if not isinstance(value.get("fixtureSet"), str) or not value["fixtureSet"].strip():
        raise ValueError(f"{path}: fixtureSet must be a non-empty string")
    fixtures = value.get("fixtures")
    if not isinstance(fixtures, list) or not fixtures:
        raise ValueError(f"{path}: fixtures must be a non-empty list")
    audio_format = value.get("audioFormat")
    if audio_format != {"codec": "pcm_s16le", "sampleRateHz": 16000, "channels": 1}:
        raise ValueError(f"{path}: audioFormat must pin 16 kHz mono PCM s16le")
    forbidden = value.get("globalForbiddenTerms")
    if not isinstance(forbidden, list) or not all(
        isinstance(term, str) and term.strip() for term in forbidden
    ):
        raise ValueError(f"{path}: globalForbiddenTerms must be non-empty strings")
    names = []
    for fixture in fixtures:
        if not isinstance(fixture, dict):
            raise ValueError(f"{path}: every fixture must be an object")
        name = fixture.get("name")
        if not isinstance(name, str) or not FIXTURE_NAME_RE.fullmatch(name) or Path(name).name != name:
            raise ValueError(f"{path}: unsafe fixture basename {name!r}")
        names.append(name)
        for field in ("sha256", "pcmSha256"):
            if not isinstance(fixture.get(field), str) or not SHA256_RE.fullmatch(fixture[field]):
                raise ValueError(f"{path}: {name} has invalid {field}")
        for field in ("bytes", "frames"):
            number = fixture.get(field)
            if isinstance(number, bool) or not isinstance(number, int) or number <= 0:
                raise ValueError(f"{path}: {name} has invalid {field}")
        duration = fixture.get("durationSeconds")
        if isinstance(duration, bool) or not isinstance(duration, (int, float)) or duration <= 0:
            raise ValueError(f"{path}: {name} has invalid durationSeconds")
        expected_duration = fixture["frames"] / audio_format["sampleRateHz"]
        if abs(duration - expected_duration) > 0.000_001:
            raise ValueError(f"{path}: {name} duration does not match frames")
        for field in ("role", "group", "track"):
            if not isinstance(fixture.get(field), str) or not fixture[field].strip():
                raise ValueError(f"{path}: {name} has invalid {field}")
        terms = fixture.get("expectedTerms")
        if not isinstance(terms, list) or not all(isinstance(term, str) and term.strip() for term in terms):
            raise ValueError(f"{path}: {name} expectedTerms must be strings")
        if "expectedDirectSpeech" in fixture and not isinstance(fixture["expectedDirectSpeech"], bool):
            raise ValueError(f"{path}: {name} expectedDirectSpeech must be boolean")
    if len(set(names)) != len(names):
        raise ValueError(f"{path}: fixture names must be unique")
    return value


def verify_fixtures(manifest_path: Path, audio_dir: Path) -> list[dict[str, Any]]:
    manifest = load_manifest(manifest_path)
    expected_format = manifest["audioFormat"]
    verified: list[dict[str, Any]] = []
    for fixture in manifest["fixtures"]:
        path = audio_dir / fixture["name"]
        if not path.is_file():
            raise ValueError(f"missing fixture: {path}")
        if path.stat().st_size != fixture["bytes"]:
            raise ValueError(
                f"{path}: size {path.stat().st_size}, expected {fixture['bytes']}"
            )
        digest = sha256_file(path)
        if digest != fixture["sha256"]:
            raise ValueError(f"{path}: SHA256 {digest}, expected {fixture['sha256']}")
        info = read_wav_info(path)
        pcm_digest = sha256_region(path, info.data_offset, info.data_bytes)
        if pcm_digest != fixture["pcmSha256"]:
            raise ValueError(
                f"{path}: PCM SHA256 {pcm_digest}, expected {fixture['pcmSha256']}"
            )
        checks = {
            "codec": (info.codec, expected_format["codec"]),
            "sample rate": (info.sample_rate_hz, expected_format["sampleRateHz"]),
            "channels": (info.channels, expected_format["channels"]),
            "frames": (info.frames, fixture["frames"]),
        }
        for label, (actual, expected) in checks.items():
            if actual != expected:
                raise ValueError(f"{path}: {label} {actual}, expected {expected}")
        if abs(info.duration_seconds - fixture["durationSeconds"]) > 0.000_001:
            raise ValueError(
                f"{path}: duration {info.duration_seconds}, expected {fixture['durationSeconds']}"
            )
        verified.append({"name": fixture["name"], "path": str(path), "wav": info})
    return verified


def select_fixture_names(manifest: dict[str, Any], selector: str) -> list[str]:
    available = {fixture["name"] for fixture in manifest["fixtures"]}
    if selector.strip() in ("", "all"):
        return [fixture["name"] for fixture in manifest["fixtures"]]
    names = [item.strip() for item in selector.split(",") if item.strip()]
    unknown = sorted(set(names) - available)
    if unknown:
        raise ValueError(f"unknown fixtures: {', '.join(unknown)}")
    return names


def normalize_text(value: str) -> str:
    return " ".join(re.findall(r"[\w]+", value.casefold(), flags=re.UNICODE))


def phrase_present(text: str, phrase: str) -> bool:
    return f" {normalize_text(phrase)} " in f" {normalize_text(text)} "


def aligned_edit_metrics(reference: str, hypothesis: str) -> dict[str, float | int | None]:
    """Return token Levenshtein distance and exact aligned-token recall."""

    reference_tokens = normalize_text(reference).split()
    hypothesis_tokens = normalize_text(hypothesis).split()
    if len(reference_tokens) > 2000 or len(hypothesis_tokens) > 2000:
        raise ValueError("ground-truth scoring is limited to 2000 normalized tokens per transcript")
    previous = [(index, 0) for index in range(len(hypothesis_tokens) + 1)]
    for ref_index, ref_token in enumerate(reference_tokens, start=1):
        current = [(ref_index, 0)]
        for hyp_index, hyp_token in enumerate(hypothesis_tokens, start=1):
            deletion = (previous[hyp_index][0] + 1, previous[hyp_index][1])
            insertion = (current[hyp_index - 1][0] + 1, current[hyp_index - 1][1])
            if ref_token == hyp_token:
                diagonal = (previous[hyp_index - 1][0], previous[hyp_index - 1][1] + 1)
            else:
                diagonal = (previous[hyp_index - 1][0] + 1, previous[hyp_index - 1][1])
            current.append(min((deletion, insertion, diagonal), key=lambda item: (item[0], -item[1])))
        previous = current
    distance, matches = previous[-1]
    return {
        "referenceTokens": len(reference_tokens),
        "hypothesisTokens": len(hypothesis_tokens),
        "tokenEditDistance": distance,
        "wordErrorRate": distance / max(1, len(reference_tokens)),
        "alignedTokens": matches,
        "alignedTokenRecall": matches / len(reference_tokens) if reference_tokens else None,
    }


def atomic_json_write(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=False) + "\n", encoding="utf-8")
    temporary.replace(path)


def iter_result_cases(payload: dict[str, Any]) -> Iterable[dict[str, Any]]:
    """Yield normalized individual-fixture result cases across runner schemas."""

    schema = payload.get("schema", "")
    if schema == "gocassini.hotword-benchmark.v1":
        for audio in payload.get("audios", []):
            yield {
                "fixture": Path(audio["path"]).name,
                "label": payload.get("label", "parakeet"),
                "text": audio.get("text", ""),
            }
        return
    if schema == "gocassini.parakeet-hotword-matrix.v1":
        for condition in payload.get("conditions", []):
            label = condition.get("label", "parakeet")
            for audio in condition.get("audios", []):
                yield {"fixture": Path(audio["path"]).name, "label": label, "text": audio.get("text", "")}
        return
    if schema in {
        "gocassini.openrouter-stt-benchmark.v1",
        "gocassini.voxtral-benchmark.v1",
        "gocassini.voxtral-realtime-benchmark.v1",
        "gocassini.voxtral-offline-benchmark.v2",
        "gocassini.voxtral-realtime-benchmark.v2",
    }:
        for result in payload.get("results", []):
            audio = result.get("audio")
            if isinstance(audio, str):
                yield {
                    "fixture": Path(audio).name,
                    "label": result.get("label", audio),
                    "text": result.get("text", ""),
                    "overlapCount": result.get("overlapCount"),
                    "overlapIntervals": result.get("overlapIntervals"),
                    "directSpeech": result.get("directSpeech"),
                }
            elif isinstance(audio, list) and audio and all(isinstance(item, str) for item in audio):
                # Multi-track attribution is scored against the meeting mix,
                # which is the first input and is also written explicitly by
                # current runners. Older result files still become visible
                # instead of being silently dropped.
                fixture = result.get("mixFixture", audio[0])
                if not isinstance(fixture, str):
                    continue
                transcript = result.get("transcript")
                if not isinstance(transcript, str):
                    transcript = result.get("text", "")
                challenge_case = result.get("challengeCase")
                if len(audio) > 1 and not (
                    isinstance(challenge_case, str) and challenge_case.strip()
                ):
                    challenge_case = result.get("label", "multi-audio")
                yield {
                    "fixture": Path(fixture).name,
                    "label": result.get("label", audio[0]),
                    "text": transcript,
                    "challengeCase": challenge_case,
                    "sourceAudio": [Path(item).name for item in audio],
                    "overlapCount": result.get("overlapCount"),
                    "overlapIntervals": result.get("overlapIntervals"),
                    "directSpeech": result.get("directSpeech"),
                    "directSpeechTrack": result.get("directSpeechTrack"),
                    "structuredParseError": result.get("structuredParseError"),
                    "structuredValidationErrors": result.get("structuredValidationErrors"),
                }
        return
    raise ValueError(f"unsupported result schema {schema!r}")


def iter_result_texts(payload: dict[str, Any]) -> Iterable[tuple[str, str, str]]:
    for case in iter_result_cases(payload):
        yield case["fixture"], case["label"], case["text"]
