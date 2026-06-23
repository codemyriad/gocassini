# Bundled Parakeet Image Publishing Plan

Date: 2026-05-19
Status: planning (post eng-review)

## Goal

Publish public GHCR images for Gocassini that include the default open-source STT model in the image. The image must not download the default model on first run.

Two image variants:

- **CPU** (broadly installable): Parakeet v3 int8, CPU sherpa-onnx.
- **CUDA** (NVIDIA GPU): Parakeet v3 fp32, CUDA sherpa-onnx.

ROCm is not in scope. The local workstation (`martianbfg`) exposes ROCm at the kernel layer but has no `/opt/rocm` userspace AND no upstream sherpa-onnx ROCm artifact; real ROCm support requires a sherpa-onnx source build with HIP onnxruntime (multi-day yak).

## Review Decisions (locked 2026-05-19)

| ID | Decision | Rationale |
|----|----------|-----------|
| D1 | **Reuse `EnsureModel` / `CASSINI_CACHE_ROOT`**; do NOT add a parallel `CASSINI_MODEL_ROOT` knob. | `models.go:98` already short-circuits when required files exist on disk. Bundled-model "no download" is automatic once files are placed at `cacheDir/models/<id>/`. One env knob, zero new code paths. |
| D2 | **Split CPU and CUDA Dockerfiles** (`Dockerfile.exapp` stays CPU; new `Dockerfile.exapp.cuda`). | Runtime base, sherpa libs, model archive, and LD config all differ. Build-arg branching would obscure the actual runtime contents. |
| D3 | **Drop `-cuda` from `EXTRA_ALIAS_SUFFIXES` once real CUDA image lands; keep `-rocm`** as a documented CPU fallback. | `:latest-cuda` must not simultaneously mean "real CUDA" and "CPU stub." ROCm has no real image yet; the CPU alias keeps ROCm-tagged AppAPI daemons installable. |
| D4 | **Land CPU and CUDA as separate PRs** (PR #31 CPU; PR #32 CUDA). | george LXC GPU passthrough is independent ops work; CPU lands cleanly on `ubuntu-latest` and shouldn't wait on it. |
| D5 | **Test fixture: Mozilla Common Voice EN clip, ≥10s, CC0**, committed under `harness/media/` with `harness/media/NOTICE` for attribution. | Real spoken English, no license burden (CC0), deterministic across CI runners. |
| D6 | **Two-pronged smoke assertion**: (a) grep container logs for absence of `downloading model`, AND (b) assert `mtime` of `encoder.int8.onnx` matches the image build layer timestamp. | Log-only is fragile (rename breaks it silently). mtime check is positive proof that the file came from the image, not a runtime download. |
| D7 | **Cache root must move outside the persistent volume mount**: set `CASSINI_CACHE_ROOT=/opt/cassini/cache` in `Dockerfile.exapp`. | `compose.yml:22` and AppAPI both mount `/var/lib/cassini-operator` as a persistent volume. Anything baked under that path at build time is **masked at runtime** by the empty volume mount. |

## Model Choices

### CPU Variant

- Image tag: `ghcr.io/codemyriad/gocassini:cpu` and `:latest`
- Model id: `parakeet-tdt-0.6b-v3-int8`
- Source archive:
  `https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2`
- Observed archive size: `487,170,055` bytes (~465 MiB).
- Observed extracted size: ~641 MiB.
- Files:
  - `encoder.int8.onnx`: 652,184,281 bytes
  - `decoder.int8.onnx`: 11,845,275 bytes
  - `joiner.int8.onnx`: 6,355,277 bytes
  - `tokens.txt`: 93,939 bytes

Local sanity check on `martianbfg` (AMD Ryzen AI 9 HX PRO 370): `--model-type=nemo_transducer`, `--feat-dim=128`, `--provider=cpu`. 19.4s of audio decoded in 0.413s after recognizer creation. Peak RSS ~1.08 GiB.

### CUDA Variant

- Image tag: `ghcr.io/codemyriad/gocassini:cuda` and `:latest-cuda`
- Model id: `parakeet-tdt-0.6b-v3`
- Source archive (currently used by PR #26):
  `https://assets.gocassini.codemyriad.io/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3.tar.bz2`
- Archive size: 2,421,315,109 bytes (~2.26 GiB).
- Runtime libs: CUDA-enabled sherpa-onnx v1.13.1 (k2-fsa).
- Base image: pinned NVIDIA CUDA/cuDNN runtime (e.g., `nvidia/cuda:12.4.1-cudnn-runtime-ubuntu22.04`).

CUDA image requires Docker NVIDIA runtime on host:

```bash
docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi
```

## Image Layout

**Bundled model path (per D1+D7):**

```text
/opt/cassini/cache/models/<model-id>/
```

This is `${CASSINI_CACHE_ROOT}/models/<model-id>/` where `CASSINI_CACHE_ROOT=/opt/cassini/cache`. The `/opt/cassini` tree is image-baked and lives **outside** the persistent `/var/lib/cassini-operator` volume mount, so the files survive container start.

CPU files:

```text
/opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/encoder.int8.onnx
/opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/decoder.int8.onnx
/opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/joiner.int8.onnx
/opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/tokens.txt
/opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/NOTICE
```

CUDA files (analogous with `parakeet-tdt-0.6b-v3` model id and non-int8 filenames).

**Env defaults in the final images:**

```text
CASSINI_CACHE_ROOT=/opt/cassini/cache       # overrides /var/lib/cassini-operator/cache for ExApp
CASSINI_STT_DEVICE=cpu                       # cuda in the CUDA variant
```

No `CASSINI_MODEL_ROOT` — explicitly rejected in D1.

Build the model into a throwaway stage; final image gets the extracted files via `COPY --from=model-fetcher`, never the archive.

## Code Changes

### 1. Extend `cassini-go-recorder/internal/transcribe/models.go`

Add the v3 model IDs:

```go
const (
    ModelParakeet06BV3Int8 ModelID = "parakeet-tdt-0.6b-v3-int8"  // CPU bundle
    ModelParakeet06BV3     ModelID = "parakeet-tdt-0.6b-v3"        // CUDA bundle
)
```

Add corresponding `knownModels` entries:

```text
ModelParakeet06BV3Int8:
    URL:         <sherpa-onnx asr-models release URL>
    EncoderFile: encoder.int8.onnx
    DecoderFile: decoder.int8.onnx
    JoinerFile:  joiner.int8.onnx
    TokensFile:  tokens.txt
    ModelType:   nemo_transducer
    SampleRate:  16000
    FeatureDim:  128            # v3 uses 128, NOT 80 like v2

ModelParakeet06BV3:
    URL:         <gocassini assets bucket URL>
    EncoderFile: encoder.onnx
    DecoderFile: decoder.onnx
    JoinerFile:  joiner.onnx
    TokensFile:  tokens.txt
    ModelType:   nemo_transducer
    SampleRate:  16000
    FeatureDim:  128
```

Switch `defaultModelID` from `ModelParakeet06B` (v2) to `ModelParakeet06BV3Int8` (CPU image's default).

### 2. Bundled-model resolution

**No new resolution code needed.** Existing `EnsureModel(cacheDir, id, ...)` (`models.go:89`) already:

- Resolves to `${cacheDir}/models/${id}/`.
- Short-circuits with no download if `allExist(requiredModelFiles(...))` returns true (`models.go:98`).
- Errors clearly if a file is missing after extraction.

The only behavioral wiring needed:

- Dockerfile sets `CASSINI_CACHE_ROOT=/opt/cassini/cache`.
- Dockerfile build step extracts the model archive into `/opt/cassini/cache/models/<model-id>/`.
- The recorder's existing cache-root resolution (whatever currently honors `CASSINI_CACHE_ROOT`) keeps doing what it does.

### 3. Device wiring

`cassini build` already accepts `--device cpu|cuda` and defaults empty to CPU. The container entrypoint should pass the device down from `CASSINI_STT_DEVICE`. For the CUDA image, the operator must invoke recorder runs with `--device cuda`.

### 4. Update doctor / preflight

`cassini-go-recorder/internal/cassini/doctor.go` currently checks the older v2 int8 cache path. Update to:

- Report selected STT model id (from `defaultModelID` or override).
- Report resolved model path (`${CASSINI_CACHE_ROOT}/models/${id}/`).
- Report selected provider/device.
- Report required model files present (each filename + size or "missing").
- For CUDA: report whether `libonnxruntime_providers_cuda.so` can be loaded.

### 5. Attribution

Bundle a `NOTICE` file per model:

```text
/opt/cassini/cache/models/<model-id>/NOTICE
```

Contents: model name, source URL, NVIDIA Parakeet v3 license (`cc-by-4.0` on Hugging Face).

## Dockerfile Strategy (per D2)

**Split from day one.** Keep `Dockerfile.exapp` as the CPU variant (extended with model bundling). Add `deployment/Dockerfile.exapp.cuda` as a separate file.

Shared concerns (Go builder stage, viewer/control-panel builder stages, frpc fetcher stage, entrypoint script, healthcheck) can either be duplicated or factored into a common base stage that both Dockerfiles `FROM`. Start with duplication; refactor only if drift becomes a maintenance problem.

The split is justified because the CUDA variant differs in:

- Base image (`nvidia/cuda:…-cudnn-runtime-ubuntu22.04` vs `node:22-bookworm-slim`).
- Sherpa-onnx native libs (separate CUDA build archive from k2-fsa, ~191 MiB compressed).
- Bundled model (~641 MiB int8 vs ~2.26 GiB fp32 archive).
- `LD_LIBRARY_PATH` (must include CUDA + cuDNN paths).
- Runtime env (`CASSINI_STT_DEVICE=cuda`).

## CI Workflow (per D4)

**Two PRs, sequential:**

- **PR #31** (this branch, `bundled-parakeet-cpu`): CPU bundled image + workflow extension + `CASSINI_CACHE_ROOT=/opt/cassini/cache` switch + remove `-cuda` from `EXTRA_ALIAS_SUFFIXES`. Lands on top of `installable-nextcloud-app` (PR #30); rebases to main when #30 merges.
- **PR #32** (later branch, `bundled-parakeet-cuda`): new `Dockerfile.exapp.cuda` + CUDA workflow job on `[self-hosted, Linux, X64, gpu]` + sherpa-onnx CUDA v1.13.1 pin + nvidia/cuda base pin + real `:latest-cuda` tag push.

### Tag alias updates (PR #31)

In `.github/workflows/publish-exapp-image.yml`:

```yaml
# Was: EXTRA_ALIAS_SUFFIXES: '-cuda -rocm'
# Now:
EXTRA_ALIAS_SUFFIXES: '-rocm'   # -cuda removed; CUDA tags pushed by Dockerfile.exapp.cuda job (PR #32)
```

Update the `prune-old-versions` step's keep-list (lines 265, 277) to match: drop `latest-cuda` from the protected tags until PR #32 lands (otherwise prune protects a tag that won't exist).

Document in `docs/exapp-install.md`: "The `:latest-rocm` tag is currently a CPU fallback for ROCm-tagged AppAPI daemons; real ROCm support is not implemented. Operators with ROCm hardware get the same image as `:cpu`/`:latest`."

### Runner setup

```yaml
runs-on: [self-hosted, Linux, X64, gpu]
```

`gpu` label was added to runner `george` on 2026-05-19 (id 22). Runner is online.

**george topology note:** george is a Proxmox host. The GHA runner lives inside an LXC with NVIDIA passthrough (driver on host, userland in the LXC, `/dev/nvidia*` bind-mounted, nvidia-container-toolkit installed in the LXC, Docker daemon inside the LXC). GPU shared across LXCs is the explicit goal; trade-off accepted per Open Risks.

### CPU Build Job (PR #31)

Runs on `ubuntu-latest`. Steps:

1. Checkout.
2. Set up Docker Buildx.
3. Build CPU image with bundled v3 int8 model (model download happens in a `model-fetcher` build stage; final image gets `COPY --from=model-fetcher /opt/cassini/cache/models/...`).
4. Smoke test HTTP plane with existing ExApp smoke script.
5. Transcription smoke: `docker run` the image with the bundled Common Voice fixture, assert non-empty `.opus` output.
6. **Two-pronged "no download" assertion (D6):**
   - `! docker logs $CTR | grep -q "downloading model"` (negative)
   - mtime of `/opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/encoder.int8.onnx` inside the running container matches the image build layer time (positive).
7. Push tags on non-PR events.

Tags:

```text
ghcr.io/codemyriad/gocassini:sha-<sha>
ghcr.io/codemyriad/gocassini:latest
ghcr.io/codemyriad/gocassini:cpu
ghcr.io/codemyriad/gocassini:latest-rocm    # CPU fallback alias (kept)
ghcr.io/codemyriad/gocassini:<version>
ghcr.io/codemyriad/gocassini:<version>-rocm
```

### CUDA Build Job (PR #32)

Runs on `[self-hosted, Linux, X64, gpu]`.

Preflight:

```bash
nvidia-smi
docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi
```

Steps:

1. Checkout.
2. Set up Docker Buildx.
3. Build CUDA image with bundled v3 fp32 model.
4. HTTP smoke test.
5. Transcription smoke inside Docker with `--gpus all`.
6. Assert logs show CUDA provider/device active.
7. Two-pronged "no download" assertion (same as CPU).
8. Push tags on non-PR events.

Tags:

```text
ghcr.io/codemyriad/gocassini:sha-<sha>-cuda
ghcr.io/codemyriad/gocassini:cuda
ghcr.io/codemyriad/gocassini:latest-cuda
ghcr.io/codemyriad/gocassini:<version>-cuda
```

Re-add `latest-cuda` to the prune `keep-list` in `publish-exapp-image.yml`.

## Test Inputs (per D5+D6)

**Fixture:** `harness/media/parakeet-smoke.mkv` — a Mozilla Common Voice EN clip, ≥10 seconds, license CC0. Attribution + source URL captured in `harness/media/NOTICE`.

Selection criteria:

- Single speaker, clear audio.
- Validated set (so the reference transcript exists if we ever want to assert content).
- ≥10s duration.
- Verbal English content (not silence padding).

The transcription assertion is **shape-only, not content**:

- `cassini build <fixture> --out /tmp/out.opus --device cpu` exits 0.
- Output `.opus` exists and is non-empty.
- `cassini inspect` reports STT model id == `parakeet-tdt-0.6b-v3-int8` and device == `cpu`.

The "no download" assertion (D6):

- Container logs do not contain `downloading model`.
- `stat -c %Y /opt/cassini/cache/models/parakeet-tdt-0.6b-v3-int8/encoder.int8.onnx` inside the running container equals the file's mtime baked into the image layer (use `docker inspect` to get the image creation time as a reference; allow ≤ image_created_at + 1s for clock skew).

## Image Size Expectations

CPU image:

- Model layer: ~641 MiB extracted.
- Total image likely well under 2 GiB compressed.

CUDA image:

- Model: ~2.26 GiB compressed archive.
- CUDA sherpa runtime: ~191 MiB compressed.
- Base + CUDA libs + model: ~3 GiB compressed expected.

Both well under GHCR's 10 GB per-layer limit, but default GitHub-hosted runner disk is a poor fit for CUDA image builds. Self-hosted GPU runner (george LXC) is the build host for CUDA.

## Open Risks

- **george LXC GPU passthrough brittleness.** Docker-in-LXC with NVIDIA passthrough is a four-cgroup stack (Proxmox → LXC → Docker → workload). Proxmox kernel upgrades can silently break `nvidia-container-cli` with `driver/library version mismatch` until the LXC is rebooted. Tradeoff accepted: sharing the GPU across LXCs requires LXC; the alternative (Proxmox VM + PCIe passthrough) would dedicate the GPU. If CUDA CI goes red after a Proxmox upgrade, first remediation is reboot the runner LXC.
- **martianbfg cannot run real ROCm tests.** Kernel layer (`amdgpu`, `/dev/kfd`, `rocm-smi`) is present, but `/opt/rocm` userspace is missing and sherpa-onnx ships no ROCm Go-native artifact upstream. A real ROCm transcription test requires a sherpa-onnx source build with HIP onnxruntime — multi-day yak. Strix Point integrated Radeon is also not on the official ROCm GPU compatibility list.
- **AppAPI deployment must select the CPU image by default** unless the deploy daemon is known to support NVIDIA GPU containers. `:latest` points to CPU.
- **CUDA base image and sherpa CUDA artifact versions must be pinned together.** Mismatch surfaces as cuDNN load errors at runtime, not at build time.
- **Large image layers may make `type=gha` Buildx cache unreliable**, especially for the ~3 GiB CUDA layer. Prefer registry cache or local self-hosted runner cache for CUDA.

## NOT in Scope

- ROCm image variant (rationale above; deferred R&D).
- Multi-arch (linux/arm64) — single-arch linux/amd64 for v1.
- Runtime model auto-download for non-default models (out of scope; v1 is bundled-only).
- Hugging Face direct-model loading.
- Switching `:latest` to GPU-preferred — `:latest` stays CPU until AppAPI gains clean hardware selection.

## What Already Exists (reused, not rebuilt)

- `cassini-go-recorder/internal/transcribe/models.go` — `EnsureModel` + `knownModels` map already handles the bundled case correctly (file-presence short-circuit). Only the model entries and `defaultModelID` need touching.
- `cassini-go-recorder/internal/cassini/doctor.go` — exists, currently checks v2 path; needs entries refreshed.
- `deployment/Dockerfile.exapp` — multi-stage build with Go builder, viewer/control-panel builders, frpc fetcher, runtime stage. Extended in PR #31, not replaced. Cloned (with adjustments) in PR #32 as `Dockerfile.exapp.cuda`.
- `.github/workflows/publish-exapp-image.yml` — has `EXTRA_ALIAS_SUFFIXES` machinery (CPU stub for `-cuda`/`-rocm`). PR #31 trims it; PR #32 adds the real CUDA tags.
- `harness/bin/ci-smoke-exapp.sh` — existing HTTP-plane smoke. Transcription smoke is a new sibling script.

## Failure Modes

| Codepath | Realistic failure | Test? | Error handling? | User sees? |
|----------|-------------------|-------|-----------------|------------|
| Model file missing after build | `EnsureModel` returns "model file not found after extraction" | Two-pronged D6 catches it | Hard error at startup | Operator: container fails healthcheck, logs show clear missing-file error |
| Volume mount masks bundled model | Container starts, recorder tries to load model, files not visible | D7 prevents this by path choice; smoke catches it via mtime assertion | EnsureModel errors out (no auto-download in production env) | Operator: clear error, no silent CPU fallback |
| sherpa-onnx CUDA lib version mismatch (CUDA only) | `libonnxruntime_providers_cuda.so` fails to load CUDA runtime | Doctor preflight check (per Code Changes #4) | Hard error at startup | Operator: doctor output names the version mismatch |
| `--gpus all` not available on host (CUDA image, no NVIDIA runtime) | Container starts, recorder falls back to CPU (or errors) | CUDA smoke includes `--gpus all` precondition check | Hard error preferred over silent CPU fallback | Operator: clear error |
| LXC GPU passthrough broken after Proxmox upgrade | `nvidia-container-cli: initialization error` in CUDA CI | CUDA preflight `nvidia-smi` step fails fast | Workflow fails, no green smoke | Maintainer: red CI, reboot LXC |
| Model archive checksum mismatch at build time | Tar extraction fails, build fails | Build step fails | Hard error in CI | Maintainer: red build |

**Critical gap to address in implementation:** the recorder must NOT silently auto-download a missing bundled model at runtime in production. Today's `EnsureModel` will download if files are absent — fine for dev, dangerous for a Nextcloud-managed container with no egress. PR #31 should add a build flag or env (`CASSINI_DISALLOW_MODEL_DOWNLOAD=1` set in `Dockerfile.exapp`) that makes the recorder error instead of download when files are absent. Add to Code Changes when implementing.

## Worktree Parallelization

Sequential implementation, no parallelization opportunity. PR #31 is a single workstream (models.go entry + Dockerfile bundle + workflow alias trim + smoke fixture + doctor refresh). PR #32 depends on PR #31's pattern.

## References

- PR #26: `D-277 STT swap: parakeet v2 -> v3 fp32 GPU + multi-transcript bundle`
- PR #30 (parent branch): `exapp: make gocassini installable as a Nextcloud ExApp (v1, CPU-only)`
- k2-fsa sherpa-onnx release assets: https://github.com/k2-fsa/sherpa-onnx/releases
- k2-fsa sherpa ASR model assets: https://github.com/k2-fsa/sherpa-onnx/releases/tag/asr-models
- NVIDIA Container Toolkit validation: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/sample-workload.html
- GitHub self-hosted runner labels: https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/using-self-hosted-runners-in-a-workflow
- NVIDIA Parakeet v3: https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3
- Mozilla Common Voice: https://commonvoice.mozilla.org/en/datasets

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 0 | — | (not run — infra/build plan, low strategic ambiguity) |
| Codex Review | `/codex review` | Independent 2nd opinion | 0 | — | (skipped per user — outside voice optional) |
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 1 | CLEAR (PLAN) | 7 decisions taken (D1–D7), 1 critical gap surfaced (volume mount masking — fixed by D7), 1 build-time hardening recommended (`CASSINI_DISALLOW_MODEL_DOWNLOAD=1`) |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | — | (n/a — no UI surface) |
| DX Review | `/plan-devex-review` | Developer experience gaps | 0 | — | (n/a — internal infra) |

**UNRESOLVED:** 0
**VERDICT:** ENG CLEARED — ready to implement on branch `bundled-parakeet-cpu`.
