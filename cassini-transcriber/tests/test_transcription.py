from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.transcription import (  # noqa: E402
    HTTPTranscriptionClient,
    HTTPTranscriptionConfig,
    LocalWhisperConfig,
    LocalWhisperTranscriptionClient,
)


class TranscriptionCacheKeyTests(unittest.TestCase):
    def test_http_client_uses_stable_cache_key(self) -> None:
        client = HTTPTranscriptionClient(HTTPTranscriptionConfig(url="http://127.0.0.1:8000/v1/transcribe"))
        self.assertEqual(client.cache_key(), "http")

    def test_local_whisper_cache_key_includes_model_device_and_language(self) -> None:
        client = LocalWhisperTranscriptionClient(
            LocalWhisperConfig(
                model="large-v3",
                device="cuda",
                language="en",
            )
        )
        self.assertEqual(client.cache_key(), "local-whisper--large-v3--cuda--en")


if __name__ == "__main__":
    unittest.main()
