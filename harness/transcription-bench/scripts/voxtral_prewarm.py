#!/usr/bin/env python3
"""Render a public, role-specific Vast onstart prewarm program.

Rendering is deliberately local and deterministic: it reads neither the
environment nor the network.  The generated program downloads public model
artifacts only and never receives benchmark fixtures or service credentials.
"""

from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import json
import sys
from dataclasses import dataclass
from pathlib import PurePosixPath
from typing import Final


PREWARM_ROOT: Final = "/workspace/.cassini-prewarm"
MAX_ONSTART_BYTES: Final = 12 * 1024
READY_SCHEMA: Final = "gocassini.voxtral-prewarm-ready.v1"
FAILED_SCHEMA: Final = "gocassini.voxtral-prewarm-failed.v1"
CONFIG_SCHEMA: Final = "gocassini.voxtral-prewarm-config.v1"
PREWARM_SCHEMA: Final = "gocassini.voxtral-prewarm.v1"


@dataclass(frozen=True)
class _Role:
    model_id: str
    revision: str
    dependencies: tuple[str, ...]
    package_versions: tuple[tuple[str, str], ...]
    imports: tuple[str, ...]
    # Exact public files at the pinned commit.  File sizes let the remote
    # validator detect interrupted or otherwise incomplete cached snapshots.
    model_files: tuple[tuple[str, int], ...]


_COMMON_IMPORTS = ("accelerate", "librosa", "mistral_common", "soundfile", "transformers")

