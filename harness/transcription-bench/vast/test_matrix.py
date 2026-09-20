#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import tempfile
import time
import unittest
from argparse import Namespace
from pathlib import Path
from unittest import mock

import matrix


class MatrixValidationTest(unittest.TestCase):
    def valid_matrix(self) -> dict:
        return {
            "schema": matrix.MATRIX_SCHEMA,
            "image": "pytorch/test:cuda",
            "diskGb": 40,
            "maxRuntimeMinutes": 30,
            "maxTotalCostUsd": 1.0,
            "search": {
                "gpuNames": ["RTX 5060 Ti"],
                "minGpuRamMb": 12000,
                "minCpuRamMb": 16000,
                "minReliability": 0.97,
                "maxDph": 0.3,
            },
            "workers": [
                {"name": "offline", "runner": "voxtral-offline", "arguments": ["--suite", "all"]}
            ],
        }

    def valid_plan(self, run_id: str = "20260828t120000z-abcdef") -> dict:
        dph = 0.2
        runtime = 30
        return {
            "schema": matrix.PLAN_SCHEMA,
            "runId": run_id,
            "labelPrefix": f"{matrix.LABEL_ROOT}-{run_id}",
            "createdAt": "2026-08-28T12:00:00Z",
            "matrixPath": "/tmp/matrix.json",
            "image": "pytorch/test:cuda",
            "diskGb": 40,
            "maxRuntimeMinutes": runtime,
            "maxTotalCostUsd": 1.0,
            "projectedDph": dph,
            "projectedBenchmarkCost": dph * runtime / 60,
            "projectedMinimumLifecycleCost": dph
            * (runtime * 60 + matrix.TEARDOWN_RESERVE_SECONDS)
            / 3600,
            "workers": [
                {
                    "name": "offline",
                    "runner": "voxtral-offline",
                    "arguments": ["--suite", "all"],
                    "prewarm": matrix.render_onstart("offline").metadata,
                    "label": f"{matrix.LABEL_ROOT}-{run_id}-offline",
                    "offer": {
                        "id": 111,
                        "machineId": 222,
                        "gpuName": "RTX 5060 Ti",
                        "gpuRamMb": 16000,
                        "cpuRamMb": 32000,
                        "dphTotal": dph,
                        "reliability": 0.99,
                    },
                }
            ],
        }

    def valid_state(self, run_id: str = "20260828t120000z-abcdef") -> dict:
        started = time.time()
        dph = 0.2
        return {
            "schema": matrix.STATE_SCHEMA,
            "runId": run_id,
            "labelPrefix": f"{matrix.LABEL_ROOT}-{run_id}",
            "maxRuntimeMinutes": 30,
            "maxTotalCostUsd": 1.0,
            "billingStartedAtEpoch": started,
            "budgetDph": dph,
            "lifecycleDeadlineEpoch": started + 1.0 / dph * 3600,
            "teardownReserveSeconds": matrix.TEARDOWN_RESERVE_SECONDS,
            "instances": [
                {
                    "worker": "offline",
                    "runner": "voxtral-offline",
                    "arguments": [],
                    "label": f"{matrix.LABEL_ROOT}-{run_id}-offline",
                    "offerId": 111,
                    "instanceId": 456,
                    "status": "ready",
                    "sshHost": "203.0.113.10",
                    "sshPort": 40122,
                    "sshEndpointKind": "direct",
                    "sshFallbacks": [],
                    "actualDph": dph,
                }
            ],
        }

    def valid_prewarm_ready(self, spec, *, fingerprint: str | None = None) -> dict:
        return {
            "schema": matrix.PREWARM_READY_SCHEMA,
            "status": "ready",
            "role": spec.role,
            "fingerprint": fingerprint or spec.fingerprint,
            "startedAtEpoch": 1000.0,
            "finishedAtEpoch": 1002.5,
            "durationSeconds": 2.5,
            "cacheHit": True,
            "venvPython": spec.python_path,
            "snapshotPath": spec.snapshot_path,
            "model": {
                "id": spec.model_id,
                "revision": spec.model_revision,
                "files": list(spec.model_files),
            },
            "snapshotBytes": 123,
            "freeBytes": 456,
            "runtime": {
                "python": "3.11.13",
                "torch": "2.8.0+cu128",
                "cuda": "12.8",
                "device": "test gpu",
                "bf16": True,
                "torchPath": "/opt/torch/__init__.py",
                "packages": dict(spec.package_versions),
            },
            "logPath": spec.log_path,
        }

    def write_valid_voxtral_bundle(
        self,
        root: Path,
        item: dict,
        *,
        fixture_hash: str | None = None,
        progress_model: str | None = None,
    ) -> None:
        fixture_hash = fixture_hash or matrix._fixture_manifest_identity()[0]
        fixture_set = matrix._fixture_manifest_identity()[1]
        role = "offline" if item["runner"] == "voxtral-offline" else "realtime"
        spec = matrix.render_onstart(role)
        schema = (
            "gocassini.voxtral-offline-benchmark.v2"
            if role == "offline"
            else "gocassini.voxtral-realtime-benchmark.v2"
        )
        (root / f"{item['worker']}.json").write_text(
            json.dumps(
                {
                    "schema": schema,
                    "fixtureSet": fixture_set,
                    "fixtureManifestSha256": fixture_hash,
                    "model": spec.model_id,
                    "modelRevision": spec.model_revision,
                    "results": [{"audio": "fixture.wav", "text": "example"}],
                }
            ),
            encoding="utf-8",
        )
        (root / f"{item['worker']}.status.json").write_text(
            json.dumps(
                {
                    "schema": matrix.REMOTE_STATUS_SCHEMA,
                    "exitCode": 0,
                    "startedAtEpoch": 1000.0,
                    "endedAtEpoch": 1010.0,
                }
            ),
            encoding="utf-8",
        )
        telemetry = root / ".telemetry"
        telemetry.mkdir(parents=True)
        invocation = "a" * 32
        common = {
            "schema": matrix.PROGRESS_SCHEMA,
            "invocationId": invocation,
            "runner": item["runner"],
            "model": progress_model or spec.model_id,
            "modelRevision": spec.model_revision,
            "startedAtEpochSeconds": 1000.0,
            "updatedAtEpochSeconds": 1004.25,
            "elapsedSeconds": 4.25,
            "expectedResults": 1,
            "completedResults": 1,
            "firstResultAtEpochSeconds": 1004.0,
        }
        (telemetry / f"{item['worker']}.json.progress.json").write_text(
            json.dumps({**common, "status": "completed"}), encoding="utf-8"
        )
        (telemetry / f"{item['worker']}.json.first-result.json").write_text(
            json.dumps(
                {
                    **common,
                    "status": "first-result",
                    "timeToFirstResultSeconds": 4.25,
                }
            ),
            encoding="utf-8",
        )

    def write_valid_parakeet_bundle(self, root: Path, item: dict) -> None:
        fixture_hash, fixture_set = matrix._fixture_manifest_identity()
        (root / f"{item['worker']}.status.json").write_text(
            json.dumps(
                {
                    "schema": matrix.REMOTE_STATUS_SCHEMA,
                    "exitCode": 0,
                    "startedAtEpoch": 1000.0,
                    "endedAtEpoch": 1010.0,
                }
            ),
            encoding="utf-8",
        )
        worker_root = root / item["worker"]
        worker_root.mkdir()
        (worker_root / "summary.json").write_text(
            json.dumps(
                {
                    "schema": "gocassini.parakeet-hotword-matrix.v1",
                    "fixtureSet": fixture_set,
                    "fixtureManifestSha256": fixture_hash,
                    "runtimeInputs": {"bench": "b" * 64},
                    "coldStart": {"provider": "cuda"},
                    "conditions": [{"label": "greedy"}],
                }
            ),
            encoding="utf-8",
        )

    def test_matrix_rejects_duplicate_worker_names(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "matrix.json"
            value = self.valid_matrix()
            value["workers"].append(dict(value["workers"][0]))
            path.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "duplicate"):
                matrix.validate_matrix(path)

    def test_plan_pins_the_prewarm_contract_before_any_rental(self) -> None:
        class FakeClient:
            def search(self, search, limit):
                return [
                    {
                        "id": 111,
                        "machine_id": 222,
                        "gpu_name": "RTX 5060 Ti",
                        "gpu_ram": 16000,
                        "cpu_ram": 32000,
                        "dph_total": 0.2,
                        "reliability": 0.99,
                    }
                ]

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            matrix_path = root / "matrix.json"
            plan_path = root / "plan.json"
            matrix_path.write_text(json.dumps(self.valid_matrix()), encoding="utf-8")
            planned = matrix.plan_matrix(FakeClient(), matrix_path, plan_path, 1)
            prewarm = planned["workers"][0]["prewarm"]
            self.assertEqual(prewarm, matrix.render_onstart("offline").metadata)
            self.assertNotIn("onstart", plan_path.read_text(encoding="utf-8"))

    def test_plan_cannot_drop_or_drift_the_prewarm_contract(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "plan.json"
            value = self.valid_plan()
            value["workers"][0].pop("prewarm")
            path.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "missing its pinned prewarm"):
                matrix.validate_plan(path)

            value = self.valid_plan()
            value["workers"][0]["prewarm"]["fingerprint"] = "0" * 64
            path.write_text(json.dumps(value), encoding="utf-8")
            with mock.patch.object(matrix, "render_worker_prewarm") as render:
                with self.assertRaisesRegex(matrix.LifecycleError, "contract changed"):
                    matrix.launch_plan(mock.Mock(), path, Path(temporary) / "state.json", 1)
            render.assert_called_once_with("voxtral-offline", "offline")

    def test_runner_command_keeps_arguments_as_argv(self) -> None:
        commands = matrix.runner_commands(
            {
                "worker": "offline",
                "runner": "voxtral-offline",
                "arguments": ["--model-id", "mistralai/example"],
            }
        )
        self.assertEqual(commands[1][-2:], ["--model-id", "mistralai/example"])
        self.assertIn("results/offline.json", commands[1])

    def test_prewarmed_runner_is_strictly_offline_and_skips_payload_bootstrap(self) -> None:
        prewarm = matrix.render_onstart("offline").metadata
        commands = matrix.runner_commands(
            {
                "worker": "offline",
                "runner": "voxtral-offline",
                "arguments": ["--suite", "baseline"],
                "prewarm": prewarm,
            }
        )
        self.assertEqual(len(commands), 1)
        self.assertIn("HF_HUB_OFFLINE=1", commands[0])
        self.assertIn(prewarm["pythonPath"], commands[0])
        self.assertIn(prewarm["snapshotPath"], commands[0])
        self.assertNotIn("scripts/bootstrap_voxtral.sh", commands[0])

    def test_vast_matrix_rejects_model_and_output_overrides(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "matrix.json"
            for argument in ("--model-id=other/model", "--output", "--snapshot-dir"):
                value = self.valid_matrix()
                value["workers"][0]["arguments"] = [argument]
                path.write_text(json.dumps(value), encoding="utf-8")
                with self.subTest(argument=argument), self.assertRaisesRegex(
                    matrix.LifecycleError, "controlled by the orchestrator"
                ):
                    matrix.validate_matrix(path)

    def test_launch_sends_bounded_public_onstart_and_persists_only_contract(self) -> None:
        class FakeClient:
            def __init__(self):
                self.live = {}
                self.body = None

            def list_by_labels(self, labels):
                return [value for value in self.live.values() if value["label"] in labels]

            def create(self, offer_id, body):
                self.body = body
                self.live[456] = {"id": 456, "label": body["label"], "actual_status": "created"}
                return 456

        with tempfile.TemporaryDirectory() as temporary, mock.patch.dict(
            os.environ,
            {
                "VAST_AI_API_KEY": "vast-secret-must-not-appear",
                "OPENROUTER_API_KEY": "router-secret-must-not-appear",
            },
        ):
            root = Path(temporary)
            plan_path = root / "plan.json"
            state_path = root / "state.json"
            plan_path.write_text(json.dumps(self.valid_plan()), encoding="utf-8")
            client = FakeClient()
            launched = matrix.launch_plan(client, plan_path, state_path, 1)
            script = client.body["onstart"]
            self.assertLess(len(script.encode()), matrix.PREWARM_SCRIPT_MAX_BYTES)
            self.assertNotIn("vast-secret-must-not-appear", script)
            self.assertNotIn("router-secret-must-not-appear", script)
            self.assertNotIn("fixtures", script)
            self.assertNotIn("ground-truth", script)
            self.assertIn("prewarm", launched["instances"][0])
            self.assertNotIn("onstart", state_path.read_text(encoding="utf-8"))

    def test_matching_prewarm_sentinel_is_accepted(self) -> None:
        spec = matrix.render_onstart("offline")
        prewarm = spec.metadata
        payload = self.valid_prewarm_ready(spec)
        ready = matrix.parse_prewarm_probe(
            ("ready\n" + json.dumps(payload)).encode(),
            prewarm,
        )
        self.assertTrue(ready["cacheHit"])
        payload.pop("schema")
        with self.assertRaisesRegex(matrix.LifecycleError, "does not match"):
            matrix.parse_prewarm_probe(
                ("ready\n" + json.dumps(payload)).encode(),
                prewarm,
            )

    def test_prewarm_poll_waits_for_exact_ready_sentinel(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state_path = root / "state.json"
            state = self.valid_state()
            spec = matrix.render_onstart("offline")
            item = state["instances"][0]
            item["prewarm"] = spec.metadata
            state_path.write_text(json.dumps(state), encoding="utf-8")
            ready = self.valid_prewarm_ready(spec)
            ready["cacheHit"] = False
            responses = [
                mock.Mock(returncode=3, stdout=b"pending\n", stderr=b""),
                mock.Mock(
                    returncode=0,
                    stdout=("ready\n" + json.dumps(ready) + "\n").encode(),
                    stderr=b"",
                ),
            ]
            with mock.patch.object(matrix, "run_transport", side_effect=responses), mock.patch.object(
                matrix, "PROGRESS_POLL_SECONDS", 0
            ):
                observed, endpoint = matrix.wait_for_prewarm(
                    state, state_path, item, None, item
                )
            self.assertEqual(observed, ready)
            self.assertEqual(endpoint["sshHost"], item["sshHost"])

    def test_failed_collection_never_publishes_partial_worker_directory(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state_path = root / "state.json"
            state = self.valid_state()
            state_path.write_text(json.dumps(state), encoding="utf-8")
            with mock.patch.object(
                matrix,
                "run_transport",
                side_effect=matrix.subprocess.CalledProcessError(1, ["scp"]),
            ), self.assertRaises(matrix.LifecycleError):
                matrix.collect_worker_results(
                    state,
                    state_path,
                    root / "output",
                    state["instances"][0],
                    None,
                )
            destination = root / "output" / state["runId"]
            self.assertFalse((destination / "offline").exists())
            self.assertEqual(list(destination.glob(".offline-collect-*")), [])

    def test_voxtral_bundle_requires_hash_model_and_completed_telemetry(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = self.valid_state()
            item = state["instances"][0]
            self.write_valid_voxtral_bundle(root, item)
            self.assertEqual(
                matrix.inspect_worker_bundle(root, state, item)["status"], "complete"
            )

            result_path = root / "offline.json"
            result = json.loads(result_path.read_text(encoding="utf-8"))
            result["fixtureManifestSha256"] = "0" * 64
            result_path.write_text(json.dumps(result), encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "Voxtral result"):
                matrix.inspect_worker_bundle(root, state, item)

            result["fixtureManifestSha256"] = matrix._fixture_manifest_identity()[0]
            result_path.write_text(json.dumps(result), encoding="utf-8")
            progress_path = root / ".telemetry" / "offline.json.progress.json"
            progress = json.loads(progress_path.read_text(encoding="utf-8"))
            progress["modelRevision"] = "0" * 40
            progress_path.write_text(json.dumps(progress), encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "completed telemetry"):
                matrix.inspect_worker_bundle(root, state, item)

    def test_collection_replaces_existing_bundle_without_stale_merge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / "output"
            state_path = root / "state.json"
            state = self.valid_state()
            item = state["instances"][0]
            state_path.write_text(json.dumps(state), encoding="utf-8")
            existing = output / state["runId"] / item["worker"]
            existing.mkdir(parents=True)
            (existing / "stale-from-earlier-attempt.txt").write_text(
                "stale", encoding="utf-8"
            )

            def fake_transport(registry, command, **kwargs):
                stage = Path(command[-1])
                self.write_valid_voxtral_bundle(stage, item)
                return mock.Mock(returncode=0, stdout=b"", stderr=b"")

            with mock.patch.object(matrix, "run_transport", side_effect=fake_transport):
                published = matrix.collect_worker_results(
                    state, state_path, output, item, None
                )
            self.assertFalse((published / "stale-from-earlier-attempt.txt").exists())
            marker = json.loads(
                (published / matrix.COLLECTION_BUNDLE_MARKER).read_text(encoding="utf-8")
            )
            self.assertEqual(marker["validatedStatus"], "complete")
            self.assertEqual(list(published.parent.glob(".offline-previous-*")), [])

    def test_final_collection_revalidates_early_bundle_before_skip(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / "output"
            state_path = root / "state.json"
            state = self.valid_state()
            item = state["instances"][0]
            item["status"] = "completed"
            worker_dir = output / state["runId"] / item["worker"]
            worker_dir.mkdir(parents=True)
            (worker_dir / "offline.json").write_text("{}", encoding="utf-8")
            item["earlyCollectedDestination"] = str(worker_dir.resolve())
            state_path.write_text(json.dumps(state), encoding="utf-8")
            transfers = 0

            def fake_transport(registry, command, **kwargs):
                nonlocal transfers
                transfers += 1
                stage = Path(command[-1])
                self.write_valid_voxtral_bundle(stage, item)
                return mock.Mock(returncode=0, stdout=b"", stderr=b"")

            with mock.patch.object(matrix, "run_transport", side_effect=fake_transport):
                matrix.collect(
                    state_path, output, None, 1, skip_early_collected=True
                )
            self.assertEqual(transfers, 1)
            self.assertEqual(
                json.loads((worker_dir / "offline.json").read_text(encoding="utf-8"))[
                    "schema"
                ],
                "gocassini.voxtral-offline-benchmark.v2",
            )

    def test_failed_and_prewarm_bundles_are_explicitly_partial(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = self.valid_state()
            item = state["instances"][0]
            item["status"] = "dispatch-failed"
            (root / "offline.status.json").write_text(
                json.dumps(
                    {
                        "schema": matrix.REMOTE_STATUS_SCHEMA,
                        "exitCode": 124,
                        "startedAtEpoch": 1000.0,
                        "endedAtEpoch": 1010.0,
                    }
                ),
                encoding="utf-8",
            )
            classification = matrix.inspect_worker_bundle(root, state, item)
            self.assertEqual(classification["status"], "partial")
            self.assertEqual(classification["remoteExitCode"], 124)

            item["status"] = "prewarming"
            item["prewarm"] = matrix.render_onstart("offline").metadata
            (root / "offline.status.json").unlink()
            diagnostics = root / "prewarm"
            diagnostics.mkdir()
            (diagnostics / "failed.json").write_text(
                json.dumps(
                    {
                        "schema": matrix.PREWARM_FAILED_SCHEMA,
                        "status": "failed",
                        "role": "offline",
                        "fingerprint": item["prewarm"]["fingerprint"],
                    }
                ),
                encoding="utf-8",
            )
            classification = matrix.inspect_worker_bundle(root, state, item)
            self.assertEqual(classification["status"], "partial")
            self.assertEqual(classification["reason"], "prewarm-failed")

    def test_partial_collection_is_published_with_integrity_marker(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            output = root / "output"
            state_path = root / "state.json"
            state = self.valid_state()
            item = state["instances"][0]
            item["status"] = "dispatch-failed"
            item["exitCode"] = 124
            state_path.write_text(json.dumps(state), encoding="utf-8")

            def fake_transport(registry, command, **kwargs):
                stage = Path(command[-1])
                (stage / "offline.status.json").write_text(
                    json.dumps(
                        {
                            "schema": matrix.REMOTE_STATUS_SCHEMA,
                            "exitCode": 124,
                            "startedAtEpoch": 1000.0,
                            "endedAtEpoch": 1010.0,
                        }
                    ),
                    encoding="utf-8",
                )
                return mock.Mock(returncode=0, stdout=b"", stderr=b"")

            with mock.patch.object(matrix, "run_transport", side_effect=fake_transport):
                published = matrix.collect_worker_results(
                    state, state_path, output, item, None
                )
            marker = json.loads(
                (published / matrix.COLLECTION_BUNDLE_MARKER).read_text(encoding="utf-8")
            )
            self.assertEqual(marker["validatedStatus"], "partial")
            self.assertEqual(marker["remoteExitCode"], 124)

    def test_completed_worker_cannot_publish_a_partial_bundle(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = self.valid_state()
            item = state["instances"][0]
            item["status"] = "completed"
            (root / "offline.status.json").write_text(
                json.dumps(
                    {
                        "schema": matrix.REMOTE_STATUS_SCHEMA,
                        "exitCode": 1,
                        "startedAtEpoch": 1000.0,
                        "endedAtEpoch": 1001.0,
                    }
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(matrix.LifecycleError, "only a partial"):
                matrix.inspect_worker_bundle(root, state, item)

    def test_parakeet_requires_nested_summary_and_fixture_hash(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = self.valid_state()
            item = state["instances"][0]
            item.update(worker="parakeet", runner="parakeet-hotwords")
            self.write_valid_parakeet_bundle(root, item)
            self.assertEqual(
                matrix.inspect_worker_bundle(root, state, item)["status"], "complete"
            )
            summary_path = root / "parakeet" / "summary.json"
            summary = json.loads(summary_path.read_text(encoding="utf-8"))
            summary["fixtureManifestSha256"] = "0" * 64
            summary_path.write_text(json.dumps(summary), encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "Parakeet summary"):
                matrix.inspect_worker_bundle(root, state, item)

    def test_per_worker_collect_refuses_running_worker(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state_path = root / "state.json"
            state = self.valid_state()
            state["instances"][0]["status"] = "running"
            state_path.write_text(json.dumps(state), encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "only safe after completion"):
                matrix.collect(state_path, root / "output", None, 1, ["offline"])

    def test_dispatch_collects_each_completed_worker_and_records_timings(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state_path = root / "state.json"
            output_dir = root / "output"
            state_path.write_text(json.dumps(self.valid_state()), encoding="utf-8")

            def fake_run(command, **kwargs):
                if command[0] == "scp" and "-r" in command:
                    stage = Path(command[-1])
                    self.write_valid_voxtral_bundle(
                        stage, self.valid_state()["instances"][0]
                    )
                return mock.Mock(returncode=0, stdout=b"", stderr=b"")

            with mock.patch.object(matrix, "package_payload"), mock.patch.object(
                matrix, "run_transport", side_effect=lambda registry, command, **kwargs: fake_run(command, **kwargs)
            ):
                dispatched = matrix.dispatch(
                    state_path,
                    root / "audio",
                    [],
                    None,
                    1,
                    early_output_dir=output_dir,
                )
            item = dispatched["instances"][0]
            self.assertEqual(item["status"], "completed")
            self.assertEqual(item["runnerTimeToFirstResultSeconds"], 4.25)
            self.assertIn("uploadStartedAtEpoch", item)
            self.assertIn("runnerEndedAtEpoch", item)
            self.assertTrue(
                (output_dir / dispatched["runId"] / "offline" / ".telemetry").is_dir()
            )

    def test_dispatch_uploads_before_waiting_for_prewarm(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state_path = root / "state.json"
            state = self.valid_state()
            spec = matrix.render_onstart("offline")
            state["instances"][0]["prewarm"] = spec.metadata
            state_path.write_text(json.dumps(state), encoding="utf-8")
            events = []

            def fake_run(command, **kwargs):
                if command[0] == "scp":
                    events.append("upload")
                elif "verify_fixtures.py" in command[-1]:
                    events.append("prepare")
                else:
                    events.append("benchmark")
                return mock.Mock(returncode=0, stdout=b"", stderr=b"")

            def fake_wait(current_state, current_path, item, identity, endpoint, transports):
                events.append("prewarm")
                return (
                    {
                        "status": "ready",
                        "role": "offline",
                        "fingerprint": spec.fingerprint,
                        "cacheHit": False,
                    },
                    endpoint,
                )

            with mock.patch.object(matrix, "package_payload"), mock.patch.object(
                matrix, "run_transport", side_effect=lambda registry, command, **kwargs: fake_run(command, **kwargs)
            ), mock.patch.object(matrix, "wait_for_prewarm", side_effect=fake_wait):
                matrix.dispatch(state_path, root / "audio", [], None, 1)
            self.assertEqual(events, ["upload", "prepare", "prewarm", "benchmark"])

    def test_dispatch_interrupt_cancels_blocked_transport_promptly(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state_path = root / "state.json"
            state = self.valid_state()
            second = dict(state["instances"][0])
            second.update(
                worker="realtime",
                runner="voxtral-realtime",
                label=f"{state['labelPrefix']}-realtime",
                offerId=112,
                instanceId=457,
            )
            state["instances"].append(second)
            state_path.write_text(json.dumps(state), encoding="utf-8")
            sleeper_started = matrix.threading.Event()

            def fake_transport(registry, command, **kwargs):
                rendered = " ".join(command)
                if "results/offline.status.json" in rendered:
                    sleeper_started.set()
                    return registry.run(
                        [matrix.sys.executable, "-c", "import time; time.sleep(30)"],
                        timeout=30,
                    )
                if "results/realtime.status.json" in rendered:
                    self.assertTrue(sleeper_started.wait(2))
                    raise KeyboardInterrupt()
                return matrix.subprocess.CompletedProcess(command, 0, b"", b"")

            started = time.monotonic()
            with mock.patch.object(matrix, "package_payload"), mock.patch.object(
                matrix, "run_transport", side_effect=fake_transport
            ), self.assertRaises(KeyboardInterrupt):
                matrix.dispatch(state_path, root / "audio", [], None, 2)
            self.assertLess(time.monotonic() - started, 4.0)

    def test_direct_ssh_mapping_precedes_proxy(self) -> None:
        candidates = matrix.ssh_endpoint_candidates(
            {
                "public_ipaddr": "203.0.113.10",
                "ports": {"22/tcp": [{"HostIp": "0.0.0.0", "HostPort": "40122"}]},
                "ssh_host": "ssh123.vast.ai",
                "ssh_port": 10222,
            }
        )
        self.assertEqual(
            candidates,
            [
                {"host": "203.0.113.10", "port": 40122, "kind": "direct"},
                {"host": "ssh123.vast.ai", "port": 10222, "kind": "proxy"},
            ],
        )

    def test_invalid_direct_mapping_falls_back_to_proxy(self) -> None:
        candidates = matrix.ssh_endpoint_candidates(
            {
                "public_ipaddr": "not-an-ip",
                "ports": {"22/tcp": [{"HostPort": "bad"}]},
                "ssh_host": "ssh123.vast.ai",
                "ssh_port": 10222,
            }
        )
        self.assertEqual(candidates, [{"host": "ssh123.vast.ai", "port": 10222, "kind": "proxy"}])

    def test_destroy_refuses_live_label_mismatch(self) -> None:
        class FakeClient:
            def list_by_labels(self, labels):
                return [{"id": 123, "label": labels[0], "actual_status": "running"}]

            def show(self, instance_id):
                return {"id": instance_id, "label": "somebody-elses-instance"}

            def destroy(self, instance_id):
                raise AssertionError(f"must not destroy {instance_id}")

        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "state.json"
            run_id = "20260828t120000z-abcdef"
            path.write_text(
                json.dumps(
                    {
                        "schema": matrix.STATE_SCHEMA,
                        "runId": run_id,
                        "labelPrefix": f"{matrix.LABEL_ROOT}-{run_id}",
                        "maxRuntimeMinutes": 30,
                        "maxTotalCostUsd": 1.0,
                        "instances": [
                            {
                                "worker": "offline",
                                "runner": "voxtral-offline",
                                "arguments": [],
                                "label": f"{matrix.LABEL_ROOT}-{run_id}-offline",
                                "instanceId": 123,
                                "status": "ready",
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(matrix.LifecycleError, "teardown incomplete"):
                matrix.destroy_owned(FakeClient(), path, 1)

    def test_destroy_rejects_tampered_out_of_scope_state_before_api(self) -> None:
        class NoAPIClient:
            def list_by_labels(self, labels):
                raise AssertionError("invalid state must be rejected before API access")

        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "state.json"
            run_id = "20260828t120000z-abcdef"
            path.write_text(
                json.dumps(
                    {
                        "schema": matrix.STATE_SCHEMA,
                        "runId": run_id,
                        "labelPrefix": f"{matrix.LABEL_ROOT}-{run_id}",
                        "maxRuntimeMinutes": 30,
                        "maxTotalCostUsd": 1.0,
                        "instances": [
                            {
                                "worker": "offline",
                                "runner": "voxtral-offline",
                                "arguments": [],
                                "label": "unrelated-production-instance",
                                "instanceId": 999,
                                "status": "ready",
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(matrix.LifecycleError, "outside exact run scope"):
                matrix.destroy_owned(NoAPIClient(), path, 1)

    def test_recover_fills_only_exact_expected_label(self) -> None:
        class FakeClient:
            def list_by_labels(self, labels):
                return [{"id": 456, "label": labels[0], "actual_status": "running"}]

        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "state.json"
            run_id = "20260828t120000z-abcdef"
            label = f"{matrix.LABEL_ROOT}-{run_id}-offline"
            path.write_text(
                json.dumps(
                    {
                        "schema": matrix.STATE_SCHEMA,
                        "runId": run_id,
                        "labelPrefix": f"{matrix.LABEL_ROOT}-{run_id}",
                        "maxRuntimeMinutes": 30,
                        "maxTotalCostUsd": 1.0,
                        "instances": [
                            {
                                "worker": "offline",
                                "runner": "voxtral-offline",
                                "arguments": [],
                                "label": label,
                                "instanceId": None,
                                "status": "launching",
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            state, recovered = matrix.recover_owned(FakeClient(), path)
            self.assertEqual(recovered, [456])
            self.assertEqual(state["instances"][0]["instanceId"], 456)

    def test_extra_secret_and_symlink_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            secret = root / ".env"
            secret.write_text("TOKEN=nope", encoding="utf-8")
            with self.assertRaisesRegex(matrix.LifecycleError, "secret-shaped"):
                matrix.checked_extra_files(secret, root / "payload.tar.gz")
            ordinary = root / "model.bin"
            ordinary.write_bytes(b"model")
            link = root / "linked-model"
            link.symlink_to(ordinary)
            with self.assertRaisesRegex(matrix.LifecycleError, "symlink"):
                matrix.checked_extra_files(link, root / "payload.tar.gz")

    def test_api_error_does_not_echo_response_body_or_key(self) -> None:
        import io
        import urllib.error

        error = urllib.error.HTTPError(
            "https://example.invalid", 403, "forbidden", {}, io.BytesIO(b"secret-response")
        )
        with mock.patch.dict(os.environ, {"VAST_AI_API_KEY": "secret-api-key"}), mock.patch(
            "urllib.request.urlopen", side_effect=error
        ):
            client = matrix.VastClient()
            with self.assertRaises(matrix.LifecycleError) as caught:
                client.request("GET", "/test")
        message = str(caught.exception)
        self.assertNotIn("secret-response", message)
        self.assertNotIn("secret-api-key", message)

    def test_lifecycle_timeout_withholds_teardown_reserve(self) -> None:
        state = {
            "billingStartedAtEpoch": 1000.0,
            "budgetDph": 1.0,
            "lifecycleDeadlineEpoch": 1600.0,
            "teardownReserveSeconds": matrix.TEARDOWN_RESERVE_SECONDS,
        }
        self.assertEqual(
            matrix.bounded_phase_timeout(state, 1000, "upload", now=1100.0), 200
        )
        with self.assertRaisesRegex(matrix.LifecycleError, "budget is exhausted"):
            matrix.bounded_phase_timeout(state, 1, "upload", now=1300.0)

    def test_definitive_stale_create_is_cleaned_and_retryable(self) -> None:
        class FakeClient:
            def __init__(self):
                self.live = {}
                self.destroyed = []

            def list_by_labels(self, labels):
                return [value for value in self.live.values() if value["label"] in labels]

            def create(self, offer_id, body):
                if offer_id == 112:
                    raise matrix.CreateRejectedError("stale", retryable_stale=True)
                self.live[456] = {"id": 456, "label": body["label"], "actual_status": "created"}
                return 456

            def show(self, instance_id):
                return self.live[instance_id]

            def destroy(self, instance_id):
                self.destroyed.append(instance_id)
                del self.live[instance_id]

        with tempfile.TemporaryDirectory() as temporary, mock.patch.object(
            matrix, "REJECTED_CREATE_RECONCILE_SECONDS", 0
        ):
            root = Path(temporary)
            plan_path = root / "plan.json"
            state_path = root / "state.json"
            plan = self.valid_plan()
            second = {
                "name": "realtime",
                "runner": "voxtral-realtime",
                "arguments": ["--delays", "480"],
                "prewarm": matrix.render_onstart("realtime").metadata,
                "label": f"{plan['labelPrefix']}-realtime",
                "offer": {
                    **plan["workers"][0]["offer"],
                    "id": 112,
                    "machineId": 223,
                },
            }
            plan["workers"].append(second)
            plan["projectedDph"] = 0.4
            plan["projectedBenchmarkCost"] = 0.4 * 30 / 60
            plan["projectedMinimumLifecycleCost"] = 0.4 * (
                30 * 60 + matrix.TEARDOWN_RESERVE_SECONDS
            ) / 3600
            plan_path.write_text(json.dumps(plan), encoding="utf-8")
            client = FakeClient()
            with self.assertRaises(matrix.StaleOfferError):
                matrix.launch_plan(client, plan_path, state_path, 2)
            state = matrix.load_state(state_path)
            self.assertEqual(
                {item["status"] for item in state["instances"]},
                {"destroyed", "not-created-confirmed"},
            )
            self.assertEqual(client.destroyed, [456])
            self.assertIn("billingEndedAtEpoch", state)

    def test_ambiguous_create_never_becomes_stale_retry(self) -> None:
        class FakeClient:
            def list_by_labels(self, labels):
                return []

            def create(self, offer_id, body):
                raise matrix.LifecycleError("transport vanished")

        with tempfile.TemporaryDirectory() as temporary, mock.patch.object(
            matrix, "CREATE_VISIBILITY_GRACE_SECONDS", 0
        ):
            root = Path(temporary)
            plan_path = root / "plan.json"
            state_path = root / "state.json"
            plan_path.write_text(json.dumps(self.valid_plan()), encoding="utf-8")
            with self.assertRaises(matrix.CreateOutcomeUncertain):
                matrix.launch_plan(FakeClient(), plan_path, state_path, 1)
            state = matrix.load_state(state_path)
            self.assertEqual(state["instances"][0]["status"], "not-created-confirmed")

    def test_run_replans_only_stale_and_spends_one_shared_cap(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = Namespace(
                command="run",
                matrix=root / "matrix.json",
                audio_dir=root / "audio",
                work_dir=root / "work",
                output_dir=root / "output",
                extra=[],
                identity=None,
                ready_timeout=600,
                launch_attempts=3,
                keep_instances=False,
                jobs=1,
            )
            plan_calls = []

            def fake_plan(client, matrix_path, plan_path, jobs, max_total_cost_usd=None):
                call = len(plan_calls) + 1
                run_id = f"20260828t12000{call}z-abcde{call}"
                plan_path.parent.mkdir(parents=True, exist_ok=True)
                plan_path.write_text("{}", encoding="utf-8")
                plan_calls.append(max_total_cost_usd)
                return {"runId": run_id}

            launch_count = 0

            def fake_launch(client, plan_path, state_path, jobs):
                nonlocal launch_count
                launch_count += 1
                state_path.write_text("{}", encoding="utf-8")
                if launch_count == 1:
                    raise matrix.StaleOfferError("stale")

            def fake_load_state(path):
                if "state-20260828t120001z" in path.name:
                    return {
                        "billingStartedAtEpoch": 100.0,
                        "billingEndedAtEpoch": 160.0,
                        "budgetDph": 0.6,
                    }
                return {
                    "runId": "20260828t120002z-abcde2",
                    "instances": [{"worker": "offline", "status": "completed"}],
                }

            with mock.patch.object(matrix, "parse_args", return_value=args), mock.patch.object(
                matrix, "VastClient", return_value=object()
            ), mock.patch.object(
                matrix, "validate_matrix", return_value={"maxTotalCostUsd": 1.0}
            ), mock.patch.object(matrix, "plan_matrix", side_effect=fake_plan), mock.patch.object(
                matrix, "launch_plan", side_effect=fake_launch
            ), mock.patch.object(matrix, "load_state", side_effect=fake_load_state), mock.patch.object(
                matrix, "wait_ready"
            ), mock.patch.object(
                matrix,
                "dispatch",
                return_value={"instances": [{"worker": "offline", "status": "completed"}]},
            ), mock.patch.object(matrix, "collect"), mock.patch.object(matrix, "destroy_owned"):
                matrix.main()
            self.assertEqual(len(plan_calls), 2)
            self.assertEqual(plan_calls[0], 1.0)
            self.assertAlmostEqual(plan_calls[1], 0.99)


if __name__ == "__main__":
    unittest.main()
