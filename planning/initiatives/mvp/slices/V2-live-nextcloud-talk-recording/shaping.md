---
shaping: true
---

# V2 — Shaping

This document shapes **V2: Live Nextcloud Talk recording worker**.

It elaborates the slice already defined in:

- `planning/initiatives/mvp/shaping.md`
- `planning/initiatives/mvp/slices.md`
- `planning/initiatives/mvp/slices/V2-live-nextcloud-talk-recording/brief.md`

The goal here is to replace the V1 fixture record stage with real Nextcloud Talk capture while staying within the MVP slice boundary.

## Selected implementation shape

The selected V2 shape is now:

- keep the V1 operator trigger/status/job backbone
- keep build and publish exactly where they already are
- swap only the record stage from fixture materialization to live Talk capture
- execute live capture by invoking `cassini record` as a subprocess
- use the existing guest-first Talk join path for V2
- add a first-cut explicit stop endpoint that cleanly stops recording and still continues into build/publish
- persist stop metadata on operator job rows through versioned schema migrations rather than ad hoc schema edits
- rely on recorder-owned resilience first and defer broad operator-level retry/re-entry to V5

This is **Shape A**, now selected.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | An operator can trigger a real Nextcloud Talk recording through the existing job flow, and the captured meeting proceeds through build/publish into the hosted library without manual stitching. | Core goal |
| R1 | V2 must reuse the existing Cassini recording pipeline and artifact contracts instead of reimplementing live capture inside a new operator-specific recorder. | Must-have |
| R2 | V2 must stay narrowly scoped to Nextcloud Talk access and recording only; invite automation, calendar automation, and broader Nextcloud product work stay out. | Must-have |
| R3 | The recorder bot can join the target Talk room through a supported minimum access/auth path for the deployments we care about. For V2, that path is guest-first, with request-level guest name override and a sane default when omitted. | Must-have |
| R4 | Transient connection, signaling, or participant-churn issues must be handled with explicit bounded behavior. V2 should rely on recorder-owned resilience first and should not add broad operator-level retry/re-entry. | Must-have |
| R5 | The operator can control intentional meeting exit through a first-cut happy-path stop contract: room-empty behavior, hard duration, and an explicit API stop request that tells the recorder to stop cleanly and then continues through build/publish from the captured artifact. | Must-have |
| R6 | Job and artifact state remain sane and operator-readable across live capture and intentional stop behavior, including persisted operator job-row stop metadata that distinguishes why recording ended without inventing a separate terminal success state. | Must-have |
| R7 | V2 must introduce versioned SQLite schema migrations with explicit up/down files, migration history, and startup auto-up behavior so live-recording metadata changes do not depend on a hardcoded one-shot schema. | Must-have |
| R8 | The V2 slice remains compatible with the existing repo harness and the canonical manual validation path is user-driven: start the stack/operator, join the meeting in the browser, speak normally, stop with `POST /jobs/:id/stop`, and inspect state/artifacts manually. | Must-have |
| R9 | V2 should leave a clean extension path for V5 rerun/recovery work without trying to solve the whole recovery product now. | Must-have |

---

## CURRENT: V1 operator + existing live recorder baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `cassini-operator` already accepts `POST /jobs?provider=nextcloud-talk`, persists SQLite job rows, runs asynchronous workers, and exposes `GET /jobs` / `GET /jobs/:id`. | |
| **CURRENT2** | The operator build and publish stages already reuse the real Cassini CLI boundaries. | |
| **CURRENT3** | The operator record stage is still a V1 placeholder that materializes a `.run` bundle from a fixture `.mkv`. | |
| **CURRENT4** | `cassini record` already parses Talk call URLs, joins Talk as a participant, captures media, and emits a normal run artifact. | |
| **CURRENT5** | The current Talk recorder already supports `--duration`, `--stop-when-room-empty`, and `--room-empty-grace`. | |
| **CURRENT6** | The current Talk recorder already contains recorder-owned resilience mechanisms such as bounded hello retries, repeated `requestoffer` attempts, room-empty timer arm/disarm, and graceful stop on context cancellation / SIGTERM. | |
| **CURRENT7** | The current repo shows a guest-style Nextcloud join path via room checks, `participants/active`, guest-name setting, signaling settings fetch, and call join. | |
| **CURRENT8** | The operator does not yet have a per-job live record process/stop surface or an explicit happy-path stop endpoint that feeds build/publish. | |
| **CURRENT9** | The operator schema is still created through a hardcoded `CREATE TABLE IF NOT EXISTS` path rather than versioned migrations. | |
| **CURRENT10** | The harness already provides local stack-up, room creation, and E2E capture flows, but the canonical manual V2 validation path has not yet been written as a user-driven browser flow. | |

