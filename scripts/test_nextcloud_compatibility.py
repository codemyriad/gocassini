"""Offline negative controls for the support promise and release authorization."""
import copy
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import nextcloud_compatibility as policy
import compatibility_evidence as evidence


class PolicyTests(unittest.TestCase):
    def setUp(self):
        self.policy = policy.read_json(policy.INVENTORY)

    def test_checked_in_contract(self):
        policy.validate(self.policy, policy.ROOT / 'appinfo/info.xml')
        self.assertEqual((policy.ROOT / 'docs/nextcloud-support-table.md').read_text(), policy.support_table(self.policy))

    def test_bad_support_contracts(self):
        changes = [lambda p: p['baselines'].pop(),
                   lambda p: p.update(minimum='33.0.8'),
                   lambda p: p.update(reference='absent'),
                   lambda p: p['baselines'].append(copy.deepcopy(p['baselines'][0])),
                   lambda p: p['previews'].append({'major': 35, 'image': 'nextcloud:35'}),
                   lambda p: p['baselines'][0]['images'].update(nextcloud='nextcloud:33'),
                   lambda p: p['baselines'][0]['apps'].pop('app_api'),
                   lambda p: p.update(canary_image='ghcr.io/codemyriad/gocassini:latest')]
        for change in changes:
            p = copy.deepcopy(self.policy)
            change(p)
            with self.subTest(policy=p), self.assertRaises(ValueError):
                policy.validate(p)

    def test_manifest_cannot_advertise_untested_major(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'info.xml'
            path.write_text('<info><dependencies><nextcloud min-version="33.0.9" max-version="36"/></dependencies></info>')
            with self.assertRaisesRegex(ValueError, 'manifest'):
                policy.validate(self.policy, path)

    def test_event_tiers(self):
        all_ids = {s['id'] for s in self.policy['baselines']}
        self.assertEqual({s['id'] for s in policy.select(self.policy, 'all')}, all_ids)
        self.assertEqual({s['id'] for s in policy.select(self.policy, 'pr', ['ci/nextcloud-compatibility.json'])}, all_ids)
        ordinary = policy.select(self.policy, 'pr', ['cassini-viewer/src/viewer/timing.ts'])
        self.assertEqual(len(ordinary), 2)
        self.assertIn(self.policy['minimum'], [s['nextcloud_version'] for s in ordinary])
        self.assertIn(self.policy['reference'], [s['id'] for s in ordinary])

    def test_app_version_resolution_fails_closed(self):
        self.assertTrue(policy.satisfies('35.0.0', '>=35.0.0 <36.0.0'))
        self.assertFalse(policy.satisfies('34.0.0', '>=35.0.0 <36.0.0'))
        self.assertFalse(policy.satisfies('35.0.0', '*'))
        self.assertFalse(policy.satisfies('35.0.0', '>=35.0.0 || >=30.0.0'))

    def test_prerelease_identity_is_allowed_only_outside_baselines(self):
        preview = copy.deepcopy(self.policy['baselines'][-1])
        preview['nextcloud_version'] = '36.0.0 RC1'
        policy.validate_stack(preview)
        self.assertEqual(policy.server_version(preview['nextcloud_version']), (36, 0, 0))
        self.policy['baselines'][-1]['nextcloud_version'] = '35.0.0 RC1'
        with self.assertRaisesRegex(ValueError, 'stable'):
            policy.validate(self.policy)
        with self.assertRaisesRegex(ValueError, 'versionstring'):
            policy.server_version('36.0.0 unknown')

    def test_canary_reports_changed_bytes_even_when_versions_match(self):
        candidate = copy.deepcopy(self.policy['baselines'][-1])
        candidate['id'] = 'canary-35'
        candidate['images']['nextcloud'] = 'nextcloud:35@' + candidate['images']['nextcloud'].split('@')[1]
        self.assertEqual(policy.candidate_changes(self.policy, candidate)['changes'], [])
        candidate['apps']['spreed']['sha256'] = 'a' * 64
        changes = policy.candidate_changes(self.policy, candidate)
        self.assertEqual(changes['baseline'], 'nc35')
        self.assertEqual([c['component'] for c in changes['changes']], ['spreed sha256'])
        candidate['nextcloud_version'] = '36.0.0'
        self.assertEqual(policy.candidate_changes(self.policy, candidate)['baseline'], self.policy['reference'])

    def test_app_replacement_requires_disposable_fixture(self):
        with patch.dict('os.environ', {}, clear=True), self.assertRaisesRegex(ValueError, 'disposable'):
            policy.install_app(self.policy['baselines'][0], 'spreedtest', 'spreed')

    def test_runner_refuses_reused_evidence_before_starting_docker(self):
        with tempfile.TemporaryDirectory() as tmp:
            marker = Path(tmp) / 'observed.json'
            marker.write_text('earlier evidence')
            result = subprocess.run([str(policy.ROOT / 'harness/bin/ci-nextcloud-compatibility.sh'), 'nc35'],
                env={**os.environ, 'LOG_DIR': tmp, 'IMAGE_REF': 'unused'}, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('new LOG_DIR', result.stderr)
            self.assertEqual(marker.read_text(), 'earlier evidence')


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.policy = policy.read_json(policy.INVENTORY)
        self.src = {'repository': 'codemyriad/gocassini', 'sha': 'a' * 40, 'ref': 'refs/tags/v1.2.3',
                    'event': 'push', 'workflow': 'codemyriad/gocassini/' + evidence.WORKFLOW,
                    'run_id': '12', 'attempt': '1'}
        self.policy_hash, self.manifest_hash = 'b' * 64, 'c' * 64
        self.config = 'sha256:' + 'd' * 64
        self.images = {}
        for name in ('cpu', 'cuda'):
            platforms = {'linux/amd64': {'manifest_digest': 'sha256:' + 'f' * 64, 'config_digest': self.config}}
            if name == 'cpu':
                platforms['linux/arm64'] = {'manifest_digest': 'sha256:' + '1' * 64, 'config_digest': 'sha256:' + '2' * 64}
            self.images[name] = {'ref': f'ghcr.io/codemyriad/gocassini:1.2.3{("-cuda" if name == "cuda" else "")}',
                                 'digest': 'sha256:' + ('e' if name == 'cpu' else '3') * 64, 'platforms': platforms}
        self.jobs = {j: {'result': 'success'} for j in evidence.REQUIRED_JOBS}
        for name, job in [('cpu', 'build-image'), ('cuda', 'build-image-cuda')]:
            self.jobs[job]['outputs'] = {'digest': self.images[name]['digest']}
        self.records = []
        for stack in self.policy['baselines']:
            self.records.append({'schema': 'cassini.compatibility.v1', 'scenario': evidence.SCENARIO, 'source': copy.deepcopy(self.src),
                'mode': 'baseline', 'policy_sha256': self.policy_hash, 'manifest_sha256': self.manifest_hash,
                'stack': copy.deepcopy(stack), 'result': 'passed', 'exit_code': 0, 'cleanup': 'passed',
                'checks': dict.fromkeys(evidence.REQUIRED_CHECKS, True), 'observed': {
                    'nextcloud_version': stack['nextcloud_version'],
                    'apps': {a: l['version'] for a, l in stack['apps'].items()},
                    'images': {s: {'ref': ref, 'config_id': self.config} for s, ref in stack['images'].items()},
                    'cassini': {'config_id': self.config}}})
        self.run = {'status': 'completed', 'conclusion': 'success', 'event': 'push', 'path': evidence.WORKFLOW,
                    'head_sha': self.src['sha'], 'head_branch': 'v1.2.3', 'head_repository': {'full_name': self.src['repository']},
                    'id': 12, 'run_attempt': 1}

    def build(self):
        return evidence.build_index(self.policy, self.records, self.jobs, self.images,
                                    self.src, self.policy_hash, self.manifest_hash)

    def verify(self, index):
        evidence.verify_index(index, self.policy, self.src['repository'], 'v1.2.3', self.src['sha'],
                              self.run, self.images, self.policy_hash, self.manifest_hash)

    def test_matching_evidence_authorizes_candidate(self):
        self.verify(self.build())

    def test_browser_failure_retains_completed_product_assertions(self):
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp)
            stack = log / 'stack.json'
            policy.write_json(stack, self.policy['baselines'][0])
            policy.write_json(log / 'summary.json', {'result': 'running', 'exit_code': 1, 'cleanup': 'passed',
                'source_image_id': self.config, 'installed_image_id': self.config,
                'control': {'recording_performed': True}, 'validation': {'result': 'passed', 'runs': [
                    {'artifact': {'segment_count': 1, 'word_count': 1},
                     'access': {'participant': True, 'outsider_denied': True}}] * 2}})
            policy.write_json(log / 'observed.json', self.records[0]['observed'])
            policy.write_json(log / 'browser/result.json', {'result': 'failed', 'checks': {'login': True}})
            record = evidence.collect(log, stack, 'baseline', 1, '2026-09-20T00:00:00+00:00')
            self.assertEqual(record['result'], 'product-failure')
            self.assertEqual({k for k, v in record['checks'].items() if not v}, {'embedded_browser'})

    def test_missing_major_and_duplicate_evidence_block(self):
        self.records.pop()
        with self.assertRaisesRegex(ValueError, 'missing='):
            self.build()
        self.setUp()
        self.records.append(copy.deepcopy(self.records[0]))
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            self.build()

    def test_incomplete_or_wrong_observations_block(self):
        changes = [lambda r: r.update(mode='canary'), lambda r: r.update(result='cancelled'),
            lambda r: r.update(scenario='http-only'),
            lambda r: r.update(result='skipped'), lambda r: r.update(result='environment-failure'),
            lambda r: r.update(cleanup='failed'), lambda r: r.update(exit_code=1),
            lambda r: r['source'].update(attempt='2'), lambda r: r['source'].update(sha='4' * 40),
            lambda r: r.update(policy_sha256='wrong'), lambda r: r.update(manifest_sha256='wrong'),
            lambda r: r['checks'].pop('embedded_browser'), lambda r: r['checks'].update(outsider_denied=False),
            lambda r: r['checks'].update(recording=1), lambda r: r['observed'].update(nextcloud_version='32.0.0'),
            lambda r: r['observed']['apps'].update(app_api='1.0.0'), lambda r: r['observed']['images'].pop('appapi-harp'),
            lambda r: r['observed']['cassini'].update(config_id='sha256:' + '9' * 64)]
        original = copy.deepcopy(self.records)
        for change in changes:
            self.records = copy.deepcopy(original)
            change(self.records[0])
            with self.subTest(record=self.records[0]), self.assertRaises(ValueError):
                self.build()

    def test_failed_or_skipped_specialized_job_blocks(self):
        for job in evidence.REQUIRED_JOBS:
            for result in ['failure', 'cancelled', 'skipped', 'timed_out']:
                with self.subTest(job=job, result=result):
                    self.jobs[job]['result'] = result
                    with self.assertRaisesRegex(ValueError, 'required job'):
                        self.build()
                    self.jobs[job]['result'] = 'success'

    def test_retagged_registry_image_blocks(self):
        self.images['cpu']['digest'] = 'sha256:' + '5' * 64
        with self.assertRaisesRegex(ValueError, 'registry tag differs'):
            self.build()

    def test_source_run_and_approval_recheck(self):
        index = self.build()
        for key, value in [('run_attempt', 2), ('event', 'pull_request'), ('head_branch', 'main'),
                           ('path', '.github/workflows/unrelated.yml'), ('conclusion', 'cancelled'),
                           ('status', 'in_progress'), ('head_sha', '0' * 40)]:
            original = self.run[key]
            self.run[key] = value
            with self.subTest(field=key), self.assertRaises(ValueError):
                self.verify(index)
            self.run[key] = original
        self.images['cpu']['digest'] = 'sha256:' + '6' * 64
        with self.assertRaisesRegex(ValueError, 'moved'):
            # Detached JSON is what an actual downloaded artifact provides.
            self.verify(json.loads(json.dumps({**index, 'images': {**index['images'], 'cpu': {
                **index['images']['cpu'], 'digest': self.jobs['build-image']['outputs']['digest']}}})))

    def test_missing_and_expired_artifacts_refuse_without_fallback(self):
        with tempfile.TemporaryDirectory() as tmp:
            for found in ([], [{'name': 'release-evidence-1', 'expired': True}]):
                with self.subTest(found=found), patch.object(evidence, 'current_commit', return_value=self.src['sha']), \
                     patch.object(evidence, 'gh', return_value={'workflow_runs': [self.run]}), \
                     patch.object(evidence, 'command', return_value=json.dumps([{'artifacts': found}])):
                    with self.assertRaisesRegex(ValueError, 'missing or expired'):
                        evidence.await_evidence(self.src['repository'], 'v1.2.3', self.src['sha'], Path(tmp)/'out.json', 1)

    def test_completed_run_hands_off_exact_attempt_artifact(self):
        index = self.build()
        body = io.BytesIO()
        with zipfile.ZipFile(body, 'w') as archive:
            archive.writestr('release-evidence.json', json.dumps(index))
        artifacts = [{'artifacts': [{'id': 123, 'name': 'release-evidence-1', 'expired': False}]}]
        with tempfile.TemporaryDirectory() as tmp, \
             patch.object(evidence, 'current_commit', return_value=self.src['sha']), \
             patch.object(evidence, 'gh', return_value={'workflow_runs': [self.run]}), \
             patch.object(evidence, 'command', return_value=json.dumps(artifacts)), \
             patch.object(evidence.subprocess, 'check_output', return_value=body.getvalue()):
            path = Path(tmp) / 'qualified/release-evidence.json'
            received, run = evidence.await_evidence(self.src['repository'], 'v1.2.3', self.src['sha'], path, 1)
            self.assertEqual(run, self.run)
            self.assertEqual(received, index)
            self.assertEqual(policy.read_json(path), index)
            self.verify(received)

    def test_moved_tag_and_ambiguous_source_runs_refuse(self):
        with patch.object(evidence, 'current_commit', return_value='0' * 40):
            with self.assertRaisesRegex(ValueError, 'tag moved'):
                evidence.await_evidence(self.src['repository'], 'v1.2.3', self.src['sha'], 'unused', 1)
        with patch.object(evidence, 'current_commit', return_value=self.src['sha']), \
             patch.object(evidence, 'gh', return_value={'workflow_runs': [self.run, {**self.run, 'id': 13}]}):
            with self.assertRaisesRegex(ValueError, 'ambiguous'):
                evidence.await_evidence(self.src['repository'], 'v1.2.3', self.src['sha'], 'unused', 1)


if __name__ == '__main__':
    unittest.main()
