#!/usr/bin/env python3
"""Version policy, locked stack preparation and observed compatibility evidence.

Uses the standard library so policy checks run before installing build tools.
Runtime operations are deliberately separate from offline validation.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent.parent
INVENTORY = ROOT / "ci/nextcloud-compatibility.json"
SERVICES = {"nextcloud": "NEXTCLOUD_IMAGE", "db": "COMPAT_DB_IMAGE",
            "reverse-proxy": "COMPAT_PROXY_IMAGE", "appapi-harp": "COMPAT_HARP_IMAGE",
            "nats": "COMPAT_NATS_IMAGE", "janus": "COMPAT_JANUS_IMAGE",
            "signaling": "COMPAT_SIGNALING_IMAGE", "coturn": "COMPAT_COTURN_IMAGE"}
APPS = {"spreed", "app_api"}
DIGEST = re.compile(r"sha256:[a-f0-9]{64}")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_json(path):
    return json.loads(Path(path).read_text())


def write_json(path, data):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    Path(path).write_text(json.dumps(data, indent=2, sort_keys=True) + "\n")


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def version(value):
    require(isinstance(value, str) and re.fullmatch(r"\d+\.\d+\.\d+", value),
            f"expected an exact stable x.y.z version: {value}")
    return tuple(map(int, value.split(".")))


def server_version(value):
    # Nextcloud's versionstring preserves its preview label (e.g. "35.0.0 RC1").
    # Baselines still require version(), which accepts stable releases only.
    require(isinstance(value, str) and re.fullmatch(r"\d+\.\d+\.\d+(?: (?:dev|(?:alpha|beta|RC)\d+))?", value),
            f"unrecognized Nextcloud versionstring: {value}")
    return version(value.split()[0])


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def validate_stack(stack):
    require(re.fullmatch(r"[a-z0-9][a-z0-9.-]*", stack["id"]), "invalid stack ID")
    server_version(stack["nextcloud_version"])
    require(set(stack["images"]) == set(SERVICES), "stack must lock every service image")
    for ref in stack["images"].values():
        require(isinstance(ref, str) and "@" in ref and
                DIGEST.fullmatch(ref.rsplit("@", 1)[1]), f"image is not digest-pinned: {ref}")
    require(set(stack["apps"]) == APPS, "stack must lock Talk and AppAPI")
    for app, lock in stack["apps"].items():
        version(lock["version"])
        require(re.fullmatch(r"[a-f0-9]{64}", lock["sha256"]), f"invalid checksum for {app}")
        require(lock["source"] in ("archive", "bundled"), f"invalid source for {app}")
        if lock["source"] == "archive":
            require(lock["url"].startswith("https://"), f"archive must use HTTPS: {app}")


def validate(policy, manifest=None):
    require(policy["schema"] == 1, "unsupported inventory schema")
    floor = version(policy["minimum"])
    ceiling = policy["maximum_major"]
    require(type(ceiling) is int and ceiling >= floor[0], "invalid support bounds")
    stacks = policy["baselines"]
    require(bool(stacks), "no required baselines")
    for stack in stacks:
        validate_stack(stack)
        v = version(stack["nextcloud_version"])
        require(floor <= v and v[0] <= ceiling, "baseline outside advertised bounds")
    ids = [s["id"] for s in stacks]
    require(len(ids) == len(set(ids)), "duplicate baseline ID")
    require(policy["reference"] in ids, "reference is not a required baseline")
    require(policy["minimum"] in [s["nextcloud_version"] for s in stacks],
            "exact advertised minimum has no required baseline")
    require({version(s["nextcloud_version"])[0] for s in stacks} == set(range(floor[0], ceiling + 1)),
            "advertised major has no required baseline (or range has a hole)")
    previews = policy["previews"]
    for preview in previews:
        require(type(preview["major"]) is int and preview["major"] > ceiling,
                "preview must be outside advertised support")
        require(isinstance(preview["image"], str) and preview["image"].startswith("nextcloud:"),
                "preview must specify a Nextcloud image")
    require(len({p['major'] for p in previews}) == len(previews), "duplicate preview major")
    require(DIGEST.fullmatch(policy["canary_image"].rsplit("@", 1)[-1]),
            "canary Cassini artifact must be frozen by digest")
    if manifest:
        nc = ET.parse(manifest).getroot().find("dependencies/nextcloud")
        require(nc is not None and nc.get("min-version") == policy["minimum"] and
                nc.get("max-version") == str(ceiling), "manifest support bounds differ from inventory")


def select(policy, tier, paths=()):
    all_versions = tier != "pr" or any(p.startswith(("ci/", ".github/", "harness/", "appinfo/",
        "scripts/", "deployment/", "cassini-operator/", "cassini-go-recorder/internal/nextcloud/",
        "cassini-go-recorder/internal/talk/", "cassini-app/")) for p in paths)
    return [s for s in policy["baselines"] if all_versions or s["id"] == policy["reference"]
            or s["nextcloud_version"] == policy["minimum"]]


def support_table(policy):
    rows = ["<!-- Generated by scripts/nextcloud_compatibility.py docs; do not edit. -->",
            f"Minimum Nextcloud: **{policy['minimum']}**. Maximum major: **{policy['maximum_major']}**.",
            "", "| Baseline | Nextcloud | Role |", "|---|---|---|"]
    for s in policy["baselines"]:
        roles = []
        if s["nextcloud_version"] == policy["minimum"]:
            roles.append("exact minimum")
        if s["id"] == policy["reference"]:
            roles.append("reference")
        rows.append(f"| {s['id']} | {s['nextcloud_version']} | {', '.join(roles) or 'required'} |")
    return "\n".join(rows) + "\n"


def prepare(stack, out):
    validate_stack(stack)
    out = Path(out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    write_json(out / "stack.json", stack)
    values = {SERVICES[k]: v for k, v in stack["images"].items()}
    values["CASSINI_COMPAT_LOCK"] = str(out / "stack.json")
    (out / "env.sh").write_text("".join(f"export {k}={shlex.quote(v)}\n" for k, v in values.items()))


def download(url, destination):
    with urllib.request.urlopen(url, timeout=90) as response, open(destination, "wb") as target:
        while chunk := response.read(1024 * 1024):
            target.write(chunk)


def compose(project, *args):
    return ["docker", "compose", "-p", project, "-f", str(ROOT / "harness/compose.yml"), *args]


def occ(project, *args):
    return command(*compose(project, "exec", "-T", "-u", "www-data", "nextcloud", "php", "occ", *args))


def install_app(stack, project, app):
    require(os.environ.get("CASSINI_COMPAT_FIXTURE") == "1", "app replacement requires a disposable compatibility fixture")
    require(app in APPS, "unknown locked app")
    lock = stack["apps"][app]
    if lock["source"] == "bundled":
        raw = subprocess.check_output(compose(project, "exec", "-T", "nextcloud", "cat",
                                             f"/var/www/html/apps/{app}/appinfo/info.xml"))
        require(hashlib.sha256(raw).hexdigest() == lock["sha256"], f"bundled {app} bytes differ from lock")
        require(ET.fromstring(raw).findtext("version") == lock["version"], f"bundled {app} version differs")
    else:
        with tempfile.TemporaryDirectory(prefix="cassini-compat-app-") as tmp:
            archive = Path(tmp) / "app.tar.gz"
            download(lock["url"], archive)
            require(sha(archive) == lock["sha256"], f"{app} archive checksum mismatch")
            with tarfile.open(archive) as tf:
                require(all(Path(m.name).parts and Path(m.name).parts[0] == app for m in tf.getmembers()),
                        f"{app} archive has unexpected root")
                tf.extractall(Path(tmp) / "unpacked", filter="data")
            folder = Path(tmp) / "unpacked" / app
            require(ET.parse(folder / "appinfo/info.xml").getroot().findtext("version") == lock["version"],
                    f"{app} archive version mismatch")
            # Only used in the explicitly reset, disposable compatibility fixture.
            subprocess.run(compose(project, "exec", "-T", "-u", "www-data", "nextcloud", "php", "occ",
                                   "app:disable", app), stdout=subprocess.DEVNULL, check=False)
            subprocess.run(compose(project, "exec", "-T", "nextcloud", "rm", "-rf",
                                   f"/var/www/html/apps/{app}", f"/var/www/html/custom_apps/{app}"), check=True)
            subprocess.run(compose(project, "cp", str(folder), f"nextcloud:/var/www/html/custom_apps/{app}"), check=True)
            subprocess.run(compose(project, "exec", "-T", "nextcloud", "chown", "-R", "www-data:www-data",
                                   f"/var/www/html/custom_apps/{app}"), check=True)
    print(occ(project, "app:enable", app))
    apps = json.loads(occ(project, "app:list", "--output=json"))
    require(apps["enabled"].get(app) == lock["version"], f"enabled {app} is not the locked version")


def image_identity(ref):
    # Metadata only: do not emit container environment variables or registry credentials.
    data = json.loads(command("docker", "image", "inspect", ref))[0]
    return {"ref": ref, "config_id": data["Id"], "repo_digests": data.get("RepoDigests", []),
            "architecture": data["Architecture"], "os": data["Os"],
            "revision": (data.get("Config", {}).get("Labels") or {}).get("org.opencontainers.image.revision", "")}


def observe(stack, project, image):
    status = json.loads(occ(project, "status", "--output=json"))
    apps = json.loads(occ(project, "app:list", "--output=json"))["enabled"]
    require(status["versionstring"] == stack["nextcloud_version"], "running Nextcloud differs from lock")
    for app, lock in stack["apps"].items():
        require(apps.get(app) == lock["version"], f"running {app} differs from lock")
    observed_images = {}
    for service, ref in stack["images"].items():
        container = command(*compose(project, "ps", "-q", service))
        require(bool(container), f"missing running service {service}")
        running = command("docker", "inspect", container, "--format", "{{.Image}}")
        identity = image_identity(ref)
        require(running == identity["config_id"], f"running {service} differs from locked image")
        observed_images[service] = identity
    return {"nextcloud_version": status["versionstring"], "apps": {a: apps[a] for a in sorted(APPS)},
            "images": observed_images, "cassini": image_identity(image)}


def resolve_image(ref):
    data = json.loads(command("docker", "buildx", "imagetools", "inspect", ref, "--format", "{{json .Manifest}}"))
    return ref.split("@")[0] + "@" + data["digest"]


def satisfies(v, spec):
    # App-store platformVersionSpec is normalized to numeric comparison terms.
    # Unknown syntax is refused rather than accidentally selecting an incompatible app.
    ops = {">=": lambda a, b: a >= b, "<=": lambda a, b: a <= b,
           ">": lambda a, b: a > b, "<": lambda a, b: a < b, "=": lambda a, b: a == b}
    for term in spec.split():
        match = re.fullmatch(r"(>=|<=|>|<|=)(\d+\.\d+\.\d+)", term)
        if not match or not ops[match[1]](version(v), version(match[2])):
            return False
    return bool(spec)


def resolve_apps(nc_version):
    url = f"https://apps.nextcloud.com/api/v1/platform/{nc_version}/apps.json"
    with urllib.request.urlopen(url, timeout=90) as response:
        catalog = json.load(response)
    locks = {}
    for app in sorted(APPS - {"app_api"}):
        record = next((a for a in catalog if a["id"] == app), None)
        require(record is not None, f"no app-store listing for {app} on {nc_version}")
        candidates = [r for r in record["releases"] if not r["isNightly"] and
                      re.fullmatch(r"\d+\.\d+\.\d+", r["version"]) and
                      satisfies(nc_version, r["platformVersionSpec"])]
        require(bool(candidates), f"no stable {app} compatible with {nc_version}")
        release = max(candidates, key=lambda r: version(r["version"]))
        with tempfile.TemporaryDirectory() as tmp:
            dest = Path(tmp) / "app.tar.gz"
            download(release["download"], dest)
            locks[app] = {"version": release["version"], "source": "archive",
                          "url": release["download"], "sha256": sha(dest)}
    return locks


def resolve_candidate(policy, major, image=None):
    ref = resolve_image(image or f"nextcloud:{major}")
    subprocess.run(["docker", "pull", ref], check=True, stdout=sys.stderr)
    container = command("docker", "create", ref)
    try:
        with tempfile.TemporaryDirectory() as tmp:
            server = Path(tmp) / 'version.php'
            subprocess.run(['docker', 'cp', f'{container}:/usr/src/nextcloud/version.php', str(server)], check=True)
            match = re.search(r"\$OC_VersionString\s*=\s*'([^'\n]+)'", server.read_text())
            require(match is not None, 'candidate image has no Nextcloud versionstring')
            nc_version = match[1]
            require(server_version(nc_version)[0] == major, "candidate image resolved to the wrong major")
            path = Path(tmp) / "info.xml"
            subprocess.run(["docker", "cp", f"{container}:/usr/src/nextcloud/apps/app_api/appinfo/info.xml", str(path)], check=True)
            app_api = {"source": "bundled", "version": ET.parse(path).getroot().findtext("version"), "sha256": sha(path)}
    finally:
        subprocess.run(["docker", "rm", "-v", container], check=True, stdout=sys.stderr)
    stack = json.loads(json.dumps(next(s for s in policy["baselines"] if s['id'] == policy['reference'])))
    stack.update(id=f"canary-{major}", nextcloud_version=nc_version)
    stack["images"]["nextcloud"] = ref
    # Hold infrastructure fixed; isolate Nextcloud and its app release train.
    stack["apps"] = {**resolve_apps(nc_version.split()[0]), "app_api": app_api}
    validate_stack(stack)
    return stack


def candidate_changes(policy, stack):
    major = server_version(stack['nextcloud_version'])[0]
    same_major = [s for s in policy['baselines'] if version(s['nextcloud_version'])[0] == major]
    baseline = max(same_major, key=lambda s: version(s['nextcloud_version'])) if same_major else next(
        s for s in policy['baselines'] if s['id'] == policy['reference'])
    changes = []

    def compare(component, before, after):
        if before != after:
            changes.append({'component': component, 'baseline': before, 'candidate': after})

    compare('Nextcloud version', baseline['nextcloud_version'], stack['nextcloud_version'])
    for app in sorted(APPS):
        for field in ('version', 'sha256', 'source'):
            compare(f'{app} {field}', baseline['apps'][app][field], stack['apps'][app][field])
    for service in sorted(SERVICES):
        # Floating and exact tags can point at identical bytes; compare the digest.
        compare(f'{service} image', baseline['images'][service].rsplit('@', 1)[1],
                stack['images'][service].rsplit('@', 1)[1])
    return {'baseline': baseline['id'], 'candidate': stack['id'], 'changes': changes}


def render_changes(report):
    lines = [f"Compared with baseline `{report['baseline']}`:", '']
    if report['changes']:
        lines += ['| Component | Baseline | Candidate |', '|---|---|---|']
        lines += [f"| {c['component']} | `{c['baseline']}` | `{c['candidate']}` |" for c in report['changes']]
    else:
        lines.append('No server, app or image changes from the locked baseline.')
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", default=str(INVENTORY))
    sub = parser.add_subparsers(dest="action", required=True)
    check = sub.add_parser("validate")
    check.add_argument("--manifest", default=str(ROOT / "appinfo/info.xml"))
    check.add_argument("--docs", default=str(ROOT / "docs/nextcloud-support-table.md"))
    sub.add_parser("docs")
    matrix = sub.add_parser("matrix")
    matrix.add_argument("--tier", choices=["pr", "all", "recorder", "canary"], default="all")
    matrix.add_argument("--paths-file")
    prep = sub.add_parser("prepare")
    prep.add_argument("--baseline")
    prep.add_argument("--stack")
    prep.add_argument("--out", required=True)
    for action in ("install-app", "observe"):
        p = sub.add_parser(action)
        p.add_argument("--stack", required=True)
        p.add_argument("--project", default="spreedtest")
        if action == "install-app":
            p.add_argument("app", choices=sorted(APPS))
        else:
            p.add_argument("--image", required=True)
            p.add_argument("--out", required=True)
    resolve = sub.add_parser("resolve")
    resolve.add_argument("--major", type=int, required=True)
    resolve.add_argument("--image")
    resolve.add_argument("--out", required=True)
    args = parser.parse_args()
    policy = read_json(args.inventory)
    validate(policy)
    if args.action == "validate":
        validate(policy, args.manifest)
        require(Path(args.docs).read_text() == support_table(policy), "generated support table is stale; run docs command")
        reference = next(s for s in policy['baselines'] if s['id'] == policy['reference'])
        default = re.search(r'\$\{NEXTCLOUD_IMAGE:-([^}]+)\}', (ROOT / 'harness/compose.yml').read_text())
        require(default is not None and default[1] == f"nextcloud:{reference['nextcloud_version']}",
                'local Compose default differs from inventory reference')
        print("PASS: manifest, exact minimum, locked baselines and support table agree")
    elif args.action == "docs":
        print(support_table(policy), end="")
    elif args.action == "matrix":
        paths = Path(args.paths_file).read_text().splitlines() if args.paths_file else []
        if args.tier == "canary":
            rows = [{"major": m, "image": f"nextcloud:{m}"} for m in range(version(policy['minimum'])[0], policy['maximum_major'] + 1)] + policy['previews']
        elif args.tier == "recorder":
            reference = next(s for s in policy['baselines'] if s['id'] == policy['reference'])
            oldest = next(s for s in policy['baselines'] if s['nextcloud_version'] == policy['minimum'])
            rows = [{"script": script, "nextcloud_image": reference['images']['nextcloud']} for script in
                    ('ci-e2e.sh', 'ci-e2e-private.sh', 'ci-e2e-rejoin.sh')]
            if oldest['id'] != reference['id']:
                rows.append({"script": "ci-e2e.sh", "nextcloud_image": oldest['images']['nextcloud']})
        else:
            rows = [{"baseline": s['id']} for s in select(policy, args.tier, paths)]
        print(json.dumps({"include": rows}, separators=(",", ":")))
    elif args.action == "prepare":
        stack = read_json(args.stack) if args.stack else next(s for s in policy['baselines'] if s['id'] == args.baseline)
        prepare(stack, args.out)
    elif args.action == "install-app":
        stack = read_json(args.stack)
        validate_stack(stack)
        install_app(stack, args.project, args.app)
    elif args.action == "observe":
        write_json(args.out, observe(read_json(args.stack), args.project, args.image))
    elif args.action == "resolve":
        stack = resolve_candidate(policy, args.major, args.image)
        write_json(args.out, stack)
        changes = candidate_changes(policy, stack)
        write_json(Path(args.out).with_suffix('.changes.json'), changes)
        Path(args.out).with_suffix('.changes.md').write_text(render_changes(changes))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, StopIteration, OSError, subprocess.CalledProcessError) as exc:
        sys.exit(f"compatibility: {exc}")
