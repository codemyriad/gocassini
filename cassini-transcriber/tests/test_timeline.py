from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.timeline import (  # noqa: E402
    TimeSpan,
    build_digest_timeline_map,
    merge_spans,
)


class MergeSpansTests(unittest.TestCase):
    def test_merge_spans_merges_overlaps_and_touching_ranges(self) -> None:
        spans = [
            TimeSpan(100, 300),
            TimeSpan(250, 400),
            TimeSpan(400, 450),
            TimeSpan(800, 900),
        ]
        self.assertEqual(
            merge_spans(spans),
            [TimeSpan(100, 450), TimeSpan(800, 900)],
        )


class DigestTimelineTests(unittest.TestCase):
    def test_build_digest_timeline_compresses_long_silence(self) -> None:
        timeline = build_digest_timeline_map(
            activity_spans=[TimeSpan(1000, 2000), TimeSpan(7000, 8000)],
            source_duration_ms=10_000,
            activity_padding_ms=0,
            keep_silence_ms=900,
            compress_silence_to_ms=800,
        )
        self.assertEqual(timeline.digest_duration_ms, 4400)
        self.assertEqual(
            [(segment.kind, segment.source, segment.digest) for segment in timeline.segments],
            [
                ("silence", TimeSpan(0, 1000), TimeSpan(0, 800)),
                ("audio", TimeSpan(1000, 2000), TimeSpan(800, 1800)),
                ("silence", TimeSpan(2000, 7000), TimeSpan(1800, 2600)),
                ("audio", TimeSpan(7000, 8000), TimeSpan(2600, 3600)),
                ("silence", TimeSpan(8000, 10_000), TimeSpan(3600, 4400)),
            ],
        )
        self.assertEqual(timeline.map_source_to_digest(1500), 1300)
        self.assertEqual(timeline.map_source_to_digest(4500), 2200)
        self.assertEqual(timeline.map_source_to_digest(9999), 4400)

    def test_build_digest_timeline_preserves_short_silence(self) -> None:
        timeline = build_digest_timeline_map(
            activity_spans=[TimeSpan(1000, 2000), TimeSpan(2200, 3000)],
            source_duration_ms=4000,
            activity_padding_ms=0,
            keep_silence_ms=900,
            compress_silence_to_ms=800,
        )
        self.assertEqual(timeline.digest_duration_ms, 3600)
        self.assertEqual(timeline.map_source_to_digest(2100), 1900)

    def test_build_digest_timeline_handles_empty_activity(self) -> None:
        timeline = build_digest_timeline_map(
            activity_spans=[],
            source_duration_ms=5000,
            activity_padding_ms=0,
            keep_silence_ms=900,
            compress_silence_to_ms=800,
        )
        self.assertEqual(timeline.digest_duration_ms, 800)
        self.assertEqual(len(timeline.segments), 1)
        self.assertEqual(timeline.segments[0].kind, "silence")

    def test_build_digest_timeline_applies_activity_padding(self) -> None:
        timeline = build_digest_timeline_map(
            activity_spans=[TimeSpan(1000, 2000)],
            source_duration_ms=3000,
            activity_padding_ms=200,
            keep_silence_ms=900,
            compress_silence_to_ms=800,
        )
        self.assertEqual(
            [(segment.kind, segment.source) for segment in timeline.segments],
            [
                ("silence", TimeSpan(0, 800)),
                ("audio", TimeSpan(800, 2200)),
                ("silence", TimeSpan(2200, 3000)),
            ],
        )


if __name__ == "__main__":
    unittest.main()
