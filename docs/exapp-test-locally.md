# Trying the ExApp image in a local Nextcloud

Three tiers, in increasing setup cost. Pick the lowest one that answers your question.

## Tier 1 — Image-only checks (no Nextcloud)

Verifies the image builds and the HTTP plane works (operator API, lifecycle, both SPAs).
This is what the CI workflow runs.

```bash
# Build the image locally
docker build -f deployment/Dockerfile.exapp -t cassini-exapp:local .

# Smoke test: image in dev mode (no APP_SECRET, middleware off)
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-smoke-exapp.sh

# E2E: image with APP_SECRET set; asserts middleware refuses without auth,
# accepts with valid AppAPI headers, lifecycle works, state survives restart
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-e2e-exapp.sh
```

You can also pull the published image instead of building:

```bash
docker pull ghcr.io/codemyriad/gocassini:latest
IMAGE_REF=ghcr.io/codemyriad/gocassini:latest ./harness/bin/ci-smoke-exapp.sh
```

## Tier 2 — Real Nextcloud install via AppAPI

This is what an admin would do, scripted end-to-end. The same recipe is
automated in [`harness/bin/ci-e2e-install-exapp.sh`](../harness/bin/ci-e2e-install-exapp.sh),
which the CI workflow runs against every PR — if you just want to run the
verification, do that instead:

```bash
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-e2e-install-exapp.sh
```

The manual recipe below is for when you want to poke at the install state
yourself.

### Prerequisites

- Docker + docker compose
- A free port (28080 by default for Nextcloud)
- The Cassini image, either built locally or pulled from ghcr

### Steps

1. **Bring up Nextcloud + database.** Use the harness's `default` profile —
   ExApp install doesn't need Talk's signaling/TURN overhead:

   ```bash
   cd harness
   SPREED_PROFILE=default docker compose -p cassini-exapp-test up -d nextcloud db
   PROJECT_NAME=cassini-exapp-test SPREED_PROFILE=default ./bin/bootstrap.sh
   ```

   The bootstrap helper reads `PROJECT_NAME` (not `COMPOSE_PROJECT_NAME`); set it
   so `bootstrap.sh` finds the same containers as your `docker compose -p` above.

2. **Install + enable AppAPI** inside Nextcloud:

   ```bash
   alias occ='docker compose -p cassini-exapp-test exec -T -u www-data nextcloud php occ'
   occ app:install app_api  # idempotent
   occ app:enable  app_api
   ```

3. **Register a manual-install daemon.** AppAPI builds the heartbeat URL from
   the daemon's host (not the app's host), so this has to be a hostname Nextcloud
   can resolve and reach. The docker-compose network gives every service DNS,
   so the container name we'll use in step 4 works directly — `null` will NOT,
   despite appearing as a placeholder in older runbooks:

   ```bash
   occ app_api:daemon:register \
       manual_install \
       "Local manual install" \
       manual-install \
       http \
       cassini-exapp \
       http://nextcloud
   ```

4. **Run the Cassini container.** It needs to be on the compose network so
   Nextcloud's DNS can find `cassini-exapp`. Override the entrypoint to skip
   `frpc` — we're not using HaRP here, Nextcloud reaches the container directly:

   ```bash
   APP_SECRET="$(head -c 24 /dev/urandom | base64 | tr -d /+= | head -c 32)"
   docker run -d --name cassini-exapp \
       --network cassini-exapp-test_default \
       -e APP_HOST=0.0.0.0 -e APP_PORT=8080 \
       -e APP_ID=gocassini -e APP_VERSION=0.1.0 \
       -e APP_SECRET="${APP_SECRET}" -e AA_VERSION=5.0.0 \
       -e CASSINI_APPAPI_REQUIRED=true \
       -e CASSINI_OPERATOR_BASE_PATH=/operator \
       -e NEXTCLOUD_URL=http://nextcloud \
       -v cassini-exapp-state:/var/lib/cassini-operator \
       -v cassini-exapp-site:/srv/cassini-site \
       --entrypoint /usr/local/bin/cassini-operator \
       cassini-exapp:local
   ```

   `NEXTCLOUD_URL` is what the operator's `/init` handler uses to PUT
   `progress=100` back to AppAPI's OCS endpoint. Without it, `--wait-finish`
   in step 5 will hang forever.

