---
shaping: true
---

# Nextcloud Store Readiness — Brief

Status: full reconciliation with main and authenticated PR metadata
Phase: 2
Date: 2026-06-22

## What This Initiative Is

This initiative prepares Cassini for distribution through the Nextcloud app marketplace / app store.

Cassini already has a serious ExApp path: `appinfo/info.xml`, AppAPI routes, basic App Store package stubs, Docker images, lifecycle handlers, embedded viewer and control panel UI entries, pinned image tags, persistent storage defaults, install documentation, manual HaRP dogfood setup, and CI around image build and install behavior. Store readiness is the next step: turn that installable technical shape into a signed, reviewable, supportable, versioned Nextcloud app release.

The researched framing/checklist now lives in `planning/initiatives/nextcloud-store-readiness/framing.md`. This brief keeps the initiative goal and decision frame; the framing document is the current source for requirements, blocker classification, open PR evidence, and slice boundaries.

Production ExApp recovery is now split out to `planning/initiatives/production-exapp-recovery/`. That initiative owns the operational failure hypothesis, container-suite versus deployed-ExApp mismatch exploration, and production recovery checklist. Store readiness should reference it as a dependency instead of carrying that exploration here.

## Why This Matters

Manual ExApp installation is useful for dogfooding and pilots, but it is not enough for normal adoption.

Store readiness matters because it gives Cassini:

- discoverability in the Nextcloud ecosystem
- a normal admin installation path
- a repeatable release process
- clearer compatibility claims
- external pressure to document security, privacy, storage, and support behavior
- a concrete milestone for Phase 2 product quality

The initiative is not only "submit a form." It is a packaging, compliance, documentation, release, and product-readiness effort.

## Current Baseline

Relevant current state:

- `appinfo/info.xml` defines the app id `gocassini`, metadata, ExApp docker install image, routes, environment variables, and Nextcloud dependency range.
- `appinfo/app.php` and `img/app.svg` exist as basic Nextcloud App Store package/icon stubs from PR #40.
- The image tag in `info.xml` is pinned to the app version for reproducible installs.
- CI builds and publishes CPU and CUDA ExApp images to `ghcr.io/codemyriad/gocassini`.
- CI validates manifest basics and release tag consistency.
- The ExApp has AppAPI middleware, lifecycle callbacks, persistent storage defaults, heartbeat, health, and status behavior.
- The viewer and control panel are registered as Nextcloud navigation entries and run on AppAPI embedded pages.
- `docs/exapp-install.md` provides a production install and Talk handoff guide, but its Talk test/limitation section is stale after HPB-internal capture landed.
- `docs/exapp-test-locally.md` documents image-only, manual AppAPI install, and production-shaped HaRP testing tiers; the HaRP tier remains manual rather than CI-gated on `main`.
- Manual-install Nextcloud/AppAPI e2e exists in CI, and `harness/bin/manual-test-setup.sh` provides a local HaRP/reverse-proxy dogfood setup. Full D-290 prod-path CI remains in open draft PR #41.
- A real Talk recording roundtrip is covered in CI for CPU and CUDA paths.
- Main now includes the HPB-internal Talk capture work (`D-283`) and private/1:1 validation work (`D-288-1-1`). The intended Talk path is no longer guest/public-only, but the ExApp manifest and install docs have not caught up.
- Nextcloud docs now confirm ExApps follow the same App Store publishing process as regular apps, with required `<external-app>` metadata in `appinfo/info.xml`.
- The repo does not yet have a store archive/signing workflow, root license file, `CHANGELOG.md`, screenshots, app-store certificate, or `appinfo/signature.json`.
- The GitHub repository is currently private, so public repository, issue tracker, support, and GHCR package visibility need a release decision before marketplace submission.
- `appinfo/info.xml` does not yet declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, so AppAPI will drop the env var needed by the default HPB-internal recorder path.
- Access control is still not product-complete: viewer and published assets are currently logged-in-user-wide unless the access-control initiative changes that.
- Storage and delivery are not product-settled: Cassini still treats the AppAPI persistent volume as the rich artifact source of truth, while Talk's native recorder model stores files in the owner's Nextcloud Files.

## Problem Statement

Cassini needs to become a store-submittable Nextcloud app rather than only a manually registered ExApp image.

The initiative must answer:

