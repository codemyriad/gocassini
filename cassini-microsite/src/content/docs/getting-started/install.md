---
title: Install on Nextcloud
description: The production install — a HaRP deploy daemon, the app registration, the verification checklist, and the reversible Talk handoff.
source: docs/exapp-install.md
copied: "2026-09-17"
---

Cassini ships as a Nextcloud AppAPI external app. One container exposes the
admin operator surface, the meeting archive, the viewer and the Talk recording
backend over a single HTTP port, and Nextcloud's AppAPI proxy fronts it with
per-route access enforcement.

The flow is deliberately two-phase and reversible. First you install and verify
the app, with Talk untouched. Then you hand Talk's recording over to Cassini,
having backed up what was there before.

All `occ …` commands below are shorthand for however your deployment invokes occ
(for example `sudo -u www-data php occ …` or
`docker exec -u www-data <nc-container> php occ …`).

## Prerequisites

- **Nextcloud 32 or newer** (the manifest's `min-version`; Cassini targets and is
  tested against Nextcloud 33+), with the **AppAPI** app installed and enabled.
- A registered AppAPI **deploy daemon** — see Step 1.
- **A `cassini` service account.** Every recording is written and read as it, in
  either audience. Cassini tries to create it and its `cassini` group itself when
  the app is enabled, which works on releases that let an external app write to
  user administration. Nextcloud 34.0.2 and later refuse that request, and then
  Cassini creates it from your browser instead: open **Cassini** as an
  administrator and press **Create the account and start**, and it makes the
  account as you, after Nextcloud's own password prompt. By hand instead:

  ```bash
  occ group:add cassini
  occ user:add --group=cassini cassini
  ```

- **Only for recordings visible to meeting participants:** the native **Group
  folders / Team folders** (`groupfolders`) and **Everyone Group**
  (`group_everyone`) apps, plus a `Cassini` Team folder mapped and ACL-enabled.
  Cassini builds the folder, its mappings and its permissions for you, as you,
  when you pick that audience. It cannot install the two apps. Without them
  Cassini still records and publishes, and its recordings are visible to anyone
  with an account on this Nextcloud. See
  [Who can see a recording](/docs/guides/who-can-see-a-recording).
- **An administrator account Cassini can act as**, to check how the instance is
  set up: which apps are enabled, whether the `cassini` account exists, whether
  there is a Team folder. That check is read-only, and the archive itself is
  never touched as an administrator — every recording is written, read and moved
  as `cassini`. In almost every case this needs no configuration; set
  `CASSINI_NC_ADMIN_USER` only when discovery cannot find an administrator or
  picks the wrong account.
- **A Docker engine** for the container. For GPU transcription it also needs the
  NVIDIA driver and Container Toolkit — see
  [CPU or GPU](/docs/guides/cpu-or-gpu).
- **Standalone Talk signalling / HPB** with an internal client secret
  (`[clients] internalsecret`), for private, group and one-to-one recording.
  Cassini's default recorder path uses this HPB-internal mode.

Persistent storage is automatic: AppAPI creates a named volume for every
docker-deployed external app, and the operator stores all durable data under it.

## Step 1 — Register a deploy daemon (HaRP)

Use a **HaRP** daemon. Upstream AppAPI recommends HaRP; the older Docker Socket
Proxy daemon is deprecated and scheduled for removal in Nextcloud 35.

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

Then register it in Nextcloud → Administration settings → AppAPI → **Register
Daemon** (template "HaRP Proxy"), and run **Test deploy** from the daemon's
three-dot menu before going further. `occ app_api:daemon:list` should show it
afterwards.

### Step 1b — Route `/exapps/*` to HaRP at your reverse proxy

**Required for every HaRP daemon**, local or remote. AppAPI does not talk to a
HaRP-hosted app over an internal address — it builds a **public** URL and dials
it:

```text
GET https://cloud.example.com/exapps/<appid>/heartbeat
```

