---
shaping: true
---

# D-283 — Nextcloud internal audio capture — Shaping

This document shapes **D-283: Nextcloud internal audio capture**.

Primary inputs:

- Linear issue `D-283`
- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/nextcloud_record_auth_gpt.md`
- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `cassini-go-recorder/internal/signaling/client.go`
- `cassini-operator/internal/operator/talk_backend.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/run.go`
- `deployment/README.md`
- `harness/README.md`
- `harness/config/signaling.conf`

## Selected shape

**Selected shape: A — add an HPB-internal Nextcloud capture mode beside the current guest-participant path, make the HPB-internal path the default, and keep the guest path as an explicit fallback.**

Why this is the selected shape:

- it matches the issue goal: record restricted and 1:1 Nextcloud Talk meetings without joining as a normal participant
- it matches the product constraint from shaping: the new path must live side-by-side with the current path and be configurable by additional params
- it keeps the current RTP/session-artifact/remux pipeline in place, which is the part Cassini already uses for per-participant tracks
- it avoids redoing work that already exists: `cassini-operator` already implements the Talk recording-backend HTTP surface
- it keeps the scope honest: support **Nextcloud with HPB only**; non-HPB Talk stays out of scope

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Cassini must be able to record restricted and 1:1 Nextcloud Talk meetings by joining through the internal HPB/signaling path rather than as a guest participant. | Core goal |
| R1 | The selected shape must preserve Cassini's current per-participant capture value: separate remote streams that still support speaker-attributed downstream processing and multi-track MKV output. | Must-have |
| R2 | The new HPB-internal behavior must live side-by-side with the current guest-participant behavior and be selectable through additional parameters. | Must-have |
| R3 | The new HPB-internal behavior should be the default capture path for supported Nextcloud Talk recordings. | Must-have |
| R4 | The first supported scope is **Nextcloud Talk deployments with standalone signaling + HPB**. Non-HPB/internal-signaling Talk deployments are out of scope for this effort. | Must-have |
| R5 | The solution should reuse the current Cassini product/control surfaces where possible rather than creating a separate recorder product path. | Must-have |
| R6 | The solution must keep the existing Talk recording-backend integration in `cassini-operator` and extend it only where needed for the new recorder bootstrap/config path. | Must-have |
| R7 | Unsupported or misconfigured deployments must fail clearly against the selected mode; the implementation should not silently hide HPB/internal-auth problems behind implicit behavior changes. | Must-have |
| R8 | Unknown mechanics discovered during shaping must be isolated into explicit spikes before implementation detail is committed. | Must-have |

---

## CURRENT: repo and product baseline

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `cassini-go-recorder/internal/talk/recorder.go` bootstraps Talk capture as a normal participant/guest: `GetRoom`, `MarkParticipantActive`, `SetGuestName`, `FetchSignalingSettings`, signaling `hello`, signaling room join with `sessionid`, then `JoinCall`. | |
| **CURRENT2** | The current recorder capture path already preserves remote RTP/RTCP into a session artifact and composes a multi-track MKV from that artifact. | |
| **CURRENT3** | The current recorder subscribes to remote sessions through the HPB-style signaling subscriber flow (`requestoffer` → `offer` → `answer` → `OnTrack`). | |
| **CURRENT4** | `cassini-operator` already implements Talk recording-backend compatibility endpoints: `GET /api/v1/welcome` and `POST /api/v1/room/{token}` with Talk recording checksum validation. | |
| **CURRENT5** | Operator record jobs already run `cassini record --call ...` and already persist request JSON, attempts, logs, and stop metadata. | |
| **CURRENT6** | Deployment/operator config already supports the Talk recording backend secret, but there is no current recorder/operator surface for HPB internal-client auth secrets. | ⚠️ |
| **CURRENT7** | The local harness full profile includes standalone signaling + Janus/HPB-style media, but the checked-in signaling config does not yet show an obvious internal-client secret/config path for this new recorder mode. | ⚠️ |
| **CURRENT8** | The repo does not yet expose a user-facing capture/auth mode parameter that distinguishes guest-participant capture from HPB-internal capture. | ⚠️ |

---

## A: Dual-mode recorder bootstrap, HPB-internal default

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Add a cross-surface `talkAuthMode` selector with `hpb-internal` and `guest-participant`. | |
| **A2** | Make `hpb-internal` the default mode for supported Nextcloud Talk recording flows. | |
| **A3** | In `hpb-internal` mode, fetch Talk signaling settings using recording-auth headers instead of guest/user room membership auth. | |
| **A4** | In `hpb-internal` mode, send signaling `hello` as an internal client using HPB/signaling internal auth, send the internal `incall` update, and join the room without a Nextcloud participant session id. | |
| **A5** | Keep the existing subscriber/requestoffer/OnTrack/session-artifact/remux path as the main media-capture path after the internal join succeeds. | |
| **A6** | Skip guest-specific OCS lifecycle calls (`MarkParticipantActive`, `SetGuestName`, `JoinCall`, `LeaveCall`, `LeaveParticipantActive`) in the internal mode. | |
| **A7** | Expose `talkAuthMode` on direct `cassini record --call ...` and generic operator-created Nextcloud jobs; Talk-backend-triggered recordings set it implicitly to `hpb-internal`. | |
| **A8** | Keep the current guest-participant path available only as an explicit fallback mode rather than deleting it. | |
| **A9** | Fail closed in `hpb-internal` mode when required secrets, standalone-signaling support, or MCU/HPB support are missing; do not auto-fallback to guest mode. | |
| **A10** | Keep secrets/config process-scoped: reuse `CASSINI_TALK_RECORDING_SECRET`, add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, and keep using `--connect-url` / `CASSINI_TALK_BACKEND_URL` for alternate Nextcloud reachability. | |
| **A11** | Keep MVP secret resolution deployment-wide, but isolate the lookup seam so per-backend URL→secret mapping can be added later without changing the product shape. | |

## B: Replace the guest path entirely with HPB-internal capture

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Rewrite the recorder bootstrap to always use the HPB-internal Nextcloud path. | |
| **B2** | Remove or effectively deprecate guest-participant capture from the current product surface. | |
| **B3** | Keep only one join path in code and config. | |

## C: Keep guest capture as the normal surface and add HPB-internal only for Talk-backend-triggered recordings

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Add HPB-internal capture only for the Talk recording-backend route in `cassini-operator`. | |
| **C2** | Leave direct/manual `cassini record --call ...` behavior on the current guest path unless explicitly routed through the Talk backend flow. | |
| **C3** | Keep the wider surface mostly unchanged and localize the new behavior to operator-driven runs. | |

---

## Shape selection summary

- **A is selected.**
  - It satisfies the “side-by-side + default new path” constraint.
  - It keeps the existing recorder/media pipeline mostly intact.
  - Its remaining risk is narrow runtime invalidation, not broad product-shape uncertainty.

- **B is rejected for this issue.**
  - It fails the explicit requirement that the new behavior live beside the current functionality surface.

- **C is rejected for MVP.**
  - It is too narrow and would keep HPB-internal behavior confined to one operator entry path instead of the broader Cassini recording surface.

---

## Fit Check: R × A

| Req | Requirement | Status | A |
|-----|-------------|--------|---|
| R0 | Cassini must be able to record restricted and 1:1 Nextcloud Talk meetings by joining through the internal HPB/signaling path rather than as a guest participant. | Core goal | ✅ |
| R1 | The selected shape must preserve Cassini's current per-participant capture value: separate remote streams that still support speaker-attributed downstream processing and multi-track MKV output. | Must-have | ✅ |
| R2 | The new HPB-internal behavior must live side-by-side with the current guest-participant behavior and be selectable through additional parameters. | Must-have | ✅ |
| R3 | The new HPB-internal behavior should be the default capture path for supported Nextcloud Talk recordings. | Must-have | ✅ |
| R4 | The first supported scope is **Nextcloud Talk deployments with standalone signaling + HPB**. Non-HPB/internal-signaling Talk deployments are out of scope for this effort. | Must-have | ✅ |
| R5 | The solution should reuse the current Cassini product/control surfaces where possible rather than creating a separate recorder product path. | Must-have | ✅ |
| R6 | The solution must keep the existing Talk recording-backend integration in `cassini-operator` and extend it only where needed for the new recorder bootstrap/config path. | Must-have | ✅ |
| R7 | Unsupported or misconfigured deployments must fail clearly against the selected mode; the implementation should not silently hide HPB/internal-auth problems behind implicit behavior changes. | Must-have | ✅ |
| R8 | Unknown mechanics discovered during shaping must be isolated into explicit spikes before implementation detail is committed. | Must-have | ✅ |

**Notes:**
- A is selected on the basis of the completed X1 static spike and the explicit decision to accept narrow runtime invalidation risk during implementation.
- A satisfies R7 by failing closed in `hpb-internal` mode; guest capture remains only an explicit operator/CLI selection.
- A keeps MVP secret handling deployment-wide (`CASSINI_TALK_RECORDING_SECRET` + `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`) while leaving a future seam for URL→secret mapping if multi-backend support becomes necessary.

## Decisions locked now

| Decision | Selected direction | Why |
|----------|--------------------|-----|
| D1 | Use one cross-surface selector: `talkAuthMode` with `hpb-internal` (default) and `guest-participant`. | The main change is the Talk bootstrap/auth path; a single mode selector fits that boundary cleanly. |
| D2 | No automatic fallback. `hpb-internal` fails clearly if secrets, standalone-signaling support, or MCU/HPB support are missing. | This is the cleanest way to satisfy R7. |
| D3 | Support both direct `cassini record --call ...` and operator-created Nextcloud jobs in MVP. Talk-backend-triggered recordings set internal mode implicitly. | This keeps the new mode side-by-side across existing Cassini surfaces and avoids Shape C's narrowness. |
| D4 | Keep secrets/config process-scoped, not per-job: reuse `CASSINI_TALK_RECORDING_SECRET`, add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, and keep using existing `--connect-url` / `CASSINI_TALK_BACKEND_URL` for alternate Nextcloud reachability. | Smallest fit with the current repo, and it keeps secret values out of job JSON and CLI history. |
| D5 | MVP secret resolution is deployment-wide, not per-backend mapping. Add the lookup seam in code, but defer multi-backend config shape. | Smallest change consistent with current deployment/operator config. |
| D6 | Treat X1 as done for shaping. If runtime disproves it, fix the recorder event/bootstrap seam rather than revisiting the product shape. | This matches the current implementation posture for D-283. |

## Spike status

| Spike | Status | Result |
|-------|--------|--------|
| X1 — subscriber capture under internal client | Done for shaping | Static docs/source strongly support reusing the subscriber/requestoffer path after internal bootstrap. Runtime invalidation remains possible but narrow. |
| X2 — HPB-internal auth/config surface | Absorbed into shaping | Parameter, fallback, and secret decisions are selected above; no separate pre-implementation spike is needed. |
| X3 — local harness support for internal-client testing | Deferred | Useful for acceptance and debugging, but no longer blocks shaping. |

## Detailed artifacts

Detailed affordance mapping for the selected shape now lives in:

- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/breadboarding.md`

Implementation slicing for the selected shape now lives in:

- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/slices.md`

Validation handoff for the selected shape now lives in:

- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/validation.md`
