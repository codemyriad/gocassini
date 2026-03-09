from __future__ import annotations

import json
import mimetypes
import shutil
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Protocol


class TranscriptionClient(Protocol):
    def validate_environment(self) -> None: ...

    def transcribe(self, audio_path: Path, timeout_seconds: int) -> dict[str, Any]: ...

    def release_resources(self) -> None: ...

    def cache_key(self) -> str: ...


@dataclass(frozen=True)
class HTTPTranscriptionConfig:
    url: str


class HTTPTranscriptionClient:
    def __init__(self, config: HTTPTranscriptionConfig) -> None:
        self._config = config

    def validate_environment(self) -> None:
        if not self._config.url.strip():
            raise ValueError("HTTP transcription backend requires a non-empty URL")

    def transcribe(self, audio_path: Path, timeout_seconds: int) -> dict[str, Any]:
        mime_type = mimetypes.guess_type(audio_path.name)[0] or "application/octet-stream"
        boundary = uuid.uuid4().hex
        with audio_path.open("rb") as handle:
            audio_bytes = handle.read()

        parts = [
            f"--{boundary}\r\n".encode("ascii"),
            (
                f'Content-Disposition: form-data; name="file"; filename="{audio_path.name}"\r\n'
            ).encode("utf-8"),
            f"Content-Type: {mime_type}\r\n\r\n".encode("ascii"),
            audio_bytes,
            b"\r\n",
            f"--{boundary}--\r\n".encode("ascii"),
        ]
        payload = b"".join(parts)
        request = urllib.request.Request(
            self._config.url,
            data=payload,
            headers={
                "Accept": "application/json",
                "Content-Type": f"multipart/form-data; boundary={boundary}",
                "Content-Length": str(len(payload)),
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
                body = response.read()
        except urllib.error.HTTPError as exc:
            details = exc.read().decode("utf-8", errors="replace")
            raise RuntimeError(
                f"Transcription request failed for {audio_path.name}: HTTP {exc.code}: {details}"
            ) from exc
        return json.loads(body.decode("utf-8"))

    def release_resources(self) -> None:
        return

    def cache_key(self) -> str:
        return "http"


@dataclass(frozen=True)
class LocalWhisperConfig:
    model: str
    device: str = "cuda"
    download_root: str | None = None
    language: str | None = None


class LocalWhisperTranscriptionClient:
    def __init__(self, config: LocalWhisperConfig) -> None:
        self._config = config
        self._model: Any | None = None

    def validate_environment(self) -> None:
        if not self._config.model.strip():
            raise RuntimeError("Local Whisper transcription requires a non-empty model name")
        if not shutil.which("ffmpeg"):
            raise RuntimeError("Local Whisper transcription requires ffmpeg on PATH")
        try:
            from faster_whisper import WhisperModel  # noqa: F401
        except ImportError as exc:
            raise RuntimeError(
                "Local Whisper transcription requires the 'faster-whisper' Python package"
            ) from exc

    def _ensure_model(self) -> Any:
        if self._model is not None:
            return self._model

        from faster_whisper import WhisperModel  # type: ignore[import-not-found]

        compute_type = "float16" if self._config.device == "cuda" else "int8"
        self._model = WhisperModel(
            self._config.model,
            device=self._config.device,
            compute_type=compute_type,
            download_root=self._config.download_root,
        )
        return self._model

    def transcribe(self, audio_path: Path, timeout_seconds: int) -> dict[str, Any]:
        del timeout_seconds
        model = self._ensure_model()
        segments, info = model.transcribe(
            str(audio_path),
            language=self._config.language,
            word_timestamps=True,
            condition_on_previous_text=False,
        )
        del info
        words: list[dict[str, Any]] = []
        segment_texts: list[str] = []
        for segment in segments:
            segment_texts.append(segment.text.strip())
            for word in segment.words or []:
                text = str(word.word or "").strip()
                if not text:
                    continue
                words.append(
                    {
                        "word": text,
                        "start": float(word.start or 0.0),
                        "end": float(word.end or 0.0),
                        "confidence": float(word.probability)
                        if isinstance(word.probability, (float, int))
                        else None,
                    }
                )
        normalized_words = []
        for word in words:
            normalized = {
                "word": word["word"],
                "start": word["start"],
                "end": word["end"],
            }
            if word.get("confidence") is not None:
                normalized["confidence"] = word["confidence"]
            normalized_words.append(normalized)
        return {
            "text": " ".join(text for text in segment_texts if text).strip(),
            "words": normalized_words,
        }

    def release_resources(self) -> None:
        self._model = None
    
    def cache_key(self) -> str:
        language = self._config.language or "auto"
        return f"local-whisper--{self._config.model}--{self._config.device}--{language}"
