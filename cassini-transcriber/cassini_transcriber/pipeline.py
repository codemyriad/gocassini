from __future__ import annotations

import hashlib
import json
import mimetypes
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

from .common import seconds_to_ms
from .llm import OpenWebUIChatClient, OpenWebUIConfig, build_readable_transcript_payload
from .speech_activity import TranscriptionChunk, detect_active_spans, plan_transcription_chunks
from .timeline import TimeSpan, TimelineMap, build_digest_timeline_map

TRACK_SAMPLE_RATE = 48_000
CHUNK_SAMPLE_RATE = 16_000


@dataclass(frozen=True)
class AudioStream:
    index: int
    order: int
    track_id: str
    codec_name: str
    channels: int | None
    speaker_id: str
    speaker_label: str


@dataclass(frozen=True)
class TrackWorkspace:
    stream: AudioStream
    audio_path: Path
    duration_ms: int
    activity_spans: tuple[TimeSpan, ...]


def run_command(cmd: list[str]) -> str:
    completed = subprocess.run(
        cmd,
        check=True,
        capture_output=True,
        text=True,
    )
    return completed.stdout


def probe_media(input_path: Path) -> list[AudioStream]:
    raw = run_command(
        [
            "ffprobe",
            "-v",
            "error",
            "-show_entries",
            "stream=index,codec_type,codec_name,channels:stream_tags=title",
            "-of",
            "json",
            str(input_path),
        ]
    )
    payload = json.loads(raw)

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
                "speaker_label": normalize_speaker_label(str(tags.get("title") or "").strip()),
            }
        )

    if not audio_stream_specs:
        raise RuntimeError(f"No audio streams found in {input_path}")

    labels = [
        spec["speaker_label"] or f"Speaker {position}"
        for position, spec in enumerate(audio_stream_specs, start=1)
    ]
    speaker_ids = make_speaker_ids(labels)
    track_ids = make_track_ids(labels)
    streams: list[AudioStream] = []
    for order, spec in enumerate(audio_stream_specs, start=1):
        label = labels[order - 1]
        streams.append(
            AudioStream(
                index=spec["index"],
                order=order,
                track_id=track_ids[order - 1],
                codec_name=spec["codec_name"],
                channels=int(spec["channels"]) if spec["channels"] is not None else None,
                speaker_id=speaker_ids[order - 1],
                speaker_label=label,
            )
        )
    return streams


def make_speaker_ids(labels: list[str]) -> list[str]:
    slug_counts: dict[str, int] = {}
    ids_by_label: dict[str, str] = {}
    speaker_ids: list[str] = []
    for label in labels:
        existing = ids_by_label.get(label)
        if existing is not None:
            speaker_ids.append(existing)
            continue

        stem = slugify(label)
        base = f"spk_{stem}"
        count = slug_counts.get(base, 0) + 1
        slug_counts[base] = count
        speaker_id = base if count == 1 else f"{base}_{count}"
        ids_by_label[label] = speaker_id
        speaker_ids.append(speaker_id)
    return speaker_ids


def make_track_ids(labels: list[str]) -> list[str]:
    return [f"track_{index:02d}_{slugify(label)}" for index, label in enumerate(labels, start=1)]


def normalize_speaker_label(label: str) -> str:
    normalized = re.sub(r"\s+(audio|video)\s*$", "", label, flags=re.IGNORECASE).strip()
    return normalized or label


def slugify(value: str) -> str:
    normalized = re.sub(r"[^A-Za-z0-9]+", "_", value.strip().lower()).strip("_")
    return normalized or "speaker"


def build_mixed_master_audio(track_audio_paths: list[Path], output_path: Path) -> None:
    if not track_audio_paths:
        raise RuntimeError("At least one decoded track is required to build the master mix")

    if len(track_audio_paths) == 1:
        cmd = [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(track_audio_paths[0]),
            "-map",
            "0:a",
            "-c:a",
            "pcm_s16le",
            "-ac",
            "1",
            "-ar",
            str(TRACK_SAMPLE_RATE),
            str(output_path),
        ]
    else:
        cmd = [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
        ]
        for track_audio_path in track_audio_paths:
            cmd.extend(["-i", str(track_audio_path)])

        labels = "".join(f"[{index}:a]" for index in range(len(track_audio_paths)))
        filter_complex = (
            f"{labels}amix=inputs={len(track_audio_paths)}:duration=longest:normalize=0,"
            "alimiter=limit=0.95[aout]"
        )
        cmd.extend(
            [
            "-filter_complex",
            filter_complex,
            "-map",
            "[aout]",
            "-c:a",
            "pcm_s16le",
            "-ac",
            "1",
            "-ar",
            str(TRACK_SAMPLE_RATE),
            str(output_path),
            ]
        )
    subprocess.run(cmd, check=True)


