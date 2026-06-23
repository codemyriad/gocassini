# Make gocassini an installable Nextcloud ExApp (v1, CPU-only)

Branch: `installable-nextcloud-app`
Reviewed: 2026-05-19 via `/plan-eng-review` + codex outside-voice challenge.
Reference: `/home/silvio/dev/app-skeleton-python/article/external-apps.md` (AppAPI protocol spec).

## Goal

Ship a Nextcloud AppAPI-compatible Docker image to `ghcr.io/codemyriad/gocassini` that admins install via the Nextcloud admin UI (manual image URL paste). The operator UI (control-panel) is reachable as an admin route; the viewer + published recordings as a USER route (org-wide archive).

## What this is NOT

- Not GPU-enabled. CPU-only v1.
- Not in the Nextcloud app store (skip krankerl).
- Not changing the recording-source model. The Talk-recorder bot still uses its own credentials.
- Not refactoring the publish pipeline. `cassini publish` still shells to the Node static exporter inside the image.
- Not redesigning per-recording ACLs. USER access = full archive visibility for now.

## What already exists

| Today | Status |
|---|---|
| `deployment/Dockerfile.operator` | Reusable as base; keeps Node 22 slim runtime (publish needs it). |
| `deployment/Dockerfile.control-panel` | Will be removed; its `dist/` becomes embedded in operator. |
| `deployment/Dockerfile.viewer` | Will be removed; its `dist/` becomes embedded in operator. |
| `deployment/compose.yml` | Stays for local dev. Add an `exapp` profile or keep separate. |
| `cassini-operator` (`internal/operator/run.go`) | Has `BasePath` via `CASSINI_OPERATOR_BASE_PATH`. Already cookieless. Will gain APP_HOST/APP_PORT env. |
| `harness/bin/ci-e2e.sh` | Existing Nextcloud-Talk E2E harness. Extend pattern for `ci-e2e-exapp.sh`. |
| `cassini-viewer/dist` | Pre-built SPA, currently served by the viewer container; will be embedded. Fetches `./catalog.json` (relative) — needs absolute-path refactor. |
| `cassini-control-panel/dist` | Pre-built Svelte app. Reads runtime config from `window.__CASSINI_CONFIG__.operatorBasePath`. Will be rebuilt with `base=/control-panel/` and re-embedded. |

## Locked decisions

| # | Decision | Choice |
|---|---|---|
| D1 | Scope | Full ExApp protocol (middleware + lifecycle + frpc + manifest + CI publish) |
| D2 | Container shape | Operator serves all three. One process, one binary. (Node still in image for publish.) |
| D3 | Access policy | Viewer = USER, operator + control-panel = ADMIN |
| D4 | Auth gate | Active iff `APP_SECRET` set; `CASSINI_APPAPI_REQUIRED=true` (set in ExApp Dockerfile) makes startup fail without secret |
| D5 | Storage | Document required volumes in README + log warning on detected ephemeral storage |
| D6 | Static assets | `go:embed` at compile time (control-panel and viewer dists copied into operator module pre-build) |
| D7 | Distribution | Skip krankerl, manual image-URL install for v1 |
| D8 | Tests | Add `harness/bin/ci-e2e-exapp.sh` (full install + enable + ADMIN/USER ping + SSE) |
| D10 | Recording ACL | Org-wide archive — any logged-in NC user reads any recording |
| D11 | Embed shape | Both viewer and control-panel embedded under separate prefixes, with explicit Vite `base=` rebuilds and absolute-path refactor of viewer's `catalog.json` fetches |
| D12 | Sequencing | **Slice 0 (spike) first** — prove the manual-install path against a local Nextcloud, then run lanes |
| D13 | SSE | Trust HaRP passes through cleanly; do not pre-emptively switch to polling. **Unresolved risk** — see Failure modes. |
| D14 | FRP local target | TCP localhost on `$APP_PORT` |
| D15 | Tag variants | Publish stub `latest-cuda` and `latest-rocm` aliases of `latest` so GPU-tagged daemons don't error on pull |

## Architecture

