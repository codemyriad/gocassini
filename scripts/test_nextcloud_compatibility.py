"""Offline negative controls for the support promise and release authorization."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

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
            self.records.append({'schema': 'cassini.compatibility.v1', 'source': copy.deepcopy(self.src),
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


if __name__ == '__main__':
    unittest.main()
