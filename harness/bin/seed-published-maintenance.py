#!/usr/bin/env python3
"""Restore published seed metadata without racing the installed operator."""
import argparse
import json
import pathlib
import subprocess
import tempfile


MAINTENANCE = """set -eu
/usr/local/bin/cassini-operator import-meeting-metadata --catalog /cassini-seed/catalog.json
/usr/local/bin/cassini-operator backfill-search --strict
/usr/local/bin/cassini-operator backfill-annotations --strict
"""


def docker(*args, capture=False):
    return subprocess.run(
        ['docker', *args], check=True, text=True,
        stdout=subprocess.PIPE if capture else None,
    ).stdout


def restore(container, catalog):
    catalog = pathlib.Path(catalog).resolve(strict=True)
    info = json.loads(docker('inspect', container, capture=True))[0]
    if not info['State']['Running']:
        raise ValueError(f'{container} must be running before seed maintenance')
    networks = list(info['NetworkSettings']['Networks'])
    if not networks:
        raise ValueError(f'{container} has no network for AppAPI archive access')

    # Docker env files avoid exposing the AppAPI secret in command arguments.
    # TemporaryDirectory is private and removed on both success and failure.
    with tempfile.TemporaryDirectory(prefix='cassini-seed-maintenance-') as temp:
        env_file = pathlib.Path(temp) / 'environment'
        env = info['Config']['Env'] or []
        if any('\n' in value or '\r' in value for value in env):
            raise ValueError('container environment cannot be represented in a Docker env file')
        env_file.write_text('\n'.join(env) + '\n')
        env_file.chmod(0o600)
        args = [
            'create', '--volumes-from', container, '--env-file', str(env_file),
            '--network', networks[0],
            '--mount', f'type=bind,src={catalog},dst=/cassini-seed/catalog.json,readonly',
            '--entrypoint', '/bin/sh',
        ]
        for key, flag in [('User', '--user'), ('WorkingDir', '--workdir')]:
            if info['Config'].get(key):
                args.extend([flag, info['Config'][key]])
        # Use the exact deployed image, even if its mutable tag has moved.
        args.extend([info['Image'], '-c', MAINTENANCE])
        helper = docker(*args, capture=True).strip()
        stopped = False
        try:
            for network in networks[1:]:
                docker('network', 'connect', network, helper)
            print(f'[published-seed] stopping {container} for metadata and index maintenance', flush=True)
            # Attempt a restart even if stopping is interrupted after Docker
            # has already delivered the stop request.
            stopped = True
            docker('stop', container, capture=True)
            docker('start', '--attach', helper)
            code = int(docker('inspect', '--format', '{{.State.ExitCode}}', helper, capture=True))
            if code:
                raise subprocess.CalledProcessError(code, ['seed-maintenance'])
        finally:
            # Remove/stop the helper before starting the server, including if
            # the attached Docker client was interrupted while it still ran.
            try:
                docker('rm', '--force', helper, capture=True)
            finally:
                if stopped:
                    print(f'[published-seed] starting {container} after seed maintenance', flush=True)
                    docker('start', container, capture=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', required=True)
    parser.add_argument('--catalog', required=True)
    args = parser.parse_args()
    try:
        restore(args.container, args.catalog)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f'[published-seed] maintenance failed: {error}\n')


if __name__ == '__main__':
    main()