_ROLES: Final[dict[str, _Role]] = {
    "offline": _Role(
        model_id="mistralai/Voxtral-Mini-3B-2507",
        revision="3060fe34b35ba5d44202ce9ff3c097642914f8f3",
        dependencies=(
            "accelerate==1.14.0",
            "annotated-types==0.8.0",
            "audioread==3.1.0",
            "hf-xet==1.6.0",
            "huggingface-hub==0.36.2",
            "joblib==1.5.3",
            "lazy-loader==0.5",
            "librosa==0.11.0",
            "llvmlite==0.49.0",
            "mistral-common[audio]==1.11.7",
            "narwhals==2.25.0",
            "numba==0.67.0",
            "pooch==1.9.0",
            "pycountry==26.2.16",
            "pydantic==2.13.4",
            "pydantic-core==2.46.4",
            "pydantic-extra-types==2.11.1",
            "regex==2026.7.19",
            "safetensors==0.8.0",
            "scikit-learn==1.9.0",
            "scipy==1.17.1",
            "sentencepiece==0.2.2",
            "soundfile==0.14.0",
            "soxr==1.1.0",
            "threadpoolctl==3.6.0",
            "tiktoken==0.14.0",
            "tokenizers==0.22.2",
            "transformers==4.57.6",
            "typing-extensions==4.16.0",
            "typing-inspection==0.4.4",
        ),
        package_versions=(
            ("accelerate", "1.14.0"),
            ("annotated-types", "0.8.0"),
            ("audioread", "3.1.0"),
            ("hf-xet", "1.6.0"),
            ("huggingface-hub", "0.36.2"),
            ("joblib", "1.5.3"),
            ("lazy-loader", "0.5"),
            ("librosa", "0.11.0"),
            ("llvmlite", "0.49.0"),
            ("mistral-common", "1.11.7"),
            ("narwhals", "2.25.0"),
            ("numba", "0.67.0"),
            ("pooch", "1.9.0"),
            ("pycountry", "26.2.16"),
            ("pydantic", "2.13.4"),
            ("pydantic-core", "2.46.4"),
            ("pydantic-extra-types", "2.11.1"),
            ("regex", "2026.7.19"),
            ("safetensors", "0.8.0"),
            ("scikit-learn", "1.9.0"),
            ("scipy", "1.17.1"),
            ("sentencepiece", "0.2.2"),
            ("soundfile", "0.14.0"),
            ("soxr", "1.1.0"),
            ("threadpoolctl", "3.6.0"),
            ("tiktoken", "0.14.0"),
            ("tokenizers", "0.22.2"),
            ("transformers", "4.57.6"),
            ("typing-extensions", "4.16.0"),
            ("typing-inspection", "0.4.4"),
        ),
        imports=_COMMON_IMPORTS + ("sentencepiece",),
        model_files=(
            ("config.json", 1361),
            ("generation_config.json", 108),
            ("model-00001-of-00002.safetensors", 4_977_385_976),
            ("model-00002-of-00002.safetensors", 4_379_088_336),
            ("model.safetensors.index.json", 68_088),
            ("params.json", 731),
            ("preprocessor_config.json", 357),
            ("tekken.json", 14_894_206),
        ),
    ),
    "realtime": _Role(
        model_id="mistralai/Voxtral-Mini-4B-Realtime-2602",
        revision="2769294da9567371363522aac9bbcfdd19447add",
        dependencies=(
            "accelerate==1.14.0",
            "annotated-doc==0.0.5",
            "annotated-types==0.8.0",
            "anyio==4.14.2",
            "audioread==3.1.0",
            "click==8.5.0",
            "h11==0.16.0",
            "hf-xet==1.6.0",
            "httpcore==1.0.9",
            "httpx==0.28.1",
            "huggingface-hub==1.29.0",
            "joblib==1.5.3",
            "lazy-loader==0.5",
            "librosa==0.11.0",
            "llvmlite==0.49.0",
            "markdown-it-py==4.2.0",
            "mdurl==0.1.2",
            "mistral-common[audio]==1.11.7",
            "narwhals==2.25.0",
            "numba==0.67.0",
            "pooch==1.9.0",
            "pycountry==26.2.16",
            "pydantic==2.13.4",
            "pydantic-core==2.46.4",
            "pydantic-extra-types==2.11.1",
            "regex==2026.7.19",
            "rich==15.0.0",
            "safetensors==0.8.0",
            "scikit-learn==1.9.0",
            "scipy==1.17.1",
            "shellingham==1.5.4",
            "soundfile==0.14.0",
            "soxr==1.1.0",
            "threadpoolctl==3.6.0",
            "tiktoken==0.14.0",
            "tokenizers==0.23.1",
            "transformers==5.16.1",
            "typer==0.27.1",
            "typing-extensions==4.16.0",
            "typing-inspection==0.4.4",
        ),
        package_versions=(
            ("accelerate", "1.14.0"),
            ("annotated-doc", "0.0.5"),
            ("annotated-types", "0.8.0"),
            ("anyio", "4.14.2"),
            ("audioread", "3.1.0"),
            ("click", "8.5.0"),
            ("h11", "0.16.0"),
            ("hf-xet", "1.6.0"),
            ("httpcore", "1.0.9"),
            ("httpx", "0.28.1"),
            ("huggingface-hub", "1.29.0"),
            ("joblib", "1.5.3"),
            ("lazy-loader", "0.5"),
            ("librosa", "0.11.0"),
            ("llvmlite", "0.49.0"),
            ("markdown-it-py", "4.2.0"),
            ("mdurl", "0.1.2"),
            ("mistral-common", "1.11.7"),
            ("narwhals", "2.25.0"),
            ("numba", "0.67.0"),
            ("pooch", "1.9.0"),
            ("pycountry", "26.2.16"),
            ("pydantic", "2.13.4"),
            ("pydantic-core", "2.46.4"),
            ("pydantic-extra-types", "2.11.1"),
            ("regex", "2026.7.19"),
            ("rich", "15.0.0"),
            ("safetensors", "0.8.0"),
            ("scikit-learn", "1.9.0"),
            ("scipy", "1.17.1"),
            ("shellingham", "1.5.4"),
            ("soundfile", "0.14.0"),
            ("soxr", "1.1.0"),
            ("threadpoolctl", "3.6.0"),
            ("tiktoken", "0.14.0"),
            ("tokenizers", "0.23.1"),
            ("transformers", "5.16.1"),
            ("typer", "0.27.1"),
            ("typing-extensions", "4.16.0"),
            ("typing-inspection", "0.4.4"),
        ),
        imports=_COMMON_IMPORTS,
        model_files=(
            ("config.json", 1572),
            ("generation_config.json", 191),
            ("model.safetensors", 8_859_446_848),
            ("params.json", 1343),
            ("processor_config.json", 384),
            ("tekken.json", 14_910_348),
        ),
    ),
}


