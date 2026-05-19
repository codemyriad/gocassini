## D-263 Spike: Cassini trigger API contract

### Context

The D-263 shaping is already selected around this direction:

- standard Nextcloud PHP app
- moderator-facing trigger from Talk meeting context
- signed request from the app to an external Cassini deployment
- Cassini continues to own live recording by joining the Talk room

The Talk UI spike already selected the client/server boundary:

- a logged-in Nextcloud script contributes the trigger affordance
- the browser calls a Nextcloud app route
- the app route calls the external Cassini service

That leaves one narrow but important seam:

- **what exact HTTP contract the Nextcloud app should use to trigger Cassini recording**

This matters because we now have two plausible backend directions:

1. reuse the existing `cassini-operator` HTTP job surface
2. invent a new recorder-facing HTTP API on top of `cassini record`

### Goal

Pick the D-263 trigger contract clearly enough that implementation can proceed without reopening:

- which Cassini process the app talks to
- what endpoint and payload the app sends
- how the request is authenticated
- what response semantics the app should expect

### Outcome

This spike is complete enough to lock the first D-263 trigger contract.

Selected for D-263:

- **External service target:** `cassini-operator`, not a new HTTP server inside `cassini-go-recorder`
- **Start endpoint:** reuse `POST /jobs?provider=nextcloud-talk`
- **Request body:** reuse the current normalized operator trigger body
- **Room identifier:** send the full Talk call URL, not `{ base_url, room_token }`
- **Optional controls:** keep `guestName`, `duration`, `stopWhenRoomEmpty`, and `roomEmptyGrace` exactly aligned with the operator's current trigger contract
- **Authentication:** add a shared-secret HMAC request-signing layer around the operator HTTP surface
- **Initial response:** reuse `202 { "id": "<job-id>" }`
- **Follow-on path:** preserve the existing job id so future Nextcloud work can layer stop/status on top of `POST /jobs/:id/stop` and `GET /jobs/:id`
- **Explicit non-goal:** do not add a second HTTP API in the recorder package for D-263

## Evidence in the current repo

### 1. The operator already is the Cassini HTTP control surface

The current repo already has an HTTP service intended to trigger recording work:

- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/README.md`

That service already:

- accepts `POST /jobs?provider=nextcloud-talk`
- validates and normalizes trigger input
- runs `cassini doctor --target record`
- runs `cassini record --call <url> --out ... --name ...`
- returns a stable job id
- exposes follow-on stop and status surfaces

So there is already a clear backend boundary for "trigger recording remotely."

### 2. The recorder already wants a full call URL

The current user-facing and operator-facing recording path is centered on:

- `cassini record --call <CALL_URL> --out ...`

Relevant references:

- `README.md`
- `cassini-go-recorder/internal/cassini/cli.go`
- `cassini-go-recorder/internal/nextcloud/call_url.go`

That means the narrowest contract is to keep sending the same full Talk call URL the recorder already understands.

### 3. The operator contract already carries the right first-cut controls

The current trigger body already supports:

- `platform`
- `url`
- optional `guestName`
- optional `duration`
- optional `stopWhenRoomEmpty`
- optional `roomEmptyGrace`

Those map directly onto current `cassini record` behavior.

So D-263 does not need to invent new payload concepts for the initial app integration.

## Answered questions

| # | Decision | Answer |
|---|----------|--------|
| **D263-API1** | Which Cassini process should the Nextcloud app call? | Call `cassini-operator`. Do not add a new HTTP server to `cassini-go-recorder` for D-263. |
| **D263-API2** | Which endpoint should the app use to start recording? | Reuse `POST /jobs?provider=nextcloud-talk`. |
| **D263-API3** | Should D-263 send full Talk URL or split room data? | Send the full Talk call URL in `url`. Keep the existing recorder-facing shape. |
| **D263-API4** | What request body should D-263 send? | Reuse the current normalized operator trigger body: `platform`, `url`, optional `guestName`, optional `duration`, optional `stopWhenRoomEmpty`, optional `roomEmptyGrace`. |
| **D263-API5** | Should the contract expose new stop/scheduling concepts? | No. Keep only the controls the operator already supports. |
| **D263-API6** | How should the request be authenticated? | Add shared-secret HMAC signing at the HTTP layer using timestamp + nonce + body digest. |
| **D263-API7** | What response should the app treat as success? | `202 Accepted` with `{ "id": "<job-id>" }`. |
| **D263-API8** | Should D-263 include richer moderator identity/audit fields in the external JSON body? | No for the first cut. Keep the external body narrow and let the Nextcloud app retain local operator context. |
| **D263-API9** | Should D-263 define a separate app-facing recording id distinct from the operator job id? | No. Reuse the operator job id so stop/status can compose later without translation. |

## Selected contract

### Request

```http
POST /jobs?provider=nextcloud-talk
Content-Type: application/json
X-Cassini-Timestamp: 1746979200
X-Cassini-Nonce: 3d5b5cf6d6dc4d3c9b653eef5ebf3f1d
X-Cassini-Signature: <hex-hmac-sha256>

