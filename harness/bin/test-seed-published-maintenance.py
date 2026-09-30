#!/usr/bin/env python3
import importlib.util
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('maintenance', pathlib.Path(__file__).with_name('seed-published-maintenance.py'))
maintenance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(maintenance)


class SeedMaintenanceTests(unittest.TestCase):
    def run_restore(self, exit_code=0, failure=None):
        calls = []
        captured = {}
        info = {
            'State': {'Running': True}, 'Image': 'sha256:deployed',
            'Config': {'Env': ['APP_SECRET=private', 'NEXTCLOUD_URL=http://nextcloud'],
                       'User': '1000', 'WorkingDir': '/opt/cassini'},
            'NetworkSettings': {'Networks': {'archive': {}, 'appapi': {}}},
        }

        def docker(*args, capture=False):
            calls.append(args)
            if args == ('inspect', 'exapp'):
                return json.dumps([info])
            if args[0] == 'create':
                env_file = pathlib.Path(args[args.index('--env-file') + 1])
                captured['env_file'] = env_file
                captured['env'] = env_file.read_text()
                captured['mode'] = env_file.stat().st_mode & 0o777
                return 'helper\n'
            if failure and args[:len(failure)] == failure:
                raise subprocess.CalledProcessError(1, ['docker', *args])
            if args[0] == 'inspect':
                return str(exit_code)
            return ''

        with tempfile.TemporaryDirectory() as temp:
            catalog = pathlib.Path(temp) / 'catalog.json'
            catalog.write_text('{}')
            with patch.object(maintenance, 'docker', side_effect=docker):
                if exit_code or failure:
                    with self.assertRaises(subprocess.CalledProcessError):
                        maintenance.restore('exapp', catalog)
                else:
                    maintenance.restore('exapp', catalog)
        return calls, captured

    def test_success_releases_server_lock_and_reuses_deployment(self):
        calls, captured = self.run_restore()
        self.assertLess(calls.index(('stop', 'exapp')), calls.index(('start', '--attach', 'helper')))
        self.assertEqual(calls[-2:], [('rm', '--force', 'helper'), ('start', 'exapp')])
        create = next(c for c in calls if c[0] == 'create')
        self.assertIn('sha256:deployed', create)
        self.assertEqual(create[create.index('--volumes-from') + 1], 'exapp')
        self.assertEqual(create[create.index('--network') + 1], 'archive')
        self.assertIn(('network', 'connect', 'appapi', 'helper'), calls)
        self.assertEqual(create[create.index('--user') + 1], '1000')
        self.assertEqual(create[create.index('--workdir') + 1], '/opt/cassini')
        self.assertTrue(create[create.index('--mount') + 1].endswith(',readonly'))
        self.assertNotIn('APP_SECRET=private', repr(calls))
        self.assertIn('APP_SECRET=private', captured['env'])
        self.assertEqual(captured['mode'], 0o600)
        self.assertFalse(captured['env_file'].exists())

    def test_nonzero_container_exit_fails_and_restarts_server(self):
        calls, _ = self.run_restore(exit_code=17)
        self.assertEqual(calls[-2:], [('rm', '--force', 'helper'), ('start', 'exapp')])

    def test_attach_failure_cleans_up_before_restart(self):
        calls, _ = self.run_restore(failure=('start', '--attach'))
        self.assertEqual(calls[-2:], [('rm', '--force', 'helper'), ('start', 'exapp')])

    def test_stop_failure_still_attempts_restart(self):
        calls, _ = self.run_restore(failure=('stop',))
        self.assertNotIn(('start', '--attach', 'helper'), calls)
        self.assertEqual(calls[-2:], [('rm', '--force', 'helper'), ('start', 'exapp')])

    def test_network_failure_leaves_server_running(self):
        calls, _ = self.run_restore(failure=('network', 'connect'))
        self.assertNotIn(('stop', 'exapp'), calls)
        self.assertEqual(calls[-1], ('rm', '--force', 'helper'))

    def test_maintenance_stops_at_each_failed_command(self):
        commands = ['import-meeting-metadata', 'backfill-search', 'backfill-annotations']
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            operator = root / 'operator'
            operator.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$SEED_TEST_LOG"\n[ "$1" != "$SEED_TEST_FAIL" ]\n')
            operator.chmod(0o700)
            script = maintenance.MAINTENANCE.replace('/usr/local/bin/cassini-operator', str(operator))
            for i, failed in enumerate(commands):
                log = root / failed
                result = subprocess.run(['sh', '-c', script], env={**os.environ, 'SEED_TEST_LOG': str(log), 'SEED_TEST_FAIL': failed})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual([line.split()[0] for line in log.read_text().splitlines()], commands[:i + 1])


if __name__ == '__main__':
    unittest.main()
