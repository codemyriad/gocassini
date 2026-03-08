from __future__ import annotations

import re
import subprocess
from dataclasses import dataclass
from pathlib import Path

from .pipeline import seconds_to_ms
from .timeline import TimeSpan, merge_spans

SILENCE_START_RE = re.compile(r"silence_start:\s*([0-9]+(?:\.[0-9]+)?)")
SILENCE_END_RE = re.compile(r"silence_end:\s*([0-9]+(?:\.[0-9]+)?)")


@dataclass(frozen=True, slots=True)
class TranscriptionChunk:
    source: TimeSpan
    emit: TimeSpan


def detect_active_spans(
    *,
    audio_path: Path,
    total_duration_ms: int,
    silence_noise_db: float = -35.0,
    minimum_silence_ms: int = 400,
    minimum_activity_ms: int = 250,
) -> list[TimeSpan]:
    if minimum_silence_ms < 0 or minimum_activity_ms < 0:
        raise ValueError("Activity detection thresholds must be non-negative")

    completed = subprocess.run(
        [
            "ffmpeg",
            "-hide_banner",
            "-nostats",
            "-i",
            str(audio_path),
            "-af",
            f"silencedetect=noise={silence_noise_db}dB:d={minimum_silence_ms / 1000.0}",
            "-f",
            "null",
            "-",
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    return parse_silencedetect_output(
        completed.stderr,
        total_duration_ms=total_duration_ms,
        minimum_activity_ms=minimum_activity_ms,
    )


def parse_silencedetect_output(
    output_text: str,
    *,
    total_duration_ms: int,
    minimum_activity_ms: int = 250,
) -> list[TimeSpan]:
    silences: list[TimeSpan] = []
    current_start_ms: int | None = None

    for line in output_text.splitlines():
        start_match = SILENCE_START_RE.search(line)
        if start_match:
            current_start_ms = seconds_to_ms(start_match.group(1))
            continue

        end_match = SILENCE_END_RE.search(line)
        if end_match:
            silence_end_ms = min(total_duration_ms, seconds_to_ms(end_match.group(1)))
            silence_start_ms = current_start_ms or 0
            if silence_end_ms > silence_start_ms:
                silences.append(TimeSpan(silence_start_ms, silence_end_ms))
            current_start_ms = None

    if current_start_ms is not None and current_start_ms < total_duration_ms:
        silences.append(TimeSpan(current_start_ms, total_duration_ms))

    merged_silences = merge_spans(
        [
            silence.clip(lower_ms=0, upper_ms=total_duration_ms)
            for silence in silences
            if silence.duration_ms > 0
        ]
    )
    active_spans: list[TimeSpan] = []
    cursor_ms = 0
    for silence in merged_silences:
        if silence.start_ms > cursor_ms:
            active_spans.append(TimeSpan(cursor_ms, silence.start_ms))
        cursor_ms = silence.end_ms

    if cursor_ms < total_duration_ms:
        active_spans.append(TimeSpan(cursor_ms, total_duration_ms))

    return [span for span in active_spans if span.duration_ms >= minimum_activity_ms]


def plan_transcription_chunks(
    *,
    activity_spans: list[TimeSpan],
    source_duration_ms: int,
    chunk_padding_ms: int = 200,
    max_chunk_ms: int = 25_000,
    chunk_overlap_ms: int = 500,
) -> list[TranscriptionChunk]:
    if chunk_padding_ms < 0 or max_chunk_ms <= 0 or chunk_overlap_ms < 0:
        raise ValueError("Chunk planning values must be valid")
    if chunk_overlap_ms >= max_chunk_ms:
        raise ValueError("chunk_overlap_ms must be smaller than max_chunk_ms")

    padded_spans = merge_spans(
        [
            span.pad(padding_ms=chunk_padding_ms, upper_ms=source_duration_ms).clip(
                lower_ms=0, upper_ms=source_duration_ms
            )
            for span in activity_spans
            if span.duration_ms > 0
        ]
    )
    chunks: list[TranscriptionChunk] = []
    for span in padded_spans:
        chunks.extend(
            _plan_chunks_for_span(
                span,
                max_chunk_ms=max_chunk_ms,
                chunk_overlap_ms=chunk_overlap_ms,
            )
        )
    return chunks


def _plan_chunks_for_span(
    span: TimeSpan,
    *,
    max_chunk_ms: int,
    chunk_overlap_ms: int,
) -> list[TranscriptionChunk]:
    if span.duration_ms <= max_chunk_ms:
        return [TranscriptionChunk(source=span, emit=span)]

    chunks: list[TranscriptionChunk] = []
    step_ms = max_chunk_ms - chunk_overlap_ms
    chunk_start_ms = span.start_ms
    while chunk_start_ms < span.end_ms:
        chunk_end_ms = min(span.end_ms, chunk_start_ms + max_chunk_ms)
        emit_start_ms = span.start_ms if chunk_start_ms == span.start_ms else chunk_start_ms + chunk_overlap_ms
        emit = TimeSpan(start_ms=min(emit_start_ms, chunk_end_ms), end_ms=chunk_end_ms)
        chunks.append(
            TranscriptionChunk(
                source=TimeSpan(start_ms=chunk_start_ms, end_ms=chunk_end_ms),
                emit=emit,
            )
        )
        if chunk_end_ms >= span.end_ms:
            break
        chunk_start_ms += step_ms
    return chunks