## A: Thin operator wrapper around `cassini record` — selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Keep the V1 job/control-plane shape and replace only the record stage implementation. | |
| **A2** | Extend the trigger request to require `platform` + `url` and support optional `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace`. Do not add `scheduledEnd` in V2. | |
| **A3** | Execute live capture by invoking `cassini doctor --target record` and then `cassini record --call <url> --out <job>.run --name <guestName>`, appending supported stop flags only when present. | |
| **A4** | Track the running record subprocess per job in operator runtime state so `POST /jobs/:id/stop` can act on the real process handle. | |
| **A5** | Add `POST /jobs/:id/stop`; it is valid only while `stage=record` and `state=running`, returns `202` when accepted, and is effectively idempotent for repeated calls against the same in-flight stop transition. | |
| **A6** | Implement stop by sending `SIGTERM` to the `cassini record` subprocess, waiting a bounded grace period, and only falling back to hard-kill if it fails to exit. A finalized `.run` after SIGTERM is a happy-path result and continues into build/publish. | |
| **A7** | Persist structured stop metadata (`stop_reason` plus stop timing / exit metadata as needed) on the operator job row. Do not mirror operator-owned stop state into recorder FS artifacts in V2. | |
| **A8** | Introduce versioned SQLite schema migrations with up/down files and a `schema_migrations` history table. Keep startup behavior to auto-apply pending up migrations only; down migrations are explicit test/manual actions. | |
| **A9** | Extract the current hardcoded V1 schema into the baseline SQL migration and bootstrap existing pre-migration DBs by recording that baseline version before migrating up as needed. | |
| **A10** | Rely on recorder-owned resilience first and keep V2 to one live attempt per job. Unrecovered live-capture failures are terminal and visible rather than retried by a second operator policy. | |
| **A11** | Keep the canonical manual validation path user-driven: local stack + operator + real browser participant + `POST /jobs` + optional `POST /jobs/:id/stop` + manual inspection. | |

## B: In-process operator-managed live recorder — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Move live recording logic directly into `cassini-operator` so the operator owns join, retry, and stop behavior in-process. | ⚠️ |
| **B2** | Build explicit operator-side lifecycle control around the live session rather than using the existing `cassini record` boundary. | ⚠️ |
| **B3** | Keep build and publish as they are after successful capture. | |

## C: Nextcloud account/bootstrap first — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Start V2 by shaping a dedicated Nextcloud bot-account/bootstrap story before selecting the live record execution boundary. | ⚠️ |
| **C2** | Make that identity/bootstrap mechanism part of the required V2 path for joining and recording meetings. | ⚠️ |
| **C3** | Then wire live capture through the operator after the auth/bootstrap model is in place. | ⚠️ |

---

## Why A is selected

Shape A preserves the repo's existing boundaries:

- operator remains orchestration
- `cassini record` remains the live recorder boundary
- build/publish stay unchanged
- the main new work is around request contract, explicit stop semantics, and stop metadata persistence

Shape B was rejected because it duplicates recorder responsibility inside the operator.

Shape C was rejected because it expands the slice before proving that the existing guest-first path is insufficient for the deployments V2 cares about.

---

## Fit Check — R × A

| Req | Requirement | Status | A |
|-----|-------------|--------|---|
| R0 | An operator can trigger a real Nextcloud Talk recording through the existing job flow, and the captured meeting proceeds through build/publish into the hosted library without manual stitching. | Core goal | ✅ |
| R1 | V2 must reuse the existing Cassini recording pipeline and artifact contracts instead of reimplementing live capture inside a new operator-specific recorder. | Must-have | ✅ |
| R2 | V2 must stay narrowly scoped to Nextcloud Talk access and recording only; invite automation, calendar automation, and broader Nextcloud product work stay out. | Must-have | ✅ |
| R3 | The recorder bot can join the target Talk room through a supported minimum access/auth path for the deployments we care about. For V2, that path is guest-first, with request-level guest name override and a sane default when omitted. | Must-have | ✅ |
| R4 | Transient connection, signaling, or participant-churn issues must be handled with explicit bounded behavior. V2 should rely on recorder-owned resilience first and should not add broad operator-level retry/re-entry. | Must-have | ✅ |
| R5 | The operator can control intentional meeting exit through a first-cut happy-path stop contract: room-empty behavior, hard duration, and an explicit API stop request that tells the recorder to stop cleanly and then continues through build/publish from the captured artifact. | Must-have | ✅ |
| R6 | Job and artifact state remain sane and operator-readable across live capture and intentional stop behavior, including persisted operator job-row stop metadata that distinguishes why recording ended without inventing a separate terminal success state. | Must-have | ✅ |
| R7 | V2 must introduce versioned SQLite schema migrations with explicit up/down files, migration history, and startup auto-up behavior so live-recording metadata changes do not depend on a hardcoded one-shot schema. | Must-have | ✅ |
| R8 | The V2 slice remains compatible with the existing repo harness and the canonical manual validation path is user-driven: start the stack/operator, join the meeting in the browser, speak normally, stop with `POST /jobs/:id/stop`, and inspect state/artifacts manually. | Must-have | ✅ |
| R9 | V2 should leave a clean extension path for V5 rerun/recovery work without trying to solve the whole recovery product now. | Must-have | ✅ |

