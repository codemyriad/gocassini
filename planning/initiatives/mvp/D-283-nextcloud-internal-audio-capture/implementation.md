---
shaping: true
---

# D-283 — Implementation handoff

## Status

**D-283 is implemented for MVP.**

- **I1–I3** are complete product-path implementation.
- **I4** was intentionally finished as **minimal validation/handoff work**, not as another recorder/operator feature slice.
- Final validation on a known-good HPB harness is still required, but that is now a **deployment/runtime proof task**, not a missing product-shape task.

Related artifacts:

- research: `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/nextcloud_record_auth_gpt.md`
- shaping: `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/shaping.md`
- breadboard: `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/breadboarding.md`
- slices: `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/slices.md`
- validation handoff: `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/validation.md`

Implementation commits:

- `f74c3ea` — slice 1
- `3574953` — slice 2
- `fdafc59` — slice 3
- `3a886df` — slice 4

## What upstream research established

The upstream research doc established that the official Nextcloud recorder does **not** join as a normal participant.
It joins as a **trusted internal service** using two secrets:

1. **Nextcloud recording secret** for signed recording-auth requests to Nextcloud.
2. **Standalone signaling `internalsecret`** for internal-client signaling auth.

That leads to the core D-283 implementation direction:

- keep Cassini's existing per-participant subscriber / `requestoffer` / `OnTrack` / remux pipeline
- replace only the **bootstrap/auth path** when using Nextcloud internal capture
- join the signaling room **without** a Nextcloud participant session id
- skip guest-specific Nextcloud lifecycle calls in internal mode

## What was actually implemented

## I1 — Mode/config/target foundation

Implemented:

- added cross-surface `talkAuthMode`
  - `hpb-internal`
  - `guest-participant`
- added native Talk target inputs
  - `talkBaseURL`
  - `talkRoomToken`
  - optional `callURL` provenance
- kept `--call` as the direct CLI convenience path
- added process-scoped internal-mode secret resolution
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
- added fail-closed internal-mode validation before guest lifecycle calls start

Main code paths:

- `cassini-go-recorder/internal/config/config.go`
- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/record_runtime.go`

## I2 — Internal HPB bootstrap on the existing capture path

Implemented:

- signed recording-auth signaling settings fetch
- internal signaling `hello`
- internal `incall`
- room join without `sessionid`
- reuse of the existing subscriber / `requestoffer` / `OnTrack` / session-artifact / remux path
- skip of guest-only OCS lifecycle calls in internal mode

Main code paths:

- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `cassini-go-recorder/internal/nextcloud/recording_auth.go`
- `cassini-go-recorder/internal/talk/internal_auth.go`
- `cassini-go-recorder/internal/talk/recorder.go`

## I3 — Default flip + Talk-backend integration + honest failure surfacing

Implemented:

- `hpb-internal` is now the default for:
  - `cassini record`
  - generic operator Nextcloud jobs
- Talk recording-backend-triggered starts force `hpb-internal`
- guest mode remains available only by explicit selection
- internal-mode failures are surfaced clearly instead of silently falling back
- operator stop classification now treats internal bootstrap/auth/topology failures as `join_failed`

Main code paths:

- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/talk_backend.go`

## I4 — Minimal validation/handoff work

Implemented:

- added harness signaling `internalsecret` wiring
- added deployment env for the recorder-side internal secret
- fixed harness bootstrap behavior for Talk signaling setup
- documented the proof flow and focused debug checklist

Main files:

- `harness/config/signaling.conf`
- `harness/bin/common.sh`
- `harness/bin/bootstrap.sh`
- `harness/README.md`
- `deployment/.env`
- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/validation.md`

## What changed by subsystem

## Recorder

Behavior change:

- recorder bootstrap is now **dual-mode**
- internal mode uses:
  - recording-auth signaling settings fetch
  - internal signaling auth
  - internal `incall`
  - room join without `sessionid`
- guest mode remains in place as explicit fallback

Not changed:

- downstream capture architecture
- per-participant session artifacts
- subscriber / `requestoffer` / `offer` / `answer` / `OnTrack` path
- remux composition model

## Operator

Behavior change:

- normalized request contract now carries `talkAuthMode`
- operator defaults generic Nextcloud jobs to `hpb-internal`
- Talk recording backend forces `hpb-internal`
- operator persists and reruns the normalized Talk target/mode honestly

Not changed:

- operator still shells out to `cassini record`
- operator still owns queueing, attempts, stop handling, and publish sequencing

## Deployment / harness

Behavior change:

- new recorder env var: `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
- harness supports signaling `internalsecret`
- harness bootstrap now prefers proper Talk signaling registration when available

Not changed:

- D-283 does not add non-HPB support
- D-283 does not solve arbitrary local-network topology issues automatically

