from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.llm import (  # noqa: E402
    ReadableTranscriptRecord,
    build_readable_records,
    build_readable_transcript_payload,
    estimate_completion_tokens,
    normalize_readable_text,
    parse_readable_records_response,
    plan_readable_record_batches,
    plan_readable_windows,
)


class ReadableResponseTests(unittest.TestCase):
    def test_parse_readable_records_response_reads_marker_format(self) -> None:
        records = [
            ReadableTranscriptRecord(
                index=1,
                speaker_id="spk_alex",
                speaker_label="Alex",
                start_ms=0,
                end_ms=1000,
                text="uh hello there",
                source_segment_ids=("seg_1",),
            ),
            ReadableTranscriptRecord(
                index=2,
                speaker_id="spk_alex",
                speaker_label="Alex",
                start_ms=1100,
                end_ms=2000,
                text="um tomorrow",
                source_segment_ids=("seg_2",),
            ),
        ]
        cleaned = parse_readable_records_response(
            "@@1@@ Alex | Hello there.\n@@2@@ Tomorrow.",
            records,
        )
        self.assertEqual(cleaned, ["Hello there.", "Tomorrow."])

    def test_normalize_readable_text_strips_outer_whitespace(self) -> None:
        self.assertEqual(normalize_readable_text("  hello   there  "), "hello there")


class ReadableWindowPlanningTests(unittest.TestCase):
    def test_plan_readable_windows_keeps_same_speaker_runs_together(self) -> None:
        segments = [
            {
                "id": "seg_1",
                "speaker": "spk_alex",
                "startMs": 0,
                "endMs": 1000,
                "text": "one",
                "words": [{"id": "w_1", "text": "one", "startMs": 0, "endMs": 1000}],
            },
            {
                "id": "seg_2",
                "speaker": "spk_alex",
                "startMs": 1100,
                "endMs": 2200,
                "text": "two three",
                "words": [
                    {"id": "w_2", "text": "two", "startMs": 1100, "endMs": 1500},
                    {"id": "w_3", "text": "three", "startMs": 1600, "endMs": 2200},
                ],
            },
            {
                "id": "seg_3",
                "speaker": "spk_chris",
                "startMs": 2300,
                "endMs": 3200,
                "text": "other speaker",
                "words": [
                    {"id": "w_4", "text": "other", "startMs": 2300, "endMs": 2600},
                    {"id": "w_5", "text": "speaker", "startMs": 2700, "endMs": 3200},
                ],
            },
        ]
        windows = plan_readable_windows(
            segments,
            max_gap_ms=1800,
            max_window_ms=45_000,
            max_window_words=120,
        )
        self.assertEqual(
            [[segment["id"] for segment in window] for window in windows],
            [["seg_1", "seg_2"], ["seg_3"]],
        )

    def test_plan_readable_record_batches_splits_by_count(self) -> None:
        records = [
            ReadableTranscriptRecord(
                index=index,
                speaker_id="spk_alex",
                speaker_label="Alex",
                start_ms=index * 1000,
                end_ms=index * 1000 + 500,
                text="hello",
                source_segment_ids=(f"seg_{index}",),
            )
            for index in range(1, 6)
        ]
        batches = plan_readable_record_batches(records, max_batch_records=2, max_batch_chars=1000)
        self.assertEqual([[record.index for record in batch] for batch in batches], [[1, 2], [3, 4], [5]])