def render_digest_audio(
    master_audio_path: Path,
    output_path: Path,
    timeline_map: TimelineMap,
) -> None:
    if not timeline_map.segments:
        raise RuntimeError("Timeline map must contain at least one segment")

    filter_parts: list[str] = []
    labels: list[str] = []
    for index, segment in enumerate(timeline_map.segments):
        label = f"seg{index}"
        labels.append(f"[{label}]")
        if segment.kind == "audio":
            filter_parts.append(
                "[0:a]"
                f"atrim=start={segment.source.start_ms / 1000.0:.6f}:"
                f"end={segment.source.end_ms / 1000.0:.6f},"
                f"asetpts=PTS-STARTPTS[{label}]"
            )
            continue
        filter_parts.append(
            f"anullsrc=r=48000:cl=mono:d={segment.digest.duration_ms / 1000.0:.6f}[{label}]"
        )

    filter_parts.append(
        "".join(labels) + f"concat=n={len(labels)}:v=0:a=1[aout]"
    )
    subprocess.run(
        [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(master_audio_path),
            "-filter_complex",
            ";".join(filter_parts),
            "-map",
            "[aout]",
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
        ],
        check=True,
    )


def extract_track_audio(input_path: Path, output_path: Path, stream: AudioStream) -> None:
    # Preserve sparse packet-time gaps so the decoded WAV stays on the meeting timeline.
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
            "-af",
            "aresample=async=1:first_pts=0",
            "-ac",
            "1",
            "-ar",
            str(TRACK_SAMPLE_RATE),
            "-c:a",
            "pcm_s16le",
            str(output_path),
        ],
        check=True,
    )


