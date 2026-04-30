---
shaping: true
---

# V2 Spike: Nextcloud join/auth, live record execution, retry/re-entry, and stop semantics

### Context

V1 already gives us the operator backbone:

- `POST /jobs?provider=nextcloud-talk`
- persisted SQLite job rows
- asynchronous staged worker flow
- build/publish via the existing Cassini CLI

What V1 does **not** give us is real Talk capture. The record stage still materializes a fixture `.mkv` into a `.run` bundle.

The repo also already contains a real live recorder path:

- `cassini record`
- `internal/talk/recorder.go`
- Nextcloud OCS room/session calls
- signaling hello retries
- repeated `requestoffer` attempts
- room-empty and duration-based stop behavior

V2 needs to connect those two halves honestly, without silently expanding scope into invites, full user management, or deep rerun/recovery work.

### Goal

Describe the cleanest V2 implementation path for live Nextcloud Talk recording in the operator, including:

- the supported Nextcloud join/auth path
- the record-stage execution boundary
- the bounded retry/re-entry model
- the happy-path stop contract
- the harness-backed manual validation flow

### Outcome

This spike is now complete enough to select the V2 shape.

Selected for V2:

- **Join/auth path:** guest-first
- **Guest identity:** request body grows a `guestName` parameter with a sane default
- **Record-stage boundary:** `cassini-operator` executes `cassini record` as a subprocess, mirroring the existing build/publish CLI wrapping pattern
- **Retry/re-entry policy:** rely on recorder-owned resilience first; no broad operator-level retry/re-entry in V2
- **Explicit stop surface:** `POST /jobs/:id/stop`
- **Stop mechanism:** `SIGTERM` the recorder subprocess, wait bounded grace, continue if finalization succeeds
- **Persistence direction:** structured stop metadata on operator job rows plus versioned up/down schema migrations
- **Validation flow:** user-driven manual browser path, not player automation

## Answered questions

| # | Decision | Answer |
|---|----------|--------|
| **V2-S1** | Accept suggestion | `POST /jobs?provider=nextcloud-talk` body requires `platform` + `url`; supports optional `guestName`, `duration`, `stopWhenRoomEmpty`, `roomEmptyGrace`; leave `scheduledEnd` out of V2. |
| **V2-S2** | Accept suggestion | Run `cassini record --call <url> --out <job>.run --name <guestName>` and append supported stop flags only when present; keep output under the operator work root; keep preflight as an operator-run doctor step before record. |
| **V2-S3** | Accept suggestion | Treat recorder-owned resilience as: hello retries, repeated `requestoffer`, room-empty timer arm/disarm, graceful stop on context cancel / SIGTERM. Treat operator-owned resilience as only failures outside that boundary. |
| **V2-S4** | Accept suggestion | Smallest safe V2 default: **no operator-level retry yet**; one live attempt per job; failures are terminal and visible; broader retry/re-entry belongs to V5 unless one very narrow must-have case appears later. |
| **V2-S5** | Accept suggestion | Keep one canonical `<job>.run` path; partial artifacts may exist but only proceed when the run bundle is finalized/usable; richer attempt accounting stays out of V2. |
| **V2-S6** | Accept suggestion | Add `POST /jobs/:id/stop`; valid only while `stage=record` and `state=running`; return `202` when accepted; make it effectively idempotent for repeated calls against the same running stop transition. |
| **V2-S7** | Modify to selected canonical model | API-level canonical stop model is `POST /jobs/:id/stop`. Worker-level canonical mechanism is: send `SIGTERM` to the `cassini record` subprocess, wait a bounded grace period, and only hard-kill if needed. A recorder that finalizes successfully after SIGTERM is treated as a happy-path exit and may continue into build/publish. |
| **V2-S8** | Modify to include schema evolution | Persist structured stop metadata and introduce a versioned SQLite migration system with explicit up/down migrations, migration history, and startup auto-up behavior. Extra operator DB version/status/migrate commands are not required for the current V2 scope. |
| **V2-S9** | Accept suggestion | Final job state still becomes `succeeded`; the distinction lives in persisted stop-reason metadata such as `operator_requested` rather than a separate terminal state. |
| **V2-S10** | Modify to canonical manual path | Manual validation is user-driven: start the local stack and operator, create or open a real Talk room, `POST /jobs`, have the user join the room in the browser and speak normally, stop with `POST /jobs/:id/stop`, then inspect `GET /jobs`, `GET /jobs/:id`, logs, and output artifacts manually. No player automation is required for the canonical manual path. |

## Selected local manual flow

1. `cassini dev stack up`
2. `cassini operator start`
3. create or open a Talk room and obtain its URL
4. `POST /jobs?provider=nextcloud-talk`
5. join the meeting in the browser and speak normally into the mic
6. optional `POST /jobs/:id/stop`
7. inspect manually:
   - `GET /jobs`
   - `GET /jobs/:id`
   - operator logs
   - `.run` artifact
   - meeting/site outputs

## Deferred to V5 or later

- broad operator-level retry/re-entry / rerun policy
- scheduled-end semantics beyond current hard-duration support
- a dedicated Nextcloud account/bootstrap story unless guest-first proves insufficient for a supported deployment
- a separate terminal success state for intentionally stopped recordings
- player-automation as the canonical manual validation path

## Acceptance

This spike is complete because:

- each V2-S question now has an accepted or modified answer
- the guest-first join/auth path is selected for V2
- the `cassini record` subprocess boundary is selected
- the exact V2 trigger fields and defaults are selected
- recorder-owned resilience vs operator-owned policy is clearly separated
- the first bounded V2 retry/re-entry policy is selected: none beyond recorder-owned behavior
- job/artifact handling around stop is selected
- the first-cut happy-path stop contract is selected
- the stop-reason persistence direction is selected
- the local manual validation path is selected
- deferred concerns are clearly named rather than left implicit

## Reassessment

The spike no longer points at more exploratory work.

It now serves as the decision record that justifies:

- selecting Shape A in `shaping.md`
- cutting concrete implementation slices in `slices.md`
- moving next to implementation planning
