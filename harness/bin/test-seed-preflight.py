#!/usr/bin/env python3
import importlib.util
import pathlib
import sqlite3
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('seed_preflight', pathlib.Path(__file__).with_name('validate-seeds.py'))
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)


class SeedPreflightTests(unittest.TestCase):
    def test_wal_is_validated_without_changing_source(self):
        with tempfile.TemporaryDirectory() as temp:
            root = pathlib.Path(temp) / 'operator'
            root.mkdir()
            db = sqlite3.connect(root / 'jobs.sqlite3')
            db.execute('PRAGMA journal_mode=WAL')
            for name in ('jobs', 'job_attempts', 'schema_migrations'):
                db.execute(f'CREATE TABLE {name}(id INTEGER)')
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
