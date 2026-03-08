from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.pipeline import (  # noqa: E402
    make_speaker_ids,
    render_captions_vtt,
    segment_word_items,
    validate_transcript_payload,
)


class SpeakerIdTests(unittest.TestCase):
    def test_make_speaker_ids_deduplicates_labels(self) -> None:
        self.assertEqual(
            make_speaker_ids(["Alex", "Alex", "Chris Jones"]),
            ["spk_alex", "spk_alex_2", "spk_chris_jones"],
        )


class SegmentationTests(unittest.TestCase):
    def test_segment_word_items_splits_on_gap(self) -> None:
        words = [
            {"text": "hello", "startMs": 0, "endMs": 200},
            {"text": "there", "startMs": 220, "endMs": 350},
            {"text": "again", "startMs": 1800, "endMs": 2100},
        ]
        segments = segment_word_items(
            words,
            speaker_id="spk_alex",
            gap_ms=600,
            max_segment_ms=10_000,
            max_segment_words=16,
        )
        self.assertEqual(len(segments), 2)
        self.assertEqual(segments[0]["text"], "hello there")
        self.assertEqual(segments[1]["text"], "again")


class CaptionsTests(unittest.TestCase):
    def test_render_captions_vtt_clamps_overlaps(self) -> None:
        transcript = {
            "version": "transcript.words.v1",
            "media": {"src": "meeting.webm", "durationMs": 5000},
            "speakers": [{"id": "spk_alex", "label": "Alex"}],
            "segments": [
                {
                    "id": "seg_000001",
                    "speaker": "spk_alex",
                    "startMs": 0,
                    "endMs": 4000,
                    "text": "First cue",
                    "words": [
                        {"id": "w_0000001", "text": "First", "startMs": 0, "endMs": 500},
                    ],
                },
                {
                    "id": "seg_000002",
                    "speaker": "spk_alex",
                    "startMs": 1500,
                    "endMs": 4500,
                    "text": "Second cue",
                    "words": [
                        {"id": "w_0000002", "text": "Second", "startMs": 1500, "endMs": 2000},
                    ],
                },
            ],
        }
        vtt = render_captions_vtt(transcript)
        self.assertIn("00:00:00.000 --> 00:00:01.500", vtt)
        self.assertIn("Alex: First cue", vtt)


class ValidationTests(unittest.TestCase):
    def test_validate_transcript_payload_accepts_expected_shape(self) -> None:
        transcript = {
            "version": "transcript.words.v1",
            "media": {
                "src": "meeting.webm",
                "durationMs": 5000,
                "sha256": "a" * 64,
            },
            "speakers": [{"id": "spk_alex", "label": "Alex"}],
            "segments": [
                {
                    "id": "seg_000001",
                    "speaker": "spk_alex",
                    "startMs": 1000,
                    "endMs": 2000,
                    "text": "hello",
                    "words": [
                        {
                            "id": "w_0000001",
                            "text": "hello",
                            "startMs": 1000,
                            "endMs": 1400,
                        }
                    ],
                }
            ],
        }
        validate_transcript_payload(transcript, actual_audio_duration_ms=5000)


if __name__ == "__main__":
    unittest.main()
