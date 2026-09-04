---
name: gocassini-test-topology
description: Use whenever planning, adding, debugging, or reasoning about tests, harness scripts, or CI in the gocassini repo — unit matrices, recording-flow integration scripts, AppAPI/HaRP ExApp install paths, GPU transcription jobs, or workflows in `.github/workflows/`.
---

# gocassini Test Topology & CI Conventions

This skill captures **what's durable** about testing and CI topology in gocassini — harness conventions, service topologies, multi-tier CI workflow division, and constraints learned in production.

**Before recommending changes:** inspect `harness/bin/`, `.github/workflows/`, and the top-of-file comment blocks in any `ci-e2e-*.sh` or `ci-transcribe-*.sh` you touch. Those define the exact current implementation; this skill defines *how testing is architected here*.

---

## 1. Harness script conventions

Test scripts in `harness/bin/` fall primarily into two families:
- `ci-e2e-*.sh`: Recording flow, container packaging, manual install, and faithful HaRP integration scenarios.
- `ci-transcribe-*.sh`: GPU transcription smoke tests and short-clip regression suites (e.g. `ci-transcribe-smoke-exapp.sh`, `ci-transcribe-short-clip-regression.sh`).

When adding or modifying harness scripts:
- `#!/usr/bin/env bash` and `set -euo pipefail` at the top.
- **Source targeted modular libraries**: Prefer sourcing specific modular harness libraries (e.g. `harness/bin/lib/stack.sh`, `harness/bin/lib/e2e-local.sh`, `harness/bin/lib-exapp-manifest.sh`) rather than loading the full legacy `common.sh` compatibility shim.
- **Top comment block for scoped tests**: New container, install, and scenario scripts should state what the script covers AND what it explicitly does NOT cover (see §4 for why). Older baseline recorder scripts (`ci-e2e.sh`, `ci-e2e-mute.sh`) do not have this block.
- `trap` cleanup on `EXIT`, including failure paths. Containers or compose stacks left running on `george` or CI runners corrupt the next job.
- One scenario per script. The CI matrix provides parallelism; do not bundle multiple scenarios into one script.
- **Job naming vs script naming**: In `ci.yml`'s recorder integration matrix, scenario name == script suffix == matrix item (`Integration (ci-e2e-*.sh)`). In `publish-exapp-image.yml`, jobs use explicit emitted check names (e.g. `Faithful installed ExApp Talk artifact (CPU)`) for branch protection contexts, and multiple jobs may intentionally share an underlying harness script.

---

## 2. Service topology & harness modes

The local and CI test harness standardizes on explicit service topologies via `cassini dev stack`:

| Topology Mode (`--services`) | Included Services | Primary Test Purpose |
|---|---|---|
| `core` | `db`, `nextcloud` | Fast Nextcloud/Talk API checks; no AppAPI/HaRP and no media stack. |
| `appapi` | `db`, `nextcloud`, `appapi-harp`, `reverse-proxy` | AppAPI/HaRP install, proxy routing, control-panel/viewer checks without WebRTC media. |
| `full` | `db`, `nextcloud`, `appapi-harp`, `reverse-proxy`, `nats`, `janus`, `signaling`, `coturn` | Full local Talk media path for recorder/player E2E and operator debugging. |
| `full-remote` | Full local stack + `signaling-public-proxy` | Remote browser / macOS host-net testing requiring public HTTPS routing. |

Use `./bin/cassini dev stack plan --services <mode>` to inspect the resolved services, ports, and environment before starting a stack.

---

## 3. Multi-tier CI workflow topology

CI is partitioned into distinct workflows in `.github/workflows/` to balance fast feedback against heavy resource costs:

