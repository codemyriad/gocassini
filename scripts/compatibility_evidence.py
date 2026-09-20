#!/usr/bin/env python3
"""Collect compatibility evidence and refuse incomplete release qualification."""

import argparse
import datetime as dt
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import zipfile

from nextcloud_compatibility import (ROOT, INVENTORY, DIGEST, command, read_json, require,
                                    sha, validate, write_json)

WORKFLOW = ".github/workflows/publish-exapp-image.yml"
REQUIRED_JOBS = ("validate-manifest", "build-image", "build-image-arm64", "build-image-cuda",
                 "faithful-installed-exapp-talk-cpu", "smoke", "e2e-container", "e2e-entrypoint",
                 "e2e-install", "e2e-talk-record-cuda", "transcribe-smoke-cuda")
REQUIRED_CHECKS = {"installation", "image_identity", "recording", "transcription", "publication",
                   "participant_access", "outsider_denied", "restart", "embedded_browser"}


def source():
    return {"repository": os.environ.get("GITHUB_REPOSITORY", "local"),
            "sha": os.environ.get("GITHUB_SHA") or command("git", "rev-parse", "HEAD"),
            "ref": os.environ.get("GITHUB_REF", "local"),
            "event": os.environ.get("GITHUB_EVENT_NAME", "local"),
            "workflow": os.environ.get("GITHUB_WORKFLOW_REF", "local").split("@")[0],
            "run_id": os.environ.get("GITHUB_RUN_ID", "local"),
            "attempt": os.environ.get("GITHUB_RUN_ATTEMPT", "1")}


def optional_json(path):
    try:
        return read_json(path)
    except (OSError, ValueError):
        return {}


def collect(log, stack_path, mode, rc, started, inventory=INVENTORY, manifest=None):
    log = Path(log)
    stack = read_json(stack_path)
    summary = optional_json(log / "summary.json")
    observed = optional_json(log / "observed.json")
    browser = optional_json(log / "browser/result.json")
    validator = summary.get("validation", {})
    runs = validator.get("runs", [])
    suite = summary.get("result") == "passed" and summary.get("exit_code") == 0 and validator.get("result") == "passed"
    media = bool(runs) and all(r.get("artifact", {}).get("segment_count", 0) > 0 and
                              r.get("artifact", {}).get("word_count", 0) > 0 for r in runs)
    access = bool(runs) and all(r.get("access", {}).get("participant") is True and
                               r.get("access", {}).get("outsider_denied") is True for r in runs)
    identity = (bool(summary.get("source_image_id")) and summary.get("source_image_id") ==
                summary.get("installed_image_id") == observed.get("cassini", {}).get("config_id"))
    checks = {"installation": suite and bool(observed), "image_identity": identity,
              "recording": suite and summary.get("control", {}).get("recording_performed") is True,
              "transcription": suite and media, "publication": suite and media,
              "participant_access": suite and access, "outsider_denied": suite and access,
              "restart": suite and len(runs) == 2,
              "embedded_browser": browser.get("result") == "passed" and
              all(browser.get("checks", {}).get(k) is True for k in
                  ("login", "embedded_app", "transcript", "recording_playback"))}
    passed = rc == 0 and all(checks.values()) and summary.get("cleanup") == "passed"
    result = "passed" if passed else ("environment-failure" if not observed else "product-failure")
    if rc in (130, 143):
        result = "cancelled"
    return {"schema": "cassini.compatibility.v1", "source": source(), "mode": mode,
            "policy_sha256": sha(inventory), "manifest_sha256": sha(manifest or ROOT / 'appinfo/info.xml'),
            "stack": stack, "observed": observed, "checks": checks, "result": result,
            "exit_code": rc, "started_at": started,
            "finished_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "duration_seconds": max(0, int(time.time() - dt.datetime.fromisoformat(started).timestamp())),
            "run_url": f"https://github.com/{source()['repository']}/actions/runs/{source()['run_id']}",
            "browser": browser, "cleanup": summary.get("cleanup", "not-run")}


