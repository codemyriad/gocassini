# Local Talk Test Harness (`harness/`)

This README separates harness setup/configuration from scenario usage.

If you are using Cassini from this repo checkout, start from the repository root
and prefer the product-facing commands:

```bash
./bin/cassini dev ...
```

The scripts in `harness/bin/` are still the underlying lab implementation and
are documented when a flow intentionally calls them directly.

## Quick jump

1. [Choose a harness variant](#1-choose-a-harness-variant)
2. [Configuration model: modes, flags, and environment](#2-configuration-model-modes-flags-and-environment)
3. [Stack lifecycle quickstart](#3-stack-lifecycle-quickstart)
4. [Guide: E2E tests](#4-guide-e2e-tests)
5. [Guide: local installed-ExApp dev mode, Linux only](#5-guide-local-installed-exapp-dev-mode-linux-only)
6. [Guide: remote HTTPS dev setup](#6-guide-remote-https-dev-setup)
7. [Guide: direct operator / HPB-internal debugging](#7-guide-direct-operator--hpb-internal-debugging)
8. [Guide: Talk recording-backend lifecycle proof](#8-guide-talk-recording-backend-lifecycle-proof)
9. [Media and scenario guides](#9-media-and-scenario-guides)
10. [Repository structure and operational reference](#10-repository-structure-and-operational-reference)
11. [Teardown reference](#11-teardown-reference)

## Purpose

`harness/` is the reproducible E2E lab for Nextcloud Talk/Spreed testing:

- start/stop a local Talk stack with Docker Compose
- bootstrap Nextcloud, Talk, signaling, TURN, and optional AppAPI/HaRP support
- create rooms quickly
- run player scenarios and recorder roundtrips
- prepare deterministic media inputs for sync validation
- generate meeting-like spoken fixtures for transcription and player validation

All generated media/runtime artifacts stay outside git-tracked source content.
Unless a section says otherwise, commands below are run from the repository root.

Preferred product-facing entry points:

- `./bin/cassini dev stack ...`
- `./bin/cassini dev room create`
- `./bin/cassini dev fixture prepare-showcase`
- `./bin/cassini dev player ...`
- `./bin/cassini dev smoke`

---

## 1. Choose a harness variant

The stack is configured by combining independent dimensions:

- **public/browser mode**: how browsers and bots reach Nextcloud/signaling
- **service topology**: which Docker Compose services start
- **Cassini mode**: whether Cassini is installed as an AppAPI ExApp
- **recording backend**: how Talk's recording backend is wired
- **ExApp image mode**: how an installed ExApp image is provided
- **lifecycle mode**: how existing containers/volumes are handled

Use `stack plan` before expensive bring-up:

```bash
./bin/cassini dev stack plan --services full
```

Common combinations:

| Goal | Public mode | Services | Cassini | Recording backend | Command shape |
|---|---:|---:|---:|---:|---|
| Historical/default local lab | `local-http` | `legacy-default` | `none` | `legacy` | `./bin/cassini dev stack up` |
| Cheap Nextcloud/Talk API checks | `local-http` | `core` | `none` | `none` | `./bin/cassini dev stack up --services core --recording-backend none` |
| AppAPI/HaRP route or install testing, no media | `local-http` | `appapi` | `installed-exapp` or manual | `none` | `./bin/cassini dev stack up --services appapi --cassini installed-exapp --recording-backend none --build` |
| Local WebRTC recorder/player E2E | `local-http` | `full` | `none` | `legacy` | `./bin/cassini dev stack up --services full` |
| Direct standalone operator debugging | `local-http` | `full` | `none` | `direct-operator` | `./bin/cassini dev stack up --services full --recording-backend direct-operator` |
| Linux local installed-ExApp recording dev | `local-http` | `full` | `installed-exapp` | `installed-exapp` | `./bin/cassini dev stack up --services full --cassini installed-exapp --recording-backend installed-exapp --build` |
| Docker Desktop for Mac installed-ExApp dev | `lan-http` | `full` | `installed-exapp` | `installed-exapp` | See [Docker Desktop for Mac](#52-docker-desktop-for-mac) for the required LAN signaling flags. |
| Remote browser / macOS browser through HTTPS proxy | `remote-https` | `full-remote` | optional | matching backend | `./bin/cassini dev stack up --public-mode remote-https --services full-remote ...` |

> **macOS note:** the full local stack works with Docker Desktop 4.34 or newer
> when **Enable host networking** is on and Talk signaling uses the Mac's LAN IP.
> The default Linux bridge-gateway signaling address is not browser-reachable on
> macOS. Follow the [Docker Desktop for Mac guide](#52-docker-desktop-for-mac),
> or use the [remote HTTPS guide](#6-guide-remote-https-dev-setup) when Docker
> Desktop host networking is unavailable.

---

## 2. Configuration model: modes, flags, and environment

### 2.1 Config hierarchy and explicit overrides

For `cassini dev stack`, the hierarchy is:

```text
explicit CLI flag > matching environment variable > default
```

Important details:

- Empty environment values are treated as unset.
- `--build` is shorthand for `--exapp-image-mode build` and overrides the image
  mode from the environment.
- Flags apply only to the `stack` command invocation where they are passed. If a
  later command such as `dev room create` must produce remote/public URLs, export
  the corresponding environment variables too.
- Remote inputs (`public-url`, `public-host`, `media-host`,
  `signaling-public-url`) are explicit. If they are set while public mode remains
  `local-http`, validation fails instead of silently changing modes.
- Passing an explicit non-remote `--public-mode local-http` or `--public-mode
  lan-http` masks ambient remote environment variables for that invocation. This
  keeps local E2E runs deterministic from shells that also contain remote dev
  exports.

Use `plan` to inspect the resolved values before `up`:

```bash
./bin/cassini dev stack plan \
  --public-mode local-http \
  --services full \
  --cassini none \
  --recording-backend legacy
```

### 2.2 Public/browser modes

| Mode | What it does | Typical use |
|---|---|---|
| `local-http` | Default. Nextcloud is exposed as `http://127.0.0.1:28080`; local signaling is exposed on `http://127.0.0.1:28082`. Remote public inputs are rejected unless an explicit non-remote flag masks ambient env. | Local Linux-host E2E, CI, recorder/player harness flows. |
| `lan-http` | Advanced HTTP mode for a trusted LAN/VM address. If `--public-url` is set, it must be `http://...`. It does not render the full remote HTTPS helper path. | Controlled VM/LAN debugging where HTTPS secure-origin browser requirements are not relevant. |
| `remote-https` | Requires an HTTPS public Nextcloud URL/host and a non-loopback media host. Renders remote-safe signaling, Janus, and TURN config. With `--services full-remote`, also starts the Docker-network HTTPS helper used by server-side Talk signaling notifications. | Browser on another machine, especially Mac browser -> Linux harness through Tailscale/HTTPS proxying. |

Remote mode derived values:

- `--public-host` is derived from `--public-url` if omitted.
- `--public-url` is derived as `https://<public-host>` if remote mode has only a
  host.
- `--signaling-public-url` defaults to `https://<public-host>:8443` in remote
  mode.
- `--media-host` must be a non-loopback host/IP. For Tailscale, use the Linux
  host's Tailscale IPv4 address.

### 2.3 Service topology modes

| `--services` value | Compose services | What it is for |
|---|---|---|
| `legacy-default` | Historical behavior. Uses `SPREED_PROFILE` when set; otherwise starts the full media profile. | Backwards compatibility for old scripts and manual lab use. Prefer explicit modes for new flows. |
| `core` | `db`, `nextcloud` | Fast Nextcloud/Talk API checks; no AppAPI/HaRP services and no WebRTC media services. |
| `appapi` | `db`, `nextcloud`, `appapi-harp`, `reverse-proxy` | AppAPI/HaRP install, proxy route, control-panel/viewer checks without Janus/signaling/TURN media. |
| `full` | `db`, `nextcloud`, `appapi-harp`, `reverse-proxy`, `nats`, `janus`, `signaling`, `coturn` | Full local Talk media path for recorder/player E2E and direct operator debugging. |
| `full-remote` | `full` plus `signaling-public-proxy` | Full media path plus remote HTTPS helper for browser access from another machine. Requires `--public-mode remote-https`. |

Validation rules worth remembering:

- `full-remote` requires `remote-https`.
- `direct-operator` and `installed-exapp` recording backends require `full`,
  `full-remote`, or `legacy-default` services because they need media services.
- `--cassini installed-exapp` requires `appapi`, `full`, `full-remote`, or
  `legacy-default`; it is rejected with `core`.

### 2.4 Cassini install modes

| `--cassini` value | Environment | What it does |
|---|---|---|
| `none` | `CASSINI_HARNESS_CASSINI_MODE=none` | Default. The harness configures Nextcloud/Talk but does not install Cassini as an ExApp. Use this for recorder/player E2E and standalone-operator debugging. |
| `installed-exapp` | `CASSINI_HARNESS_CASSINI_MODE=installed-exapp` | Installs/enables AppAPI, configures a HaRP deploy daemon, prepares the Cassini ExApp image, registers `gocassini`, verifies `/api/v1/welcome`, `/operator/status`, and `/viewer/`. |

Installed ExApp setup is opt-in. It also enables the patch/image phases below.

### 2.5 Recording backend modes

| `--recording-backend` value | Environment | What it does |
|---|---|---|
| `legacy` | `CASSINI_HARNESS_RECORDING_BACKEND=legacy` | Default historical mode. In media topologies, Talk recording is pointed at a host/gateway `:4000` recording backend unless `CASSINI_TALK_RECORDING_URL` overrides it. |
| `direct-operator` | `CASSINI_HARNESS_RECORDING_BACKEND=direct-operator` | Explicit standalone-operator mode. Uses the same gateway URL resolution as `legacy`, but makes the intent clear for direct operator debugging. |
| `installed-exapp` | `CASSINI_HARNESS_RECORDING_BACKEND=installed-exapp` | Points Talk recording at the AppAPI proxy route `http://reverse-proxy/index.php/apps/app_api/proxy/gocassini`. Requires `--cassini installed-exapp` and media services. |
| `none` | `CASSINI_HARNESS_RECORDING_BACKEND=none` | Deletes Talk `recording_servers` config and sets Talk call recording to `no`. Use for core/AppAPI-only tests. |

### 2.6 ExApp image modes

| Flag value | Environment | What it does |
|---|---|---|
| `--exapp-image-mode build` or `--build` | `CASSINI_HARNESS_EXAPP_IMAGE_MODE=build` | Builds `deployment/Dockerfile.exapp` as `cassini-exapp:e2e-v3-cpu-gpu`, then tags it as the production image reference from `appinfo/info.xml`. |
| `--exapp-image-mode reuse-local` | `CASSINI_HARNESS_EXAPP_IMAGE_MODE=reuse-local` | Default. Reuses an existing local `cassini-exapp:e2e-v3-cpu-gpu` image and tags it as the production reference. Fails if the local image is missing. |
| `--exapp-image-mode pull` | `CASSINI_HARNESS_EXAPP_IMAGE_MODE=pull` | Leaves the `ghcr.io` image unmapped so AppAPI can pull the image declared by `appinfo/info.xml`. |

### 2.7 Lifecycle modes

`stack up` is non-destructive by default.

| Command/flag | Environment | What it does |
|---|---|---|
| `stack up` | `CASSINI_HARNESS_EXISTING=fail` (default) | Fails if harness containers, volumes, or networks already exist, and prints the safe next command. |
| `stack up --resume` | `CASSINI_HARNESS_EXISTING=resume` | Starts matching stopped resources. Fails if the stopped services do not match the resolved config. |
| `stack up --reset` | `CASSINI_HARNESS_EXISTING=reset` | Removes and recreates resources for the resolved stack. For installed ExApp mode, AppAPI-spawned ExApp resources are removed first so the network can be deleted. |
| `stack down` | command flag only | Removes compose containers and ExApp containers, keeps data volumes. |
| `stack down --suspend` | command flag only | Stops compose containers but keeps them for `up --resume`. Cannot combine with `--volumes` or `--full`. |
| `stack down --volumes` | command flag only | Removes containers and volumes for the current Compose project. |
| `stack down --full` | command flag only | Removes all harness-owned Compose and installed-ExApp resources, including volumes, across known harness projects. |

`dev stack stop` has been removed; use `dev stack down` or `dev stack down
--suspend`.

### 2.8 Flag / environment reference

| CLI flag | Environment variable | Default | Notes |
|---|---|---:|---|
| `--public-mode local-http|lan-http|remote-https` | `CASSINI_HARNESS_PUBLIC_MODE` | `local-http` | Browser/public access mode. |
| `--public-url URL` | `CASSINI_HARNESS_PUBLIC_URL` | unset | Browser-facing Nextcloud base URL. Required in `lan-http`; required in `remote-https` unless `--public-host` can derive it. |
| `--public-host HOST` | `CASSINI_HARNESS_PUBLIC_HOST` | derived/unset | Bare host, no scheme/path. Derived from `--public-url` when possible. |
| `--media-host HOST_OR_IP` | `CASSINI_HARNESS_MEDIA_HOST` | unset/derived | Advertised to WebRTC media services. Required and non-loopback for `lan-http` media and `remote-https`. |
| `--signaling-public-url URL` | `CASSINI_HARNESS_SIGNALING_PUBLIC_URL` | `https://<public-host>:8443` in remote mode | Browser-facing standalone signaling URL. Required, HTTP, and non-loopback for `lan-http` media; HTTPS in `remote-https`. |
| `--talk-backend-url URL` | `CASSINI_TALK_BACKEND_URL` | derived/unset | Nextcloud URL used by Cassini for Talk callbacks. Required for `lan-http` installed-ExApp recording. |
| `--services VALUE` | `CASSINI_HARNESS_SERVICE_MODE` | `legacy-default` | Preferred service topology flag. |
| `--service-mode VALUE` | `CASSINI_HARNESS_SERVICE_MODE` | `legacy-default` | Alias for `--services`; cannot disagree with `--services`. |
| `--cassini none|installed-exapp` | `CASSINI_HARNESS_CASSINI_MODE` | `none` | Whether stack bring-up installs Cassini as an AppAPI ExApp. |
| `--recording-backend legacy|direct-operator|installed-exapp|none` | `CASSINI_HARNESS_RECORDING_BACKEND` | `legacy` | How Talk's recording backend is configured during bootstrap. |
| `--exapp-image-mode build|reuse-local|pull` | `CASSINI_HARNESS_EXAPP_IMAGE_MODE` | `reuse-local` | Only meaningful with `--cassini installed-exapp`. |
| `--build` | n/a; sets image mode | n/a | Shorthand for image mode `build`; requires `--cassini installed-exapp`. |
| `stack up --resume` | `CASSINI_HARNESS_EXISTING=resume` | `fail` | Up-only lifecycle behavior. |
| `stack up --reset` | `CASSINI_HARNESS_EXISTING=reset` | `fail` | Up-only lifecycle behavior. |
| `stack down --suspend` | n/a | false | Down-only; stop containers but keep them. |
| `stack down --volumes` | n/a | false | Down-only; remove project volumes too. |
| `stack down --full` | n/a | false | Down-only; remove all known harness resources. |

### 2.9 Supporting environment variables without stack flags

| Variable | Default | Purpose |
|---|---:|---|
| `PROJECT_NAME` | `spreedtest` | Docker Compose project name. CI/e2e scripts often set a run-scoped value. |
| `NEXTCLOUD_HOST_PORT` | `28080` | Host port mapped to Nextcloud port 80. Some e2e scripts randomize this to avoid stale-run collisions. |
| `NEXTCLOUD_IMAGE` | `nextcloud:34.0.0` | Override the pinned Nextcloud image. CI compatibility legs may set this. |
| `CASSINI_HARNESS_HOST` | `127.0.0.1` or VM route source IP | Host/IP added to Nextcloud trusted domains and used by some play helpers. |
| `SPREED_PROFILE` | derived | Legacy compose profile escape hatch. Explicit `--services` values set it for you. |
| `NEXTCLOUD_URL` | `http://127.0.0.1:${NEXTCLOUD_HOST_PORT}` | Operator/API URL used by harness scripts. |
| `NEXTCLOUD_PUBLIC_URL` | public URL or `NEXTCLOUD_URL` | URL used when `create-room.sh` prints the final call URL. Export remote public values for multi-command remote sessions. |
| `ADMIN_USER` / `ADMIN_PASSWORD` | `admin` / `admin` | Dev-only Nextcloud admin credentials. |
| `BOT_USER` / `BOT_PASSWORD` | `botuser` / generated default | Dev-only player bot credentials. |
| `SIGNALING_SHARED_SECRET` | committed dev value | Shared secret between Talk and standalone signaling. Dev only. |
| `SIGNALING_INTERNAL_SECRET` | committed dev value | Standalone signaling `internalsecret`; must match `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. Dev only. |
| `TURN_SERVER` / `TURN_SHARED_SECRET` | `127.0.0.1:13479` / committed dev value | TURN configuration. Remote mode derives `TURN_SERVER` from `CASSINI_HARNESS_MEDIA_HOST`. |
| `CASSINI_TALK_RECORDING_URL` | derived | Overrides the URL written into Talk `recording_servers`. |
| `CASSINI_TALK_RECORDING_SECRET` | committed dev value | Talk recording backend HMAC secret. Dev only. |
| `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | `SIGNALING_INTERNAL_SECRET` | Secret passed to Cassini for HPB-internal signaling auth. |

The committed secrets and passwords are for the dev harness only. Never reuse
them for real Nextcloud, Talk, signaling, TURN, or Cassini deployments.

### 2.10 Plan validation warnings

`dev stack` distinguishes invalid configuration from valid-but-surprising
configuration:

```text
resolve flags + environment
          |
          v
   hard validation
      |        |
 invalid      valid
      |        |
 exit 2        v
 no plan   collect warnings
               |
               +-- plan: validation: ok / warnings list
               `-- up/down: stderr preflight, then run normally
```

Hard validation still rejects contradictory or unsupported modes before a plan
is printed. Warnings do not change the exit code. They call out:

- explicit values that the resolved topology overrides or ignores, such as
  `SPREED_PROFILE`, ExApp image mode, patch mode, and recording backend;
- an installed ExApp bypassed by a legacy/direct recording backend;
- remote media values that cannot serve the selected topology, including
  RFC1918 media IPs that browsers outside the private network cannot reach;
- destructive lifecycle intent from `up --reset`, `down --volumes`, and
  `down --full`.

The default plan has no warnings. Warnings about optional modes fire only when
the corresponding flag or environment variable expresses user intent.

---

## 3. Stack lifecycle quickstart

### 3.1 Inspect, start, create a room, play media

The installed-ExApp path uses Python to read `appinfo/info.xml` before Docker
starts. Use Python 3.12 for the harness rather than an unversioned, moving
`python3`. With direnv, pin the interpreter in the checkout's gitignored
`.envrc`:

```bash
# Install once on macOS when needed: brew install python@3.12
layout_python python3.12
```

After adding or changing the pin, run `direnv allow` (or `direnv reload` for an
already allowed file), then verify that the interpreter and its native Expat
module work:

```bash
python3 --version
python3 -c 'import pyexpat, xml.etree.ElementTree as ET; print(pyexpat.EXPAT_VERSION)'
```

The version should be Python 3.12 and the import must exit successfully. A
virtualenv does not repair native standard-library modules from its underlying
interpreter, so recreate `.direnv/python-3.12` after replacing that interpreter
if the import fails.

```bash
./bin/cassini dev stack plan --services full
./bin/cassini dev stack up --services full
CALL_URL="$(./bin/cassini dev room create --name "Local smoke room" | tail -n1)"
./bin/cassini dev player video --call-url "$CALL_URL" --duration 20
```

One-command smoke test:

```bash
./bin/cassini dev smoke
```

### 3.2 Stack commands

```bash
./bin/cassini dev stack plan     # print resolved config only
./bin/cassini dev stack up       # start/bootstrap the resolved config
./bin/cassini dev stack status   # compose ps + Nextcloud status + last call URL
./bin/cassini dev stack down     # remove containers, keep volumes
./bin/cassini dev stack down --suspend  # stop containers, keep for up --resume
./bin/cassini dev stack down --volumes  # remove current project containers + volumes
./bin/cassini dev stack down --full     # remove all harness-owned resources
```

`stack up` is non-destructive by default. If resources already exist, it fails
with suggested next commands. Use:

```bash
./bin/cassini dev stack up --resume   # matching stopped resources
./bin/cassini dev stack up --reset    # recreate resolved stack
./bin/cassini dev stack down --full   # complete harness cleanup
```

### 3.3 Direct script equivalents

The `cassini dev` commands call scripts in `harness/bin/`:

- `harness/bin/up.sh`
- `harness/bin/down.sh`
- `harness/bin/status.sh`
- `harness/bin/bootstrap.sh`
- `harness/bin/create-room.sh`
- `harness/bin/smoke.sh`

Prefer the `./bin/cassini dev ...` surface for product-facing docs and for any
flow where stack flags/env resolution matters.

---

## 4. Guide: E2E tests

The E2E scripts that need a local stack now choose explicit stack topology and
sanitize ambient remote environment variables before they source the common
harness code. That means local CI-like tests remain deterministic even from a
shell that also contains `CASSINI_HARNESS_PUBLIC_URL` or other remote exports.

### 4.1 Stack-backed E2E variants

| Script/command | Stack topology | What it validates |
|---|---|---|
| `./bin/cassini dev smoke` | `local-http` + `full` + `cassini none` + `recording legacy` | Quick end-to-end local smoke. Keeps the normal non-destructive guard. |
| `./bin/cassini dev ci-e2e` or `./harness/bin/ci-e2e.sh` | `local-http` + `full` + `cassini none` + `recording legacy` | Baseline full Nextcloud + recorder + player run used by CI. Uses `--reset` and tears down with `--volumes`. |
| `./harness/bin/ci-e2e-mute.sh` | same as baseline | Mute-aware three-player flow; validates multi-player capture via session artifacts and player mute logs. |
| `./harness/bin/ci-e2e-rejoin.sh` | same as baseline | Leave/rejoin flow with two player phases; validates player phases and recorder subscription evidence. |
| `IMAGE_REF=... ./harness/bin/ci-e2e-install-exapp.sh` | `local-http` + `core` + `cassini none` + `recording none` | Real Nextcloud + AppAPI install handshake against a provided ExApp image. The script manually starts/registers the image so it can test AppAPI route patterns. |
| `IMAGE_REF=... ./harness/bin/ci-e2e-talk-record-roundtrip.sh` | `local-http` + `full` + `cassini none` + `recording legacy`, then custom operator container | Full Talk record-button roundtrip: Talk recording-backend HMAC -> operator -> recorder -> transcribe -> publish -> transcript check. |
| `./harness/bin/d263-nextcloud-lifecycle.sh` | run after a stack is up | Native Talk recording-backend lifecycle against local Nextcloud/Talk with a fake media worker. Not a full media acceptance test. |

Baseline local CI run:

```bash
./bin/cassini dev ci-e2e
```

Harness-specific variants:

```bash
./harness/bin/ci-e2e-mute.sh
./harness/bin/ci-e2e-rejoin.sh
```

Both CI entrypoints use bounded retry when creating the temporary Talk room to
handle transient OCS/API bootstrap races in freshly-started local stacks.
`bootstrap.sh` also auto-resolves a container-reachable signaling URL for local
Docker runs (gateway address instead of host-loopback). The recorder publisher
E2E also verifies that `gocassini-remux` can rebuild an artifact-based MKV from
`session.json`.

### 4.2 Artifact-centric scenario assertions

Scenario assertions avoid brittle assumptions about final remux layout:

- `ci-e2e-mute.sh` requires at least one final video/audio track, then validates
  multi-player capture via session artifact stream counts and publisher mute
  logs.
- `ci-e2e-rejoin.sh` validates both player phases plus recorder evidence of
  remote session subscription attempts and successful stream capture.
- `ci-e2e-rejoin.sh` does not fail solely on missing final video in a flaky run;
  recorder/subscription evidence and session artifacts are the primary signal.

Default local endpoints for stack-backed tests:

- Nextcloud API: `http://127.0.0.1:28080`
- Signaling server: `http://127.0.0.1:28082`

### 4.3 ExApp image/container checks that do not use the stack bring-up path

These scripts validate the ExApp image or transcription behavior without using
`cassini dev stack up` as their main setup surface:

```bash
IMAGE_REF=ghcr.io/codemyriad/gocassini:<tag> ./harness/bin/ci-smoke-exapp.sh
IMAGE_REF=ghcr.io/codemyriad/gocassini:<tag> ./harness/bin/ci-e2e-exapp.sh
IMAGE_REF=ghcr.io/codemyriad/gocassini:<tag> ./harness/bin/ci-transcribe-smoke-exapp.sh
IMAGE_REF=ghcr.io/codemyriad/gocassini:<tag> ./harness/bin/ci-e2e-v3-transcript-verify.sh
IMAGE_REF=ghcr.io/codemyriad/gocassini:<tag> ./harness/bin/ci-transcribe-short-clip-regression.sh
```

Use the stack-backed install/roundtrip tests above when you need Nextcloud,
AppAPI, Talk recording-backend, or WebRTC media coverage.

---

## 5. Guide: local installed-ExApp dev mode

This is the production-shaped local development path: Nextcloud installs Cassini
as a real AppAPI ExApp through HaRP, Talk points its recording backend at the
AppAPI proxy, and the ExApp records through HPB-internal signaling auth.

Supported hosts are Linux with Docker Engine + Compose v2, and macOS with Docker
Desktop 4.34 or newer plus host networking enabled. The macOS command differs
because one signaling address must be reachable from both the Nextcloud
container and the browser.

### 5.1 Start the full installed-ExApp stack

```bash
./bin/cassini dev stack plan \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build

./bin/cassini dev stack up \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

What happens:

1. The ExApp image is built and tagged using the `<image-tag>` from
   `appinfo/info.xml`.
2. Compose starts Nextcloud, Postgres, reverse proxy, AppAPI HaRP, NATS, Janus,
   standalone signaling, and Coturn.
3. Nextcloud is bootstrapped with Talk, Team folders, Everyone Group, trusted
   domains, signaling, TURN, and Talk recording settings.
4. AppAPI is installed/enabled.
5. The HaRP deploy daemon is registered.
6. Cassini is registered as `gocassini` and route checks are performed.

Recordings are access-controlled — there is no other mode. `bootstrap.sh`
installs the two prerequisites an ExApp cannot install for itself — Team folders
and Everyone Group — and the ExApp provisions its folder, groups and ACLs on
enable. Production installers must enable both native apps first. See
[`docs/exapp-nextcloud-recordings-permissions.md`](../docs/exapp-nextcloud-recordings-permissions.md).

If you already have a suitable local ExApp image, omit `--build` and use the
default `reuse-local` mode. If you want AppAPI to pull the manifest image, use
`--exapp-image-mode pull`.

### 5.2 Docker Desktop for Mac runbook

The macOS path follows the harness configuration contract:

- The harness does **not** discover or guess the Mac LAN address.
- Supply browser, media, signaling, and ExApp callback addresses explicitly.
- Run `stack plan` first; incomplete or loopback-only LAN media configuration is
  rejected before Docker resources are changed.
- `stack up` verifies the configured signaling URL from the host after the media
  services start and fails instead of leaving Talk stuck on “Connecting”.

The browser keeps the potentially trustworthy loopback origin, while signaling
and media use the explicitly configured Mac LAN address:

```text
Browser ── http://127.0.0.1:28080 ──> Nextcloud container
   │
   └────── http://<MAC_LAN_IP>:28082 ──> Docker Desktop host network
                                                │
Nextcloud container ─────────────────────────────┤
                                                v
                                      signaling -> Janus/TURN

Cassini ExApp ── http://reverse-proxy ──> Nextcloud/Talk callbacks
```

#### Step 1: prepare Docker Desktop and the checkout

1. Install Docker Desktop 4.34 or newer.
2. Open Docker Desktop → **Settings** → **Resources** → **Network**.
3. Enable **Enable host networking**, then apply and restart Docker Desktop.
4. Install Go (`brew install go`); `bin/cassini` builds the CLI on every run.
5. Keep the checkout outside `~/Documents`, or grant Docker access to that
   directory. macOS TCC can otherwise reject Compose bind mounts with
   `operation not permitted`.
6. Allow incoming connections for Docker Desktop if the macOS firewall prompts.

Confirm the command-line prerequisites:

```bash
docker version
docker compose version
go version
```

#### Step 2: choose the configuration

Determine and inspect the LAN address yourself. This shell snippet is a
convenience for the operator; the harness receives only the resulting explicit
value:

```bash
LAN_IF="$(route -n get default | awk '/interface:/ { print $2; exit }')"
LAN_IP="$(ipconfig getifaddr "$LAN_IF")"
test -n "$LAN_IP" || { echo "Could not determine the Mac LAN IP" >&2; exit 1; }
printf 'Using interface %s with LAN IP %s\n' "$LAN_IF" "$LAN_IP"
```

For the full installed-ExApp stack, these inputs are required:

| Purpose | CLI flag | Environment variable | macOS value |
|---|---|---|---|
| Public mode | `--public-mode` | `CASSINI_HARNESS_PUBLIC_MODE` | `lan-http` |
| Browser-facing Nextcloud | `--public-url` | `CASSINI_HARNESS_PUBLIC_URL` | `http://127.0.0.1:28080` |
| WebRTC media address | `--media-host` | `CASSINI_HARNESS_MEDIA_HOST` | the non-loopback `$LAN_IP` |
| Browser-facing signaling | `--signaling-public-url` | `CASSINI_HARNESS_SIGNALING_PUBLIC_URL` | `http://${LAN_IP}:28082` |
| ExApp → Talk callback | `--talk-backend-url` | `CASSINI_TALK_BACKEND_URL` | `http://reverse-proxy` |
| Services | `--services` | `CASSINI_HARNESS_SERVICE_MODE` | `full` |
| Cassini installation | `--cassini` | `CASSINI_HARNESS_CASSINI_MODE` | `installed-exapp` |
| Talk recording backend | `--recording-backend` | `CASSINI_HARNESS_RECORDING_BACKEND` | `installed-exapp` |
| Image handling | `--build` | `CASSINI_HARNESS_EXAPP_IMAGE_MODE` | `build` |

`CASSINI_HARNESS_HOST` is not required for this topology. Leaving it unset keeps
room/player helpers pointed at loopback Nextcloud.

#### Step 3: validate the plan

```bash
./bin/cassini dev stack plan \
  --public-mode lan-http \
  --public-url http://127.0.0.1:28080 \
  --media-host "$LAN_IP" \
  --signaling-public-url "http://${LAN_IP}:28082" \
  --talk-backend-url http://reverse-proxy \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

The plan must end in `validation: ok` and show the selected media host,
signaling URL, and Talk backend URL. Missing values and loopback media or
signaling addresses are hard validation errors.

#### Step 4: start the stack

The same configuration can be passed directly to `stack up`:

```bash
./bin/cassini dev stack up \
  --public-mode lan-http \
  --public-url http://127.0.0.1:28080 \
  --media-host "$LAN_IP" \
  --signaling-public-url "http://${LAN_IP}:28082" \
  --talk-backend-url http://reverse-proxy \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

Startup is non-destructive by default. If matching resources already exist, the
command fails and tells you to choose `--resume` for a previously suspended
stack or `--reset` to remove and recreate the resolved stack and its volumes.
Do not add `--reset` unless that data loss is intended.

Equivalent environment-only configuration is:

```bash
export CASSINI_HARNESS_PUBLIC_MODE=lan-http
export CASSINI_HARNESS_PUBLIC_URL=http://127.0.0.1:28080
export CASSINI_HARNESS_MEDIA_HOST="$LAN_IP"
export CASSINI_HARNESS_SIGNALING_PUBLIC_URL="http://${LAN_IP}:28082"
export CASSINI_TALK_BACKEND_URL=http://reverse-proxy
export CASSINI_HARNESS_SERVICE_MODE=full
export CASSINI_HARNESS_CASSINI_MODE=installed-exapp
export CASSINI_HARNESS_RECORDING_BACKEND=installed-exapp
export CASSINI_HARNESS_EXAPP_IMAGE_MODE=build

./bin/cassini dev stack plan
./bin/cassini dev stack up
```

Explicit CLI flags override these environment variables. Empty environment
values count as unset.

#### Step 5: verify the running stack

`stack up` performs the host signaling reachability check automatically. For an
additional operator check:

```bash
curl -fsS "http://${LAN_IP}:28082/api/v1/welcome"
./bin/cassini dev stack status
open http://127.0.0.1:28080/
```

Log in as `admin` / `admin`, open Talk, and start a call. A healthy single-user
call shows **Call in progress** and **Waiting for others to join**. Nextcloud
remains on loopback HTTP so Chrome can request microphone/camera access without
`--unsafely-treat-insecure-host-as-secure`.

Two independent configuration mistakes otherwise produce the same Talk symptom:

| Signaling address | Nextcloud container | macOS browser |
|---|:---:|:---:|
| `127.0.0.1:28082` | no | yes |
| Docker bridge gateway (for example `172.18.0.1:28082`) | yes | no |
| Mac LAN IP (for example `192.168.1.67:28082`) | yes | yes |

#### Step 6: stop or remove the stack

```bash
# Stop containers while retaining state for a later `stack up ... --resume`.
./bin/cassini dev stack down --suspend

# Or remove all harness-owned containers and volumes, including the ExApp.
./bin/cassini dev stack down --full
```

#### Troubleshooting

| Symptom | Action |
|---|---|
| Plan rejects LAN configuration | Supply every required value from the configuration table; do not substitute loopback for media or signaling. |
| Host signaling preflight fails | Confirm Docker Desktop host networking is enabled, the LAN IP has not changed, the firewall permits Docker, and `curl http://$LAN_IP:28082/api/v1/welcome` works. |
| Bind mount reports `operation not permitted` | Move the checkout outside `~/Documents` or grant Docker Desktop access to the directory. |
| ExApp build times out fetching base-image metadata | Pre-pull the four build images below, then rerun the same command. |
| Python reports `No module named expat`, a missing `_XML_*` symbol, or fails while parsing `appinfo/info.xml` | The selected interpreter's native `pyexpat` extension is incompatible with its runtime Expat library. Pin the harness to Python 3.12, reload direnv, and run the import check below before retrying. |
| Existing resources error | Use `stack status`; choose `--resume`, intentional `--reset`, or `stack down --full` rather than letting the harness guess. |

The Expat failure happens in `harness/bin/lib-exapp-manifest.sh` while
`stack up` reads the ExApp image tag from `appinfo/info.xml`; it occurs before
Compose starts. The final `No module named expat` message does not necessarily
mean the module is absent: `pyexpat` can be present but unable to load because
its compiled symbols do not match the runtime library. Do not work around that
ABI mismatch with `DYLD_LIBRARY_PATH` or an ad-hoc Expat installation. Select a
working Python 3.12 interpreter and rebuild the direnv environment instead:

```bash
# In .envrc:
layout_python python3.12

# In the shell:
direnv allow
rm -rf .direnv/python-3.12  # only needed if the existing environment is stale

direnv reload
python3 -c 'import pyexpat, xml.etree.ElementTree as ET; print(pyexpat.EXPAT_VERSION)'
./bin/cassini dev stack plan
```

Then retry `stack up`. If the first attempt reached Docker despite a different
failure, inspect `stack status` and follow the lifecycle guidance rather than
adding `--reset` automatically.

```bash
docker pull debian:bookworm-slim
docker pull golang:1.24-bookworm
docker pull node:22-bookworm
docker pull node:22-bookworm-slim
```

### 5.3 AppAPI-only local dev

For admin/viewer proxy route work that does not need Janus/signaling/TURN:

```bash
./bin/cassini dev stack up \
  --services appapi \
  --cassini installed-exapp \
  --recording-backend none \
  --build
```

### 5.4 Open and validate

Default users:

- admin: `admin` / `admin`
- standard viewer user created by ExApp install: `alice` / `Tn8mY3qVrJ2x!E2e`

Default URLs:

- Nextcloud: `http://127.0.0.1:28080/`
- Cassini AppAPI proxy: `http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini`
- Cassini UI: open the "Cassini" entry in the Nextcloud top bar (admins get the operator surface inside it)

Create a room and play media:

```bash
CALL_URL="$(./bin/cassini dev room create --name "Installed ExApp local dev" | tail -n1)"
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

Private installed-ExApp validation helper (requires host `ffmpeg` and
`ffprobe` to decode the downloaded portable artifact):

```bash
git lfs pull \
  --include="harness/media/processed/showcase-lantern-festival-v1/mira.ivf,harness/media/processed/showcase-lantern-festival-v1/mira.ogg"

./harness/bin/validate-installed-exapp-private-talk.sh \
  --run-count 1 \
  --media-prefix "$PWD/harness/media/processed/showcase-lantern-festival-v1/mira" \
  --duration 30
```

The helper consumes an already-running installed stack. It scaffolds private
Talk users/conversations, drives playback into the admin call, waits for the
new ExApp job, and requires a matching viewer artifact with positive segments
and decoded words. Its default two-run mode additionally verifies that prior
catalog entries remain available.

CI and exact-image local validation use the stack-owning faithful vertical:

```bash
IMAGE_REF=cassini-exapp:local-faithful
docker build -f deployment/Dockerfile.exapp -t "$IMAGE_REF" .
IMAGE_REF="$IMAGE_REF" ./harness/bin/ci-e2e-installed-exapp-talk.sh
```

That script accepts exactly one prebuilt image, tags it for `reuse-local`, lets
AppAPI/HaRP create `nc_app_gocassini`, verifies the installed image ID and
manifest-gated Talk configuration, performs one recording, validates the
viewer artifact, and treats D-493 teardown/leak checks as part of the pass. It
never retries a failed recording.

#### D-403 manifest allow-list sensitivity control (negative run)

The same orchestrator runs its own negative control. AppAPI injects only the env
keys the manifest *declares*, so deleting the
`CASSINI_TALK_SIGNALING_INTERNAL_SECRET` `<variable>` from `appinfo/info.xml`
makes the installed operator report the signaling secret unconfigured and fails
the install at route verification — the exact D-403 regression the faithful gate
exists to catch. `make-negative-manifest.py` generates that stripped manifest on
demand (it never ships checked in, so it cannot drift as `<version>` bumps), and
`D453_EXPECT_CONFIG_FAILURE=1` tells the orchestrator to assert the boundary
instead of recording:

```bash
git lfs pull \
  --include="harness/media/processed/showcase-lantern-festival-v1/mira.ivf,harness/media/processed/showcase-lantern-festival-v1/mira.ogg"

# 1. Generate a manifest identical to appinfo/info.xml minus the signaling secret.
python3 harness/bin/make-negative-manifest.py \
  appinfo/info.xml /tmp/info-no-signaling.xml

# 2. Run the control. It exits 0 when the boundary held (secret unconfigured,
#    no recording), non-zero if the secret leaked in or the install died for an
#    unrelated reason.
IMAGE_REF=cassini-exapp:local-faithful \
D453_EXPECT_CONFIG_FAILURE=1 \
D453_MANIFEST_PATH=/tmp/info-no-signaling.xml \
LOG_DIR=/tmp/d403-control \
  ./harness/bin/ci-e2e-installed-exapp-talk.sh

# 3. Read the machine-checkable verdict from the retained evidence.
jq '.result, .control' /tmp/d403-control/summary.json
# result           => "sensitivity-control-passed"
# control.signaling_internal_secret_configured => false   (the Done-when)
# control.recording_performed                  => false   (a fact about the run)
# control.branch                               => "stack-verification"
```

On stack-up failure the ExApp container is still installed and serving through
the AppAPI proxy, so the control probes `/operator/status` directly and asserts
the parsed value: `signaling_internal_secret_configured == false` *and*
`secret_configured == true`. The second half is differential — it proves the
*only* difference is the missing declaration, not a wholesale env-injection
failure. The offline generator contract test runs on every PR with no Docker:

```bash
./harness/bin/test-negative-manifest.sh
```

In CI this control is a `workflow_dispatch`-only job in
`publish-exapp-image.yml`, **"D-403 manifest allow-list sensitivity control
(CPU)"** — distinct from the required per-PR "Faithful installed ExApp Talk
artifact (CPU)" gate. Trigger it from the Actions tab ("Run workflow"). The
cheap always-on tiers are `test-negative-manifest.sh` (ci.yml contracts) and the
required-fields marker lint in `validate-manifest` that fails per-PR if either
Talk secret `<variable>` disappears from `appinfo/info.xml`.

---

## 6. Guide: remote HTTPS dev setup

Use this when the harness runs on a Linux Docker host but the browser is on
another machine, for example a Mac. The browser reaches Nextcloud and signaling
through trusted HTTPS proxying (Tailscale Serve is the common path), while media
services advertise the Linux host's reachable Tailnet/LAN IP.

### 6.1 Prerequisites

On the Linux harness host:

- Docker Engine + Docker Compose v2
- `tailscale` logged in, if using Tailscale Serve
- `jq` for the snippets below
- firewall/Tailnet policy that allows the browser to reach WebRTC media ports on
  the Linux host (`13479` for TURN plus the Janus/Coturn ranges rendered in the
  config, currently Janus RTP `20000-20100` and Coturn relay `49160-49200`)

### 6.2 Export remote environment for multi-command workflows

Flags are enough for a single `stack up`, but later commands such as `room
create` need the same public URL to print browser-safe call URLs. Export the
remote values once:

```bash
TS_FQDN="$(tailscale status --self --json | jq -r '.Self.DNSName | sub("\\.$"; "")')"
TS_IP="$(tailscale ip -4)"

export CASSINI_HARNESS_PUBLIC_MODE=remote-https
export CASSINI_HARNESS_PUBLIC_URL="https://${TS_FQDN}"
export CASSINI_HARNESS_PUBLIC_HOST="${TS_FQDN}"
export CASSINI_HARNESS_MEDIA_HOST="${TS_IP}"
export CASSINI_HARNESS_SIGNALING_PUBLIC_URL="https://${TS_FQDN}:8443"
```

### 6.3 Start HTTPS proxying

With Tailscale Serve:

```bash
sudo tailscale serve --bg --https=443 http://127.0.0.1:28080
sudo tailscale serve --bg --https=8443 http://127.0.0.1:28082
```

The browser uses:

- Nextcloud: `https://$TS_FQDN/`
- Talk signaling: `https://$TS_FQDN:8443`

### 6.4 Start the remote-safe full stack

Base remote browser harness:

```bash
./bin/cassini dev stack plan --services full-remote
./bin/cassini dev stack up --services full-remote --reset
```

Remote installed-ExApp harness:

```bash
./bin/cassini dev stack up \
  --services full-remote \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build \
  --reset
```

Equivalent flag-only shape, useful in scripts that do not export environment:

```bash
./bin/cassini dev stack up \
  --public-mode remote-https \
  --public-url "https://${TS_FQDN}" \
  --public-host "${TS_FQDN}" \
  --media-host "${TS_IP}" \
  --signaling-public-url "https://${TS_FQDN}:8443" \
  --services full-remote \
  --reset
```

Remote mode renders:

- signaling backend allowlist entries for local, host, and public Nextcloud URLs
- Janus `nat_1_1_mapping` using `CASSINI_HARNESS_MEDIA_HOST`
- Coturn `external-ip` / `relay-ip` using `CASSINI_HARNESS_MEDIA_HOST`
- a Docker-network HTTPS helper (`signaling-public-proxy`) for Nextcloud's
  server-side HPB notification checks on hosts where containers cannot hairpin
  to host ports

The browser still connects to the real host signaling service through the HTTPS
proxy (`https://$TS_FQDN:8443`).

### 6.5 Create a room and use it from the remote browser

Because the remote public environment is exported, `create-room` prints the
HTTPS URL:

```bash
CALL_URL="$(./bin/cassini dev room create --name "Remote HTTPS smoke" | tail -n1)"
echo "$CALL_URL"
```

Open the URL in the remote browser, or stream a synthetic participant:

```bash
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

### 6.6 Remote troubleshooting

- Run `./bin/cassini dev stack plan --services full-remote` and confirm
  `public.mode` is `remote-https`, `public.url` is HTTPS, and `media_host` is not
  loopback.
- If remote env vars are set but you want a local run, pass explicit
  `--public-mode local-http`; the CLI masks ambient remote values for that one
  invocation.
- If room creation fails server-side even though browser proxying works, inspect
  the `signaling-public-proxy` service; it exists to satisfy Nextcloud/Talk's
  server-side HPB notification probes inside the Docker network.
- If users join but no media flows, check the host firewall/Tailscale ACLs for
  the TURN and Janus media ports listed above.

---

## 7. Guide: direct operator / HPB-internal debugging

For direct recorder/operator debugging without installing Cassini as an ExApp:

```bash
./bin/cassini dev stack up \
  --services full \
  --cassini none \
  --recording-backend direct-operator
```

Talk recording will be configured to reach a standalone recording backend on the
host/gateway (port `4000` by default, unless `CASSINI_TALK_RECORDING_URL` is set).

### D-283 internal HPB proof

The harness carries a standalone signaling `internalsecret` in:

- `harness/config/signaling.conf`
- `harness/bin/lib/stack-env.sh` as `SIGNALING_INTERNAL_SECRET`

If you change signaling config or the secret, restart the `signaling` service
before running the proof path.

Minimal proof flow from the repo root:

```bash
source harness/bin/common.sh
harness_stack_env_resolve

./bin/cassini dev stack up --services full --recording-backend legacy --reset
CALL_URL="$(./bin/cassini dev room create --name "D-283 internal proof" | tail -n1)"
./bin/cassini dev player video --call-url "$CALL_URL" --duration 20 &
PLAYER_PID=$!

CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET" \
CASSINI_TALK_SIGNALING_INTERNAL_SECRET="$SIGNALING_INTERNAL_SECRET" \
./bin/cassini record --call "$CALL_URL" --duration 15 --out /tmp/d283-internal.run

wait "$PLAYER_PID"
```

Notes:

- `cassini record` defaults to `hpb-internal` mode for Talk recordings.
- Use `--talk-auth-mode guest-participant` only when intentionally testing the
  legacy fallback path.
- If the run ends with `no remuxable streams`, treat that as a media-routing /
  runtime acceptance issue, not necessarily an auth/bootstrap failure.

Focused debug checklist for internal-mode failures:

1. confirm `CASSINI_TALK_RECORDING_SECRET` is set
2. confirm `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` matches signaling
   `internalsecret`
3. confirm Talk signaling settings advertise a reachable standalone signaling URL
   for both Nextcloud and the recorder
4. if `hello` fails with `invalid_client_type`, the signaling server did not pick
   up `internalsecret`
5. if `hello` fails with `invalid_token` / `auth_failed`, the internal secret
   does not match
6. if join succeeds but no streams arrive, inspect the room `join` / participants
   events, subscriber creation, and `requestoffer` seam

---

## 8. Guide: Talk recording-backend lifecycle proof

Use `harness/bin/d263-nextcloud-lifecycle.sh` to test the native Talk
recording-backend lifecycle against a real local Nextcloud/Talk stack without
requiring real browser media capture.

The script:

- starts a temporary `cassini-operator` on port `14000`
- configures Talk's recording backend to call that operator
- creates and activates a Talk room
- starts recording through Nextcloud's Talk recording API
- uses a fake `cassini` executable only for the media worker
- stops recording through Nextcloud's Talk recording API
- waits for the operator job to reach `done/succeeded`

Run it after a harness stack is up:

```bash
./bin/cassini dev stack up --services full --recording-backend direct-operator
./harness/bin/d263-nextcloud-lifecycle.sh
```

Useful overrides:

```bash
OPERATOR_PORT=14001 ./harness/bin/d263-nextcloud-lifecycle.sh
KEEP_RUNTIME=1 ./harness/bin/d263-nextcloud-lifecycle.sh
CASSINI_OPERATOR_BIN=/abs/path/to/cassini-operator ./harness/bin/d263-nextcloud-lifecycle.sh
```

This is not the full media acceptance test. It validates the Nextcloud/Talk
start/stop/backend lifecycle and operator pipeline handoff. Browser media,
HPB/WebRTC packet capture, remux, and Cassini viewer playback belong to the
manual full-media path or the full E2E roundtrip scripts.

---

## 9. Media and scenario guides

### 9.1 Showcase meeting

The preferred demo and cleanup-evaluation sample is the showcase meeting:

```bash
./bin/cassini dev fixture prepare-showcase
CALL_URL="$(./bin/cassini dev room create --name "Lantern Festival Demo" | tail -n1)"
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

That showcase scenario is synthetic, but it is written more like a real meeting
and is the better sample for judging transcript cleanup quality.

Roundtrip it end to end with the synthetic meeting roundtrip script by pointing
at a real Talk room:

```bash
./harness/bin/roundtrip-synthetic-meeting.sh \
  --scenario ./harness/scenarios/showcase-lantern-festival.v1.json \
  --output-dir ./harness/media/processed/showcase-lantern-festival-v1 \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

### 9.2 Harness synthetic fixture

The original synthetic fixture remains useful for harness coverage. It gives us:

- spoken language instead of synthetic sine audio
- stable participant names and join delays
- overlaps, abbreviations, dates, filenames, and code terms
- a repeatable reference transcript for transcriber tuning

Recommended local setup:

```bash
uv run --python 3.12 --with-requirements harness/requirements-tts.txt python --version
```

The wrappers default to `uv run --python 3.12`, so they can provision a
compatible interpreter on demand. The realistic backend uses `kokoro-onnx`
under the hood and caches its model files on first run under
`$XDG_CACHE_HOME/gocassini/kokoro-onnx` or `~/.cache/gocassini/kokoro-onnx`.

Override the Python version if needed:

```bash
UV_PYTHON=3.12 ./harness/bin/prepare-synthetic-meeting.sh
```

Force a specific preinstalled interpreter:

```bash
PYTHON_BIN=/path/to/python3.12 ./harness/bin/prepare-synthetic-meeting.sh
```

Generate the default fixture only:

```bash
./harness/bin/prepare-synthetic-meeting.sh
```

This writes a scenario-driven generated media set under
`harness/media/processed/synthetic-pied-piper-v1/`, including:

- one media prefix per participant (`.mp4`, `.ivf`, `.ogg`)
- `manifest.json`
- `reference.txt`

The tracked inputs for this flow live in `harness/scenarios/` plus the generator
scripts; the rendered media stays gitignored.

The current default scenario is intentionally still harness-oriented. It is good
for repeatable timing, joins, and transcript plumbing, but it is not the best
demo or cleanup-evaluation sample.

Play it into a room:

```bash
CALL_URL="$(./bin/cassini dev room create --name "Synthetic Pied Piper Review" | tail -n1)"
./harness/bin/stream-synthetic-meeting.sh --call-url "$CALL_URL"
```

The current default scenario is a six-person Pied Piper review. The player uses
the scenario join delays both for room entry and media timeline alignment, so
late-join playback lands on the intended absolute meeting time instead of being
delayed twice.

Run the full cloud/local roundtrip in one command:

```bash
./harness/bin/roundtrip-synthetic-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

That flow will:

- generate or reuse the synthetic meeting media
- publish it into the real Talk room
- record the meeting into one MKV with the actual Go recorder
- run `cassini-publisher/bin/process-meeting.sh --bundle-viewer`
- leave you with `meeting.webm`, `transcript.words.v1.json`, `captions.vtt`,
  `manifest.json`, and a static viewer bundle rooted at `index.html` with
  `catalog.json` plus `meetings/<meeting-id>/...` artifact files

If you want to test the plumbing without installing the TTS model yet:

```bash
./harness/bin/prepare-synthetic-meeting.sh --backend mock --force
```

That mock path uses only the lightweight core requirements, so it stays fast
even when the full Kokoro stack is not installed yet.

### 9.3 Three-stream sync test (Go)

The three-stream test uses full-length songs with alignment by start-delay
padding, not trimming:

- `Giulia 2:09 == Vibrazioni 2:16`
- `Giulia 0:42 == Frankie 0:44`

`prepare-youtube-set.sh` pipeline:

1. download sources with `uvx yt-dlp` (skips download if file already exists)
2. render aligned full-length MP4 files
3. transcode aligned files to WebRTC-friendly VP8/Opus files (`.ivf` + `.ogg`)

Then `stream-three-songs.sh` starts three players with staggered joins and
rotates audio audibility every five seconds by muting at sender packet emission
time, so muted tracks stop sending RTP audio packets.

Default display names:

- `Le Vibrazioni - Giulia`
- `Elio e le Storie Tese - Spalman`
- `Frankie Hi-NRG MC - Chiedi Chiedi`

Start a local room and stream:

```bash
./bin/cassini dev stack up --services full
CALL_URL="$(./bin/cassini dev room create --name "3-song room" | tail -n1)"
./bin/cassini dev player three-songs --call-url "$CALL_URL"
```

Join delay override:

```bash
./harness/bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --join-delay-giulia 4 \
  --join-delay-vibrazioni 6 \
  --join-delay-frankie 11
```

Audio-ready override, useful when aligned media includes initial silence padding:

```bash
./harness/bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --audio-ready-after-giulia 7 \
  --audio-ready-after-vibrazioni 0 \
  --audio-ready-after-frankie 5
```

Custom labels:

```bash
./harness/bin/stream-three-songs.sh \
  --call-url "$CALL_URL" \
  --name-spalman "Elio e le Storie Tese - Spalman"
```

Prep only:

```bash
./harness/bin/prepare-youtube-set.sh
./harness/bin/prepare-youtube-set.sh --force
```

Cloud room test:

```bash
./harness/bin/stream-three-songs.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --skip-prepare
```

Continuous retry loop for on/off manual validation windows:

```bash
./harness/bin/stream-three-songs-until.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --until "08:00" \
  --skip-prepare
```

You can also pass an absolute stop time:

```bash
./harness/bin/stream-three-songs-until.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --until "2026-03-03 08:00" \
  --skip-prepare
```

Recorder + capture in one command:

```bash
CALL_URL="$(./bin/cassini dev room create --name "3-song-recorded" | tail -n1)"
./harness/bin/record-three-songs.sh \
  --call-url "$CALL_URL" \
  --duration 180 \
  --skip-prepare \
  --output /tmp/three-songs.mkv
```

This generates:

- raw recorder output MKV (`/tmp/three-songs.mkv`)
- session artifact directory under `/tmp/sessions/<meeting_id>/`
- sync validation output (`verify-sync-from-report.sh`, auto-run unless
  `--skip-sync-check`)
- recorder/player logs (`/tmp/three-songs.mkv.recorder.log`,
  `/tmp/three-songs.mkv.publisher.log`)

The MKV is the primary meeting artifact and carries Cassini metadata inside the
container itself. Add `--write-report` to the recorder invocation if you also
want the legacy external JSON sidecar for debug/export workflows. If you
explicitly need a separate compatibility archive path, pass
`--archive-output /tmp/three-songs.csr`.

Run sync validation manually:

```bash
# requires the legacy sidecar; run recorder with --write-report first
./harness/bin/verify-sync-from-report.sh \
  --recording /tmp/three-songs.mkv \
  --report /tmp/three-songs.mkv.json \
  --tolerance 0.35
```

### 9.4 Basic media preparation

Small local sample media assets are generated by:

```bash
./harness/bin/prepare-media.sh
```

The basic player flow uses those sample assets:

```bash
CALL_URL="$(./bin/cassini dev room create --name "Basic video room" | tail -n1)"
./bin/cassini dev player video --call-url "$CALL_URL" --duration 20
```

---

## 10. Repository structure and operational reference

### 10.1 Structure

- `compose.yml`: local stack (Nextcloud + Postgres + optional AppAPI and full
  WebRTC services)
- `config/`: signaling, Janus, and Coturn config used by `compose.yml`
- `runtime/generated/`: rendered remote/full-profile config generated at stack
  bring-up time
- `bin/`: operator scripts
  - `up.sh`, `down.sh`, `status.sh`: stack lifecycle
  - `bootstrap.sh`: Nextcloud/Talk bootstrap and app settings
  - `create-room.sh`: create Talk room and print call URL
  - `prepare-media.sh`: small local sample media assets (`.mp4` + `.ivf` +
    `.ogg` + `.ulaw`)
  - `prepare-synthetic-meeting.sh`: meeting-like multi-speaker media fixture
    generation
  - `prepare-synthetic-meeting.py`: TTS-backed generator used by the shell
    wrapper
  - `prepare-youtube-set.sh`: YouTube download + alignment + WebRTC transcode
  - `stream-synthetic-meeting.sh`: play a synthetic meeting fixture with
    realistic names and join delays
  - `stream-video.sh`: basic player flow using the Go rotator and local sample
    assets
  - `roundtrip-synthetic-meeting.sh`: record a real Talk meeting MKV, then build
    the transcriber + publisher + viewer artifact bundle
  - `stream-three-songs.sh`: three-client synchronized player flow
  - `stream-three-songs-until.sh`: retrying loop for continuous cloud/local soak
    until a wall-clock time
  - `record-three-songs.sh`: Go recorder + three-client stream capture in one
    command
  - `verify-sync-from-report.sh`: compare final MKV stream start offsets against
    recorder report expectations
  - `verify-av-drift.sh`: compare paired audio/video elapsed time in a final MKV
  - `verify-session-artifact.sh`: validate session artifact structure beside a
    final MKV
  - `smoke.sh`: end-to-end smoke run
- `go-talk-rotator/`: Go room player used by streaming scenarios
- `media/`: test inputs and processed generated fixtures (`raw/`, `aligned/`,
  `webrtc/`, `processed/`), gitignored except placeholders/notices
- `runtime/`: runtime state (`last_call_url`, `last_room_token`, logs)
- `scenarios/`: tracked synthetic meeting scenario definitions

### 10.2 Default local ports

| Service | Port |
|---|---:|
| Nextcloud HTTP | `28080` |
| Standalone signaling HTTP | `28082` |
| Janus WebSocket (signaling -> Janus) | `28188` on loopback |
| NATS | `14222` on loopback |
| TURN | `13479` |
| Janus RTP range | `20000-20100` |
| Coturn relay range | `49160-49200` |

### 10.3 Runtime outputs

Common runtime outputs:

- `harness/runtime/last_call_url`
- `harness/runtime/last_room_token`
- `harness/runtime/generated/*` remote/full-profile rendered config
- `/tmp/gocassini-*` CI outputs and logs
- `/tmp/sessions/<meeting_id>/` recorder session artifacts
- generated media under `harness/media/processed/...`

---

## 11. Teardown reference

Bare `down` removes containers but keeps volumes:

```bash
./bin/cassini dev stack down
```

Stop containers and keep them resumable:

```bash
./bin/cassini dev stack down --suspend
./bin/cassini dev stack up --resume
```

Remove current project containers and volumes:

```bash
./bin/cassini dev stack down --volumes
```

Remove all known harness-owned resources, including installed ExApp containers
and volumes:

```bash
./bin/cassini dev stack down --full
```

If a stack was created with a non-default `PROJECT_NAME`, keep that environment
set for project-scoped `status`, `down`, `--resume`, or `--volumes` operations.
`--full` also sweeps the default harness projects (`spreedtest` and
`cassini-exapp-test`) in addition to the current `PROJECT_NAME`.
