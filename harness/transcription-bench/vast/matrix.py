#!/usr/bin/env python3
"""Safely run independent transcription benchmark workers on Vast.ai.

The all-in-one ``run`` command tears every owned instance down in ``finally``
unless ``--keep-instances`` is explicit.  Lifecycle subcommands exist for
inspection and recovery.  Destruction is restricted to instance IDs recorded
in a state file and is gated by an exact live-label match.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import contextlib
import ctypes
import ipaddress
import json
import math
import os
import re
import secrets
import signal
import shlex
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent.parent / "scripts"
BENCH_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(SCRIPT_DIR))

from benchlib import atomic_json_write, load_manifest, sha256_file, verify_fixtures  # noqa: E402
from voxtral_prewarm import (  # noqa: E402
    FAILED_SCHEMA as PREWARM_FAILED_SCHEMA,
    MAX_ONSTART_BYTES as PREWARM_SCRIPT_MAX_BYTES,
    PREWARM_ROOT,
    PREWARM_SCHEMA,
    READY_SCHEMA as PREWARM_READY_SCHEMA,
    render_onstart,
)


API_ORIGIN = "https://console.vast.ai"
API_BASE = f"{API_ORIGIN}/api/v0"
LABEL_ROOT = "gocassini-tbench"
MATRIX_SCHEMA = "gocassini.vast-transcription-matrix.v1"
PLAN_SCHEMA = "gocassini.vast-transcription-plan.v1"
STATE_SCHEMA = "gocassini.vast-transcription-state.v1"
COLLECTION_BUNDLE_SCHEMA = "gocassini.vast-worker-bundle.v1"
COLLECTION_BUNDLE_MARKER = ".collection-integrity.json"
REMOTE_STATUS_SCHEMA = "gocassini.remote-status.v1"
PROGRESS_SCHEMA = "gocassini.transcription-runner-progress.v1"
MAX_COLLECTION_JSON_BYTES = 32 * 1024 * 1024
MAX_COLLECTION_ENTRIES = 10_000
WORKER_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,39}$")
RUN_ID_RE = re.compile(r"^[0-9]{8}t[0-9]{6}z-[a-f0-9]{6}$")
RUNNERS = {"voxtral-offline", "voxtral-realtime", "parakeet-hotwords"}
RESERVED_COMMON_ARGUMENTS = frozenset({"--manifest", "--audio-dir", "--output"})
RESERVED_VOXTRAL_ARGUMENTS = frozenset(
    {
        "--model-id",
        "--revision",
        "--snapshot-dir",
        "--model-cache-dir",
        "--local-files-only",
        "--telemetry-dir",
    }
)
TRANSFER_TIMEOUT_SECONDS = 1800
SSH_GRACE_SECONDS = 180
TEARDOWN_RESERVE_SECONDS = 300
PREWARM_FINGERPRINT_RE = re.compile(r"^[a-f0-9]{64}$")
PROGRESS_POLL_SECONDS = 4
# A create request can time out after the provider accepted it.  Reconcile the
# exact, already-persisted labels for a full provider visibility window before
# declaring an ambiguous response absent.  Tests patch this bounded wait.
CREATE_VISIBILITY_GRACE_SECONDS = 300
REJECTED_CREATE_RECONCILE_SECONDS = 20
STALE_CREATE_HTTP_STATUSES = frozenset({400, 404, 410})


class LifecycleError(RuntimeError):
    pass


class VastAPIError(LifecycleError):
    """Redacted HTTP failure with enough structure for safe retry policy."""

    def __init__(self, method: str, path: str, status: int) -> None:
        super().__init__(f"Vast API {method} {path} failed with HTTP {status}")
        self.method = method
        self.path = path
        self.status = status


class CreateRejectedError(LifecycleError):
    """Provider definitively rejected a create request; no contract exists."""

    def __init__(self, message: str, *, retryable_stale: bool) -> None:
        super().__init__(message)
        self.retryable_stale = retryable_stale


class CreateOutcomeUncertain(LifecycleError):
    """Transport/response failed after create may have reached the provider."""


class StaleOfferError(LifecycleError):
    """All failed creates were definitive stale-offer rejections and cleanup completed."""


class TransportRegistry:
    """Run SSH/SCP processes that can all be stopped before lifecycle teardown.

    ``ThreadPoolExecutor`` waits for running workers on context exit.  Keeping
    every transport in one registry lets the main thread terminate those
    processes first, while the cancellation event also wakes readiness loops.
    """

    def __init__(self) -> None:
        self._cancelled = threading.Event()
        self._lock = threading.Lock()
        self._processes: set[subprocess.Popen[bytes]] = set()

    @property
    def cancelled(self) -> bool:
        return self._cancelled.is_set()

    def wait(self, seconds: float) -> None:
        if self._cancelled.wait(seconds):
            raise LifecycleError("SSH/SCP phase was cancelled for teardown")

    @staticmethod
    def _signal(process: subprocess.Popen[bytes], value: signal.Signals) -> None:
        if process.poll() is not None:
            return
        try:
            os.killpg(process.pid, value)
        except ProcessLookupError:
            pass

    def run(
        self,
        command: list[str],
        *,
        check: bool = False,
        stdout: int | None = None,
        stderr: int | None = None,
        timeout: float | None = None,
    ) -> subprocess.CompletedProcess[bytes]:
        with self._lock:
            if self.cancelled:
                raise LifecycleError("refusing to start SSH/SCP after teardown cancellation")
            process = subprocess.Popen(
                command,
                stdout=stdout,
                stderr=stderr,
                start_new_session=True,
            )
            self._processes.add(process)
        try:
            try:
                output, errors = process.communicate(timeout=timeout)
            except subprocess.TimeoutExpired as error:
                self._signal(process, signal.SIGTERM)
                try:
                    output, errors = process.communicate(timeout=5)
                except subprocess.TimeoutExpired:
                    self._signal(process, signal.SIGKILL)
                    output, errors = process.communicate()
                raise subprocess.TimeoutExpired(
                    command,
                    error.timeout,
                    output=output,
                    stderr=errors,
                ) from None
        finally:
            with self._lock:
                self._processes.discard(process)
        completed = subprocess.CompletedProcess(command, process.returncode, output, errors)
        if check and completed.returncode:
            raise subprocess.CalledProcessError(
                completed.returncode,
                command,
                output=completed.stdout,
                stderr=completed.stderr,
            )
        return completed

    def cancel(self, grace_seconds: float = 2.0) -> None:
        """Wake loops, terminate every process group, and synchronously reap it."""

        self._cancelled.set()
        with self._lock:
            processes = list(self._processes)
        for process in processes:
            self._signal(process, signal.SIGTERM)
        deadline = time.monotonic() + grace_seconds
        for process in processes:
            try:
                process.wait(timeout=max(0.0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                self._signal(process, signal.SIGKILL)
        for process in processes:
            with contextlib.suppress(subprocess.TimeoutExpired):
                process.wait(timeout=2)


def run_transport(
    registry: TransportRegistry, command: list[str], **kwargs: Any
) -> subprocess.CompletedProcess[bytes]:
    """Patchable transport boundary used by non-ASR lifecycle tests."""

    return registry.run(command, **kwargs)


def validate_worker_arguments(runner: str, arguments: object, source: str) -> list[str]:
    if not isinstance(arguments, list) or not all(isinstance(item, str) for item in arguments):
        raise LifecycleError(f"{source}: arguments must be strings")
    reserved = set(RESERVED_COMMON_ARGUMENTS)
    if runner.startswith("voxtral-"):
        reserved.update(RESERVED_VOXTRAL_ARGUMENTS)
    for argument in arguments:
        name = argument.split("=", 1)[0]
        if name in reserved:
            raise LifecycleError(f"{source}: {name} is controlled by the orchestrator")
    return arguments


class VastClient:
    """Small REST client with a global rate gate and redacted failures."""

    def __init__(self) -> None:
        self._key = os.environ.get("VAST_AI_API_KEY", "").strip()
        if not self._key:
            raise LifecycleError("VAST_AI_API_KEY is not set")
        self._rate_lock = threading.Lock()
        self._last_request = 0.0

    def request(self, method: str, path: str, payload: dict | None = None) -> dict:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        url = f"{API_ORIGIN}{path}" if path.startswith("/api/") else f"{API_BASE}{path}"
        request = urllib.request.Request(
            url,
            data=body,
            method=method,
            headers={
                "Authorization": f"Bearer {self._key}",
                "Content-Type": "application/json",
                "User-Agent": "gocassini-transcription-bench/1",
            },
        )
        for attempt in range(5):
            with self._rate_lock:
                delay = 0.55 - (time.monotonic() - self._last_request)
                if delay > 0:
                    time.sleep(delay)
                self._last_request = time.monotonic()
            try:
                with urllib.request.urlopen(request, timeout=30) as response:
                    raw = response.read()
            except urllib.error.HTTPError as error:
                # Never echo response bodies: create responses and diagnostics
                # can contain credentials owned by the remote instance.
                if error.code == 429 and attempt < 4:
                    raw = None
                else:
                    raise VastAPIError(method, path, error.code) from None
            except urllib.error.URLError as error:
                raise LifecycleError(f"Vast API {method} {path} failed: {error.reason}") from None
            if raw is not None:
                try:
                    return json.loads(raw)
                except json.JSONDecodeError:
                    raise LifecycleError(f"Vast API {method} {path} returned invalid JSON") from None
            time.sleep(2**attempt)
        raise LifecycleError(f"Vast API {method} {path} remained rate-limited")

    def search(self, search: dict[str, Any], limit: int) -> list[dict[str, Any]]:
        payload: dict[str, Any] = {
            "rentable": {"eq": True},
            "num_gpus": {"eq": 1},
            "direct_port_count": {"gte": 1},
            "gpu_name": {"in": search["gpuNames"]},
            "gpu_ram": {"gte": search["minGpuRamMb"]},
            "cpu_ram": {"gte": search["minCpuRamMb"]},
            "reliability": {"gte": search["minReliability"]},
            "dph_total": {"lte": search["maxDph"]},
            "verified": {"eq": search.get("verified", True)},
            "type": search.get("type", "ondemand"),
            "limit": limit,
        }
        response = self.request("POST", "/bundles/", payload)
        offers = response.get("offers")
        if not isinstance(offers, list):
            raise LifecycleError("Vast offer search returned no offers array")
        return offers

    def create(self, offer_id: int, body: dict[str, Any]) -> int:
        path = f"/asks/{offer_id}/"
        try:
            response = self.request("PUT", path, body)
        except VastAPIError as error:
            if error.status in STALE_CREATE_HTTP_STATUSES:
                raise CreateRejectedError(
                    f"offer {offer_id} was rejected with HTTP {error.status}",
                    retryable_stale=True,
                ) from error
            raise CreateOutcomeUncertain(
                f"create outcome for offer {offer_id} is uncertain after HTTP {error.status}"
            ) from error
        except LifecycleError as error:
            raise CreateOutcomeUncertain(
                f"create outcome for offer {offer_id} is uncertain: {error}"
            ) from error
        instance_id = response.get("new_contract")
        if (
            response.get("success") is not True
            or isinstance(instance_id, bool)
            or not isinstance(instance_id, int)
            or instance_id <= 0
        ):
            # This is a successfully decoded provider response explicitly
            # lacking a contract ID, not an ambiguous network timeout.
            raise CreateRejectedError(
                f"Vast rejected offer {offer_id} without a contract", retryable_stale=False
            )
        return instance_id

    def show(self, instance_id: int) -> dict[str, Any]:
        response = self.request("GET", f"/instances/{instance_id}/")
        instance = response.get("instances")
        if not isinstance(instance, dict):
            raise LifecycleError(f"Vast returned no instance {instance_id}")
        return instance

    def destroy(self, instance_id: int) -> None:
        response = self.request("DELETE", f"/instances/{instance_id}/")
        if response.get("success") is not True:
            raise LifecycleError(f"Vast did not confirm destruction of instance {instance_id}")

    def list_by_labels(self, labels: list[str]) -> list[dict[str, Any]]:
        if not labels or len(labels) > 16:
            raise LifecycleError("exact-label recovery requires 1..16 labels")
        query = urllib.parse.urlencode(
            {
                "limit": 25,
                "select_cols": json.dumps(["id", "label", "actual_status"]),
                "select_filters": json.dumps({"label": {"in": labels}}),
            }
        )
        response = self.request("GET", f"/api/v1/instances/?{query}")
        instances = response.get("instances")
        if response.get("success") is not True or not isinstance(instances, list):
            raise LifecycleError("Vast exact-label recovery returned no instances array")
        return instances


def read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise LifecycleError(f"cannot read {path}: {error}") from error
    if not isinstance(value, dict):
        raise LifecycleError(f"{path}: expected a JSON object")
    return value


def merged_search(common: dict[str, Any], worker: dict[str, Any]) -> dict[str, Any]:
    value = dict(common)
    value.update(worker.get("search", {}))
    required = {
        "gpuNames": list,
        "minGpuRamMb": int,
        "minCpuRamMb": int,
        "minReliability": (int, float),
        "maxDph": (int, float),
    }
    for key, kind in required.items():
        if not isinstance(value.get(key), kind):
            raise LifecycleError(f"search.{key} has the wrong type")
    if not value["gpuNames"] or not all(isinstance(item, str) for item in value["gpuNames"]):
        raise LifecycleError("search.gpuNames must be a non-empty string list")
    if value["minGpuRamMb"] < 8000 or value["minCpuRamMb"] < 8000:
        raise LifecycleError("workers require at least 8 GB GPU RAM and 8 GB CPU RAM")
    if not 0.5 <= value["minReliability"] <= 1:
        raise LifecycleError("search.minReliability must be in 0.5..1")
    if not 0 < value["maxDph"] <= 2:
        raise LifecycleError("search.maxDph must be in (0, 2]")
    return value


def validate_matrix(path: Path) -> dict[str, Any]:
    matrix = read_json(path)
    if matrix.get("schema") != MATRIX_SCHEMA:
        raise LifecycleError(f"{path}: unsupported matrix schema")
    if not isinstance(matrix.get("image"), str) or not matrix["image"].strip():
        raise LifecycleError("matrix.image is required")
    if not isinstance(matrix.get("diskGb"), int) or not 20 <= matrix["diskGb"] <= 200:
        raise LifecycleError("matrix.diskGb must be in 20..200")
    runtime = matrix.get("maxRuntimeMinutes")
    if not isinstance(runtime, int) or not 10 <= runtime <= 240:
        raise LifecycleError("matrix.maxRuntimeMinutes must be in 10..240")
    max_cost = matrix.get("maxTotalCostUsd")
    if isinstance(max_cost, bool) or not isinstance(max_cost, (int, float)) or not 0 < max_cost <= 20:
        raise LifecycleError("matrix.maxTotalCostUsd must be in (0, 20]")
    workers = matrix.get("workers")
    if not isinstance(workers, list) or not workers or len(workers) > 16:
        raise LifecycleError("matrix.workers must contain 1..16 workers")
    seen = set()
    for worker in workers:
        name = worker.get("name")
        if not isinstance(name, str) or not WORKER_RE.fullmatch(name) or name in seen:
            raise LifecycleError(f"invalid or duplicate worker name {name!r}")
        seen.add(name)
        if worker.get("runner") not in RUNNERS:
            raise LifecycleError(f"worker {name}: unknown runner {worker.get('runner')!r}")
        arguments = worker.get("arguments", [])
        validate_worker_arguments(worker["runner"], arguments, f"worker {name}")
        merged_search(matrix.get("search", {}), worker)
    return matrix


def new_run_id() -> str:
    return time.strftime("%Y%m%dt%H%M%Sz-", time.gmtime()) + secrets.token_hex(3)


def safe_offer(offer: dict[str, Any]) -> dict[str, Any]:
    try:
        value = {
            "id": int(offer["id"]),
            "machineId": int(offer.get("machine_id", 0)),
            "gpuName": str(offer["gpu_name"]),
            "gpuRamMb": int(offer["gpu_ram"]),
            "cpuRamMb": int(offer["cpu_ram"]),
            "dphTotal": float(offer["dph_total"]),
            "reliability": float(offer.get("reliability", offer.get("reliability2", 0))),
        }
        if value["id"] <= 0 or value["machineId"] <= 0 or not 0 < value["dphTotal"] <= 2:
            raise ValueError("offer and machine IDs must be positive")
        return value
    except (KeyError, TypeError, ValueError) as error:
        raise LifecycleError(f"malformed Vast offer: {error}") from error


def plan_matrix(
    client: VastClient,
    matrix_path: Path,
    plan_path: Path,
    jobs: int,
    max_total_cost_usd: float | None = None,
) -> dict[str, Any]:
    matrix = validate_matrix(matrix_path)
    effective_cap = float(matrix["maxTotalCostUsd"])
    if max_total_cost_usd is not None:
        if not 0 < max_total_cost_usd <= effective_cap:
            raise LifecycleError("remaining all-in-one run budget is exhausted")
        effective_cap = max_total_cost_usd
    workers = matrix["workers"]

    def find(worker: dict[str, Any]) -> tuple[str, list[dict[str, Any]]]:
        search = merged_search(matrix["search"], worker)
        offers = [safe_offer(item) for item in client.search(search, max(12, len(workers) * 4))]
        offers.sort(key=lambda item: (item["dphTotal"], -item["reliability"]))
        return worker["name"], offers

    offers_by_worker: dict[str, list[dict[str, Any]]] = {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
        futures = [pool.submit(find, worker) for worker in workers]
        for future in concurrent.futures.as_completed(futures):
            name, offers = future.result()
            offers_by_worker[name] = offers

    run_id = new_run_id()
    label_prefix = f"{LABEL_ROOT}-{run_id}"
    used_offers: set[int] = set()
    used_machines: set[int] = set()
    planned_workers = []
    for worker in workers:
        choices = [
            offer for offer in offers_by_worker[worker["name"]]
            if offer["id"] not in used_offers and offer["machineId"] not in used_machines
        ]
        if not choices:
            raise LifecycleError(f"no distinct offer available for worker {worker['name']}")
        offer = choices[0]
        used_offers.add(offer["id"])
        used_machines.add(offer["machineId"])
        prewarm = render_worker_prewarm(worker["runner"], worker["name"])
        planned_workers.append(
            {
                "name": worker["name"],
                "runner": worker["runner"],
                "arguments": worker.get("arguments", []),
                "label": f"{label_prefix}-{worker['name']}",
                "offer": offer,
            } | ({"prewarm": prewarm.metadata} if prewarm is not None else {})
        )
    plan = {
        "schema": PLAN_SCHEMA,
        "runId": run_id,
        "labelPrefix": label_prefix,
        "createdAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "matrixPath": str(matrix_path.resolve()),
        "image": matrix["image"],
        "diskGb": matrix["diskGb"],
        "maxRuntimeMinutes": matrix["maxRuntimeMinutes"],
        "maxTotalCostUsd": effective_cap,
        "projectedDph": sum(worker["offer"]["dphTotal"] for worker in planned_workers),
        "workers": planned_workers,
    }
    plan["projectedBenchmarkCost"] = (
        plan["projectedDph"] * plan["maxRuntimeMinutes"] / 60
    )
    plan["projectedMinimumLifecycleCost"] = plan["projectedDph"] * (
        plan["maxRuntimeMinutes"] * 60 + TEARDOWN_RESERVE_SECONDS
    ) / 3600
    if plan["projectedMinimumLifecycleCost"] > plan["maxTotalCostUsd"]:
        raise LifecycleError(
            f"benchmark plus teardown reserve ${plan['projectedMinimumLifecycleCost']:.3f} "
            "exceeds the remaining lifecycle cap "
            f"${plan['maxTotalCostUsd']:.3f}"
        )
    atomic_json_write(plan_path, plan)
    print(
        f"planned {len(planned_workers)} independent workers as {run_id}; "
        f"projected ${plan['projectedDph']:.3f}/hour"
    )
    return plan


def validate_plan(plan_path: Path) -> dict[str, Any]:
    plan = read_json(plan_path)
    if plan.get("schema") != PLAN_SCHEMA or not RUN_ID_RE.fullmatch(plan.get("runId", "")):
        raise LifecycleError(f"{plan_path}: invalid plan")
    expected_prefix = f"{LABEL_ROOT}-{plan['runId']}"
    if plan.get("labelPrefix") != expected_prefix:
        raise LifecycleError(f"{plan_path}: unsafe label prefix")
    if not isinstance(plan.get("image"), str) or not plan["image"].strip() or len(plan["image"]) > 300:
        raise LifecycleError(f"{plan_path}: invalid image")
    if (
        isinstance(plan.get("diskGb"), bool)
        or not isinstance(plan.get("diskGb"), int)
        or not 20 <= plan["diskGb"] <= 200
    ):
        raise LifecycleError(f"{plan_path}: invalid disk size")
    workers = plan.get("workers")
    if not isinstance(workers, list) or not workers or len(workers) > 16:
        raise LifecycleError(f"{plan_path}: plan must have 1..16 workers")
    seen_workers: set[str] = set()
    seen_labels: set[str] = set()
    seen_offers: set[int] = set()
    seen_machines: set[int] = set()
    planned_dph = 0.0
    for worker in workers:
        name = worker.get("name")
        label = worker.get("label")
        offer = worker.get("offer")
        offer_id = offer.get("id") if isinstance(offer, dict) else None
        machine_id = offer.get("machineId") if isinstance(offer, dict) else None
        dph = offer.get("dphTotal") if isinstance(offer, dict) else None
        if not isinstance(name, str) or not WORKER_RE.fullmatch(name) or name in seen_workers:
            raise LifecycleError(f"{plan_path}: invalid or duplicate plan worker")
        if label != f"{expected_prefix}-{name}" or label in seen_labels:
            raise LifecycleError(f"{plan_path}: worker label is outside run scope")
        if isinstance(offer_id, bool) or not isinstance(offer_id, int) or offer_id <= 0 or offer_id in seen_offers:
            raise LifecycleError(f"{plan_path}: invalid or duplicate offer ID")
        if (
            isinstance(machine_id, bool)
            or not isinstance(machine_id, int)
            or machine_id <= 0
            or machine_id in seen_machines
        ):
            raise LifecycleError(f"{plan_path}: invalid or duplicate physical machine")
        if isinstance(dph, bool) or not isinstance(dph, (int, float)) or not 0 < dph <= 2:
            raise LifecycleError(f"{plan_path}: invalid offer price")
        if worker.get("runner") not in RUNNERS:
            raise LifecycleError(f"{plan_path}: invalid runner")
        if worker["runner"].startswith("voxtral-"):
            if "prewarm" not in worker:
                raise LifecycleError(f"{plan_path}: Voxtral worker is missing its pinned prewarm contract")
            validate_prewarm_metadata(worker["prewarm"], worker["runner"])
        elif "prewarm" in worker:
            raise LifecycleError(f"{plan_path}: non-Voxtral worker has prewarm metadata")
        arguments = worker.get("arguments")
        validate_worker_arguments(worker["runner"], arguments, str(plan_path))
        seen_workers.add(name)
        seen_labels.add(label)
        seen_offers.add(offer_id)
        seen_machines.add(machine_id)
        planned_dph += dph
    runtime = plan.get("maxRuntimeMinutes")
    if isinstance(runtime, bool) or not isinstance(runtime, int) or not 10 <= runtime <= 240:
        raise LifecycleError(f"{plan_path}: invalid runtime")
    max_cost = plan.get("maxTotalCostUsd")
    projected_dph = plan.get("projectedDph")
    benchmark_cost = plan.get("projectedBenchmarkCost")
    lifecycle_cost = plan.get("projectedMinimumLifecycleCost")
    if (
        isinstance(max_cost, bool)
        or not isinstance(max_cost, (int, float))
        or not 0 < max_cost <= 20
        or isinstance(projected_dph, bool)
        or not isinstance(projected_dph, (int, float))
        or projected_dph <= 0
        or abs(projected_dph - planned_dph) > 0.000_001
        or isinstance(benchmark_cost, bool)
        or not isinstance(benchmark_cost, (int, float))
        or isinstance(lifecycle_cost, bool)
        or not isinstance(lifecycle_cost, (int, float))
        or lifecycle_cost > max_cost
    ):
        raise LifecycleError(f"{plan_path}: invalid or over-budget plan")
    expected_benchmark = planned_dph * runtime / 60
    expected_lifecycle = planned_dph * (runtime * 60 + TEARDOWN_RESERVE_SECONDS) / 3600
    if (
        abs(benchmark_cost - expected_benchmark) > 0.000_001
        or abs(lifecycle_cost - expected_lifecycle) > 0.000_001
    ):
        raise LifecycleError(f"{plan_path}: projected cost does not match selected offers")
    return plan


def load_state(state_path: Path) -> dict[str, Any]:
    state = read_json(state_path)
    if state.get("schema") != STATE_SCHEMA or not RUN_ID_RE.fullmatch(state.get("runId", "")):
        raise LifecycleError(f"{state_path}: invalid state")
    if state.get("labelPrefix") != f"{LABEL_ROOT}-{state['runId']}":
        raise LifecycleError(f"{state_path}: unsafe state label prefix")
    runtime = state.get("maxRuntimeMinutes")
    if isinstance(runtime, bool) or not isinstance(runtime, int) or not 10 <= runtime <= 240:
        raise LifecycleError(f"{state_path}: invalid maxRuntimeMinutes")
    max_cost = state.get("maxTotalCostUsd")
    if isinstance(max_cost, bool) or not isinstance(max_cost, (int, float)) or not 0 < max_cost <= 20:
        raise LifecycleError(f"{state_path}: invalid maxTotalCostUsd")
    budget_fields = (
        "billingStartedAtEpoch",
        "budgetDph",
        "lifecycleDeadlineEpoch",
        "teardownReserveSeconds",
    )
    present_budget_fields = [name for name in budget_fields if name in state]
    if present_budget_fields and len(present_budget_fields) != len(budget_fields):
        raise LifecycleError(f"{state_path}: incomplete lifecycle budget fields")
    if present_budget_fields:
        started = state["billingStartedAtEpoch"]
        budget_dph = state["budgetDph"]
        deadline = state["lifecycleDeadlineEpoch"]
        reserve = state["teardownReserveSeconds"]
        if (
            isinstance(started, bool)
            or not isinstance(started, (int, float))
            or started <= 0
            or isinstance(budget_dph, bool)
            or not isinstance(budget_dph, (int, float))
            or not 0 < budget_dph <= 32
            or isinstance(deadline, bool)
            or not isinstance(deadline, (int, float))
            or deadline <= started
            or isinstance(reserve, bool)
            or not isinstance(reserve, int)
            or reserve != TEARDOWN_RESERVE_SECONDS
            or abs(deadline - (started + max_cost / budget_dph * 3600)) > 0.01
        ):
            raise LifecycleError(f"{state_path}: invalid lifecycle budget")
    if "billingEndedAtEpoch" in state and (
        not present_budget_fields
        or isinstance(state["billingEndedAtEpoch"], bool)
        or not isinstance(state["billingEndedAtEpoch"], (int, float))
        or state["billingEndedAtEpoch"] < state["billingStartedAtEpoch"]
    ):
        raise LifecycleError(f"{state_path}: invalid billing end timestamp")
    instances = state.get("instances")
    if not isinstance(instances, list) or not instances or len(instances) > 16:
        raise LifecycleError(f"{state_path}: state must have 1..16 instances")
    seen_workers: set[str] = set()
    seen_labels: set[str] = set()
    seen_ids: set[int] = set()
    for item in instances:
        if not isinstance(item, dict):
            raise LifecycleError(f"{state_path}: instance entry must be an object")
        worker = item.get("worker")
        label = item.get("label")
        if not isinstance(worker, str) or not WORKER_RE.fullmatch(worker) or worker in seen_workers:
            raise LifecycleError(f"{state_path}: invalid or duplicate worker")
        if label != f"{state['labelPrefix']}-{worker}" or label in seen_labels:
            raise LifecycleError(f"{state_path}: instance label is outside exact run scope")
        if item.get("runner") not in RUNNERS:
            raise LifecycleError(f"{state_path}: invalid runner for {worker}")
        arguments = item.get("arguments")
        validate_worker_arguments(item["runner"], arguments, str(state_path))
        instance_id = item.get("instanceId")
        if instance_id is not None:
            if (
                isinstance(instance_id, bool)
                or not isinstance(instance_id, int)
                or instance_id <= 0
                or instance_id in seen_ids
            ):
                raise LifecycleError(f"{state_path}: invalid or duplicate instance ID")
            seen_ids.add(instance_id)
        if "sshHost" in item and (
            not isinstance(item["sshHost"], str)
            or not item["sshHost"]
            or len(item["sshHost"]) > 255
            or any(character.isspace() for character in item["sshHost"])
        ):
            raise LifecycleError(f"{state_path}: invalid SSH host for {worker}")
        if "sshPort" in item and (
            isinstance(item["sshPort"], bool)
            or not isinstance(item["sshPort"], int)
            or not 1 <= item["sshPort"] <= 65535
        ):
            raise LifecycleError(f"{state_path}: invalid SSH port for {worker}")
        if ("sshHost" in item) != ("sshPort" in item):
            raise LifecycleError(f"{state_path}: SSH host/port must be paired for {worker}")
        if "sshEndpointKind" in item and item["sshEndpointKind"] not in {"direct", "proxy"}:
            raise LifecycleError(f"{state_path}: invalid SSH endpoint kind for {worker}")
        if "actualDph" in item and (
            isinstance(item["actualDph"], bool)
            or not isinstance(item["actualDph"], (int, float))
            or item["actualDph"] <= 0
        ):
            raise LifecycleError(f"{state_path}: invalid live price for {worker}")
        if "providerPresent" in item and not isinstance(item["providerPresent"], bool):
            raise LifecycleError(f"{state_path}: invalid providerPresent for {worker}")
        if "prewarm" in item:
            if not item["runner"].startswith("voxtral-"):
                raise LifecycleError(f"{state_path}: non-Voxtral worker has prewarm metadata")
            validate_prewarm_metadata(item["prewarm"], item["runner"])
        timing_fields = (
            "createRequestStartedAtEpoch",
            "createResponseAtEpoch",
            "contractCreatedAtEpoch",
            "providerRunningObservedAtEpoch",
            "sshReadyAtEpoch",
            "bootstrapReadyObservedAtEpoch",
            "uploadStartedAtEpoch",
            "uploadEndedAtEpoch",
            "remotePreparationStartedAtEpoch",
            "remotePreparationEndedAtEpoch",
            "runnerStartedAtEpoch",
            "runnerEndedAtEpoch",
            "firstResultReportedAtEpoch",
            "firstResultCollectedAtEpoch",
            "earlyCollectionStartedAtEpoch",
            "earlyCollectedAtEpoch",
            "collectionStartedAtEpoch",
            "collectedAtEpoch",
            "destroyConfirmedAtEpoch",
        )
        for field in timing_fields:
            stamp = item.get(field)
            if field in item and (
                isinstance(stamp, bool) or not isinstance(stamp, (int, float)) or stamp <= 0
            ):
                raise LifecycleError(f"{state_path}: invalid {field} for {worker}")
        duration = item.get("runnerTimeToFirstResultSeconds")
        if "runnerTimeToFirstResultSeconds" in item and (
            isinstance(duration, bool) or not isinstance(duration, (int, float)) or duration < 0
        ):
            raise LifecycleError(
                f"{state_path}: invalid runnerTimeToFirstResultSeconds for {worker}"
            )
        fallbacks = item.get("sshFallbacks", [])
        if not isinstance(fallbacks, list) or len(fallbacks) > 2:
            raise LifecycleError(f"{state_path}: invalid SSH fallbacks for {worker}")
        for endpoint in fallbacks:
            if (
                not isinstance(endpoint, dict)
                or not isinstance(endpoint.get("host"), str)
                or not endpoint["host"]
                or any(character.isspace() for character in endpoint["host"])
                or isinstance(endpoint.get("port"), bool)
                or not isinstance(endpoint.get("port"), int)
                or not 1 <= endpoint["port"] <= 65535
                or endpoint.get("kind") not in {"direct", "proxy"}
            ):
                raise LifecycleError(f"{state_path}: invalid SSH fallback for {worker}")
        seen_workers.add(worker)
        seen_labels.add(label)
    return state


def lifecycle_seconds_remaining(
    state: dict[str, Any], *, now: float | None = None, include_teardown_reserve: bool = True
) -> float:
    """Conservative wall-clock budget left before the aggregate cost cap.

    The hourly rate is the sum for all workers and the clock starts immediately
    before the first create.  This intentionally over-counts staggered starts
    and individually destroyed workers.  Ordinary phases cannot consume the
    final teardown reserve.
    """

    required = {
        "billingStartedAtEpoch",
        "budgetDph",
        "lifecycleDeadlineEpoch",
        "teardownReserveSeconds",
    }
    if not required.issubset(state):
        raise LifecycleError("state predates lifecycle budget enforcement; make a fresh plan")
    current = time.time() if now is None else now
    reserve = state["teardownReserveSeconds"] if include_teardown_reserve else 0
    return float(state["lifecycleDeadlineEpoch"]) - current - reserve


def bounded_phase_timeout(
    state: dict[str, Any], requested_seconds: int, phase: str, *, now: float | None = None
) -> int:
    if requested_seconds <= 0:
        raise LifecycleError(f"{phase}: timeout must be positive")
    remaining = lifecycle_seconds_remaining(state, now=now)
    if remaining < 1:
        raise LifecycleError(
            f"{phase}: maxTotalCostUsd lifecycle budget is exhausted; tearing down"
        )
    return max(1, min(requested_seconds, int(remaining)))


def estimated_lifecycle_cost(state: dict[str, Any], *, now: float | None = None) -> float:
    if "billingStartedAtEpoch" not in state or "budgetDph" not in state:
        return 0.0
    ended = state.get("billingEndedAtEpoch")
    current = float(ended) if isinstance(ended, (int, float)) else (
        time.time() if now is None else now
    )
    seconds = max(0.0, current - float(state["billingStartedAtEpoch"]))
    return seconds * float(state["budgetDph"]) / 3600


def recover_owned(
    client: VastClient, state_path: Path, wait_seconds: int = 0
) -> tuple[dict[str, Any], list[int]]:
    """Recover create responses lost after the expected labels were persisted.

    Only exact labels already validated as members of this run are queried.
    This closes the normal create-response persistence window and also gives a
    crash-recovery path without any broad account deletion.
    """

    state = load_state(state_path)
    by_label = {item["label"]: item for item in state["instances"]}
    recovered: list[int] = []
    deadline = time.monotonic() + wait_seconds
    while True:
        live_instances = client.list_by_labels(list(by_label))
        seen_labels: set[str] = set()
        for live in live_instances:
            label = live.get("label")
            instance_id = live.get("id")
            if label not in by_label:
                raise LifecycleError("Vast exact-label query returned an out-of-scope label")
            if label in seen_labels:
                raise LifecycleError(f"multiple live instances use exact owned label {label}")
            if isinstance(instance_id, bool) or not isinstance(instance_id, int) or instance_id <= 0:
                raise LifecycleError(f"Vast returned an invalid instance ID for {label}")
            seen_labels.add(label)
            item = by_label[label]
            stored_id = item.get("instanceId")
            if stored_id is not None and stored_id != instance_id:
                raise LifecycleError(f"owned label {label} maps to a different instance ID")
            if stored_id is None:
                item.update(instanceId=instance_id, status="recovered")
                recovered.append(instance_id)
        if all(item.get("instanceId") is not None for item in state["instances"]):
            break
        if time.monotonic() >= deadline:
            break
        time.sleep(2)
    for label, item in by_label.items():
        item["providerPresent"] = label in seen_labels
    atomic_json_write(state_path, state)
    if recovered:
        print(f"recovered {len(recovered)} exact-label instance IDs")
    return state, recovered


def launch_plan(client: VastClient, plan_path: Path, state_path: Path, jobs: int) -> dict[str, Any]:
    plan = validate_plan(plan_path)
    if state_path.exists():
        raise LifecycleError(
            f"refusing to overwrite existing state {state_path}; use recover/destroy or a new path"
        )
    prewarm_specs: dict[str, Any] = {}
    for worker in plan["workers"]:
        spec = render_worker_prewarm(worker["runner"], worker["name"])
        if spec is not None:
            planned_prewarm = worker["prewarm"]
            if planned_prewarm != spec.metadata:
                raise LifecycleError(
                    f"worker {worker['name']}: prewarm contract changed since planning; re-plan"
                )
            prewarm_specs[worker["name"]] = spec
    billing_started = time.time()
    state = {
        "schema": STATE_SCHEMA,
        "runId": plan["runId"],
        "labelPrefix": plan["labelPrefix"],
        "planPath": str(plan_path.resolve()),
        "maxRuntimeMinutes": plan["maxRuntimeMinutes"],
        "maxTotalCostUsd": plan["maxTotalCostUsd"],
        "billingStartedAtEpoch": billing_started,
        "budgetDph": plan["projectedDph"],
        "lifecycleDeadlineEpoch": (
            billing_started + plan["maxTotalCostUsd"] / plan["projectedDph"] * 3600
        ),
        "teardownReserveSeconds": TEARDOWN_RESERVE_SECONDS,
        "instances": [
            ({
                "worker": worker["name"],
                "runner": worker["runner"],
                "arguments": worker["arguments"],
                "label": worker["label"],
                "offerId": worker["offer"]["id"],
                "instanceId": None,
                "status": "launching",
            } | (
                {"prewarm": prewarm_specs[worker["name"]].metadata}
                if worker["name"] in prewarm_specs else {}
            ))
            for worker in plan["workers"]
        ],
    }
    atomic_json_write(state_path, state)
    state, preexisting = recover_owned(client, state_path)
    if preexisting:
        raise LifecycleError(
            "refusing launch because exact run labels already exist; destroy or use the state file"
        )

    launch_state_lock = threading.Lock()

    def launch_one(index: int) -> tuple[int, int, float]:
        item = state["instances"][index]
        body = {
            "image": plan["image"],
            "disk": plan["diskGb"],
            "runtype": "ssh_direct",
            "label": item["label"],
            "cancel_unavail": True,
        }
        spec = prewarm_specs.get(item["worker"])
        if spec is not None:
            if spec.fingerprint != item["prewarm"]["fingerprint"]:
                raise LifecycleError(f"worker {item['worker']}: prewarm fingerprint drifted")
            body["onstart"] = spec.script
        with launch_state_lock:
            item["createRequestStartedAtEpoch"] = time.time()
            atomic_json_write(state_path, state)
        instance_id = client.create(item["offerId"], body)
        return index, instance_id, time.time()

    failures: list[tuple[int, Exception]] = []
    try:
        with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
            futures = {
                pool.submit(launch_one, index): index for index in range(len(state["instances"]))
            }
            for future in concurrent.futures.as_completed(futures):
                index = futures[future]
                try:
                    _, instance_id, response_at = future.result()
                    with launch_state_lock:
                        state["instances"][index].update(
                            instanceId=instance_id,
                            status="created",
                            contractCreatedAtEpoch=response_at,
                        )
                        atomic_json_write(state_path, state)
                    print(f"launched {state['instances'][index]['worker']} as instance {instance_id}")
                except Exception as error:  # preserve other successful IDs for cleanup
                    kind = (
                        "rejected" if isinstance(error, CreateRejectedError) else "uncertain"
                    )
                    with launch_state_lock:
                        state["instances"][index].update(
                            status=f"create-{kind}",
                            launchErrorKind=kind,
                            createResponseAtEpoch=time.time(),
                        )
                        atomic_json_write(state_path, state)
                    failures.append((index, error))
    except BaseException:
        # Keyboard interrupts are recoverable too: expected labels were durable
        # before any create request, so reconcile and tear down exact matches.
        state = load_state(state_path)
        for item in state["instances"]:
            if item.get("instanceId") is None and item.get("status") == "launching":
                item.update(status="create-uncertain", launchErrorKind="interrupted")
        atomic_json_write(state_path, state)
        try:
            state, _ = recover_owned(
                client, state_path, wait_seconds=CREATE_VISIBILITY_GRACE_SECONDS
            )
            for item in state["instances"]:
                if item.get("instanceId") is None and item.get("status") == "create-uncertain":
                    item.update(
                        status="create-absence-confirmed",
                        absenceConfirmedAtEpoch=time.time(),
                        reconciliationSeconds=CREATE_VISIBILITY_GRACE_SECONDS,
                    )
            atomic_json_write(state_path, state)
            destroy_owned(client, state_path, jobs)
        except Exception as cleanup_error:
            raise LifecycleError(
                "launch interrupted and exact-label cleanup was incomplete; retain state "
                f"{state_path} and run recover/destroy: {cleanup_error}"
            ) from cleanup_error
        raise
    # Reconcile exact labels even if the process missed a completed future's
    # response between provider creation and local state persistence.
    reconcile_seconds = (
        CREATE_VISIBILITY_GRACE_SECONDS
        if any(not isinstance(error, CreateRejectedError) for _, error in failures)
        else REJECTED_CREATE_RECONCILE_SECONDS
    )
    try:
        state, _ = recover_owned(client, state_path, wait_seconds=reconcile_seconds)
    except Exception as reconciliation_error:
        labels = ", ".join(item["label"] for item in load_state(state_path)["instances"])
        try:
            # A transient list failure must not bypass default teardown. This
            # makes one full, exact-scope cleanup attempt; destroy_owned itself
            # performs the bounded ambiguity reconciliation when necessary.
            destroy_owned(client, state_path, jobs)
        except Exception as cleanup_error:
            raise LifecycleError(
                "create reconciliation did not complete and cleanup is NOT confirmed; automatic "
                f"retry is forbidden. Exact pending labels: {labels}. Retain {state_path}; run "
                f"`matrix.py recover --state {state_path}` followed by `matrix.py destroy --state "
                f"{state_path}`. Reconciliation: {reconciliation_error}; cleanup: {cleanup_error}"
            ) from reconciliation_error
        raise LifecycleError(
            "create reconciliation failed, but the fallback exact-label teardown completed; "
            f"automatic retry is refused. Auditable state: {state_path}"
        ) from reconciliation_error
    if failures:
        failed_indexes = {index for index, _ in failures}
        for index in failed_indexes:
            item = state["instances"][index]
            if item.get("instanceId") is None and item.get("status") == "create-uncertain":
                item.update(
                    status="create-absence-confirmed",
                    absenceConfirmedAtEpoch=time.time(),
                    reconciliationSeconds=reconcile_seconds,
                )
        atomic_json_write(state_path, state)
    if failures:
        try:
            destroy_owned(client, state_path, jobs)
        except Exception as cleanup_error:
            raise LifecycleError(
                "launch failed and cleanup was incomplete; automatic retry is forbidden. "
                f"Retain {state_path} and run recover/destroy: {cleanup_error}"
            ) from failures[0][1]
        if all(
            isinstance(error, CreateRejectedError) and error.retryable_stale
            for _, error in failures
        ):
            raise StaleOfferError(
                "one or more planned offers became stale; exact-label reconciliation and cleanup "
                "completed, so the all-in-one run may safely re-plan"
            ) from failures[0][1]
        raise CreateOutcomeUncertain(
            "a create outcome was ambiguous or non-retryable; exact-label reconciliation and "
            f"cleanup completed, but automatic re-plan is refused. State: {state_path}"
        ) from failures[0][1]
    state["launchCompletedAtEpoch"] = time.time()
    atomic_json_write(state_path, state)
    return state


def ssh_endpoint_candidates(live: dict[str, Any]) -> list[dict[str, Any]]:
    """Prefer Vast's direct public 22/tcp mapping, then retain its SSH proxy."""

    candidates: list[dict[str, Any]] = []
    public_ip = live.get("public_ipaddr")
    ports = live.get("ports")
    try:
        parsed_ip = ipaddress.ip_address(public_ip) if isinstance(public_ip, str) else None
    except ValueError:
        parsed_ip = None
    if parsed_ip is not None and parsed_ip.version == 4 and not parsed_ip.is_unspecified:
        mappings = ports.get("22/tcp", []) if isinstance(ports, dict) else []
        if isinstance(mappings, list):
            for mapping in mappings:
                if not isinstance(mapping, dict):
                    continue
                try:
                    port = int(mapping.get("HostPort"))
                except (TypeError, ValueError):
                    continue
                if 1 <= port <= 65535:
                    candidates.append({"host": public_ip, "port": port, "kind": "direct"})
                    break
    proxy_host = live.get("ssh_host")
    proxy_port = live.get("ssh_port")
    try:
        proxy_port = int(proxy_port)
    except (TypeError, ValueError):
        proxy_port = 0
    if (
        isinstance(proxy_host, str)
        and proxy_host
        and not any(character.isspace() for character in proxy_host)
        and 1 <= proxy_port <= 65535
    ):
        proxy = {"host": proxy_host, "port": proxy_port, "kind": "proxy"}
        if not any((item["host"], item["port"]) == (proxy_host, proxy_port) for item in candidates):
            candidates.append(proxy)
    return candidates


