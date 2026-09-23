# Installing Cassini as a Nextcloud ExApp

> Recording access changed to one built-in Nextcloud Files direct-share model.
> The older Team-folder and storage-switch sections in this guide are retained
> for historical installations; follow the [current recording access and
> cutover guide](direct-shares-cutover.md) for this version.

Start with [Before installing Cassini](before-installing.md) to check eligibility
and identify who can configure the required services.


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

After installation, **Cassini → Operator → Publish pipeline** diagnoses missing
configuration and
helps you verify a short recording. See [Recording readiness](recording-readiness.md)
for the guided flow, AIO-specific setup, and restart persistence.

## Prerequisites

- Nextcloud **32 or newer** (the manifest's `min-version`; Cassini targets and
  is tested against Nextcloud 33+) with the **AppAPI** app installed and
  enabled.
- A registered AppAPI **deploy daemon** (next section).
- **Required.** A `cassini` service account. Every recording is written and
  read as it, in either storage mode. Cassini tries to create it and its
  `cassini` group itself when the app is enabled, which works on releases that
  let an ExApp write to user administration. Nextcloud 34.0.2 and later refuse
  that request (they require password confirmation, which an ExApp has no
  session to give), and then Cassini creates it from your browser instead: open
  **Cassini** as an administrator and press **Create the account and start** in
  the dialog it shows once per install, and it will make the account as you,
  after Nextcloud's own password prompt. By hand instead:

  ```bash
  occ group:add cassini
  occ user:add --group=cassini cassini
  ```

- **Required:** Nextcloud Files sharing enabled. Cassini keeps the `.opus`
  files in the `cassini` account's private `CassiniRecordings/meetings` tree and
  creates direct shares for the local people, groups and Teams captured from
  each Talk room. No additional Nextcloud app is required. See
  [Recording permissions](./direct-shares-cutover.md).
