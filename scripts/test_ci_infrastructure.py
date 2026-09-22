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

    def test_phase_timing_preserves_command_failure(self):
        script = self.directory / "phases.sh"
        script.write_text('''#!/usr/bin/env bash
set -euo pipefail
source "$1/harness/bin/lib/ci-phases.sh"
trap 'rc=$?; ci_phase_end "$rc"; exit "$rc"' EXIT
ci_phase_begin product
bash -c 'exit 7'
exit 0
''')
        result = subprocess.run(["bash", str(script), str(ROOT)], env=self.env,
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 7, result.stderr)

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

    def test_cuda_cleanup_only_when_needed(self):
        self.executable("df", '''
            import os, pathlib
            cleaned = (pathlib.Path(os.environ["RUNNER_TEMP"]) / "reclaimed").exists()
            free = os.environ["FREE_AFTER" if cleaned else "FREE_BEFORE"]
            print("Filesystem 1024-blocks Used Available Capacity Mounted on")
            print("/dev/test 150000000 10000000 " + free + " 10% /")
        ''')
        self.executable("sudo", '''
            import os, pathlib, sys
            root = pathlib.Path(os.environ["RUNNER_TEMP"])
            with (root / "sudo-calls").open("a") as out:
                out.write(" ".join(sys.argv[1:]) + "\\n")
            (root / "reclaimed").touch()
        ''')
        script = "scripts/ci-cuda-build-space.sh"
        result = self.run_script(script, FREE_BEFORE="86000000", FREE_AFTER="110000000")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.directory / "sudo-calls").exists())
        result = self.run_script(script, FREE_BEFORE="14000000", FREE_AFTER="20000000")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("rm -rf", (self.directory / "sudo-calls").read_text())


if __name__ == "__main__":
    unittest.main()
