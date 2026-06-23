# P2 — Nextcloud marketplace readiness

> Make Cassini a first-class, distributable Nextcloud app — installable as an ExApp and shipped as ready-to-run runtime images.

- **Initiative:** [Cassini MVP](../../initiative-cassini-mvp.md) · **Phase:** PHASE-1 · **Status:** ◑ in progress (ExApp v1 shipped; runtime image bundling active)

## What success looks like

A Nextcloud administrator can adopt Cassini without any bespoke build steps. Cassini installs as a first-class Nextcloud **ExApp** through AppAPI — manifest, lifecycle middleware, and static asset serving all behave like a native marketplace app — and the heavy runtime dependencies (notably the Parakeet STT model) arrive as **prebuilt, ready-to-run images** rather than something the operator has to assemble locally. The end state is a distribution story where "install the app, pull the images, run" is the whole adoption path, complementing P1's self-host bundle (see [G6 self-host-deployment](../G6-self-host-deployment/goal.md)) with the marketplace/packaging surface.

## Scope

- ExApp packaging: app manifest, `Dockerfile.exapp`, AppAPI middleware/lifecycle, static asset serving, E2E harness.
- Prebuilt runtime images bundling the Parakeet STT model (CPU + CUDA variants).
- Distribution ergonomics so install requires no local build steps.

## Out of scope

- The internal MVP product surface (operator, control panel, viewer, summaries) — owned by P1.
- App-store / krankerl submission (future).
- Multi-arch builds and real GPU-validated variants (future).
- Full Nextcloud + AppAPI install E2E in CI (deferred).

## Goals

Post-MVP distribution goals are intentionally lightweight and not yet fully decomposed into Linear tasks; they are tracked here against the two reference shaping docs.

| Goal | Status | Focus |
| --- | --- | --- |
| Installable Nextcloud ExApp | ✅ v1 shipped (verified 2026-05-19) | Manifest, `Dockerfile.exapp`, AppAPI middleware/lifecycle, static asset serving, E2E harness — see [./installable-nextcloud-app.md](./installable-nextcloud-app.md) |
| Bundled Parakeet runtime images | ◑ active | CPU + CUDA model image variants (PRs #31 / #32) — see [./bundled-parakeet-images.md](./bundled-parakeet-images.md) |

## Done criteria

- [x] Cassini installs as a Nextcloud ExApp via AppAPI (manifest + `Dockerfile.exapp` + lifecycle middleware + static asset serving).
- [x] ExApp v1 verified end-to-end through the E2E harness (2026-05-19).
- [ ] Parakeet STT model shipped as prebuilt runtime images (CPU + CUDA variants).
- [ ] Viewer relative → absolute path handling resolved for app-hosted serving.
- [ ] Full Nextcloud + AppAPI install E2E running in CI.
- [ ] Multi-arch builds and real GPU-validated variants.
- [ ] krankerl packaging / app-store submission.

## Shaping & reference docs

- [./installable-nextcloud-app.md](./installable-nextcloud-app.md) — ExApp packaging shaping and v1 outcome.
- [./bundled-parakeet-images.md](./bundled-parakeet-images.md) — bundled Parakeet runtime image strategy (CPU/CUDA).
- Related: [G6 self-host-deployment](../G6-self-host-deployment/goal.md) — shared deployment/runtime concern from P1.
