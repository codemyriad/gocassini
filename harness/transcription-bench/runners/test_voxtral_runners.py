#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from voxtral_realtime import processor_for_delay
from voxtral_support import (
    ResultTelemetry,
    VOXTRAL_OFFLINE_SNAPSHOT,
    VOXTRAL_REALTIME_SNAPSHOT,
    environment_requests_offline,
    resolve_model_snapshot,
)


def materialize_snapshot(directory: Path, files: tuple[str, ...]) -> None:
    directory.mkdir(parents=True)
    for name in files:
        (directory / name).write_text("fixture", encoding="utf-8")


class ModelSnapshotTest(unittest.TestCase):
    def test_pinned_allowlists_exclude_duplicate_and_documentation_files(self) -> None:
        self.assertEqual(
            VOXTRAL_OFFLINE_SNAPSHOT.files,
            (
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
        self.assertEqual(
            VOXTRAL_REALTIME_SNAPSHOT.files,
            (
                "config.json",
                "generation_config.json",
                "model.safetensors",
                "params.json",
                "processor_config.json",
                "tekken.json",
            ),
        )
        for spec in (VOXTRAL_OFFLINE_SNAPSHOT, VOXTRAL_REALTIME_SNAPSHOT):
            self.assertNotIn("consolidated.safetensors", spec.files)
            self.assertNotIn("README.md", spec.files)
            self.assertNotIn(".gitattributes", spec.files)

    def test_pinned_cache_resolution_is_exact_and_can_be_network_free(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            snapshot = root / "snapshot"
            cache = root / "cache"
            materialize_snapshot(snapshot, VOXTRAL_OFFLINE_SNAPSHOT.files)
            calls = []

            def download(model_id, **options):
                calls.append((model_id, options))
                return str(snapshot)

            resolved = resolve_model_snapshot(
                VOXTRAL_OFFLINE_SNAPSHOT.model_id,
                VOXTRAL_OFFLINE_SNAPSHOT.revision,
                VOXTRAL_OFFLINE_SNAPSHOT,
                cache_dir=cache,
                local_files_only=True,
                downloader=download,
            )
            self.assertEqual(resolved, snapshot)
            self.assertEqual(len(calls), 1)
            model_id, options = calls[0]
            self.assertEqual(model_id, VOXTRAL_OFFLINE_SNAPSHOT.model_id)
            self.assertEqual(options["revision"], VOXTRAL_OFFLINE_SNAPSHOT.revision)
            self.assertTrue(options["local_files_only"])
            self.assertEqual(options["cache_dir"], str(cache))
            self.assertEqual(options["allow_patterns"], list(VOXTRAL_OFFLINE_SNAPSHOT.files))
            self.assertIs(options["token"], False)

    def test_explicit_snapshot_never_invokes_downloader_and_is_validated(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            snapshot = Path(temporary) / "snapshot"
            materialize_snapshot(snapshot, VOXTRAL_REALTIME_SNAPSHOT.files)
            downloader = mock.Mock(side_effect=AssertionError("network lookup attempted"))
            resolved = resolve_model_snapshot(
                VOXTRAL_REALTIME_SNAPSHOT.model_id,
                VOXTRAL_REALTIME_SNAPSHOT.revision,
                VOXTRAL_REALTIME_SNAPSHOT,
                snapshot_dir=snapshot,
                downloader=downloader,
            )
            self.assertEqual(resolved, snapshot.resolve())
            downloader.assert_not_called()

            (snapshot / "tekken.json").unlink()
            with self.assertRaisesRegex(RuntimeError, "missing: tekken.json"):
                resolve_model_snapshot(
                    VOXTRAL_REALTIME_SNAPSHOT.model_id,
                    VOXTRAL_REALTIME_SNAPSHOT.revision,
                    VOXTRAL_REALTIME_SNAPSHOT,
                    snapshot_dir=snapshot,
                    downloader=downloader,
                )

    def test_manual_custom_revision_retains_full_snapshot_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            snapshot = Path(temporary) / "custom"
            snapshot.mkdir()
            calls = []

            def download(model_id, **options):
                calls.append((model_id, options))
                return str(snapshot)

            resolve_model_snapshot(
                "example/custom-voxtral",
                "custom-revision",
                VOXTRAL_OFFLINE_SNAPSHOT,
                downloader=download,
            )
            _, options = calls[0]
            self.assertFalse(options["local_files_only"])
            self.assertNotIn("allow_patterns", options)
            self.assertNotIn("token", options)

    def test_local_cache_miss_has_actionable_error(self) -> None:
        def missing(*_args, **_kwargs):
            raise FileNotFoundError("not cached")

        with self.assertRaisesRegex(RuntimeError, "prewarm it, pass --snapshot-dir"):
            resolve_model_snapshot(
                VOXTRAL_OFFLINE_SNAPSHOT.model_id,
                VOXTRAL_OFFLINE_SNAPSHOT.revision,
                VOXTRAL_OFFLINE_SNAPSHOT,
                local_files_only=True,
                downloader=missing,
            )

    def test_standard_environment_variables_enable_offline_mode(self) -> None:
        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertFalse(environment_requests_offline())
            os.environ["HF_HUB_OFFLINE"] = "1"
            self.assertTrue(environment_requests_offline())
            os.environ["HF_HUB_OFFLINE"] = "false"
            os.environ["TRANSFORMERS_OFFLINE"] = "yes"
            self.assertTrue(environment_requests_offline())


class RealtimeProcessorTest(unittest.TestCase):
    def test_delay_overlay_processor_is_forced_local(self) -> None:
        class ProcessorFactory:
            calls = []

            @classmethod
            def from_pretrained(cls, path, **options):
                cls.calls.append((Path(path), options))
                return "processor"

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            snapshot = root / "snapshot"
            snapshot.mkdir()
            (snapshot / "config.json").write_text("{}", encoding="utf-8")
            (snapshot / "tekken.json").write_text(
                json.dumps({"config": {"transcription_delay_ms": 480}}),
                encoding="utf-8",
            )
            workspace = root / "work"
            workspace.mkdir()
            processor, changed = processor_for_delay(
                snapshot, 2400, workspace, ProcessorFactory
            )
            self.assertEqual(processor, "processor")
            self.assertEqual(changed, 1)
            _, options = ProcessorFactory.calls[-1]
            self.assertEqual(options, {"local_files_only": True})


class ResultTelemetryTest(unittest.TestCase):
    def test_first_result_and_progress_are_atomic_and_transcript_free(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / "worker.json"
            telemetry_dir = root / "telemetry"
            telemetry_dir.mkdir()
            stale = telemetry_dir / "worker.json.first-result.json"
            stale.write_text('{"stale":true}\n', encoding="utf-8")

            telemetry = ResultTelemetry(
                output,
                runner="test-runner",
                model_id="public/model",
                revision="abc123",
                expected_results=2,
                telemetry_dir=telemetry_dir,
            )
            self.assertFalse(stale.exists())
            initial = json.loads(telemetry.progress_path.read_text(encoding="utf-8"))
            self.assertEqual(initial["status"], "initializing")
            self.assertEqual(initial["completedResults"], 0)

            telemetry.record(
                {
                    "kind": "transcription",
                    "label": "first",
                    "audio": ["private/path/clip.wav"],
                    "elapsedSeconds": 0.25,
                    "text": "sensitive transcript",
                    "prompt": "sensitive prompt",
                }
            )
            first_text = telemetry.first_result_path.read_text(encoding="utf-8")
            first = json.loads(first_text)
            self.assertEqual(first["status"], "first-result")
            self.assertEqual(first["completedResults"], 1)
            self.assertEqual(first["latestResult"]["audio"], ["clip.wav"])
            self.assertNotIn("sensitive transcript", first_text)
            self.assertNotIn("sensitive prompt", first_text)

            telemetry.record(
                {
                    "kind": "transcription",
                    "label": "second",
                    "audio": "second.wav",
                    "delayMs": 480,
                    "text": "another transcript",
                }
            )
            self.assertEqual(
                json.loads(telemetry.first_result_path.read_text(encoding="utf-8")), first
            )
            running = json.loads(telemetry.progress_path.read_text(encoding="utf-8"))
            self.assertEqual(running["status"], "running")
            self.assertEqual(running["completedResults"], 2)
            self.assertEqual(running["latestResult"]["label"], "second")

            telemetry.complete()
            completed = json.loads(telemetry.progress_path.read_text(encoding="utf-8"))
            self.assertEqual(completed["status"], "completed")
            self.assertEqual(completed["completedResults"], 2)
            self.assertEqual(completed["invocationId"], first["invocationId"])
            self.assertEqual(list(telemetry_dir.glob(".*.tmp")), [])

    def test_parallel_outputs_have_independent_sidecars(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            first = ResultTelemetry(
                root / "one.json",
                runner="one",
                model_id="m",
                revision="r",
                expected_results=1,
            )
            second = ResultTelemetry(
                root / "two.json",
                runner="two",
                model_id="m",
                revision="r",
                expected_results=1,
            )
            self.assertNotEqual(first.progress_path, second.progress_path)
            self.assertTrue(first.progress_path.is_file())
            self.assertTrue(second.progress_path.is_file())


if __name__ == "__main__":
    unittest.main()
