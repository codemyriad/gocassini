---
shaping: true
---

# D-263 — Shaping

This document shapes **D-263: Native Talk recording UX with Cassini backend**.

It elaborates the slice already defined in:

- `planning/initiatives/mvp/slices/D-263-nextcloud-app-recording/brief.md`

The goal here is to choose a concrete implementation shape for:

- keeping Talk's native recording UX
- making Cassini the recording backend behind that UX
- preserving Cassini-owned capture and downstream artifacts

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | A Talk moderator can use the native Talk recording controls and have Cassini start/stop the actual recording work for that room. | Core goal |
| R1 | Cassini must remain the recording owner: the selected shape must keep Cassini on the live-capture path rather than switching to a `.webm` import-only architecture. | Must-have |
| R2 | The integration should reuse Talk's existing recording UX and avoid a custom meeting-button path. | Must-have |
| R3 | The integration must work with Cassini deployed externally and configured through a clear backend URL + shared secret setup. | Must-have |
| R4 | The selected shape should use supported Talk/Nextcloud integration surfaces where possible, preferring backend protocol compatibility over frontend DOM shims. | Must-have |
| R5 | The first cut must define the exact Talk recording-backend lifecycle Cassini supports: start, stop, health/auth, and recording result handling. | Must-have |
| R6 | D-263 must stay narrowly scoped to recording-backend integration and admin setup; viewer embedding and broad Nextcloud productization stay out of this cut. | Must-have |
| R7 | The selected shape should leave a clean extension path for later viewer/artifact integration without forcing that into the first implementation. | Must-have |
| R8 | The selected shape should fit the way Nextcloud expects recording backends to be deployed: an external HTTP API, typically behind reverse-proxy TLS, within a Talk deployment that already satisfies signaling prerequisites. | Must-have |

---

## CURRENT: Cassini records Talk, but Talk does not drive it natively

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | Cassini already records a Nextcloud Talk meeting when given a Talk call URL through the existing recording path. | |
| **CURRENT2** | Cassini already owns room join, signaling/WebRTC negotiation, media capture, and downstream artifact generation. | |
| **CURRENT3** | `cassini-operator` already exposes a job-trigger control-plane surface for starting recording work remotely. | |
| **CURRENT4** | Talk does not yet use Cassini as its recording backend. | |
| **CURRENT5** | The current repo does not yet implement the Talk recording-backend protocol boundary for Cassini. | |

## A: Cassini implements the Talk recording-backend contract — selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Keep Talk's native recording UI as the moderator-facing control surface. | |
| **A2** | Implement a Cassini backend endpoint that accepts Talk recording backend start/stop requests with the expected request signing/auth behavior. | |
| **A3** | Adapt Cassini's current control boundary so Talk backend events start and stop the existing live recording flow for the target room. | |
| **A4** | Provide backend health/readiness checking and clear admin setup around Cassini backend URL and secret. | |
| **A5** | Define how Cassini satisfies the Talk recording result/store lifecycle while preserving its own downstream artifact pipeline. | |
| **A6** | Match the official recording server's operational shape where useful: external HTTP backend, reverse-proxy TLS, Talk-facing auth/callback/upload behavior. | |

## B: Custom Nextcloud app trigger + Talk shim + Cassini operator API — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Build a custom Nextcloud app trigger surface and inject a Cassini action into the Talk UI. | |
| **B2** | Use a signed Nextcloud-app-to-operator API to start Cassini recording jobs. | |
| **B3** | Keep Talk's native recording controls out of the main integration path. | |

## C: Native Talk UX + Nextcloud `.webm` output + Cassini post-processing — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Let Talk or its existing backend produce `.webm` recordings. | |
| **C2** | Feed those recordings into Cassini only after capture completes. | |
| **C3** | Remove Cassini from the live-capture role. | |

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | A Talk moderator can use the native Talk recording controls and have Cassini start/stop the actual recording work for that room. | Core goal | ✅ | ❌ | ✅ |
| R1 | Cassini must remain the recording owner: the selected shape must keep Cassini on the live-capture path rather than switching to a `.webm` import-only architecture. | Must-have | ✅ | ✅ | ❌ |
| R2 | The integration should reuse Talk's existing recording UX and avoid a custom meeting-button path. | Must-have | ✅ | ❌ | ✅ |
| R3 | The integration must work with Cassini deployed externally and configured through a clear backend URL + shared secret setup. | Must-have | ✅ | ✅ | ✅ |
| R4 | The selected shape should use supported Talk/Nextcloud integration surfaces where possible, preferring backend protocol compatibility over frontend DOM shims. | Must-have | ✅ | ❌ | ✅ |
| R5 | The first cut must define the exact Talk recording-backend lifecycle Cassini supports: start, stop, health/auth, and recording result handling. | Must-have | ✅ | ❌ | ❌ |
| R6 | D-263 must stay narrowly scoped to recording-backend integration and admin setup; viewer embedding and broad Nextcloud productization stay out of this cut. | Must-have | ✅ | ❌ | ✅ |
| R7 | The selected shape should leave a clean extension path for later viewer/artifact integration without forcing that into the first implementation. | Must-have | ✅ | ✅ | ✅ |
| R8 | The selected shape should fit the way Nextcloud expects recording backends to be deployed: an external HTTP API, typically behind reverse-proxy TLS, within a Talk deployment that already satisfies signaling prerequisites. | Must-have | ✅ | ⚠️ | ⚠️ |