- An administrator account Cassini can act as, to check how the instance is set
  up: which apps are enabled, whether the `cassini` account exists, whether
  there is a Team folder. That check is read-only, and the archive itself is
  never touched as an administrator — every recording is written, read and
  moved as `cassini`, including when the storage mode is switched. The writes
  the operator may attempt as the administrator are creating the `cassini`
  account and its group when the app is enabled, and installing the two native
  apps when you ask it to from the settings section. See
  [Administrator discovery](#administrator-discovery) — in almost all cases this
  needs no configuration.
- A Docker engine for the ExApp container. For GPU transcription it needs the
  NVIDIA driver + Container Toolkit (see [GPU transcription](#gpu-transcription-cuda)).
- For private, group, and one-to-one Talk recording: standalone Nextcloud Talk
  signaling / HPB configured with an internal client secret (`[clients]`
  `internalsecret`). Cassini's default Talk recorder path uses this
  HPB-internal mode.

Persistent storage is automatic: AppAPI creates a named volume for every
docker-deployed ExApp and the operator stores all durable data under it
(see [Persistent storage](#persistent-storage)).

## Step 1 — Register a deploy daemon (HaRP)

**Already have a working HaRP daemon for the intended Cassini host?** Reuse it.
With a recent successful Test deploy and unchanged configuration, continue to
[Step 2](#step-2--pick-an-image-tag). Otherwise run Test deploy first.
**First ExApp, no daemon, or a failed test?** Follow the
[first ExApp walkthrough](first-exapp.md), then return to Step 2 once the test
passes. The provisioning example below is for a new standalone HaRP service.

**AIO:** check the installed version's available components and registered
daemon. Start integrated HaRP where provided and run Test deploy. Older/custom
versions may need migration or separate provisioning. Reuse a working integrated
service rather than launching a second HaRP. Enable AIO's Talk component for HPB. See [AIO restart persistence](recording-readiness.md#aio-restart-persistence)
before handing recording over to Cassini.


Use a **HaRP** daemon for this installation guide, following
[upstream's recommended setup](https://docs.nextcloud.com/server/latest/admin_manual/exapps_management/AppAPIAndExternalApps.html#harp).
If you already use Docker Socket Proxy, follow the upstream migration guidance
for your version before choosing this HaRP installation path.

For a new standalone service, run HaRP next to a Docker engine (full options in the
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
**Register Daemon** using the HaRP template matching your deployment.
`occ app_api:daemon:list` should show the registered daemon. Complete the routing
in Step 1b, then run **Test deploy** from the daemon's three-dot menu. Continue
to Step 2 only after all test stages through Enabled succeed. If a stage fails,
use the [first ExApp troubleshooting steps](first-exapp.md#if-the-test-fails).

### Step 1b — Route `/exapps/*` to HaRP at your reverse proxy

**Required for every HaRP daemon**, local or remote. Current integrated AIO can
provide this route in its own frontend: first inspect the deployed topology and
[the routing guidance](recording-readiness.md#harp-routing-and-missing-navigation).
The explicit host rules below apply when that route is not already provided.
The HaRP ExApp URL must resolve through the configured route, for example:

```
GET https://cloud.example.com/exapps/<appid>/heartbeat
```

The proxy chain must deliver `/exapps/*` to the correct HaRP frontend, commonly
port `8780`, rather than to Nextcloud's PHP handler. Integrated AIO can provide
this hop. Missing or incorrect routing can produce 404/502 errors and prevent
installation or enablement; inspect the response and proxy logs before changing
configuration.

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
| `X.Y.Z` | Multi-arch portable image (`linux/amd64`, `linux/arm64`). Immutable by convention; matches `<version>`/`<image-tag>` in `appinfo/info.xml`. It records without a GPU, and runs local CPU transcription with explicitly installed Parakeet models on x86_64 and 64-bit ARM servers. |
| `X.Y.Z-cuda` | CUDA release build for x86_64 (CUDA 12 / cuDNN 9 sherpa-onnx, optional separately installed fp32 Parakeet, `CASSINI_STT_DEVICE=cuda`) |
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

When the deploy daemon's compute device is CUDA, AppAPI tries
`<image-tag>-cuda` first and can fall back to the plain image. Cassini's Auto
policy uses CUDA when its runtime and device are usable, and CPU otherwise.
The recording checks report the selected processing device. An explicit CUDA override on a
host without usable CUDA blocks processing until corrected.

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

You may save the internal secret in **Operator → Publish pipeline → Talk authentication** after
installation instead of supplying a deployment environment variable. The
environment variable, when supplied, takes precedence.

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
| `CASSINI_NC_ADMIN_USER` | On instances where no discovered account is an administrator | Administrator account used to CHECK how this instance is set up — which apps are enabled, whether the `cassini` account exists, whether there is a Team folder. That check creates nothing; the only writes made as this account are the attempt to create the `cassini` service account and its group when the app is enabled, and the app-install attempt when you pick meeting participants in the settings, and switching audiences moves nothing as it — the archive is copied by WebDAV as `cassini`, in both directions. A switch does re-run that same read-only check before it writes, so it still needs an administrator to be resolvable. Leave empty for automatic discovery (see [Administrator discovery](#administrator-discovery)); set it when discovery cannot find one or picks the wrong account. Recordings are still owned, written, and managed by `cassini` |
| `CASSINI_PUBLISH_SINK` | No | Where published recordings are stored. `nextcloud-files` (the default for an installed app) puts them in Nextcloud Files; `local` keeps them on the app's own volume. Set `local` only deliberately. Under `nextcloud-files`, *who can see* a recording is a separate setting, in **Operator › Settings › Who can see recordings**, not here |
| `CASSINI_STT_BACKEND` | No | Which registered speech-to-text engine transcription uses; empty selects the default (`sherpa-onnx`). An unknown value fails transcription while preserving the prepared audio |
| `CASSINI_DISALLOW_MODEL_DOWNLOAD` | No | Set `1` on a host with no outbound network access. Images contain no models. This forbids model downloads and resumed network jobs. Local `cassini models import`, runtime checks, activation, and already installed models remain available; missing models never block audio publication |
| `CASSINI_ATTRIBUTION_DISABLED` | No | Set `1` to skip the cross-track speaker-attribution stage. By default every word is annotated with acoustic evidence; no words are changed or removed either way |
| `CASSINI_ATTRIBUTION_DROP` | No | Set `1` to delete words the acoustic evidence contradicts instead of annotating them (room-system microphones). The manifest records how many words were removed |
| `CASSINI_ARTIFACT_RETENTION` | No | How much of each recording's per-run working files the app keeps on its own volume. `sealed` (the default) reclaims a completed run's working copies — all duplicated in the canonical library or transient staging — and keeps the sealed meeting file and every log; `superseded` reclaims only runs a rerun replaced; `all` keeps everything, as the escape hatch when something must be recovered from a completed run. Nothing removes the last copy of anything, and published recordings are never touched |
| `CASSINI_ROOM_ID_PEPPER` | No (recommended) | Deployment-wide secret mixed into the one-way derivation of each meeting's room id. A meeting publishes a derived id rather than its Talk conversation token, because for a public conversation that token is also the link that joins it — and a Talk token is short enough that an unpeppered derivation can be reversed by enumeration offline. With a pepper set it cannot. **Choose it once:** changing it changes every room id, while meetings already published keep the ids they were written with, so a room splits in two. Existing recordings retain their original room ids |
| `OPENROUTER_API_KEY` | No | Initial API key for the LLM endpoint, when it needs one (a self-hosted model server usually does not). Pre-fills the app's LLM settings on first start; afterwards keys are managed in the app |
| `LLM_BASE_URL` | No | Initial OpenAI-compatible API base URL for meeting summaries — a hosted provider or your own model server. Pre-fills the app's LLM settings on first start; afterwards endpoints are changed in the app (`PUT /settings/llm`), never by redeploying. **The full local transcript is sent to whatever endpoint is configured** (transcription itself is always local). Unset, the app starts with no LLM endpoint and publishes raw transcripts without summaries. Defaults to `https://openrouter.ai/api/v1` when `OPENROUTER_API_KEY` is set |
| `LLM_MODEL` | No | Initial model for summaries (default `openai/gpt-4o-mini`) |
| `CASSINI_OPERATOR_API_TOKEN` | No | Bearer token for direct non-AppAPI operator API calls. AppAPI-proxied requests are authenticated by Nextcloud/AppAPI |

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

Cassini looks at how this instance is set up — whether the `cassini` service
account and its narrow owner group exist, whether there is a `Cassini` Team
folder, which native apps are enabled — as a Nextcloud administrator. It never
stores, reads or relocates recordings as one; that is the service account's job,
deliberately kept without instance-admin rights.

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
app may not act as any administrator — the preflight **stops and says so**
rather than continuing as an account that may not exist. Set
`CASSINI_NC_ADMIN_USER` to the account and re-enable Cassini.

### Where recordings live

The installed app writes each new recording to
`cassini/CassiniRecordings/meetings/<job-id>.opus` in Nextcloud Files. This is a
private directory owned by the dedicated `cassini` account. Cassini creates
Nextcloud file shares for the Talk room's captured local users, groups and
Teams. For public rooms, those shares may allow resharing if the instance
permits it. The app creates no public link. No additional Nextcloud app is
required.

The viewer's list is assembled from the caller's current Nextcloud shares.
Nextcloud authorizes each audio and annotation read as that caller. The local
SQLite meeting metadata index helps build cards and never decides access.
There is no `catalog.json` in the Nextcloud archive. The static exporter still
makes one for static hosting.

The `/operator/status` response reports `recordings_access.state`, `step`,
`detail` and the private `root`. A failed setup check blocks new publication;
previous shares remain controlled by Nextcloud. Use **Operator › Publish
pipeline › Who can see recordings** to recheck setup or create the owner account
from an administrator's Nextcloud session.

Older archives are not migrated automatically. See
[Recording access and cutover](./direct-shares-cutover.md) for the manual steps.

## Step 4 — Verify the install (before touching Talk)

All of these must pass before the Talk handoff:

1. `occ app_api:daemon:list` shows the daemon and its **Test deploy** passes.
2. `occ app_api:app:list` shows `gocassini` enabled.
3. The Nextcloud app menu shows one **Cassini** entry for every logged-in user.
   It opens the meeting browser; administrators also see an **Operator** section
   inside the app. If the entry is missing, check the container log for
   `exapp ui:` errors, then disable and re-enable the app to retry registration.
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

**Back up the current backend first**, then switch. Operator → Publish pipeline → Connect Talk generates
these commands for your instance. AIO users must also follow the
[restart persistence instructions](recording-readiness.md#aio-restart-persistence).

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
3. Confirm a Cassini job appears in Cassini’s **Operator** section.
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
libraries and `CASSINI_STT_DEVICE=cuda` baked in. Install the fp32 Parakeet model separately in Settings or through offline import.
The GPU accelerates the **transcription (build) stage**; live call capture is
CPU-bound either way.

To use it, set the deploy daemon's **Compute device** to CUDA. AppAPI then
pulls `<image-tag>-cuda` automatically and attaches the host's NVIDIA GPUs to
the container via Docker device requests. The Docker engine running the ExApp
needs the NVIDIA driver + [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/);
verify with `docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi`
on that engine before registering the app.

CPU transcription is supported on amd64 and arm64. Transcription starts Off;
recordings remain playable without a model. In Settings, choose a quality/device,
download and check its model, then explicitly enable transcription and save.
Missing or unusable models produce audio with an explanatory transcription status.
Models and VAD persist independently of application images, so routine upgrades
reuse them. See [model configuration and air-gapped pack/import](proposals/optional-transcription-model-storage/implementation.md).


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
majors kept in sync across host reboots. `nvidia-smi` and `docker run --gpus all … nvidia-smi`
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
$APP_PERSISTENT_STORAGE/operator/models          # verified models/VAD and resumable downloads
$APP_PERSISTENT_STORAGE/operator/settings.json   # transcription policy and active revision
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

## Updating from an earlier recording archive

This release does not migrate old recording roots. See
[Recording access and cutover](./direct-shares-cutover.md) before retiring an old
Team folder or broad access rule.

## Uninstall

Restore Talk's previous recording backend first (see the rollback command in
Step 5), then:

```bash
occ app_api:app:unregister gocassini            # keeps the data volume
occ app_api:app:unregister gocassini --rm-data  # also deletes recordings + job history
```

## Standalone operator (dev/staging only)

`deployment/compose.yml` brings up the operator and viewer as
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