- what exact submission path Nextcloud currently expects for ExApps
- whether Krankerl, app signing, app bundles, or store-specific metadata are required
- whether the current `appinfo/info.xml` is enough or needs store-specific fields/assets
- how Docker image tags, app versions, git tags, and store releases line up
- what compatibility claims are safe for Nextcloud, AppAPI, HaRP, CPU, CUDA, ROCm, and arm64
- what security and privacy notes must be complete before submission
- which Phase 2 product gaps block store readiness versus which can be documented limitations
- whether the store-ready product keeps Cassini-owned storage authoritative, moves built artifacts into Nextcloud Files, or ships a hybrid transition

Current researched answer: an ExApp is published through the normal Nextcloud App Store flow. Cassini needs a signed app archive whose top-level folder matches `gocassini`, a valid `appinfo/info.xml` with ExApp metadata, a release archive signature, code signing, app-store registration, app-store metadata, and Docker images reachable from the declared registry/tag.

## Confirmed External Requirements

| Area | Requirement |
|---|---|
| Publishing path | Use the App Store developer web UI or REST API after registering the app id with an approved certificate. |
| Release artifact | Upload a `tar.gz` archive with one top-level folder named exactly like the app id. |
| Signing | App Store releases require code signing and an archive signature generated from the app private key. |
| Metadata | Store metadata is read from `appinfo/info.xml` and `CHANGELOG.md`; `info.xml` is schema-validated. |
| ExApp metadata | ExApps need `<external-app><docker-install>` fields for registry, image, and image-tag. |
| App rules | Apps must use public Nextcloud APIs, follow design/HTML/CSS guidelines, handle upgrades/uninstall, communicate purpose/features, respect privacy, and provide contact/support. |
| Privacy | Data leaving the instance must be clearly explained and minimized. |
| HaRP | HaRP support is strongly recommended for Nextcloud 32+ because the older Docker Socket Proxy path is deprecated toward removal. |

## Core Requirements

| ID | Requirement | Status |
|---|---|---|
| R0 | Cassini has a confirmed Nextcloud app-store submission path for its ExApp shape. | Core goal |
| R1 | Release artifacts are reproducible and version-consistent. | Must-have |
| R2 | Store metadata, screenshots, icon, descriptions, links, and support information are prepared. | Must-have |
| R3 | Security, privacy, and data-processing behavior are documented accurately. | Must-have |
| R4 | Install, upgrade, rollback, uninstall, and Talk handoff docs are reviewable by a normal admin. | Must-have |
| R5 | CI gates the release path enough to avoid broken store submissions. | Must-have |
| R6 | Compatibility claims are tested or explicitly limited. | Must-have |
| R7 | Known product limitations are either fixed, documented, or deliberately deferred. | Must-have |
| R8 | Marketplace submission does not depend on hidden local knowledge or one-off manual edits. | Must-have |

## Store-Blocking Questions

These questions need final answers before we can call the initiative done. Initial researched answers and current blocker classification are in `framing.md`.

- Does the Nextcloud store currently accept AppAPI ExApps directly, and if so what artifact is submitted?
- Is `krankerl` required for ExApps, or only for classic PHP apps?
- Does the store require app signing for an ExApp whose runtime is a Docker image?
- Does the store validate `appinfo/info.xml` against the public app schema only, or also against AppAPI-specific rules?
- Are external Docker images allowed to be hosted on GHCR, or must they follow specific registry/tag conventions?
- Are version tags immutable enough for store review, or do we need digest pinning in addition to semantic image tags?
- What review expectations apply to apps that record meetings and process transcripts?
- What review expectations apply to optional LLM/API usage?
- Can the app be listed before private/group Talk recording is proven through production-shaped AppAPI/HaRP validation?
- Can the app be listed while viewer access control is not yet complete, or is that a hard blocker?

## Product Readiness Gates

The initiative should separate submission mechanics from product readiness.

Likely hard gates:

