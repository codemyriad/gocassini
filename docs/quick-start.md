# Quick start

This is the fastest way to see Cassini working end to end **the way it really
runs**: as a Nextcloud Talk ExApp installed through AppAPI.

Goal:

- start a local Nextcloud + AppAPI/HaRP + Talk stack
- build and install Cassini as an ExApp (the production topology)
- record a Talk meeting through Talk's record button
- watch the job run through record -> build -> publish
- open the published meeting in the viewer — with its real conversation name

## Before you begin

Assumptions:

- you are at the repo root
- Docker is available (the ExApp image is built and run via Docker)

> Why the ExApp path is the default here: it exercises the real runtime —
> AppAPI/HaRP proxy → ExApp → operator → recorder → publish → viewer. Bugs that
> only appear on that path (AppAPI env allow-listing, publish-step regressions,
> Talk conversation-name resolution) are invisible to the lighter
> individual-service path and to unit tests. See the "Why this is the default"
> note below and Linear **D-453**.

## 1. Bring up the full ExApp stack

From the repo root:

```bash
./bin/cassini dev stack up \
  --public-mode local-http \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

This one command:

- starts local Nextcloud + AppAPI/HaRP + the full Talk signaling stack;
- installs the native Team folders and Everyone Group prerequisites;
- builds and tags the Cassini ExApp image from `appinfo/info.xml`;
- installs/reinstalls Cassini via AppAPI;
- passes both Talk secrets and `CASSINI_NC_ACCESS_CONTROL=true` as deploy env,
  and points Talk's `recording_servers` at the installed ExApp proxy path.

The first run is slow — it builds the ExApp image. Later runs without `--build`
reuse the existing image. Keep `--build` when validating changes in the current
checkout; otherwise the harness deliberately runs the previously built image,
which may be an older release even though Git is on your feature branch.
Access control is on by default in the harness, which installs its native apps
and lets Cassini provision the Team folder/ACL topology automatically. See
[Recording permissions](./exapp-nextcloud-recordings-permissions.md). Use
`--nc-access-control=false` only to compare the legacy public behavior.

When it finishes, open Nextcloud and sign in as `admin` / `admin`:

- `http://127.0.0.1:28080/`

Verify the install:

- **Cassini** appears for logged-in users and opens the viewer;
- **Cassini Admin** appears for admins and opens the control panel;
- `/operator/status` reports both `secret_configured` and
  `signaling_internal_secret_configured` as `true`.

## 2. Record a meeting

**Manual:** create or join a Talk room, speak for 20–60 seconds, and use Talk's
**record** button. Because Talk's recording backend points at the installed
ExApp, the operator joins as the recorder and runs the pipeline.

**Automated (recommended for a first end-to-end check):**

Materialize the one known audible LFS fixture, then run a single recording:

```bash
git lfs pull \
  --include="harness/media/processed/showcase-lantern-festival-v1/mira.ivf,harness/media/processed/showcase-lantern-festival-v1/mira.ogg"

./harness/bin/validate-installed-exapp-private-talk.sh \
  --nextcloud-host 127.0.0.1 \
  --run-count 1 \
  --media-prefix "$PWD/harness/media/processed/showcase-lantern-festival-v1/mira" \
  --duration 30
```

This creates/reuses a private one-to-one conversation and triggers one Talk
recording through the installed ExApp. A pass requires one new succeeded job,
the `.opus` and catalog to exist directly in Nextcloud Files, viewer responses
identified as Files-backed, positive recorder segments, authenticated artifact
download, and a transcript that decodes to at least one word. This strict check
also catches accidentally testing a stale pre-feature image. There is no
recording retry. Omit `--run-count 1` to run the validator's two-record
archive-preservation mode.

For the exact-image, stack-owning CI equivalent, start from a clean stack and
provide the already-built image explicitly:

```bash
IMAGE_REF=cassini-exapp:local-faithful
docker build -f deployment/Dockerfile.exapp -t "$IMAGE_REF" .
IMAGE_REF="$IMAGE_REF" ./harness/bin/ci-e2e-installed-exapp-talk.sh
```

That command installs the exact image through AppAPI/HaRP, performs one
Talk-to-viewer run, writes machine-readable evidence, and always tears down its
owned containers, network, and volumes. It is Linux-only and requires native
Docker Engine/Compose, Docker socket access, Go, `git-lfs`, `jq`, `xmllint`,
`curl`, and Python 3. It does not require Kokoro or `uv` because it uses the
materialized Mira pair above.

## 3. Open the result in the viewer

Open **Cassini** inside Nextcloud. The published meeting shows its **Talk
conversation name** and **real recording date** — the operator resolves the
conversation name through the AppAPI-authenticated Talk API (which is why this
only works on the installed-ExApp path, not the standalone bundle).

## Why this is the default

The installed-ExApp path is the only local topology that faithfully exercises
the production AppAPI manifest/install boundary:

- The **AppAPI/HaRP proxy + manifest allow-list** is in the request path, so it
  catches env/allow-list bugs (e.g. D-403) that a direct operator container
  never sees.
- The **operator has AppAPI credentials**, so Talk conversation-name resolution
  actually runs — the standalone bundle can only show the "Untitled meeting" +
  date fallback.
- It drives **record -> build -> publish -> viewer** for real, so it catches
  publish-path regressions (e.g. the D-462 `export-static-meetings.mjs`
  temporal-dead-zone crash) that unit tests structurally cannot.

This is the same argument behind making the ExApp harness the default for CI
test runners — see Linear **D-453**.

## Teardown

Per local-dev hygiene, tear the stack down at the end of a run:

```bash
./bin/cassini dev stack down --volumes \
  --public-mode local-http \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp
```

Drop `--volumes` if you want to keep the Nextcloud state. The canonical stack
command also owns AppAPI-created ExApp resources such as `nc_app_gocassini`;
do not replace it with raw Compose teardown.

## Lighter path: individual services

If you only need the **viewer** or are iterating on a **single component**, you
can run the services directly instead of the full ExApp. This is faster but
does **not** exercise AppAPI/HaRP, Talk's record button, or conversation-name
resolution — treat it as a component-development convenience, not an end-to-end
check.

The two-stack individual flow (local Talk harness + the operator / control
panel / viewer deployment bundle) is documented in full here:

- [Running the local developer stack](./local-developer-stack.md)

Condensed:

```bash
# 1. Local Talk harness
./bin/cassini dev stack up
CALL_URL="$(./bin/cassini dev room create --name "Local demo" | tail -n1)"

# 2. Operator + control panel + viewer bundle
cd deployment && docker compose up --build
#   operator :4000  control panel :4173  viewer :8765
```

Then paste `CALL_URL` into the control panel to submit a job, watch it move
`record -> build -> publish -> done`, and refresh the viewer. For viewer-only
work, `cassini-viewer`'s own dev server (`npm run dev`) is lighter still.

## Where to go next

- The mental model behind the flow: [Mental model](./mental-model.md)
- The full local runtime layout: [Running the local developer stack](./local-developer-stack.md)
- Tiered ExApp-in-Nextcloud testing (image-only → AppAPI → full Talk):
  [Trying the ExApp image in a local Nextcloud](./exapp-test-locally.md)
- The stage-by-stage pipeline: [Core pipeline](./core-pipeline.md)