## What was not implemented

Deliberately not implemented for MVP:

- non-HPB / non-standalone-signaling Talk support
- automatic fallback from `hpb-internal` to guest mode
- per-backend URL→secret mapping
- a second recording architecture
- broad topology auto-detection for every local harness shape

Also not claimed here:

- a fully successful end-to-end internal recording proof on this specific local machine

Reason:

- the remaining uncertainty is harness/runtime topology, especially whether one advertised signaling URL is reachable from both **Nextcloud** and the **host-run recorder**
- that is captured as validation/handoff work in `validation.md`

## What was verified during implementation

Automated tests run successfully:

```bash
cd cassini-go-recorder && go test ./internal/config ./internal/cassini ./internal/nextcloud ./internal/talk
cd cassini-operator && go test ./internal/operator
```

Local harness checks completed successfully:

```bash
./harness/bin/bootstrap.sh
./harness/bin/create-room.sh --name D283HandoffCheck
```

What still needs a working target harness:

- full internal-mode recording proof against a supported Nextcloud + standalone signaling + HPB deployment

Use:

- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/validation.md`

## Migration required from an existing instance

If an instance previously supported Cassini's guest-participant Talk recording flow, D-283 requires these changes.

## 1. Configure standalone signaling internal-client auth

In the signaling server config:

```ini
[clients]
internalsecret = <shared-secret>
```

Then restart the signaling service.

This is mandatory for `hpb-internal`.
Without it, internal `hello` fails, typically as `invalid_client_type`.

## 2. Expose the same secret to Cassini

Set:

```bash
CASSINI_TALK_SIGNALING_INTERNAL_SECRET=<same-shared-secret>
```

Keep the existing recording secret:

```bash
CASSINI_TALK_RECORDING_SECRET=<recording-backend-secret>
```

These are separate secrets with different roles.

## 3. Ensure Talk advertises a reachable standalone signaling URL

Nextcloud signaling settings must advertise a signaling URL that is reachable from:

- Nextcloud itself
- the place where `cassini record` actually runs

This is the main migration pitfall.
If Talk advertises a URL only one side can reach, D-283 will fail honestly instead of silently falling back.

## 4. Keep or set the correct alternate Nextcloud reachability

If the Talk room base URL is not the same URL the recorder/operator can use for HTTP/OCS requests, keep using:

- `CASSINI_TALK_BACKEND_URL`
- or CLI/operator `connect-url` / `talkConnectURL`

This matters when the public Talk URL differs from container-to-container or host-to-container reachability.

## 5. Expect the default behavior to change

After D-283:

- `cassini record --call ...` defaults to `hpb-internal`
- generic operator Nextcloud jobs default to `hpb-internal`
- Talk recording-backend starts force `hpb-internal`

That means an instance that previously worked only because guest flow happened implicitly may now fail until internal-mode prerequisites are configured.

## 6. Use guest mode only as an explicit transitional fallback

If you need temporary fallback during rollout, use:

- CLI: `--talk-auth-mode guest-participant`
- operator request JSON: `"talkAuthMode": "guest-participant"`

There is intentionally **no automatic fallback**.

## 7. Validate on a supported HPB deployment

Run the proof flow in:

- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/validation.md`

Success criteria are about the **bootstrap/auth seam** first:

- recording-auth settings fetch
- internal `hello`
- internal `incall`
- room join without `sessionid`
- participant discovery and subscriber reuse

## Changed files

High-signal implementation files changed:

### Recorder

- `cassini-go-recorder/internal/config/config.go`
- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `cassini-go-recorder/internal/nextcloud/recording_auth.go`
- `cassini-go-recorder/internal/talk/internal_auth.go`
- `cassini-go-recorder/internal/talk/recorder.go`

### Operator

- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/talk_backend.go`

### Deployment / harness / docs

- `deployment/.env`
- `deployment/README.md`
- `harness/config/signaling.conf`
- `harness/bin/common.sh`
- `harness/bin/bootstrap.sh`
- `harness/README.md`
- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/validation.md`

### Tests updated/added

- `cassini-go-recorder/internal/cassini/cli_test.go`
- `cassini-go-recorder/internal/config/config_test.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client_test.go`
- `cassini-go-recorder/internal/talk/internal_auth_test.go`
- `cassini-operator/internal/operator/run_test.go`
- `cassini-operator/internal/operator/talk_backend_test.go`

## Bottom line

D-283 is now a **real dual-mode bootstrap implementation** with:

- internal HPB bootstrap
- explicit guest fallback
- internal mode as the default
- Talk-backend enforcement
- documented migration and validation handoff

What remains is not more product-path coding.
What remains is validating the bootstrap seam on a correctly wired supported harness.