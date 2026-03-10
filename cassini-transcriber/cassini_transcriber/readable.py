from __future__ import annotations

import json
import shutil
from pathlib import Path
from typing import Any

from .llm import (
    LocalOllamaChatClient,
    LocalOllamaConfig,
    LocalTransformersChatClient,
    LocalTransformersConfig,
    OpenAICompatibleChatClient,
    OpenAICompatibleConfig,
    OpenWebUIChatClient,
    OpenWebUIConfig,
    ReadableTranscriptClient,
    ReadableTranscriptRecord,
)


class PassthroughReadableClient:
    def validate_environment(self) -> None:
        return

    def rewrite_readable_records(
        self,
        records: list[ReadableTranscriptRecord],
    ) -> list[str]:
        return [record.text for record in records]


def load_transcript_payload(path: Path) -> dict[str, Any]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ValueError(f"Could not parse transcript JSON: {path}") from exc
    if not isinstance(payload, dict):
        raise ValueError(f"Transcript payload must be a JSON object: {path}")
    if payload.get("version") != "transcript.words.v1":
        raise ValueError(f"Unsupported transcript version in {path}: {payload.get('version')!r}")
    return payload


def resolve_readable_backend(
    *,
    readable_backend: str,
    readable_transcript_name: str | None,
    api_base_url: str | None,
    api_key: str | None,
    api_model: str | None,
    openwebui_base_url: str | None,
    openwebui_email: str | None,
    openwebui_password: str | None,
    openwebui_model: str | None,
    ollama_binary: str,
) -> str:
    if not readable_transcript_name:
        return "none"

    if readable_backend != "auto":
        return readable_backend

    if api_key and (api_model or api_base_url):
        return "openai-compatible"
    if all((openwebui_base_url, openwebui_email, openwebui_password, openwebui_model)):
        return "openwebui"
    if shutil.which(ollama_binary):
        return "local-ollama"
    return "none"


def create_readable_client(
    *,
    readable_backend: str,
    api_base_url: str | None = None,
    api_key: str | None = None,
    api_model: str | None = None,
    api_timeout_seconds: int = 240,
    api_app_name: str = "gocassini",
    api_site_url: str | None = None,
    openwebui_base_url: str | None = None,
    openwebui_email: str | None = None,
    openwebui_password: str | None = None,
    openwebui_model: str | None = None,
    openwebui_timeout_seconds: int = 240,
    local_llm_model: str = "Qwen/Qwen2.5-1.5B-Instruct",
    local_llm_device: str = "cuda",
    local_llm_download_root: Path | None = None,
    local_llm_max_new_tokens: int = 1024,
    ollama_model: str = "qwen35-9b-q4:latest",
    ollama_binary: str = "ollama",
    ollama_auto_pull: bool = True,
    allow_none_passthrough: bool = False,
) -> ReadableTranscriptClient:
    if readable_backend == "none":
        if allow_none_passthrough:
            client: ReadableTranscriptClient = PassthroughReadableClient()
            client.validate_environment()
            return client
        raise ValueError("Readable transcript output was requested, but readable_backend=none")

    if readable_backend == "openwebui":
        missing_openwebui = [
            name
            for name, value in (
                ("openwebui_base_url", openwebui_base_url),
                ("openwebui_email", openwebui_email),
                ("openwebui_password", openwebui_password),
                ("openwebui_model", openwebui_model),
            )
            if not value
        ]
        if missing_openwebui:
            raise ValueError(
                "Readable transcript generation with Open WebUI requires: "
                + ", ".join(missing_openwebui)
            )
        client = OpenWebUIChatClient(
            OpenWebUIConfig(
                base_url=str(openwebui_base_url),
                email=str(openwebui_email),
                password=str(openwebui_password),
                model=str(openwebui_model),
                timeout_seconds=openwebui_timeout_seconds,
            )
        )
        client.validate_environment()
        return client

    if readable_backend == "openai-compatible":
        missing_api = [
            name
            for name, value in (
                ("api_base_url", api_base_url),
                ("api_key", api_key),
                ("api_model", api_model),
            )
            if not value
        ]
        if missing_api:
            raise ValueError(
                "Readable transcript generation with an OpenAI-compatible API requires: "
                + ", ".join(missing_api)
            )
        client = OpenAICompatibleChatClient(
            OpenAICompatibleConfig(
                base_url=str(api_base_url),
                api_key=str(api_key),
                model=str(api_model),
                timeout_seconds=api_timeout_seconds,
                app_name=api_app_name,
                site_url=api_site_url,
            )
        )
        client.validate_environment()
        return client

    if readable_backend == "local-transformers":
        client = LocalTransformersChatClient(
            LocalTransformersConfig(
                model=local_llm_model,
                device=local_llm_device,
                download_root=(
                    str(local_llm_download_root.resolve()) if local_llm_download_root else None
                ),
                max_new_tokens=local_llm_max_new_tokens,
            )
        )
        client.validate_environment()
        return client

    if readable_backend == "local-ollama":
        client = LocalOllamaChatClient(
            LocalOllamaConfig(
                model=ollama_model,
                binary=ollama_binary,
                auto_pull=ollama_auto_pull,
            )
        )
        client.validate_environment()
        return client

    raise ValueError(f"Unsupported readable transcript backend: {readable_backend}")
