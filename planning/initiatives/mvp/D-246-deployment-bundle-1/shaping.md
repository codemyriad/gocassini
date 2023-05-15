---
shaping: true
---

# Deployment bundle 1 — Shaping

This document shapes Linear **D-246**.

It elaborates:

- `planning/initiatives/mvp/shaping.md` (V6 self-host bundle)
- `cassini-operator/README.md`
- `cassini-control-panel/README.md`
- `cassini-viewer/README.md`

## Working position

**Provisional working shape: B — three separate services in one compose bundle, with fixed in-container defaults, host-bound public ports, compose-managed shared volumes, and control-panel same-origin proxying back through the host-published operator port.**

Reason: it matches the ticket goal and your latest config clarification better than a merged service or an out-of-band proxy setup, while keeping the deployment-facing contract narrow instead of exposing every in-container path/bind detail as config.

Big unknowns still to resolve:

- none at shaping level right now; next step is to lock the concrete bundle layout and then breadboard it

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | A repo-root deployment bundle can start `cassini-operator`, `cassini-control-panel`, and `cassini-viewer` with `docker compose up`, without the Nextcloud dev harness. | Core goal |
| R1 | The control panel talks only to operator HTTP/SSE, and the viewer consumes only operator-published site output from shared storage. Neither browser surface reads operator DB/work-root internals directly. | Must-have |
| R2 | **Configuration contract** | |
| R2.1 | 🟡 All deployment-facing knobs are env-driven and use `CASSINI_*` names, but only genuinely external/runtime knobs need to be exposed. | Must-have |
| R2.2 | 🟡 Operator in-container bind settings are fixed defaults (`0.0.0.0:4000`) rather than user-facing config; deployment config should focus on host-published ports and service behavior, not internal listen details. | Must-have |
| R2.3 | 🟡 Shared DB/work/site/cache roots may be fixed in-container paths, with compose controlling whether they map to named volumes or bind mounts; the viewer gets the published site root read-only. | Must-have |
| R2.4 | 🟡 Record/build worker counts remain configurable. | Must-have |
| 🟡 R2.5 | 🟡 One shared API base-path setting should be available for the deployment bundle: it prefixes operator HTTP routes and tells the control panel which same-origin API path to call. Default should be `/`. | Must-have |
| R3 | The operator container preserves the current shell boundary: `cassini-operator` still runs `cassini doctor`, `cassini record`, `cassini build`, and `cassini publish` successfully inside the image. | Must-have |
| R4 | The control-panel service is self-contained in the bundle and does not require an out-of-band reverse proxy or operator-side browser CORS policy to function. | Must-have |
| R5 | The viewer service is self-contained, serves the operator-published library from shared storage, and does not mutate that storage. | Must-have |
| R6 | Each shipped service has its own Dockerfile, and the bundle lives in a dedicated repo-root folder with `.env` defaults. | Must-have |

---

## CURRENT: Repo and runtime baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | The repo already has a Docker Compose stack under `harness/compose.yml`, but it is only the local Nextcloud/Talk dev harness, not the product deployment bundle. | |
| **CURRENT2** | `cassini-control-panel` currently uses browser-side `CASSINI_OPERATOR_BASE_PATH` and Vite dev/preview proxy-side `CASSINI_OPERATOR_URL`. It does not yet expose a split host/port deployment contract. | |
| **CURRENT3** | `cassini-operator` currently binds via `--bind` (default `127.0.0.1:8080`) and still uses a mixed env surface: `CASSINI_BIN` plus unprefixed `WORK_ROOT`, `SITE_ROOT`, `MAX_RECORD_WORKERS`, and `MAX_BUILD_WORKERS`. | |
| **CURRENT4** | `cassini-operator` shells out to `cassini`, and `cassini publish` shells out again to `cassini-publisher/bin/export-static-meetings.sh`, which currently needs `node`/`npm` plus repo-local `cassini-viewer` files. | |
| **CURRENT5** | The operator already owns the durable runtime state: SQLite DB, work-root bundles, and published site root. That published site root is the natural operator → viewer handoff boundary. | |
| **CURRENT6** | `cassini-viewer` is already static-build friendly, and `cassini serve` can serve a generated site root, but there is no viewer service image/Dockerfile yet. | |
| **CURRENT7** | There is no repo-root deployment-bundle folder yet with service Dockerfiles, compose file, or `.env` defaults. | |
| **CURRENT8** | The operator deliberately assumes a same-origin UI proxy and does not currently solve browser CORS itself. | |

---

