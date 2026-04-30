---
shaping: true
---

# V2 Brief — Live Nextcloud Talk recording

## What this slice is

V2 turns the V1 operator backbone into a real meeting-capture flow.

V1 proved that Cassini can:

- accept a remote trigger request
- persist a durable job row
- run work asynchronously
- build and publish artifacts into the hosted library

But V1 still fakes the recording step with a fixture MKV.

V2 replaces that placeholder with the real Nextcloud Talk capture path.

The outcome we want is simple:

- an operator sends the same kind of job request
- the worker joins a real Nextcloud Talk room as the recorder bot
- the meeting is captured through the existing Cassini recording flow
- the normal build/publish pipeline runs afterward
- the resulting meeting appears in the library without manual stitching

## Selected implementation direction

The selected V2 direction is now:

- keep the V1 job/control-plane backbone intact
- keep build and publish where they already are
- replace only the record stage with live `cassini record` execution
- use the existing guest-first Talk join path for V2
- add a first-cut explicit stop endpoint that cleanly stops recording and still continues into build/publish
- persist stop metadata on operator job rows through a versioned operator schema with explicit migrations
- keep broad operator-level retry/re-entry out of V2; rely on recorder-owned resilience first

## Why this matters now

This is the first slice that proves the core product promise rather than only the control plane around it.

Until V2 exists, Cassini can demonstrate:

- job orchestration
- artifact processing
- publication

But it cannot yet demonstrate the thing a pilot operator actually needs:

- "give Cassini a real Talk meeting and have it record that meeting for me"

That is the gap this slice closes.

## The problem we are solving

We already have two strong halves of the system:

1. **Operator control flow** in `cassini-operator`
   - trigger job
   - persist job state
   - run background stages
   - publish results

2. **Real Talk recording capability** in `cassini record`
   - parse a Talk call URL
   - join the room
   - negotiate signaling/WebRTC
   - auto-stop on room-empty or duration
   - emit a normal Cassini recording artifact

What we do **not** yet have is a clean production slice that connects those halves with the right operational behavior.

In practice, V2 has to answer:

- how the bot joins a Talk room in the deployments we care about
- how the operator invokes live capture through the existing job flow
- how the worker behaves when signaling or connectivity is flaky
- how the operator intentionally stops a recording and still gets build/publish
- how stop metadata is persisted without ad hoc schema drift
- how to validate all of that using the existing local harness plus a user-driven browser path

## In scope

This slice stays narrowly focused on making the operator-backed live recording flow real.

### 1. Real Nextcloud Talk recording from the worker

- replace the V1 fixture-based record stage with the real Talk capture path
- keep the V1 job/control-plane backbone intact
- preserve the existing build → publish stages after successful capture

### 2. Minimum Nextcloud access/auth behavior required to join and record

- use a guest-first join/auth path for V2
- support request-level guest name override with a sane default when omitted
- if some minimum identity/bootstrap step is later found to be required for a deployment shape we care about, keep scope to only that minimum recording-related behavior

### 3. Explicit record-stage subprocess boundary

- execute live capture by invoking `cassini record` from the operator
- keep output under the operator work root as one canonical `<job>.run`
- run operator-owned preflight before record rather than moving record logic in-process

### 4. Happy-path stop behavior

- support the first-cut stop controls we want the operator to use intentionally:
  - room-empty exit after a grace/buffer period
  - hard duration exit
  - explicit operator-requested stop while recording is running
- when the operator explicitly stops a running job through the API, that is still a **happy-path** completion mode for V2:
  - instruct the recorder to stop cleanly
  - preserve the captured artifact if finalization succeeds
  - continue with build and publish

### 5. Operator-facing stop surface

- add `POST /jobs/:id/stop`
- make the stop request feed the normal downstream flow rather than short-circuiting the job
- keep the final job state honest: successful stop still ends as `succeeded`, with the distinction captured in stop metadata

### 6. Stop metadata and schema evolution

- persist structured stop metadata needed for observability and later V5 work on the operator job row
- introduce versioned SQLite schema migrations with explicit up/down files
- treat the existing hardcoded schema as the initial baseline and extract it into SQL files
- keep startup behavior to auto-apply pending up migrations only

### 7. Harness-backed manual end-to-end validation path

- use the existing repo harness for stack bring-up and operator startup
- treat the canonical manual path as **user-driven**, not agent-driven:
  - start the stack
  - start the operator
  - create or open a real Talk room
  - `POST /jobs`
  - have the user join the room in the browser and speak normally
  - stop with `POST /jobs/:id/stop`
  - inspect `GET /jobs`, `GET /jobs/:id`, logs, and output artifacts manually
- no player automation is required for this canonical manual path

## Explicitly out of scope

These stay out of V2 even if they are adjacent.