def check_records(policy, records, expected_source, policy_hash, manifest_hash, expected_ids=None):
    wanted = {s['id']: s for s in policy['baselines'] if expected_ids is None or s['id'] in expected_ids}
    require(bool(wanted), "empty required compatibility set")
    actual = [r.get('stack', {}).get('id') for r in records]
    require(len(actual) == len(set(actual)), "duplicate compatibility evidence")
    require(set(actual) == set(wanted), f"required baseline evidence mismatch: missing={sorted(set(wanted)-set(actual))}, unexpected={sorted(set(actual)-set(wanted))}")
    for record in records:
        name = record['stack']['id']
        require(record.get('schema') == 'cassini.compatibility.v1', f"{name}: unsupported evidence schema")
        require(record.get('source') == expected_source, f"{name}: different source run, attempt, ref or commit")
        require(record.get('policy_sha256') == policy_hash and record.get('manifest_sha256') == manifest_hash,
                f"{name}: different policy or manifest")
        require(record.get('mode') == 'baseline', f"{name}: canary/preview evidence cannot qualify a release")
        require(record['stack'] == wanted[name], f"{name}: evidence used a different baseline lock")
        require(record.get('result') == 'passed' and record.get('exit_code') == 0 and record.get('cleanup') == 'passed',
                f"{name}: required compatibility run did not pass")
        require(set(record.get('checks', {})) == REQUIRED_CHECKS and all(v is True for v in record['checks'].values()),
                f"{name}: incomplete product assertions")
        observed = record.get('observed', {})
        require(observed.get('nextcloud_version') == wanted[name]['nextcloud_version'], f"{name}: wrong observed server")
        require(observed.get('apps') == {a: l['version'] for a, l in wanted[name]['apps'].items()},
                f"{name}: wrong observed app versions")
        require(set(observed.get('images', {})) == set(wanted[name]['images']), f"{name}: missing observed images")
        for service, ref in wanted[name]['images'].items():
            image = observed['images'][service]
            require(image.get('ref') == ref and DIGEST.fullmatch(image.get('config_id', '')),
                    f"{name}: wrong observed {service} image")
        require(DIGEST.fullmatch(observed.get('cassini', {}).get('config_id', '')), f"{name}: no tested Cassini identity")


def registry_identity(ref):
    manifest = json.loads(command('docker', 'buildx', 'imagetools', 'inspect', ref, '--format', '{{json .Manifest}}'))
    digest = manifest['digest']
    require(DIGEST.fullmatch(digest), 'invalid registry digest')
    repository = ref.split('@')[0].rsplit(':', 1)[0]
    raw = json.loads(command('docker', 'buildx', 'imagetools', 'inspect', repository + '@' + digest, '--raw'))
    platforms = {}
    for item in raw.get('manifests', []):
        p = item.get('platform', {})
        if p.get('os') == 'linux' and p.get('architecture') in ('amd64', 'arm64'):
            child = json.loads(command('docker', 'buildx', 'imagetools', 'inspect', repository + '@' + item['digest'], '--raw'))
            platforms['linux/' + p['architecture']] = {'manifest_digest': item['digest'], 'config_digest': child['config']['digest']}
    if 'config' in raw:
        # CUDA currently publishes one linux/amd64 image, not a manifest list.
        platforms['linux/amd64'] = {'manifest_digest': digest, 'config_digest': raw['config']['digest']}
    require('linux/amd64' in platforms, 'image has no linux/amd64 platform')
    return {'ref': ref, 'digest': digest, 'platforms': platforms}