- installable ExApp through AppAPI with a pinned release image
- working viewer and control-panel navigation entries
- working Talk recording-backend handoff
- durable persistent storage and migrations
- no committed secrets
- clear admin setup and rollback docs
- clear privacy/data-processing docs
- tested route access levels and middleware behavior
- HPB-internal Talk capture is configurable through the ExApp manifest and admin UI, including `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
- install docs match the current HPB-internal/default capture model rather than the old guest/public-only model
- admin status/doctor output reports all required Talk config presence without leaking secret values
- no known universal recording visibility leak unless explicitly accepted as a documented limitation

Likely soft gates or documented limitations:

- CUDA support may be optional and deployment-specific
- ROCm support may remain unsupported or CPU-aliased
- arm64 may be deferred if the store does not require it
- private/group Talk recording may still be limited by production validation, but it is no longer only a future code path after D-283/D-288-1-1
- LLM summaries may remain optional and disabled without API keys

The next shaping session should decide which items are blockers for a first public listing.

## In Scope

- investigate the current Nextcloud app-store submission process for AppAPI ExApps
- define the required release artifact and packaging process
- prepare or update store metadata
- prepare screenshots and visual assets
- validate `appinfo/info.xml` against store expectations
- document permissions, routes, environment variables, storage, and data processing
- document HPB-internal Talk capture configuration, fallback behavior, and production validation
- decide and document the storage source-of-truth model across Cassini volume, Talk raw upload, post-build Files delivery, and `/published/*`
- document admin setup, Talk handoff, health checks, upgrade, rollback, and uninstall
- define release/versioning policy across app version, image tags, git tags, and docs
- harden CI around release submission checks
- identify product gaps that block store submission
- create a final release checklist

## Explicitly Out Of Scope For This Initiative

- implementing viewer access control itself, except as a blocker/dependency
- implementing private Talk conversation support itself, except as a blocker/dependency
- building a managed hosted service or billing product
- adding non-Nextcloud meeting platforms
- redesigning the viewer/control panel unrelated to store requirements
- guaranteeing every possible self-hosted topology before first listing
- actual submission before investigation and readiness gates are complete

## Dependencies

Primary dependency:

- `planning/initiatives/viewer-access-control/` if marketplace readiness requires per-recording privacy before listing.

Other likely dependencies:

- private/group Talk conversation support if the marketplace or pilot target requires it
- owner delivery to Nextcloud Files if the product promise includes Files integration rather than viewer-only consumption
- ExApp env parity for HPB-internal capture before production Talk recording is advertised
- production validation for archive preservation if published catalogs can collapse when `<work-root>/current` contains only the latest meeting
- retention/deletion policy if privacy review or customer expectations require it
- release version bump automation and CI if store artifacts depend on exact version alignment

## Success Criteria

This initiative is successful when:

- the team knows exactly how a Cassini ExApp is submitted to the Nextcloud store
- the repo contains all store metadata and assets needed for the submission path
- release CI validates version, manifest, image, and docs consistency
- installation and Talk handoff docs are good enough for a Nextcloud admin without project context
- privacy, security, data retention, LLM, and recording-consent behavior are documented
- known limitations are explicit and classified as blockers or accepted caveats
- a dry-run or pre-submission checklist can be executed without hidden steps
- Cassini is ready for a real store submission or has a short, explicit blocker list

## Key Decisions Needed

- Is the first public listing allowed before HPB-internal production ExApp setup is fully documented and validated?
- Is per-recording viewer access control mandatory before store submission?
- Is owner delivery to Nextcloud Files mandatory before store submission?
- Is the first storage model Cassini-owned, Nextcloud-Files-native, or a hybrid?
- Should CUDA be advertised in the first listing, or kept as advanced documentation?
- Should arm64 be supported before the first listing?
- Should ROCm be listed as unsupported rather than shipped as an alias?
- What support channel should the store listing expose?
- What license and third-party dependency notices are required for bundled recorder, transcription, model, CUDA, and frontend assets?
- Who owns the final review and submission account/process?

## References

- `planning/installable-nextcloud-app.md`
- `planning/initiatives/nextcloud-store-readiness/framing.md`
- `planning/initiatives/production-exapp-recovery/brief.md`
- `planning/initiatives/production-exapp-recovery/production-failure-hypothesis.md`
- `planning/initiatives/production-exapp-recovery/exapp-suite-mismatch-exploration.md`
- `planning/initiatives/production-exapp-recovery/archive-overwrite-hypothesis.md`
- `docs/exapp-install.md`
- `docs/exapp-test-locally.md`
- `appinfo/info.xml`
- `appinfo/app.php`
- `img/app.svg`
- `harness/bin/manual-test-setup.sh`
- `.github/workflows/publish-exapp-image.yml`
- `deployment/Dockerfile.exapp`
- `deployment/Dockerfile.exapp.cuda`
