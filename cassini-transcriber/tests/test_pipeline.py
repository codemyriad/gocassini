from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.pipeline import (  # noqa: E402
    AudioStream,
    TrackWorkspace,
    build_meeting_activity_spans,
    filter_words_to_time_window,
    make_speaker_ids,
    remap_words_to_digest_timeline,
    render_captions_vtt,
    serialize_timeline_map,
    segment_word_items,
    validate_transcript_payload,
)
from cassini_transcriber.timeline import TimeSpan, build_digest_timeline_map  # noqa: E402


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


class TimelineIntegrationTests(unittest.TestCase):
    def test_build_meeting_activity_spans_offsets_track_ranges(self) -> None:
        workspace = TrackWorkspace(
            stream=AudioStream(
                index=1,
                order=1,
                codec_name="opus",
                channels=1,
                speaker_id="spk_alex",
                speaker_label="Alex",
                start_ms=5000,
                duration_ms=4000,
            ),
            audio_path=Path("/tmp/alex.wav"),
            duration_ms=4000,
            activity_spans=(TimeSpan(100, 900), TimeSpan(1500, 2000)),
        )
        self.assertEqual(
            build_meeting_activity_spans([workspace], source_duration_ms=10_000),
            [TimeSpan(5100, 5900), TimeSpan(6500, 7000)],
        )

    def test_filter_and_remap_words_to_digest_timeline(self) -> None:
        timeline = build_digest_timeline_map(
            activity_spans=[TimeSpan(1000, 2000), TimeSpan(7000, 8000)],
            source_duration_ms=10_000,
            activity_padding_ms=0,
            keep_silence_ms=900,
            compress_silence_to_ms=800,
        )
        words = [
            {"text": "alpha", "startMs": 1500, "endMs": 1700},
            {"text": "beta", "startMs": 2450, "endMs": 2600},
            {"text": "gamma", "startMs": 7200, "endMs": 7400},
        ]
        filtered = filter_words_to_time_window(
            words,
            window_start_ms=1000,
            window_end_ms=7000,
        )
        remapped = remap_words_to_digest_timeline(filtered, timeline)
        self.assertEqual(
            remapped,
            [
                {"text": "alpha", "startMs": 1300, "endMs": 1500},
                {"text": "beta", "startMs": 1872, "endMs": 1896},
            ],
        )

    def test_serialize_timeline_map(self) -> None:
        timeline = build_digest_timeline_map(
            activity_spans=[TimeSpan(1000, 2000)],
            source_duration_ms=3000,
            activity_padding_ms=0,
            keep_silence_ms=900,
            compress_silence_to_ms=800,
        )
        payload = serialize_timeline_map(timeline)
        self.assertEqual(payload["version"], "timeline.map.v1")
        self.assertEqual(payload["sourceDurationMs"], 3000)
        self.assertEqual(payload["digestDurationMs"], timeline.digest_duration_ms)
        self.assertEqual(payload["segments"][1]["kind"], "audio")


if __name__ == "__main__":
    unittest.main()
