#!/usr/bin/env python3
import importlib.util
import base64
import gzip
import json
import pathlib
import sqlite3
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('seed_preflight', pathlib.Path(__file__).with_name('validate-seeds.py'))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class SeedPreflightTests(unittest.TestCase):
    def test_combined_seed_refuses_pending_durable_edits(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp)
            (root / 'operator').mkdir()
            (root / 'meetings').mkdir()
            (root / 'meetings/test.opus').write_bytes(b'fixture')
            (root / 'catalog.json').write_text(json.dumps({'version': 'cassini.viewer.catalog.v1', 'meetings': [{'id': 'test', 'audioPath': './meetings/test.opus'}]}))
            db = sqlite3.connect(root / 'operator/annotations.sqlite3')
            db.execute('PRAGMA user_version=7')
            db.execute('CREATE TABLE annotation_head(desired INTEGER, confirmed INTEGER, republish_json TEXT)')
            db.execute('INSERT INTO annotation_head VALUES(2,1,NULL)')
            db.commit()
            db.close()
            payload = base64.urlsafe_b64encode(gzip.compress(json.dumps({'kind': 'cassini-portable-meeting', 'version': 1}).encode())).decode()
            probe = {'format': {'tags': {'CASSINI_FORMAT': 'org.cassini.portable-meeting/1', 'CASSINI_PAYLOAD_CHUNK_COUNT': '1', 'CASSINI_PAYLOAD_000': payload}}}
            with patch.object(preflight.subprocess, 'run', return_value=SimpleNamespace(stdout=json.dumps(probe))):
                with self.assertRaisesRegex(ValueError, 'pending annotation edits'):
                    preflight.validate(temp, temp)

    def test_wal_is_validated_without_changing_source(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp) / 'operator'
            root.mkdir()
            db = sqlite3.connect(root / 'jobs.sqlite3')
            db.execute('PRAGMA journal_mode=WAL')
            for name in ('jobs', 'job_attempts'):
                db.execute(f'CREATE TABLE {name}(id INTEGER)')
            db.execute('CREATE TABLE schema_migrations(version INTEGER)')
            db.commit()
            before = {p.name: p.read_bytes() for p in root.iterdir()}
            preflight.validate('', temp)
            self.assertEqual(before, {p.name: p.read_bytes() for p in root.iterdir()})
            db.close()

    def test_corrupt_database_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp) / 'operator'
            root.mkdir()
            (root / 'jobs.sqlite3').write_bytes(b'not a database')
            with self.assertRaises(sqlite3.DatabaseError):
                preflight.validate('', temp)

    def test_future_durable_schema_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp) / 'operator'
            root.mkdir()
            db = sqlite3.connect(root / 'annotations.sqlite3')
            db.execute('PRAGMA user_version=999')
            db.close()
            with self.assertRaisesRegex(ValueError, 'unsupported schema'):
                preflight.validate('', temp)


if __name__ == '__main__':
    unittest.main()