def extract_audio_span(input_path: Path, output_path: Path, span: TimeSpan) -> None:
    subprocess.run(
        [
            "ffmpeg",
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-i",
            str(input_path),
            "-ss",
            f"{span.start_ms / 1000.0:.6f}",
            "-to",
            f"{span.end_ms / 1000.0:.6f}",
            "-ac",
            "1",
            "-ar",
            str(CHUNK_SAMPLE_RATE),
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


def analyze_tracks(
    *,
    input_path: Path,
    audio_streams: list[AudioStream],
    tracks_dir: Path,
    silence_noise_db: float,
    minimum_silence_ms: int,
    minimum_activity_ms: int,
) -> list[TrackWorkspace]:
    tracks_dir.mkdir(parents=True, exist_ok=True)
    workspaces: list[TrackWorkspace] = []
    for stream in audio_streams:
        track_audio_path = tracks_dir / f"{stream.order:02d}-{stream.track_id}.wav"
        extract_track_audio(input_path, track_audio_path, stream)
        track_duration_ms = probe_duration_ms(track_audio_path)
        activity_spans = detect_active_spans(
            audio_path=track_audio_path,
            total_duration_ms=track_duration_ms,
            silence_noise_db=silence_noise_db,
            minimum_silence_ms=minimum_silence_ms,
            minimum_activity_ms=minimum_activity_ms,
        )
        workspaces.append(
            TrackWorkspace(
                stream=stream,
                audio_path=track_audio_path,
                duration_ms=track_duration_ms,
                activity_spans=tuple(activity_spans),
            )
        )
    return workspaces


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


def build_meeting_activity_spans(
    track_workspaces: list[TrackWorkspace],
    source_duration_ms: int,
) -> list[TimeSpan]:
    spans: list[TimeSpan] = []
    for workspace in track_workspaces:
        for span in workspace.activity_spans:
            absolute = span.clip(lower_ms=0, upper_ms=source_duration_ms)
            if absolute.duration_ms > 0:
                spans.append(absolute)
    return spans


def filter_words_to_time_window(
    words: list[dict[str, Any]],
    *,
    window_start_ms: int,
    window_end_ms: int,
) -> list[dict[str, Any]]:
    filtered: list[dict[str, Any]] = []
    for word in words:
        midpoint_ms = (int(word["startMs"]) + int(word["endMs"])) // 2
        if window_start_ms <= midpoint_ms < window_end_ms:
            filtered.append(word)
    return filtered


def remap_words_to_digest_timeline(
    words: list[dict[str, Any]],
    timeline_map: TimelineMap,
) -> list[dict[str, Any]]:
    remapped: list[dict[str, Any]] = []
    for word in words:
        start_ms = timeline_map.map_source_to_digest(int(word["startMs"]))
        end_ms = timeline_map.map_source_to_digest(int(word["endMs"]))
        remapped_word = {
            "text": word["text"],
            "startMs": start_ms,
            "endMs": max(start_ms, end_ms),
        }
        if "confidence" in word:
            remapped_word["confidence"] = word["confidence"]
        remapped.append(remapped_word)
    return remapped


def transcribe_track_chunks(
    *,
    workspace: TrackWorkspace,
    chunks: list[TranscriptionChunk],
    chunks_dir: Path,
    responses_dir: Path,
    transcriber_url: str,
    timeout_seconds: int,
    timeline_map: TimelineMap,
) -> list[dict[str, Any]]:
    speaker_chunks_dir = chunks_dir / workspace.stream.track_id
    speaker_responses_dir = responses_dir / workspace.stream.track_id
    speaker_chunks_dir.mkdir(parents=True, exist_ok=True)
    speaker_responses_dir.mkdir(parents=True, exist_ok=True)

    remapped_words: list[dict[str, Any]] = []
    for index, chunk in enumerate(chunks, start=1):
        chunk_audio_path = speaker_chunks_dir / f"chunk-{index:04d}.wav"
        response_path = speaker_responses_dir / f"chunk-{index:04d}.json"
        extract_audio_span(workspace.audio_path, chunk_audio_path, chunk.source)
        raw_response = transcribe_file(chunk_audio_path, transcriber_url, timeout_seconds)
        response_path.write_text(json.dumps(raw_response, indent=2), encoding="utf-8")

        normalized_words = normalize_words(
            raw_response.get("words") or [],
            start_offset_ms=chunk.source.start_ms,
        )
        filtered_words = filter_words_to_time_window(
            normalized_words,
            window_start_ms=chunk.emit.start_ms,
            window_end_ms=chunk.emit.end_ms,
        )
        remapped_words.extend(remap_words_to_digest_timeline(filtered_words, timeline_map))

    remapped_words.sort(key=lambda word: (word["startMs"], word["endMs"], word["text"]))
    return remapped_words


def serialize_timeline_map(timeline_map: TimelineMap) -> dict[str, Any]:
    return {
        "version": "timeline.map.v1",
        "sourceDurationMs": timeline_map.source_duration_ms,
        "digestDurationMs": timeline_map.digest_duration_ms,
        "segments": [
            {
                "kind": segment.kind,
                "sourceStartMs": segment.source.start_ms,
                "sourceEndMs": segment.source.end_ms,
                "digestStartMs": segment.digest.start_ms,
                "digestEndMs": segment.digest.end_ms,
            }
            for segment in timeline_map.segments
        ],
    }


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
    readable_name: str | None,
    captions_name: str,
    timeline_name: str | None,
    speaker_count: int,
    chunk_count: int,
    segment_count: int,
    word_count: int,
    timeline_segment_count: int,
    source_duration_ms: int,
    digest_duration_ms: int,
) -> dict[str, Any]:
    files: dict[str, str] = {
        "audio": audio_name,
        "transcript": transcript_name,
        "captions": captions_name,
    }
    if readable_name:
        files["readableTranscript"] = readable_name
    if timeline_name:
        files["timeline"] = timeline_name

    return {
        "version": "cassini.meeting-artifact.v1",
        "generatedAt": datetime.now(timezone.utc).isoformat(),
        "source": {
            "basename": source_path.name,
            "durationMs": source_duration_ms,
        },
        "files": files,
        "speakerCount": speaker_count,
        "chunkCount": chunk_count,
        "segmentCount": segment_count,
        "wordCount": word_count,
        "timelineSegmentCount": timeline_segment_count,
        "digestDurationMs": digest_duration_ms,
        "silenceReductionMs": max(0, source_duration_ms - digest_duration_ms),
    }


def build_meeting_artifact(
    *,
    input_path: Path,
    output_dir: Path,
    transcriber_url: str,
    audio_name: str = "meeting.webm",
    transcript_name: str = "transcript.words.v1.json",
    readable_transcript_name: str | None = None,
    captions_name: str = "captions.vtt",
    manifest_name: str | None = "manifest.json",
    timeline_name: str | None = "timeline.map.v1.json",
    timeout_seconds: int = 3600,
    segment_gap_ms: int = 900,
    max_segment_ms: int = 15_000,
    max_segment_words: int = 32,
    silence_noise_db: float = -35.0,
    minimum_silence_ms: int = 400,
    minimum_activity_ms: int = 250,
    activity_padding_ms: int = 200,
    keep_silence_ms: int = 900,
    compress_silence_to_ms: int = 800,
    chunk_padding_ms: int = 200,
    max_chunk_ms: int = 25_000,
    chunk_overlap_ms: int = 500,
    max_bridge_gap_ms: int = 1_500,
    openwebui_base_url: str | None = None,
    openwebui_email: str | None = None,
    openwebui_password: str | None = None,
    openwebui_model: str | None = None,
    openwebui_timeout_seconds: int = 240,
    readable_max_gap_ms: int = 1_800,
    readable_max_window_ms: int = 45_000,
    readable_max_window_words: int = 120,
    work_dir: Path | None = None,
    keep_work_dir: bool = False,
) -> dict[str, Any]:
    if not shutil.which("ffmpeg") or not shutil.which("ffprobe"):
        raise RuntimeError("ffmpeg and ffprobe must be available on PATH")

    input_path = input_path.resolve()
    output_dir = output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    audio_streams = probe_media(input_path)
    speaker_order: dict[str, int] = {}
    speakers: list[dict[str, str]] = []
    seen_speakers: set[str] = set()
    for stream in audio_streams:
        speaker_order.setdefault(stream.speaker_id, stream.order)
        if stream.speaker_id in seen_speakers:
            continue
        seen_speakers.add(stream.speaker_id)
        speakers.append({"id": stream.speaker_id, "label": stream.speaker_label})

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
        readable_output_path = (
            output_dir / readable_transcript_name if readable_transcript_name else None
        )
        captions_output_path = output_dir / captions_name
        manifest_output_path = output_dir / manifest_name if manifest_name else None
        timeline_output_path = output_dir / timeline_name if timeline_name else None

        tracks_dir = active_work_dir / "tracks"
        chunks_dir = active_work_dir / "chunks"
        responses_dir = active_work_dir / "responses"
        master_audio_path = active_work_dir / "mixed-master.wav"

        track_workspaces = analyze_tracks(
            input_path=input_path,
            audio_streams=audio_streams,
            tracks_dir=tracks_dir,
            silence_noise_db=silence_noise_db,
            minimum_silence_ms=minimum_silence_ms,
            minimum_activity_ms=minimum_activity_ms,
        )
        build_mixed_master_audio(
            [workspace.audio_path for workspace in track_workspaces],
            master_audio_path,
        )
        source_duration_ms = probe_duration_ms(master_audio_path)
        meeting_activity_spans = build_meeting_activity_spans(
            track_workspaces,
            source_duration_ms=source_duration_ms,
        )
        timeline_map = build_digest_timeline_map(
            activity_spans=meeting_activity_spans,
            source_duration_ms=source_duration_ms,
            activity_padding_ms=activity_padding_ms,
            keep_silence_ms=keep_silence_ms,
            compress_silence_to_ms=compress_silence_to_ms,
        )
        render_digest_audio(master_audio_path, audio_output_path, timeline_map)
        audio_duration_ms = probe_duration_ms(audio_output_path)
        audio_sha256 = sha256_file(audio_output_path)

        all_segments: list[dict[str, Any]] = []
        chunk_count = 0
        for workspace in track_workspaces:
            transcription_chunks = plan_transcription_chunks(
                activity_spans=list(workspace.activity_spans),
                source_duration_ms=workspace.duration_ms,
                chunk_padding_ms=chunk_padding_ms,
                max_chunk_ms=max_chunk_ms,
                chunk_overlap_ms=chunk_overlap_ms,
                max_bridge_gap_ms=max_bridge_gap_ms,
            )
            chunk_count += len(transcription_chunks)
            normalized_words = transcribe_track_chunks(
                workspace=workspace,
                chunks=transcription_chunks,
                chunks_dir=chunks_dir,
                responses_dir=responses_dir,
                transcriber_url=transcriber_url,
                timeout_seconds=timeout_seconds,
                timeline_map=timeline_map,
            )
            speaker_segments = segment_word_items(
                normalized_words,
                speaker_id=workspace.stream.speaker_id,
                gap_ms=segment_gap_ms,
                max_segment_ms=max_segment_ms,
                max_segment_words=max_segment_words,
            )
            all_segments.extend(speaker_segments)

        finalized_segments = assign_ids(all_segments, speaker_order)
        word_count = sum(len(segment["words"]) for segment in finalized_segments)
        transcript_payload = build_transcript_payload(
            audio_name=audio_name,
            audio_duration_ms=audio_duration_ms,
            audio_sha256=audio_sha256,
            speakers=speakers,
            segments=finalized_segments,
        )
        captions_vtt = render_captions_vtt(transcript_payload)
        readable_payload: dict[str, Any] | None = None
        if readable_output_path is not None:
            missing_openwebui = [
                name
                for name, value in (
                    ("openwebui_base_url", openwebui_base_url),
                    ("openwebui_email", openwebui_email),
                    ("openwebui_password", openwebui_password),
                    ("openwebui_model", openwebui_model),
                )
                if not value
            ]
            if missing_openwebui:
                raise ValueError(
                    "Readable transcript generation requires Open WebUI settings: "
                    + ", ".join(missing_openwebui)
                )
            readable_payload = build_readable_transcript_payload(
                transcript_payload=transcript_payload,
                client=OpenWebUIChatClient(
                    OpenWebUIConfig(
                        base_url=str(openwebui_base_url),
                        email=str(openwebui_email),
                        password=str(openwebui_password),
                        model=str(openwebui_model),
                        timeout_seconds=openwebui_timeout_seconds,
                    )
                ),
                max_gap_ms=readable_max_gap_ms,
                max_window_ms=readable_max_window_ms,
                max_window_words=readable_max_window_words,
            )

        transcript_output_path.write_text(
            json.dumps(transcript_payload, indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )
        if readable_output_path is not None and readable_payload is not None:
            readable_output_path.write_text(
                json.dumps(readable_payload, indent=2, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )
        captions_output_path.write_text(captions_vtt, encoding="utf-8")
        if timeline_output_path is not None:
            timeline_output_path.write_text(
                json.dumps(serialize_timeline_map(timeline_map), indent=2, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )

        if manifest_output_path is not None:
            manifest = build_manifest(
                source_path=input_path,
                audio_name=audio_name,
                transcript_name=transcript_name,
                readable_name=readable_transcript_name,
                captions_name=captions_name,
                timeline_name=timeline_name,
                speaker_count=len(speakers),
                chunk_count=chunk_count,
                segment_count=len(finalized_segments),
                word_count=word_count,
                timeline_segment_count=len(timeline_map.segments),
                source_duration_ms=source_duration_ms,
                digest_duration_ms=audio_duration_ms,
            )
            manifest_output_path.write_text(
                json.dumps(manifest, indent=2, ensure_ascii=False) + "\n",
                encoding="utf-8",
            )

        return {
            "audio_path": str(audio_output_path),
            "transcript_path": str(transcript_output_path),
            "readable_transcript_path": str(readable_output_path) if readable_output_path else None,
            "captions_path": str(captions_output_path),
            "timeline_path": str(timeline_output_path) if timeline_output_path else None,
            "manifest_path": str(manifest_output_path) if manifest_output_path else None,
            "speaker_count": len(speakers),
            "segment_count": len(finalized_segments),
            "word_count": word_count,
            "chunk_count": chunk_count,
            "source_duration_ms": source_duration_ms,
            "duration_ms": audio_duration_ms,
            "reduction_ms": max(0, source_duration_ms - audio_duration_ms),
            "timeline_segment_count": len(timeline_map.segments),
            "work_dir": str(active_work_dir),
        }
    finally:
        if temporary_root is not None:
            temporary_root.cleanup()