@dataclass(frozen=True)
class PrewarmSpec:
    """All local and remote coordinates needed by the matrix lifecycle."""

    schema: str
    role: str
    script: str
    fingerprint: str
    ready_path: str
    failed_path: str
    log_path: str
    python_path: str
    snapshot_path: str
    hf_home: str
    model_id: str
    model_revision: str
    model_files: tuple[str, ...]
    dependencies: tuple[str, ...]
    package_versions: tuple[tuple[str, str], ...]

    @property
    def venv_python(self) -> str:
        """Compatibility spelling for code that treats this as a venv detail."""

        return self.python_path

    @property
    def metadata(self) -> dict[str, str]:
        """Provider-state fields expected by the Vast lifecycle controller."""

        return {
            "schema": self.schema,
            "role": self.role,
            "fingerprint": self.fingerprint,
            "readyPath": self.ready_path,
            "failedPath": self.failed_path,
            "pythonPath": self.python_path,
            "snapshotPath": self.snapshot_path,
            "hfHome": self.hf_home,
        }


_PROBE_PROGRAM = r'''import importlib,importlib.metadata as md,json,os,pathlib,sys
import torch
s=json.loads(sys.argv[1]); out=pathlib.Path(sys.argv[2]); mode=sys.argv[3]
if tuple(sys.version_info[:2]) != tuple(s["runtime"]["python"]): raise SystemExit("python version mismatch")
tv=torch.__version__.split("+",1)[0]
if tv != s["runtime"]["torch"]: raise SystemExit("torch version mismatch")
if torch.version.cuda != s["runtime"]["cuda"]: raise SystemExit("CUDA runtime mismatch")
if not torch.cuda.is_available(): raise SystemExit("CUDA unavailable")
if not torch.cuda.is_bf16_supported(): raise SystemExit("BF16 unavailable")
versions={}
if mode == "venv":
    for name,want in s["packageVersions"].items():
        got=md.version(name)
        if got != want: raise SystemExit("package version mismatch: "+name)
        versions[name]=got
    for name in s["imports"]: importlib.import_module(name)
    vp=pathlib.Path(s["venvPath"]).resolve()
    if pathlib.Path(torch.__file__).resolve().is_relative_to(vp): raise SystemExit("venv installed its own torch")
value={"python":".".join(map(str,sys.version_info[:3])),"torch":torch.__version__,"cuda":torch.version.cuda,
       "device":torch.cuda.get_device_name(0),"bf16":True,"torchPath":str(pathlib.Path(torch.__file__).resolve()),
       "packages":versions}
tmp=out.with_name(out.name+".tmp"); tmp.write_text(json.dumps(value,sort_keys=True)+"\n"); os.chmod(tmp,0o600); os.replace(tmp,out)
'''

_DOWNLOAD_PROGRAM = r'''import json,os,pathlib,sys
from huggingface_hub import snapshot_download
s=json.loads(sys.argv[1]); out=pathlib.Path(sys.argv[2]); local=sys.argv[3]=="local"
p=snapshot_download(repo_id=s["model"]["id"],revision=s["model"]["revision"],
    allow_patterns=[x["name"] for x in s["model"]["files"]],cache_dir=s["hfHubCache"],
    local_files_only=local,token=False,max_workers=4)
tmp=out.with_name(out.name+".tmp"); tmp.write_text(json.dumps({"snapshotPath":p})+"\n"); os.chmod(tmp,0o600); os.replace(tmp,out)
'''

