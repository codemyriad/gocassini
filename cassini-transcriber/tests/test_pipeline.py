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
    build_manifest,
    filter_words_to_time_window,
    make_track_ids,
    make_speaker_ids,
    normalize_speaker_label,
    resolve_readable_backend,
    resolve_transcriber_backend,
    remap_words_to_digest_timeline,
    render_captions_vtt,
    serialize_timeline_map,
    segment_word_items,
    validate_transcript_payload,
)
from cassini_transcriber.timeline import TimeSpan, build_digest_timeline_map  # noqa: E402


class SpeakerIdTests(unittest.TestCase):
    def test_make_speaker_ids_reuses_ids_for_rejoined_labels(self) -> None:
        self.assertEqual(
            make_speaker_ids(["Alex", "Alex", "Chris Jones"]),
            ["spk_alex", "spk_alex", "spk_chris_jones"],
        )

    def test_normalize_speaker_label_strips_common_stream_suffixes(self) -> None:
        self.assertEqual(normalize_speaker_label("Chris audio"), "Chris")
        self.assertEqual(normalize_speaker_label("Silvio video"), "Silvio")
        self.assertEqual(normalize_speaker_label("chima"), "chima")

    def test_make_track_ids_stays_unique_for_duplicate_labels(self) -> None:
        self.assertEqual(
            make_track_ids(["Chris", "Chris", "Alex"]),
            ["track_01_chris", "track_02_chris", "track_03_alex"],
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


class ManifestTests(unittest.TestCase):
    def test_build_manifest_includes_digest_summary(self) -> None:
        manifest = build_manifest(
            source_path=Path("/tmp/daily-meeting.mkv"),
            audio_name="meeting.webm",
            transcript_name="transcript.words.v1.json",
            readable_name="transcript.readable.v1.json",
            captions_name="captions.vtt",
            timeline_name="timeline.map.v1.json",
            speaker_count=4,
            chunk_count=12,
            segment_count=34,
            word_count=567,
            timeline_segment_count=89,
            source_duration_ms=120_000,
            digest_duration_ms=95_000,
        )
        self.assertEqual(manifest["files"]["timeline"], "timeline.map.v1.json")
        self.assertEqual(
            manifest["files"]["readableTranscript"],
            "transcript.readable.v1.json",
        )
        self.assertEqual(manifest["speakerCount"], 4)
        self.assertEqual(manifest["chunkCount"], 12)
        self.assertEqual(manifest["segmentCount"], 34)
        self.assertEqual(manifest["wordCount"], 567)
        self.assertEqual(manifest["timelineSegmentCount"], 89)
        self.assertEqual(manifest["silenceReductionMs"], 25_000)


class TimelineIntegrationTests(unittest.TestCase):
    def test_build_meeting_activity_spans_uses_absolute_track_timeline(self) -> None:
        workspace = TrackWorkspace(
            stream=AudioStream(
                index=1,
                order=1,
                track_id="track_01_alex",
                codec_name="opus",
                channels=1,
                speaker_id="spk_alex",
                speaker_label="Alex",
            ),
            audio_path=Path("/tmp/alex.wav"),
            duration_ms=4000,
            activity_spans=(TimeSpan(100, 900), TimeSpan(1500, 12_000)),
        )
        self.assertEqual(
            build_meeting_activity_spans([workspace], source_duration_ms=10_000),
            [TimeSpan(100, 900), TimeSpan(1500, 10_000)],
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


class BackendResolutionTests(unittest.TestCase):
    def test_resolve_transcriber_backend_prefers_http_when_url_is_present(self) -> None:
        backend, device, model = resolve_transcriber_backend(
            transcriber_backend="auto",
            transcriber_url="http://127.0.0.1:8000/v1/transcribe",
            whisper_device="auto",
            whisper_model="auto",
        )
        self.assertEqual(backend, "http")
        self.assertEqual(model, "large-v3")
        self.assertIn(device, {"cpu", "cuda"})

    def test_resolve_readable_backend_turns_off_when_no_output_is_requested(self) -> None:
        backend = resolve_readable_backend(
            readable_backend="auto",
            readable_transcript_name=None,
            api_base_url="https://openrouter.ai/api/v1",
            api_key="test-key",
            api_model="openai/gpt-4o-mini",
            openwebui_base_url=None,
            openwebui_email=None,
            openwebui_password=None,
            openwebui_model=None,
            ollama_binary="ollama",
        )
        self.assertEqual(backend, "none")

    def test_resolve_readable_backend_prefers_openai_compatible_when_api_key_is_present(self) -> None:
        backend = resolve_readable_backend(
            readable_backend="auto",
            readable_transcript_name="transcript.readable.v1.json",
            api_base_url="https://openrouter.ai/api/v1",
            api_key="test-key",
            api_model="openai/gpt-4o-mini",
            openwebui_base_url=None,
            openwebui_email=None,
            openwebui_password=None,
            openwebui_model=None,
            ollama_binary="ollama",
        )
        self.assertEqual(backend, "openai-compatible")


if __name__ == "__main__":
    unittest.main()
