# P2 — Nextcloud marketplace readiness

> Make Cassini a first-class, distributable Nextcloud app — installable as an ExApp and shipped as ready-to-run runtime images.

- **Initiative:** [Cassini MVP](../initiative-cassini-mvp.md) · **Phase:** PHASE-1
- **Status:** ◑ In progress — ExApp v1 shipped; runtime image bundling active
- **Last updated:** 2026-06-23

## What success looks like

A Nextcloud administrator can adopt Cassini without any bespoke build steps. Cassini installs as a first-class Nextcloud **ExApp** through AppAPI — manifest, lifecycle middleware, and static asset serving all behave like a native marketplace app — and the heavy runtime dependencies (notably the Parakeet STT model) arrive as **prebuilt, ready-to-run images** rather than something the operator assembles locally. The end state: "install the app, pull the images, run" is the whole adoption path, complementing P1's self-host bundle ([G6](G6-self-host-deployment.md)) with the marketplace/packaging surface.

## Scope

- ExApp packaging: app manifest, `Dockerfile.exapp`, AppAPI middleware/lifecycle, static asset serving, E2E harness.
- Prebuilt runtime images bundling the Parakeet STT model (CPU + CUDA variants).
- Distribution ergonomics so install requires no local build steps.

## Out of scope

- The internal MVP product surface (operator, control panel, viewer, summaries) — owned by [P1](P1-cassini-internal-mvp.md).
- App-store / krankerl submission (future).
- Multi-arch builds and real GPU-validated variants (future).
- Full Nextcloud + AppAPI install E2E in CI (deferred).

## Goals done

| Goal | Focus |
| --- | --- |
| Installable Nextcloud ExApp ✅ (v1 verified 2026-05-19) | Manifest, `Dockerfile.exapp`, AppAPI middleware/lifecycle, static asset serving, E2E harness — see [installable-nextcloud-app.md](P2-nextcloud-marketplace-readiness/installable-nextcloud-app.md). |

## Goals TODO

| Goal | Status | Outstanding |
| --- | --- | --- |
| Bundled Parakeet runtime images | ◑ Active | CPU + CUDA model image variants (PRs #31 / #32) — see [bundled-parakeet-images.md](P2-nextcloud-marketplace-readiness/bundled-parakeet-images.md). |
| Hardened distribution | ⏳ Future | Full NC+AppAPI install E2E in CI; viewer relative→absolute paths; multi-arch builds; real GPU variants; krankerl / app-store submission. |

## Gaps to completion

ExApp v1 is shipped and verified. Remaining toward a marketplace-ready distribution:

- Finish Parakeet runtime image bundling (CPU done / CUDA in progress).
- Resolve viewer relative → absolute path handling for app-hosted serving.
- Stand up full Nextcloud + AppAPI install E2E in CI.
- Multi-arch + real GPU-validated image variants.
- krankerl packaging and app-store submission.

> Post-MVP distribution goals are intentionally lightweight and not yet decomposed into Linear tasks; they are tracked here against the two reference docs.

## Shaping & reference docs

In [`P2-nextcloud-marketplace-readiness/`](P2-nextcloud-marketplace-readiness/):

- [installable-nextcloud-app.md](P2-nextcloud-marketplace-readiness/installable-nextcloud-app.md) — ExApp packaging shaping and v1 outcome.
- [bundled-parakeet-images.md](P2-nextcloud-marketplace-readiness/bundled-parakeet-images.md) — bundled Parakeet runtime image strategy (CPU/CUDA).
