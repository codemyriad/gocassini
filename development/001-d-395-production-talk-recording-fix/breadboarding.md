---
shaping: true
---

# D-395 — Breadboard

## UI affordances

| ID | Place | Affordance | Wires out |
|---|---|---|---|
| **UI1** | Nextcloud AppAPI / Apps UI | Deploy options for Cassini ExApp, including Talk recording secret and HPB internal signaling secret. | N1, N2 |
| **UI2** | Nextcloud navigation | `Cassini` user entry opens embedded viewer; `Cassini Admin` admin entry opens embedded control panel. | N5, N6 |
| **UI3** | Cassini Admin / control panel | Admin sees jobs progress from record → build → publish. | N5, N8 |
| **UI4** | Nextcloud Talk 1:1 conversation | Admin starts/observes Talk recording for admin + Erlich Bachman private call. | N4, N7 |
| **UI5** | Cassini viewer | User/admin sees published meeting and transcript after processing. | N6, N9 |
| **UI6** | Documentation / tutorial | Operator follows local validation and production deployment steps. | N10 |

## Non-UI affordances

| ID | Place | Affordance | Wires out |
|---|---|---|---|
| **N1** | `appinfo/info.xml` | Declared ExApp env var `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. | N2 |
| **N2** | AppAPI install/register | AppAPI passes declared Talk env vars into `nc_app_gocassini`. | N3 |
| **N3** | ExApp operator runtime | Runtime receives `CASSINI_TALK_RECORDING_SECRET`, `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, `NEXTCLOUD_URL`, and optional `CASSINI_TALK_BACKEND_URL`. | N4, N5, N8 |
| **N4** | Talk recording backend routes | `PUBLIC` `/api/v1/welcome` and `/api/v1/room/<token>` accept Talk HMAC requests through AppAPI proxy. | N7 |
| **N5** | `/operator/status` and `/operator/jobs` | Admin API reports config presence and job lifecycle through AppAPI proxy. | UI3, N8 |
| **N6** | Embedded viewer/control-panel assets | Operator-served `/ui/*.js`, `/viewer/*`, `/control-panel/*`, `/published/*` behind AppAPI routes. | UI2, UI5 |
| **N7** | Recorder HPB-internal path | `cassini record` joins Talk using signaling internal auth and captures private/1:1 media. | N8 |
| **N8** | Operator pipeline | Record → promote run → Talk upload/callback → build transcript → publish site. | N9 |
| **N9** | AppAPI persistent storage | DB/work/current meeting bundles and published catalog survive container recreate and preserve previous meetings. | UI5 |
| **N10** | Harness scripts | Installed-ExApp setup and private validation scripts run inside `dev-vm` via `multipass exec`. | UI1, UI4, UI5 |

## Wiring diagram

```mermaid
flowchart LR
  subgraph Host[Host repo]
    H1[Edit source files]
  end

  subgraph VM[dev-vm]
    V1[/home/ubuntu/dev/workspace mount]
    V2[harness/bin/manual-test-setup.sh installs ExApp by default]
    V3[private validation helper]
  end

  subgraph Nextcloud[Local Nextcloud + AppAPI + HaRP]
    NC1[AppAPI deploy options]
    NC2[Installed ExApp container]
    NC3[AppAPI proxy routes]
    NC4[Talk app / 1:1 call]
  end

  subgraph Cassini[Cassini ExApp runtime]
    C1[operator /api/v1/room]
    C2[/operator/status + /operator/jobs]
    C3[recorder hpb-internal]
    C4[build + publish]
    C5[/viewer + /published]
    C6[APP_PERSISTENT_STORAGE]
  end

  subgraph Browser[Browser validation]
    B1[Nextcloud app menu]
    B2[Cassini Admin control panel]
    B3[Cassini viewer transcript]
  end

  H1 --> V1
  V1 --> V2
  V2 --> NC1
  NC1 --> NC2
  NC2 --> C1
  NC2 --> C2
  NC2 --> C5
  NC3 --> C1
  NC3 --> C2
  NC3 --> C5
  NC4 -- Talk HMAC recording backend --> NC3
  C1 --> C3
  C3 --> C4
  C4 --> C6
  C6 --> C5
  V3 -- scaffold/play private --> NC4
  V3 -- poll admin API --> NC3
  B1 --> NC3
  B2 --> C2
  B3 --> C5
```

## Validation breadboard

| Step | Place | Action | Expected evidence |
|---|---|---|---|
| **V1** | VM harness | Start full-profile local Nextcloud/AppAPI/HaRP/Talk; Cassini ExApp installs by default with required env vars. | `occ app_api:app:list` shows `gocassini`; `/api/v1/welcome` through proxy returns `{"version":1}`. |
| **V2** | Browser | Open `http://<vm-ip>:28080/` and log in as admin. | Nextcloud menu contains Cassini and Cassini Admin. |
| **V3** | Browser/admin route | Open Cassini Admin. | Control panel loads through installed ExApp route. |
| **V4** | Browser/user route | Open Cassini viewer. | Viewer loads through installed ExApp route; catalog is reachable even if empty. |
| **V5** | VM validation script | Scaffold private 1:1 users/conversation and run admin + Erlich playback. | Talk OCS recording starts and operator accepts a job. |
| **V6** | AppAPI proxied operator API | Poll latest job. | Job reaches `done/succeeded`; logs show record/build/publish complete. |
| **V7** | Viewer/published catalog | Fetch catalog/transcript through AppAPI proxy. | New meeting transcript contains generated speech / non-empty segments. |
| **V8** | Archive preservation | Run a second separate private recording job and compare catalog IDs after both publishes. | Previous IDs remain; both new job IDs/meetings remain; catalog count does not collapse. |
