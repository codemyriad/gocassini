#!/usr/bin/env python3
"""Replay every audio track through a compiled Cassini boundary benchmark."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import time


def digest(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def atomic_json(path, value):
    temp = path.with_suffix(path.suffix + '.tmp')
    with open(temp, 'w') as f:
        json.dump(value, f, indent=2)
        f.write('\n')
        f.flush()
        os.fsync(f.fileno())
    os.chmod(temp, 0o600)
    os.replace(temp, path)


def library_hashes(directory):
    root = Path(directory).resolve()
    return {str(p.relative_to(root)): digest(p) for p in sorted(root.rglob('*.so*')) if p.is_file()}


def completed_rows(path, expected):
    if not path.exists():
        return 0
    ids = []
    with open(path) as f:
        for line in f:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                break
            ids.append(row['fixture'])
    if len(ids) != len(set(ids)) or any(i not in expected for i in ids):
        raise ValueError('unexpected or duplicated fixture rows')
    return len(ids)


def run(args):
    os.umask(0o077)
    out = Path(args.output_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    binary = Path(args.test_binary).resolve()
    ffprobe = Path(args.ffmpeg_bin).resolve() / 'ffprobe'
    manifest = json.loads(Path(args.manifest).read_text())
    if not isinstance(manifest, list) or not manifest:
        raise ValueError('manifest must be a nonempty array')
    seen = set()
    inputs = []
    for meeting in manifest:
        mid = meeting['id']
        if not isinstance(mid, str) or not mid or mid in seen:
            raise ValueError('meeting IDs must be nonempty and unique')
        seen.add(mid)
        if bool(meeting.get('mkvPath')) == bool(meeting.get('remoteMkvPath')):
            raise ValueError(f'{mid}: specify exactly one MKV path')
        if 'streamIndices' in meeting:
            selected = meeting['streamIndices']
            if (not isinstance(selected, list) or not selected or
                    any(type(i) is not int or i < 0 for i in selected) or len(set(selected)) != len(selected)):
                raise ValueError(f'{mid}: streamIndices must be nonempty unique nonnegative integers')
        if meeting.get('remoteMkvPath') and not args.ssh_host:
            raise ValueError('remoteMkvPath requires --ssh-host')
        if not isinstance(meeting.get('expectedDurationMs'), (int, float)) or meeting['expectedDurationMs'] <= 0:
            raise ValueError(f'{mid}: expectedDurationMs must be positive')
        if 'configuredVocabulary' in meeting and (not isinstance(meeting['configuredVocabulary'], list) or any(not isinstance(x, str) for x in meeting['configuredVocabulary'])):
            raise ValueError(f'{mid}: configuredVocabulary must be a list of strings')
        if 'deriveParticipantHints' in meeting and type(meeting['deriveParticipantHints']) is not bool:
            raise ValueError(f'{mid}: deriveParticipantHints must be boolean')
        if meeting.get('hotwordsFiles') and (meeting.get('configuredVocabulary') or meeting.get('deriveParticipantHints') is True):
            raise ValueError(f'{mid}: manual hotwordsFiles conflict with automatic/configured vocabulary')
        item = {'id': mid, 'hotwords': {str(k): digest(v) for k, v in meeting.get('hotwordsFiles', {}).items()}}
        if meeting.get('mkvPath'):
            st = Path(meeting['mkvPath']).stat()
            item['localFile'] = [str(Path(meeting['mkvPath']).resolve()), st.st_size, st.st_mtime_ns]
        inputs.append(item)
    config = {
        'schema': 1, 'profile': args.profile, 'manifestSha256': digest(args.manifest),
        'binarySha256': digest(binary), 'runtimeHashes': library_hashes(args.runtime_lib),
        'ffprobeSha256': digest(ffprobe), 'ffmpegSha256': digest(Path(args.ffmpeg_bin) / 'ffmpeg'),
        'ffmpegLibHashes': library_hashes(args.ffmpeg_lib),
        'cudaLibs': [str(Path(p).resolve()) for p in args.cuda_lib],
        'modelRoot': str(Path(args.model_root).resolve()), 'model': args.model,
        'sshHost': args.ssh_host, 'sshSudo': args.ssh_sudo, 'inputs': inputs,
        'hintEnvironment': {key: os.environ.get(key, '') for key in ('CASSINI_STT_HINTS_DISABLED', 'CASSINI_STT_HINTS_SCORE')},
    }
    if not config['runtimeHashes']:
        raise ValueError('--runtime-lib contains no shared libraries')
    fingerprint = hashlib.sha256(json.dumps(config, sort_keys=True).encode()).hexdigest()
    config['fingerprint'] = fingerprint
    config_path = out / 'configuration.json'
    if config_path.exists() and json.loads(config_path.read_text()) != config:
        raise ValueError('output directory configuration differs; choose a new output directory')
    atomic_json(config_path, config)
    env = {k: v for k, v in os.environ.items() if not k.startswith('CASSINI_BOUNDARY_')}
    env.update({
        'PATH': str(Path(args.ffmpeg_bin).resolve()) + os.pathsep + env.get('PATH', ''),
        'LD_LIBRARY_PATH': os.pathsep.join(str(Path(p).resolve()) for p in [args.runtime_lib, args.ffmpeg_lib, *args.cuda_lib]),
        'CASSINI_BOUNDARY_PIPELINE': args.profile, 'CASSINI_BOUNDARY_SKIP_WARMUP': '1',
        'CASSINI_DISALLOW_MODEL_DOWNLOAD': '1',
        'CASSINI_BOUNDARY_DEVICE': 'none' if args.profile == 'audio-audit' else 'cuda', 'CASSINI_BOUNDARY_MODEL': args.model,
        'CASSINI_BUNDLED_MODEL_ROOT': str(Path(args.model_root).resolve()),
        'CASSINI_BUNDLED_MODELS': args.model, 'CASSINI_CACHE_ROOT': str(Path(args.model_root).resolve()),
    })
    failures = 0
    for meeting in manifest:
        mid = meeting['id']
        folder = out / hashlib.sha256(mid.encode()).hexdigest()[:24]
        folder.mkdir(exist_ok=True)
        status_path = folder / 'status.json'
        result_path = folder / 'results.jsonl'
        if status_path.exists():
            previous = json.loads(status_path.read_text())
            if previous.get('state') == 'complete' and previous.get('fingerprint') == fingerprint:
                if previous.get('resultsSha256') == digest(result_path):
                    print(f'{mid}: already complete', flush=True)
                    continue
                raise ValueError(f'{mid}: completed results changed')
        started = time.monotonic()
        status = {'id': mid, 'fingerprint': fingerprint, 'state': 'running',
                  'expectedDurationMs': meeting['expectedDurationMs'], 'expectedTracks': None,
                  'completedTracks': 0, 'errors': [], 'elapsedSeconds': 0}
        atomic_json(status_path, status)
        spool = folder / 'recording.mkv'
        expected = []
        try:
            with open(folder / 'run.log', 'wb') as log:
                if meeting.get('remoteMkvPath'):
                    command = ('sudo -n ' if args.ssh_sudo else '') + 'cat -- ' + shlex.quote(meeting['remoteMkvPath'])
                    with open(spool, 'wb') as target:
                        subprocess.run(['ssh', '--', args.ssh_host, command], stdout=target, stderr=log, check=True)
                    mkv = spool
                else:
                    mkv = Path(meeting['mkvPath']).resolve()
                probe = subprocess.run([str(ffprobe), '-v', 'error', '-show_streams', '-show_format', '-of', 'json', str(mkv)], env=env, stdout=subprocess.PIPE, stderr=log, check=True)
                metadata = json.loads(probe.stdout)
                atomic_json(folder / 'probe.json', metadata)
                streams = [s for s in metadata['streams'] if s['codec_type'] == 'audio']
                if not streams:
                    raise ValueError('recording has no audio streams')
                status['availableTracks'] = len(streams)
                status['availableStreamIndices'] = [s['index'] for s in streams]
                if 'streamIndices' in meeting:
                    selected = set(meeting['streamIndices'])
                    if not selected.issubset(status['availableStreamIndices']):
                        raise ValueError('streamIndices contains absent or non-audio stream indices')
                    streams = [s for s in streams if s['index'] in selected]
                status['targetedReplay'] = 'streamIndices' in meeting
                fixtures = []
                for stream in streams:
                    fixture = {'id': f'{mid}/stream-{stream["index"]}', 'mkvPath': str(mkv), 'streamIndex': stream['index']}
                    hotwords = meeting.get('hotwordsFiles', {}).get(str(stream['index']))
                    if hotwords:
                        fixture['hotwordsFile'] = str(Path(hotwords).resolve())
                    elif args.profile == 'production':
                        fixture['deriveParticipantHints'] = meeting.get('deriveParticipantHints', True)
                        fixture['configuredVocabulary'] = meeting.get('configuredVocabulary', [])
                    fixtures.append(fixture)
                expected = [f['id'] for f in fixtures]
                status.update(expectedTracks=len(fixtures), streamIndices=[s['index'] for s in streams],
                              probedDurationMs=float(metadata.get('format', {}).get('duration', 0)) * 1000)
                atomic_json(status_path, status)
                corpus_path = folder / 'corpus.json'
                atomic_json(corpus_path, fixtures)
                # Retry incomplete meetings in full; never append to partial inference.
                if result_path.exists():
                    result_path.replace(folder / 'previous-incomplete-results.jsonl')
                meeting_env = dict(env, CASSINI_BOUNDARY_CORPUS=str(corpus_path), CASSINI_BOUNDARY_OUTPUT=str(result_path))
                command = [str(binary), '-test.run=^TestRecordedBoundaryBenchmark$', '-test.v', '-test.count=1', '-test.timeout=' + args.timeout]
                with subprocess.Popen(command, env=meeting_env, stdout=log, stderr=subprocess.STDOUT) as process:
                    while process.poll() is None:
                        status['completedTracks'] = completed_rows(result_path, expected)
                        status['elapsedSeconds'] = time.monotonic() - started
                        atomic_json(status_path, status)
                        time.sleep(5)
                    if process.returncode:
                        raise subprocess.CalledProcessError(process.returncode, command)
                status['completedTracks'] = completed_rows(result_path, expected)
                if status['completedTracks'] != len(expected):
                    raise ValueError('benchmark did not produce every audio track')
                status.update(state='complete', resultsSha256=digest(result_path))
                if spool.exists():
                    spool.unlink()
        except Exception as exc:
            failures += 1
            status['state'] = 'failed'
            status['errors'].append(str(exc))
            try:
                status['completedTracks'] = completed_rows(result_path, expected)
            except Exception as row_error:
                status['errors'].append(str(row_error))
            # Keep results/logs but bound recording storage even after failures.
            if spool.exists():
                spool.unlink()
        finally:
            status['elapsedSeconds'] = time.monotonic() - started
            atomic_json(status_path, status)
        print(f'{mid}: {status["state"]}, {status["completedTracks"]}/{status["expectedTracks"]} tracks', flush=True)
    return bool(failures)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for flag in ('manifest', 'test-binary', 'runtime-lib', 'ffmpeg-bin', 'ffmpeg-lib', 'model-root', 'output-dir'):
        parser.add_argument('--' + flag, required=True)
    parser.add_argument('--profile', choices=('legacy', 'production', 'audio-audit'), required=True)
    parser.add_argument('--cuda-lib', action='append', default=[])
    parser.add_argument('--ssh-host')
    parser.add_argument('--ssh-sudo', action='store_true')
    parser.add_argument('--model', default='parakeet-tdt-0.6b-v3')
    parser.add_argument('--timeout', default='12h')
    args = parser.parse_args()
    try:
        return run(args)
    except (OSError, ValueError, KeyError) as exc:
        parser.error(str(exc))


if __name__ == '__main__':
    raise SystemExit(main())