Your TLS terminator must send `/exapps/*` to HaRP's `8780`, *not* to Nextcloud.
Without this route the request reaches Nextcloud, which 502s, and install/enable
never completes.

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
HaRP routes on `/exapps/<appid>/…`. The nginx equivalent is
`location /exapps/ { … }` with a `proxy_pass` that has **no** trailing path
element, so the prefix survives.

The failure is indirect and easy to misdiagnose: the app reports `[enabled]` in
`occ app_api:app:list` but its **navigation icon never appears**, because Cassini
registers its navigation entries only after receiving `PUT /enabled?enabled=1` —
a callback that never arrives.

Do not test this with an unauthenticated `curl https://…/exapps/…`. It returns
502 whether the route is right or wrong. Verify with `occ` plus `nextcloud.log`,
and by looking for `PUT /enabled` in the container log.

## Step 2 — Pick an image tag

CI publishes to `ghcr.io/codemyriad/gocassini`:

| Tag | What it is |
|---|---|
| `X.Y.Z` | Multi-arch portable image (`linux/amd64`, `linux/arm64`). Immutable by convention; matches `<version>`/`<image-tag>` in the manifest. It records without a GPU and runs local CPU transcription with a bundled int8 Parakeet model, on x86_64 and 64-bit ARM servers. |
| `X.Y.Z-cuda` | CUDA release build for x86_64 (CUDA 12 / cuDNN 9 sherpa-onnx, fp32 Parakeet model, `CASSINI_STT_DEVICE=cuda`) |
| `X.Y.Z-rocm` | Alias of the CPU build so ROCm-tagged daemons install; no ROCm acceleration yet |
| `sha-<shortsha>` / `sha-<shortsha>-cuda` | Every pushed commit, for pinning a specific build |
| `latest` / `latest-cuda` / `latest-rocm` | Convenience tags — fine for demos, **not** for production installs |

`appinfo/info.xml` pins `<image-tag>` to the release version, so a default AppAPI
install (and any later reinstall) pulls exactly the build the registered app
version was cut from, instead of whatever `latest` points at that day.

You do not select the `-cuda` tag by hand. When the deploy daemon's compute
device is CUDA, AppAPI tries `<image-tag>-cuda` first and falls back to the plain
tag. Cassini detects that fallback: the plain image remains available for
capture, the status endpoint reports CUDA unavailable, and build jobs enter
`build/blocked` with instructions to install the matching `-cuda` image, instead
of decoding on the CPU.

The checked-in manifest already pins the current release. To install a different
build, download `appinfo/info.xml`, set `<image-tag>` to the `sha-…` or release
tag you want, and register from that local copy.

## Step 3 — Register the app

The Talk recording secret is **optional**: if you omit
`CASSINI_TALK_RECORDING_SECRET`, the operator generates one on first start and
persists it on the AppAPI volume, and Step 5 reads it back from the operator's
provisioning endpoint — so nobody has to invent or copy it. Supply one explicitly
only when you want to manage it out of band; an explicit value always wins:

```bash
# Optional — omit to let Cassini self-generate. Never reuse an example value.
CASSINI_SECRET="$(openssl rand -hex 32)"
```

### Finding the signalling internal secret

`CASSINI_TALK_SIGNALING_INTERNAL_SECRET` must equal your Talk signalling / HPB
server's `[clients] internalsecret`. It is the one value Cassini cannot
self-provision: the invisible HPB-internal recorder authenticates to the
signalling server with this shared secret, and no API exposes it, so you supply
it once.

- **Nextcloud All-in-One:** `docker exec nextcloud-aio-talk printenv INTERNAL_SECRET`
- **Standalone HPB:** the `[clients] internalsecret` in the signalling server's
  config (for example `server.conf`). If you are setting signalling up at the
  same time, generate the value once and put it in both places.

```bash
SIGNALING_INTERNAL_SECRET="<value from the command / config above>"
```

These are two different secrets. `CASSINI_SECRET` authenticates Talk's
recording-backend HTTP protocol; `SIGNALING_INTERNAL_SECRET` authenticates
Cassini as an internal signalling client for HPB-internal call capture.

