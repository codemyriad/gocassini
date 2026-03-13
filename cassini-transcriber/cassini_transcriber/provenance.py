from __future__ import annotations

from typing import Any
from urllib.parse import urlparse


def build_speech_to_text_provenance(
    *,
    backend: str,
    transcriber_url: str | None,
    whisper_model: str | None,
    whisper_device: str | None,
    whisper_language: str | None,
    explicit_engine: str | None = None,
    explicit_model: str | None = None,
) -> dict[str, Any]:
    engine = _clean(explicit_engine)
    model = _clean(explicit_model)
    device = _clean(whisper_device)
    language = _clean(whisper_language)
    base_url = _clean(transcriber_url)

    if backend == "local-whisper":
        if not engine:
            engine = "faster-whisper"
        if not model:
            model = _clean(whisper_model)
    elif backend == "http":
        if not engine:
            engine = "http"
        device = ""
        language = ""
    elif not engine:
        engine = backend

    provenance: dict[str, Any] = {
        "backend": backend,
        "engine": engine,
    }
    if model:
        provenance["model"] = model
    if device:
        provenance["device"] = device
    if language:
        provenance["language"] = language
    if base_url:
        provenance["baseUrl"] = base_url
        host = _clean(urlparse(base_url).netloc)
        if host:
            provenance["host"] = host
    return provenance


def build_readable_cleanup_provenance(
    *,
    backend: str,
    readable_source: str,
    openwebui_base_url: str | None,
    openwebui_model: str | None,
    api_base_url: str | None,
    api_model: str | None,
    local_llm_model: str | None,
    local_llm_device: str | None,
    ollama_model: str | None,
) -> dict[str, Any]:
    if readable_source == "disabled" or backend == "none":
        return {
            "backend": "none",
            "engine": "none",
            "source": "disabled",
        }

    provenance: dict[str, Any] = {
        "backend": backend,
        "source": "generated",
    }
    if backend == "openwebui":
        provenance["engine"] = "openwebui"
        if _clean(openwebui_model):
            provenance["model"] = _clean(openwebui_model)
        if _clean(openwebui_base_url):
            provenance["baseUrl"] = _clean(openwebui_base_url)
            host = _clean(urlparse(str(openwebui_base_url)).netloc)
            if host:
                provenance["host"] = host
    elif backend == "openai-compatible":
        provenance["engine"] = "openai-compatible"
        if _clean(api_model):
            provenance["model"] = _clean(api_model)
        if _clean(api_base_url):
            provenance["baseUrl"] = _clean(api_base_url)
            host = _clean(urlparse(str(api_base_url)).netloc)
            if host:
                provenance["host"] = host
    elif backend == "local-transformers":
        provenance["engine"] = "transformers"
        if _clean(local_llm_model):
            provenance["model"] = _clean(local_llm_model)
        if _clean(local_llm_device):
            provenance["device"] = _clean(local_llm_device)
    elif backend == "local-ollama":
        provenance["engine"] = "ollama"
        if _clean(ollama_model):
            provenance["model"] = _clean(ollama_model)
    else:
        provenance["engine"] = backend
    return provenance


def build_artifact_provenance(
    *,
    speech_to_text: dict[str, Any] | None,
    readable_cleanup: dict[str, Any] | None,
) -> dict[str, Any]:
    provenance: dict[str, Any] = {}
    if speech_to_text:
        provenance["speechToText"] = speech_to_text
    if readable_cleanup:
        provenance["readableCleanup"] = readable_cleanup
    return provenance


def _clean(value: Any) -> str:
    if value is None:
        return ""
    return str(value).strip()
