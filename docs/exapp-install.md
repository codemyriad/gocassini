# Installing Cassini as a Nextcloud ExApp

This is the **production install guide**. Cassini ships as a Nextcloud AppAPI
external app: one container exposes the admin control-panel, the recording
viewer, the published meeting archive, and the Talk recording backend over a
single HTTP port; Nextcloud's AppAPI proxy fronts it with per-route access
enforcement.

The flow is deliberately two-phase and reversible:

1. **Install and verify the ExApp** — deploy daemon, app registration,
   proxy-route checks. Talk is untouched.
2. **Hand Nextcloud Talk's recording over to Cassini** — back up the current
   backend, switch, run a controlled test, keep the rollback command ready.

All `occ …` commands below are shorthand for however your deployment invokes
occ (e.g. `sudo -u www-data php occ …` or
`docker exec -u www-data <nc-container> php occ …`).

The standalone Docker Compose bundle under `deployment/` is **not** the app
install — see [Standalone operator (dev/staging only)](#standalone-operator-devstaging-only).

## Prerequisites

- Nextcloud **32 or newer** (the manifest's `min-version`; Cassini targets and
  is tested against Nextcloud 33+) with the **AppAPI** app installed and
  enabled.
- A registered AppAPI **deploy daemon** (next section).
- A Docker engine for the ExApp container. For GPU transcription it needs the
  NVIDIA driver + Container Toolkit (see [GPU transcription](#gpu-transcription-cuda)).

Persistent storage is automatic: AppAPI creates a named volume for every
docker-deployed ExApp and the operator stores all durable data under it
(see [Persistent storage](#persistent-storage)).

## Step 1 — Register a deploy daemon (HaRP)

Use a **HaRP** daemon. Upstream AppAPI recommends HaRP; the older Docker
Socket Proxy daemon is deprecated and scheduled for removal in Nextcloud 35.

Run HaRP next to a Docker engine (full options in the
[HaRP README](https://github.com/nextcloud/HaRP)):

```bash
docker run \
  -e HP_SHARED_KEY="<generate-a-strong-ascii-key>" \
  -e NC_INSTANCE_URL="https://cloud.example.com" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$(pwd)"/certs:/certs \
  --name appapi-harp -h appapi-harp \
  --restart unless-stopped \
  -p 8780:8780 -p 8782:8782 \
  -d ghcr.io/nextcloud/nextcloud-appapi-harp:release
```

Then register it in Nextcloud → Administration settings → AppAPI →
**Register Daemon** (template "HaRP Proxy"), and run **Test deploy** from the
daemon's three-dot menu before going further. `occ app_api:daemon:list` should
show it afterwards.

## Step 2 — Pick an image tag

CI publishes to `ghcr.io/codemyriad/gocassini`:

| Tag | What it is |
|---|---|
| `X.Y.Z` | CPU release build. Immutable by convention; matches `<version>`/`<image-tag>` in `appinfo/info.xml` |
| `X.Y.Z-cuda` | CUDA release build (CUDA 12 / cuDNN 9 sherpa-onnx, fp32 Parakeet model, `CASSINI_STT_DEVICE=cuda`) |
| `X.Y.Z-rocm` | Alias of the CPU build so ROCm-tagged daemons install; no ROCm acceleration yet |
| `sha-<shortsha>` / `sha-<shortsha>-cuda` | Every pushed commit, for pinning a specific build |
| `latest` / `latest-cuda` / `latest-rocm` | Convenience tags — fine for demos, **not** for production installs |

`appinfo/info.xml` pins `<image-tag>` to the release version, so a default
AppAPI install (and any later reinstall) pulls exactly the build the
registered app version was cut from instead of whatever `latest` points at
that day.

### Cutting a release

```
./scripts/bump-exapp-version.sh 0.2.0   # bumps <version> + <image-tag> together
git commit -am "release: 0.2.0"
git tag v0.2.0
git push origin main v0.2.0
```

The tag push publishes `0.2.0`, `0.2.0-cuda`, and `0.2.0-rocm`. CI refuses to
publish when the git tag and the manifest version disagree, or when
`<image-tag>` drifts from `<version>`.

You don't select the `-cuda` tag by hand: when the deploy daemon's compute
device is CUDA, AppAPI automatically tries `<image-tag>-cuda` first and falls
back to the plain tag.

The checked-in manifest already pins the current release; to install a
different build, download `appinfo/info.xml`, set `<image-tag>` to the
desired `sha-…` or release tag, and register from that local copy.

## Step 3 — Register the app

Generate a fresh Talk recording secret — never reuse a value from this repo or
its examples:

```bash
CASSINI_SECRET="$(openssl rand -hex 32)"
```

Register from a pinned manifest (`--info-xml` accepts a local path or a raw
URL; pin a tag or commit SHA, not a moving branch):

```bash
curl -fsSL "https://raw.githubusercontent.com/codemyriad/gocassini/<tag-or-sha>/appinfo/info.xml" \
    -o /tmp/gocassini-info.xml
# Optional: override <image-tag> in the copy to a specific sha-… build (Step 2)

occ app_api:app:register gocassini <daemon-name> \
    --info-xml /tmp/gocassini-info.xml \
    --env CASSINI_TALK_RECORDING_SECRET="${CASSINI_SECRET}" \
    --wait-finish
```

If `occ` runs inside a container, copy the manifest in first
(`docker cp /tmp/gocassini-info.xml <nc-container>:/tmp/`) or pass the raw URL
directly to `--info-xml`.

`<daemon-name>` is the `Name` column of `occ app_api:daemon:list`. AppAPI
pulls the image, creates the persistent volume, starts the container
(`nc_app_gocassini`), and enables the app once the container answers its
heartbeat and reports init completion.

### App configuration (`--env`)

These variables are declared under `<environment-variables>` in
`appinfo/info.xml`. That declaration is what makes them settable at all:
AppAPI only passes declared variables to the container, and `--env` values
for undeclared keys are **silently dropped**. Set them at registration time,
either on the command line as above or in the External Apps admin UI (Deploy
Options).

| Variable | Required | What it does |
|---|---|---|
| `CASSINI_TALK_RECORDING_SECRET` | For the Talk record button | Shared secret for Talk's recording backend protocol; must match the `secret` in `spreed`'s `recording_servers` (Step 5). Unset, the operator rejects every recording request |
| `CASSINI_TALK_BACKEND_URL` | No | Override for operator→Talk callbacks (started/stopped notifications, recording upload). Leave empty to use the backend URL Talk sends with each request |
| `OPENROUTER_API_KEY` | No | API key for LLM transcript cleanup + meeting summaries. Unset, raw transcripts are published without summaries |
| `LLM_BASE_URL` | No | OpenAI-compatible API base URL; defaults to `https://openrouter.ai/api/v1` when `OPENROUTER_API_KEY` is set |
| `LLM_MODEL` | No | Model for cleanup/summaries (default `openai/gpt-4o-mini`) |

### Runtime environment reference

AppAPI injects these on every container start, regardless of daemon flavor.
You only supply them yourself when running the image outside AppAPI (dev,
smoke tests):

| Variable | What it does |
|---|---|
| `APP_HOST` / `APP_PORT` | Bind address inside the container (default `0.0.0.0:8080`) |
| `APP_ID` | Must match the `<id>` in `appinfo/info.xml` (`gocassini`) |
| `APP_VERSION` | Must match the `<version>` in the manifest |
| `APP_SECRET` | Shared secret with AppAPI; enables the AppAPI auth middleware |
| `AA_VERSION` | AppAPI version Nextcloud is running |
| `NEXTCLOUD_URL` | Base URL the app uses to call back into Nextcloud (init-progress report, OCS calls). Without it, `--wait-finish` hangs until its timeout |
| `APP_PERSISTENT_STORAGE` | Mount path of AppAPI's persistent volume; the operator defaults its data roots under it |
| `COMPUTE_DEVICE` | `cpu` / `cuda` / `rocm`, from the daemon's compute-device setting |

**HaRP-tunnel-only** variables — AppAPI injects these only when deploying
through a HaRP daemon without direct connect; the entrypoint starts `frpc`
when they are present and runs the operator directly otherwise (Docker Socket
Proxy daemons, HaRP direct-connect, manual installs):

| Variable | What it does |
|---|---|
| `HP_FRP_ADDRESS` / `HP_FRP_PORT` / `HP_SHARED_KEY` | HaRP tunnel parameters used by `frpc` |

`CASSINI_APPAPI_REQUIRED=true` is baked into the ExApp image (not injected by
AppAPI); it makes the operator refuse to start without `APP_SECRET`.

## Step 4 — Verify the install (before touching Talk)

All of these must pass before the Talk handoff:

1. `occ app_api:daemon:list` shows the daemon and its **Test deploy** passes.
2. `occ app_api:app:list` shows `gocassini` enabled.
3. The Nextcloud app menu shows a **Cassini** entry for every logged-in user
   (opens the viewer) and a **Cassini Admin** entry for admins only (opens
   the control panel). The app registers both with AppAPI when it is
   enabled; if they are missing, check the container log for `exapp ui:`
   errors, then disable and re-enable the app to retry the registration.
4. The container runs the intended image:
   `docker inspect nc_app_gocassini --format '{{.Config.Image}}'`.
5. The Talk welcome endpoint answers through the AppAPI proxy (it is a PUBLIC
   route, so plain curl works):

   ```bash
   curl -fsS https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/api/v1/welcome
   # → {"version":1}
   ```

6. The admin control panel renders for an admin user (the **Cassini Admin**
   menu entry, or directly):
   `https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/control-panel/`
7. The viewer renders for a normal logged-in user (the **Cassini** menu
   entry, or directly):
   `https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/viewer/`
8. The doctor/status endpoint reports `"ok": true` (ADMIN route — use an
   admin login with an app password):

   ```bash
   curl -fsS -u admin:<app-password> \
     https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status
   ```

   It reports the app version, the STT device (`cpu`/`cuda`) and whether that
   device is actually usable, whether the Talk recording secret is configured
   (never the value), and DB/storage health — the same answers that used to
   require shell access into the container.
9. CUDA installs only: the image tag ends in `-cuda` and the container can see
   the GPU — `docker exec nc_app_gocassini nvidia-smi`. The status endpoint in
   the previous step must show `"device": "cuda"` with `"device_usable": true`;
   a CUDA container without GPU access also logs
   `ERROR: stt_device cuda is not usable` at startup instead of silently
   falling back to CPU.

## Step 5 — Talk handoff (reversible)

Point Talk's recording backend at the AppAPI proxy base. The `api/v1/welcome`
and `api/v1/room/*` routes are declared PUBLIC in the manifest, so Talk's
recording protocol (authenticated by its own HMAC, not a Nextcloud session)
passes through the proxy.

**Back up the current backend first**, then switch:

```bash
# 0. Back up (empty output = no recording backend configured)
occ config:app:get spreed recording_servers | tee /root/recording_servers.backup

# 1. Switch — same secret you registered the app with in Step 3
occ config:app:set spreed recording_servers --value='{"servers":[{"server":"https://cloud.example.com/index.php/apps/app_api/proxy/gocassini","verify":true}],"secret":"<secret>"}'
occ config:app:set spreed call_recording --value=yes
```

**Controlled test** — use a non-critical, *public* room:

1. Create a public test conversation, join the call.
2. Start recording. A `CassiniRecorder` guest joins within ~10–15 s and the
   recording indicator turns on.
3. Speak for a minute, stop the recording, leave the call.
4. Watch the job progress through record → build → publish in the control
   panel. The audio file is uploaded back to Talk (stored in the recording
   owner's attachments folder, with a notification to share it into the
   chat); the transcript/summary appears in the viewer.

**Rollback** — restore the saved value and Talk records through the previous
backend again; the Cassini ExApp can stay installed:

```bash
occ config:app:set spreed recording_servers --value="$(cat /root/recording_servers.backup)"
# or, if there was no recording backend before:
occ config:app:delete spreed recording_servers
```

Keep the previous backend running until your test recording passes.

### Known limitation: public conversations only

The recorder joins calls as an anonymous guest, and Talk only admits guests
into **public** conversations — so the record button currently works in
public rooms only; group and one-to-one conversations cannot be recorded.
The recorder is also visible in the call as a `CassiniRecorder` guest
participant. Supporting non-public conversations (the hidden internal-client
join used by the reference recorder) is tracked as follow-up work.

## GPU transcription (CUDA)

`latest-cuda` is a real CUDA build: CUDA-enabled sherpa-onnx/onnxruntime
libraries, the fp32 Parakeet model, and `CASSINI_STT_DEVICE=cuda` baked in.
The GPU accelerates the **transcription (build) stage**; live call capture is
CPU-bound either way.

To use it, set the deploy daemon's **Compute device** to CUDA. AppAPI then
pulls `<image-tag>-cuda` automatically and attaches the host's NVIDIA GPUs to
the container via Docker device requests. The Docker engine running the ExApp
needs the NVIDIA driver + [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/);
verify with `docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi`
on that engine before registering the app.

### Remote GPU node

If the Nextcloud host has no GPU, HaRP can drive a **remote Docker engine**
over its FRP tunnel (see "Remote Docker Engines" in the
[HaRP README](https://github.com/nextcloud/HaRP)):

1. Install Docker + NVIDIA Container Toolkit on the GPU node.
2. Copy the client certificates from the HaRP container's `/certs/frp` and
   run `frpc` on the GPU node to tunnel its Docker socket back to HaRP
   (one remote port per engine, 24001–24099).
3. Register a second deploy daemon for that engine with Compute device =
   CUDA, and register (or re-register) `gocassini` against it.

**Docker-in-LXC note:** if the GPU "node" is an LXC container running Docker
(e.g. on Proxmox), the NVIDIA stack must work *inside* the LXC: the
`/dev/nvidia*` devices have to be passed through and their cgroup device
majors kept in sync across host reboots — see
[`docs/proxmox-jellyfin-nvidia.md`](./proxmox-jellyfin-nvidia.md) for the
device-major pitfalls. `nvidia-smi` and `docker run --gpus all … nvidia-smi`
must both succeed inside the LXC before you register the daemon.

## Persistent storage

AppAPI's docker deploy creates a named volume (`nc_app_gocassini_data`),
mounts it in the container at `/nc_app_gocassini_data`, and exposes that
path as `APP_PERSISTENT_STORAGE`. The operator stores all durable data
under it:

```
$APP_PERSISTENT_STORAGE/operator/jobs.sqlite3    # SQLite job DB
$APP_PERSISTENT_STORAGE/operator/app-state.json  # AppAPI lifecycle state
$APP_PERSISTENT_STORAGE/operator/jobs            # per-attempt artifacts (raw recordings)
$APP_PERSISTENT_STORAGE/site/published           # published meeting site (read by the viewer)
```

No manual volume mounts are required — job history, recordings, and the
published site survive app updates and container recreates.

Setting `CASSINI_OPERATOR_DB_PATH`, `CASSINI_OPERATOR_WORK_ROOT`, or
`CASSINI_OPERATOR_SITE_ROOT` to a non-default path overrides the
corresponding location (mount your own volume there). The container logs a
warning at startup when an effective data path sits on an ephemeral
filesystem (overlay or tmpfs).

Outside AppAPI (plain `docker run` without `APP_PERSISTENT_STORAGE`) the
image defaults apply: `/var/lib/cassini-operator` for the DB + work root and
`/srv/cassini-site/published` for the site — mount volumes there yourself.

**Two copies, and what to back up.** Each recording exists in two places: the
raw `.mkv` that Cassini uploads into the **room owner's `Talk/Recordings`
folder in Nextcloud Files** (quota-counted and part of your normal Files
backups — see [Step 5](#step-5--talk-handoff-reversible)), and the
operator's own copy plus the derived artifacts (transcript, summary, viewer
site) under `nc_app_gocassini_data`. The transcript, summary, and site live
**only** in that volume — they are not in Nextcloud's Files backups — so back
up `nc_app_gocassini_data` separately if you want to keep them. Deleting one
copy does not affect the other.

**Retention.** There is no automatic retention or pruning yet: recordings and
job history accumulate until removed manually (delete the Files `.mkv`, or
`occ app_api:app:unregister --rm-data` to wipe the whole volume). Plan disk
accordingly. Retention controls are tracked in
[`planning/storage-and-access-shaping.md`](../planning/storage-and-access-shaping.md).

## Access policy

The manifest declares per-route access levels enforced by Nextcloud's proxy:

| Route | Access | What it is |
|---|---|---|
| `/control-panel/*` | ADMIN | Svelte admin UI for starting and monitoring jobs |
| `/operator/jobs`, `/operator/jobs/...`, `/operator/events` | ADMIN | Operator JSON + SSE API |
| `/operator/status` | ADMIN | Doctor/status endpoint (version, device usability, Talk config, DB/storage health) |
| `/viewer/*` | USER | Viewer SPA |
| `/published/*` | USER | Published meeting bundles — **owner-scoped** (each user sees only recordings they own) |
| `/ui/viewer.js` | USER | Bootstrap script behind the **Cassini** navigation entry |
| `/ui/control-panel.js` | ADMIN | Bootstrap script behind the **Cassini Admin** navigation entry |
| `/img/app.svg` | USER | Navigation icon |
| `/api/v1/welcome`, `/api/v1/room/*` | PUBLIC | Talk recording-backend protocol (HMAC-authenticated by Talk itself) |

`USER` means the AppAPI proxy admits any logged-in Nextcloud user — but the
operator then **scopes the published archive to the caller**. The viewer's
`catalog.json` lists only the meetings that user owns (the recording's Talk
`owner`, persisted on the job), and another owner's per-meeting assets return
`404`. A logged-in user who has recorded nothing sees an empty archive;
recordings created outside Talk's record button (no owner) are shown to nobody
through the archive. This reverses the original org-wide model (D10): recordings
are owner-private.

Sharing a recording with other people currently goes through Nextcloud Files on
the raw `.mkv` (see [Talk handoff](#step-5--talk-handoff-reversible) and
[Persistent storage](#persistent-storage)) — richer in-Nextcloud delivery of the
transcript/summary (deliver-to-Files + share-into-conversation) is tracked as
follow-up; see [`planning/storage-and-access-shaping.md`](../planning/storage-and-access-shaping.md).

`PUT /enabled` and `POST /init` are AppAPI **lifecycle callbacks**, not
proxied browser routes; they do not appear in `<routes>`.

## Uninstall

Restore Talk's previous recording backend first (see the rollback command in
Step 5), then:

```bash
occ app_api:app:unregister gocassini            # keeps the data volume
occ app_api:app:unregister gocassini --rm-data  # also deletes recordings + job history
```

## Standalone operator (dev/staging only)

`deployment/compose.yml` brings up the operator, control panel, and viewer as
plain Compose services. That bundle is for **development, staging, and
diagnostics** — it can satisfy Talk's recording-backend API, but it does not
register an ExApp, does not expose anything through the AppAPI proxy, adds
nothing to the Nextcloud UI, and bypasses the AppAPI auth middleware (no
`APP_SECRET`). Do not document or deploy it as the production app install.
See [`deployment/README.md`](../deployment/README.md).

## Testing the image

See [`docs/exapp-test-locally.md`](./exapp-test-locally.md) for three tiers:
image-only checks (no Nextcloud), manual install against a local Nextcloud,
and the production-shaped HaRP-fronted install
([`harness/bin/manual-test-setup.sh`](../harness/bin/manual-test-setup.sh)).

## CI

`.github/workflows/publish-exapp-image.yml` validates the manifest (including
that `<image-tag>` equals `<version>` and, on release tags, that the git tag
matches the manifest), builds the CPU and CUDA images, runs the smoke and
e2e suites, and pushes to `ghcr.io/codemyriad/gocassini`: `sha-<shortsha>`
[+`-cuda`] on every push, `latest`-family tags on `main`, and the immutable
`X.Y.Z`-family release tags on `vX.Y.Z` tag pushes.