class ReadablePayloadTests(unittest.TestCase):
    def test_build_readable_records_uses_source_windows(self) -> None:
        transcript = {
            "version": "transcript.words.v1",
            "media": {"src": "meeting.webm", "durationMs": 6000},
            "speakers": [{"id": "spk_alex", "label": "Alex"}],
            "segments": [
                {
                    "id": "seg_000001",
                    "speaker": "spk_alex",
                    "startMs": 0,
                    "endMs": 2000,
                    "text": "uh I think we should ship it",
                    "words": [
                        {"id": "w_1", "text": "uh", "startMs": 0, "endMs": 200},
                        {"id": "w_2", "text": "I", "startMs": 250, "endMs": 300},
                        {"id": "w_3", "text": "think", "startMs": 310, "endMs": 500},
                    ],
                },
                {
                    "id": "seg_000002",
                    "speaker": "spk_alex",
                    "startMs": 2200,
                    "endMs": 4000,
                    "text": "um tomorrow",
                    "words": [
                        {"id": "w_4", "text": "um", "startMs": 2200, "endMs": 2400},
                        {"id": "w_5", "text": "tomorrow", "startMs": 2450, "endMs": 2800},
                    ],
                },
            ],
        }
        records = build_readable_records(
            transcript_payload=transcript,
            max_gap_ms=1800,
            max_window_ms=45_000,
            max_window_words=120,
        )
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0].source_segment_ids, ("seg_000001", "seg_000002"))
        self.assertEqual(records[0].speaker_label, "Alex")

    def test_build_readable_transcript_payload_uses_llm_text(self) -> None:
        transcript = {
            "version": "transcript.words.v1",
            "media": {"src": "meeting.webm", "durationMs": 6000},
            "speakers": [{"id": "spk_alex", "label": "Alex"}],
            "segments": [
                {
                    "id": "seg_000001",
                    "speaker": "spk_alex",
                    "startMs": 0,
                    "endMs": 2000,
                    "text": "uh I think we should ship it",
                    "words": [
                        {"id": "w_1", "text": "uh", "startMs": 0, "endMs": 200},
                        {"id": "w_2", "text": "I", "startMs": 250, "endMs": 300},
                        {"id": "w_3", "text": "think", "startMs": 310, "endMs": 500},
                    ],
                },
                {
                    "id": "seg_000002",
                    "speaker": "spk_alex",
                    "startMs": 2200,
                    "endMs": 4000,
                    "text": "um tomorrow",
                    "words": [
                        {"id": "w_4", "text": "um", "startMs": 2200, "endMs": 2400},
                        {"id": "w_5", "text": "tomorrow", "startMs": 2450, "endMs": 2800},
                    ],
                },
            ],
        }

        class FakeClient:
            def rewrite_readable_records(self, records: list[ReadableTranscriptRecord]) -> list[str]:
                self.record_indexes = [record.index for record in records]
                return ["I think we should ship it tomorrow."]

        client = FakeClient()
        payload = build_readable_transcript_payload(
            transcript_payload=transcript,
            client=client,  # type: ignore[arg-type]
            max_gap_ms=1800,
            max_window_ms=45_000,
            max_window_words=120,
        )
        self.assertEqual(payload["version"], "transcript.readable.v1")
        self.assertEqual(payload["segments"][0]["text"], "I think we should ship it tomorrow.")
        self.assertEqual(payload["segments"][0]["sourceSegmentIds"], ["seg_000001", "seg_000002"])
        self.assertEqual(client.record_indexes, [1])

    def test_build_readable_transcript_payload_falls_back_to_source_text(self) -> None:
        transcript = {
            "version": "transcript.words.v1",
            "media": {"src": "meeting.webm", "durationMs": 2000},
            "speakers": [{"id": "spk_alex", "label": "Alex"}],
            "segments": [
                {
                    "id": "seg_000001",
                    "speaker": "spk_alex",
                    "startMs": 0,
                    "endMs": 2000,
                    "text": "uh hello there",
                    "words": [
                        {"id": "w_1", "text": "uh", "startMs": 0, "endMs": 100},
                        {"id": "w_2", "text": "hello", "startMs": 120, "endMs": 500},
                        {"id": "w_3", "text": "there", "startMs": 520, "endMs": 900},
                    ],
                }
            ],
        }

        class FailingClient:
            def rewrite_readable_records(self, records: list[ReadableTranscriptRecord]) -> list[str]:
                raise RuntimeError("boom")

        payload = build_readable_transcript_payload(
            transcript_payload=transcript,
            client=FailingClient(),  # type: ignore[arg-type]
            max_gap_ms=1800,
            max_window_ms=45_000,
            max_window_words=120,
        )
        self.assertEqual(payload["segments"][0]["text"], "uh hello there")

    def test_estimate_completion_tokens_has_bounds(self) -> None:
        tokens = estimate_completion_tokens(
            [
                ReadableTranscriptRecord(
                    index=1,
                    speaker_id="spk_alex",
                    speaker_label="Alex",
                    start_ms=0,
                    end_ms=1000,
                    text="hello",
                    source_segment_ids=("seg_1",),
                )
            ]
        )
        self.assertGreaterEqual(tokens, 512)
        self.assertLessEqual(tokens, 3000)


if __name__ == "__main__":
    unittest.main()
