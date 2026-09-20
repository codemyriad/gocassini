#!/usr/bin/env python3
"""Shared, dependency-light support for the Voxtral benchmark runners."""

from __future__ import annotations

import json
import os
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable


@dataclass(frozen=True)
class ModelSnapshotSpec:
    model_id: str
    revision: str
    files: tuple[str, ...]


# Keep only the Transformers-format weights. Both repositories also contain a
# consolidated.safetensors copy for mistral-inference, which is not read by
# these runners and nearly doubles cold-download time and disk consumption.
VOXTRAL_OFFLINE_SNAPSHOT = ModelSnapshotSpec(
    model_id="mistralai/Voxtral-Mini-3B-2507",
    revision="3060fe34b35ba5d44202ce9ff3c097642914f8f3",
    files=(
        "config.json",
        "generation_config.json",
        "model-00001-of-00002.safetensors",
        "model-00002-of-00002.safetensors",
        "model.safetensors.index.json",
        "params.json",
        "preprocessor_config.json",
        "tekken.json",
    ),
)

VOXTRAL_REALTIME_SNAPSHOT = ModelSnapshotSpec(
    model_id="mistralai/Voxtral-Mini-4B-Realtime-2602",
    revision="2769294da9567371363522aac9bbcfdd19447add",
    files=(
        "config.json",
        "generation_config.json",
        "model.safetensors",
        "params.json",
        "processor_config.json",
        "tekken.json",
    ),
)


def environment_requests_offline() -> bool:
    """Return whether standard Hugging Face offline-mode variables are set."""

    false_values = {"", "0", "false", "no", "off"}
    return any(
        os.environ.get(name, "").strip().casefold() not in false_values
        for name in ("HF_HUB_OFFLINE", "TRANSFORMERS_OFFLINE")
    )


def _validate_snapshot(snapshot: Path, required_files: tuple[str, ...]) -> Path:
    if not snapshot.is_dir():
        raise RuntimeError(f"model snapshot is not a directory: {snapshot}")
    missing = [name for name in required_files if not (snapshot / name).is_file()]
    if missing:
        raise RuntimeError(
            f"model snapshot {snapshot} is incomplete; missing: {', '.join(missing)}"
        )
    return snapshot


def resolve_model_snapshot(
    model_id: str,
    revision: str,
    pinned: ModelSnapshotSpec,
    *,
    snapshot_dir: Path | None = None,
    cache_dir: Path | None = None,
    local_files_only: bool = False,
    downloader: Callable[..., str] | None = None,
) -> Path:
    """Resolve a model to a validated local directory.

    Pinned revisions use an exact allowlist. A caller-provided model or
    revision retains the historical full-snapshot behavior because guessing a
    file list for an arbitrary repository could create an unusable snapshot.
    """

    exact_pin = model_id == pinned.model_id and revision == pinned.revision
    required_files = pinned.files if exact_pin else ()
    if snapshot_dir is not None:
        return _validate_snapshot(snapshot_dir.expanduser().resolve(), required_files)

    if downloader is None:
        from huggingface_hub import snapshot_download

        downloader = snapshot_download

    options: dict[str, Any] = {
        "revision": revision,
        "local_files_only": local_files_only,
    }
    if cache_dir is not None:
        options["cache_dir"] = str(cache_dir.expanduser())
    if exact_pin:
        options["allow_patterns"] = list(pinned.files)
        # Both pinned repositories are public. Avoid accidentally consulting an
        # ambient personal token while preparing a portable benchmark cache.
        options["token"] = False

    try:
        resolved = Path(downloader(model_id, **options))
    except Exception as error:
        if not local_files_only:
            raise
        raise RuntimeError(
            f"model {model_id}@{revision} is not complete in the local Hugging Face cache; "
            "prewarm it, pass --snapshot-dir, or omit --local-files-only for a manual download"
        ) from error
    return _validate_snapshot(resolved, required_files)


def _atomic_json_write(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
    try:
        with temporary.open("w", encoding="utf-8") as destination:
            json.dump(value, destination, indent=2, sort_keys=False)
            destination.write("\n")
            destination.flush()
            os.fsync(destination.fileno())
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def _result_summary(result: dict[str, Any], index: int) -> dict[str, Any]:
    """Return progress metadata without copying transcript or prompt content."""

    summary: dict[str, Any] = {"index": index}
    for name in (
        "kind",
        "label",
        "challengeCase",
        "delayMs",
        "audioSeconds",
        "aggregateAudioSeconds",
        "elapsedSeconds",
        "rtf",
    ):
        value = result.get(name)
        if isinstance(value, (str, int, float)) and not isinstance(value, bool):
            summary[name] = value
    audio = result.get("audio")
    if isinstance(audio, str):
        summary["audio"] = Path(audio).name
    elif isinstance(audio, list) and all(isinstance(item, str) for item in audio):
        summary["audio"] = [Path(item).name for item in audio]
    return summary


class ResultTelemetry:
    """Atomically expose first-result and current-progress runner milestones."""

    schema = "gocassini.transcription-runner-progress.v1"

    def __init__(
        self,
        output: Path,
        *,
        runner: str,
        model_id: str,
        revision: str,
        expected_results: int,
        telemetry_dir: Path | None = None,
        wall_clock: Callable[[], float] = time.time,
        monotonic_clock: Callable[[], float] = time.perf_counter,
    ) -> None:
        directory = telemetry_dir or output.parent / ".telemetry"
        self.progress_path = directory / f"{output.name}.progress.json"
        self.first_result_path = directory / f"{output.name}.first-result.json"
        self.runner = runner
        self.model_id = model_id
        self.revision = revision
        self.expected_results = expected_results
        self.wall_clock = wall_clock
        self.monotonic_clock = monotonic_clock
        self.started_at = wall_clock()
        self.started_monotonic = monotonic_clock()
        self.invocation_id = uuid.uuid4().hex
        self.completed_results = 0
        self.first_result_at: float | None = None
        self.latest: dict[str, Any] | None = None

        # A reused output name must never expose a previous invocation's first
        # result as the current one. The invocation ID also lets a poller
        # correlate the two independently replaced files.
        self.first_result_path.unlink(missing_ok=True)
        self._write_progress("initializing")

    def _base_payload(self, status: str, now: float) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "schema": self.schema,
            "status": status,
            "invocationId": self.invocation_id,
            "runner": self.runner,
            "model": self.model_id,
            "modelRevision": self.revision,
            "startedAtEpochSeconds": self.started_at,
            "updatedAtEpochSeconds": now,
            "elapsedSeconds": self.monotonic_clock() - self.started_monotonic,
            "expectedResults": self.expected_results,
            "completedResults": self.completed_results,
        }
        if self.first_result_at is not None:
            payload["firstResultAtEpochSeconds"] = self.first_result_at
        if self.latest is not None:
            payload["latestResult"] = self.latest
        return payload

    def _write_progress(self, status: str) -> None:
        _atomic_json_write(self.progress_path, self._base_payload(status, self.wall_clock()))

    def record(self, result: dict[str, Any]) -> None:
        self.completed_results += 1
        self.latest = _result_summary(result, self.completed_results)
        now = self.wall_clock()
        if self.completed_results == 1:
            self.first_result_at = now
            first = self._base_payload("first-result", now)
            first["timeToFirstResultSeconds"] = (
                self.monotonic_clock() - self.started_monotonic
            )
            _atomic_json_write(self.first_result_path, first)
        _atomic_json_write(self.progress_path, self._base_payload("running", now))

    def complete(self) -> None:
        self._write_progress("completed")
