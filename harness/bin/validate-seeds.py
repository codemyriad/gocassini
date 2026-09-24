#!/usr/bin/env python3
"""Read-only seed preflight. Runs before image builds or container changes."""
import argparse
import base64
import gzip
import hashlib
import json
import pathlib
import shutil
import sqlite3
import subprocess
import tempfile


def validate(published, operator):
    if published:
        root = pathlib.Path(published)
        catalog = json.loads((root / 'catalog.json').read_text())
        if catalog.get('version') != 'cassini.viewer.catalog.v1' or not catalog.get('meetings'):
            raise ValueError('expected a non-empty cassini.viewer.catalog.v1 catalog')
        expected = {}
        ids = set()
        for entry in catalog['meetings']:
            rel = entry.get('audioPath', '').removeprefix('./')
            parts = pathlib.PurePosixPath(rel).parts
            if len(parts) != 2 or parts[0] != 'meetings' or not parts[1].endswith('.opus') or rel in expected or not entry.get('id') or entry['id'] in ids:
                raise ValueError('invalid or duplicate catalog entry')
            asset = root / rel
            if asset.is_symlink() or (root / 'meetings').is_symlink() or not asset.is_file() or asset.stat().st_size == 0:
                raise ValueError(f'{rel}: missing, empty, or linked recording')
            expected[rel] = entry
            ids.add(entry['id'])
        manifest_path = root / 'seed-manifest.json'
        if manifest_path.exists():
            manifest = json.loads(manifest_path.read_text())
            version = manifest.get('version')
            if version not in ('cassini.seed.pack.v1', 'cassini.seed.pack.v2'):
                raise ValueError('unsupported seed manifest version')
            if version.endswith('v2') and (manifest.get('complete') is not True or manifest.get('failed') or manifest.get('selected') != len(expected) or manifest.get('annotations') not in ('current', 'embedded')):
                raise ValueError('incomplete or invalid v2 seed pack; resume its pull')
            seen, total = set(), 0
            for entry in manifest['meetings']:
                rel = entry['path']
                if rel in seen or rel not in expected or entry['id'] != expected[rel]['id']:
                    raise ValueError('manifest/catalog identity mismatch')
                asset = root / rel
                size = asset.stat().st_size
                if size != entry['bytes']:
                    raise ValueError(f'{rel}: seed size mismatch')
                if version.endswith('v2') or entry.get('sha256'):
                    digest = hashlib.sha256()
                    with asset.open('rb') as stream:
                        for block in iter(lambda: stream.read(1024 * 1024), b''):
                            digest.update(block)
                    if digest.hexdigest() != entry.get('sha256'):
                        raise ValueError(f'{rel}: seed SHA-256 mismatch')
                total += size
                seen.add(rel)
            if seen != expected.keys() or manifest['totals'] != {'meetings': len(expected), 'bytes': total}:
                raise ValueError('manifest/catalog totals mismatch')
        else:
            print('[seed-preflight] legacy static pack: no integrity manifest', flush=True)
        for entry in catalog['meetings']:
            asset = root / entry['audioPath'].removeprefix('./')
            result = subprocess.run(['ffprobe', '-v', 'error', '-show_entries',
                                     'format_tags:stream_tags', '-of', 'json', str(asset)],
                                    capture_output=True, text=True, timeout=60, check=True)
            probe = json.loads(result.stdout)
            tags = dict(probe.get('format', {}).get('tags', {}))
            for stream in probe.get('streams', []):
                tags.update(stream.get('tags', {}))
            tags = {k.upper(): v for k, v in tags.items()}
            if tags.get('CASSINI_FORMAT') != 'org.cassini.portable-meeting/1' or not tags.get('CASSINI_PAYLOAD_000'):
                raise ValueError(f'{asset.name}: not a portable meeting recording')
            encoded = ''.join(tags[f'CASSINI_PAYLOAD_{n:03d}'] for n in range(int(tags['CASSINI_PAYLOAD_CHUNK_COUNT'])))
            payload = json.loads(gzip.decompress(base64.urlsafe_b64decode(encoded + '=' * (-len(encoded) % 4))))
            if payload.get('version') != 1 or payload.get('kind') != 'cassini-portable-meeting':
                raise ValueError(f'{asset.name}: unsupported portable payload')
        print(f'[seed-preflight] {len(catalog["meetings"])} portable recordings readable', flush=True)
    if operator:
        root = pathlib.Path(operator) / 'operator'
        # Open a temporary copy including WAL. SQLite may create/update SHM even
        # on a read-only connection; never let that change the source snapshot.
        with tempfile.TemporaryDirectory(prefix='cassini-seed-check-') as temp:
            for name in ('jobs.sqlite3', 'annotations.sqlite3', 'search.sqlite3', 'meetings.sqlite3'):
                source = root / name
                if not source.exists():
                    continue
                for suffix in ('', '-wal'):
                    p = root / (name + suffix)
                    if p.exists():
                        shutil.copyfile(p, pathlib.Path(temp) / p.name)
                db = sqlite3.connect(str(pathlib.Path(temp) / name))
                try:
                    if db.execute('PRAGMA quick_check').fetchall() != [('ok',)]:
                        raise ValueError(f'{name}: SQLite integrity check failed')
                    tables = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
                    if name == 'jobs.sqlite3' and not {'jobs', 'job_attempts', 'schema_migrations'} <= tables:
                        raise ValueError('jobs.sqlite3: incompatible operator database')
                    if name == 'annotations.sqlite3':
                        version = db.execute('PRAGMA user_version').fetchone()[0]
                        if version not in range(2, 8):
                            raise ValueError(f'annotations.sqlite3: unsupported schema {version}')
                        if published and 'annotation_head' in tables:
                            columns = {r[1] for r in db.execute('PRAGMA table_info(annotation_head)')}
                            predicate = 'desired != confirmed'
                            if 'republish_json' in columns:
                                predicate += ' OR republish_json IS NOT NULL'
                            if db.execute('SELECT count(*) FROM annotation_head WHERE ' + predicate).fetchone()[0]:
                                raise ValueError('operator seed has pending annotation edits; export current annotations into the meeting pack and use a synchronized operator snapshot')
                finally:
                    db.close()
        print('[seed-preflight] operator SQLite snapshot readable (including WAL)', flush=True)
        if published:
            print('[seed-preflight] meeting pack supplies content and annotations; operator seed supplies job history. Destination indexes will be rebuilt.', flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--published', default='')
    parser.add_argument('--operator', default='')
    args = parser.parse_args()
    try:
        validate(args.published, args.operator)
    except (ValueError, KeyError, TypeError, OSError, sqlite3.Error, subprocess.SubprocessError) as error:
        parser.exit(1, f'seed preflight failed: {error}\n')
