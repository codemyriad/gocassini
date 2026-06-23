# G2 — Live Nextcloud Talk meeting capture

> Record real Nextcloud Talk meetings end-to-end, producing artifacts through the existing `cassini` pipeline.

- **Project:** [P1 — Cassini internal MVP](P1-cassini-internal-mvp.md) · **Phase:** PHASE-1 · **Requirements:** R0, R6
- **Status:** ✅ Complete
- **Last updated:** 2026-06-23

## What this goal is

G2 makes Cassini a first-class recorder of live Nextcloud Talk conversations. It establishes Cassini as Talk's native recording backend — Talk backend adapter, recording-backend protocol, auth, and start/stop/store lifecycle — so a real meeting can be captured and driven to a finished artifact without leaving the existing `cassini` CLI and artifact contracts (R6). On that foundation it brings live capture into the operator control plane, makes high-performance-backend (HPB) internal audio capture the default (with a guest fallback), and supplies a dev playback harness so meetings — including private 1:1 calls — can be simulated deterministically.

## Why it matters

This is the goal that turns Cassini from a pipeline operating on pre-existing media into a system that records actual Talk meetings (R0). Native backend integration and reuse of the artifact contracts (R6) keep capture aligned with the rest of the MVP.

## Definition of done

- A real Nextcloud Talk meeting records end-to-end and emerges as a `cassini` artifact (R0).
- Capture runs through the existing CLI / artifact contracts — no bespoke pipeline (R6).
- Cassini registers as Talk's native recording backend with a working start/stop/store lifecycle and auth.
- HPB-internal capture is the default, with guest-based capture as fallback.
- Live capture is triggerable and observable from the operator control plane (G1).
- Meetings, including private 1:1 calls, can be played back / simulated for testing.

## Work done

| Task | What it delivered |
| --- | --- |
| [D-263 — nextcloud-app-recording](D-263-nextcloud-app-recording) | Foundation: Cassini as Talk's native recording backend (adapter, protocol, auth, start/stop/store lifecycle). Built upon by D-283/D-288/D-290. |
| [D-234 — live-nextcloud-talk-recording](D-234-live-nextcloud-talk-recording) | Slice V2: live capture in the operator via real `cassini record` + stop API + schema migrations. |
| [D-283 — nextcloud-internal-audio-capture](D-283-nextcloud-internal-audio-capture) | Dual-mode HPB-internal audio capture as the default, with guest fallback. |
| [D-288 — play-commands](D-288-play-commands) | `cassini dev play` harness for media playback. |
| [D-288-1-1-meeting](D-288-1-1-meeting) | Private 1:1 playback: `cassini dev play-private`, Pied Piper fixture, Nextcloud user scaffolding. |

## Work TODO

None — all tasks associated with this goal are complete.

## Gaps to completion

The goal is met. Residual items are operational, not product work:

- Runtime validation on supported production HPB deployments (D-283 handoff).
- Downstream production end-to-end and fix work (e.g. D-290) builds on the D-263 backend but is tracked outside these planning directories.