1. **`.github/workflows/ci.yml` (Fast PR & Main Gate)**:
   - `contracts`: Offline manifest validation (`./scripts/test-info-schema.sh`), classifier checks (`./scripts/test-classify-image-relevance.sh`), harness regression scripts (`./harness/bin/test-*.sh`), and shellcheck.
   - `ui`: Workspace unit and component test suites (`cassini-viewer`, `cassini-app`) via Vitest (`npm test -w <pkg>`) using root lockfile (`npm ci`). No Playwright browser suite in `ci.yml`.
   - `unit`: Go unit test matrix with `-race` across `cassini-go-recorder`, `cassini-operator`, and `harness/go-talk-rotator`.
   - `integration`: Fast recorder integration matrix (`ci-e2e.sh`, `ci-e2e-private.sh`, `ci-e2e-rejoin.sh`) plus `nextcloud:33` oldest CI-tested major compatibility leg (`appinfo/info.xml` declares `min-version="32"`).
   - `integration-mute`: Dedicated `ci-e2e-mute.sh` run (gated on recorder-side `stream_opened` evidence via D-444 to prevent retry noise).
   - PR concurrency cancels superseded runs (`cancel-in-progress: true`), preserving main pushes.

2. **`.github/workflows/publish-exapp-image.yml` (Heavy Packaging & Product Verticals)**:
   - Builds CPU and CUDA ExApp container images (CUDA split into content-hashed base and app layer).
   - Container e2e (`ci-e2e-exapp.sh`) and published entrypoint validation (`ci-e2e-entrypoint-exapp.sh`).
   - Manual-install checks (`ci-e2e-install-exapp.sh`) and faithful AppAPI/HaRP installation and Talk verification (`ci-e2e-installed-exapp-talk.sh`).
   - Talk recording roundtrip (`ci-e2e-talk-record-roundtrip.sh`), GPU transcription smoke (`ci-transcribe-smoke-exapp.sh`), and v3 transcript verification (`ci-e2e-v3-transcript-verify.sh`).

3. **`.github/workflows/deploy-preview.yml` (Branch Previews & GPU Processing)**:
   - Deploys UI previews or runs full GPU transcription pipelines.
   - Self-hosted GPU jobs run on `george` under the `george-gpu` concurrency group.

4. **`.github/workflows/microsite.yml` (Documentation & Microsite Gate)**:
   - Builds Astro microsite and validates Markdown content schemas (`src/content/config.ts`).
   - Kept separate from `ci.yml` because `ci.yml` intentionally ignores `**/*.md`.

5. **`.github/workflows/changelog-check.yml` (Release Notes Gate)**:
   - Enforces changelog fragments under `changelog.d/` on PRs modifying shipping code (bypassable via `skip-changelog` label).

6. **`.github/workflows/lint.yml`**:
   - Always-on fast PR gate. Runs `gofmt`, `shellcheck`, `actionlint` (workflow YAML only), and individually-enrolled offline harness unit tests (requiring no Docker and no network, e.g. `test-session-artifact-packet-guard.sh`, `test-prepare-synthetic-meeting.sh`). Note: does not run general YAML or Markdown linters.

---

## 4. The production path: don't bypass real components

Integration tests that stub out production components to simplify the harness test the harness, not production:

- **Faithful HaRP install path vs manual-install**:
  - `ci-e2e-installed-exapp-talk.sh` (D-453): The faithful product vertical (real image, real entrypoint, HaRP deploy daemon, reverse-proxy, Talk recording and playback).
  - `ci-e2e-install-exapp.sh`: Fast manual-install verification (`core` topology, manual daemon registration, overrides entrypoint with `cassini-operator`).
- **Real entrypoint rule**: Default to running the real container entrypoint (`exapp-start.sh`). If a test must bypass it (e.g. `ci-e2e-exapp.sh` testing bare container HTTP endpoints directly), the top comment must explicitly state what coverage is surrendered.
- **Log parsing fragility**: Nextcloud/AppAPI log strings drift between versions. Always prefer querying `occ` with `--output=json`.

---

## 5. Constraints that won't change soon

