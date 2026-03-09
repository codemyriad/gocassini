from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber import cli  # noqa: E402


class CLITests(unittest.TestCase):
    def test_http_backend_requires_url(self) -> None:
        argv = [
            "cassini-transcriber",
            "--input",
            "/tmp/in.mkv",
            "--output-dir",
            "/tmp/out",
            "--transcriber-backend",
            "http",
        ]
        with patch.object(sys, "argv", argv):
            with self.assertRaises(SystemExit) as exc:
                cli.main()
        self.assertEqual(
            str(exc.exception),
            "--transcriber-url is required when --transcriber-backend=http",
        )

    def test_local_backend_passes_whisper_options(self) -> None:
        argv = [
            "cassini-transcriber",
            "--input",
            "/tmp/in.mkv",
            "--output-dir",
            "/tmp/out",
            "--transcriber-backend",
            "local-whisper",
            "--whisper-model",
            "small",
            "--whisper-device",
            "cuda",
        ]
        with (
            patch.object(sys, "argv", argv),
            patch("cassini_transcriber.cli.build_meeting_artifact") as build_meeting_artifact,
        ):
            build_meeting_artifact.return_value = {
                "audio_path": "/tmp/out/meeting.webm",
                "transcript_path": "/tmp/out/transcript.words.v1.json",
                "readable_transcript_path": None,
                "captions_path": "/tmp/out/captions.vtt",
                "timeline_path": None,
                "manifest_path": None,
                "speaker_count": 1,
                "segment_count": 1,
                "word_count": 1,
                "chunk_count": 1,
                "timeline_segment_count": 1,
                "source_duration_ms": 1000,
                "duration_ms": 1000,
                "reduction_ms": 0,
            }
            exit_code = cli.main()

        self.assertEqual(exit_code, 0)
        kwargs = build_meeting_artifact.call_args.kwargs
        self.assertEqual(kwargs["transcriber_backend"], "local-whisper")
        self.assertEqual(kwargs["whisper_model"], "small")
        self.assertEqual(kwargs["whisper_device"], "cuda")

    def test_readable_backend_none_disables_readable_output(self) -> None:
        argv = [
            "cassini-transcriber",
            "--input",
            "/tmp/in.mkv",
            "--output-dir",
            "/tmp/out",
            "--transcriber-backend",
            "local-whisper",
            "--readable-backend",
            "none",
            "--readable-transcript-name",
            "transcript.readable.v1.json",
        ]
        with (
            patch.object(sys, "argv", argv),
            patch("cassini_transcriber.cli.build_meeting_artifact") as build_meeting_artifact,
        ):
            build_meeting_artifact.return_value = {
                "audio_path": "/tmp/out/meeting.webm",
                "transcript_path": "/tmp/out/transcript.words.v1.json",
                "readable_transcript_path": None,
                "captions_path": "/tmp/out/captions.vtt",
                "timeline_path": None,
                "manifest_path": None,
                "speaker_count": 1,
                "segment_count": 1,
                "word_count": 1,
                "chunk_count": 1,
                "timeline_segment_count": 1,
                "source_duration_ms": 1000,
                "duration_ms": 1000,
                "reduction_ms": 0,
            }
            cli.main()

        kwargs = build_meeting_artifact.call_args.kwargs
        self.assertEqual(kwargs["readable_backend"], "none")
        self.assertIsNone(kwargs["readable_transcript_name"])

    def test_openai_compatible_backend_passes_api_options(self) -> None:
        argv = [
            "cassini-transcriber",
            "--input",
            "/tmp/in.mkv",
            "--output-dir",
            "/tmp/out",
            "--transcriber-backend",
            "local-whisper",
            "--readable-backend",
            "openai-compatible",
            "--readable-transcript-name",
            "transcript.readable.v1.json",
            "--api-base-url",
            "https://openrouter.ai/api/v1",
            "--api-key",
            "test-key",
            "--api-model",
            "openai/gpt-4o-mini",
        ]
        with (
            patch.object(sys, "argv", argv),
            patch("cassini_transcriber.cli.build_meeting_artifact") as build_meeting_artifact,
        ):
            build_meeting_artifact.return_value = {
                "audio_path": "/tmp/out/meeting.webm",
                "transcript_path": "/tmp/out/transcript.words.v1.json",
                "readable_transcript_path": "/tmp/out/transcript.readable.v1.json",
                "captions_path": "/tmp/out/captions.vtt",
                "timeline_path": None,
                "manifest_path": None,
                "speaker_count": 1,
                "segment_count": 1,
                "word_count": 1,
                "chunk_count": 1,
                "timeline_segment_count": 1,
                "source_duration_ms": 1000,
                "duration_ms": 1000,
                "reduction_ms": 0,
            }
            cli.main()

        kwargs = build_meeting_artifact.call_args.kwargs
        self.assertEqual(kwargs["readable_backend"], "openai-compatible")
        self.assertEqual(kwargs["api_base_url"], "https://openrouter.ai/api/v1")
        self.assertEqual(kwargs["api_key"], "test-key")
        self.assertEqual(kwargs["api_model"], "openai/gpt-4o-mini")


if __name__ == "__main__":
    unittest.main()
