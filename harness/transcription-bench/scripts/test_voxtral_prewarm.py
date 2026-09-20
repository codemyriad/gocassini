#!/usr/bin/env python3

from __future__ import annotations

import json
import os
import re
import shlex
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

RUNNERS_DIR = Path(__file__).resolve().parent.parent / "runners"
sys.path.insert(0, str(RUNNERS_DIR))

from voxtral_prewarm import MAX_ONSTART_BYTES, PREWARM_ROOT, render_onstart
from voxtral_support import VOXTRAL_OFFLINE_SNAPSHOT, VOXTRAL_REALTIME_SNAPSHOT


class PrewarmRenderTest(unittest.TestCase):
    def test_role_contracts_are_deterministic_public_and_bounded(self) -> None:
        with mock.patch.dict(
            os.environ,
            {
                "VAST_AI_API_KEY": "must-not-appear-vast-secret",
                "OPENROUTER_API_KEY": "must-not-appear-router-secret",
                "HF_TOKEN": "must-not-appear-hf-secret",
            },
        ):
            first = {role: render_onstart(role) for role in ("offline", "realtime")}
            second = {role: render_onstart(role) for role in ("offline", "realtime")}

        for role, spec in first.items():
            with self.subTest(role=role):
                self.assertEqual(spec, second[role])
                self.assertEqual(spec.schema, "gocassini.voxtral-prewarm.v1")
                self.assertEqual(spec.role, role)
                self.assertRegex(spec.fingerprint, r"^[0-9a-f]{64}$")
                self.assertLess(len(spec.script.encode("utf-8")), MAX_ONSTART_BYTES)
                self.assertEqual(spec.ready_path, f"{PREWARM_ROOT}/state/ready.json")
                self.assertEqual(spec.failed_path, f"{PREWARM_ROOT}/state/failed.json")
                self.assertEqual(spec.hf_home, f"{PREWARM_ROOT}/hf")
                self.assertTrue(spec.python_path.startswith(f"{PREWARM_ROOT}/venvs/{role}-"))
                self.assertEqual(spec.metadata["pythonPath"], spec.python_path)
                self.assertEqual(spec.metadata["snapshotPath"], spec.snapshot_path)
                self.assertEqual(spec.metadata["hfHome"], spec.hf_home)
                self.assertTrue(all(re.search(r"==[^=]+$", item) for item in spec.dependencies))
                dependency_versions = {
                    re.sub(r"\[.*\]$", "", item.split("==", 1)[0])
                    .replace("_", "-")
                    .casefold(): item.split("==", 1)[1]
                    for item in spec.dependencies
                }
                self.assertEqual(dependency_versions, dict(spec.package_versions))
                self.assertNotIn("consolidated.safetensors", spec.model_files)
                self.assertNotIn("README.md", spec.model_files)
                for secret in (
                    "must-not-appear-vast-secret",
                    "must-not-appear-router-secret",
                    "must-not-appear-hf-secret",
                ):
                    self.assertNotIn(secret, spec.script)
                syntax = subprocess.run(
                    ["bash", "-n"], input=spec.script, text=True, capture_output=True
                )
                self.assertEqual(syntax.returncode, 0, syntax.stderr)

        self.assertNotEqual(first["offline"].fingerprint, first["realtime"].fingerprint)
        self.assertNotEqual(first["offline"].model_revision, first["realtime"].model_revision)

    def test_exact_model_contracts_exclude_duplicate_weights(self) -> None:
        offline = render_onstart("offline")
        self.assertEqual(offline.model_id, "mistralai/Voxtral-Mini-3B-2507")
        self.assertEqual(
            offline.model_revision, "3060fe34b35ba5d44202ce9ff3c097642914f8f3"
        )
        self.assertEqual(
            offline.model_files,
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
        self.assertEqual(offline.model_id, VOXTRAL_OFFLINE_SNAPSHOT.model_id)
        self.assertEqual(offline.model_revision, VOXTRAL_OFFLINE_SNAPSHOT.revision)
        self.assertEqual(offline.model_files, VOXTRAL_OFFLINE_SNAPSHOT.files)
        realtime = render_onstart("realtime")
        self.assertEqual(realtime.model_id, "mistralai/Voxtral-Mini-4B-Realtime-2602")
        self.assertEqual(
            realtime.model_revision, "2769294da9567371363522aac9bbcfdd19447add"
        )
        self.assertEqual(
            realtime.model_files,
            (
                "config.json",
                "generation_config.json",
                "model.safetensors",
                "params.json",
                "processor_config.json",
                "tekken.json",
            ),
        )
        self.assertEqual(realtime.model_id, VOXTRAL_REALTIME_SNAPSHOT.model_id)
        self.assertEqual(realtime.model_revision, VOXTRAL_REALTIME_SNAPSHOT.revision)
        self.assertEqual(realtime.model_files, VOXTRAL_REALTIME_SNAPSHOT.files)

    def test_invalid_role_and_root_are_rejected_without_side_effects(self) -> None:
        with self.assertRaisesRegex(ValueError, "offline or realtime"):
            render_onstart("other")
        with self.assertRaisesRegex(ValueError, "absolute POSIX"):
            render_onstart("offline", _root="relative/cache")
        with self.assertRaisesRegex(ValueError, "absolute POSIX"):
            render_onstart("offline", _root="/")


class PrewarmShellTest(unittest.TestCase):
    @staticmethod
    def _fake_python(path: Path, marker: Path, real_python: str, fail_download: bool) -> None:
        fail = "exit 47" if fail_download else ":"
        body = f"""#!/usr/bin/env bash
set -euo pipefail
if [[ "${{1:-}}" == "-m" && "${{2:-}}" == "venv" ]]; then
  destination="${{@: -1}}"
  mkdir -p "$destination/bin"
  cp "$0" "$destination/bin/python"
  chmod 700 "$destination/bin/python"
  exit 0
fi
if [[ "${{1:-}}" == "-m" && "${{2:-}}" == "pip" ]]; then
  printf 'pip\n' >> {shlex.quote(str(marker))}
  sleep 0.15
  exit 0
fi
if [[ "${{1:-}}" == "-c" && "${{2:-}}" == *snapshot_download* ]]; then
  {fail}
  {shlex.quote(real_python)} - "$3" "$4" "$5" <<'PY'
import json, os, pathlib, sys
s=json.loads(sys.argv[1]); out=pathlib.Path(sys.argv[2]); mode=sys.argv[3]; snapshot=pathlib.Path(s["snapshotPath"])
snapshot.mkdir(parents=True, exist_ok=True)
if mode != "local":
    for item in s["model"]["files"]:
        target=snapshot/item["name"]
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("wb") as destination: destination.truncate(item["size"])
out.write_text(json.dumps({{"snapshotPath":str(snapshot)}})+"\\n")
os.chmod(out,0o600)
PY
  exit 0
fi
if [[ "${{1:-}}" == "-c" ]]; then
  {shlex.quote(real_python)} - "$3" "$4" "$5" <<'PY'
import json, os, pathlib, sys
s=json.loads(sys.argv[1]); out=pathlib.Path(sys.argv[2]); mode=sys.argv[3]
value={{"python":"3.11.0","torch":"2.8.0+cu128","cuda":"12.8","device":"fake CUDA GPU","bf16":True,
       "torchPath":"/opt/conda/lib/python3.11/site-packages/torch/__init__.py",
       "packages":s["packageVersions"] if mode=="venv" else {{}}}}
out.write_text(json.dumps(value)+"\\n"); os.chmod(out,0o600)
PY
  exit 0
fi
exec {shlex.quote(real_python)} "$@"
"""
        path.write_text(body, encoding="utf-8")
        path.chmod(0o700)

    def _environment(self, fake_bin: Path, secret: str) -> dict[str, str]:
        return {
            "PATH": f"{fake_bin}:{os.environ.get('PATH', '/usr/bin:/bin')}",
            "HOME": os.environ.get("HOME", "/tmp"),
            "OPENROUTER_API_KEY": secret,
            "VAST_AI_API_KEY": secret,
            "CONTAINER_API_KEY": secret,
            "HF_TOKEN": secret,
        }

    def test_parallel_invocations_install_once_and_second_is_cache_hit(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            root = work / "cache"
            fake_bin = work / "bin"
            fake_bin.mkdir()
            marker = work / "pip-runs"
            self._fake_python(fake_bin / "python3", marker, sys.executable, False)
            spec = render_onstart("offline", _root=str(root))
            script_path = work / "onstart.sh"
            script_path.write_text(spec.script, encoding="utf-8")
            environment = self._environment(fake_bin, "parallel-secret-must-not-leak")

            processes = [
                subprocess.Popen(
                    ["bash", str(script_path)],
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    env=environment,
                )
                for _ in range(2)
            ]
            completed = [process.communicate(timeout=20) for process in processes]
            for process, (_, stderr) in zip(processes, completed):
                self.assertEqual(process.returncode, 0, stderr)

            self.assertEqual(marker.read_text(encoding="utf-8").splitlines(), ["pip"])
            ready = json.loads(Path(spec.ready_path).read_text(encoding="utf-8"))
            self.assertEqual(ready["status"], "ready")
            self.assertEqual(ready["fingerprint"], spec.fingerprint)
            self.assertTrue(ready["cacheHit"])
            self.assertEqual(ready["snapshotPath"], spec.snapshot_path)
            self.assertTrue(Path(spec.python_path).is_file())
            self.assertFalse(Path(spec.failed_path).exists())
            self.assertEqual(stat.S_IMODE(Path(spec.ready_path).stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(Path(spec.log_path).stat().st_mode), 0o600)
            log = Path(spec.log_path).read_text(encoding="utf-8")
            self.assertNotIn("parallel-secret-must-not-leak", log)
            self.assertFalse(any(root.joinpath("state").glob(".*.tmp")))

            # A matching sentinel is not trusted blindly. Corrupting one
            # cached artifact forces a validated rebuild and repairs it.
            (Path(spec.snapshot_path) / spec.model_files[0]).write_bytes(b"bad")
            repaired = subprocess.run(
                ["bash", str(script_path)],
                text=True,
                capture_output=True,
                env=environment,
                timeout=20,
            )
            self.assertEqual(repaired.returncode, 0, repaired.stderr)
            self.assertEqual(marker.read_text(encoding="utf-8").splitlines(), ["pip", "pip"])
            repaired_ready = json.loads(Path(spec.ready_path).read_text(encoding="utf-8"))
            self.assertFalse(repaired_ready["cacheHit"])

    def test_download_failure_is_atomic_sanitized_and_retryable(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            root = work / "cache"
            fake_bin = work / "bin"
            fake_bin.mkdir()
            marker = work / "pip-runs"
            self._fake_python(fake_bin / "python3", marker, sys.executable, True)
            spec = render_onstart("realtime", _root=str(root))
            result = subprocess.run(
                ["bash"],
                input=spec.script,
                text=True,
                capture_output=True,
                env=self._environment(fake_bin, "failure-secret-must-not-leak"),
                timeout=20,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(Path(spec.ready_path).exists())
            failed = json.loads(Path(spec.failed_path).read_text(encoding="utf-8"))
            self.assertEqual(failed["status"], "failed")
            self.assertEqual(failed["fingerprint"], spec.fingerprint)
            self.assertEqual(failed["stage"], "download-model")
            self.assertEqual(failed["errorType"], "RuntimeError")
            self.assertEqual(stat.S_IMODE(Path(spec.failed_path).stat().st_mode), 0o600)
            log = Path(spec.log_path).read_text(encoding="utf-8")
            self.assertNotIn("failure-secret-must-not-leak", log)
            self.assertFalse(Path(spec.python_path).exists())
            self.assertFalse(any(root.joinpath("state").glob(".*.tmp")))

            # The same public program can repair the failed attempt after the
            # external failure disappears; no stale failed sentinel survives.
            self._fake_python(fake_bin / "python3", marker, sys.executable, False)
            retried = subprocess.run(
                ["bash"],
                input=spec.script,
                text=True,
                capture_output=True,
                env=self._environment(fake_bin, "retry-secret-must-not-leak"),
                timeout=20,
            )
            self.assertEqual(retried.returncode, 0, retried.stderr)
            self.assertTrue(Path(spec.ready_path).is_file())
            self.assertFalse(Path(spec.failed_path).exists())
            self.assertNotIn(
                "retry-secret-must-not-leak",
                Path(spec.log_path).read_text(encoding="utf-8"),
            )


if __name__ == "__main__":
    unittest.main()