- **Public repo safety**: No secrets, private credentials, or undisclosed internal infrastructure in scripts, fixtures, or CI logs (documented self-hosted paths like `/mnt/data/cassini/...` on `george` and standard container mounts are expected). Untrusted PR inputs belong in `env:`, never interpolated directly into `run:` scripts.
- **GitHub Action pinning**: First-party `actions/*` and established Docker actions (`docker/*`) pin to trusted major-version tags (e.g. `@v4`, `@v5`, `@v6`). Any unvetted third-party actions must be pinned to immutable commit SHAs.
- **Bundled vs runtime model cache topology**:
  - `/opt/cassini/cache`: Static bundled-model root where `deployment/Dockerfile.exapp` embeds default models (int8 Parakeet + Silero VAD) and CUDA base embeds fp32.
  - Runtime writable cache: Redirected by the operator to persistent storage (`APP_PERSISTENT_STORAGE` / `operator/models`) for quality tiers not bundled in the image.
  - `EnsureModel` serializes downloads across all models using a single cache-wide lock (`.install.lock` via `withCacheLock`), guarding the volume's shared free-space budget against concurrent download overruns. Validated via completion markers.
- **Path deny-list filtering**: Docs and planning changes are skipped via `paths-ignore` deny-lists in `ci.yml` and mirrored in `scripts/classify-image-relevance.sh` (D-505).

---

## 6. The `george` runner

Single self-hosted Linux/X64 GPU runner:

- **Runner labels**:
  - `[self-hosted, Linux, X64, gpu]` in `publish-exapp-image.yml`
  - `[self-hosted, george]` in `deploy-preview.yml`
- **Shared state**: Docker containers, volumes, cached models, and `/tmp` persist between jobs. All scripts running on `george` must clean up in `trap` (on both success and failure).
- **GPU concurrency**: `deploy-preview.yml` serializes GPU runs under `group: george-gpu`. In `publish-exapp-image.yml`, GPU jobs serialize only by single-runner capacity.
- **Fork security**: Fork PRs are strictly barred from reaching the self-hosted GPU runner.

---

## 7. CI efficiency conventions

- **Harness stack startup**: `harness_start_compose_stack` brings up containers with `docker compose up -d` (without `--wait`) followed by condition-based HTTP polling (`wait_for_nextcloud`). Dedicated `docker compose pull` steps are reserved for heavy image-publishing pipelines in `publish-exapp-image.yml`.
- **Trim per-job install footprints**: Only install heavy tools (e.g. `ffmpeg`, `ripgrep`) on jobs that require them.
- **Cache by lockfile**: `go.sum`, `package-lock.json`, Python lockfiles.
- **Readiness polling**: Nextcloud has no Compose healthcheck and can report running before it finishes initializing; always rely on explicit HTTP readiness polling (`wait_for_nextcloud`) rather than Compose `--wait` or blind `sleep`.

---

## 8. Where to route a new test

- **Pure logic** → Colocated unit tests (`*_test.go`, `*.test.ts`); runs in the `ci.yml` `unit` or `ui` matrix.
- **Offline harness tests (no Docker, no network)** → Add script to `harness/bin/test-*.sh`. Enroll it individually in `lint.yml` (the always-on fast offline gate). For manifest and classifier checks, also wire into `contracts` in `ci.yml`.
- **Direct recorder integration** → Add `ci-e2e-*.sh` in `harness/bin/`, wire into the `integration` matrix in `ci.yml`.
- **Container packaging, entrypoint, or HaRP install** → Add script to `harness/bin/`, wire into `publish-exapp-image.yml`.
- **GPU-accelerated transcription** → Add `ci-transcribe-*.sh` in `harness/bin/`, wire into `publish-exapp-image.yml` or `deploy-preview.yml` targeting `[self-hosted, Linux, X64, gpu]` or `[self-hosted, george]`.

---

## 9. Stabilization milestones & active tracking

- **D-444**: Deflaked `ci-e2e-mute.sh` by synchronizing participant rotation on recorder-side `stream_opened` evidence, eliminating retry matrices.
- **D-453**: Closed the install-path gap by adding full HaRP and reverse-proxy e2e coverage (`ci-e2e-installed-exapp-talk.sh`).
- **D-505**: Synchronized doc deny-lists between `ci.yml` and `scripts/classify-image-relevance.sh` to prevent false-green CI skips.
- **D-578**: Pinned root npm workspace lockfile for reproducible UI installs.