```
                       ┌─────────────────────────────────────────┐
                       │ ghcr.io/codemyriad/gocassini:<tag>      │
                       │                                          │
  ┌─────────┐   HaRP   │  frpc ─┐                                 │
  │  HaRP   │ ◀──────▶ │        ▼                                 │
  │  frps   │  tunnel  │     127.0.0.1:$APP_PORT                  │
  └─────────┘          │     ┌─────────────────────────────────┐  │
       ▲               │     │ cassini-operator (Go)           │  │
       │               │     │                                  │  │
  Nextcloud PHP        │     │  AppAPIAuthMiddleware            │  │
  proxy + ACL          │     │   ├─ verify AUTHORIZATION-APP-API│  │
       ▲               │     │   └─ extract userId / app meta   │  │
       │ proxied req   │     │                                  │  │
       │               │     │  routes (mounted at BasePath):   │  │
  ┌─────────┐          │     │   /control-panel/*   [ADMIN]     │  │
  │ Browser │          │     │   /operator/*        [ADMIN]     │  │
  └─────────┘          │     │   /viewer/*          [USER]      │  │
                       │     │   /published/*       [USER]      │  │
                       │     │                                  │  │
                       │     │  lifecycle (not in <routes>):    │  │
                       │     │   PUT  /enabled                  │  │
                       │     │   POST /init                     │  │
                       │     │                                  │  │
                       │     │  embedded static (go:embed):     │  │
                       │     │   control-panel/dist  /control-panel│
                       │     │   viewer/dist         /viewer    │  │
                       │     │   /srv/cassini-site/published     │  │
                       │     │                       /published │  │
                       │     │                                  │  │
                       │     │  subprocess (preserved):         │  │
                       │     │   cassini build / publish        │  │
                       │     │   (Node export-static-meetings)  │  │
                       │     └─────────────────────────────────┘  │
                       │                                          │
                       │  /var/lib/cassini-operator/  (volume)    │
                       │  /srv/cassini-site/          (volume)    │
                       └──────────────────────────────────────────┘
```

## info.xml shape

```xml
<info>
  <id>gocassini</id>
  <name>Cassini</name>
  <version>0.1.0</version>
  <dependencies>
    <nextcloud min-version="32" max-version="35"/>
  </dependencies>

  <external-app>
    <docker-install>
      <registry>ghcr.io</registry>
      <image>codemyriad/gocassini</image>
      <image-tag>latest</image-tag>
    </docker-install>

    <routes>
      <!-- ADMIN: control-panel UI -->
      <route><url>^/control-panel$</url><verb>GET</verb><access_level>ADMIN</access_level></route>
      <route><url>^/control-panel/.*$</url><verb>GET</verb><access_level>ADMIN</access_level></route>
      <route><url>^/control-panel/.*$</url><verb>HEAD</verb><access_level>ADMIN</access_level></route>

      <!-- ADMIN: operator JSON API -->
      <route><url>^/operator/jobs$</url><verb>GET</verb><access_level>ADMIN</access_level></route>
      <route><url>^/operator/jobs$</url><verb>POST</verb><access_level>ADMIN</access_level></route>
      <route><url>^/operator/jobs/[^/]+$</url><verb>GET</verb><access_level>ADMIN</access_level></route>
      <route><url>^/operator/jobs/[^/]+/stop$</url><verb>POST</verb><access_level>ADMIN</access_level></route>
      <route><url>^/operator/jobs/[^/]+/rerun$</url><verb>POST</verb><access_level>ADMIN</access_level></route>
      <route><url>^/operator/events$</url><verb>GET</verb><access_level>ADMIN</access_level></route>

      <!-- USER: viewer SPA + published recordings (org-wide archive) -->
      <route><url>^/viewer$</url><verb>GET</verb><access_level>USER</access_level></route>
      <route><url>^/viewer/.*$</url><verb>GET</verb><access_level>USER</access_level></route>
      <route><url>^/viewer/.*$</url><verb>HEAD</verb><access_level>USER</access_level></route>
      <route><url>^/published/.*$</url><verb>GET</verb><access_level>USER</access_level></route>
      <route><url>^/published/.*$</url><verb>HEAD</verb><access_level>USER</access_level></route>
    </routes>
  </external-app>
</info>
```

