from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber import readable_cli  # noqa: E402
from cassini_transcriber.llm import ReadableTranscriptRecord  # noqa: E402
from cassini_transcriber.readable import create_readable_client, load_transcript_payload  # noqa: E402


class ReadableCLITests(unittest.TestCase):
    def test_none_backend_builds_readable_transcript_from_canonical_input(self) -> None:
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

        with tempfile.TemporaryDirectory() as tmp:
            input_path = Path(tmp) / "transcript.words.v1.json"
            input_path.write_text(json.dumps(transcript) + "\n", encoding="utf-8")

            exit_code = readable_cli.main(
                [
                    "--input-transcript",
                    str(input_path),
                    "--readable-backend",
                    "none",
                ]
            )

            self.assertEqual(exit_code, 0)
            output_path = Path(tmp) / "transcript.readable.v1.json"
            self.assertTrue(output_path.exists())
            payload = json.loads(output_path.read_text(encoding="utf-8"))
            self.assertEqual(payload["version"], "transcript.readable.v1")
            self.assertEqual(payload["segments"][0]["text"], "uh hello there")
            self.assertEqual(payload["sourceTranscriptVersion"], "transcript.words.v1")

    def test_load_transcript_payload_rejects_wrong_version(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            input_path = Path(tmp) / "transcript.words.v1.json"
            input_path.write_text(
                json.dumps({"version": "transcript.readable.v1", "segments": []}) + "\n",
                encoding="utf-8",
            )
            with self.assertRaises(ValueError):
                load_transcript_payload(input_path)

    def test_passthrough_readable_client_returns_source_text(self) -> None:
        client = create_readable_client(readable_backend="none", allow_none_passthrough=True)
        records = [
            ReadableTranscriptRecord(
                index=1,
                speaker_id="spk_alex",
                speaker_label="Alex",
                start_ms=0,
                end_ms=1000,
                text="hello there",
                source_segment_ids=("seg_1",),
            )
        ]
        self.assertEqual(client.rewrite_readable_records(records), ["hello there"])


if __name__ == "__main__":
    unittest.main()