def _safe_prewarm_path(value: object) -> bool:
    if not isinstance(value, str) or not value.startswith(PREWARM_ROOT + "/"):
        return False
    path = Path(value)
    return path.is_absolute() and ".." not in path.parts and not any(
        character.isspace() for character in value
    )


def validate_prewarm_metadata(value: object, runner: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise LifecycleError("Voxtral worker is missing prewarm metadata")
    role = "offline" if runner == "voxtral-offline" else "realtime"
    if value.get("schema") != PREWARM_SCHEMA or value.get("role") != role:
        raise LifecycleError(f"invalid {role} prewarm metadata")
    if not PREWARM_FINGERPRINT_RE.fullmatch(str(value.get("fingerprint", ""))):
        raise LifecycleError(f"invalid {role} prewarm fingerprint")
    for key in ("readyPath", "failedPath", "pythonPath", "snapshotPath", "hfHome"):
        if not _safe_prewarm_path(value.get(key)):
            raise LifecycleError(f"invalid {role} prewarm {key}")
    if value["readyPath"] == value["failedPath"]:
        raise LifecycleError(f"invalid {role} prewarm sentinel paths")
    return value


def render_worker_prewarm(runner: str, worker: str) -> Any | None:
    if not runner.startswith("voxtral-"):
        return None
    role = "offline" if runner == "voxtral-offline" else "realtime"
    spec = render_onstart(role)
    if len(spec.script.encode("utf-8")) >= PREWARM_SCRIPT_MAX_BYTES:
        raise LifecycleError(
            f"worker {worker}: rendered onstart exceeds the conservative limit"
        )
    validate_prewarm_metadata(spec.metadata, runner)
    return spec


def prewarm_probe_script(prewarm: dict[str, Any]) -> str:
    ready = shlex.quote(prewarm["readyPath"])
    failed = shlex.quote(prewarm["failedPath"])
    return (
        f"if [ -f {failed} ]; then printf '%s\\n' failed; cat -- {failed}; exit 20; fi; "
        f"if [ -f {ready} ]; then printf '%s\\n' ready; cat -- {ready}; exit 0; fi; "
        "printf '%s\\n' pending; exit 3"
    )


def parse_prewarm_probe(output: bytes, expected: dict[str, Any]) -> dict[str, Any]:
    if len(output) > 64 * 1024:
        raise LifecycleError("prewarm sentinel exceeds 64 KiB")
    marker, separator, raw = output.partition(b"\n")
    if marker != b"ready" or not separator:
        raise LifecycleError("prewarm readiness probe returned an invalid marker")
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise LifecycleError("prewarm ready sentinel is not valid JSON") from error
    if not isinstance(value, dict):
        raise LifecycleError("prewarm ready sentinel must be an object")
    role = expected["role"]
    spec = render_onstart(role)
    model = value.get("model")
    runtime = value.get("runtime")
    started = value.get("startedAtEpoch")
    finished = value.get("finishedAtEpoch")
    duration = value.get("durationSeconds")
    numbers_are_valid = all(
        isinstance(number, (int, float))
        and not isinstance(number, bool)
        and math.isfinite(float(number))
        for number in (started, finished, duration)
    )
    if (
        value.get("schema") != PREWARM_READY_SCHEMA
        or value.get("status") != "ready"
        or value.get("role") != role
        or value.get("fingerprint") != expected["fingerprint"]
        or not isinstance(value.get("cacheHit"), bool)
        or value.get("venvPython") != expected.get("pythonPath", spec.python_path)
        or value.get("snapshotPath") != expected.get("snapshotPath", spec.snapshot_path)
        or value.get("logPath") != spec.log_path
        or not isinstance(model, dict)
        or model.get("id") != spec.model_id
        or model.get("revision") != spec.model_revision
        or model.get("files") != list(spec.model_files)
        or not isinstance(runtime, dict)
        or runtime.get("bf16") is not True
        or not all(
            isinstance(runtime.get(key), str) and runtime[key]
            for key in ("python", "torch", "cuda", "device", "torchPath")
        )
        or runtime.get("packages") != dict(spec.package_versions)
        or not numbers_are_valid
        or float(started) <= 0
        or float(finished) < float(started)
        or float(duration) < 0
        or abs(float(duration) - (float(finished) - float(started))) > 0.01
        or isinstance(value.get("snapshotBytes"), bool)
        or not isinstance(value.get("snapshotBytes"), int)
        or value["snapshotBytes"] <= 0
        or isinstance(value.get("freeBytes"), bool)
        or not isinstance(value.get("freeBytes"), int)
        or value["freeBytes"] < 0
    ):
        raise LifecycleError("prewarm ready sentinel does not match the launched worker")
    return value


def wait_ready(
    client: VastClient,
    state_path: Path,
    timeout_seconds: int,
    jobs: int,
    identity: Path | None = None,
) -> dict[str, Any]:
    state = load_state(state_path)
    transports = TransportRegistry()
    ready_timeout = bounded_phase_timeout(state, timeout_seconds, "readiness")
    deadline = time.monotonic() + ready_timeout
    state_dir = state_path.parent

    def wait_one(
        index: int,
    ) -> tuple[
        int,
        dict[str, Any],
        dict[str, Any],
        list[dict[str, Any]],
        dict[str, float],
        dict[str, Any] | None,
    ]:
        item = state["instances"][index]
        known_hosts = state_dir / f"known-hosts-{state['runId']}-{item['worker']}"
        instance_id = item.get("instanceId")
        if not isinstance(instance_id, int):
            raise LifecycleError(f"worker {item['worker']} has no instance ID")
        provider_running_at: float | None = None
        while time.monotonic() < deadline:
            live = client.show(instance_id)
            if live.get("label") != item["label"]:
                raise LifecycleError(f"instance {instance_id} label does not match owned worker")
            if live.get("actual_status") == "running":
                if provider_running_at is None:
                    provider_running_at = time.time()
                candidates = ssh_endpoint_candidates(live)
                for candidate in candidates:
                    endpoint = {"sshHost": candidate["host"], "sshPort": candidate["port"]}
                    probe = ssh_base(endpoint, known_hosts, identity) + [
                        "true"
                    ]
                    try:
                        probe_timeout = min(25, max(1, int(deadline - time.monotonic())))
                        completed = run_transport(
                            transports,
                            probe,
                            stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL,
                            timeout=probe_timeout,
                        )
                    except (OSError, subprocess.TimeoutExpired):
                        continue
                    if completed.returncode == 0:
                        fallbacks = [value for value in candidates if value != candidate]
                        observed = time.time()
                        timings = {
                            "providerRunningObservedAtEpoch": provider_running_at or observed,
                            "sshReadyAtEpoch": observed,
                        }
                        return index, live, candidate, fallbacks, timings, None
            if live.get("actual_status") in {"exited", "offline", "error"}:
                raise LifecycleError(
                    f"worker {item['worker']} entered {live.get('actual_status')}: {live.get('status_msg')}"
                )
            transports.wait(PROGRESS_POLL_SECONDS)
        raise LifecycleError(f"worker {item['worker']} was not ready within {ready_timeout}s")

    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
        futures = [pool.submit(wait_one, index) for index in range(len(state["instances"]))]
        try:
            for future in concurrent.futures.as_completed(futures):
                index, live, endpoint, fallbacks, timings, prewarm_ready = future.result()
                dph = live.get("dph_total")
                if isinstance(dph, bool) or not isinstance(dph, (int, float)) or dph <= 0:
                    raise LifecycleError(f"worker {state['instances'][index]['worker']} has invalid live price")
                state["instances"][index].update(
                    status="ready",
                    sshHost=endpoint["host"],
                    sshPort=endpoint["port"],
                    sshEndpointKind=endpoint["kind"],
                    sshFallbacks=fallbacks,
                    actualDph=float(dph),
                    **timings,
                )
                if prewarm_ready is not None:
                    state["instances"][index]["prewarmReady"] = {
                        key: prewarm_ready[key]
                        for key in (
                            "status",
                            "role",
                            "fingerprint",
                            "startedAtEpoch",
                            "finishedAtEpoch",
                            "durationSeconds",
                            "cacheHit",
                        )
                        if key in prewarm_ready
                    }
                atomic_json_write(state_path, state)
                print(f"ready: {state['instances'][index]['worker']} via {endpoint['kind']} SSH")
        except BaseException:
            transports.cancel()
            for future in futures:
                future.cancel()
            raise
    actual_dph = sum(item["actualDph"] for item in state["instances"])
    state["budgetDph"] = actual_dph
    state["lifecycleDeadlineEpoch"] = (
        state["billingStartedAtEpoch"] + state["maxTotalCostUsd"] / actual_dph * 3600
    )
    atomic_json_write(state_path, state)
    bounded_phase_timeout(state, 1, "post-readiness budget check")
    return state


def ssh_base(item: dict[str, Any], known_hosts: Path, identity: Path | None = None) -> list[str]:
    command = [
        "ssh",
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=15",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", f"UserKnownHostsFile={known_hosts}",
        "-p", str(item["sshPort"]),
    ]
    if identity:
        command += ["-i", str(identity)]
    command.append(f"root@{item['sshHost']}")
    return command


def scp_base(item: dict[str, Any], known_hosts: Path, identity: Path | None = None) -> list[str]:
    command = [
        "scp", "-q",
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=15",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", f"UserKnownHostsFile={known_hosts}",
        "-P", str(item["sshPort"]),
    ]
    if identity:
        command += ["-i", str(identity)]
    return command


def endpoint_variants(item: dict[str, Any]) -> list[dict[str, Any]]:
    values = [{**item, "sshEndpointKind": item.get("sshEndpointKind", "unknown")}]
    for fallback in item.get("sshFallbacks", []):
        values.append(
            {
                **item,
                "sshHost": fallback["host"],
                "sshPort": fallback["port"],
                "sshEndpointKind": fallback["kind"],
            }
        )
    return values


PACKAGE_FILES = (
    "Makefile",
    "PARAKEET_RUNTIME.md",
    "README.md",
    "fixtures/hotwords.txt",
    "fixtures/ground-truth.example.json",
    "fixtures/ground-truth.schema.json",
    "fixtures/manifest.v1.json",
    "requirements/voxtral-offline.txt",
    "requirements/voxtral-realtime.txt",
    "runners/parakeet_hotword.sh",
    "runners/voxtral_offline.py",
    "runners/voxtral_realtime.py",
    "runners/voxtral_support.py",
    "scripts/benchlib.py",
    "scripts/bootstrap_voxtral.sh",
    "scripts/score_results.py",
    "scripts/stage_fixtures.py",
    "scripts/verify_fixtures.py",
)
SENSITIVE_EXTRA_COMPONENTS = {".git", ".ssh", ".env", ".envrc", "known_hosts"}
SENSITIVE_EXTRA_SUFFIXES = {".key", ".pem", ".p12", ".pfx"}


def checked_extra_files(extra: Path, destination: Path) -> list[tuple[Path, Path]]:
    """Return regular files as (source, relative path), rejecting secrets and symlinks."""

    if extra.is_symlink():
        raise LifecycleError(f"--extra must not be a symlink: {extra}")
    resolved = extra.resolve()
    if not resolved.exists():
        raise LifecycleError(f"missing --extra: {extra}")
    if destination.resolve().is_relative_to(resolved):
        raise LifecycleError(f"payload destination must not be inside --extra: {extra}")
    candidates = [resolved] if resolved.is_file() else sorted(resolved.rglob("*"))
    files: list[tuple[Path, Path]] = []
    for candidate in candidates:
        relative = (
            Path(resolved.name)
            if resolved.is_file()
            else Path(resolved.name) / candidate.relative_to(resolved)
        )
        lowered = {part.casefold() for part in relative.parts}
        if (
            lowered & SENSITIVE_EXTRA_COMPONENTS
            or candidate.name.casefold().startswith(".env")
            or candidate.name.casefold().startswith("id_")
            or candidate.suffix.casefold() in SENSITIVE_EXTRA_SUFFIXES
        ):
            raise LifecycleError(f"refusing secret-shaped --extra path: {relative}")
        if candidate.is_symlink():
            raise LifecycleError(f"--extra tree contains a symlink: {candidate}")
        if candidate.is_dir():
            continue
        if not candidate.is_file():
            raise LifecycleError(f"--extra tree contains a special file: {candidate}")
        files.append((candidate, relative))
    if not files:
        raise LifecycleError(f"--extra has no regular files: {extra}")
    return files


def package_payload(audio_dir: Path, extras: list[Path], destination: Path) -> None:
    manifest_path = BENCH_ROOT / "fixtures/manifest.v1.json"
    verify_fixtures(manifest_path, audio_dir)
    manifest = load_manifest(manifest_path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.is_symlink() or (destination.exists() and not destination.is_file()):
        raise LifecycleError(f"payload destination is not a regular file: {destination}")

    extra_names: set[str] = set()
    checked_extras: list[tuple[Path, Path]] = []
    for extra in extras:
        resolved = extra.resolve()
        if resolved.name in extra_names:
            raise LifecycleError(f"duplicate --extra basename: {extra}")
        extra_names.add(resolved.name)
        checked_extras.extend(checked_extra_files(extra, destination))

    with tarfile.open(destination, "w:gz") as archive:
        archive.dereference = False
        for relative_name in PACKAGE_FILES:
            source = BENCH_ROOT / relative_name
            if source.is_symlink() or not source.is_file():
                raise LifecycleError(f"package source is not an explicit regular file: {source}")
            archive.add(source, arcname=relative_name, recursive=False)
        for fixture in manifest["fixtures"]:
            source = audio_dir / fixture["name"]
            if source.is_symlink() or not source.is_file():
                raise LifecycleError(f"fixture is not an explicit regular file: {source}")
            archive.add(source, arcname=f"fixtures/audio/{fixture['name']}", recursive=False)
        for source, relative in checked_extras:
            archive.add(source, arcname=str(Path("extras") / relative), recursive=False)


def runner_commands(item: dict[str, Any]) -> list[list[str]]:
    output = f"results/{item['worker']}.json"
    common = ["--manifest", "fixtures/manifest.v1.json", "--audio-dir", "fixtures/audio"]
    arguments = item.get("arguments", [])
    prewarm = item.get("prewarm")
    if prewarm is not None and item["runner"].startswith("voxtral-"):
        prewarm = validate_prewarm_metadata(prewarm, item["runner"])
        runner = (
            "runners/voxtral_offline.py"
            if item["runner"] == "voxtral-offline"
            else "runners/voxtral_realtime.py"
        )
        return [[
            "env",
            "CUDA_VISIBLE_DEVICES=0",
            f"HF_HOME={prewarm['hfHome']}",
            "HF_HUB_OFFLINE=1",
            "TRANSFORMERS_OFFLINE=1",
            "HF_HUB_DISABLE_IMPLICIT_TOKEN=1",
            "HF_HUB_DISABLE_TELEMETRY=1",
            prewarm["pythonPath"],
            runner,
            *common,
            "--output",
            output,
            "--snapshot-dir",
            prewarm["snapshotPath"],
            "--local-files-only",
            *arguments,
        ]]
    if item["runner"] == "voxtral-offline":
        return [
            ["bash", "scripts/bootstrap_voxtral.sh", "offline"],
            [".venv-voxtral-offline/bin/python", "runners/voxtral_offline.py", *common,
             "--output", output, *arguments],
        ]
    if item["runner"] == "voxtral-realtime":
        return [
            ["bash", "scripts/bootstrap_voxtral.sh", "realtime"],
            [".venv-voxtral-realtime/bin/python", "runners/voxtral_realtime.py", *common,
             "--output", output, *arguments],
        ]
    return [
        ["bash", "runners/parakeet_hotword.sh", *common,
         "--output-dir", f"results/{item['worker']}", *arguments]
    ]


def _ordered_endpoints(
    item: dict[str, Any], preferred: dict[str, Any] | None = None
) -> list[dict[str, Any]]:
    values = endpoint_variants(item)
    if preferred is not None:
        values.insert(0, preferred)
    unique: list[dict[str, Any]] = []
    seen: set[tuple[str, int]] = set()
    for value in values:
        key = (value["sshHost"], value["sshPort"])
        if key not in seen:
            unique.append(value)
            seen.add(key)
    return unique


def wait_for_prewarm(
    state: dict[str, Any],
    state_path: Path,
    item: dict[str, Any],
    identity: Path | None,
    preferred_endpoint: dict[str, Any],
    transports: TransportRegistry | None = None,
) -> tuple[dict[str, Any] | None, dict[str, Any]]:
    transports = transports or TransportRegistry()
    prewarm = item.get("prewarm")
    if prewarm is None:
        return None, preferred_endpoint
    prewarm = validate_prewarm_metadata(prewarm, item["runner"])
    timeout_seconds = bounded_phase_timeout(
        state, TRANSFER_TIMEOUT_SECONDS, f"prewarm {item['worker']}"
    )
    deadline = time.monotonic() + timeout_seconds
    known_hosts = state_path.parent / f"known-hosts-{state['runId']}-{item['worker']}"
    while time.monotonic() < deadline:
        for endpoint in _ordered_endpoints(item, preferred_endpoint):
            command = ssh_base(endpoint, known_hosts, identity) + [
                "bash -lc " + shlex.quote(prewarm_probe_script(prewarm))
            ]
            try:
                probe_timeout = min(25, max(1, int(deadline - time.monotonic())))
                completed = run_transport(
                    transports,
                    command,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL,
                    timeout=probe_timeout,
                )
            except (OSError, subprocess.TimeoutExpired):
                continue
            if completed.returncode == 20:
                raise LifecycleError(
                    f"worker {item['worker']} reported a failed prewarm; inspect "
                    f"{prewarm['failedPath']} and {PREWARM_ROOT}/logs/bootstrap.log"
                )
            if completed.returncode == 0:
                return parse_prewarm_probe(completed.stdout, prewarm), endpoint
        transports.wait(PROGRESS_POLL_SECONDS)
    raise LifecycleError(
        f"worker {item['worker']} prewarm was not ready within {timeout_seconds}s"
    )


def _collection_regular_file(path: Path, description: str) -> os.stat_result:
    try:
        metadata = path.lstat()
    except OSError as error:
        raise LifecycleError(f"missing {description}: {path}") from error
    if not stat.S_ISREG(metadata.st_mode):
        raise LifecycleError(f"{description} is not a regular file: {path}")
    return metadata


def _read_collection_json(
    path: Path, description: str, *, max_bytes: int = MAX_COLLECTION_JSON_BYTES
) -> dict[str, Any]:
    metadata = _collection_regular_file(path, description)
    if metadata.st_size <= 0 or metadata.st_size > max_bytes:
        raise LifecycleError(
            f"{description} has an unsafe size ({metadata.st_size} bytes): {path}"
        )
    try:
        with path.open("r", encoding="utf-8") as source:
            value = json.load(source)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise LifecycleError(f"{description} is not valid JSON: {path}") from error
    if not isinstance(value, dict):
        raise LifecycleError(f"{description} must be a JSON object: {path}")
    return value


def _validate_collection_tree(root: Path) -> None:
    """Reject links and special files before a remote bundle becomes local output."""

    if root.is_symlink() or not root.is_dir():
        raise LifecycleError(f"collection stage is not a real directory: {root}")
    entries = 0
    for current, directories, filenames in os.walk(root, followlinks=False):
        for name in [*directories, *filenames]:
            entries += 1
            if entries > MAX_COLLECTION_ENTRIES:
                raise LifecycleError(
                    f"collection stage exceeds {MAX_COLLECTION_ENTRIES} entries"
                )
            path = Path(current) / name
            try:
                mode = path.lstat().st_mode
            except OSError as error:
                raise LifecycleError(f"cannot inspect staged result path: {path}") from error
            if stat.S_ISLNK(mode):
                raise LifecycleError(f"staged result contains a symlink: {path}")
            if not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
                raise LifecycleError(f"staged result contains a special file: {path}")


def _positive_collection_number(value: object, description: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value <= 0:
        raise LifecycleError(f"invalid {description}")
    return float(value)


def _fixture_manifest_identity() -> tuple[str, str]:
    manifest_path = BENCH_ROOT / "fixtures/manifest.v1.json"
    manifest = load_manifest(manifest_path)
    return sha256_file(manifest_path), manifest["fixtureSet"]


def _validate_remote_status(root: Path, item: dict[str, Any]) -> dict[str, Any] | None:
    status_path = root / f"{item['worker']}.status.json"
    if not status_path.exists():
        return None
    value = _read_collection_json(status_path, "remote runner status", max_bytes=64 * 1024)
    exit_code = value.get("exitCode")
    if (
        value.get("schema") != REMOTE_STATUS_SCHEMA
        or isinstance(exit_code, bool)
        or not isinstance(exit_code, int)
        or not 0 <= exit_code <= 255
    ):
        raise LifecycleError(f"invalid remote runner status for {item['worker']}")
    started = _positive_collection_number(
        value.get("startedAtEpoch"), f"remote start time for {item['worker']}"
    )
    ended = _positive_collection_number(
        value.get("endedAtEpoch"), f"remote end time for {item['worker']}"
    )
    if ended < started:
        raise LifecycleError(f"remote runner status runs backwards for {item['worker']}")
    return value


def _expected_voxtral_identity(runner: str) -> tuple[str, str]:
    role = "offline" if runner == "voxtral-offline" else "realtime"
    spec = render_onstart(role)
    return spec.model_id, spec.model_revision


def _validate_voxtral_complete_bundle(
    root: Path,
    item: dict[str, Any],
    fixture_hash: str,
    fixture_set: str,
) -> None:
    expected_schema = {
        "voxtral-offline": "gocassini.voxtral-offline-benchmark.v2",
        "voxtral-realtime": "gocassini.voxtral-realtime-benchmark.v2",
    }[item["runner"]]
    result_path = root / f"{item['worker']}.json"
    result = _read_collection_json(result_path, "Voxtral result")
    expected_model, expected_revision = _expected_voxtral_identity(item["runner"])
    results = result.get("results")
    if (
        result.get("schema") != expected_schema
        or result.get("fixtureManifestSha256") != fixture_hash
        or result.get("fixtureSet") != fixture_set
        or result.get("model") != expected_model
        or result.get("modelRevision") != expected_revision
        or not isinstance(results, list)
        or not results
        or not all(isinstance(value, dict) for value in results)
    ):
        raise LifecycleError(f"invalid or mismatched Voxtral result for {item['worker']}")

    telemetry_root = root / ".telemetry"
    if telemetry_root.is_symlink() or not telemetry_root.is_dir():
        raise LifecycleError(f"missing telemetry directory for {item['worker']}")
    progress_path = telemetry_root / f"{item['worker']}.json.progress.json"
    progress = _read_collection_json(
        progress_path, "completed Voxtral progress telemetry", max_bytes=1024 * 1024
    )
    expected_results = progress.get("expectedResults")
    completed_results = progress.get("completedResults")
    invocation_id = progress.get("invocationId")
    if (
        progress.get("schema") != PROGRESS_SCHEMA
        or progress.get("status") != "completed"
        or progress.get("runner") != item["runner"]
        or progress.get("model") != result["model"]
        or progress.get("modelRevision") != result["modelRevision"]
        or not isinstance(invocation_id, str)
        or not re.fullmatch(r"[a-f0-9]{32}", invocation_id)
        or isinstance(expected_results, bool)
        or not isinstance(expected_results, int)
        or expected_results <= 0
        or completed_results != expected_results
        or len(results) != expected_results
    ):
        raise LifecycleError(f"invalid or mismatched completed telemetry for {item['worker']}")
    _positive_collection_number(
        progress.get("startedAtEpochSeconds"), f"telemetry start time for {item['worker']}"
    )
    _positive_collection_number(
        progress.get("updatedAtEpochSeconds"), f"telemetry update time for {item['worker']}"
    )
    elapsed = progress.get("elapsedSeconds")
    if isinstance(elapsed, bool) or not isinstance(elapsed, (int, float)) or elapsed < 0:
        raise LifecycleError(f"invalid telemetry elapsed time for {item['worker']}")

    first_path = telemetry_root / f"{item['worker']}.json.first-result.json"
    first = _read_collection_json(
        first_path, "Voxtral first-result telemetry", max_bytes=1024 * 1024
    )
    if (
        first.get("schema") != PROGRESS_SCHEMA
        or first.get("status") != "first-result"
        or first.get("invocationId") != invocation_id
        or first.get("runner") != item["runner"]
        or first.get("model") != result["model"]
        or first.get("modelRevision") != result["modelRevision"]
    ):
        raise LifecycleError(f"stale or mismatched first-result telemetry for {item['worker']}")
    _positive_collection_number(
        first.get("firstResultAtEpochSeconds"),
        f"first-result timestamp for {item['worker']}",
    )
    first_elapsed = first.get("timeToFirstResultSeconds")
    if (
        isinstance(first_elapsed, bool)
        or not isinstance(first_elapsed, (int, float))
        or first_elapsed < 0
    ):
        raise LifecycleError(f"invalid first-result duration for {item['worker']}")


def _validate_parakeet_complete_bundle(
    root: Path,
    item: dict[str, Any],
    fixture_hash: str,
    fixture_set: str,
) -> None:
    worker_root = root / item["worker"]
    if worker_root.is_symlink() or not worker_root.is_dir():
        raise LifecycleError(f"missing nested Parakeet result directory for {item['worker']}")
    summary = _read_collection_json(
        worker_root / "summary.json", "Parakeet summary"
    )
    conditions = summary.get("conditions")
    if (
        summary.get("schema") != "gocassini.parakeet-hotword-matrix.v1"
        or summary.get("fixtureManifestSha256") != fixture_hash
        or summary.get("fixtureSet") != fixture_set
        or not isinstance(summary.get("runtimeInputs"), dict)
        or not isinstance(summary.get("coldStart"), dict)
        or not isinstance(conditions, list)
        or not conditions
        or not all(isinstance(value, dict) for value in conditions)
    ):
        raise LifecycleError(f"invalid or mismatched Parakeet summary for {item['worker']}")


def _prewarm_partial_reason(root: Path, item: dict[str, Any]) -> str | None:
    if not item["runner"].startswith("voxtral-") or item.get("prewarm") is None:
        return None
    prewarm = validate_prewarm_metadata(item["prewarm"], item["runner"])
    diagnostic_root = root / "prewarm"
    if diagnostic_root.is_symlink() or not diagnostic_root.is_dir():
        return None
    failed_path = diagnostic_root / Path(prewarm["failedPath"]).name
    if failed_path.exists():
        failed = _read_collection_json(
            failed_path, "prewarm failure sentinel", max_bytes=1024 * 1024
        )
        if (
            failed.get("schema") != PREWARM_FAILED_SCHEMA
            or failed.get("status") != "failed"
            or failed.get("role") != prewarm["role"]
            or failed.get("fingerprint") != prewarm["fingerprint"]
        ):
            raise LifecycleError(f"mismatched prewarm failure sentinel for {item['worker']}")
        return "prewarm-failed"
    log_path = diagnostic_root / "bootstrap.tail.log"
    if log_path.exists():
        metadata = _collection_regular_file(log_path, "prewarm diagnostic log")
        if metadata.st_size > 4 * 1024 * 1024:
            raise LifecycleError(f"prewarm diagnostic log is larger than 4 MiB for {item['worker']}")
        return "prewarm-incomplete"
    return None


def inspect_worker_bundle(
    root: Path, state: dict[str, Any], item: dict[str, Any]
) -> dict[str, Any]:
    """Return a validated complete/partial classification for one worker bundle."""

    _validate_collection_tree(root)
    status = _validate_remote_status(root, item)
    fixture_hash, fixture_set = _fixture_manifest_identity()
    if status is not None and status["exitCode"] == 0:
        if item["runner"].startswith("voxtral-"):
            _validate_voxtral_complete_bundle(
                root, item, fixture_hash, fixture_set
            )
        else:
            _validate_parakeet_complete_bundle(
                root, item, fixture_hash, fixture_set
            )
        return {
            "status": "complete",
            "reason": "validated-success",
            "fixtureManifestSha256": fixture_hash,
            "remoteExitCode": 0,
        }

    if status is not None:
        classification = {
            "status": "partial",
            "reason": "remote-runner-failed",
            "remoteExitCode": status["exitCode"],
        }
    else:
        prewarm_reason = _prewarm_partial_reason(root, item)
        if prewarm_reason is not None:
            classification = {"status": "partial", "reason": prewarm_reason}
        elif (
            item.get("status") == "dispatch-failed"
            or (
                isinstance(item.get("exitCode"), int)
                and not isinstance(item.get("exitCode"), bool)
                and item["exitCode"] != 0
            )
        ) and any(
            (root / f"{item['worker']}.{suffix}.log").is_file()
            for suffix in ("stdout", "stderr")
        ):
            classification = {
                "status": "partial",
                "reason": "dispatch-failed-without-remote-status",
            }
        else:
            raise LifecycleError(f"worker bundle is incomplete for {item['worker']}")
    if item.get("status") == "completed" or item.get("exitCode") == 0:
        raise LifecycleError(
            f"completed worker {item['worker']} has only a partial result bundle"
        )
    return classification


def _mark_worker_bundle(
    root: Path,
    state: dict[str, Any],
    item: dict[str, Any],
    classification: dict[str, Any],
) -> None:
    marker = {
        "schema": COLLECTION_BUNDLE_SCHEMA,
        "runId": state["runId"],
        "worker": item["worker"],
        "runner": item["runner"],
        "validatedStatus": classification["status"],
        "reason": classification["reason"],
        "validatedAtEpoch": time.time(),
    }
    for key in ("fixtureManifestSha256", "remoteExitCode"):
        if key in classification:
            marker[key] = classification[key]
    atomic_json_write(root / COLLECTION_BUNDLE_MARKER, marker)


def _publish_worker_stage(stage: Path, worker_dir: Path) -> None:
    """Publish a fully validated stage without merging old and new files."""

    if not worker_dir.exists():
        stage.replace(worker_dir)
        return
    # Linux renameat2(RENAME_EXCHANGE) swaps two directory entries in one
    # atomic operation. Readers therefore see either the complete old bundle
    # or the complete validated replacement, never a gap or merged tree.
    renameat2 = getattr(ctypes.CDLL(None, use_errno=True), "renameat2", None)
    if renameat2 is None:
        raise LifecycleError("atomic result replacement requires Linux renameat2")
    renameat2.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    renameat2.restype = ctypes.c_int
    if renameat2(-100, os.fsencode(stage), -100, os.fsencode(worker_dir), 2) != 0:
        error_number = ctypes.get_errno()
        raise LifecycleError(
            f"atomic result replacement failed: {os.strerror(error_number)}"
        )
    # After the exchange, ``stage`` names the obsolete bundle.
    shutil.rmtree(stage)


def collect_worker_results(
    state: dict[str, Any],
    state_path: Path,
    output_dir: Path,
    item: dict[str, Any],
    identity: Path | None,
    *,
    preferred_endpoint: dict[str, Any] | None = None,
    transports: TransportRegistry | None = None,
) -> Path:
    """Validate a staged worker bundle, then publish it under the exact run scope."""

    transports = transports or TransportRegistry()
    destination = output_dir / state["runId"]
    destination.mkdir(parents=True, exist_ok=True)
    worker_dir = destination / item["worker"]
    if worker_dir.is_symlink() or (worker_dir.exists() and not worker_dir.is_dir()):
        raise LifecycleError(f"unsafe local worker result path: {worker_dir}")
    known_hosts = state_path.parent / f"known-hosts-{state['runId']}-{item['worker']}"
    errors: list[str] = []
    for endpoint in _ordered_endpoints(item, preferred_endpoint):
        stage: Path | None = Path(
            tempfile.mkdtemp(prefix=f".{item['worker']}-collect-", dir=destination)
        )
        command = scp_base(endpoint, known_hosts, identity) + [
            "-r",
            f"root@{endpoint['sshHost']}:/workspace/cassini-bench/results/.",
            str(stage),
        ]
        collect_timeout = bounded_phase_timeout(
            state, TRANSFER_TIMEOUT_SECONDS, f"collect {item['worker']}"
        )
        try:
            run_transport(
                transports,
                command,
                check=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                timeout=collect_timeout,
            )
            classification = inspect_worker_bundle(stage, state, item)
            _mark_worker_bundle(stage, state, item, classification)
            _publish_worker_stage(stage, worker_dir)
            stage = None
            return worker_dir
        except (
            LifecycleError,
            OSError,
            subprocess.CalledProcessError,
            subprocess.TimeoutExpired,
        ) as error:
            errors.append(type(error).__name__)
        finally:
            if stage is not None:
                with contextlib.suppress(OSError):
                    shutil.rmtree(stage)
    raise LifecycleError(
        f"all SSH collection endpoints failed validation or transfer "
        f"({len(errors)} attempts: {', '.join(errors)})"
    )


def read_first_result_telemetry(worker_dir: Path, item: dict[str, Any]) -> dict[str, float] | None:
    if not item["runner"].startswith("voxtral-"):
        return None
    path = worker_dir / ".telemetry" / f"{item['worker']}.json.first-result.json"
    if not path.exists():
        return None
    value = read_json(path)
    if (
        value.get("schema") != "gocassini.transcription-runner-progress.v1"
        or value.get("status") != "first-result"
        or value.get("runner") != item["runner"]
    ):
        raise LifecycleError(f"invalid first-result telemetry for {item['worker']}")
    result: dict[str, float] = {}
    for source, destination in (
        ("firstResultAtEpochSeconds", "firstResultReportedAtEpoch"),
        ("timeToFirstResultSeconds", "runnerTimeToFirstResultSeconds"),
    ):
        number = value.get(source)
        if (
            isinstance(number, bool)
            or not isinstance(number, (int, float))
            or number < 0
            or (source == "firstResultAtEpochSeconds" and number == 0)
        ):
            raise LifecycleError(f"invalid {source} telemetry for {item['worker']}")
        result[destination] = float(number)
    return result


def dispatch(
    state_path: Path,
    audio_dir: Path,
    extras: list[Path],
    identity: Path | None,
    jobs: int,
    early_output_dir: Path | None = None,
) -> dict[str, Any]:
    state = load_state(state_path)
    transports = TransportRegistry()
    state_dir = state_path.parent
    state_dir.mkdir(parents=True, exist_ok=True)
    archive_path = state_dir / f"payload-{state['runId']}.tar.gz"
    state["payloadPackagingStartedAtEpoch"] = time.time()
    atomic_json_write(state_path, state)
    package_payload(audio_dir, extras, archive_path)
    state["payloadPackagingEndedAtEpoch"] = time.time()
    atomic_json_write(state_path, state)
    remote_archive = f"/tmp/{LABEL_ROOT}-{state['runId']}.tar.gz"
    state_lock = threading.Lock()

    def record(index: int, **values: Any) -> None:
        with state_lock:
            state["instances"][index].update(values)
            atomic_json_write(state_path, state)

    def collect_early(index: int, endpoint: dict[str, Any]) -> bool:
        if early_output_dir is None:
            return False
        item = state["instances"][index]
        record(index, earlyCollectionStartedAtEpoch=time.time())
        try:
            worker_dir = collect_worker_results(
                state,
                state_path,
                early_output_dir,
                item,
                identity,
                preferred_endpoint=endpoint,
                transports=transports,
            )
            first_result = read_first_result_telemetry(worker_dir, item) or {}
            collected_values: dict[str, Any] = {
                "earlyCollectedAtEpoch": time.time(),
                "earlyCollectedDestination": str(worker_dir.resolve()),
                **first_result,
            }
            if first_result:
                collected_values["firstResultCollectedAtEpoch"] = time.time()
            record(index, **collected_values)
            print(f"early-collected: {item['worker']} -> {worker_dir}")
            return True
        except (LifecycleError, OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            record(index, earlyCollectionErrorKind=type(error).__name__)
            return False

    def run_one(index: int) -> tuple[int, int, dict[str, Any], bool]:
        item = state["instances"][index]
        known_hosts = state_dir / f"known-hosts-{state['runId']}-{item['worker']}"
        if item.get("status") not in {"ready", "dispatch-failed"}:
            raise LifecycleError(f"worker {item['worker']} is not ready")
        record(index, status="uploading", uploadStartedAtEpoch=time.time())
        active_endpoint = None
        last_upload_error: Exception | None = None
        for endpoint in endpoint_variants(item):
            upload = scp_base(endpoint, known_hosts, identity) + [
                str(archive_path), f"root@{endpoint['sshHost']}:{remote_archive}"
            ]
            try:
                upload_timeout = bounded_phase_timeout(
                    state, TRANSFER_TIMEOUT_SECONDS, f"upload {item['worker']}"
                )
                run_transport(
                    transports,
                    upload,
                    check=True,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.PIPE,
                    timeout=upload_timeout,
                )
                active_endpoint = endpoint
                alternatives = [
                    {
                        "host": value["sshHost"],
                        "port": value["sshPort"],
                        "kind": value["sshEndpointKind"],
                    }
                    for value in endpoint_variants(item)
                    if (value["sshHost"], value["sshPort"])
                    != (endpoint["sshHost"], endpoint["sshPort"])
                ]
                record(
                    index,
                    uploadEndedAtEpoch=time.time(),
                    sshHost=endpoint["sshHost"],
                    sshPort=endpoint["sshPort"],
                    sshEndpointKind=endpoint["sshEndpointKind"],
                    sshFallbacks=alternatives,
                )
                break
            except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
                last_upload_error = error
        if active_endpoint is None:
            raise LifecycleError(f"all SSH upload endpoints failed: {last_upload_error}")
        prepare_script = (
            "set -euo pipefail; rm -rf /workspace/cassini-bench; "
            "mkdir -p /workspace/cassini-bench; "
            f"tar -xzf {shlex.quote(remote_archive)} -C /workspace/cassini-bench; "
            f"rm -f {shlex.quote(remote_archive)}; cd /workspace/cassini-bench; "
            "python3 scripts/verify_fixtures.py --manifest fixtures/manifest.v1.json "
            "--audio-dir fixtures/audio; mkdir -p results"
        )
        prepare = ssh_base(active_endpoint, known_hosts, identity) + [
            "bash -lc " + shlex.quote(prepare_script)
        ]
        record(index, status="preparing", remotePreparationStartedAtEpoch=time.time())
        try:
            prepare_timeout = bounded_phase_timeout(
                state, SSH_GRACE_SECONDS, f"prepare {item['worker']}"
            )
            run_transport(
                transports,
                prepare,
                check=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                timeout=prepare_timeout,
            )
        finally:
            record(index, remotePreparationEndedAtEpoch=time.time())
        if item.get("prewarm") is not None:
            record(index, status="prewarming")
            try:
                prewarm_ready, active_endpoint = wait_for_prewarm(
                    state, state_path, item, identity, active_endpoint, transports
                )
            except LifecycleError:
                prewarm = validate_prewarm_metadata(item["prewarm"], item["runner"])
                diagnostic_script = (
                    "set -euo pipefail; cd /workspace/cassini-bench; "
                    "mkdir -p results/prewarm; "
                    f"for source in {shlex.quote(prewarm['readyPath'])} "
                    f"{shlex.quote(prewarm['failedPath'])}; do "
                    "if [ -f \"$source\" ]; then cp -- \"$source\" results/prewarm/; fi; done; "
                    f"if [ -f {shlex.quote(PREWARM_ROOT + '/logs/bootstrap.log')} ]; then "
                    f"tail -c 4194304 -- {shlex.quote(PREWARM_ROOT + '/logs/bootstrap.log')} "
                    ">results/prewarm/bootstrap.tail.log; fi"
                )
                diagnostic = ssh_base(active_endpoint, known_hosts, identity) + [
                    "bash -lc " + shlex.quote(diagnostic_script)
                ]
                with contextlib.suppress(
                    LifecycleError,
                    OSError,
                    subprocess.CalledProcessError,
                    subprocess.TimeoutExpired,
                ):
                    run_transport(
                        transports,
                        diagnostic,
                        check=True,
                        stdout=subprocess.DEVNULL,
                        stderr=subprocess.DEVNULL,
                        timeout=min(
                            60,
                            bounded_phase_timeout(
                                state, 60, f"prewarm diagnostics {item['worker']}"
                            ),
                        ),
                    )
                collect_early(index, active_endpoint)
                raise
            alternatives = [
                {
                    "host": value["sshHost"],
                    "port": value["sshPort"],
                    "kind": value["sshEndpointKind"],
                }
                for value in endpoint_variants(item)
                if (value["sshHost"], value["sshPort"])
                != (active_endpoint["sshHost"], active_endpoint["sshPort"])
            ]
            ready_summary = {
                key: prewarm_ready[key]
                for key in (
                    "status",
                    "role",
                    "fingerprint",
                    "startedAtEpoch",
                    "finishedAtEpoch",
                    "durationSeconds",
                    "cacheHit",
                )
                if prewarm_ready is not None and key in prewarm_ready
            }
            record(
                index,
                bootstrapReadyObservedAtEpoch=time.time(),
                prewarmReady=ready_summary,
                sshHost=active_endpoint["sshHost"],
                sshPort=active_endpoint["sshPort"],
                sshEndpointKind=active_endpoint["sshEndpointKind"],
                sshFallbacks=alternatives,
            )
        commands = runner_commands(item)
        benchmark = " && ".join(shlex.join(command) for command in commands)
        prewarm_evidence = ""
        if item.get("prewarm") is not None:
            prewarm_contract = validate_prewarm_metadata(item["prewarm"], item["runner"])
            prewarm_evidence = (
                "mkdir -p results/prewarm; "
                f"cp -- {shlex.quote(prewarm_contract['readyPath'])} "
                "results/prewarm/ready.json; "
            )
        requested_runtime = int(state["maxRuntimeMinutes"]) * 60
        remote_window = bounded_phase_timeout(
            state,
            requested_runtime + SSH_GRACE_SECONDS,
            f"benchmark {item['worker']}",
        )
        if remote_window <= SSH_GRACE_SECONDS:
            raise LifecycleError(
                f"benchmark {item['worker']}: lifecycle budget leaves no bounded SSH grace"
            )
        runtime_seconds = min(requested_runtime, remote_window - SSH_GRACE_SECONDS)
        stdout_log = f"results/{item['worker']}.stdout.log"
        stderr_log = f"results/{item['worker']}.stderr.log"
        status_file = f"results/{item['worker']}.status.json"
        status_tmp = f"{status_file}.tmp"
        script = (
            "set -euo pipefail; cd /workspace/cassini-bench; "
            "test -d results; "
            f"{prewarm_evidence}"
            "started=$(date +%s.%N); set +e; "
            f"timeout --signal=TERM --kill-after=30s {runtime_seconds}s bash -lc {shlex.quote(benchmark)} "
            f">{shlex.quote(stdout_log)} 2>{shlex.quote(stderr_log)}; rc=$?; "
            "set -e; ended=$(date +%s.%N); "
            f"printf '{{\"schema\":\"gocassini.remote-status.v1\",\"exitCode\":%s,"
            f"\"startedAtEpoch\":%s,\"endedAtEpoch\":%s}}\\n' \"$rc\" \"$started\" "
            f"\"$ended\" >{shlex.quote(status_tmp)}; mv -f -- {shlex.quote(status_tmp)} "
            f"{shlex.quote(status_file)}; exit \"$rc\""
        )
        remote = ssh_base(active_endpoint, known_hosts, identity) + ["bash -lc " + shlex.quote(script)]
        record(index, status="running", runnerStartedAtEpoch=time.time())
        try:
            completed = run_transport(
                transports,
                remote,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                timeout=remote_window,
            )
        finally:
            record(index, runnerEndedAtEpoch=time.time())
        early_collected = collect_early(index, active_endpoint)
        return index, completed.returncode, active_endpoint, early_collected

    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
        futures = {
            pool.submit(run_one, index): index for index in range(len(state["instances"]))
        }
        try:
            for future in concurrent.futures.as_completed(futures):
                index = futures[future]
                try:
                    _, returncode, _, _ = future.result()
                except (LifecycleError, OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
                    returncode = 255
                record(
                    index,
                    status="completed" if returncode == 0 else "dispatch-failed",
                    exitCode=returncode,
                )
                print(f"{state['instances'][index]['status']}: {state['instances'][index]['worker']}")
        except BaseException:
            transports.cancel()
            for future in futures:
                future.cancel()
            raise
    return state


def collect(
    state_path: Path,
    output_dir: Path,
    identity: Path | None,
    jobs: int,
    workers: list[str] | None = None,
    *,
    skip_early_collected: bool = False,
) -> Path:
    state = load_state(state_path)
    transports = TransportRegistry()
    destination = output_dir / state["runId"]
    destination.mkdir(parents=True, exist_ok=True)
    known_workers = {item["worker"]: item for item in state["instances"]}
    requested = list(known_workers) if workers is None else workers
    if not requested or len(requested) != len(set(requested)):
        raise LifecycleError("--worker values must be a non-empty unique list")
    unknown = sorted(set(requested) - set(known_workers))
    if unknown:
        raise LifecycleError(f"unknown worker(s): {', '.join(unknown)}")
    if workers is not None:
        incomplete = [
            name
            for name in requested
            if known_workers[name].get("status") not in {"completed", "dispatch-failed"}
        ]
        if incomplete:
            raise LifecycleError(
                "per-worker collection is only safe after completion: "
                + ", ".join(sorted(incomplete))
            )

    selected: list[dict[str, Any]] = []
    skipped: dict[str, dict[str, Any]] = {}
    for name in requested:
        item = known_workers[name]
        worker_dir = destination / name
        if (
            skip_early_collected
            and item.get("earlyCollectedDestination") == str(worker_dir.resolve())
            and worker_dir.is_dir()
            and not worker_dir.is_symlink()
        ):
            try:
                classification = inspect_worker_bundle(worker_dir, state, item)
                _mark_worker_bundle(worker_dir, state, item, classification)
                skipped[name] = classification
            except (LifecycleError, OSError):
                if item.get("sshHost"):
                    selected.append(item)
        elif item.get("sshHost"):
            selected.append(item)

    summary: dict[str, Any] = {
        "schema": "gocassini.vast-collection.v1",
        "runId": state["runId"],
        "startedAtEpoch": time.time(),
        "workers": {
            name: {
                "status": (
                    "already-collected"
                    if classification["status"] == "complete"
                    else "already-collected-partial"
                ),
                "integrity": classification,
            }
            for name, classification in skipped.items()
        },
    }
    state["collectionStartedAtEpoch"] = summary["startedAtEpoch"]
    atomic_json_write(state_path, state)
    selected_names = {item["worker"] for item in selected}
    failures: list[str] = [
        name for name in requested if name not in selected_names and name not in skipped
    ]
    for name in failures:
        summary["workers"][name] = {"status": "missing-ssh-endpoint"}
    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
        futures = {
            pool.submit(
                collect_worker_results,
                state,
                state_path,
                output_dir,
                item,
                identity,
                transports=transports,
            ): item["worker"]
            for item in selected
        }
        try:
            for future in concurrent.futures.as_completed(futures):
                worker = futures[future]
                try:
                    worker_dir = future.result()
                    classification = inspect_worker_bundle(
                        worker_dir, state, known_workers[worker]
                    )
                    first_result = (
                        read_first_result_telemetry(worker_dir, known_workers[worker]) or {}
                        if classification["status"] == "complete"
                        else {}
                    )
                    worker_summary: dict[str, Any] = {
                        "status": (
                            "collected"
                            if classification["status"] == "complete"
                            else "collected-partial"
                        ),
                        "destination": str(worker_dir.resolve()),
                        "finishedAtEpoch": time.time(),
                        "integrity": classification,
                        **first_result,
                    }
                    if first_result:
                        worker_summary["firstResultCollectedAtEpoch"] = time.time()
                    summary["workers"][worker] = worker_summary
                except (LifecycleError, OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
                    failures.append(worker)
                    summary["workers"][worker] = {
                        "status": "failed",
                        "finishedAtEpoch": time.time(),
                    }
        except BaseException:
            transports.cancel()
            for future in futures:
                future.cancel()
            raise
    summary["finishedAtEpoch"] = time.time()
    atomic_json_write(destination / "collection-summary.json", summary)
    state["collectedAtEpoch"] = summary["finishedAtEpoch"]
    atomic_json_write(state_path, state)
    shutil.copy2(state_path, destination / "vast-state.json")
    print(f"collected results in {destination}")
    if failures:
        raise LifecycleError(f"result collection failed for: {', '.join(sorted(failures))}")
    return destination


def destroy_owned(client: VastClient, state_path: Path, jobs: int) -> None:
    initial = load_state(state_path)
    ambiguous_null = [
        item for item in initial["instances"]
        if item.get("instanceId") is None
        and item.get("status") in {"launching", "create-uncertain"}
    ]
    state, _ = recover_owned(
        client,
        state_path,
        wait_seconds=CREATE_VISIBILITY_GRACE_SECONDS if ambiguous_null else 0,
    )
    if ambiguous_null:
        for item in state["instances"]:
            if item.get("instanceId") is None and item.get("status") in {
                "launching", "create-uncertain"
            }:
                item.update(
                    status="create-absence-confirmed",
                    absenceConfirmedAtEpoch=time.time(),
                    reconciliationSeconds=CREATE_VISIBILITY_GRACE_SECONDS,
                )
        atomic_json_write(state_path, state)

    def destroy_one(index: int) -> tuple[int, str, float | None]:
        item = state["instances"][index]
        if item.get("providerPresent") is not True and item.get("status") == "destroyed":
            return index, "destroyed", item.get("destroyConfirmedAtEpoch")
        instance_id = item.get("instanceId")
        if not isinstance(instance_id, int):
            if item.get("status") not in {
                "create-rejected",
                "create-absence-confirmed",
                "not-created-confirmed",
            }:
                raise LifecycleError(
                    f"worker {item['worker']} has an unresolved create outcome"
                )
            return index, "not-created-confirmed", None
        if item.get("providerPresent") is not True:
            raise LifecycleError(
                f"instance {instance_id} is absent from exact-label results; refusing to assume deletion"
            )
        live = client.show(instance_id)
        if live.get("label") != item["label"]:
            raise LifecycleError(
                f"refusing to destroy instance {instance_id}: live label does not exactly match {item['label']}"
            )
        client.destroy(instance_id)
        return index, "destroyed", time.time()

    failures: list[tuple[str, int | None]] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=jobs) as pool:
        futures = {
            pool.submit(destroy_one, index): index for index in range(len(state["instances"]))
        }
        for future in concurrent.futures.as_completed(futures):
            index = futures[future]
            try:
                _, status, destroyed_at = future.result()
            except Exception:
                status = "destroy-failed"
                destroyed_at = None
                failures.append(
                    (state["instances"][index]["worker"], state["instances"][index].get("instanceId"))
                )
            state["instances"][index]["status"] = status
            if destroyed_at is not None:
                state["instances"][index]["destroyConfirmedAtEpoch"] = destroyed_at
            atomic_json_write(state_path, state)
            print(f"{status}: {state['instances'][index]['worker']}")
    if failures:
        remaining = ", ".join(f"{worker}={instance_id}" for worker, instance_id in failures)
        raise LifecycleError(f"teardown incomplete; inspect exact owned instances: {remaining}")
    state["billingEndedAtEpoch"] = time.time()
    state["estimatedLifecycleCostUsd"] = estimated_lifecycle_cost(state)
    atomic_json_write(state_path, state)


def positive_jobs(value: str) -> int:
    jobs = int(value)
    if not 1 <= jobs <= 16:
        raise argparse.ArgumentTypeError("jobs must be in 1..16")
    return jobs


def bounded_attempts(value: str) -> int:
    attempts = int(value)
    if not 1 <= attempts <= 3:
        raise argparse.ArgumentTypeError("launch attempts must be in 1..3")
    return attempts


def add_shared(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--jobs", type=positive_jobs, default=4)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    plan = commands.add_parser("plan")
    plan.add_argument("--matrix", type=Path, required=True)
    plan.add_argument("--plan", type=Path, required=True)
    add_shared(plan)
    launch = commands.add_parser("launch")
    launch.add_argument("--plan", type=Path, required=True)
    launch.add_argument("--state", type=Path, required=True)
    launch.add_argument(
        "--manual-lifecycle",
        action="store_true",
        required=True,
        help="acknowledge that standalone launch requires a later explicit destroy",
    )
    add_shared(launch)
    wait = commands.add_parser("wait")
    wait.add_argument("--state", type=Path, required=True)
    wait.add_argument("--timeout", type=int, default=600)
    wait.add_argument("--identity", type=Path)
    add_shared(wait)
    send = commands.add_parser("dispatch")
    send.add_argument("--state", type=Path, required=True)
    send.add_argument("--audio-dir", type=Path, required=True)
    send.add_argument("--extra", type=Path, action="append", default=[])
    send.add_argument("--identity", type=Path)
    add_shared(send)
    get = commands.add_parser("collect")
    get.add_argument("--state", type=Path, required=True)
    get.add_argument("--output-dir", type=Path, required=True)
    get.add_argument("--identity", type=Path)
    get.add_argument(
        "--worker",
        action="append",
        help="collect one completed worker immediately; repeat for several workers",
    )
    add_shared(get)
    destroy = commands.add_parser("destroy")
    destroy.add_argument("--state", type=Path, required=True)
    add_shared(destroy)
    recover = commands.add_parser("recover")
    recover.add_argument("--state", type=Path, required=True)
    execute = commands.add_parser("execute")
    execute.add_argument("--plan", type=Path, required=True)
    execute.add_argument("--audio-dir", type=Path, required=True)
    execute.add_argument("--state", type=Path, required=True)
    execute.add_argument("--output-dir", type=Path, required=True)
    execute.add_argument("--extra", type=Path, action="append", default=[])
    execute.add_argument("--identity", type=Path)
    execute.add_argument("--ready-timeout", type=int, default=600)
    execute.add_argument(
        "--keep-instances", action="store_true",
        help="explicitly disable the default teardown (continues billing)",
    )
    add_shared(execute)
    run = commands.add_parser("run")
    run.add_argument("--matrix", type=Path, required=True)
    run.add_argument("--audio-dir", type=Path, required=True)
    run.add_argument("--work-dir", type=Path, default=BENCH_ROOT / ".vast-state")
    run.add_argument("--output-dir", type=Path, default=BENCH_ROOT / "results" / "vast")
    run.add_argument("--extra", type=Path, action="append", default=[])
    run.add_argument("--identity", type=Path)
    run.add_argument("--ready-timeout", type=int, default=600)
    run.add_argument(
        "--launch-attempts",
        type=bounded_attempts,
        default=3,
        help="fresh-plan attempts after definitive stale-offer rejection (maximum 3)",
    )
    run.add_argument(
        "--keep-instances", action="store_true",
        help="explicitly disable the default teardown (continues billing)",
    )
    add_shared(run)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    client = VastClient()
    if args.command == "plan":
        plan_matrix(client, args.matrix, args.plan, args.jobs)
    elif args.command == "launch":
        launch_plan(client, args.plan, args.state, args.jobs)
    elif args.command == "wait":
        wait_ready(client, args.state, args.timeout, args.jobs, args.identity)
    elif args.command == "dispatch":
        dispatch(args.state, args.audio_dir, args.extra, args.identity, args.jobs)
    elif args.command == "collect":
        collect(args.state, args.output_dir, args.identity, args.jobs, args.worker)
    elif args.command == "destroy":
        destroy_owned(client, args.state, args.jobs)
    elif args.command == "recover":
        state, recovered = recover_owned(client, args.state)
        print(f"state has {sum(isinstance(item.get('instanceId'), int) for item in state['instances'])} "
              f"known instance IDs ({len(recovered)} newly recovered)")
    elif args.command == "execute":
        launched = False
        collected_dir: Path | None = None
        try:
            launch_plan(client, args.plan, args.state, args.jobs)
            launched = True
            wait_ready(client, args.state, args.ready_timeout, args.jobs, args.identity)
            dispatched = dispatch(
                args.state,
                args.audio_dir,
                args.extra,
                args.identity,
                args.jobs,
                early_output_dir=args.output_dir,
            )
            collected_dir = args.output_dir / load_state(args.state)["runId"]
            collect(
                args.state,
                args.output_dir,
                args.identity,
                args.jobs,
                skip_early_collected=True,
            )
            failed = [item["worker"] for item in dispatched["instances"] if item["status"] != "completed"]
            if failed:
                raise LifecycleError(f"workers failed: {', '.join(failed)}")
        finally:
            try:
                if launched and not args.keep_instances:
                    destroy_owned(client, args.state, args.jobs)
                elif launched:
                    print(f"instances retained by explicit request; recover with state {args.state}")
            finally:
                if collected_dir is not None and collected_dir.is_dir() and args.state.is_file():
                    shutil.copy2(args.state, collected_dir / "vast-state.json")
    elif args.command == "run":
        args.work_dir.mkdir(parents=True, exist_ok=True)
        configured_cap = float(validate_matrix(args.matrix)["maxTotalCostUsd"])
        remaining_cap = configured_cap
        for attempt in range(1, args.launch_attempts + 1):
            # Plan chooses a fresh run ID on every safe retry. Retain every plan
            # and state as an audit trail; neither file contains the API key.
            provisional = args.work_dir / f".plan-{os.getpid()}-{secrets.token_hex(3)}.json"
            plan = plan_matrix(
                client,
                args.matrix,
                provisional,
                args.jobs,
                max_total_cost_usd=remaining_cap,
            )
            plan_path = args.work_dir / f"plan-{plan['runId']}.json"
            provisional.replace(plan_path)
            state_path = args.work_dir / f"state-{plan['runId']}.json"
            try:
                launch_plan(client, plan_path, state_path, args.jobs)
            except StaleOfferError:
                failed_state = load_state(state_path)
                spent = estimated_lifecycle_cost(failed_state)
                remaining_cap = max(0.0, remaining_cap - spent)
                if attempt >= args.launch_attempts:
                    raise LifecycleError(
                        f"stale offers exhausted {args.launch_attempts} bounded launch attempts; "
                        f"last cleaned state: {state_path}"
                    )
                print(
                    f"stale offers on attempt {attempt}; exact-label cleanup completed; "
                    f"re-planning with ${remaining_cap:.4f} of the original "
                    f"${configured_cap:.4f} lifecycle cap remaining"
                )
                continue

            launched = True
            collected_dir: Path | None = None
            try:
                wait_ready(client, state_path, args.ready_timeout, args.jobs, args.identity)
                dispatched = dispatch(
                    state_path,
                    args.audio_dir,
                    args.extra,
                    args.identity,
                    args.jobs,
                    early_output_dir=args.output_dir,
                )
                collected_dir = args.output_dir / load_state(state_path)["runId"]
                collect(
                    state_path,
                    args.output_dir,
                    args.identity,
                    args.jobs,
                    skip_early_collected=True,
                )
                failed = [
                    item["worker"] for item in dispatched["instances"]
                    if item["status"] != "completed"
                ]
                if failed:
                    raise LifecycleError(f"workers failed: {', '.join(failed)}")
            finally:
                try:
                    if not args.keep_instances:
                        destroy_owned(client, state_path, args.jobs)
                    else:
                        print(
                            f"instances retained by explicit request; recover with state {state_path}"
                        )
                finally:
                    if (
                        collected_dir is not None
                        and collected_dir.is_dir()
                        and state_path.is_file()
                    ):
                        shutil.copy2(state_path, collected_dir / "vast-state.json")
            break


if __name__ == "__main__":
    try:
        main()
    except (LifecycleError, subprocess.CalledProcessError) as error:
        print(f"vast-matrix: {error}", file=sys.stderr)
        raise SystemExit(2)