**Notes:**
- B fails R0 and R2 because it bypasses the native Talk recording controls entirely.
- B fails R4 because it depends on a custom Talk shim rather than the supported recording-backend surface.
- B fails R5 and R6 because it solves the wrong boundary for the newly selected architecture.
- C fails R1 because Cassini no longer owns live capture.
- C fails R5 because the Talk backend lifecycle is no longer Cassini's main integration seam.

## Selected shape

**Selected shape: A — Cassini implements the Talk recording-backend contract**

Why A fits best:

- it preserves the native Talk recording UX
- it keeps Cassini on the live-capture path
- it removes the custom Talk frontend integration risk
- it moves the integration seam onto a supported Talk backend contract
- it keeps D-263 focused on recording integration rather than broader app/UI work
- it matches the deployment/runtime expectations shown by the official recording server more closely than the rejected shapes

## Detail A

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Use Talk's native `Start recording` / `Stop recording` UI as the only moderator-facing recording controls for D-263. | |
| **A2** | Expose a Cassini backend HTTP surface compatible with the Talk recording backend protocol for start/stop requests, including the expected signature/auth handling. | |
| **A3** | Map Talk room identity into the existing Cassini recording path so backend start events launch recording for the correct room. | |
| **A4** | Map Talk backend stop events into clean Cassini recording finalization. | |
| **A5** | Provide a backend health/readiness check and operator-facing setup guidance for backend URL + secret configuration. | |
| **A6** | Implement the Talk-specific adapter inside `cassini-operator`, while keeping runtime and protocol concerns internally separated. | |
| **A7** | Use `baseURL + roomToken` as the native execution identity for Talk-driven runs; keep full call URLs as a CLI convenience only. | |
| **A8** | Upload Cassini's final meeting `.mkv` to Talk `/store`; keep portable `.opus` and richer viewer artifacts downstream from that contract. | |
| **A9** | Treat the official `nextcloud-talk-recording` server as a protocol and deployment reference, not as the recorder architecture to copy. | |

## Deferred from this D-263 cut

- custom Talk DOM injection
- custom Nextcloud app meeting trigger route
- `.webm` post-processing-only architecture
- viewer embedding inside Nextcloud
- artifact browser inside a Nextcloud app

## Main Nextcloud references

- Talk recording backend:
  - https://nextcloud-talk.readthedocs.io/en/stable/recording/
- Official recording server:
  - https://github.com/nextcloud/nextcloud-talk-recording
- Recording server installation:
  - https://portal.nextcloud.com/article/Nextcloud-Talk/Recording-Server/Installation
- High-performance backend installation:
  - https://portal.nextcloud.com/article/Nextcloud-Talk/High-Performance-Backend/Installation-of-Nextcloud-Talk-High-performance-backend
- Talk integration:
  - https://docs.nextcloud.com/server/stable/developer_manual/digging_deeper/talk.html
- Settings:
  - https://docs.nextcloud.com/server/stable/developer_manual/basics/setting.html
- HTTP client:
  - https://docs.nextcloud.com/server/27/developer_manual/digging_deeper/http_client.html

## What the official server clarified

The official `nextcloud-talk-recording` repository and docs clarified several D-263 questions:

1. **Backend deployment shape**
   - Nextcloud expects a recording backend to be an external HTTP API, with TLS commonly handled by a reverse proxy.

2. **Talk deployment prerequisites**
   - The official server assumes the standalone Talk signaling server exists. For D-263, "recording button appears" is downstream of Talk being properly deployed and recording backend config being present.

3. **Execution identity**
   - The official server works from configured Nextcloud base URL plus room token; it does not need a copied moderator call URL as a primary backend input.

4. **What to copy vs not copy**
   - We should copy its Talk-facing auth/callback/upload behavior.
   - We should not copy its browser-plus-ffmpeg recording architecture, because Cassini already has its own recorder model.

## Implementation note: additional Talk layer, not `/jobs` mutation

The current D-263 implementation should add the Talk recording-backend API as an
additional HTTP layer inside `cassini-operator`, not by reshaping the existing
generic operator `/jobs` API into Talk protocol semantics.

Why:

- `/jobs` is a generic operator control surface
- Talk recording is a provider-specific backend contract with its own:
  - request paths
  - request signing rules
  - room-token lifecycle
  - callback/upload obligations

So the intended structure is:

- keep existing generic operator routes:
  - `POST /jobs`
  - `POST /jobs/:id/stop`
  - `GET /jobs`
  - `GET /jobs/:id`
- add Talk-specific routes beside them inside the same process:
  - `GET /api/v1/welcome`
  - `POST /api/v1/room/{token}`
  - readiness/health route as needed
- let both route families use shared runtime/service primitives underneath

This keeps:

- one binary/process
- one runtime truth
- one persistence/control layer

while avoiding protocol confusion between:

- generic operator job control
- Talk recording-backend compatibility

## Open shaping checks

1. **Optional thin app scope**
   - We still need to decide whether D-263 needs a Nextcloud app at all or whether Talk admin configuration plus documentation is enough for the first cut.

## Suggested next move

The shape is selected enough to move into:

1. a breadboard for the Talk backend protocol + Cassini lifecycle, and then
2. `slices.md` for implementation planning