Note: `PUT /enabled` and `POST /init` are **lifecycle callbacks**, not entries in `<routes>`. AppAPI calls them on the app directly without proxying browser traffic. The Go handlers exist but are not declared as proxied routes.

## Implementation slices

### Slice 0 — Install-path spike (0.5-1 day)

Before any Go work. Goal: prove the manual-image-URL install path works end-to-end against a local Nextcloud + AppAPI.

- Hand-craft minimal `appinfo/info.xml` with one ADMIN route `^/ping$`.
- Hand-craft minimal `deployment/Dockerfile.spike` that bundles `frpc` + a tiny `start.sh` + a static binary that returns `200 OK` on `/ping` and 200 on `PUT /enabled`, `POST /init`.
- Build, push to a throwaway ghcr tag.
- Stand up a local Nextcloud (extend existing `harness/` patterns) with AppAPI app installed.
- Register Docker daemon, install the spike image via manual URL, hit `/ping` through the proxy, verify enable handshake.
- If this fails: stop. Re-evaluate (Python skeleton wrapper? Different distribution path? Different Nextcloud version?). All four lanes below depend on this working.

Output: confirmed-working install procedure documented in `docs/exapp-install.md`.

### Slice A — Go: middleware + lifecycle + binding (Lane A)

Files:
- `cassini-operator/internal/operator/appapi/middleware.go` — new package.
  - `Middleware(next http.Handler) http.Handler` wrapping all proxied routes.
  - `Authenticate(req)` decodes `AUTHORIZATION-APP-API`, verifies against `APP_SECRET` env, extracts userId, attaches to `request.Context()`.
  - Refuses request with no/malformed/wrong-secret header → 401. Logs `AA-REQUEST-ID` only (never the header value).
  - Skipped entirely when `APP_SECRET` env is empty (dev mode).
- `cassini-operator/internal/operator/appapi/middleware_test.go` — 8 unit cases.
- `cassini-operator/internal/operator/lifecycle.go` — new file.
  - `PUT /enabled` handler: idempotent enable/disable, persists state to existing SQLite DB.
  - `POST /init` handler: idempotent migration runner (already exists via `schema_migrations`).
- `cassini-operator/internal/operator/lifecycle_test.go` — happy path + idempotency + state-survives-restart regression.
- `cassini-operator/internal/operator/run.go` — modifications:
  - New env vars: `APP_HOST`, `APP_PORT`, `APP_SECRET`, `APP_ID`, `APP_VERSION`, `AA_VERSION`, `CASSINI_APPAPI_REQUIRED`.
  - When `APP_PORT` set: bind to `$APP_HOST:$APP_PORT` (default `APP_HOST=127.0.0.1`).
  - When `CASSINI_APPAPI_REQUIRED=true` and `APP_SECRET` empty: log fatal and exit non-zero.
  - When `APP_SECRET` set: wire `appapi.Middleware` over all proxied routes.
  - Wire lifecycle handlers as `mux.Handle("PUT /enabled", ...)` and `mux.Handle("POST /init", ...)` (NOT under `BasePath`, since AppAPI hits them at the root).
  - On startup: check `/var/lib/cassini-operator` and `/srv/cassini-site` mountpoint via `mountinfo` (or simpler: check if path is writable and warn if it looks tmpfs). Best-effort warning; do not block.
- `cassini-operator/internal/operator/run_test.go` — extend.
  - Regression: existing `-port` flag and `CASSINI_OPERATOR_PORT` env still work when `APP_PORT` unset.

### Slice B — Static asset embed + viewer path refactor (Lane B, sequential after A's run.go shape settles)