5. **Register Cassini with AppAPI.** Use `--json-info` with the route allowlist
   embedded inline. Route URL patterns must NOT carry a leading slash and must
   escape internal slashes as `\/` — AppAPI's proxy controller wraps each
   pattern in `/.../i` delimiters before `preg_match`, so an unescaped `/`
   produces "Unknown modifier" errors and 404s on every proxied request:

   ```bash
   JSON=$(jq -nc --arg secret "$APP_SECRET" \
     '{
        appid: "gocassini",
        name:  "Cassini",
        daemon_config_name: "manual_install",
        version: "0.1.0",
        secret:  $secret,
        port: 8080,
        protocol: "http",
        system_app: 0,
        routes: [
          {url: "^control-panel\\/?$",              verb: "GET",      access_level: 2},
          {url: "^control-panel\\/.+$",             verb: "GET,HEAD", access_level: 2},
          {url: "^operator\\/jobs\\/?$",            verb: "GET,POST", access_level: 2},
          {url: "^operator\\/jobs\\/[^\\/]+\\/?$",  verb: "GET",      access_level: 2},
          {url: "^operator\\/jobs\\/[^\\/]+\\/stop\\/?$",  verb: "POST",  access_level: 2},
          {url: "^operator\\/jobs\\/[^\\/]+\\/rerun\\/?$", verb: "POST",  access_level: 2},
          {url: "^operator\\/events\\/?$",          verb: "GET",      access_level: 2},
          {url: "^viewer\\/?$",                     verb: "GET",      access_level: 1},
          {url: "^viewer\\/.+$",                    verb: "GET,HEAD", access_level: 1},
          {url: "^published\\/.+$",                 verb: "GET,HEAD", access_level: 1}
        ]
      }')
   occ app_api:app:register gocassini manual_install \
       --json-info "$JSON" \
       --force-scopes \
       --wait-finish
   ```

   `--wait-finish` polls until the operator reports `progress=100` via the OCS
   callback. The operator's `/init` does that automatically when `NEXTCLOUD_URL`
   is set (step 4).

6. **Force `PUT /enabled` by cycling.** `app_api:app:register` flips the
   Nextcloud-side enabled flag but never PUTs `/enabled` to the container.
   `app_api:app:enable` short-circuits when the flag is already set
   ("already enabled"). The reliable way to make AppAPI actually call the
   container's lifecycle handler is disable → enable:

   ```bash
   occ app_api:app:disable gocassini
   occ app_api:app:enable  gocassini
   docker exec cassini-exapp cat /var/lib/cassini-operator/app-state.json
   # -> {"enabled":true,...}
   ```

7. **Verify proxied routes** (admin/admin works out of the box; create another
   user with `occ user:add` for the USER-tier checks):

   ```bash
   PROXY=http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini
   curl -u admin:admin     -o /dev/null -w '%{http_code}\n' "$PROXY/control-panel/"  # -> 200
   curl -u admin:admin     -o /dev/null -w '%{http_code}\n' "$PROXY/operator/jobs"   # -> 200
   curl -u admin:admin     -o /dev/null -w '%{http_code}\n' "$PROXY/viewer/"         # -> 200
   curl -u alice:alicepass -o /dev/null -w '%{http_code}\n' "$PROXY/viewer/"         # -> 200
   curl -u alice:alicepass -o /dev/null -w '%{http_code}\n' "$PROXY/control-panel/"  # -> 404
   ```

### Teardown

```bash
docker rm -f cassini-exapp
docker volume rm cassini-exapp-state cassini-exapp-site
docker compose -p cassini-exapp-test down --volumes
```

## Tier 3 — Production-shaped install via HaRP

A real production AppAPI install spawns the container itself via the deploy
daemon and routes traffic through HaRP. Reproducing this locally requires:

- AppAPI's HaRP server running alongside Nextcloud
- A Docker daemon registered with AppAPI's docker-install daemon type
- The container's `frpc` dialing the HaRP server on startup

This is not yet automated in the harness. The deferred Slice D (in
`planning/installable-nextcloud-app.md`) tracks turning Tier 2 into a CI E2E
plus extending it to a real HaRP-fronted install.

For now: Tier 2 verifies the install + enable contract using the same routes
and access levels a production install would use; Tier 1 verifies the
middleware itself.
