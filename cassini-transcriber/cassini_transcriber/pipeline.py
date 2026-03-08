from __future__ import annotations

import hashlib
import json
import mimetypes
import os
import re
import shutil
import subprocess
import tempfile
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


@dataclass(frozen=True)
class AudioStream:
    index: int
    order: int
    codec_name: str
    channels: int | None
    speaker_id: str
    speaker_label: str
    start_ms: int
    duration_ms: int | None


def run_command(cmd: list[str]) -> str:
    completed = subprocess.run(
        cmd,
        check=True,
        capture_output=True,
        text=True,
    )
    return completed.stdout


def probe_media(input_path: Path) -> tuple[list[AudioStream], int | None]:
    raw = run_command(
        [
            "ffprobe",
            "-v",
            "error",
            "-show_entries",
            "format=duration:stream=index,codec_type,codec_name,channels,start_time,duration:stream_tags=title",
            "-of",
            "json",
            str(input_path),
        ]
    )
    payload = json.loads(raw)
    format_duration = payload.get("format", {}).get("duration")
    input_duration_ms = seconds_to_ms(format_duration) if format_duration is not None else None

    audio_stream_specs: list[dict[str, Any]] = []
    for stream in payload.get("streams", []):
        if stream.get("codec_type") != "audio":
            continue
        tags = stream.get("tags") or {}
        audio_stream_specs.append(
            {
                "index": int(stream["index"]),
                "codec_name": stream.get("codec_name") or "unknown",
                "channels": stream.get("channels"),
                "speaker_label": str(tags.get("title") or "").strip(),
                "start_ms": seconds_to_ms(stream.get("start_time") or 0),
                "duration_ms": (
                    seconds_to_ms(stream.get("duration"))
                    if stream.get("duration") is not None
                    else None
                ),
            }
        )

    if not audio_stream_specs:
        raise RuntimeError(f"No audio streams found in {input_path}")

    speaker_ids = make_speaker_ids(
        [
            spec["speaker_label"] or f"Speaker {position}"
            for position, spec in enumerate(audio_stream_specs, start=1)
        ]
    )
    streams: list[AudioStream] = []
    for order, spec in enumerate(audio_stream_specs, start=1):
        label = spec["speaker_label"] or f"Speaker {order}"
        streams.append(
            AudioStream(
                index=spec["index"],
                order=order,
                codec_name=spec["codec_name"],
                channels=int(spec["channels"]) if spec["channels"] is not None else None,
                speaker_id=speaker_ids[order - 1],
                speaker_label=label,
                start_ms=int(spec["start_ms"]),
                duration_ms=spec["duration_ms"],
            )
        )
    return streams, input_duration_ms


def make_speaker_ids(labels: list[str]) -> list[str]:
    counts: dict[str, int] = {}
    speaker_ids: list[str] = []
    for label in labels:
        stem = slugify(label)
        base = f"spk_{stem}"
        count = counts.get(base, 0) + 1
        counts[base] = count
        speaker_ids.append(base if count == 1 else f"{base}_{count}")
    return speaker_ids


def slugify(value: str) -> str:
    normalized = re.sub(r"[^A-Za-z0-9]+", "_", value.strip().lower()).strip("_")
    return normalized or "speaker"


def seconds_to_ms(value: Any) -> int:
    return max(0, int(round(float(value) * 1000)))


def build_audio_derivative(input_path: Path, output_path: Path, audio_streams: list[AudioStream]) -> None:
    if len(audio_streams) == 1:
        cmd = [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(input_path),
            "-map",
            f"0:{audio_streams[0].index}",
            "-vn",
            "-sn",
            "-dn",
            "-c:a",
            "libopus",
            "-b:a",
            "64k",
            "-vbr",
            "on",
            "-compression_level",
            "10",
            "-application",
            "voip",
            "-ac",
            "1",
            "-ar",
            "48000",
            str(output_path),
        ]
    else:
        labels = "".join(f"[0:{stream.index}]" for stream in audio_streams)
        filter_complex = f"{labels}amix=inputs={len(audio_streams)}:normalize=0,alimiter=limit=0.95[aout]"
        cmd = [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(input_path),
            "-filter_complex",
            filter_complex,
            "-map",
            "[aout]",
            "-vn",
            "-sn",
            "-dn",
            "-c:a",
            "libopus",
            "-b:a",
            "64k",
            "-vbr",
            "on",
            "-compression_level",
            "10",
            "-application",
            "voip",
            "-ac",
            "1",
            "-ar",
            "48000",
            str(output_path),
        ]
    subprocess.run(cmd, check=True)


