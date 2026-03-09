from __future__ import annotations

import json
import re
import shutil
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Protocol

DEFAULT_READABLE_BATCH_RECORDS = 36
DEFAULT_READABLE_BATCH_CHARS = 7000


@dataclass(frozen=True)
class OpenWebUIConfig:
    base_url: str
    email: str
    password: str
    model: str
    timeout_seconds: int = 240


@dataclass(frozen=True)
class ReadableTranscriptRecord:
    index: int
    speaker_id: str | None
    speaker_label: str
    start_ms: int
    end_ms: int
    text: str
    source_segment_ids: tuple[str, ...]


@dataclass(frozen=True)
class OpenAICompatibleConfig:
    base_url: str
    api_key: str
    model: str
    timeout_seconds: int = 240
    app_name: str = "gocassini"
    site_url: str | None = None


class OpenWebUIChatClient:
    def __init__(self, config: OpenWebUIConfig) -> None:
        self._config = config
        self._token: str | None = None

    def rewrite_readable_records(
        self,
        records: list[ReadableTranscriptRecord],
    ) -> list[str]:
        prompt = build_readable_records_prompt(records)
        content = self._chat_completion(
            prompt,
            system_prompt=(
                "Return only rewritten records in the @@index@@ text format. "
                "No markdown, no JSON, no commentary."
            ),
            max_tokens=estimate_completion_tokens(records),
            retry_on_unauthorized=True,
        )
        return parse_readable_records_response(content, records)

    def _chat_completion(
        self,
        prompt: str,
        *,
        system_prompt: str,
        max_tokens: int,
        retry_on_unauthorized: bool,
    ) -> str:
        token = self._ensure_token()
        payload = {
            "model": self._config.model,
            "temperature": 0,
            "stream": False,
            "max_tokens": max_tokens,
            "messages": [
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": prompt},
            ],
        }
        try:
            response = self._post_json(
                "/api/chat/completions",
                payload,
                headers={"Authorization": f"Bearer {token}"},
            )
        except urllib.error.HTTPError as error:
            if retry_on_unauthorized and error.code == 401:
                self._token = None
                return self._chat_completion(
                    prompt,
                    system_prompt=system_prompt,
                    max_tokens=max_tokens,
                    retry_on_unauthorized=False,
                )
            raise
        choices = response.get("choices") or []
        if not choices:
            raise ValueError("LLM response did not include choices")
        message = choices[0].get("message") or {}
        content = message.get("content")
        if not isinstance(content, str):
            raise ValueError("LLM response content was not a string")
        return content

    def _ensure_token(self) -> str:
        if self._token is not None:
            return self._token
        response = self._post_json(
            "/api/v1/auths/signin",
            {
                "email": self._config.email,
                "password": self._config.password,
            },
        )
        token = response.get("token")
        if not isinstance(token, str) or not token:
            raise ValueError("Open WebUI sign-in did not return a bearer token")
        self._token = token
        return token

    def _post_json(
        self,
        path: str,
        payload: dict[str, Any],
        *,
        headers: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        request_headers = {"Content-Type": "application/json"}
        if headers:
            request_headers.update(headers)
        request = urllib.request.Request(
            urllib.parse.urljoin(self._normalized_base_url(), path),
            data=json.dumps(payload).encode("utf-8"),
            headers=request_headers,
        )
        with urllib.request.urlopen(request, timeout=self._config.timeout_seconds) as response:
            return json.loads(response.read().decode("utf-8"))

    def _normalized_base_url(self) -> str:
        return self._config.base_url.rstrip("/") + "/"


class OpenAICompatibleChatClient:
    def __init__(self, config: OpenAICompatibleConfig) -> None:
        self._config = config

    def validate_environment(self) -> None:
        if not self._config.base_url.strip():
            raise RuntimeError("OpenAI-compatible readable transcript generation requires a base URL")
        if not self._config.api_key.strip():
            raise RuntimeError("OpenAI-compatible readable transcript generation requires an API key")
        if not self._config.model.strip():
            raise RuntimeError("OpenAI-compatible readable transcript generation requires a model id")

    def rewrite_readable_records(
        self,
        records: list[ReadableTranscriptRecord],
    ) -> list[str]:
        payload = {
            "model": self._config.model,
            "temperature": 0,
            "messages": [
                {"role": "system", "content": readable_transcript_system_prompt()},
                {"role": "user", "content": build_readable_records_prompt(records)},
            ],
            "max_tokens": estimate_completion_tokens(records),
        }
        request_headers = {
            "Authorization": f"Bearer {self._config.api_key}",
            "HTTP-Referer": self._config.site_url or "https://gocassini.local",
            "X-Title": self._config.app_name,
        }
        request = urllib.request.Request(
            urllib.parse.urljoin(self._normalized_base_url(), "chat/completions"),
            data=json.dumps(payload).encode("utf-8"),
            headers={
                "Content-Type": "application/json",
                **request_headers,
            },
        )
        with urllib.request.urlopen(request, timeout=self._config.timeout_seconds) as response:
            content = json.loads(response.read().decode("utf-8"))
        choices = content.get("choices") or []
        if not choices:
            raise ValueError("LLM response did not include choices")
        message = choices[0].get("message") or {}
        text = message.get("content")
        if not isinstance(text, str):
            raise ValueError("LLM response content was not a string")
        return parse_readable_records_response(text, records)

    def _normalized_base_url(self) -> str:
        return self._config.base_url.rstrip("/") + "/"


@dataclass(frozen=True)
class LocalTransformersConfig:
    model: str
    device: str = "cuda"
    download_root: str | None = None
    max_new_tokens: int = 1024


class ReadableTranscriptClient(Protocol):
    def rewrite_readable_records(
        self,
        records: list["ReadableTranscriptRecord"],
    ) -> list[str]: ...

    def validate_environment(self) -> None: ...


class LocalTransformersChatClient:
    def __init__(self, config: LocalTransformersConfig) -> None:
        self._config = config
        self._model: Any | None = None
        self._tokenizer: Any | None = None

    def validate_environment(self) -> None:
        if not self._config.model.strip():
            raise RuntimeError("Local readable transcript generation requires a non-empty model id")
        try:
            import torch  # type: ignore[import-not-found]
            import transformers  # noqa: F401
        except ImportError as exc:
            raise RuntimeError(
                "Local readable transcript generation requires the 'torch' and "
                "'transformers' Python packages"
            ) from exc
        if self._config.device == "cuda" and not torch.cuda.is_available():
            raise RuntimeError(
                "Local readable transcript generation requested CUDA, but no CUDA device is available"
            )

    def _ensure_model(self) -> tuple[Any, Any]:
        if self._model is not None and self._tokenizer is not None:
            return self._model, self._tokenizer

        import torch  # type: ignore[import-not-found]
        from transformers import AutoModelForCausalLM, AutoTokenizer  # type: ignore[import-not-found]

        self._tokenizer = AutoTokenizer.from_pretrained(
            self._config.model,
            cache_dir=self._config.download_root,
        )
        torch_dtype = torch.float16 if self._config.device == "cuda" else torch.float32
        self._model = AutoModelForCausalLM.from_pretrained(
            self._config.model,
            cache_dir=self._config.download_root,
            torch_dtype=torch_dtype,
        )
        self._model.to(self._config.device)
        self._model.eval()
        return self._model, self._tokenizer

    def rewrite_readable_records(
        self,
        records: list["ReadableTranscriptRecord"],
    ) -> list[str]:
        model, tokenizer = self._ensure_model()
        prompt = build_readable_records_prompt(records)
        messages = [
            {
                "role": "system",
                "content": readable_transcript_system_prompt(),
            },
            {"role": "user", "content": prompt},
        ]
        if hasattr(tokenizer, "apply_chat_template"):
            rendered_prompt = tokenizer.apply_chat_template(
                messages,
                tokenize=False,
                add_generation_prompt=True,
            )
        else:
            rendered_prompt = (
                f"System: {messages[0]['content']}\n\nUser: {messages[1]['content']}\n\nAssistant:"
            )

        import torch  # type: ignore[import-not-found]

        inputs = tokenizer(rendered_prompt, return_tensors="pt")
        inputs = {key: value.to(self._config.device) for key, value in inputs.items()}
        with torch.inference_mode():
            outputs = model.generate(
                **inputs,
                do_sample=False,
                max_new_tokens=min(
                    self._config.max_new_tokens,
                    max(estimate_completion_tokens(records), 256),
                ),
                pad_token_id=tokenizer.pad_token_id or tokenizer.eos_token_id,
            )
        completion_tokens = outputs[0][inputs["input_ids"].shape[1] :]
        content = tokenizer.decode(completion_tokens, skip_special_tokens=True)
        return parse_readable_records_response(content, records)


@dataclass(frozen=True)
class LocalOllamaConfig:
    model: str
    binary: str = "ollama"
    auto_pull: bool = True


class LocalOllamaChatClient:
    def __init__(self, config: LocalOllamaConfig) -> None:
        self._config = config

    def validate_environment(self) -> None:
        if not self._config.model.strip():
            raise RuntimeError("Local Ollama readable transcript generation requires a non-empty model id")
        if not shutil.which(self._config.binary):
            raise RuntimeError(
                f"Local Ollama readable transcript generation requires '{self._config.binary}' on PATH"
            )
        show = subprocess.run(
            [self._config.binary, "show", self._config.model],
            capture_output=True,
            text=True,
        )
        if show.returncode == 0:
            return
        if not self._config.auto_pull:
            raise RuntimeError(
                f"Ollama model '{self._config.model}' is not available: "
                f"{show.stderr.strip() or show.stdout.strip()}"
            )
        pull = subprocess.run(
            [self._config.binary, "pull", self._config.model],
            capture_output=True,
            text=True,
        )
        if pull.returncode != 0:
            raise RuntimeError(
                f"Could not pull Ollama model '{self._config.model}': "
                f"{pull.stderr.strip() or pull.stdout.strip()}"
            )

    def rewrite_readable_records(
        self,
        records: list["ReadableTranscriptRecord"],
    ) -> list[str]:
        prompt = (
            readable_transcript_system_prompt()
            + "\n\n"
            + build_readable_records_prompt(records)
        )
        completed = subprocess.run(
            [self._config.binary, "run", self._config.model, prompt],
            check=False,
            capture_output=True,
            text=True,
        )
        if completed.returncode != 0:
            raise RuntimeError(
                f"Ollama generation failed: {completed.stderr.strip() or completed.stdout.strip()}"
            )
        return parse_readable_records_response(completed.stdout, records)


def normalize_readable_text(text: str) -> str:
    return " ".join(text.split()).strip()


def plan_readable_windows(
    segments: list[dict[str, Any]],
    *,
    max_gap_ms: int,
    max_window_ms: int,
    max_window_words: int,
) -> list[list[dict[str, Any]]]:
    if not segments:
        return []

    windows: list[list[dict[str, Any]]] = []
    current: list[dict[str, Any]] = []
    current_word_count = 0

    def flush() -> None:
        nonlocal current
        nonlocal current_word_count
        if not current:
            return
        windows.append(current)
        current = []
        current_word_count = 0

    for segment in segments:
        segment_words = len(segment.get("words") or [])
        if current:
            previous = current[-1]
            gap_ms = int(segment["startMs"]) - int(previous["endMs"])
            window_duration_ms = int(segment["endMs"]) - int(current[0]["startMs"])
            same_speaker = segment.get("speaker") == current[0].get("speaker")
            should_flush = (
                not same_speaker
                or gap_ms > max_gap_ms
                or current_word_count + segment_words > max_window_words
                or window_duration_ms > max_window_ms
            )
            if should_flush:
                flush()
        current.append(segment)
        current_word_count += segment_words

    flush()
    return windows


def build_readable_records(
    *,
    transcript_payload: dict[str, Any],
    max_gap_ms: int,
    max_window_ms: int,
    max_window_words: int,
) -> list[ReadableTranscriptRecord]:
    speakers = list(transcript_payload.get("speakers", []))
    speaker_labels = {
        speaker["id"]: speaker.get("label", speaker["id"])
        for speaker in speakers
        if "id" in speaker
    }
    source_segments = list(transcript_payload.get("segments", []))
    windows = plan_readable_windows(
        source_segments,
        max_gap_ms=max_gap_ms,
        max_window_ms=max_window_ms,
        max_window_words=max_window_words,
    )
    records: list[ReadableTranscriptRecord] = []
    for index, window in enumerate(windows, start=1):
        speaker_id = window[0].get("speaker")
        records.append(
            ReadableTranscriptRecord(
                index=index,
                speaker_id=speaker_id,
                speaker_label=speaker_labels.get(speaker_id, speaker_id or "Unknown speaker"),
                start_ms=int(window[0]["startMs"]),
                end_ms=int(window[-1]["endMs"]),
                text=" ".join(segment["text"] for segment in window).strip(),
                source_segment_ids=tuple(
                    str(segment["id"]) for segment in window if isinstance(segment.get("id"), str)
                ),
            )
        )
    return records


def plan_readable_record_batches(
    records: list[ReadableTranscriptRecord],
    *,
    max_batch_records: int = DEFAULT_READABLE_BATCH_RECORDS,
    max_batch_chars: int = DEFAULT_READABLE_BATCH_CHARS,
) -> list[list[ReadableTranscriptRecord]]:
    if not records:
        return []

    batches: list[list[ReadableTranscriptRecord]] = []
    current: list[ReadableTranscriptRecord] = []
    current_chars = 0

    def flush() -> None:
        nonlocal current
        nonlocal current_chars
        if not current:
            return
        batches.append(current)
        current = []
        current_chars = 0

    for record in records:
        record_chars = len(record.text) + len(record.speaker_label) + 24
        if current and (
            len(current) >= max_batch_records or current_chars + record_chars > max_batch_chars
        ):
            flush()
        current.append(record)
        current_chars += record_chars

    flush()
    return batches


def build_readable_records_prompt(records: list[ReadableTranscriptRecord]) -> str:
    if not records:
        raise ValueError("At least one readable transcript record is required")
    lines = [
        f"@@{record.index}@@ {record.speaker_label} | {record.text}"
        for record in records
    ]
    return (
        "Rewrite each transcript window into readable text without paraphrasing.\n"
        "Input records start with @@index@@ speakerLabel | raw ASR text.\n"
        "Output exactly one rewritten record for each input record using the same marker format "
        "and nothing else:\n"
        "@@index@@ cleaned readable text\n"
        "Rules:\n"
        "- Keep the same number of records and the same indexes.\n"
        "- Preserve meaning, order, and surviving wording.\n"
        "- Only delete filler words, immediate repetitions, and obvious false starts.\n"
        "- Add punctuation and casing.\n"
        "- Do not paraphrase, reorder, or replace words with synonyms.\n"
        "- Keep technical terms and names as heard, even if uncertain.\n"
        "- Do not merge records, drop records, add new records, or add speaker labels.\n"
        "- If a record is noisy or uncertain, keep the surviving words rather than inventing text.\n"
        f"- This batch contains exactly records {records[0].index}..{records[-1].index}.\n"
        "Transcript:\n"
        + "\n".join(lines)
    )


def readable_transcript_system_prompt() -> str:
    return (
        "You clean noisy ASR transcript windows.\n"
        "Return only rewritten records in the @@index@@ text format.\n"
        "No markdown, no JSON, no explanations, no surrounding prose.\n"
        "Keep the same number of records and the same indexes."
    )


def parse_readable_records_response(
    raw_content: str,
    records: list[ReadableTranscriptRecord],
) -> list[str]:
    matches = re.findall(r"@@(\d+)@@\s*(.*?)(?=\s*@@\d+@@|$)", raw_content, re.S)
    if len(matches) != len(records):
        raise ValueError(
            f"Readable transcript response returned {len(matches)} records, "
            f"expected {len(records)}"
        )

    cleaned: list[str] = []
    for expected_record, (index_text, text) in zip(records, matches, strict=True):
        if int(index_text) != expected_record.index:
            raise ValueError(
                f"Readable transcript response returned record {index_text}, "
                f"expected {expected_record.index}"
            )
        normalized = normalize_readable_text(text)
        speaker_prefix = f"{expected_record.speaker_label} | "
        if normalized.startswith(speaker_prefix):
            normalized = normalized[len(speaker_prefix) :].strip()
        if not normalized:
            raise ValueError(f"Readable transcript response for {expected_record.index} was empty")
        cleaned.append(normalized)
    return cleaned


def estimate_completion_tokens(records: list[ReadableTranscriptRecord]) -> int:
    total_chars = sum(len(record.text) for record in records)
    return max(512, min(3000, int(total_chars / 3)))


def build_readable_transcript_payload(
    *,
    transcript_payload: dict[str, Any],
    client: ReadableTranscriptClient,
    max_gap_ms: int,
    max_window_ms: int,
    max_window_words: int,
) -> dict[str, Any]:
    speakers = list(transcript_payload.get("speakers", []))
    records = build_readable_records(
        transcript_payload=transcript_payload,
        max_gap_ms=max_gap_ms,
        max_window_ms=max_window_ms,
        max_window_words=max_window_words,
    )
    readable_segments = []
    for batch in plan_readable_record_batches(records):
        try:
            cleaned_texts = client.rewrite_readable_records(batch)
        except Exception:
            cleaned_texts = [record.text for record in batch]
        for record, readable_text in zip(batch, cleaned_texts, strict=True):
            readable_segments.append(
                {
                    "id": f"rseg_{record.index:06d}",
                    "speaker": record.speaker_id,
                    "startMs": record.start_ms,
                    "endMs": record.end_ms,
                    "text": readable_text,
                    "sourceSegmentIds": list(record.source_segment_ids),
                }
            )

    return {
        "version": "transcript.readable.v1",
        "media": dict(transcript_payload.get("media") or {}),
        "speakers": speakers,
        "segments": readable_segments,
        "sourceTranscriptVersion": transcript_payload.get("version"),
    }
