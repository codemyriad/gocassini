---
shaping: true
---

# D-290 E2E testing — Shaping

This document shapes Linear **D-290**.

It elaborates `framing.md` (in this directory) and sits alongside the broader MVP shape in `planning/initiatives/mvp/shaping.md` and the ExApp plan in `planning/installable-nextcloud-app.md`.

## Working position

**Selected shape: C — One-job multi-step, phased prod-path with per-phase reporting** (2026-05-28).

Rationale:
- One compose-up cost (avoids B's double-stack overhead) while preserving per-phase ✅/❌ signal in the GH Actions UI (avoids A's "the last log banner is your error message").
- Phase function structure makes future expansion (e.g. an upgrade-path slice when R7.2 comes back in scope) a small change rather than a refactor.
- Replaces the three bypass-based jobs with a single coherent successor whose phases mirror the production user journey: install → record → view.

Still to resolve before slicing can begin:
- ~~**Spike X1**~~ — **done 2026-05-28.** Mechanism sound; surfaced an install-path gap (Talk shared secret not provisioned to the ExApp container). Absorbed into D-290 as **Slice 0**. See `spike-x1-recording-via-proxy.md`.
- ~~**Spike X2**~~ — **done 2026-05-28.** Bypass-test audit complete; migration list captured. 5 items to move out (3 negative-auth cases + replay idempotency + restart persistence → Go unit tests; disable→enable cycle + non-admin 404 → Shape C phase assertions). See `spike-x2-bypass-coverage-audit.md`.
- ~~**Branching strategy**~~ — resolved: `d-290-e2e-testing` branched off PR #40 (`cassini-appstore-dogfood`). Rebase to `main` once PR #40 merges.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | A regression in the production install + recording path fails loudly in CI on the PR that introduces it. | Core goal |
| R1 | **Install path coverage (full production wiring)** | |
| R1.1 | CI exercises the same code path that the App Store "Install" button triggers — `app_api:app:register` with the HaRP daemon and `info.xml`, end-to-end. | Must-have |
| R1.2 | The installed ExApp container runs its real entrypoint (`exapp-start.sh`), establishes its frpc tunnel to HaRP, and passes the healthcheck. | Must-have |
| R1.3 | AppAPI's heartbeat reaches the container through HaRP + reverse-proxy (the path that broke in D-286), and install completes in `[enabled]`. | Must-have |
| 🟡 R1.4 | After install, the user viewer route loads through the AppAPI proxy with USER ACL enforced (logged-in NC user sees viewer; unauthenticated request rejected). **Admin control-panel intentionally out of scope** — it's being rewritten in a separate unit of work. | Must-have |
| R2 | **Recording path coverage (full production wiring)** | |
| R2.1 | A Talk room with the record button triggers Cassini via Talk's `recording_servers` → AppAPI proxy → HaRP → operator (the path `manual-test-setup.sh` wires; not the direct-to-operator wiring of today's `ci-e2e-talk-record-roundtrip.sh`). | Must-have |
| R2.2 | The recording is captured, transcribed, and the resulting meeting appears in the published library, readable through the proxy in the viewer. | Must-have |
| 🟡 ~~R2.3~~ | ~~Talk's HMAC over the shared secret is verified end-to-end.~~ Dropped — implied by R2.1 ("record button works" exercises HMAC); surface as an explicit assertion during implementation if it turns out not to be exercised transitively. | — |
| R3 | **Replace bypass-based tests** | |
| R3.1 | The full-prod-path tests replace the existing bypass-based tests (`ci-e2e-exapp.sh`, `ci-e2e-install-exapp.sh`, `ci-e2e-talk-record-roundtrip.sh`) rather than running alongside them. | Must-have |
| R3.2 | Any coverage the bypass-based tests provided that the full-path tests don't (e.g. focused middleware-rejection cases) is preserved — extracted into the new tests or covered by unit tests. | Must-have |
| R4 | **CI honesty & maintainability** | |
| R4.1 | Each remaining CI script's top comment names what it covers AND what it explicitly does NOT cover (the convention the harness already follows; preserved going forward). | Must-have |
| R4.2 | When a future change adds a new production-path component (e.g. a future auth wrapper), the convention forces the author to either extend the full-path test or document the gap. | Must-have |
| R5 | **CI runtime efficiency** (Ivan's "nice-to-have"; scoped to a late slice) | |
| 🟡 R5.1 | Per-job runtime is profiled before and after this work; the new full-path job stays under a **12-minute soft ceiling**, and we apply every reasonable efficiency lever (caching, split-pull-from-up, trimmed installs, parallelisation) to land as far under it as possible. | Must-have |
| R5.2 | Implicit slow steps are split out so the GitHub Actions UI shows accurate per-step progress (e.g. `docker compose pull` separate from `docker compose up`). | Must-have |
| R5.3 | Caching is keyed by lockfile / model hash, not by date; the GPU runner reuses model downloads across jobs. | Must-have |
| R5.4 | Per-job install footprint is trimmed — jobs install only what they need. | Should-have |
| R6 | **Doesn't regress what works today** | |
| 🟡 R6.1 | Recording-flow integration tests (`ci-e2e.sh`, `ci-e2e-mute.sh`, `ci-e2e-rejoin.sh`) — recorder against local Talk independent of the ExApp wrapper — **keep running as independent value** (focused recorder-only signal, isolates recorder bugs from ExApp/HaRP wiring). Not retired, not absorbed. | Must-have |
| 🟡 R6.2 | GPU transcription smoke + Levenshtein verify (`ci-e2e-v3-transcript-verify.sh`) — **keeps running as independent value** (transcript-quality gate that doesn't depend on the install path). Not retired, not absorbed. | Must-have |
| R6.3 | Total CI wall time on a typical PR doesn't increase materially despite the broader full-path coverage. | Should-have |
| R7 | **Out of scope** | |
| R7.1 | Browser-driven user-perspective e2e (Playwright clicking actual NC admin UI buttons) — defer. | Out |
| R7.2 | ExApp upgrade path (install vN, upgrade to vN+1, verify state) — defer; relevant once we publish to App Store and have versions to upgrade between. | Out |
| R7.3 | Summary / LLM-output quality testing — already tracked in D-268 (backburner). | Out |
| R7.4 | Multi-user concurrent recording stress testing — defer; not load-tested today, not blocking MVP. | Out |

---

## CURRENT: Existing test surface (baseline)

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `.github/workflows/ci.yml` runs unit-test matrix (3 Go modules) and integration matrix (`ci-e2e.sh`, `ci-e2e-mute.sh`, `ci-e2e-rejoin.sh`) — the recorder-against-local-Talk flows. | |
| **CURRENT2** | `.github/workflows/publish-exapp-image.yml` runs the ExApp lane on every PR + push: `validate-manifest`, `build-image` (CPU + CUDA), `smoke`, `transcribe-smoke` (CPU + GPU), `e2e-container` (`ci-e2e-exapp.sh`), `e2e-install` (`ci-e2e-install-exapp.sh`), `e2e-talk-roundtrip` (CPU + GPU). GPU jobs run on `george`. | |
| **CURRENT3** | `ci-e2e-exapp.sh` runs the ExApp container with AppAPI-shaped env, asserts middleware (401/200), `/init` + `/enabled` lifecycle, SPA serving, BasePath behavior. Bypasses `exapp-start.sh` entrypoint and frpc. | |
| **CURRENT4** | `ci-e2e-install-exapp.sh` brings up Nextcloud + AppAPI via harness compose, installs Cassini via `app_api:app:register` with the `manual-install` daemon, asserts proxied routes per role. Bypasses HaRP, reverse-proxy, and the entrypoint. | |
| **CURRENT5** | `ci-e2e-talk-record-roundtrip.sh` runs the full Talk + signaling + ExApp stack (CPU and GPU variants), triggers recording via Talk's recording-backend HTTP protocol, asserts Levenshtein match against expected transcript. Points Talk's `recording_servers` directly at the operator container — bypasses AppAPI proxy and HaRP. | |
| **CURRENT6** | `ci-e2e-v3-transcript-verify.sh` extends the transcribe smoke with a Levenshtein assertion against the LibriSpeech reference. Container-only; no install path. | |
| **CURRENT7** | `harness/bin/manual-test-setup.sh` (on branch `cassini-appstore-dogfood`, PR #40 — not yet merged, not in CI) stands up the full production topology locally: NC + db + AppAPI HaRP daemon + nginx reverse-proxy + mock App Store catalog, then registers Cassini via `app_api:app:register harp_local --info-xml --test-deploy-mode --wait-finish`. Reproduced D-286 on first run. | |
| **CURRENT8** | The `manual-install` daemon (used in CURRENT4) skips HaRP routing entirely — it records IP/port for a container already running and proxies AppAPI calls direct. Documented as legacy and scheduled for removal in NC 35. | |
| **CURRENT9** | The `harp_local` daemon (used by `manual-test-setup.sh`) is the production-shape daemon — registers a backend in HaRP, requires frpc tunnel from the ExApp side, requires nginx reverse-proxy splitting `/exapps/...` → HaRP from `/` → NC. | |
| **CURRENT10** | `george` (single self-hosted Linux/X64 GPU runner) handles the CUDA jobs. State (containers, volumes, model cache) is shared across jobs. | |
| **CURRENT11** | CI runtime, as of PR #40: install-e2e ~2min, container-e2e ~1min, transcribe-smoke-cuda ~2.5min, talk-record-roundtrip-CPU ~4.8min, talk-record-roundtrip-CUDA ~2.6min, integration matrix ~3min each. Total wall time gated by the longest job; no current target. | |
| **CURRENT12** | Per-job step granularity is uneven: some `docker compose up` invocations implicitly pull images, hiding pull-vs-startup timing. | |

---

## H: Shared building block — Install harness (CI-shape of manual-test-setup.sh)

All three shapes start from the same install harness. Treating it as a shared block keeps the shape comparison about *what attaches to install*, not *how install gets set up*.

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **H1** | Promote `harness/bin/manual-test-setup.sh` (currently on PR #40 branch) to a CI-runnable `harness/bin/ci-e2e-install-prod-path.sh`: strip interactive helpers (`xdg-open`, banner prints), source the existing image via the `load-exapp-image` action instead of building inline, stable `PROJECT_NAME`, `trap` cleanup on EXIT/INT/TERM. | |
| **H2** | Add explicit assertions: parse `occ app_api:app:list --output=json` → assert `gocassini` state is `[enabled]`; assert ExApp container is healthy; assert HaRP routing table contains a `gocassini` backend. | |
| **H3** | Land PR #40's `harness/compose.yml` additions (HaRP service, reverse-proxy service, mock App Store catalog) on `main` first as a prerequisite — or scope a vendored copy under `harness/compose.exapp-prod-path.yml` if PR #40's merge is delayed. | ⚠️ |
| **H4** | Top comment block on the new harness names what it covers AND what it explicitly does NOT (existing convention; see R4.1). | |

---

## A: One-script one-job — single dogfood loop

Single bash script does install → recording → viewer-read as sequential phases in one process. Single CI job. Failure is identified by the last log banner the script emits.

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Use H as the install phase. | |
| **A2** | Append a recording phase to the same script: create Talk room via OCS API as `admin`, start a call, POST to Talk's recording-start endpoint (which AppAPI proxy forwards to the operator per `bootstrap.sh`'s `recording_servers` wiring), wait for the recording-finalized callback to Talk, assert the resulting `.mkv` was uploaded and transcribed. | ⚠️ |
| **A3** | Append a viewer phase: log in as `alice` via OCS, fetch the viewer page through the AppAPI proxy URL, assert `catalog.json` lists the published meeting; fetch the meeting JSON; assert transcript words are present and non-empty. | ⚠️ |
| **A4** | Single CI job `e2e-prod-path` in `publish-exapp-image.yml`, ≤12 min, replaces three existing jobs. | |
| **A5** | Retire `ci-e2e-exapp.sh`, `ci-e2e-install-exapp.sh`, `ci-e2e-talk-record-roundtrip.sh` (CPU + CUDA variants) — delete script and job. | |
| **A6** | Audit retired tests for unique coverage (mostly: negative middleware tests in `ci-e2e-exapp.sh`); rescue valuable assertions into Go unit tests in `cassini-operator/internal/exapi/`. | ⚠️ |

**Trade-offs of A:** Cheapest to write, cheapest to run (one compose stack, one wall clock). Weakest signal — a recording flake brings down the install green-check too; failure identification relies on log banners, not on GH Actions step status.

---

## B: Two-job split — fail-independent prod-path

Install runs as a fast, focused job. Recording (with viewer-read inside it) runs as a separate, slower job in parallel. Each fails independently; they share helpers but not a compose stack.

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Install-only job `e2e-install-prod-path`: uses H, asserts install reaches `[enabled]`, tears down. Fast (~4 min target). | |
| **B2** | Recording-with-viewer job `e2e-record-prod-path`: parallel to B1, starts its own compose stack, runs H to install, then drives the recording roundtrip + viewer-read. ≤12 min. | ⚠️ |
| **B3** | Shared helpers (compose-up wrapper, bootstrap, HaRP registration, OCS-API helpers) extracted to `harness/bin/common-prod-path.sh`, sourced by both jobs and by the harness. | |
| **B4** | Two CI jobs in `publish-exapp-image.yml`, parallel (no `needs:` between them). | |
| **B5** | Retire `ci-e2e-exapp.sh`, `ci-e2e-install-exapp.sh`, `ci-e2e-talk-record-roundtrip.sh` (CPU + CUDA). | |
| **B6** | Audit retired tests for unique coverage; rescue assertions into unit tests. | ⚠️ |

**Trade-offs of B:** Sharpest signal — install ❌ and recording ✅ (or vice versa) is unambiguous in the GH Actions UI. Costs roughly two compose-up cycles in parallel (≈ wall-clock max of the two jobs). Install regressions show up in ~4 min instead of waiting ~12 min for the full path to run.

---

## C: One-job multi-step — phased prod-path with per-phase reporting

Single CI job, single compose stack, but three GitHub Actions *steps* call the harness with `--phase=install` / `--phase=record` / `--phase=viewer`. Each step reports independently in the UI even though they all run on the same runner against the same docker daemon.

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Single script `harness/bin/ci-e2e-prod-path.sh` accepts `--phase=install\|record\|viewer\|all`. Each phase is a function; phases after `install` assume the compose stack is up from the previous step. | |
| 🟡 **C2** | Single CI job `e2e-prod-path` with three Actions steps: step "install" runs `--phase=install`; step "record" runs `--phase=record`; step "viewer" runs `--phase=viewer`. Compose stack started before step 1 (or as part of `install`), torn down in a final step with `if: always()`. **Spike X1 confirmed: mechanism is sound; depends on Slice 0 (Talk-secret provisioning) for the record phase to authenticate.** | |
| **C3** | Steps after step 1 use `if: success()` so a failed install short-circuits the rest with a clear UI marker, rather than running and failing on missing precondition. | |
| **C4** | Per-step ✅/❌ in the GH Actions UI distinguishes install / record / viewer signal even within one job. ≤12 min. | |
| **C5** | Retire `ci-e2e-exapp.sh`, `ci-e2e-install-exapp.sh`, `ci-e2e-talk-record-roundtrip.sh` (CPU + CUDA). | |
| 🟡 **C6** | Audit retired tests for unique coverage; rescue assertions per X2's migration list (3 Go unit tests for negative-auth + replay + restart-persistence; 2 added Shape-C phase assertions for disable→enable cycle and non-admin 404). **Spike X2 done; migration list concrete.** | |

**Trade-offs of C:** Mid-ground. One compose-up cost (cheaper than B), per-phase signal in UI (better than A). Install failure stops the rest cleanly. Slightly more script complexity (phase dispatching) than A.

---

## Fit Check

Per the shaping methodology, flagged unknowns force ❌ until the spike resolves them.

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | A regression in the production install + recording path fails loudly in CI on the PR that introduces it. | Core goal | ✅ | ✅ | ✅ |
| R1.1 | CI exercises the same code path that the App Store "Install" button triggers — `app_api:app:register` with the HaRP daemon and `info.xml`, end-to-end. | Must-have | ✅ | ✅ | ✅ |
| R1.2 | The installed ExApp container runs its real entrypoint (`exapp-start.sh`), establishes its frpc tunnel to HaRP, and passes the healthcheck. | Must-have | ✅ | ✅ | ✅ |
| R1.3 | AppAPI's heartbeat reaches the container through HaRP + reverse-proxy and install completes in `[enabled]`. | Must-have | ✅ | ✅ | ✅ |
| R1.4 | After install, the user viewer route loads through the AppAPI proxy with USER ACL enforced. | Must-have | ✅ | ✅ | ✅ |
| 🟡 R2.1 | Talk room with the record button triggers Cassini via `recording_servers` → AppAPI proxy → HaRP → operator. | Must-have | ✅ | ✅ | ✅ |
| 🟡 R2.2 | The recording is captured, transcribed, and the resulting meeting appears in the published library, readable through the proxy in the viewer. | Must-have | ✅ | ✅ | ✅ |
| R3.1 | Full-prod-path tests replace existing bypass-based tests. | Must-have | ✅ | ✅ | ✅ |
| 🟡 R3.2 | Any unique coverage from the bypass tests is preserved — extracted into new tests or unit tests. | Must-have | ✅ | ✅ | ✅ |
| R4.1 | Each remaining CI script's top comment names what it covers AND what it explicitly does NOT cover. | Must-have | ✅ | ✅ | ✅ |
| R4.2 | When a future change adds a new production-path component, the convention forces extending the full-path test or documenting the gap. | Must-have | ✅ | ✅ | ✅ |
| R5.1 | New full-path job stays under a 12-minute soft ceiling; every reasonable efficiency lever applied. | Must-have | ✅ | ✅ | ✅ |
| R5.2 | Implicit slow steps split out so the UI shows accurate per-step progress. | Must-have | ✅ | ✅ | ✅ |
| R5.3 | Caching keyed by lockfile / model hash, not date; `george` reuses model downloads across jobs. | Must-have | ✅ | ✅ | ✅ |
| R5.4 | Per-job install footprint trimmed. | Should-have | ✅ | ✅ | ✅ |
| R6.1 | Recording-flow tests (`ci-e2e.sh` / `mute` / `rejoin`) keep running as independent value. | Must-have | ✅ | ✅ | ✅ |
| R6.2 | GPU transcript-verify (`ci-e2e-v3-transcript-verify.sh`) keeps running as independent value. | Must-have | ✅ | ✅ | ✅ |
| R6.3 | Total CI wall time on a typical PR doesn't increase materially despite broader coverage. | Should-have | ❌ | ❌ | ❌ |

**Notes (post-spike, 2026-05-28):**

- 🟡 **R2.1 / R2.2 lifted to ✅** by spike X1: HMAC POST through AppAPI proxy reaches the operator's recording handler. Conditional on **Slice 0** (Talk-secret provisioning) for full pass at slice-implementation time. See `spike-x1-recording-via-proxy.md`.
- 🟡 **R3.2 lifted to ✅** by spike X2: 5 migration items identified (3 Go unit tests + 2 added Shape-C phase assertions). Slice 4 (retire bypass tests) is unblocked. See `spike-x2-bypass-coverage-audit.md`.
- **R6.3 still ❌ across the board** — likely-real wall-time increase. Current install-lane jobs run in parallel with longest at ~5 min; a 12-min full-path job extends PR wall clock by ~7 min (though overall PR wall clock is gated by `Build CUDA ExApp image` at ~10 min, so the effective stretch is ~2 min). Should-have; Slice 5 (CI efficiency pass) targets it.
- **R1.4 ✅ all** — all three shapes drive a viewer fetch as `alice` after install; the ACL assertion is the same in each.
- **R5 row ✅ all** — those requirements are about engineering hygiene, not shape choice.

---

## Spikes (resolved 2026-05-28)

- ✅ **X1** — Recording trigger via AppAPI proxy + HaRP. Mechanism sound; surfaced the Talk-secret provisioning gap (now Slice 0). See `spike-x1-recording-via-proxy.md`.
- ✅ **X2** — Bypass-test coverage audit. 5-item migration list captured. See `spike-x2-bypass-coverage-audit.md`.

Both spikes were independent of the A/B/C choice; they had to happen for any shape to claim R2 and R3.

---

## Slices (Shape C)

| Slice | Outcome | Depends on |
|---|---|---|
| **Slice 0** | ExApp install provisions a Talk shared secret to the operator container (via `<environment-variables>` in `info.xml` + matching plumbing into Nextcloud's `spreed.recording_servers.secret`). Surfaced by spike X1. | — |
| **Slice 1** | `harness/bin/ci-e2e-install-prod-path.sh` (the CI-shape of `manual-test-setup.sh`) green as a new CI job. Phase=install only. | H3 (PR #40 compose.yml additions on main), Slice 0 (not strictly — install reaches `[enabled]` without secret) |
| **Slice 2** | Phase=record green. Talk recording trigger via proxy → operator → transcript published. | Slice 0 (without secret the trigger 403s), Slice 1, X2 |
| **Slice 3** | Phase=viewer green. `alice` fetches viewer page through proxy and reads the published meeting. | Slice 2 |
| **Slice 4** | Bypass tests retired (`ci-e2e-exapp.sh`, `ci-e2e-install-exapp.sh`, `ci-e2e-talk-record-roundtrip.sh` CPU+GPU). | X2 (audit migration list complete), Slice 3 |
| **Slice 5** | CI efficiency pass (R5): profile, split implicit steps, cache by lockfile/model hash, trim per-job install footprint. | Slice 4 |

## Next steps

1. **Spike X2** — read-only audit of bypass-test assertions. Output: migration list.
2. **Re-run Fit Check** with X1/X2 flags lifted.
3. **Detail Shape C** with the `/breadboarding` skill (UI + non-UI affordances + wiring across Place).
4. **Implement Slice 0**, then Slice 1, in PR order.