Files:
- `cassini-operator/embedded/control-panel/` — generated during Docker build, gitignored. `Dockerfile.exapp` runs `npm run build` with `VITE_BASE=/control-panel/` and copies `dist/` here.
- `cassini-operator/embedded/viewer/` — same pattern, `VITE_BASE=/viewer/`.
- `cassini-operator/internal/operator/static/embed.go` — `//go:embed control-panel viewer` (relative to `cassini-operator/embedded/...`).
- `cassini-operator/internal/operator/static/handler.go` — SPA-aware static handler.
  - Serves `index.html` for any path under `/control-panel` or `/viewer` that doesn't match a file (SPA fallback).
  - Sets correct `Content-Type` from extension.
  - Supports `HEAD` for viewer asset probing.
- `cassini-operator/internal/operator/static/handler_test.go` — SPA fallback for nested routes, asset Content-Type, HEAD.
- `cassini-viewer` refactor:
  - All `fetch("./catalog.json")` and `fetch("./meetings/...")` rewritten to use `${VITE_PUBLISHED_BASE || "/published"}/catalog.json` etc.
  - `vite.config.ts` accepts `VITE_BASE` and `VITE_PUBLISHED_BASE` from env.
  - Existing dev mode (running against `npm run dev`) keeps relative paths by defaulting `VITE_PUBLISHED_BASE=""`.
- `cassini-control-panel` build flag: `VITE_BASE=/control-panel/` for the embed; default `/` for dev.
- `/published/*` route handler: serves from `/srv/cassini-site/published` on disk (existing volume mount). No embed.

### Slice C — Packaging + CI (Lane C, parallel to A)

Files:
- `appinfo/info.xml` — manifest as above.
- `deployment/Dockerfile.exapp` — multi-stage:
  1. Go-builder stage (extends existing operator builder): builds operator with embed sources in place.
  2. Node stage: builds `cassini-control-panel` with `VITE_BASE=/control-panel/` and `cassini-viewer` with `VITE_BASE=/viewer/` + `VITE_PUBLISHED_BASE=/published`. Copies dists into `cassini-operator/embedded/{control-panel,viewer}` BEFORE Go build.
  3. Runtime stage: `node:22-bookworm-slim` + operator binary + frpc + start.sh + healthcheck.sh + Node-side publish runner (existing).
  4. ENV: `CASSINI_APPAPI_REQUIRED=true`, `APP_HOST=127.0.0.1`, `APP_PORT=8080`.