{
  "platform": "nextcloud-talk",
  "url": "https://cloud.example.com/call/abcd1234",
  "guestName": "CassiniRecorder",
  "duration": 3600,
  "stopWhenRoomEmpty": true,
  "roomEmptyGrace": 30
}
```

### Success response

```http
202 Accepted
Content-Type: application/json

{
  "id": "01JV2M5YJ4W6Y9SZ7Q7EN7Q6RP"
}
```

### Error classes

- `400 Bad Request`
  - missing/invalid JSON
  - missing `platform`
  - missing `url`
  - invalid numeric values
- `401 Unauthorized`
  - missing auth headers
  - invalid signature
  - expired timestamp window
  - replayed nonce
- `503 Service Unavailable`
  - max record workers exceeded
- `500 Internal Server Error`
  - internal persistence or runtime failure before acceptance

For D-263, the Nextcloud app only needs to distinguish:

- accepted
- rejected as invalid
- rejected as unauthorized
- rejected as busy
- unreachable / unexpected failure

## HMAC shape

Selected D-263 signing input:

```text
METHOD + "\n" +
PATH_WITH_QUERY + "\n" +
TIMESTAMP + "\n" +
NONCE + "\n" +
HEX(SHA256(BODY))
```

Selected D-263 signature:

```text
HEX(HMAC-SHA256(shared_secret, canonical_string))
```

Selected D-263 headers:

- `X-Cassini-Timestamp`
- `X-Cassini-Nonce`
- `X-Cassini-Signature`

Why this shape:

- easy for the Nextcloud PHP app to produce
- easy for the Go operator to verify
- avoids ambiguity around JSON pretty-printing by signing the exact transmitted bytes via body digest
- keeps the auth story independent from reverse-proxy-specific auth products

### Replay policy

Selected for D-263:

- require timestamp freshness within a small window, for example `±300s`
- keep a best-effort in-memory TTL set of seen nonces inside `cassini-operator`
- reject a request when the same nonce appears again within the acceptance window

Why this is enough for D-263:

- it materially improves over a bare shared-secret header
- it fits the current single-process operator runtime
- it avoids introducing a second persistence mechanism just for nonce tracking

This is intentionally a pilot-grade replay defense, not a multi-node distributed auth system.

## Why not add a new recorder HTTP API?

That would create a second control boundary for the same action:

- one HTTP control surface in `cassini-operator`
- one new HTTP trigger surface in `cassini-go-recorder`

It would also force D-263 to answer avoidable questions:

- where stop/status should live
- whether job ids exist at all
- whether build/publish chaining is recorder-owned or operator-owned
- how future rerun/status work maps back into the repo's existing control plane

Reusing the operator surface avoids those questions.

## Why not split `base_url` and `room_token` now?

The current recorder already accepts and parses the full call URL.
Using the same shape has three advantages:

- the app can forward a meeting URL it already knows from Talk context
- the operator can continue to drive `cassini record --call <url>`
- we avoid introducing a new parallel room-address representation for D-263

If a later cut needs a more structured room identity, we can widen the contract then.
It is unnecessary for the first triggerable integration.

## What the Nextcloud app should persist

For D-263, the app-side admin settings should treat the configured "Cassini URL" as:

- **the base URL of `cassini-operator`**

That URL is not:

- the static Cassini viewer URL
- a direct recorder CLI endpoint
- a Talk recording-backend endpoint

It is the operator/control-plane HTTP base used to trigger recording work.

## Follow-on compatibility

Selecting the operator job contract now leaves a clean extension path:

- `POST /jobs/:id/stop` for explicit stop
- `GET /jobs/:id` for job state
- `GET /jobs` for debugging/admin views
- `POST /jobs/:id/rerun` for later operator/admin flows

D-263 does not need to implement all of those in Nextcloud now.
It benefits from returning a job id that makes them available later.

## Acceptance

This spike is complete because it resolves the trigger-contract ambiguity:

- the app should call `cassini-operator`
- the app should reuse `POST /jobs?provider=nextcloud-talk`
- the request should send a full Talk call URL
- the payload should stay aligned with the current operator body
- the request should be protected with shared-secret HMAC signing plus basic replay defense
- the response should reuse the operator job id for future stop/status compatibility

## Reassessment

This spike eliminates the main API-shape ambiguity.

What remains is no longer "what contract should we invent?"
It is:

- where the HMAC verification middleware lives inside `cassini-operator`
- whether D-263 also wants a tiny signed health endpoint for admin setup checks

Those are implementation details or a small follow-on spike, not a reason to reopen the main trigger-contract choice.