If the signalling secret is missing, the operator logs
`WARNING: talk_signaling_internal_secret_set -> false: …` at startup, and
`GET /operator/status` returns `"signaling_internal_secret_configured": false`
with a hint. Recording stays disabled until it is set.

### Register from a pinned manifest

`--info-xml` accepts a local path or a raw URL; pin a tag or commit SHA, not a
moving branch.

```bash
curl -fsSL "https://raw.githubusercontent.com/codemyriad/gocassini/<tag-or-sha>/appinfo/info.xml" \
    -o /tmp/gocassini-info.xml

occ app_api:app:register gocassini <daemon-name> \
    --info-xml /tmp/gocassini-info.xml \
    --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET}" \
    --wait-finish
    # Optionally add: --env CASSINI_TALK_RECORDING_SECRET="${CASSINI_SECRET}"
```

If `occ` runs inside a container, copy the manifest in first
(`docker cp /tmp/gocassini-info.xml <nc-container>:/tmp/`) or pass the raw URL
straight to `--info-xml`.

`<daemon-name>` is the `Name` column of `occ app_api:daemon:list`. AppAPI pulls
the image, creates the persistent volume, starts the container
(`nc_app_gocassini`), and enables the app once the container answers its
heartbeat and reports init completion.

### App configuration (`--env`)

These variables are declared under `<environment-variables>` in
`appinfo/info.xml`. That declaration is what makes them settable at all: AppAPI
only passes declared variables to the container, and `--env` values for
undeclared keys are **silently dropped**. Set them at registration time, on the
command line as above or in the External Apps admin UI (Deploy Options).

| Variable | Required | What it does |
|---|---|---|
| `CASSINI_TALK_RECORDING_SECRET` | No (auto-generated) | Shared secret for Talk's recording backend protocol; must match the `secret` in `spreed`'s `recording_servers` (Step 5). If omitted the operator generates and persists one. An explicit value wins and is treated as externally managed |
| `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | For HPB-internal (default) Talk recording | Internal client secret for standalone Talk signalling / HPB; must match `[clients] internalsecret`. Required for private, group and one-to-one recording |
| `CASSINI_TALK_BACKEND_URL` | No | Override for operator→Talk callbacks. Leave empty to use the backend URL Talk sends with each request |
| `CASSINI_NC_ADMIN_USER` | On instances where no discovered account is an administrator | The account Cassini acts as when it checks how the instance is set up. That check creates nothing. Recordings are still owned, written and managed by `cassini` |
| `CASSINI_PUBLISH_SINK` | No | Where published recordings are stored. `nextcloud-files` (the default for an installed app) puts them in Nextcloud Files; `local` keeps them on the app's own volume. Set `local` only deliberately |
| `CASSINI_STT_BACKEND` | No | Which speech-to-text engine transcription uses; empty selects the default (`sherpa-onnx`). An unknown value fails the build loudly before any audio is decoded |
| `CASSINI_DISALLOW_MODEL_DOWNLOAD` | No | Set `1` on a host with no outbound network access. Each image bundles the model of its default quality tier; another tier downloads once into the model cache on the persistent volume. With this set, a build whose tier needs that download is blocked with a message naming the missing model |
| `CASSINI_ROOM_ID_PEPPER` | No (recommended) | Deployment-wide secret mixed into the one-way derivation of each meeting's room id, so the Talk conversation token it comes from cannot be recovered by enumeration. **Choose it once:** changing it changes every room id, and meetings already published keep the ids they were written with |
| `LLM_BASE_URL`, `LLM_MODEL`, `OPENROUTER_API_KEY` | No | Seed the app's AI settings on first start only; afterwards endpoints are managed in the app. See [AI providers](/docs/guides/ai-providers) |
| `CASSINI_OPERATOR_API_TOKEN` | No | Bearer token for direct, non-AppAPI operator API calls. Requests proxied through Nextcloud are authenticated by AppAPI |

The full set, including the attribution and retention knobs and everything AppAPI
injects on its own, is in
[`docs/exapp-install.md`](https://github.com/codemyriad/gocassini/blob/main/docs/exapp-install.md)
and
[`docs/exapp-talk-env-vars.md`](https://github.com/codemyriad/gocassini/blob/main/docs/exapp-talk-env-vars.md).

### Updating deploy options after install

AppAPI deploy environment is container-creation-time configuration, not live
Nextcloud app config. Changing Talk's `spreed.recording_servers.secret` does
**not** update `CASSINI_TALK_RECORDING_SECRET` in a deployed container, and
changing the signalling `internalsecret` does not update its variable either.

For secret rotation, recreate or redeploy the app with all required `--env`
values while preserving the AppAPI persistent volume. `app_api:app:update`
reuses stored deploy options and has no `--env` flag.

Because deploy environment is creation-time only, **a release that adds a new
*required* environment variable cannot be delivered by the admin UI's Update
button** — it is a breaking change that needs a redeploy.

### Where recordings live

Each audience has its own root inside the `cassini` account's Files, and the two
are deliberately different paths:

```text
the `cassini` service account's Files
─────────────────────────────────────
  CassiniNoACL/Recordings   its OWN directory. No Team folder is mounted there
                            and no other account has a mount of it. Visible to
                            ANYONE WITH A NEXTCLOUD ACCOUNT.

  Cassini/Recordings        inside the `Cassini` Team folder, under advanced
                            ACLs. Visible to MEETING PARTICIPANTS — each
                            recording readable only by the people who were in
                            the call, enforced by Nextcloud itself.
