# Trying the ExApp image in a local Nextcloud

Use the lowest tier that exercises the surface you care about.

## Tier 1 — Image-only checks (no Nextcloud)

Verifies that the ExApp image builds and that the HTTP plane works: operator
API, lifecycle endpoints, AppAPI middleware, control panel, and viewer assets.
This does **not** validate Nextcloud/AppAPI registration or Talk recording.

```bash
# Build the image locally
docker build -f deployment/Dockerfile.exapp -t cassini-exapp:local .

# Smoke test: image in dev mode (no APP_SECRET, middleware off)
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-smoke-exapp.sh

# E2E: image with APP_SECRET set; asserts middleware refuses without auth,
# accepts valid AppAPI headers, lifecycle works, state survives restart
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-e2e-exapp.sh
```

You can also pull a published image instead of building:

```bash
docker pull ghcr.io/codemyriad/gocassini:latest
IMAGE_REF=ghcr.io/codemyriad/gocassini:latest ./harness/bin/ci-smoke-exapp.sh
```

## Tier 2 — AppAPI install/proxy checks without Talk recording

[`harness/bin/ci-e2e-install-exapp.sh`](../harness/bin/ci-e2e-install-exapp.sh)
installs Cassini into a real local Nextcloud through AppAPI and validates:

- AppAPI registration;
- AppAPI proxy route ACLs;
- control panel and viewer routes;
- AppAPI lifecycle callbacks;
- persistent state survival.

Run it when you need a quick installed-ExApp smoke test:

```bash
docker build -f deployment/Dockerfile.exapp -t cassini-exapp:local .
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-e2e-install-exapp.sh
```

Important scope note: this script uses a local/manual install shape and does
not configure Talk's record button. It is useful for AppAPI proxy/UI
regressions, but it does not prove production Talk recording.

## Tier 3 — Production-shaped AppAPI/HaRP + Talk harness

This is the D-395 surface: Nextcloud + AppAPI + HaRP/reverse proxy + full Talk
signaling stack, with Cassini installed as an ExApp and Talk pointed at the
AppAPI proxy base:

```text
Talk record button
→ /index.php/apps/app_api/proxy/gocassini/api/v1/room/<token>
→ AppAPI route check
→ HaRP / ExApp container
→ cassini-operator
→ HPB-internal recorder
→ publish into APP_PERSISTENT_STORAGE
→ viewer through the ExApp proxy
```

### Local host

From the repo root:

```bash
./bin/cassini dev stack up \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

The stack command starts the harness, builds/tags the ExApp image from
`appinfo/info.xml`, installs/reinstalls Cassini via AppAPI, passes both Talk
secrets as deploy env, and configures Talk's `recording_servers` to the
installed ExApp proxy path. Use `./bin/cassini dev stack plan ...` with the
same flags to inspect the resolved config without mutating containers.

### `dev-vm`

For the Multipass VM used by the D-395 validation, run from the mounted repo
inside the VM:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./bin/cassini dev stack up \
    --services full \
    --cassini installed-exapp \
    --recording-backend installed-exapp \
    --build
'
```

Open Nextcloud at `http://<vm-ip>:28080/` (`admin` / `admin`) and verify:

- **Cassini** appears for logged-in users and opens the viewer;
- **Cassini Admin** appears for admins and opens the control panel;
- `GET /api/v1/welcome` through the AppAPI proxy returns `{"version":1}`;
- `/operator/status` reports both `secret_configured` and
  `signaling_internal_secret_configured` as true.

### Installed-ExApp private Talk validation

After the harness is up, validate the real Talk recording path with the D-395
helper:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./harness/bin/validate-installed-exapp-private-talk.sh \
    --nextcloud-host <vm-ip> \
    --duration 60
'
```

The helper uses `./bin/cassini dev play-private` to create/reuse the admin +
Erlich Bachman private one-to-one conversation, triggers recording through
Talk so the installed ExApp receives the backend request, waits for publish,
then runs a second recording and verifies both new transcripts remain visible
in the viewer catalog.

### Archive preservation checks

The validation helper captures catalog IDs before recording, then fails if the
second publish removes either the first new job or any pre-existing catalog ID.
For manual inspection of the AppAPI persistent volume:

```bash
docker exec nc_app_gocassini sh -lc 'find "$APP_PERSISTENT_STORAGE/operator/jobs/current" -maxdepth 1 -name "*.meeting" | sort'
docker exec nc_app_gocassini sh -lc 'python3 - <<PY
import json, os
p=os.path.join(os.environ["APP_PERSISTENT_STORAGE"], "site/published/catalog.json")
d=json.load(open(p))
print(len(d.get("meetings", [])))
print([m.get("id") for m in d.get("meetings", [])])
PY'
```

Expected result after the D-395 helper: at least the two new job IDs remain in
`catalog.json`; if a catalog existed before the run, those earlier IDs remain
too.

## Related direct-container Talk test

[`harness/bin/ci-e2e-talk-record-roundtrip.sh`](../harness/bin/ci-e2e-talk-record-roundtrip.sh)
validates Talk's recording-backend protocol against a directly run
`cassini-operator` container. It is still valuable for fast recording/upload
regressions, but it bypasses AppAPI/HaRP, ExApp deploy env allow-listing, and
Nextcloud's installed app UI. Use Tier 3 for production-shaped validation.

## Teardown

For the AppAPI/HaRP harness:

```bash
./bin/cassini dev stack stop --full
```

For the VM harness, use the same command inside `/home/ubuntu/dev/workspace` or
use the helper documented by the script output.
