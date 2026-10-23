# Cassini Live transcript panel — run it in the harness, ship it to staging

The transcript panel is a browser UI over the live captions the
[cassini-live](https://github.com/codemyriad/cassini-live) ExApp produces
(submodule `cassini-live/`, code in `cassini-live/ui/`). It reaches the Talk
page as a **bookmarklet** — no Nextcloud app or PHP changes — and is served by
the cassini-live ExApp itself through the AppAPI proxy. D-830 is the parent
effort; the UI spec is Chris's "Cassini Live Captions UI" artifact.

The panel mounts as an extra **tab in Talk's right sidebar** (next to Chat /
Participants / Shared items) and hides Talk's native caption overlay while it
is on — the sidebar replaces it.

```
                   Talk call page (browser)
                   ├── Talk's own 4-line overlay  ✕ hidden while the tab is on
                   └── bookmarklet ──▶ "Live transcript" sidebar tab (shadow DOM)
                                          ▲ 'transcript' events
                   OCA.Talk.SimpleWebRTC ─┘   (every caption, pre-filtering)
                          ▲ signalling WebSocket
 speakers ──WebRTC──▶ HPB ◀──HPB internal client── cassini-live (VAD + Whistle)
                                                        ▲ POST /api/v1/call/transcribe
                   Talk server (captions enabled per session via the CC menu)
```

Two bookmarklet flavours, both picked up on the install page the ExApp serves
(`…/apps/app_api/proxy/live_transcription/panel/`):

- **loader** (default): tiny; injects the ExApp-served bundle with the page's
  CSP nonce. Updates with every ExApp redeploy.
- **standalone**: the whole panel in one `javascript:` URL; survives the ExApp
  being unreachable, frozen at the copied version.

## Run it in the local harness

```bash
git submodule update --init cassini-live

# 1. Nextcloud + Talk + HPB + AppAPI (no gocassini install needed)
./bin/cassini dev stack up --services full --cassini none --recording-backend none

# 2. Build, register and start cassini-live with the panel (idempotent)
./harness/bin/live-panel-setup.sh

# 3. Make a room
./bin/cassini dev room create --name 'Live panel test'
```

Then in a browser:

1. Open **http://localhost:28080/** (admin/admin). Use `localhost`, not
   `127.0.0.1` — the HPB keys rooms by backend URL and the ExApp joins via
   `overwrite.cli.url` (`http://localhost:28080`); a browser on a different
   host string lands in a different room instance and captions never arrive.
2. Open `http://localhost:28080/index.php/apps/app_api/proxy/live_transcription/panel/`
   and drag the bookmarklet to your bookmarks bar.
3. Open the room, join the call, enable **closed captions** (CC in the call
   menu), click the bookmarklet, and talk.

Headless proof of the same flow (speaks the JFK clip through a fake
microphone, asserts captions render as own lines):
`cassini-live/ui/tests/harness-e2e.mjs` — run instructions in its header.

UI-only iteration without any stack: `cd cassini-live/ui && npm ci && npm test`
(unit + Playwright against a CSP-faithful fake Talk page), and
`npm run serve:fixture` for manual poking. On NixOS run Playwright inside
`mcr.microsoft.com/playwright:v1.64.1-noble` (see `cassini-live/ui/README.md`).

## Deploy to staging

Staging already runs AppAPI with a HaRP daemon, so this is the cassini-live
install flow from `cassini-live/docs/install.md`, at version 0.9.0 (the panel
routes ride in `appinfo/info.xml`, so the version bump makes AppAPI re-read
them):

```bash
# on the staging Docker host
git clone git@github.com:codemyriad/cassini-live && cd cassini-live
git checkout feat/d-830-live-ui
docker build -t ghcr.io/codemyriad/cassini-live:0.9.0 .

# on the Nextcloud server (occ), once per daemon if GHCR pulls are not wanted:
occ app_api:daemon:registry:add --registry-from=ghcr.io --registry-to=local DAEMON_NAME

# if the official live_transcription ExApp (or an older cassini-live) is installed:
occ app_api:app:unregister live_transcription --force

occ app_api:app:register live_transcription DAEMON_NAME \
    --info-xml /path/on/nextcloud/cassini-live/appinfo/info.xml \
    --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET=YOUR_HPB_INTERNAL_SECRET \
    --wait-finish

# enforced limits BEFORE enabling captions (engine refuses to load without them)
docker update --cpus=4 --memory=2g --memory-swap=2g --pids-limit=128 nc_app_live_transcription
docker exec nc_app_live_transcription python -m cassini_live.capacity --installed
```

Checks and caveats:

- `GET <staging>/index.php/apps/app_api/proxy/live_transcription/panel/` as a
  logged-in user → the install page. Share that URL with the team; each person
  drags the loader bookmarklet once.
- `GET …/live_transcription/status` → `"signallingConfigured": true`.
- The backend URL cassini-live presents to the HPB comes from Nextcloud's
  `overwrite.cli.url`; it must be in the HPB's backend list and must be the
  same URL people use in the browser (on a proper staging domain it is).
- Captions are per-session: every viewer enables CC themselves; the panel then
  shows everything, including their own lines.

## What this is not (yet)

No caption history (late joiners see only what arrives after they enable CC
and open the panel), no pop-out page, no timestamps from the backend (the
panel stamps arrival time). Named users resolve from the signalling roster; a
guest with no roster display name and no participant card falls back to a
shortened session id. Those are the next slices in D-830 (history route + SSE
stream in cassini-live).