**Notes:**
- A satisfies R4 by explicitly selecting recorder-owned resilience plus one live attempt per job, rather than a second operator retry system.
- A satisfies R7 only if stop metadata is shaped together with a real migration surface, not as an ad hoc schema edit.
- A satisfies R6 by keeping operator-owned stop state on the job row rather than mutating recorder artifacts from wrapper code, which matches current repo precedent.

## Detail A: concrete V2 mechanisms

| Part | Mechanism |
|------|-----------|
| **A2.1** | Trigger body: `platform`, `url`, optional `guestName`, optional `duration` (seconds), optional `stopWhenRoomEmpty` (bool), optional `roomEmptyGrace` (seconds). |
| **A2.2** | Trigger defaults: `guestName= CassiniRecorder`, `stopWhenRoomEmpty=true`, `roomEmptyGrace=30`, `duration` omitted means no fixed limit. |
| **A3.1** | Operator preflight runs `cassini doctor --target record` before starting the record subprocess. |
| **A3.2** | Record subprocess command: `cassini record --call <url> --out <job>.run --name <guestName>` plus supported stop flags only when present. |
| **A4.1** | Runtime keeps an in-memory map from job id to live record process handle, stop state, and stop timestamps. |
| **A5.1** | `POST /jobs/:id/stop` returns `404` for unknown jobs, `409` when the job is not currently stoppable, and `202` when the stop request is accepted or already in progress for that running record. |
| **A6.1** | On accepted stop: send `SIGTERM`, wait bounded grace, classify the result from exit status + finalized `.run`, then enqueue build/publish on successful finalization. |
| **A6.2** | If the process ignores SIGTERM and no valid finalized `.run` exists after fallback kill, the job fails honestly rather than pretending the stop succeeded. |
| **A7.1** | Operator job rows grow stop metadata fields so operator state can reflect `room_empty`, `duration_limit`, `operator_requested`, `join_failed`, `signaling_connection_error`, or `record_process_exit_nonzero` without log scraping. |
| **A7.2** | Recorder FS artifacts are not mutated by operator-owned stop metadata in V2; downstream wrappers continue to read recorder outputs and write separate downstream artifacts only. |
| **A8.1** | Operator DB schema uses numbered up/down migrations and a `schema_migrations` history table; startup auto-applies pending up migrations only. |
| **A9.1** | The current hardcoded V1 `jobs` schema becomes the baseline SQL migration, and existing pre-migration DBs are marked at that baseline before migrating up if needed. |
| **A10.1** | There is no operator retry slice in V2. Recorder resilience is in; broad operator retry/re-entry is deferred to V5. |
| **A11.1** | Canonical manual validation: stack up, operator start, create/open room, `POST /jobs`, speak in browser, optional `POST /jobs/:id/stop`, inspect `GET /jobs`, `GET /jobs/:id`, logs, `.run`, `.meeting`, and published site output. |

## Deferred from this V2 cut

- scheduled-end semantics beyond hard duration
- broad operator-level retry/re-entry / rerun / recovery
- dedicated Nextcloud account/bootstrap unless guest-first proves insufficient for a supported deployment
- player-automation as the canonical manual validation path
- a distinct terminal success state for intentionally stopped recordings

## Reassessment: where we are now

The shaping has moved past exploration.

We now have:

- a selected implementation shape
- a selected trigger contract
- a selected record subprocess boundary
- a selected explicit stop model
- a selected no-operator-retry policy for V2
- a selected schema migration direction for stop metadata
- a selected user-driven manual validation path

The remaining work is no longer shape discovery. It is implementation planning and cutline discipline.

## Next step

Use the selected Shape A to drive the internal V2 slice plan in:

- `planning/initiatives/mvp/slices/V2-live-nextcloud-talk-recording/slices.md`

The focused S3 persistence spike is now answered in:

- `planning/initiatives/mvp/slices/V2-live-nextcloud-talk-recording/spike-stop-metadata-and-migrations.md`

That spike does not reopen the V2 live-record shape. It now serves as the decision record for stop metadata persistence and migration policy while implementation planning finishes.