def build_index(policy, records, jobs, images, src, policy_hash, manifest_hash):
    require(src['event'] == 'push' and src['ref'].startswith('refs/tags/v'), 'release evidence requires a tag-push run')
    require(src['workflow'] == src['repository'] + '/' + WORKFLOW, 'unexpected source workflow')
    for job in REQUIRED_JOBS:
        require(jobs.get(job, {}).get('result') == 'success', f"required job did not pass: {job}")
    check_records(policy, records, src, policy_hash, manifest_hash)
    require(set(images) == {'cpu', 'cuda'}, 'release must identify CPU and CUDA images')
    require(set(images['cpu']['platforms']) == {'linux/amd64', 'linux/arm64'}, 'CPU image must include both platforms')
    for name, job in [('cpu', 'build-image'), ('cuda', 'build-image-cuda')]:
        require(images[name]['digest'] == jobs[job]['outputs']['digest'], f'{name}: registry tag differs from source build')
    tested = images['cpu']['platforms']['linux/amd64']['config_digest']
    require(all(r['observed']['cassini']['config_id'] == tested for r in records), 'tested CPU image differs from release image')
    return {'schema': 'cassini.release-evidence.v1', 'source': src, 'policy_sha256': policy_hash,
            'manifest_sha256': manifest_hash, 'jobs': jobs, 'images': images, 'records': records}


def verify_index(index, policy, repo, tag, commit, run, current_images, policy_hash, manifest_hash):
    require(index.get('schema') == 'cassini.release-evidence.v1', 'unsupported release evidence schema')
    src = index['source']
    require(src['repository'] == repo and src['ref'] == 'refs/tags/' + tag and src['sha'] == commit,
            'release source does not match repository, tag and commit')
    require(run['status'] == 'completed' and run['conclusion'] == 'success', 'source workflow did not complete successfully')
    require(run['event'] == 'push' and run['path'] == WORKFLOW and run['head_sha'] == commit and
            run['head_branch'] == tag and run['head_repository']['full_name'] == repo,
            'source workflow is not the trusted tag-push run')
    require(str(run['id']) == src['run_id'] and str(run['run_attempt']) == src['attempt'], 'run or attempt changed')
    require(index['policy_sha256'] == policy_hash and index['manifest_sha256'] == manifest_hash, 'policy or manifest changed')
    rebuilt = build_index(policy, index['records'], index['jobs'], index['images'], src, policy_hash, manifest_hash)
    require(rebuilt == index, 'unexpected release evidence fields')
    require(current_images == index['images'], 'release images moved after qualification')


def gh(path):
    return json.loads(command('gh', 'api', path))


def current_commit(repo, tag):
    # commits resolves annotated and lightweight tags to the commit SHA.
    from urllib.parse import quote
    return gh(f'repos/{repo}/commits/{quote(tag, safe="")}')['sha']


def await_evidence(repo, tag, commit, out, timeout):
    from urllib.parse import urlencode
    deadline = time.monotonic() + timeout
    selected = None
    while time.monotonic() < deadline:
        require(current_commit(repo, tag) == commit, 'release tag moved while waiting')
        runs = gh(f'repos/{repo}/actions/workflows/publish-exapp-image.yml/runs?' +
                  urlencode({'head_sha': commit, 'event': 'push', 'per_page': 100}))['workflow_runs']
        matches = [r for r in runs if r['head_branch'] == tag and r['path'] == WORKFLOW and r['head_repository']['full_name'] == repo]
        require(len(matches) <= 1, 'ambiguous source tag runs; select/requalify explicitly')
        if matches:
            run = matches[0]
            identity = (run['id'], run['run_attempt'])
            require(selected is None or selected == identity, 'source run attempt changed while waiting')
            selected = identity
            if run['status'] == 'completed':
                require(run['conclusion'] == 'success', f"source image workflow ended {run['conclusion']}")
                artifacts = json.loads(command('gh', 'api', '--paginate', '--slurp',
                    f"repos/{repo}/actions/runs/{run['id']}/artifacts?per_page=100"))
                wanted = f"release-evidence-{run['run_attempt']}"
                found = [a for page in artifacts for a in page['artifacts'] if a['name'] == wanted]
                require(len(found) == 1 and not found[0]['expired'], 'required release evidence is missing or expired')
                body = subprocess.check_output(['gh', 'api', f"repos/{repo}/actions/artifacts/{found[0]['id']}/zip"])
                with zipfile.ZipFile(io.BytesIO(body)) as archive:
                    index = json.loads(archive.read('release-evidence.json'))
                write_json(out, index)
                return index, run
        print('Waiting for exact tagged build and compatibility evidence...', flush=True)
        time.sleep(15)
    raise ValueError('timed out waiting for tagged compatibility evidence')


