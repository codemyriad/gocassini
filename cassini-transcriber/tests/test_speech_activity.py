from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.speech_activity import (  # noqa: E402
    TranscriptionChunk,
    parse_silencedetect_output,
    plan_transcription_chunks,
)
from cassini_transcriber.timeline import TimeSpan  # noqa: E402


class SilenceDetectParserTests(unittest.TestCase):
    def test_parse_silencedetect_output_builds_active_spans(self) -> None:
        output_text = """
[silencedetect @ 0x1] silence_start: 0
[silencedetect @ 0x1] silence_end: 1.500 | silence_duration: 1.500
[silencedetect @ 0x1] silence_start: 3.000
[silencedetect @ 0x1] silence_end: 4.000 | silence_duration: 1.000
        """.strip()
        self.assertEqual(
            parse_silencedetect_output(output_text, total_duration_ms=5000, minimum_activity_ms=100),
            [TimeSpan(1500, 3000), TimeSpan(4000, 5000)],
        )

    def test_parse_silencedetect_output_handles_trailing_silence(self) -> None:
        output_text = "[silencedetect @ 0x1] silence_start: 2.500"
        self.assertEqual(
            parse_silencedetect_output(output_text, total_duration_ms=5000, minimum_activity_ms=100),
            [TimeSpan(0, 2500)],
        )


class ChunkPlanningTests(unittest.TestCase):
    def test_plan_transcription_chunks_pads_and_merges_activity(self) -> None:
        chunks = plan_transcription_chunks(
            activity_spans=[TimeSpan(1000, 1200), TimeSpan(1300, 1500)],
            source_duration_ms=5000,
            chunk_padding_ms=200,
            max_chunk_ms=25_000,
            chunk_overlap_ms=500,
        )
        self.assertEqual(
            chunks,
            [TranscriptionChunk(source=TimeSpan(800, 1700), emit=TimeSpan(800, 1700))],
        )

    def test_plan_transcription_chunks_splits_long_span_with_overlap(self) -> None:
        chunks = plan_transcription_chunks(
            activity_spans=[TimeSpan(0, 70_000)],
            source_duration_ms=70_000,
            chunk_padding_ms=0,
            max_chunk_ms=25_000,
            chunk_overlap_ms=500,
        )
        self.assertEqual(
            chunks[:3],
            [
                TranscriptionChunk(source=TimeSpan(0, 25_000), emit=TimeSpan(0, 25_000)),
                TranscriptionChunk(
                    source=TimeSpan(24_500, 49_500),
                    emit=TimeSpan(25_000, 49_500),
                ),
                TranscriptionChunk(
                    source=TimeSpan(49_000, 70_000),
                    emit=TimeSpan(49_500, 70_000),
                ),
            ],
        )

    def test_plan_transcription_chunks_bridges_nearby_activity(self) -> None:
        chunks = plan_transcription_chunks(
            activity_spans=[TimeSpan(1000, 1600), TimeSpan(2300, 2900), TimeSpan(10_000, 10_600)],
            source_duration_ms=20_000,
            chunk_padding_ms=100,
            max_chunk_ms=25_000,
            chunk_overlap_ms=500,
            max_bridge_gap_ms=1_500,
        )
        self.assertEqual(
            chunks,
            [
                TranscriptionChunk(source=TimeSpan(900, 3000), emit=TimeSpan(900, 3000)),
                TranscriptionChunk(source=TimeSpan(9900, 10_700), emit=TimeSpan(9900, 10_700)),
            ],
        )


if __name__ == "__main__":
    unittest.main()
