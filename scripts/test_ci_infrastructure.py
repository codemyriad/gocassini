"""Offline regressions for CI assertions and retention; no Docker/network calls."""
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[1]


class InfrastructureTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="cassini-ci-tests-")
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.env = dict(os.environ, RUNNER_TEMP=str(self.directory), OWNER="test-owner",
                        GH_TOKEN="test-token", DRY_RUN="true", KEEP="1",
                        LOG_DIR=str(self.directory), GITHUB_ACTIONS="true")
        self.env["PATH"] = str(self.directory) + os.pathsep + os.environ["PATH"]

    def executable(self, name, body):
        path = self.directory / name
        path.write_text("#!/usr/bin/env python3\n" + textwrap.dedent(body))
        path.chmod(0o755)

    def run_script(self, script, *args, **env):
        return subprocess.run([str(ROOT / script), *args], cwd=ROOT,
                              env=dict(self.env, **env), text=True, capture_output=True)

    def quality(self, words, **kwargs):
        path = self.directory / "transcript.json"
        path.write_text(json.dumps({"segments": [{"words": [{"text": w} for w in words]}]}))
        return subprocess.run(["python3", str(ROOT / "harness/bin/verify-smoke-transcript.py"),
                               str(path), *[item for key, value in kwargs.items()
                                            for item in ("--" + key, value)]],
                              text=True, capture_output=True)

    def test_quality_accepts_normalised_words_and_rejects_wrong_or_empty_transcript(self):
        self.assertEqual(self.quality(["Héllo,", "WORLD!"], expected="hello world").returncode, 0)
        for words in ([], ["totally unrelated gibberish"]):
            result = self.quality(words, expected="hello world")
            self.assertNotEqual(result.returncode, 0, result.stdout)
        for threshold in ("nan", "0", "-1", "1.1"):
            self.assertNotEqual(self.quality(["hello"], expected="hello", minimum=threshold).returncode, 0)

    def test_failed_phase_keeps_errexit_and_cleanup(self):
        script = self.directory / "phases.sh"
        script.write_text('''#!/usr/bin/env bash
set -euo pipefail
source "$1/harness/bin/lib/ci-phases.sh"
finish() {
  local rc=$?
  trap - EXIT
  ci_phase_end "$rc"
  ci_phase_begin cleanup
  touch "$LOG_DIR/cleaned"
  ci_phase_end
  exit "$rc"
}
trap finish EXIT
ci_phase_begin product
bash -c 'exit 7'
touch "$LOG_DIR/should-not-exist"
''')
        result = subprocess.run(["bash", str(script), str(ROOT)], env=self.env,
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertFalse((self.directory / "should-not-exist").exists())
        self.assertTrue((self.directory / "cleaned").exists())
        rows = [json.loads(line) for line in (self.directory / "phase-timings.jsonl").read_text().splitlines()]
        self.assertEqual([(r["phase"], r["exit_code"]) for r in rows], [("product", 7), ("cleanup", 0)])
        self.assertTrue(all(r["schema_version"] == 1 and r["duration_seconds"] >= 0 for r in rows))
        self.assertEqual(result.stdout.count("::group::"), result.stdout.count("::endgroup::"))

    def registry_fixture(self):
        self.executable("gh", '''
            import os, pathlib, sys
            root = pathlib.Path(os.environ["RUNNER_TEMP"])
            if "DELETE" in sys.argv:
                with (root / "deleted").open("a") as out:
                    out.write(sys.argv[-1].rsplit("/", 1)[-1] + "\\n")
            else:
                print((root / "versions.json").read_text())
        ''')
        self.executable("docker", '''
            import json, os, sys
            if sys.argv[1] == "login":
                sys.stdin.read()
                sys.exit(0)
            ref = sys.argv[-1]
            if os.environ.get("MISSING_REF") and ref.endswith(os.environ["MISSING_REF"]):
                sys.exit(1)
            if ref.endswith(":1.0.0"):
                print(json.dumps({"manifests": [{"digest": "sha256:release-child"}]}))
            elif ref.endswith(":sha-new"):
                print(json.dumps({"manifests": [{"digest": "sha256:rolling-child"}]}))
            else:
                print("{}")
        ''')
        recent = datetime.now(timezone.utc).isoformat()
        def version(identifier, digest, tags, created="2020-01-01T00:00:00Z"):
            return dict(id=identifier, name="sha256:" + digest, created_at=created,
                        metadata=dict(container=dict(tags=tags)))
        versions = [version(1, "release", ["1.0.0"]), version(2, "release-child", []),
                    version(3, "rolling", ["sha-new"], "2021-01-01T00:00:00Z"),
                    version(4, "rolling-child", []), version(5, "expired", ["sha-old"]),
                    version(6, "orphan", []), version(7, "upload", [], recent)]
        # gh --paginate returns adjacent JSON arrays; retention must see all pages.
        (self.directory / "versions.json").write_text(json.dumps(versions[:4]) + "\n" + json.dumps(versions[4:]))

    def test_retention_dry_run_then_delete_preserves_named_and_child_manifests(self):
        self.registry_fixture()
        script = "scripts/prune-container-images.sh"
        result = self.run_script(script, "images")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("DRY RUN: would delete gocassini version 5", result.stdout)
        self.assertIn("DRY RUN: would delete gocassini version 6", result.stdout)
        self.assertFalse((self.directory / "deleted").exists())
        result = self.run_script(script, "images", DRY_RUN="false")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.directory / "deleted").read_text().splitlines(), ["5", "6"])
        result = self.run_script(script, "verify")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotEqual(self.run_script(script, "verify", MISSING_REF="@sha256:release-child").returncode, 0)

    def test_registry_inspection_failure_prevents_deletion(self):
        self.registry_fixture()
        result = self.run_script("scripts/prune-container-images.sh", "images", DRY_RUN="false", MISSING_REF=":1.0.0")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.directory / "deleted").exists())

    def test_base_retention_sorts_across_pages(self):
        self.registry_fixture()
        result = self.run_script("scripts/prune-container-images.sh", "cuda-base", DRY_RUN="false")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(set((self.directory / "deleted").read_text().splitlines()), {"1", "2", "3", "4", "5", "6"})

    def test_cuda_base_hit_and_miss_decisions_and_cleanup_order(self):
        self.executable("curl", 'print("ETag: fixed-test-etag")\n')
        self.executable("docker", '''
            import os, sys
            if any(arg.startswith("nvidia/cuda:") for arg in sys.argv):
                print('"sha256:fixed-test-nvidia"')
            else:
                sys.exit(0 if os.environ["BASE_EXISTS"] == "true" else 1)
        ''')
        workflow = (ROOT / ".github/workflows/publish-exapp-image.yml").read_text()
        base = workflow.split("  build-cuda-base:\n", 1)[1].split("  build-image-cuda:\n", 1)[0]
        resolve = base.split("      - name: Resolve base ref + check existence\n", 1)[1]
        body, remainder = resolve.split("        run: |\n", 1)[1].split("      - name:", 1)
        body = textwrap.dedent(body).replace("${{ env.REGISTRY }}", "ghcr.io").replace("${{ github.repository_owner }}", "test-owner")
        self.assertTrue(remainder.startswith(" Free disk for a missing CUDA base\n"))
        cleanup, build = remainder.split("      - name: Build + push base image", 1)
        for block in (cleanup, build):
            self.assertIn("if: steps.base.outputs.build == 'true'", block)
        for exists, expected in (("true", "false"), ("false", "true")):
            output = self.directory / (exists + ".output")
            result = subprocess.run(["bash", "-c", body], cwd=ROOT, env=dict(self.env, BASE_EXISTS=exists, GITHUB_OUTPUT=str(output)),
                                    text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("build=" + expected, output.read_text())


if __name__ == "__main__":
    unittest.main()
