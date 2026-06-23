# G2 — Live Nextcloud Talk Meeting Capture

> Record real Nextcloud Talk meetings end-to-end, producing artifacts through the existing cassini pipeline.

- **Project:** [P1 — Cassini internal MVP](../P1-cassini-internal-mvp/spec.md) · **Phase:** PHASE-1 · **Status:** ✅ Completed · **Requirements:** R0, R6

## What this goal is

G2 makes Cassini a first-class recorder of live Nextcloud Talk conversations. It establishes Cassini as Talk's native recording backend — wiring the Talk backend adapter, recording-backend protocol, auth, and start/stop/store lifecycle — so that a real meeting can be captured and driven all the way to a finished artifact without leaving the existing cassini CLI and artifact contracts (R6).

On top of that foundation, the goal brings live capture into the operator control plane, makes high-performance-backend (HPB) internal audio capture the default capture mode (with a guest fallback), and supplies a dev playback harness so meetings — including private 1:1 calls — can be simulated and exercised deterministically during testing.

## Why it matters

This is the goal that turns Cassini from a pipeline operating on pre-existing media into a system that records actual Talk meetings (R0). Native backend integration and reuse of the cassini artifact contracts (R6) keep capture aligned with the rest of the MVP rather than forking a parallel path.

## Definition of done / success signals

- A real Nextcloud Talk meeting can be recorded end-to-end and emerge as a cassini artifact (R0).
- Capture is driven through the existing cassini CLI / artifact contracts — no bespoke pipeline (R6).
- Cassini registers as Talk's native recording backend with a working start/stop/store lifecycle and auth.
- HPB-internal audio capture is the default capture mode, with guest-based capture as fallback.
- Live capture is triggerable and observable from the operator control plane (via G1).
- Meetings — including private 1:1 calls — can be played back and simulated deterministically for testing.

## Tasks

| Task | Status | What it delivered |
| --- | --- | --- |
| [D-263 nextcloud-app-recording](../D-263-nextcloud-app-recording) | ✅ | Foundation: Cassini as Talk's native recording backend — Talk backend adapter, recording-backend protocol, auth, and start/stop/store lifecycle. Built upon by D-283/D-288/D-290. |
| [D-234 live-nextcloud-talk-recording](../D-234-live-nextcloud-talk-recording) | ✅ | Slice V2: live capture in the operator via real `cassini record` + stop API + schema migrations. |
| [D-283 nextcloud-internal-audio-capture](../D-283-nextcloud-internal-audio-capture) | ✅ | Dual-mode HPB-internal audio capture as the default, with guest fallback. |
| [D-288 play-commands](../D-288-play-commands) | ✅ | `cassini dev play` harness for media playback. Deferred: better synthetic fixture + 1:1 simulation. |
| [D-288-1-1-meeting](../D-288-1-1-meeting) | ✅ | Private 1:1 playback: `cassini dev play-private`, Pied Piper fixture, Nextcloud user scaffolding. |

## Lineage & remaining work

D-263 is the foundation — it makes Cassini a native Talk recording backend with a full start/stop/store lifecycle. D-234 (slice V2) wires that live capture into the operator control plane (see [G1 — operator-control-plane](../G1-operator-control-plane/goal.md)) via real `cassini record` plus a stop API and schema migrations. D-283 then makes HPB-internal capture the default mode, with a guest fallback for cases the internal path cannot serve. The D-288 pair (play-commands and 1-1-meeting) add the dev playback harness and private 1:1 simulation that make capture testable without a live human meeting — D-288-1-1-meeting realizes the synthetic-fixture and 1:1 followup that D-288 deferred.

All tasks under this goal are complete. Downstream production end-to-end and fix work (e.g. D-290) builds on the D-263 backend but lives outside these planning directories.