- `deployment/exapp-start.sh`:
  - Writes `/frpc.toml` from `HP_FRP_ADDRESS`, `HP_FRP_PORT`, `HP_SHARED_KEY` env. Errors clearly if missing.
  - Plugin section: `[proxies.plugin] type = "tcp"` pointing to `127.0.0.1:$APP_PORT`.
  - Launches `frpc` in background, then `exec` operator.
  - Optional mutual TLS when `/certs/frp` is mounted (match skeleton's behavior).
- `deployment/healthcheck.sh` — checks frpc PID + curls `127.0.0.1:$APP_PORT/operator/jobs` (expects 401 due to missing AppAPI header — that proves the listener is up and middleware is active).
- `frpc` binary — fetched at build time, version pinned (compatible with current HaRP release; pin verified in slice 0).
- `.github/workflows/publish-exapp-image.yml`:
  - Triggers: push to main, tags `v*`, pull_request.
  - Builds `Dockerfile.exapp` for `linux/amd64`.
  - On PR: tags as `sha-<short>`. On main: also `latest`. On tag: also `<tag>` and the stub variants `<tag>-cuda`, `<tag>-rocm`, `latest-cuda`, `latest-rocm` (all pointing to the same CPU image).
  - Authenticates to ghcr via `GITHUB_TOKEN`.
- `docs/exapp-install.md`:
  - Required deploy-daemon configuration (persistent volume for `/var/lib/cassini-operator` and `/srv/cassini-site`).
  - "GPU acceleration is not yet supported; the image runs CPU-only even on cuda/rocm daemons."
  - Step-by-step manual install via Nextcloud admin UI.

### Slice D — E2E install harness (Lane D, depends on A + B + C)

Files:
- `harness/bin/ci-e2e-exapp.sh`:
  - Boot local Nextcloud (extending existing harness) with AppAPI app installed (separate Nextcloud admin curl: `occ app:install app_api`).
  - Build operator image locally with `docker build -f deployment/Dockerfile.exapp .`.
  - Register Docker daemon: `occ app_api:daemon:register manual_install ...`.
  - Install gocassini from the local image tag: `occ app_api:app:register gocassini`.
  - Enable: `occ app_api:app:enable gocassini` (this drives `PUT /enabled`).
  - Assert: hit `/control-panel/` through Nextcloud proxy as admin → 200, as USER → 403 (Nextcloud-side, before app sees it).
  - Assert: hit `/viewer/` through proxy as USER → 200.
  - Assert: hit `/operator/jobs` as admin → 200, JSON shape.
  - Assert: SSE stream `/operator/events` returns at least one event when a job is posted (D13 trust-but-verify).
  - Assert: hit `127.0.0.1:$APP_PORT/operator/jobs` directly (bypass proxy) without `AUTHORIZATION-APP-API` → 401 (this is the proper middleware test, not through proxy).
- `.github/workflows/ci.yml` — add `ci-e2e-exapp.sh` to integration matrix. Mark `continue-on-error: true` for first 2 weeks; promote to required after.

## Worktree parallelization

| Step | Modules touched | Depends on |
|---|---|---|
| Slice 0 — install spike | `appinfo/`, `deployment/`, `harness/` | — |
| Slice A — middleware + lifecycle + binding | `cassini-operator/internal/operator/` | Slice 0 |
| Slice B — embed + viewer refactor | `cassini-operator/`, `cassini-viewer/`, `cassini-control-panel/` | Slice 0; Slice A's `run.go` shape |
| Slice C — packaging + CI | `appinfo/`, `deployment/`, `.github/workflows/` | Slice 0 |
| Slice D — E2E harness | `harness/`, `.github/workflows/ci.yml` | A + B + C |

**Lanes:**
- Lane 1: Slice 0 sequential gate.
- Lane 2 (after Lane 1): A and C parallel — different modules. B starts when A's `run.go` env interface is committed.
- Lane 3 (after Lane 2): D.

**Conflict flags:**
- Slice A and Slice B both touch `cassini-operator/internal/operator/run.go` — sequential within Go module. Land A first, then B layers static handler over the routing.
- Slice C's `Dockerfile.exapp` references files from A and B. Land Dockerfile last in Slice C, or build in stages as A/B land.

## Tests in the plan

### Unit
- `appapi/middleware_test.go`:
  1. Valid header + matching secret → 200, userId in context.
  2. Missing AUTHORIZATION-APP-API → 401.
  3. Malformed base64 → 401.
  4. Valid base64, wrong secret → 401.
  5. Wrong `EX-APP-ID` → 401.
  6. Wrong `EX-APP-VERSION` → 401 (or 200 depending on AppAPI tolerance; verify in slice 0).
  7. Middleware disabled (no `APP_SECRET`) → pass-through.
  8. Logging: 401 path logs `AA-REQUEST-ID`, never `AUTHORIZATION-APP-API` value (assert via captured log output).
- `lifecycle_test.go`:
  1. `PUT /enabled` enable=true → 200.
  2. `PUT /enabled` enable=false → 200.
  3. **REGRESSION** State persisted across operator process restart (use temp DB, restart, read state).
  4. `POST /init` first call → 200.
  5. `POST /init` second call → 200, no schema breakage.
- `static/handler_test.go`:
  1. `/control-panel/` → embedded index.html.
  2. `/control-panel/some/spa/route` → SPA fallback index.html (200, NOT 404).
  3. `/control-panel/assets/<hash>.js` → correct Content-Type `application/javascript`.
  4. `HEAD /viewer/assets/foo.mp3` → 200 with Content-Length, no body.
  5. `/viewer/` → embedded viewer index.html.
- `run_test.go`:
  1. `APP_HOST=0.0.0.0` `APP_PORT=8080` → operator binds there.
  2. **REGRESSION** Existing `-port` flag works when `APP_PORT` unset.
  3. `CASSINI_APPAPI_REQUIRED=true` + no `APP_SECRET` → fatal exit.

### Integration (existing harness + new)
- `harness/bin/ci-e2e-exapp.sh` per Slice D above.

### Manifest validation
- `appinfo/info.xml` validated against Nextcloud AppAPI XSD as part of CI workflow (download XSD from AppAPI repo, `xmllint --schema`). Catches manifest typos pre-merge.

## Failure modes

| Mode | Trigger | Detection | Mitigation |
|---|---|---|---|
| frpc dies | Network blip, frpc crash | `healthcheck.sh` reports unhealthy | Daemon restarts container. Slice 0 verifies recovery. |
| APP_SECRET mismatch | Operator and Nextcloud out of sync (manual rotation) | All proxied requests 401 | Log `AA-REQUEST-ID` + "auth mismatch", never the header value. Admin sees "auth failure" in logs. |
| Ephemeral `/var/lib/cassini-operator` | Admin forgot bind mount | Startup log warning; SQLite gone after restart | Documented + warning. Cannot fully prevent. |
| `POST /init` re-runs | Reinstall, version upgrade | None | Migration runner already idempotent via `schema_migrations`. |
| Concurrent enable/disable | Admin double-clicks | None visible | Lifecycle handler idempotent + DB-backed. |
| SSE through HaRP fails | HaRP/PHP proxy doesn't passthrough SSE cleanly (D13 risk) | E2E SSE assertion in `ci-e2e-exapp.sh` | If E2E fails: fall back to polling in control-panel (already implemented as the reconnect path). **Unresolved risk** — D13 chose not to pre-emptively switch. |
| frpc version drift | HaRP server upgraded, our frpc client incompatible | Tunnel fails to establish; install hangs | Slice 0 pins frpc version. Add version check to start.sh that logs frpc version on boot. |
| GPU-tagged daemon | Admin's daemon is cuda-tagged | Pull succeeds (stub tag), but no GPU acceleration | D15 stub tags; README note about GPU support pending. |
| Recorder cannot reach Talk room | Talk bot needs separate credentials not provided by AppAPI | Recording fails at record stage | **Out of v1 scope.** AppAPI doesn't provide Talk room credentials; recorder keeps using bot user as today. |

**Critical gaps (no test, no error handling, silent failure):** none after this plan lands. SSE failure mode has detection (E2E) and a fallback path (existing polling on reconnect).

## TODOS (deferred, captured)

- **Multi-arch (linux/arm64)** — Self-hosted Nextcloud on ARM is real. Adds qemu buildx + 15-20 min CI. Sherpa-onnx supports arm64; need to verify all cgo deps cross-build.
- **GPU image variants** — Real `latest-cuda` (CUDA-enabled sherpa-onnx) and `latest-rocm` builds. Today: stub tags only.
- **Krankerl + Nextcloud app-store submission** — Required for in-Nextcloud app catalog discovery. Needs signing, screenshots, copy, review cycle.
- **Outbound OCS/WebDAV to Nextcloud Files** — Write recordings into the inviting user's Files via app-scoped credentials. Reduces "where did my recording go?" friction and lets users share via existing NC sharing.
- **Talk room listing via OCS API** — Control-panel could populate a room picker via `/ocs/v2.php/apps/spreed/api/v4/room` (as the app). Better UX than typing tokens.
- **Per-recording ACL** — Today (D10): org-wide archive. Future: filter `/published/catalog.json` by attendance, integrate with NC sharing.
- **Move recorder bot credentials into AppAPI lifecycle** — Today recorder uses its own bot account configured outside the ExApp. Could be configured via NC admin UI through the app.
- **SSE polling fallback** — D13 trusted SSE works. If E2E shows it doesn't, promote control-panel polling to primary.

(File each as a row in `TODOS.md` when implementation lands the v1.)

## Unresolved decisions / risks

- **SSE through HaRP (D13)** — Chose trust, no pre-emptive fallback. E2E will tell. If it fails, follow-up PR switches to polling.

## What actually shipped (v1 delta from the plan)

| Plan | Shipped | Why |
|---|---|---|
| D6 — `go:embed` of control-panel + viewer dists | **Disk-served via `CASSINI_CONTROL_PANEL_DIST` / `CASSINI_VIEWER_DIST` env paths** | Codex flagged that `go:embed` can't reach across modules; the workaround (copy dists into the operator module before `go build`) added pre-build orchestration. Disk-served gives the same UX with a smaller change. Embed remains an option for a future PR. |
| D8 — Full Nextcloud-fronted install E2E in `ci-e2e-exapp.sh` | **Container-level E2E only**: middleware refuses 401/accepts 200, lifecycle callbacks, SPA fallback, state-survives-restart, all verified against the real image | The full Nextcloud + AppAPI + HaRP install loop in CI is a larger orchestration effort. v1 ships the parts of the contract we can test deterministically (everything the operator does inside the container) and defers the cross-system install assertion to a follow-up. The job runs `continue-on-error: true` initially anyway. |
| D11 — Embed both viewer + control-panel with explicit Vite base rebuilds | **Dockerfile sets `VITE_BASE=/control-panel/` and `VITE_BASE=/viewer/` per stage**; viewer's `./catalog.json` fetches still relative | Vite base rebuilds done at Docker build time. Refactoring viewer's relative asset fetches to absolute `/published/...` is still required for the viewer SPA to find recordings when served from `/viewer/` — captured as a TODO, not blocking the initial install path. |
| Lifecycle persistence in SQLite | **JSON file `<db_dir>/app-state.json`** | Single-row state didn't justify schema migration; JSON file behaves identically and is easier to reason about. |

Verified locally (2026-05-19):
- `go test ./...` in `cassini-operator/` — green (37 tests across operator, appapi, lifecycle, exapp packages).
- `docker buildx build -f deployment/Dockerfile.exapp` — green (image `cassini-exapp:local`, ~150s cold, includes operator + recorder + frpc 0.61.1 + control-panel dist + viewer dist).
- `harness/bin/ci-smoke-exapp.sh` against the local image — green (operator API, lifecycle, both SPAs respond).
- `harness/bin/ci-e2e-exapp.sh` against the local image — green (middleware refuses unauthed, accepts AppAPI headers, lifecycle works, state survives `docker restart`).

Still on the TODO list (in priority order):
1. Full Nextcloud + AppAPI + HaRP install E2E in CI.
2. Viewer's relative `./catalog.json` fetches → absolute `/published/...` paths.
3. Multi-arch (linux/arm64) image builds.
4. Real GPU image variants (vs current CPU-aliased stub tags).
5. Krankerl + Nextcloud app-store submission.
6. Outbound OCS/WebDAV writes to Nextcloud Files.

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 0 | — | not run; packaging task, not product strategy |
| Codex Review | `/codex review` | Independent 2nd opinion | 1 | issues_found | 22 findings, 5 incorporated as plan corrections, 4 surfaced as cross-model tensions D10/D11/D12/D13 |
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 1 | clean | 4 architecture decisions locked, 2 code-quality decisions, 1 test E2E decision, 2 small decisions; codex challenge incorporated |
| Design Review | `/plan-design-review` | UI/UX gaps | 0 | — | not run; UI is existing control-panel/viewer with new prefixes only |
| DX Review | `/plan-devex-review` | Developer experience gaps | 0 | — | not run |

**CODEX:** 22 findings, 5 incorporated as plan corrections (lifecycle not in routes, Node stays, embed cross-module, no header logging, anchored regexes + HEAD), 4 surfaced as cross-model tensions for user decision (D10/D11/D12/D13).
**CROSS-MODEL:** Codex pushed for simpler v1 (single USER prefix, drop /viewer embed) — user rejected via D11 in favor of explicit embed with Vite base rebuilds. Codex pushed for spike-first sequencing — user accepted via D12.
**UNRESOLVED:** 1 (D13 SSE trust-but-verify).
**VERDICT:** ENG CLEARED — Slice 0 spike must succeed before lanes A/B/C/D launch.