```

Both have the same shape inside: `meetings/<job-id>.opus` beside a
`catalog.json`. Cassini resolves which one an install gets when the app is
enabled, from what is already on the instance, and never widens an existing
archive on its own. Full detail, and how to switch, is in
[Who can see a recording](/docs/guides/who-can-see-a-recording).

## Step 4 — Verify the install (before touching Talk)

All of these must pass before the Talk handoff.

1. `occ app_api:daemon:list` shows the daemon and its **Test deploy** passes.
2. `occ app_api:app:list` shows `gocassini` enabled.
3. The Nextcloud app menu shows a **Cassini** entry for every logged-in user. If
   it is missing, check the container log for `exapp ui:` errors, then disable
   and re-enable the app to retry the registration.
4. The container runs the intended image:
   `docker inspect nc_app_gocassini --format '{{.Config.Image}}'`.
5. The Talk welcome endpoint answers through the AppAPI proxy (a PUBLIC route,
   so plain curl works):

   ```bash
   curl -fsS https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/api/v1/welcome
   # → {"version":1}
   ```

6. The status endpoint reports `"ok": true`. It is an ADMIN route, so use an
   admin login with an app password:

   ```bash
   curl -fsS -u admin:<app-password> \
     https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status
   ```

   It reports the app version, the transcription device (`cpu`/`cuda`) and
   whether it is actually usable, whether the Talk recording secret and the
   signalling internal secret are configured (never the values), and DB and
   storage health. The Talk fields should look like:

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

   `secret_source` is `generated` when the operator made the recording secret
   itself, or `env` when you supplied one. `recording_backend_url` is the value
   to register in Step 5 — never the secret itself, which comes from the
   provisioning endpoint below.

7. CUDA installs only: the image tag ends in `-cuda` and the container can see
   the GPU (`docker exec nc_app_gocassini nvidia-smi`). The status endpoint must
   show `"device": "cuda"` with `"device_usable": true`.

## Step 5 — Talk handoff (reversible)

Point Talk's recording backend at the AppAPI proxy base. The `api/v1/welcome` and
`api/v1/room/*` routes are declared PUBLIC in the manifest, so Talk's recording
protocol — authenticated by its own HMAC, not a Nextcloud session — passes
through the proxy.

Talk has no API for an app to register itself as the recording backend, so this
one admin step stays manual. It is secret-free: the operator's ADMIN-only
provisioning endpoint returns the ready-to-apply `recording_servers` value,
including the self-generated secret.

**Back up the current backend first**, then switch:

```bash
# 0. Back up (empty output = no recording backend configured)
occ config:app:get spreed recording_servers | tee /root/recording_servers.backup

# 1. Pull the ready-to-apply value from Cassini and register it in one step.
RS="$(curl -fsS -u admin:<app-password> \
  https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/talk/provisioning \
  | jq -c '.recording_servers')"
occ config:app:set spreed recording_servers --value="$RS"
occ config:app:set spreed call_recording --value=yes
```

If you supplied `CASSINI_TALK_RECORDING_SECRET` yourself in Step 3, you can set
`recording_servers` directly with that secret and the `recording_backend_url`
from the status endpoint.

**A controlled test.** Use a non-critical private, group or one-to-one
conversation, so the HPB-internal path is exercised.

1. Pick a test conversation with at least one speaking participant.
2. Start recording from Talk's **Record** button.
3. Confirm a Cassini job appears in the app's Operator surface.
4. Speak for a minute, then stop the recording or leave the call.
5. Watch the job progress through record → build → seal → publish. Talk receives
   started and stopped status per its recording-backend protocol and nothing
   else; the meeting itself is published as a portable `.opus` into Nextcloud
   Files.
6. Run a second recording and confirm both transcripts stay visible.

**Rollback.** Restore the saved value and Talk records through the previous
backend again; the Cassini app can stay installed.

```bash
occ config:app:set spreed recording_servers --value="$(cat /root/recording_servers.backup)"
# or, if there was no recording backend before:
occ config:app:delete spreed recording_servers
```

Keep the previous backend running until your test recording passes.

### Secret rotation

Rotate secrets as a coordinated operation; do not change only one side.

For the Talk recording secret: pause recordings, update
`spreed.recording_servers.secret`, redeploy Cassini with the same value as
`CASSINI_TALK_RECORDING_SECRET`, confirm `/operator/status` reports
`secret_configured: true`, and run a controlled recording.

For the signalling internal secret: update the HPB `[clients] internalsecret` and
restart signalling, redeploy Cassini with the same value as
`CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, confirm `/operator/status` reports
`signaling_internal_secret_configured: true`, and run a private, group or
one-to-one recording.

## GPU transcription

Set the deploy daemon's **Compute device** to CUDA, and AppAPI pulls
`<image-tag>-cuda` and attaches the host's NVIDIA GPUs to the container. The
Docker engine running the app needs the NVIDIA driver and the
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/);
verify with
`docker run --rm --gpus all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi` on
that engine before registering the app.

There is no CPU fallback from a CUDA install: a plain image under a CUDA daemon
is a permanent mismatch, so recording finishes and the build enters
`build/blocked` with an actionable message. Install the matching `-cuda` image,
then use **Rerun** to process the recording that was preserved.

Choosing between the two, and what changes in the transcript, is in
[CPU or GPU](/docs/guides/cpu-or-gpu).

## Persistent storage

AppAPI's docker deploy creates a named volume (`nc_app_gocassini_data`), mounts
it at `/nc_app_gocassini_data`, and exposes that path as
`APP_PERSISTENT_STORAGE`. The operator stores all durable data under it:

```text
$APP_PERSISTENT_STORAGE/operator/jobs.sqlite3    # SQLite job DB
$APP_PERSISTENT_STORAGE/operator/app-state.json  # AppAPI lifecycle state
$APP_PERSISTENT_STORAGE/operator/jobs            # per-attempt artifacts
```

No manual volume mounts are required — job history and recordings survive app
updates and container recreates. The container logs a warning at startup when an
effective data path sits on an ephemeral filesystem.

## Uninstall

Restore Talk's previous recording backend first (the rollback command in Step 5),
then:

```bash
occ app_api:app:unregister gocassini            # keeps the data volume
occ app_api:app:unregister gocassini --rm-data  # also deletes recordings + job history
```

Recordings already published into Nextcloud Files are unaffected either way; they
are ordinary Nextcloud files from the moment they are written.
