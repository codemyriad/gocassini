# Cassini Sandbox Deployment

This directory contains the repo-owned deployment wrapper for a cheap VPS
sandbox. The goal is a mutable environment that can be updated, reset, or
destroyed without hand-editing Nextcloud state.

The sandbox reuses the existing harness topology:

- Nextcloud + Postgres
- AppAPI + HaRP deploy daemon
- Cassini as an AppAPI ExApp
- optional full Talk stack: signaling, Janus, NATS, TURN

Unlike the CI harness, the sandbox intentionally tracks the newest upstream
Nextcloud container by default: `NEXTCLOUD_IMAGE=nextcloud:latest`, with Compose
`pull_policy: always` so normal deploys refresh the image before starting
Nextcloud. Pin `NEXTCLOUD_IMAGE` in `sandbox/.env` only when debugging a
version-specific issue.

## First setup on a VPS

1. Install Docker, Docker Compose, Git, curl, openssl, and perl.
2. Point a DNS name at the VPS.
3. Make sure the VPS does not map the public sandbox hostname to loopback in
   `/etc/hosts`. AppAPI-created ExApp containers can inherit that mapping; if
   `cassini-sandbox.example.com` resolves to `127.0.1.1` inside the ExApp,
   Cassini cannot connect to Talk signaling. Prefer either public DNS
   resolution or a host-gateway/public-IP mapping.
4. Put TLS in front of Nextcloud. The host reverse proxy must route normal
   Nextcloud traffic to `NEXTCLOUD_HOST_PORT` and route Talk HPB traffic under
   `/spreed` to signaling on `28082`. Talk appends its own websocket and
   backend API paths to `SANDBOX_SIGNALING_URL`, so the proxy must strip the
   outer `/spreed` prefix before forwarding nested paths such as
   `/spreed/spreed` and `/spreed/api`.

   A minimal Caddy shape is:

   ```caddyfile
   cassini-sandbox.example.com {
       encode gzip zstd

       handle /spreed/* {
           uri strip_prefix /spreed
           reverse_proxy 127.0.0.1:28082
       }

       handle /spreed* {
           reverse_proxy 127.0.0.1:28082
       }

       handle {
           reverse_proxy 127.0.0.1:28080
       }
   }
   ```

   Open both TCP 443 and UDP 443 if you want Caddy's default HTTP/3 support.
   After changing HTTP protocol settings, restart Caddy rather than only
   reloading it so the HTTP/3 listener and `Alt-Svc` advertisement agree.

   For `SPREED_PROFILE=full`, also open the Talk media ports:

   - TCP/UDP `13479` for TURN.
   - UDP `49160-49200` for TURN relay allocations.
   - UDP `20000-20100` for Janus RTP. The sandbox renders Janus with
     `nat_1_1_mapping` set from `SANDBOX_JANUS_EXTERNAL_IP`, or from
     `SANDBOX_TURN_EXTERNAL_IP` when that is unset.

5. Copy the sample env and edit it:

   ```bash
   cp sandbox/env.example sandbox/.env
   $EDITOR sandbox/.env
   ```

6. Deploy:

   ```bash
   sandbox/deploy.sh
   ```

Use `SPREED_PROFILE=default` in `sandbox/.env` if you only need to demo AppAPI
install, the control panel, and the viewer. Use `SPREED_PROFILE=full` when you
need Talk call recording.

## Updating

Deploy a specific image:

```bash
sandbox/deploy.sh --image ghcr.io/codemyriad/gocassini:branch-d-290-e2e-testing
```

Rebuild locally on the VPS from the current checkout:

```bash
sandbox/deploy.sh --build --image ghcr.io/codemyriad/gocassini:sandbox-local
```

Only re-register the ExApp, without restarting Nextcloud:

```bash
sandbox/deploy.sh --register-only --image ghcr.io/codemyriad/gocassini:latest
```

## Demo UI mode

The current demo uses the existing Cassini viewer and control panel through
AppAPI proxy URLs so the sandbox shows the expected end-user shape today. To
support that embedded/proxied UI, `SANDBOX_PATCH_APPAPI_CSP=true` applies a
sandbox-only patch to AppAPI's `ExAppProxyController.php`.

That patch intentionally modifies a signed AppAPI file, so Nextcloud will report
an `app_api` integrity warning. This is acceptable only for mutable demo and
development sandboxes. Set `SANDBOX_PATCH_APPAPI_CSP=false` for a
production-like sandbox, with the tradeoff that the proxied embedded UI may hit
browser CSP restrictions until Cassini is moved to AppAPI's native UI
script/style mechanism.

## Persistence

Nextcloud, Postgres, AppAPI/HaRP certificates, and the Cassini ExApp each use
Docker-managed named volumes. In ExApp mode, AppAPI mounts Cassini's persistent
volume at `/nc_app_gocassini_data` and sets `APP_PERSISTENT_STORAGE`.

Cassini stores its operator database, job artifacts, temporary files, and
published viewer output under that AppAPI persistent storage path. This data
survives container recreation and ExApp re-registration. It is removed only when
the corresponding Docker volume is removed, for example with
`sandbox/destroy.sh --volumes` or `sandbox/deploy.sh --reset`.

The sandbox registers Cassini with `CASSINI_TALK_BACKEND_URL` set to the public
`SANDBOX_PUBLIC_URL`. Nextcloud sets `Secure` session cookies when
`SANDBOX_SCHEME=https`; using an internal plain-HTTP callback URL for the
recorder would prevent the recorder's cookie jar from retaining the Talk guest
session between `participants/active` and `call/{token}`.

Each deploy also configures Nextcloud's reverse proxy trust from the active
Compose network. It sets `trusted_proxies` to the Docker bridge gateway used by
the host TLS proxy, plus the internal `reverse-proxy` container address used by
AppAPI callbacks, and sets `forwarded_for_headers` to `HTTP_X_FORWARDED_FOR`.
This keeps the admin security checks green after reset/redeploy without
hand-editing `config.php`.

Reset all Compose volumes and start from a clean Nextcloud:

```bash
sandbox/deploy.sh --reset
```

## Teardown

Stop containers but keep data:

```bash
sandbox/destroy.sh
```

Remove containers and volumes:

```bash
sandbox/destroy.sh --volumes
```

## GitHub Actions

The `Deploy Sandbox` workflow builds and pushes an ExApp image, then SSHes into
the VPS and runs this same `sandbox/deploy.sh` script. CI should orchestrate the
deployment; the deployment behavior should stay in this directory.

Expected repository secrets:

- `SANDBOX_SSH_HOST`
- `SANDBOX_SSH_USER`
- `SANDBOX_SSH_KEY`
- optional `SANDBOX_SSH_PORT`
- optional `SANDBOX_REPO_DIR`