# This is the complete program sent to the public rental.  It only receives a
# base64-encoded public configuration.  Child processes get an allowlisted
# environment, keeping provider/API credentials away from pip and HF tooling.
_BOOTSTRAP_PROGRAM = r'''import base64,fcntl,gzip,hashlib,json,os,pathlib,shutil,subprocess,sys,time

payload=json.loads(gzip.decompress(base64.b64decode(sys.argv[1],validate=True)))
s=payload["config"]
canonical=json.dumps(s,sort_keys=True,separators=(",",":" )).encode()
if hashlib.sha256(canonical).hexdigest()!=payload["fingerprint"]: raise SystemExit("invalid prewarm fingerprint")
os.umask(0o077)
root=pathlib.Path(s["root"]); state=root/"state"; logs=root/"logs"; venvs=root/"venvs"
for p in (root,state,logs,venvs,pathlib.Path(s["hfHubCache"]),pathlib.Path(s["hfXetCache"])): p.mkdir(parents=True,exist_ok=True,mode=0o700)
ready=pathlib.Path(s["readyPath"]); failed=pathlib.Path(s["failedPath"]); log_path=pathlib.Path(s["logPath"])
lock_fd=os.open(state/"bootstrap.lock",os.O_WRONLY|os.O_CREAT,0o600); fcntl.flock(lock_fd,fcntl.LOCK_EX)
log_fd=os.open(log_path,os.O_WRONLY|os.O_CREAT|os.O_APPEND,0o600); os.chmod(log_path,0o600)
log=os.fdopen(log_fd,"a",buffering=1); started=time.time(); stage="initialize"
venv=pathlib.Path(s["venvPath"]); tmp_venv=venvs/("."+s["role"]+"-"+payload["fingerprint"]+".tmp")
probe_out=state/(".probe-"+str(os.getpid())+".json"); snap_out=state/(".snapshot-"+str(os.getpid())+".json")

def event(name,**fields):
    value={"atEpoch":time.time(),"event":name,"fingerprint":payload["fingerprint"],"role":s["role"]}; value.update(fields)
    log.write(json.dumps(value,sort_keys=True,separators=(",",":"))+"\n")
def atomic(path,value):
    tmp=path.with_name("."+path.name+"."+str(os.getpid())+".tmp")
    fd=os.open(tmp,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,"w") as f: json.dump(value,f,sort_keys=True,indent=2); f.write("\n"); f.flush(); os.fsync(f.fileno())
    os.replace(tmp,path)
def child_env():
    keep=("PATH","HOME","LD_LIBRARY_PATH","CUDA_VISIBLE_DEVICES","NVIDIA_VISIBLE_DEVICES","NVIDIA_DRIVER_CAPABILITIES","SSL_CERT_FILE","REQUESTS_CA_BUNDLE")
    e={k:os.environ[k] for k in keep if k in os.environ}
    e.update({"HOME":"/root","PIP_CONFIG_FILE":"/dev/null","PIP_INDEX_URL":"https://pypi.org/simple","PIP_NO_CACHE_DIR":"1",
              "HF_HOME":str(root/"hf"),"HF_HUB_CACHE":s["hfHubCache"],"HF_XET_CACHE":s["hfXetCache"],
              "HF_HUB_DISABLE_IMPLICIT_TOKEN":"1","HF_HUB_DISABLE_TELEMETRY":"1","HF_HUB_DISABLE_PROGRESS_BARS":"1",
              "HF_HUB_ETAG_TIMEOUT":"30","HF_HUB_DOWNLOAD_TIMEOUT":"600","TOKENIZERS_PARALLELISM":"false"})
    return e
env=child_env(); system_python=shutil.which("python3",path=env.get("PATH"))
def run(argv):
    result=subprocess.run(argv,env=env,stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT,check=False)
    if result.returncode: raise RuntimeError("child process failed")
def probe(python,mode):
    probe_out.unlink(missing_ok=True); run([str(python),"-c",s["probeProgram"],json.dumps(s,separators=(",",":")),str(probe_out),mode])
    return json.loads(probe_out.read_text())
def snapshot(python,mode):
    snap_out.unlink(missing_ok=True); run([str(python),"-c",s["downloadProgram"],json.dumps(s,separators=(",",":")),str(snap_out),mode])
    return pathlib.Path(json.loads(snap_out.read_text())["snapshotPath"])
def validate_files(path):
    if path.name!=s["model"]["revision"]: raise RuntimeError("snapshot revision mismatch")
    total=0
    for item in s["model"]["files"]:
        p=path/item["name"]
        if not p.is_file() or p.stat().st_size!=item["size"]: raise RuntimeError("snapshot file mismatch")
        total+=item["size"]
    return total
def write_ready(cache_hit,runtime,path,total):
    finished=time.time(); free=shutil.disk_usage(root).free
    atomic(ready,{"schema":s["readySchema"],"status":"ready","role":s["role"],"fingerprint":payload["fingerprint"],
        "startedAtEpoch":started,"finishedAtEpoch":finished,"durationSeconds":finished-started,"cacheHit":cache_hit,
        "venvPython":s["venvPython"],"snapshotPath":str(path),"model":{"id":s["model"]["id"],"revision":s["model"]["revision"],
        "files":[x["name"] for x in s["model"]["files"]]},"snapshotBytes":total,"freeBytes":free,"runtime":runtime,"logPath":s["logPath"]})

try:
    event("start")
    if not system_python: raise RuntimeError("python3 unavailable")
    prior=None
    try: prior=json.loads(ready.read_text())
    except (FileNotFoundError,json.JSONDecodeError,OSError): pass
    ready.unlink(missing_ok=True); failed.unlink(missing_ok=True)
    if prior and prior.get("status")=="ready" and prior.get("fingerprint")==payload["fingerprint"] and pathlib.Path(s["venvPython"]).is_file():
        try:
            stage="validate-cache"; runtime=probe(s["venvPython"],"venv"); path=snapshot(s["venvPython"],"local"); total=validate_files(path)
        except Exception as exc: event("cache-invalid",errorType=type(exc).__name__)
        else: write_ready(True,runtime,path,total); event("ready",cacheHit=True); raise SystemExit(0)
    stage="validate-runtime"; probe(system_python,"runtime")
    free=shutil.disk_usage(root).free
    if free < s["model"]["totalBytes"]+s["reserveBytes"]: raise RuntimeError("insufficient disk")
    stage="create-venv"; shutil.rmtree(tmp_venv,ignore_errors=True); run([system_python,"-m","venv","--system-site-packages",str(tmp_venv)])
    stage="install-dependencies"; run([str(tmp_venv/"bin/python"),"-m","pip","install","--disable-pip-version-check","--no-input","--no-cache-dir","--only-binary=:all:",*s["dependencies"]])
    stage="validate-venv"; runtime=probe(tmp_venv/"bin/python","venv")
    stage="download-model"; path=snapshot(tmp_venv/"bin/python","download"); total=validate_files(path)
    stage="validate-local-model"; path=snapshot(tmp_venv/"bin/python","local"); total=validate_files(path)
    stage="publish-venv"; shutil.rmtree(venv,ignore_errors=True); os.replace(tmp_venv,venv)
    runtime=probe(venv/"bin/python","venv"); path=snapshot(venv/"bin/python","local"); total=validate_files(path)
    write_ready(False,runtime,path,total); event("ready",cacheHit=False)
except SystemExit as exc:
    if exc.code not in (None,0):
        finished=time.time(); atomic(failed,{"schema":s["failedSchema"],"status":"failed","role":s["role"],"fingerprint":payload["fingerprint"],
            "startedAtEpoch":started,"failedAtEpoch":finished,"durationSeconds":finished-started,"stage":stage,"errorType":"SystemExit","exitCode":1,"logPath":s["logPath"]})
        event("failed",stage=stage,errorType="SystemExit"); raise
except BaseException as exc:
    finished=time.time(); atomic(failed,{"schema":s["failedSchema"],"status":"failed","role":s["role"],"fingerprint":payload["fingerprint"],
        "startedAtEpoch":started,"failedAtEpoch":finished,"durationSeconds":finished-started,"stage":stage,"errorType":type(exc).__name__,"exitCode":1,"logPath":s["logPath"]})
    event("failed",stage=stage,errorType=type(exc).__name__); shutil.rmtree(tmp_venv,ignore_errors=True); raise SystemExit(1)
finally:
    probe_out.unlink(missing_ok=True); snap_out.unlink(missing_ok=True); log.close(); os.close(lock_fd)
'''