## A: Minimal containerization around today's runtimes

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Build an operator image that ships prebuilt `cassini-operator` and `cassini` binaries plus the current publish/export runtime (`node`/`npm`, publisher script, viewer files) and runs the current operator almost unchanged. | ⚠️ |
| **A2** | Build `cassini-control-panel` directly and run it with `vite preview`, keeping the existing same-origin `/operator` proxy model inside the service container. | ⚠️ |
| **A3** | Add a `cassini-viewer` service that serves the shared published site root read-only, using either `cassini serve` or a minimal static file server. | ⚠️ |
| **A4** | Add `CASSINI_*` aliases around the current operator/config surface, plus compose volumes and `.env` defaults, with as little behavior change as possible. | ⚠️ |

## B: Packaged three-service bundle with fixed in-container defaults and host-bound public config

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Build an operator image that ships prebuilt `cassini-operator` and `cassini` binaries plus the runtime pieces needed for `doctor`/`record`/`build`/`publish`, and stop depending on the repo's temp-build wrapper scripts inside the container. | ⚠️ |
| **B2** | Define a narrow `CASSINI_*` deployment contract around host-published service ports, worker counts, and shared-volume behavior, while keeping in-container bind/path defaults fixed. | ⚠️ |
| **B3** | Build `cassini-control-panel` as its own service and keep the browser boundary same-origin: the service itself serves the app and proxies `/operator` back through the host-published operator port. | ⚠️ |
| **B4** | Build `cassini-viewer` as its own service that serves the operator-owned published site root from a read-only shared volume. | |
| **B5** | Add a dedicated repo-root deployment folder with compose file, `.env` defaults, named volumes, and one Dockerfile per shipped service. | |

## C: External proxy or merged-service packaging

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Keep the services incomplete on their own and require an out-of-band nginx/caddy/traefik deployment to wire them together. | |
| **C2** | Or collapse viewer/control-panel concerns into another service boundary instead of keeping three explicit services with a shared contract. | |

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | A repo-root deployment bundle can start `cassini-operator`, `cassini-control-panel`, and `cassini-viewer` with `docker compose up`, without the Nextcloud dev harness. | Core goal | ✅ | ✅ | ❌ |
| R1 | The control panel talks only to operator HTTP/SSE, and the viewer consumes only operator-published site output from shared storage. Neither browser surface reads operator DB/work-root internals directly. | Must-have | ✅ | ✅ | ❌ |
| R2.1 | 🟡 All deployment-facing knobs are env-driven and use `CASSINI_*` names, but only genuinely external/runtime knobs need to be exposed. | Must-have | ✅ | ✅ | ❌ |
| R2.2 | 🟡 Operator in-container bind settings are fixed defaults (`0.0.0.0:4000`) rather than user-facing config; deployment config should focus on host-published ports and service behavior, not internal listen details. | Must-have | ❌ | ✅ | ❌ |
| R2.3 | 🟡 Shared DB/work/site/cache roots may be fixed in-container paths, with compose controlling whether they map to named volumes or bind mounts; the viewer gets the published site root read-only. | Must-have | ✅ | ✅ | ❌ |
| R2.4 | 🟡 Record/build worker counts remain configurable. | Must-have | ✅ | ✅ | ❌ |
| R3 | The operator container preserves the current shell boundary: `cassini-operator` still runs `cassini doctor`, `cassini record`, `cassini build`, and `cassini publish` successfully inside the image. | Must-have | ✅ | ✅ | ✅ |
| R4 | The control-panel service is self-contained in the bundle and does not require an out-of-band reverse proxy or operator-side browser CORS policy to function. | Must-have | ✅ | ✅ | ❌ |
| R5 | The viewer service is self-contained, serves the operator-published library from shared storage, and does not mutate that storage. | Must-have | ✅ | ✅ | ❌ |
| R6 | Each shipped service has its own Dockerfile, and the bundle lives in a dedicated repo-root folder with `.env` defaults. | Must-have | ✅ | ✅ | ❌ |

**Notes:**

- **A** is still the smallest implementation path, but it currently fails the clarified config-contract requirement because it does not cleanly codify the "fixed in-container defaults + host-published public knobs" model.
- **B** is the best current fit because it preserves the three-service boundary, keeps same-origin browser behavior inside the bundle, and narrows configuration to the knobs you actually care about.
- **C** fails the self-contained bundle requirement and drifts away from the explicit three-service direction.

---

