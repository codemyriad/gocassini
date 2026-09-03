# Installing Cassini as a Nextcloud ExApp

This is the **production install guide**. Cassini ships as a Nextcloud AppAPI
external app: one container exposes the admin operator surface, the recording
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
- **Required.** The native **Group folders / Team folders** (`groupfolders`)
  and **Everyone Group** (`group_everyone`) apps, installed and enabled *before*
  Cassini is enabled. Recordings are always access-controlled, and Cassini
  provisions the Team folder, the `cassini` service account and every ACL itself
  — but an ExApp reaches Nextcloud only over HTTP and cannot install a PHP app.
  Without them recordings are served to nobody and `/operator/status` reports the
  reason. The Everyone Group is instance-wide and may appear in other Nextcloud
  sharing pickers; see
  [Recording permissions](./exapp-nextcloud-recordings-permissions.md).
- An administrator account Cassini can act as, for one-time setup only. See
  [Administrator discovery](#administrator-discovery) — in almost all cases this
  needs no configuration.
- A Docker engine for the ExApp container. For GPU transcription it needs the
  NVIDIA driver + Container Toolkit (see [GPU transcription](#gpu-transcription-cuda)).
- For private, group, and one-to-one Talk recording: standalone Nextcloud Talk
  signaling / HPB configured with an internal client secret (`[clients]`
  `internalsecret`). Cassini's default Talk recorder path uses this
  HPB-internal mode.
- Only for experimental participant source-audio capture: the matching
  `cassini_capture` native companion app. Ordinary Cassini recording and
  transcription do not need it.

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

### Step 1b — Route `/exapps/*` to HaRP at your reverse proxy

**Required for every HaRP daemon**, local or remote. AppAPI does not talk to a
HaRP-hosted ExApp over an internal address — it builds a **public** URL and
dials it:

```
GET https://cloud.example.com/exapps/<appid>/heartbeat
```

Your TLS terminator must send `/exapps/*` to HaRP's `8780`, *not* to Nextcloud.
Without this route the request reaches Nextcloud, which 502s, and
install/enable never completes.

Caddy:

```caddyfile
handle /exapps/* {
    reverse_proxy 127.0.0.1:8780        # HaRP
}

handle {
    reverse_proxy 127.0.0.1:11000 {     # Nextcloud
        header_up Host {host}
    }
}
```

`handle`, **not** `handle_path`: `handle_path` strips the matched prefix, and
HaRP routes on `/exapps/<appid>/…`. nginx equivalent: `location /exapps/ { … }`
with a `proxy_pass` that has **no** trailing path element, so the prefix
survives.

The failure is indirect and easy to misdiagnose: the app reports `[enabled]` in
`occ app_api:app:list` but its **navigation icon never appears**, because
Cassini registers its nav entries only after receiving
`PUT /enabled?enabled=1` — a callback that never arrives.

**Do not test this with an unauthenticated `curl https://…/exapps/…`.** It
returns 502 whether the route is right or wrong. Verify with `occ` plus
`nextcloud.log`, and by looking for `PUT /enabled` in the ExApp container log.

## Step 2 — Pick an image tag

CI publishes to `ghcr.io/codemyriad/gocassini`:

| Tag | What it is |
|---|---|
| `X.Y.Z` | Portable capture image. Immutable by convention; matches `<version>`/`<image-tag>` in `appinfo/info.xml`. It records without a GPU, while operator-managed transcription immediately enters `build/blocked` instead of using CPU ASR. |
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
back to the plain tag. Cassini detects that fallback: the plain image remains
available for capture, `/operator/status` reports CUDA unavailable, and build
jobs immediately enter `build/blocked` with instructions to install the matching
`-cuda` image instead of decoding on CPU.

The checked-in manifest already pins the current release; to install a
different build, download `appinfo/info.xml`, set `<image-tag>` to the
desired `sha-…` or release tag, and register from that local copy.

## Step 3 — Register the app

The Talk recording secret is **optional** since D-447: if you omit
`CASSINI_TALK_RECORDING_SECRET`, the operator generates one on first start and
persists it on the AppAPI volume, and Step 5 reads it back from the operator's
provisioning endpoint — so no human ever has to invent or copy it. Supply one
explicitly only when you want to manage it out of band (e.g. a shared secret
manager); an explicit value always wins over the generated one:

```bash
# Optional — omit to let Cassini self-generate. Never reuse a repo/example value.
CASSINI_SECRET="$(openssl rand -hex 32)"
```

#### Finding the signaling internal secret

`CASSINI_TALK_SIGNALING_INTERNAL_SECRET` must equal your Talk signaling / HPB
server's `[clients] internalsecret`. It is the **one** value Cassini cannot
self-provision (unlike the recording secret): the invisible HPB-internal recorder
authenticates to the signaling server with this shared secret, and there is no
API that exposes it, so you supply it once. Where to find it:

- **Nextcloud All-in-One:** `docker exec nextcloud-aio-talk printenv INTERNAL_SECRET`
- **Standalone HPB:** the `[clients] internalsecret` in the signaling server's
  config (e.g. `server.conf`). If you are setting up signaling at the same time,
  generate the value once and put the same value in both places.

```bash
SIGNALING_INTERNAL_SECRET="<value from the command / config above>"
```

These are two different secrets: `CASSINI_SECRET` authenticates Talk's
recording-backend HTTP protocol; `SIGNALING_INTERNAL_SECRET` authenticates
Cassini as an internal signaling client for HPB-internal call capture.

> **Knowing whether it's set:** if it is missing, the operator logs
> `WARNING: talk_signaling_internal_secret_set -> false: …` at startup, and
> `GET /operator/status` returns `"signaling_internal_secret_configured": false`
> with a `signaling_internal_secret_hint` telling you how to fix it. Recording
> stays disabled until the secret is set.

Register from a pinned manifest (`--info-xml` accepts a local path or a raw
URL; pin a tag or commit SHA, not a moving branch):

```bash
curl -fsSL "https://raw.githubusercontent.com/codemyriad/gocassini/<tag-or-sha>/appinfo/info.xml" \
    -o /tmp/gocassini-info.xml
# Optional: override <image-tag> in the copy to a specific sha-… build (Step 2)

occ app_api:app:register gocassini <daemon-name> \
    --info-xml /tmp/gocassini-info.xml \
    --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET}" \
    --wait-finish
    # Optionally add: --env CASSINI_TALK_RECORDING_SECRET="${CASSINI_SECRET}"
    #   Omit it to let the operator self-generate + persist the recording
    #   secret (D-447); Step 5 reads it back from the provisioning endpoint.
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
| `CASSINI_TALK_RECORDING_SECRET` | No (auto-generated) | Shared secret for Talk's recording backend protocol; must match the `secret` in `spreed`'s `recording_servers` (Step 5). **Since D-447, if omitted the operator generates and persists one** — read it back from the provisioning endpoint (Step 5). An explicit value wins and is treated as externally managed |
| `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | For HPB-internal/default Talk recording | Internal client secret for standalone Talk signaling / HPB; must match `[clients] internalsecret`. Required for private, group, and one-to-one Talk recording |
| `CASSINI_TALK_BACKEND_URL` | No | Override for operator→Talk callbacks (started/stopped/failed notifications) and OCS calls. Leave empty to use the backend URL Talk sends with each request |
| `CASSINI_NC_ADMIN_USER` | On instances where no discovered account is an administrator | Administrator account used only to create the `cassini` service account, its narrow owner group, and the Team-folder topology. Leave empty for automatic discovery (see [Administrator discovery](#administrator-discovery)); set it when discovery cannot find one or picks the wrong account. Recordings are still owned, written, and managed by `cassini` |
| `CASSINI_PUBLISH_SINK` | No | Where published recordings are stored. `nextcloud-files` (the default for an installed app) puts them in the `Cassini` Team folder with per-participant ACLs; `local` keeps them on the app's own volume with no access control and no Nextcloud prerequisites. Set `local` only deliberately |
| `CASSINI_STT_BACKEND` | No | Which registered speech-to-text engine transcription uses; empty selects the default (`sherpa-onnx`). An unknown value fails the build loudly before any audio is decoded |
| `CASSINI_DISALLOW_MODEL_DOWNLOAD` | No | Set `1` on a host with no outbound network access. Each image bundles the model of the quality tier it runs by default, and any other tier downloads once into the model cache on the persistent volume. With this set, a build whose tier needs that download is blocked with a message that names the missing model and asks for a tier the image bundles, instead of starting and failing at the network |
| `CASSINI_ATTRIBUTION_DISABLED` | No | Set `1` to skip the cross-track speaker-attribution stage. By default every word is annotated with acoustic evidence; no words are changed or removed either way |
| `CASSINI_ATTRIBUTION_DROP` | No | Set `1` to delete words the acoustic evidence contradicts instead of annotating them (room-system microphones). The manifest records how many words were removed |
| `CASSINI_ARTIFACT_RETENTION` | No | How much of each recording's per-run working files the app keeps on its own volume. `sealed` (the default) reclaims a completed run's working copies — all duplicated in the canonical library or transient staging — and keeps the sealed meeting file and every log; `superseded` reclaims only runs a rerun replaced; `all` keeps everything, as the escape hatch when something must be recovered from a completed run. Nothing removes the last copy of anything, and published recordings are never touched |
| `CASSINI_SOURCE_CAPTURE` | No | Set `1` to let participants' browsers record their own microphone while Talk's official recording is active and upload when it stops. **Off by default, and experimental.** Joining a call alone does not collect audio, but once Talk confirms a recording every authenticated participant of that call is captured: Cassini has no per-participant control and records no answer from participants. Telling the room is Talk's job — its recording indicator, and its own `recording_consent` setting if participants must be asked. With this off the browser assets are not served and uploads are refused, so nothing is collected and no storage is used. It also needs the matching `cassini_capture` companion app. Uploads are bounded by `CASSINI_CAPTURE_OWNER_QUOTA_MB`, `CASSINI_CAPTURE_TOTAL_QUOTA_MB` and `CASSINI_CAPTURE_MIN_FREE_DISK_MB`, and swept after `CASSINI_CAPTURE_MAX_AGE_HOURS`. See [docs/source-audio-capture.md](source-audio-capture.md) |
| `CASSINI_SOURCE_AUDIO_INGEST` | No | Set `1` to build transcripts from audio participants captured in their own browser, where they uploaded it, instead of from what reached the recorder over the network. **Off by default, and experimental.** Capture and upload are collected either way; this switch controls only whether that audio replaces the recorded track for transcription. Placing an upload depends on the participant's clock agreeing with the server's, so enable it only where clients are time-synchronised. See [docs/source-audio-capture.md](source-audio-capture.md) |
| `CASSINI_OPERATOR_CAPTURE_ROOT` | No | Where participant-uploaded source audio is stored before a build uses it. Leave empty for the default under the app's persistent volume. Uploads land here whether or not `CASSINI_SOURCE_AUDIO_INGEST` is set. They are bounded by the capture quotas and removed by the sweep once they pass `CASSINI_CAPTURE_MAX_AGE_HOURS` |
| `CASSINI_ROOM_ID_PEPPER` | No (recommended) | Deployment-wide secret mixed into the one-way derivation of each meeting's room id. A meeting publishes a derived id rather than its Talk conversation token, because for a public conversation that token is also the link that joins it — and a Talk token is short enough that an unpeppered derivation can be reversed by enumeration offline. With a pepper set it cannot. **Choose it once:** changing it changes every room id, while meetings already published keep the ids they were written with, so a room splits in two. Re-running `scripts/backfill-catalog-rooms.sh --apply` repairs every meeting this installation has a job row for; only recordings imported from elsewhere need the manual merge in `scripts/reattribute-catalog-room.sh` |
| `OPENROUTER_API_KEY` | No | API key for LLM transcript cleanup + meeting summaries. **When set, the full local transcript is sent to that third-party endpoint** for cleanup/summarisation (transcription itself is always local). Unset, raw transcripts are published without summaries |
| `LLM_BASE_URL` | No | OpenAI-compatible API base URL; defaults to `https://openrouter.ai/api/v1` when `OPENROUTER_API_KEY` is set |
| `LLM_MODEL` | No | Model for cleanup/summaries (default `openai/gpt-4o-mini`) |
| `CASSINI_OPERATOR_API_TOKEN` | No | Bearer token for direct non-AppAPI operator API calls. AppAPI-proxied requests are authenticated by Nextcloud/AppAPI |

### Installing the source-capture companion (experimental)

The ExApp cannot place JavaScript on a Talk page. If you enable
`CASSINI_SOURCE_CAPTURE`, install the same-version `cassini_capture.tar.gz`
CI artifact (or tagged GitHub release asset) as a second native app:

```bash
tar -xzf cassini_capture.tar.gz -C /path/to/nextcloud/apps
occ app:enable cassini_capture
```

For a local build:

```bash
npm ci
npm run build:capture -w cassini-app
./scripts/build-capture-companion.sh --skip-js-build
```

The companion uses Nextcloud's public additional-scripts event and loads only
on authenticated Talk call routes. It carries no audio/storage logic. On each
AppAPI lifecycle edge the ExApp mirrors `CASSINI_SOURCE_CAPTURE` into AppAPI's
ExApp config store, which the companion reads into initial state; after first installing the companion,
disable/re-enable or redeploy `gocassini` once if it was already running.

To turn the feature off, first redeploy with `CASSINI_SOURCE_CAPTURE` unset and
wait at least 30 seconds for open calls to observe the fail-closed poll, then
disable `cassini_capture`. Anonymous Talk guests and mobile clients are not
captured.

### Updating deploy options after install

AppAPI deploy env is container-creation-time configuration, not live Nextcloud
app config. Changing Talk's `spreed.recording_servers.secret` does **not**
update `CASSINI_TALK_RECORDING_SECRET` in an already deployed ExApp container.
Likewise, changing the signaling server `internalsecret` does not update
`CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.

For secret rotation or for an existing pre-D-395 install, recreate/redeploy the
ExApp with all required `--env` values while preserving the AppAPI persistent
storage volume. Local development can use AppAPI's `--test-deploy-mode` for
repeat installs; production should follow your AppAPI backup/redeploy policy.
`app_api:app:update` reuses stored deploy options and has no `--env` flag.
`app_api:app:config:set` writes a separate app-config store and is not the
container environment Cassini reads today.

Because deploy env is creation-time only, **a release that adds a new
*required* environment variable cannot be delivered by the admin UI's Update
button** — it is a breaking change needing a redeploy. See
[`exapp-update-constraints.md`](./exapp-update-constraints.md) for the full set
of rules on what Install/Update can and cannot deliver.

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

### Administrator discovery

Cassini performs one-time setup — creating the `cassini` service account, its
narrow owner group, and the `Cassini` Team folder — as a Nextcloud
administrator. It never stores or reads recordings as one; that is the service
account's job, deliberately kept without instance-admin rights.

An external app cannot be *told* who the administrator is, and every Nextcloud
API that would reveal one is itself admin-gated, so discovery is a **probe**
rather than a lookup:

1. `CASSINI_NC_ADMIN_USER`, if you set it.
2. `admin`, the conventional id.
3. Every account on the instance, enumerated through AppAPI (which answers
   without needing an identity), up to a bounded number.

Each candidate is asked whether it is an administrator; the first that says yes
is used and named in `/status` as `admin_user`. If none is — an instance with a
large user base whose administrator sorts past the probe limit, or one where the
app may not act as any administrator — provisioning **stops and says so** rather
than continuing as an account that may not exist. Set `CASSINI_NC_ADMIN_USER` to
the account and re-enable Cassini.

### Verifying the recordings substrate

Recordings are only visible to the right people if the Team folder, its ACL
floor and the universal `everyone` mount actually exist. Provisioning happens
automatically when the app is enabled, and reports what it found:

```bash
curl -sS -u admin:<pass> \
  "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status" \
  | jq .recordings_access
```

A healthy install:

```json
{
  "applicable": true,
  "publish_sink": "nextcloud-files",
  "state": "provisioned",
  "ok": true,
  "admin_user": "admin",
  "prerequisites": [
    { "name": "groupfolders", "state": "enabled" },
    { "name": "group_everyone", "state": "enabled" }
  ],
  "checked_at": "2026-08-04T09:14:02Z"
}
```

`state` is the thing to read. Anything other than `provisioned` or
`not_applicable` also makes `/operator/status` answer **503**, so a broken
substrate is visible rather than a silently empty archive.

| `state` | Means | What to do |
|---|---|---|
| `provisioned` | Setup completed. | Nothing. |
| `unavailable` | A **named** thing is missing. `step` says which: `app_missing:<id>` or `administrator`. | Install the app, or set `CASSINI_NC_ADMIN_USER`; then re-enable Cassini. |
| `degraded` | A setup **call failed**. `step` names it (`acl_enable`, `mount_mapping:everyone`, `root_acl`, …). | Read the matching `nc provision:` line in the container log, fix the fault, re-enable Cassini. |
| `unknown` | Setup has not run in this process — the container was restarted without the app being re-enabled. | Disable and re-enable Cassini. **Publishing is refused until then**: nothing has verified where recordings would land. |
| `not_applicable` | This deployment does not serve recordings from Nextcloud Files (standalone operator, or `CASSINI_PUBLISH_SINK=local`). | Nothing. |

```json
{ "state": "unavailable", "step": "app_missing:group_everyone",
  "detail": "app_missing:group_everyone: the \"group_everyone\" app is not enabled; an ExApp cannot install it" }

{ "state": "unavailable", "step": "administrator",
  "detail": "administrator: no Nextcloud administrator could be resolved (probed 3: [admin alice bob]); set CASSINI_NC_ADMIN_USER to an account in the \"admin\" group" }
```

Until the substrate is `provisioned`, publishing **fails** rather than writing
recordings into the `cassini` account's private home where nobody can reach
them. The `occ`-side verification of the resulting topology is in
[Recording permissions](./exapp-nextcloud-recordings-permissions.md).

### What people see when setup is not finished

You do not have to read `/operator/status` to find out — opening **Cassini**
from the Nextcloud app menu says it, and says something different depending on
who is looking. The split is decided by the same boundary that decides whether
you get the **Operator** tab at all: the operator API is ADMIN, so being able to
read it is what makes you an administrator here.

| Who | What Cassini shows |
|---|---|
| An administrator | The step that stopped, in words and in commands. `app_missing:<id>`: the app by name and id, the `occ app:install` line for it, and the `occ app_api:app:disable`/`:enable` pair that re-runs setup. `administrator`: `CASSINI_NC_ADMIN_USER`. `degraded`: the `nc provision:` log lines, rather than an app to install — nothing is missing that you could install. `unknown`: that the container has not re-run setup since it restarted. In every case, the operator's own `detail` sentence — the same one in the container log. |
| Everyone else | That Cassini is not set up, that it is not their account, and a link to this Cassini page to hand to an administrator — who, opening it, gets the row above. Nothing names an app, a step or a command. |

The verdict behind this comes from `GET /operator/setup`, which is USER-level
and carries `ok` and `state` only:

```bash
curl -sS -u alice:<pass> \
  "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/setup"
# {"ok":false,"state":"unavailable"}
```

It answers **200 in every state**, including the broken ones: the caller is a
browser deciding what to render, and it has to be able to tell "Cassini is not
set up" from "the ExApp is down", which is what a 503 in front of it looks like.
`/operator/status` is the endpoint that answers 503 — use that one for
monitoring.

Whether it replaces the meeting list or sits above it depends on whether the
archive can still be **read**, which is a different question from whether setup
completed — the read path fetches every recording as the individual caller and
never consults the setup record.

| `state` | Where the message goes | Why |
|---|---|---|
| `unavailable`, `degraded` | Replaces the meeting list | Nothing is mounted, or the mount root has been narrowed to owner-only, so the list underneath would be an error or an empty lie |
| `unknown` | A strip above the meeting list, which stays | The container merely restarted. Every published recording still opens; only publishing is refused. Blanking the archive on every reboot would be worse than the problem |

An administrator keeps the **Operator** tab throughout.

> This route reaches AppAPI at **registration** time. An app installed before
> this version has to be re-registered (an app update does this) before
> `/operator/setup` exists; until then the panel does not appear and the viewer
> behaves as it did before.

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

6. The **Cassini** navigation entry renders for any logged-in user (the viewer
   / meeting archive). Admin users additionally get the operator surface
   (recording control + job history) inside the same entry. This is the
   supported entry point — it runs on AppAPI's nonce'd embedded page under
   Nextcloud's normal CSP, no AppAPI patch required.
7. The doctor/status endpoint reports `"ok": true` (ADMIN route — use an
   admin login with an app password):

   ```bash
   curl -fsS -u admin:<app-password> \
     https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status
   ```

   It reports the app version, the STT device (`cpu`/`cuda`) and whether that
   device is actually usable, whether the Talk recording secret and signaling
   internal secret are configured (never the values), the optional backend URL
   override presence, and DB/storage health — the same answers that used to
   require shell access into the container.

   Relevant Talk fields should look like:

   ```json
   {
     "talk": {
       "secret_configured": true,
       "signaling_internal_secret_configured": true,
       "backend_url_override_configured": false,
       "secret_source": "generated",
       "recording_backend_url": "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini"
     }
   }
   ```

   `secret_source` is `generated` when the operator self-generated the recording
   secret (D-447) or `env` when you supplied one; `recording_backend_url` is the
   value to register in Step 5 (never the secret itself — that comes from the
   provisioning endpoint below).
9. CUDA installs only: the image tag ends in `-cuda` and the container can see
   the GPU — `docker exec nc_app_gocassini nvidia-smi`. The status endpoint in
   the previous step must show `"device": "cuda"` with `"device_usable": true`;
   a CUDA container without GPU access also logs
   `ERROR: stt_device cuda is not usable` at startup instead of silently
   falling back to CPU.

### URL reachability preflight

Talk sends Cassini a `Talk-Recording-Backend` URL and Cassini uses it for
recording started/stopped callbacks and OCS signaling-settings requests, unless
`CASSINI_TALK_BACKEND_URL` overrides it. Cassini never uploads a recording to
Talk — the meeting is published as `.opus` into Nextcloud Files.

Before handoff, verify these URLs are coherent:

```bash
# Browser/Talk-facing base URL Nextcloud uses in generated absolute URLs.
occ config:system:get overwrite.cli.url

# AppAPI proxy base Talk will call.
curl -fsS https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/api/v1/welcome

# From the ExApp container, confirm Nextcloud is reachable. This should print
# an HTTP status, not a DNS/connectivity failure.
docker exec nc_app_gocassini sh -lc 'curl -k -s -o /dev/null -w "%{http_code}\n" "$NEXTCLOUD_URL/status.php"'
```

Set `CASSINI_TALK_BACKEND_URL=https://cloud.example.com` only when the URL Talk
advertises cannot be reached from the ExApp container.

## Step 5 — Talk handoff (reversible)

Point Talk's recording backend at the AppAPI proxy base. The `api/v1/welcome`
and `api/v1/room/*` routes are declared PUBLIC in the manifest, so Talk's
recording protocol (authenticated by its own HMAC, not a Nextcloud session)
passes through the proxy.

Talk has no API for an app to register itself as the recording backend, so this
one admin step stays manual — but since D-447 it is **secret-free**: the
operator's ADMIN-only provisioning endpoint returns the ready-to-apply
`recording_servers` value (including the self-generated secret), so you never
copy a secret by hand.

**Back up the current backend first**, then switch:

```bash
# 0. Back up (empty output = no recording backend configured)
occ config:app:get spreed recording_servers | tee /root/recording_servers.backup

# 1. Pull the ready-to-apply recording_servers value from Cassini (ADMIN route,
#    use an admin login with an app password) and register it in one step.
RS="$(curl -fsS -u admin:<app-password> \
  https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/talk/provisioning \
  | jq -c '.recording_servers')"
occ config:app:set spreed recording_servers --value="$RS"
occ config:app:set spreed call_recording --value=yes
```

If you supplied `CASSINI_TALK_RECORDING_SECRET` yourself in Step 3, you can
instead set `recording_servers` directly with that same secret and the
`recording_backend_url` from the status endpoint.

**Controlled test** — use a non-critical private/group or one-to-one
conversation so the HPB-internal path is exercised:

1. Create or pick a private test conversation with at least one speaking
   participant.
2. Start recording from Talk's **Record** button.
3. Confirm a Cassini job appears in the **Cassini Admin** control panel.
4. Speak for a minute, stop the recording, leave the call, or let the
   empty-room timeout stop it.
5. Watch the job progress through record → build → seal → publish. Talk receives
   started/stopped status per its recording-backend protocol and nothing else;
   the meeting itself is published as a portable `.opus` into Nextcloud Files,
   where the transcript/summary appear in the Cassini viewer.
6. Run a second controlled recording and confirm both the first and second
   transcripts remain visible in the viewer/catalog.

**Rollback** — restore the saved value and Talk records through the previous
backend again; the Cassini ExApp can stay installed:

```bash
occ config:app:set spreed recording_servers --value="$(cat /root/recording_servers.backup)"
# or, if there was no recording backend before:
occ config:app:delete spreed recording_servers
```

Keep the previous backend running until your test recording passes.

### Secret rotation checklist

Rotate secrets as a coordinated operation; do not change only one side.

For the Talk recording secret:

1. Pause or avoid active recordings.
2. Update `spreed.recording_servers.secret`.
3. Recreate/redeploy the Cassini ExApp with the same value as
   `CASSINI_TALK_RECORDING_SECRET`.
4. Confirm `/operator/status` reports `secret_configured: true`.
5. Run a controlled recording.

For the signaling internal secret:

1. Update the standalone signaling / HPB `[clients] internalsecret` and restart
   signaling as required.
2. Recreate/redeploy the Cassini ExApp with the same value as
   `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
3. Confirm `/operator/status` reports
   `signaling_internal_secret_configured: true`.
4. Run a private/group/one-to-one controlled recording.

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

There is no CPU transcription fallback. A plain portable image is a permanent
inference mismatch: recording finishes, but the build immediately enters
`build/blocked` with no `build_retry_not_before`; `/operator/status` answers 503
with an actionable `stt.detail`. Install/redeploy the matching `-cuda` image,
then use **Rerun** in Cassini Admin to process the preserved recording.

On a CUDA-capable image, temporary RAM or VRAM pressure is different. The
operator keeps the build queued, records `build_retry_not_before`, and retries
with exponential backoff (starting at 15 seconds and capped at 15 minutes).
After sixteen unsuccessful deferrals (about 2¾ hours at the default schedule)
it moves the job to `build/blocked` instead
of retrying forever. Restore capacity and use **Rerun** to create a fresh
attempt.

### Remote GPU node

If the Nextcloud host has no GPU, HaRP can drive a **remote Docker engine**
over its FRP tunnel (see "Remote Docker Engines" in the
[HaRP README](https://github.com/nextcloud/HaRP)):

1. Install Docker + NVIDIA Container Toolkit on the GPU node.
2. Copy the client certificates from the HaRP container's `/certs/frp` and
   run `frpc` on the GPU node to tunnel its Docker socket back to HaRP
   (one remote port per engine, 24001–24099).
3. Make sure `/exapps/*` reaches HaRP at your reverse proxy
   ([Step 1b](#step-1b--route-exapps-to-harp-at-your-reverse-proxy)). This is
   the step that actually blocks the install, and its symptom — an app that
   reports `[enabled]` with no navigation icon — points nowhere near the
   reverse proxy.
4. Register a second deploy daemon for that engine with Compute device =
   CUDA, and register (or re-register) `gocassini` against it.

You do not re-register the app to switch between CPU and GPU images: the
compute device is a property of the **daemon**, and AppAPI derives the image
variant from it. See
[`exapp-update-constraints.md`](./exapp-update-constraints.md) for what that
implies for the Install/Update buttons.

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
$APP_PERSISTENT_STORAGE/site/published           # legacy published site (see below)
```

No manual volume mounts are required — job history and recordings survive app
updates and container recreates.

An installed app publishes into Nextcloud Files, so `site/published` is not
written and not served: it holds only what an older, pre-Nextcloud-Files version
left behind. See [Updating from a pre-Nextcloud-Files
version](#updating-from-a-pre-nextcloud-files-version).

Setting `CASSINI_OPERATOR_DB_PATH`, `CASSINI_OPERATOR_WORK_ROOT`, or
`CASSINI_OPERATOR_SITE_ROOT` to a non-default path overrides the
corresponding location (mount your own volume there). The container logs a
warning at startup when an effective data path sits on an ephemeral
filesystem (overlay or tmpfs).

Outside AppAPI (plain `docker run` without `APP_PERSISTENT_STORAGE`) the
image defaults apply: `/var/lib/cassini-operator` for the DB + work root and
`/srv/cassini-site/published` for the site — mount volumes there yourself.

## Updating from a pre-Nextcloud-Files version

Recordings published by a current install go straight into Nextcloud Files, and
the delivery is part of the publish — if it fails, the publish fails. Nothing
below applies to them.

Recordings published by an **older** version live only on the app's own volume,
under `$APP_PERSISTENT_STORAGE/site/published`. Nothing republishes them, so
after updating they are not in Nextcloud Files and the viewer does not list
them. Migrating them is one manual step:

```bash
./scripts/backfill-nc-files.sh --dry-run   # report what would move, change nothing
./scripts/backfill-nc-files.sh             # migrate
```

Run it on the host where the app container runs, **after** enabling the updated
app — enabling is what provisions the `Cassini` Team folder the migration writes
into. Pass `--container NAME` if your app id is not `gocassini`.

Do not record meetings while it runs. It checks that Nextcloud Files is empty
once, at the start, and writes the recording index at the end; a meeting
published in between would be dropped from that index. On a quiet instance this
is a non-issue, and a recording published during the run can be republished
afterwards.

Two things worth knowing before you run it:

- **Running it when unsure is safe.** It stops without changing anything both
  when Nextcloud Files already holds recordings and when there is no older
  archive to migrate — the ordinary state of an install created after the
  switch. Either way it exits 3 and says so. It is a one-shot migration, not a
  repair tool: it will not reconcile a partially populated archive.
- **Migrated recordings are private by default** — readable only by the
  `cassini` service account, because the audience a recording had at publish
  time cannot be recovered after the fact. Grant access from the Files UI, or
  pass `--public` to make them readable by every signed-in account, which
  matches how a pre-access-control archive behaved.

Skipping the migration entirely is a legitimate choice: the old recordings stay
on the volume, and everything published from now on works normally.

The exit code tells you whether anything was written, which is what decides
whether re-running is safe:

| Exit | Meaning | What to do |
|---|---|---|
| 0 | Migrated | Check `Cassini/Recordings/` in Files, then grant access |
| 3 | Nothing to migrate — already in Files, or no older archive | Nothing. This is the normal answer on a current install |
| 4 | Stopped before writing anything | Fix the reported error and run it again. Nothing to clean up |
| 1 | Stopped part-way, after writing began | Do **not** just re-run: the guard will now refuse. Fix the error, remove the recordings that run uploaded from `Cassini/Recordings/` in the Files app, then run it again |

## Access policy

The manifest declares per-route access levels enforced by Nextcloud's proxy:

| Route | Access | What it is |
|---|---|---|
| `/operator/jobs`, `/operator/jobs/...`, `/operator/events` | ADMIN | Operator JSON + SSE API |
| `/operator/settings` | ADMIN | STT-quality settings (read + update) |
| `/operator/status` | ADMIN | Doctor/status endpoint (version, device usability, Talk config, DB/storage health) |
| `/operator/setup` | USER | Whether recordings can be served at all — `{"ok":…,"state":…}` and nothing else |
| `/viewer/*` | USER | Viewer SPA |
| `/published/*` | USER | Published meeting bundles (catalog + recordings) |
| `/ui/viewer.js`, `/ui/viewer.css` | USER | Bootstrap script + stylesheet behind the **Cassini** navigation entry |
| `/img/app.svg` | USER | Navigation icon |
| `/api/v1/welcome`, `/api/v1/room/*` | PUBLIC | Talk recording-backend protocol (HMAC-authenticated by Talk itself) |

USER means the proxy route requires a logged-in Nextcloud user. Being logged in
is necessary but not sufficient: the operator serves the catalog and each
recording **as the individual caller**, so Nextcloud Files enforces that
meeting's advanced ACL — a non-participant sees no catalog entry and a direct
fetch 404s. See
[Managing recording permissions](./exapp-nextcloud-recordings-permissions.md).

`/operator/setup` is the one deliberate exception to "the operator API is
ADMIN", and it is USER for a reason: without it, the only thing a non-admin
could learn about an unfinished install was the viewer failing to load. It
carries the `recordings_access` **verdict** — `ok` and `state` — and nothing
else. No step, no administrator, no paths, no versions; the diagnosis stays on
`/operator/status`. See [What people see when setup is not
finished](#what-people-see-when-setup-is-not-finished).

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
image-only checks (no Nextcloud), installed-ExApp checks against a local
Nextcloud, and the production-shaped HaRP-fronted install via
`cassini dev stack up --cassini installed-exapp`.

## CI

`.github/workflows/publish-exapp-image.yml` validates the manifest (including
that `<image-tag>` equals `<version>` and, on release tags, that the git tag
matches the manifest), builds the CPU and CUDA images, runs the smoke and
e2e suites, and pushes to `ghcr.io/codemyriad/gocassini`: `sha-<shortsha>`
[+`-cuda`] on every push, `latest`-family tags on `main`, and the immutable
`X.Y.Z`-family release tags on `vX.Y.Z` tag pushes.