- Nextcloud invite automation
- calendar integration
- replacing the manual `POST /jobs` trigger flow
- a full operator UI/dashboard
- broad operator-level retry/re-entry or rerun/recovery depth beyond what V2 needs
- non-happy-path stop/failure handling beyond the clean stop → build → publish flow
- scheduled-end semantics beyond the current hard-duration control
- summary generation
- packaging / pilot quickstart work
- non-Nextcloud meeting platforms
- broad Nextcloud user-management/productization work

More specifically:

- if a dedicated Nextcloud user turns out to be required on some deployments, V2 may shape the **minimum recording-related bootstrap/use** of that identity
- V2 does **not** expand into a general user lifecycle feature
- V2 does **not** add a second recorder implementation inside `cassini-operator`

## Repo signal that already helps

The existing repo already answers part of the problem.

### `cassini record` already contains useful live-capture behavior

The current recorder already has:

- Talk URL parsing
- OCS room checks
- participant-active join flow
- guest-name setting
- signaling settings fetch
- signaling hello retries
- repeated `requestoffer` attempts with a bounded attempt counter
- room-empty stop behavior
- hard-duration stop behavior
- graceful stop on context cancellation / SIGTERM

That means V2 should start by **reusing** the existing recorder path rather than redesigning live capture from scratch.

### The operator already has the right high-level backbone

The current operator already has:

- accepted job persistence in SQLite
- stage-separated worker flow
- build queueing
- sequential publish
- stable trigger/status endpoints

That means V2 should mostly change the **record stage boundary and its live semantics**, not the whole operator model.

### The harness already gives us a local Talk lab

The repo already has:

- `harness/bin/up.sh`
- `harness/bin/create-room.sh`
- `harness/bin/stream-*.sh`
- `harness/bin/ci-e2e.sh`
- `cassini-go-recorder/e2e_with_publisher.sh`

So V2 should learn from and reuse those flows rather than inventing a second manual test environment.

## Main shaping answers for V2

1. **Join/auth path**
   - V2 is guest-first.
   - Request body supports `guestName`; omit any broader identity story unless the guest path proves insufficient for a supported deployment.

2. **Record-stage execution boundary**
   - The operator runs `cassini record` as a subprocess.
   - V2 keeps this boundary rather than moving recording logic in-process.

3. **Retry/re-entry policy**
   - V2 relies on recorder-owned resilience first.
   - V2 does **not** add broad operator-level retry/re-entry; one live attempt per job is the selected default.

4. **Stop contract**
   - Trigger-time controls: `duration`, `stopWhenRoomEmpty`, `roomEmptyGrace`, plus optional `guestName`.
   - Explicit stop surface: `POST /jobs/:id/stop`.
   - Stop mechanism: send `SIGTERM`, wait a bounded grace period, and continue if the `.run` finalizes successfully.

5. **Stop metadata / persistence**
   - Persist stop metadata such as `stop_reason` and stop timing on the operator job row.
   - Add versioned up/down schema migrations rather than continuing with a hardcoded one-shot schema.
   - Keep operator-owned stop state out of recorder FS artifacts in V2 unless a future repo precedent emerges for wrapper-owned recorder-artifact mutation.

## What “done” should look like

A reviewer should be able to:

1. start the local Talk stack with the harness
2. start the operator
3. create or open a real Nextcloud Talk room and obtain its URL
4. send a V2 operator trigger request with that real Talk URL
5. join the meeting in the browser and speak normally into the mic
6. optionally stop the recording through `POST /jobs/:id/stop`
7. see the worker run real live capture rather than fixture materialization
8. confirm the resulting meeting lands in the normal build/publish path
9. inspect job state, stop metadata, and output artifacts honestly
10. open the newly published meeting in the viewer library

## Likely code areas

Based on the current repo shape, the most likely touch points are:

- `cassini-operator/internal/operator/run.go`
- new or refactored operator live-record runtime code under `cassini-operator/internal/operator`
- operator job model / status transitions
- operator schema/migration plumbing
- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/internal/cassini/run_bundle.go`
- `cassini-go-recorder/internal/config/config.go`
- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `harness/bin/up.sh`
- `harness/bin/create-room.sh`
- Nextcloud web/manual validation notes under the V2 slice docs

## Guardrails for whoever picks this up

Good V2 behavior is:

- keep the V1 trigger/status backbone stable
- reuse the existing Cassini recording pipeline
- make stop behavior explicit and bounded
- persist stop metadata through a real migration path
- keep the slice focused on operator-backed live Talk capture
- learn from the existing harness and repo wiring before introducing new machinery

Bad V2 behavior is:

- turning the slice into Nextcloud invite automation
- turning it into a general Nextcloud app effort
- redesigning the whole operator architecture before proving live capture
- over-solving V5 reruns/recovery right now
- introducing an ad hoc schema change path without explicit migrations

## Where this leaves the shaping process

The spike answers are now strong enough to:

- select the thin operator wrapper shape
- cut concrete internal implementation slices
- treat retry/re-entry beyond recorder-owned behavior as deferred
- move next into concrete implementation planning rather than more shape exploration
