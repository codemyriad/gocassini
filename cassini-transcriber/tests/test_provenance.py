from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from cassini_transcriber.provenance import (  # noqa: E402
    build_readable_cleanup_provenance,
    build_speech_to_text_provenance,
)


class SpeechToTextProvenanceTests(unittest.TestCase):
    def test_local_whisper_infers_engine_and_model(self) -> None:
        provenance = build_speech_to_text_provenance(
            backend="local-whisper",
            transcriber_url=None,
            whisper_model="large-v3",
            whisper_device="cuda",
            whisper_language="en",
        )
        self.assertEqual(provenance["backend"], "local-whisper")
        self.assertEqual(provenance["engine"], "faster-whisper")
        self.assertEqual(provenance["model"], "large-v3")
        self.assertEqual(provenance["device"], "cuda")
        self.assertEqual(provenance["language"], "en")

    def test_http_backend_accepts_explicit_engine_and_model(self) -> None:
        provenance = build_speech_to_text_provenance(
            backend="http",
            transcriber_url="http://parakeet.internal:9000/v1/transcribe",
            whisper_model=None,
            whisper_device=None,
            whisper_language=None,
            explicit_engine="parakeet",
            explicit_model="nvidia/parakeet-tdt-0.6b-v2",
        )
        self.assertEqual(provenance["backend"], "http")
        self.assertEqual(provenance["engine"], "parakeet")
        self.assertEqual(provenance["model"], "nvidia/parakeet-tdt-0.6b-v2")
        self.assertEqual(provenance["baseUrl"], "http://parakeet.internal:9000/v1/transcribe")
        self.assertEqual(provenance["host"], "parakeet.internal:9000")


class ReadableCleanupProvenanceTests(unittest.TestCase):
    def test_disabled_cleanup_is_explicit(self) -> None:
        provenance = build_readable_cleanup_provenance(
            backend="none",
            readable_source="disabled",
            openwebui_base_url=None,
            openwebui_model=None,
            api_base_url=None,
            api_model=None,
            local_llm_model=None,
            local_llm_device=None,
            ollama_model=None,
        )
        self.assertEqual(
            provenance,
            {
                "backend": "none",
                "engine": "none",
                "source": "disabled",
            },
        )

    def test_openai_cleanup_includes_model_and_host(self) -> None:
        provenance = build_readable_cleanup_provenance(
            backend="openai-compatible",
            readable_source="generated",
            openwebui_base_url=None,
            openwebui_model=None,
            api_base_url="https://openrouter.ai/api/v1",
            api_model="openai/gpt-4o-mini",
            local_llm_model=None,
            local_llm_device=None,
            ollama_model=None,
        )
        self.assertEqual(provenance["engine"], "openai-compatible")
        self.assertEqual(provenance["model"], "openai/gpt-4o-mini")
        self.assertEqual(provenance["host"], "openrouter.ai")


if __name__ == "__main__":
    unittest.main()