def render(index):
    lines = ['# Release compatibility evidence', '', f"Commit: `{index['source']['sha']}`", '',
             '| Baseline | Nextcloud | Result |', '|---|---|---|']
    for r in index['records']:
        lines.append(f"| {r['stack']['id']} | {r['observed']['nextcloud_version']} | {r['result']} |")
    lines += ['', 'Exact dependencies, assertions and image identities are in `release-evidence.json`.', '']
    return '\n'.join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    p = sub.add_parser('collect')
    for name in ('log', 'stack', 'started', 'out'):
        p.add_argument('--' + name, required=True)
    p.add_argument('--mode', choices=['baseline', 'canary'], default='baseline')
    p.add_argument('--exit-code', type=int, required=True)
    p.add_argument('--manifest')
    for action in ('aggregate', 'index'):
        p = sub.add_parser(action)
        p.add_argument('--records', required=True)
        p.add_argument('--matrix')
        p.add_argument('--out')
    p = sub.add_parser('verify-release')
    p.add_argument('--repo', required=True)
    p.add_argument('--tag', required=True)
    p.add_argument('--commit', required=True)
    p.add_argument('--out', required=True)
    p.add_argument('--existing', action='store_true')
    p.add_argument('--timeout', type=int, default=5400)
    args = parser.parse_args()
    policy = read_json(INVENTORY)
    validate(policy)
    if args.action == 'collect':
        record = collect(args.log, args.stack, args.mode, args.exit_code, args.started, manifest=args.manifest)
        write_json(args.out, record)
        print(f"{record['stack']['id']}: {record['result']} ({record['duration_seconds']}s)")
        require(record['result'] == 'passed', 'required installed-product assertions did not pass')
    elif args.action in ('aggregate', 'index'):
        records = [read_json(p) for p in Path(args.records).rglob('compatibility.json')]
        ids = [r['baseline'] for r in json.loads(args.matrix)['include']] if args.matrix else None
        check_records(policy, records, source(), sha(INVENTORY), sha(ROOT / 'appinfo/info.xml'), ids)
        if args.action == 'index':
            jobs = json.loads(os.environ['COMPAT_BUILD_JOBS'])
            v = manifest_version()
            images = {k: registry_identity(f"ghcr.io/{source()['repository']}:{v}{suffix}") for k, suffix in [('cpu', ''), ('cuda', '-cuda')]}
            index = build_index(policy, records, jobs, images, source(), sha(INVENTORY), sha(ROOT / 'appinfo/info.xml'))
            write_json(args.out, index)
            Path(args.out).with_suffix('.md').write_text(render(index))
        print('PASS: complete matching compatibility evidence')
    elif args.action == 'verify-release':
        require(command('git', 'rev-parse', 'HEAD') == args.commit, 'checkout differs from release commit')
        require(current_commit(args.repo, args.tag) == args.commit, 'release tag moved')
        if args.existing:
            index = read_json(args.out)
            run = gh(f"repos/{args.repo}/actions/runs/{index['source']['run_id']}")
        else:
            index, run = await_evidence(args.repo, args.tag, args.commit, args.out, args.timeout)
        images = {k: registry_identity(v['ref']) for k, v in index['images'].items()}
        verify_index(index, policy, args.repo, args.tag, args.commit, run, images,
                     sha(INVENTORY), sha(ROOT / 'appinfo/info.xml'))
        require(current_commit(args.repo, args.tag) == args.commit, 'release tag moved during verification')
        Path(args.out).with_suffix('.md').write_text(render(index))
        print('PASS: exact release artifact and complete required evidence verified')


def manifest_version():
    import xml.etree.ElementTree as ET
    return ET.parse(ROOT / 'appinfo/info.xml').getroot().findtext('version')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as exc:
        sys.exit(f'release compatibility blocked: {exc}')
