# Cassini Live diagnostics page — run it in the harness, ship it to staging

The diagnostics page is an admin-only web page served by the
[cassini-live](https://github.com/codemyriad/cassini-live) ExApp (submodule
`cassini-live/`) through the AppAPI proxy. It shows what the in-call diagnostic
video shows: caption status, voices heard, seats, caption lag, two minutes of
speaker lanes, the lag graph, the governor's last decision, memory and CPU
budgets, and the five steps of the caption pipeline. It shows both views at
once, updated every second, and you don't have to be in the call. D-886 is the
effort; the product-side description is `cassini-live/docs/diagnostics-page.md`.

The page polls one admin endpoint. The app samples its own state only while
someone polls: 30 seconds after the last poll it stops and drops its
two-minute history. The diagnostic video, which `CASSINI_LIVE_DEBUG_VIDEO`
still enables, keeps its own sampler and is unchanged.

```
 admin browser                              cassini-live ExApp
 ┌───────────────────────────┐   GET 1/s   ┌──────────────────────────────────┐
 │ …/diagnostics/            │────────────▶│ /diagnostics/api/feed  (ADMIN)   │
 │  status · hero · lanes    │◀────────────│   membership check (Talk, as you)│
 │  lag graph · pipeline     │  sample +   │   DiagnosticFeed ── its own      │
 └───────────────────────────┘  columns    │   sampler, 120 one-second columns│
                                           │   (only while polled < 30 s ago) │
                                           │ DiagnosticVideo (opt-in, as before)│
                                           └──────────────────────────────────┘
```

Speaker names are scoped at least as strictly as the video scopes them. The
feed samples one room: the video's room while the video is publishing,
otherwise the oldest room with captions on. You see that room's speakers'
names only if you are a participant of that Talk conversation (you don't need
to have joined the call). The app checks this with Talk, acting as you.
Everyone else sees "Participant", and lanes from other calls read "other
call". Talk participant lists are read only while such a participant has the
page open, or while the video runs.

## Run it in the local harness

```bash
git submodule update --init cassini-live

# 1. Nextcloud + Talk + HPB + AppAPI
./bin/cassini dev stack up --services full --cassini none --recording-backend none

# 2. Build, register and start cassini-live (idempotent); it now also checks
#    that the diagnostics page answers through the proxy
./harness/bin/live-panel-setup.sh

# 3. Make a room
./bin/cassini dev room create --name 'Live diagnostics test'
```

Then, in a browser, as admin/admin at **http://localhost:28080/**. Use
`localhost`, not `127.0.0.1`; see `docs/live-transcript-panel.md`.

1. Open `http://localhost:28080/index.php/apps/app_api/proxy/live_transcription/diagnostics/`.
   With no call it reads "No captions running" and still shows the engine and
   the memory and CPU budgets.
2. In another window, join the room's call and turn captions on (CC). Within a
   second the page goes to "Captions live". Speak and watch your lane fill.
3. Open the page as a different admin who is not a participant of that
   conversation. The lanes read "Participant" instead of names.

The page is also linked from the cassini-live settings page (Administration
settings > Assistant > the "?" next to Cassini Live > "Live diagnostics").

UI-only iteration without a stack: `cd cassini-live/ui && npm ci && npm test`.
That runs the unit tests, plus Playwright against a fixture server that serves
the page with its real CSP and a scripted feed. On NixOS, run Playwright inside
`mcr.microsoft.com/playwright:v1.64.1-noble`.

## Deploy to staging

This is the same flow as the transcript panel (`docs/live-transcript-panel.md`),
at version **0.11.0**, built from the cassini-live branch
`feat/d-886-diagnostics-page`. The new routes are in `appinfo/info.xml`, so
re-register the app to make AppAPI read them:

```bash
# on the staging Docker host
cd cassini-live && git fetch && git checkout feat/d-886-diagnostics-page
docker build -t ghcr.io/codemyriad/cassini-live:0.11.0 .

# on the Nextcloud server (occ)
occ app_api:app:unregister live_transcription --force
occ app_api:app:register live_transcription DAEMON_NAME \
    --info-xml /path/on/nextcloud/cassini-live/appinfo/info.xml \
    --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET=YOUR_HPB_INTERNAL_SECRET \
    --wait-finish
docker update --cpus=4 --memory=2g --memory-swap=2g --pids-limit=128 nc_app_live_transcription
```

Checks:

- As an admin: `GET <staging>/index.php/apps/app_api/proxy/live_transcription/diagnostics/`
  should return the page. For a non-admin user AppAPI refuses the request
  (in the harness it answers 404, as for any route the user may not reach).
- `GET …/live_transcription/diagnostics/api/feed` as an admin should return
  JSON with a `sample`, and `"names": false` unless you are a participant of
  the sampled conversation.
- The video option `CASSINI_LIVE_DEBUG_VIDEO` is independent. Leave it as
  staging has it.
