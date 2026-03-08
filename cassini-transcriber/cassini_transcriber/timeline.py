from __future__ import annotations

from dataclasses import dataclass
from typing import Literal


TimelineKind = Literal["audio", "silence"]


@dataclass(frozen=True, slots=True)
class TimeSpan:
    """Half-open interval on a timeline: [start_ms, end_ms)."""

    start_ms: int
    end_ms: int

    def __post_init__(self) -> None:
        if self.start_ms < 0 or self.end_ms < 0:
            raise ValueError("TimeSpan values must be non-negative")
        if self.end_ms < self.start_ms:
            raise ValueError("TimeSpan end must be >= start")

    @property
    def duration_ms(self) -> int:
        return self.end_ms - self.start_ms

    def clip(self, *, lower_ms: int, upper_ms: int) -> "TimeSpan":
        start_ms = min(max(self.start_ms, lower_ms), upper_ms)
        end_ms = min(max(self.end_ms, lower_ms), upper_ms)
        if end_ms < start_ms:
            end_ms = start_ms
        return TimeSpan(start_ms=start_ms, end_ms=end_ms)

    def pad(self, *, padding_ms: int, upper_ms: int) -> "TimeSpan":
        return TimeSpan(
            start_ms=max(0, self.start_ms - padding_ms),
            end_ms=min(upper_ms, self.end_ms + padding_ms),
        )


@dataclass(frozen=True, slots=True)
class TimelineSegment:
    kind: TimelineKind
    source: TimeSpan
    digest: TimeSpan


@dataclass(frozen=True, slots=True)
class TimelineMap:
    source_duration_ms: int
    digest_duration_ms: int
    segments: tuple[TimelineSegment, ...]

    def map_source_to_digest(self, source_ms: int) -> int:
        if source_ms <= 0:
            return 0
        if source_ms >= self.source_duration_ms:
            return self.digest_duration_ms

        for segment in self.segments:
            if segment.source.start_ms <= source_ms < segment.source.end_ms:
                return _map_within_segment(segment, source_ms)

        raise ValueError(f"Source time {source_ms}ms is outside the timeline map")


def _map_within_segment(segment: TimelineSegment, source_ms: int) -> int:
    if segment.source.duration_ms == 0:
        return segment.digest.start_ms

    offset_ms = source_ms - segment.source.start_ms
    if segment.kind == "audio" or segment.source.duration_ms == segment.digest.duration_ms:
        return segment.digest.start_ms + offset_ms

    ratio = offset_ms / segment.source.duration_ms
    mapped_offset_ms = int(round(ratio * segment.digest.duration_ms))
    return min(segment.digest.end_ms, segment.digest.start_ms + mapped_offset_ms)


def merge_spans(spans: list[TimeSpan]) -> list[TimeSpan]:
    if not spans:
        return []

    ordered = sorted(spans, key=lambda span: (span.start_ms, span.end_ms))
    merged: list[TimeSpan] = [ordered[0]]
    for span in ordered[1:]:
        last = merged[-1]
        if span.start_ms <= last.end_ms:
            merged[-1] = TimeSpan(start_ms=last.start_ms, end_ms=max(last.end_ms, span.end_ms))
            continue
        merged.append(span)
    return merged


def build_digest_timeline_map(
    *,
    activity_spans: list[TimeSpan],
    source_duration_ms: int,
    activity_padding_ms: int = 200,
    keep_silence_ms: int = 900,
    compress_silence_to_ms: int = 800,
) -> TimelineMap:
    if source_duration_ms < 0:
        raise ValueError("source_duration_ms must be non-negative")
    if activity_padding_ms < 0 or keep_silence_ms < 0 or compress_silence_to_ms < 0:
        raise ValueError("Timeline tuning values must be non-negative")

    clipped_activity = [
        span.pad(padding_ms=activity_padding_ms, upper_ms=source_duration_ms).clip(
            lower_ms=0, upper_ms=source_duration_ms
        )
        for span in activity_spans
        if span.duration_ms > 0
    ]
    merged_activity = merge_spans(clipped_activity)

    segments: list[TimelineSegment] = []
    digest_cursor_ms = 0
    source_cursor_ms = 0

    def append_segment(kind: TimelineKind, source: TimeSpan, digest_duration_ms: int) -> None:
        nonlocal digest_cursor_ms
        if source.duration_ms == 0 and digest_duration_ms == 0:
            return
        digest = TimeSpan(
            start_ms=digest_cursor_ms,
            end_ms=digest_cursor_ms + digest_duration_ms,
        )
        segments.append(TimelineSegment(kind=kind, source=source, digest=digest))
        digest_cursor_ms = digest.end_ms

    if not merged_activity:
        gap = TimeSpan(start_ms=0, end_ms=source_duration_ms)
        digest_duration_ms = _compress_gap_duration(
            gap.duration_ms,
            keep_silence_ms=keep_silence_ms,
            compress_silence_to_ms=compress_silence_to_ms,
        )
        append_segment("silence", gap, digest_duration_ms)
        return TimelineMap(
            source_duration_ms=source_duration_ms,
            digest_duration_ms=digest_cursor_ms,
            segments=tuple(segments),
        )

    for activity in merged_activity:
        if activity.start_ms > source_cursor_ms:
            gap = TimeSpan(start_ms=source_cursor_ms, end_ms=activity.start_ms)
            append_segment(
                "silence",
                gap,
                _compress_gap_duration(
                    gap.duration_ms,
                    keep_silence_ms=keep_silence_ms,
                    compress_silence_to_ms=compress_silence_to_ms,
                ),
            )

        append_segment("audio", activity, activity.duration_ms)
        source_cursor_ms = activity.end_ms

    if source_cursor_ms < source_duration_ms:
        gap = TimeSpan(start_ms=source_cursor_ms, end_ms=source_duration_ms)
        append_segment(
            "silence",
            gap,
            _compress_gap_duration(
                gap.duration_ms,
                keep_silence_ms=keep_silence_ms,
                compress_silence_to_ms=compress_silence_to_ms,
            ),
        )

    return TimelineMap(
        source_duration_ms=source_duration_ms,
        digest_duration_ms=digest_cursor_ms,
        segments=tuple(segments),
    )


def _compress_gap_duration(
    gap_duration_ms: int,
    *,
    keep_silence_ms: int,
    compress_silence_to_ms: int,
) -> int:
    if gap_duration_ms <= keep_silence_ms:
        return gap_duration_ms
    return min(gap_duration_ms, compress_silence_to_ms)