## Working shape: B

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | **Operator runtime image** | |
| B1.1 | Multi-stage Dockerfile builds prebuilt `cassini-operator` and `cassini` binaries into the image rather than relying on repo temp-build wrappers. | |
| 🟡 B1.2 | 🟡 The operator image also carries the extra runtime pieces needed by the current shell boundary: `ffmpeg`, `ffprobe`, writable cache/temp roots, `node`/`npm`, and the publish/export runtime subtree. | |
| B1.3 | The operator runs against explicit mounted DB/work/site/cache roots so container restarts do not discard durable state. | |
| **B2** | **Config contract** | |
| 🟡 B2.1 | 🟡 Fix operator in-container bind defaults at `0.0.0.0:4000` instead of exposing them as user-facing config. | |
| 🟡 B2.2 | 🟡 Expose only the public/deployment knobs through `CASSINI_*`: host-published service ports, worker counts, and one shared API base-path setting defaulting to `/`. | |
| 🟡 B2.3 | 🟡 Keep DB/work/site/cache roots as fixed in-container paths; compose decides named volume vs bind mount, and the viewer mounts the published site root read-only. | |
| 🟡 B2.4 | 🟡 Route control-panel → operator traffic through the host-published operator port using host networking for the relevant service path in v1, rather than container-name discovery or a special internal-only API address. | |
| **B3** | **Control-panel service** | |
| B3.1 | Build `cassini-control-panel` directly from its package. | |
| 🟡 B3.2 | 🟡 Serve the built app and proxy the shared same-origin operator API base path back through the host-published operator port from inside the service container; for v1 we can rely on host networking to make that straightforward. | |
| 🟡 B3.3 | 🟡 When `CASSINI_OPERATOR_BASE_PATH=/`, special-case the proxy so only operator API routes (`/jobs`, `/jobs/*`, `/events`) are forwarded and the control-panel app/assets still serve correctly from `/`. | |
| B3.4 | Keep browser traffic same-origin so the bundle does not need operator CORS work just to function. | |
| **B4** | **Viewer service** | |
| B4.1 | Serve the operator-owned published site root from a shared read-only volume. | |
| B4.2 | Keep viewer storage read-only; the operator remains the only writer of the published site output. | |
| **B5** | **Bundle packaging** | |
| B5.1 | Create a dedicated repo-root deployment folder with compose file and `.env` defaults. | |
| B5.2 | Ship one Dockerfile per service. | |
| 🟡 B5.3 | 🟡 Default shared storage to Docker-managed named volumes, with compose-level override paths available when we want bind mounts. | |
| **B6** | **Scope guardrails** | |
| B6.1 | Exclude the Nextcloud dev harness from this bundle. | |
| B6.2 | First pass targets `docker compose up`; a later `cassini prod stack up` wrapper can reuse the same bundle. | |
| B6.3 | Do not add new product features beyond packaging, config normalization, and service wiring. | |

---

## Decisions / extracted constraints so far

1. **🟡 Operator in-container bind defaults are fixed.** V1 should hardcode `0.0.0.0:4000` inside the operator container rather than exposing bind host/port as deployment knobs.
2. **🟡 Deployment-facing config should stay narrow.** The important user-facing knobs are host-published service ports, worker counts, and one shared API base-path contract.
3. **🟡 In-container runtime paths can stay fixed.** Compose should decide named volume vs bind mount; we do not need env-driven DB/work/site/cache path customization in the first pass.
4. **🟡 The operator image should ship its own binaries.** `cassini-operator` and `cassini` should be built into the image, and in-container env wiring should point at them without exposing bin-path config publicly.
5. **🟡 Bundle v1 keeps the current publish boundary.** That means the operator image also ships `node`/`npm` plus the minimal exporter/viewer runtime subtree needed by `cassini publish`.
6. **The published site root is the right operator → viewer contract.** Operator writes it, viewer serves it read-only.
7. **The bundle should preserve same-origin browser behavior.** That keeps the control panel thin and avoids turning CORS into a packaging prerequisite.
8. **🟡 Control-panel → operator traffic should go through the host-published operator port.** For v1 we can use host networking to make that path straightforward.
9. **🟡 The selected direction is one shared API base path.** It should affect both the operator route prefix and the control-panel request/proxy path, with default `/`.
10. **`docker compose up` is the first-class entrypoint for this ticket.** A later `cassini prod stack up` helper can wrap the same artifacts after the compose bundle exists.

## Spikes

- `./spike-service-config.md` — resolved env contract direction across public ports, shared API base path, host networking, and shared roots.
- `./spike-operator-runtime-dependencies.md` — resolved operator image contents needed for `doctor` / `record` / `build` / `publish`.

## Detailed artifacts

- `./breadboarding.md` — concrete deployment breadboard for the selected shape
- `./slice.md` — implementation slices derived from that breadboard

## Current slice order

1. **I1 — Packaged operator + control-panel surface**
2. **I2 — Shared published-site viewer surface**
3. **I3 — Final bundle contract + quickstart**

Breadboarding is now complete. If this still feels right, the next step after shaping is implementation against `./slice.md`.
