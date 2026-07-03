# Cassini deployment bundle

This folder is the repo-root Docker Compose bundle for:

- `cassini-operator`
- `cassini-control-panel`
- `cassini-viewer`

## Quickstart

```bash
cd deployment
cp .env.example .env   # optional: only needed to change defaults or set secrets
docker compose up --build
```

Default browser surfaces (published on loopback only):

- control panel: `http://127.0.0.1:4173/`
- operator API: `http://127.0.0.1:4000/`
- viewer: `http://127.0.0.1:8765/`

The control panel talks to the operator through the shared same-origin API path set by `CASSINI_OPERATOR_BASE_PATH`.
With the default `CASSINI_OPERATOR_BASE_PATH=/`, the browser uses `/jobs` and `/events` on the control-panel origin.
If you change it to `/operator`, the browser uses `/operator/jobs` and `/operator/events` instead.

## Network exposure

The operator job API has no authentication of its own: anyone who can reach
`http://<host>:4000` can create jobs (point the recorder bot at arbitrary
URLs), stop recordings, and read all job metadata. Because of that, all
published ports bind to `127.0.0.1` by default.

If you really need to expose the bundle on another interface, set
`CASSINI_PUBLISH_ADDRESS` in `.env` (for example to a LAN or Docker bridge
address). Keep in mind:

- Docker port publishing bypasses host firewalls such as ufw/firewalld; the
  port is reachable as soon as it is published.
- `CASSINI_PUBLISH_ADDRESS=0.0.0.0` exposes the unauthenticated operator API
  to every network the host is attached to. Put a reverse proxy with
  authentication in front of it before doing that on a shared network.
- The `compose.hostnet.yml` override (below) is dev-only: with host networking
  the operator listens on `0.0.0.0:4000` directly, ignoring
  `CASSINI_PUBLISH_ADDRESS`.

## Public bundle knobs

The tracked `.env.example` documents the deployment-facing contract (copy it
to `.env` and edit):

- `CASSINI_PUBLISH_ADDRESS`
- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`
- `CASSINI_TALK_RECORDING_SECRET`
- `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
- `CASSINI_TALK_BACKEND_URL`

### Talk recording secret

Generate a fresh secret per deployment and configure Talk with the same value:

```bash
openssl rand -hex 32
```

> **Warning — rotate the old default.** Earlier revisions of this repository
> committed a concrete `CASSINI_TALK_RECORDING_SECRET` value in
> `deployment/.env`, and the local harness (`harness/bin/common.sh`) still
> uses that value as its dev-only fallback. The value is permanently visible
> in git history, so any real deployment that adopted it must rotate to a
> freshly generated secret (update both the operator `.env` and the Talk
> recording-backend configuration).

When using Cassini as a Nextcloud Talk recording backend, configure Talk with the
same `CASSINI_TALK_RECORDING_SECRET` value and a backend URL that is reachable
from the Nextcloud container. For the repo harness this is usually the Docker
bridge gateway, for example `http://172.17.0.1:4000`, not
`http://127.0.0.1:4000`. With the default loopback port binding a
containerized Nextcloud cannot reach the bridge gateway address, so either set
`CASSINI_PUBLISH_ADDRESS` to that gateway address (for example `172.17.0.1`)
or use the host-network override below.

Cassini now defaults Nextcloud Talk recording jobs to `hpb-internal` mode.
For that mode, set `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to the standalone
signaling server `internalsecret` value. Use `talkAuthMode=guest-participant`
only as an explicit fallback.

If Talk advertises a public Nextcloud URL such as `http://localhost:28080`, set
`CASSINI_TALK_BACKEND_URL` to the URL the operator container can use to reach
that same Nextcloud instance, for example `http://host.docker.internal:28080`.

For local manual Talk recording against the repo Nextcloud harness, use the
host-network override so the recorder and browser participant are seen by the
high-performance signaling server as part of the same active call (dev-only:
this exposes the operator on all host interfaces, see "Network exposure"):

```bash
cd deployment
docker compose -f compose.yml -f compose.hostnet.yml up -d --build
```

Then configure Talk's recording backend with the host gateway URL that the
Nextcloud container can reach, for example `http://172.18.0.1:4000`.

## Storage defaults

By default the bundle uses Docker-managed named volumes:

- `cassini_operator_state`
- `cassini_published_site`

The shared published-site storage is mounted at `/srv/cassini-site`.
The live deployed site is the child directory `/srv/cassini-site/published`, so the operator can atomically swap `published/` during promotion while the viewer mounts the same shared storage read-only.

## Operator startup (site seeding)

Before starting the operator process, the operator container's entrypoint prepares the shared storage so the viewer works on a clean deployment. It:

1. ensures the operator state directories exist;
2. ensures the published-site directory exists;
3. migrates an older flat site layout into `published/` when necessary;
4. seeds an initial empty live site if `published/index.html` is missing.

The seeded site is minimal but complete — `index.html`, `assets/`, an empty `catalog.json`, and a site-level `cassini.json` — so the viewer starts and serves an empty catalog before any meeting has been published.

## Bind-mount override

If you want host-visible storage instead of the default named volumes, set absolute paths in `.env`:

```dotenv
CASSINI_OPERATOR_STATE_STORAGE=/absolute/path/to/cassini-operator-state
CASSINI_PUBLISHED_SITE_STORAGE=/absolute/path/to/cassini-published-site-storage
```

Then run the same command:

```bash
cd deployment
docker compose up --build
```

## Optional capability pass-through

The bundle also passes these optional operator capability envs through when you set them in `.env` (see `.env.example`):

- `OPENROUTER_API_KEY`
- `OPENROUTER_BASE_URL`
- `LLM_BASE_URL`
- `LLM_MODEL`
- `SUMMARY_MODEL`
- `CASSINI_SUMMARY_DISABLED`
- `CASSINI_READABLE_STRICT_BATCHES`

They are intentionally not part of the narrow core deployment contract.

## Quick checks

```bash
cd deployment
docker compose up --build
curl -s http://127.0.0.1:4000/jobs
curl -s http://127.0.0.1:4173/jobs
curl -s http://127.0.0.1:8765/catalog.json
```