def _canonical_json(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode("utf-8")


def _validated_root(value: str) -> str:
    root = PurePosixPath(value)
    if not root.is_absolute() or ".." in root.parts or not 2 <= len(str(root)) <= 240:
        raise ValueError("prewarm root must be a bounded absolute POSIX path")
    return str(root)


def _config(role: str, root_value: str) -> dict[str, object]:
    try:
        selected = _ROLES[role]
    except KeyError:
        raise ValueError("Voxtral prewarm role must be offline or realtime") from None
    root = PurePosixPath(_validated_root(root_value))
    program_sha = hashlib.sha256(_BOOTSTRAP_PROGRAM.encode("utf-8")).hexdigest()
    # The fingerprint is added after paths derived from it are known.  Use a
    # first hash over all immutable inputs, then include that stable value in
    # the role-specific venv path and hash the complete configuration.
    basis = {
        "schema": CONFIG_SCHEMA,
        "role": role,
        "root": str(root),
        "programSha256": program_sha,
        "runtime": {"python": [3, 11], "torch": "2.8.0", "cuda": "12.8"},
        "dependencies": list(selected.dependencies),
        "packageVersions": dict(selected.package_versions),
        "imports": list(selected.imports),
        "model": {
            "id": selected.model_id,
            "revision": selected.revision,
            "files": [{"name": name, "size": size} for name, size in selected.model_files],
            "totalBytes": sum(size for _, size in selected.model_files),
        },
        "reserveBytes": 2 * 1024**3,
    }
    path_key = hashlib.sha256(_canonical_json(basis)).hexdigest()
    model_cache = "models--" + selected.model_id.replace("/", "--")
    basis.update(
        {
            "readySchema": READY_SCHEMA,
            "failedSchema": FAILED_SCHEMA,
            "readyPath": str(root / "state" / "ready.json"),
            "failedPath": str(root / "state" / "failed.json"),
            "logPath": str(root / "logs" / "bootstrap.log"),
            "hfHubCache": str(root / "hf" / "hub"),
            "hfXetCache": str(root / "hf" / "xet"),
            "venvPath": str(root / "venvs" / f"{role}-{path_key}"),
            "venvPython": str(root / "venvs" / f"{role}-{path_key}" / "bin" / "python"),
            "snapshotPath": str(
                root / "hf" / "hub" / model_cache / "snapshots" / selected.revision
            ),
            "probeProgram": _PROBE_PROGRAM,
            "downloadProgram": _DOWNLOAD_PROGRAM,
        }
    )
    return basis


def render_onstart(role: str, *, _root: str = PREWARM_ROOT) -> PrewarmSpec:
    """Return the deterministic public onstart program and readiness contract."""

    config = _config(role, _root)
    fingerprint = hashlib.sha256(_canonical_json(config)).hexdigest()
    payload = base64.b64encode(
        gzip.compress(
            _canonical_json({"config": config, "fingerprint": fingerprint}), mtime=0
        )
    ).decode("ascii")
    compressed_program = base64.b64encode(
        gzip.compress(_BOOTSTRAP_PROGRAM.encode("utf-8"), mtime=0)
    ).decode("ascii")
    script = (
        "#!/usr/bin/env bash\n"
        "set -euo pipefail\n"
        f"exec python3 - {compressed_program!r} {payload!r} <<'CASSINI_VOXTRAL_PREWARM_PY'\n"
        "import base64,gzip,sys\n"
        "source=gzip.decompress(base64.b64decode(sys.argv[1],validate=True))\n"
        "sys.argv=[sys.argv[0],sys.argv[2]]\n"
        "exec(compile(source,'<cassini-voxtral-prewarm>','exec'),{'__name__':'__main__'})\n"
        "CASSINI_VOXTRAL_PREWARM_PY\n"
    )
    if len(script.encode("utf-8")) >= MAX_ONSTART_BYTES:
        raise ValueError("rendered Vast onstart program exceeds the 12 KiB safety limit")
    selected = _ROLES[role]
    return PrewarmSpec(
        schema=PREWARM_SCHEMA,
        role=role,
        script=script,
        fingerprint=fingerprint,
        ready_path=str(config["readyPath"]),
        failed_path=str(config["failedPath"]),
        log_path=str(config["logPath"]),
        python_path=str(config["venvPython"]),
        snapshot_path=str(config["snapshotPath"]),
        hf_home=str(PurePosixPath(str(config["root"])) / "hf"),
        model_id=selected.model_id,
        model_revision=selected.revision,
        model_files=tuple(name for name, _ in selected.model_files),
        dependencies=selected.dependencies,
        package_versions=selected.package_versions,
    )


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("role", choices=sorted(_ROLES))
    args = parser.parse_args()
    sys.stdout.write(render_onstart(args.role).script)


if __name__ == "__main__":
    main()