def extract_track_audio(input_path: Path, output_path: Path, stream: AudioStream) -> None:
    subprocess.run(
        [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(input_path),
            "-map",
            f"0:{stream.index}",
            "-vn",
            "-sn",
            "-dn",
            "-ac",
            "1",
            "-ar",
            "16000",
            "-c:a",
            "pcm_s16le",
            str(output_path),
        ],
        check=True,
    )


def probe_duration_ms(path: Path) -> int:
    raw = run_command(
        [
            "ffprobe",
            "-v",
            "error",
            "-show_entries",
            "format=duration",
            "-of",
            "json",
            str(path),
        ]
    )
    payload = json.loads(raw)
    duration = payload.get("format", {}).get("duration")
    if duration is None:
        raise RuntimeError(f"Could not read duration for {path}")
    return seconds_to_ms(duration)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def transcribe_file(audio_path: Path, transcriber_url: str, timeout_seconds: int) -> dict[str, Any]:
    mime_type = mimetypes.guess_type(audio_path.name)[0] or "application/octet-stream"
    boundary = uuid.uuid4().hex
    with audio_path.open("rb") as handle:
        audio_bytes = handle.read()

    parts = [
        f"--{boundary}\r\n".encode("ascii"),
        (
            f'Content-Disposition: form-data; name="file"; filename="{audio_path.name}"\r\n'
        ).encode("utf-8"),
        f"Content-Type: {mime_type}\r\n\r\n".encode("ascii"),
        audio_bytes,
        b"\r\n",
        f"--{boundary}--\r\n".encode("ascii"),
    ]
    payload = b"".join(parts)
    request = urllib.request.Request(
        transcriber_url,
        data=payload,
        headers={
            "Accept": "application/json",
            "Content-Type": f"multipart/form-data; boundary={boundary}",
            "Content-Length": str(len(payload)),
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
            body = response.read()
    except urllib.error.HTTPError as exc:
        details = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(
            f"Transcription request failed for {audio_path.name}: HTTP {exc.code}: {details}"
        ) from exc
    return json.loads(body.decode("utf-8"))


def normalize_words(raw_words: list[dict[str, Any]], start_offset_ms: int) -> list[dict[str, Any]]:
    normalized: list[dict[str, Any]] = []
    for raw_word in raw_words:
        text = str(raw_word.get("word") or raw_word.get("text") or "").strip()
        if not text:
            continue
        if "startMs" in raw_word:
            start_ms = max(0, int(raw_word["startMs"])) + start_offset_ms
        else:
            start_ms = seconds_to_ms(raw_word.get("start", 0)) + start_offset_ms
        if "endMs" in raw_word:
            end_ms = max(start_ms, int(raw_word["endMs"]) + start_offset_ms)
        else:
            end_ms = max(start_ms, seconds_to_ms(raw_word.get("end", 0)) + start_offset_ms)
        normalized_word = {
            "text": text,
            "startMs": start_ms,
            "endMs": end_ms,
        }
        confidence = raw_word.get("confidence")
        if isinstance(confidence, (float, int)):
            normalized_word["confidence"] = float(confidence)
        normalized.append(normalized_word)
    return normalized


def segment_word_items(
    words: list[dict[str, Any]],
    speaker_id: str,
    gap_ms: int,
    max_segment_ms: int,
    max_segment_words: int,
) -> list[dict[str, Any]]:
    if not words:
        return []

    segments: list[dict[str, Any]] = []
    current_words: list[dict[str, Any]] = []

    def flush_current() -> None:
        nonlocal current_words
        if not current_words:
            return
        segment = {
            "speaker": speaker_id,
            "startMs": current_words[0]["startMs"],
            "endMs": max(word["endMs"] for word in current_words),
            "text": " ".join(word["text"] for word in current_words).strip(),
            "words": [dict(word) for word in current_words],
        }
        segments.append(segment)
        current_words = []

    for word in words:
        if current_words:
            previous = current_words[-1]
            current_duration = word["endMs"] - current_words[0]["startMs"]
            gap = word["startMs"] - previous["endMs"]
            should_split = (
                gap > gap_ms
                or len(current_words) >= max_segment_words
                or current_duration > max_segment_ms
            )
            if should_split:
                flush_current()
        current_words.append(word)

    flush_current()
    return segments


def assign_ids(
    segments: list[dict[str, Any]],
    speaker_order: dict[str, int],
) -> list[dict[str, Any]]:
    sorted_segments = sorted(
        segments,
        key=lambda segment: (
            segment["startMs"],
            segment["endMs"],
            speaker_order.get(segment.get("speaker", ""), 10_000),
            segment["text"],
        ),
    )
    next_word_id = 1
    finalized_segments: list[dict[str, Any]] = []
    for segment_index, segment in enumerate(sorted_segments, start=1):
        finalized_words: list[dict[str, Any]] = []
        for word in segment["words"]:
            finalized_word = {
                "id": f"w_{next_word_id:07d}",
                "text": word["text"],
                "startMs": int(word["startMs"]),
                "endMs": int(word["endMs"]),
            }
            if "confidence" in word:
                finalized_word["confidence"] = word["confidence"]
            finalized_words.append(finalized_word)
            next_word_id += 1
        finalized_segments.append(
            {
                "id": f"seg_{segment_index:06d}",
                "speaker": segment.get("speaker"),
                "startMs": int(segment["startMs"]),
                "endMs": int(segment["endMs"]),
                "text": segment["text"],
                "words": finalized_words,
            }
        )
    return finalized_segments


def build_transcript_payload(
    audio_name: str,
    audio_duration_ms: int,
    audio_sha256: str,
    speakers: list[dict[str, str]],
    segments: list[dict[str, Any]],
) -> dict[str, Any]:
    payload = {
        "version": "transcript.words.v1",
        "media": {
            "src": audio_name,
            "durationMs": audio_duration_ms,
            "sha256": audio_sha256,
        },
        "speakers": speakers,
        "segments": segments,
    }
    validate_transcript_payload(payload, audio_duration_ms)
    return payload


def validate_transcript_payload(payload: dict[str, Any], actual_audio_duration_ms: int) -> None:
    if payload.get("version") != "transcript.words.v1":
        raise ValueError("Unsupported transcript version")

    media = payload.get("media") or {}
    duration_ms = media.get("durationMs")
    if not isinstance(duration_ms, int) or duration_ms < 0:
        raise ValueError("media.durationMs must be a non-negative integer")
    if abs(duration_ms - actual_audio_duration_ms) > 1000:
        raise ValueError(
            f"media.durationMs ({duration_ms}) does not match audio duration "
            f"({actual_audio_duration_ms})"
        )

    speaker_ids = [speaker.get("id") for speaker in payload.get("speakers", [])]
    if len(speaker_ids) != len(set(speaker_ids)):
        raise ValueError("Speaker IDs must be unique")
    known_speakers = set(speaker_ids)

    last_sort_key: tuple[int, int, str] | None = None
    for segment in payload.get("segments", []):
        start_ms = segment.get("startMs")
        end_ms = segment.get("endMs")
        if not isinstance(start_ms, int) or not isinstance(end_ms, int) or start_ms > end_ms:
            raise ValueError(f"Invalid segment bounds: {segment}")
        speaker = segment.get("speaker")
        if speaker is not None and speaker not in known_speakers:
            raise ValueError(f"Segment references unknown speaker: {speaker}")
        current_sort_key = (start_ms, end_ms, segment.get("id", ""))
        if last_sort_key and current_sort_key < last_sort_key:
            raise ValueError("Segments must be in stable time order")
        last_sort_key = current_sort_key

        words = segment.get("words")
        if not isinstance(words, list):
            raise ValueError("Segment words must be a list")
        for word in words:
            word_start = word.get("startMs")
            word_end = word.get("endMs")
            if (
                not isinstance(word_start, int)
                or not isinstance(word_end, int)
                or word_start > word_end
            ):
                raise ValueError(f"Invalid word bounds: {word}")
            if word_start < start_ms - 500 or word_end > end_ms + 500:
                raise ValueError(f"Word timing falls outside expected segment bounds: {word}")


def render_captions_vtt(
    transcript_payload: dict[str, Any],
    include_speaker_labels: bool = True,
) -> str:
    speaker_labels = {
        speaker["id"]: speaker["label"]
        for speaker in transcript_payload.get("speakers", [])
        if "id" in speaker and "label" in speaker
    }
    segments = list(transcript_payload.get("segments", []))
    lines = ["WEBVTT", ""]
    for index, segment in enumerate(segments, start=1):
        start_ms = int(segment["startMs"])
        end_ms = int(segment["endMs"])
        if index < len(segments):
            next_start = int(segments[index]["startMs"])
            if next_start > start_ms:
                end_ms = min(end_ms, next_start)
        if end_ms <= start_ms:
            end_ms = start_ms + 1

        speaker_text = ""
        speaker = segment.get("speaker")
        if include_speaker_labels and speaker in speaker_labels:
            speaker_text = f"{speaker_labels[speaker]}: "

        lines.append(str(index))
        lines.append(f"{format_vtt_timestamp(start_ms)} --> {format_vtt_timestamp(end_ms)}")
        lines.append(f"{speaker_text}{segment['text']}".strip())
        lines.append("")
    return "\n".join(lines)


def format_vtt_timestamp(milliseconds: int) -> str:
    hours, remainder = divmod(milliseconds, 3_600_000)
    minutes, remainder = divmod(remainder, 60_000)
    seconds, millis = divmod(remainder, 1000)
    return f"{hours:02d}:{minutes:02d}:{seconds:02d}.{millis:03d}"


def build_manifest(
    source_path: Path,
    audio_name: str,
    transcript_name: str,
    captions_name: str,
    speaker_count: int,
) -> dict[str, Any]:
    return {
        "version": "cassini.meeting-artifact.v1",
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "source": {
            "basename": source_path.name,
        },
        "files": {
            "audio": audio_name,
            "transcript": transcript_name,
            "captions": captions_name,
        },
        "speakerCount": speaker_count,
    }


def build_meeting_artifact(
    *,
    input_path: Path,
    output_dir: Path,
    transcriber_url: str,
    audio_name: str = "meeting.webm",
    transcript_name: str = "transcript.words.v1.json",
    captions_name: str = "captions.vtt",
    manifest_name: str | None = "manifest.json",
    timeout_seconds: int = 3600,
    segment_gap_ms: int = 900,
    max_segment_ms: int = 15_000,
    max_segment_words: int = 32,
    work_dir: Path | None = None,
    keep_work_dir: bool = False,
) -> dict[str, Any]:
    if not shutil.which("ffmpeg") or not shutil.which("ffprobe"):
        raise RuntimeError("ffmpeg and ffprobe must be available on PATH")

    input_path = input_path.resolve()
    output_dir = output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    audio_streams, _ = probe_media(input_path)
    speaker_order = {stream.speaker_id: stream.order for stream in audio_streams}
    speakers = [
        {"id": stream.speaker_id, "label": stream.speaker_label}
        for stream in audio_streams
    ]

    temporary_root: tempfile.TemporaryDirectory[str] | None = None
    if work_dir is not None:
        active_work_dir = work_dir.resolve()
        active_work_dir.mkdir(parents=True, exist_ok=True)
    elif keep_work_dir:
        active_work_dir = output_dir / "_work"
        active_work_dir.mkdir(parents=True, exist_ok=True)
    else:
        temporary_root = tempfile.TemporaryDirectory(prefix="cassini-transcriber-")
        active_work_dir = Path(temporary_root.name)

    try:
        audio_output_path = output_dir / audio_name
        transcript_output_path = output_dir / transcript_name
        captions_output_path = output_dir / captions_name
        manifest_output_path = output_dir / manifest_name if manifest_name else None

        build_audio_derivative(input_path, audio_output_path, audio_streams)
        audio_duration_ms = probe_duration_ms(audio_output_path)
        audio_sha256 = sha256_file(audio_output_path)

        all_segments: list[dict[str, Any]] = []
        for stream in audio_streams:
            track_audio_path = active_work_dir / f"{stream.order:02d}-{stream.speaker_id}.wav"
            track_response_path = active_work_dir / f"{stream.order:02d}-{stream.speaker_id}.json"
            extract_track_audio(input_path, track_audio_path, stream)
            raw_response = transcribe_file(track_audio_path, transcriber_url, timeout_seconds)
            track_response_path.write_text(json.dumps(raw_response, indent=2), encoding="utf-8")
            normalized_words = normalize_words(raw_response.get("words") or [], stream.start_ms)
            speaker_segments = segment_word_items(
                normalized_words,
                speaker_id=stream.speaker_id,
                gap_ms=segment_gap_ms,
                max_segment_ms=max_segment_ms,
                max_segment_words=max_segment_words,
            )
            all_segments.extend(speaker_segments)

        finalized_segments = assign_ids(all_segments, speaker_order)
        transcript_payload = build_transcript_payload(
            audio_name=audio_name,
            audio_duration_ms=audio_duration_ms,
            audio_sha256=audio_sha256,
            speakers=speakers,
            segments=finalized_segments,
        )
        captions_vtt = render_captions_vtt(transcript_payload)

        transcript_output_path.write_text(
            json.dumps(transcript_payload, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )
        captions_output_path.write_text(captions_vtt, encoding="utf-8")

        if manifest_output_path is not None:
            manifest = build_manifest(
                source_path=input_path,
                audio_name=audio_name,
                transcript_name=transcript_name,
                captions_name=captions_name,
                speaker_count=len(speakers),
            )
            manifest_output_path.write_text(
                json.dumps(manifest, indent=2, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )

        return {
            "audio_path": str(audio_output_path),
            "transcript_path": str(transcript_output_path),
            "captions_path": str(captions_output_path),
            "manifest_path": str(manifest_output_path) if manifest_output_path else None,
            "speaker_count": len(speakers),
            "segment_count": len(finalized_segments),
            "duration_ms": audio_duration_ms,
            "work_dir": str(active_work_dir),
        }
    finally:
        if temporary_root is not None:
            temporary_root.cleanup()

