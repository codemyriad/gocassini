## D-263 Spike: admin health-check surface

### Context

The D-263 shaping now has two major seams already selected:

- the Talk UI trigger enters through a logged-in Nextcloud script plus a narrow app route
- the Nextcloud app triggers recording by calling `cassini-operator` at `POST /jobs?provider=nextcloud-talk`

That leaves one remaining admin-setup question:

- **what should the Nextcloud app call when an admin clicks "Test Cassini connection" in settings**

This matters because admin setup needs a real answer to:

- is the configured URL reachable?
- is the shared secret correct?
- is the operator actually ready to start recording jobs?

Those are different questions, and D-263 should answer them with one bounded health surface rather than guessing from trigger failures later.

### Goal

Select the D-263 health-check contract clearly enough that implementation can proceed without reopening:

- whether the app should reuse an existing endpoint or add a dedicated one
- whether health-check auth should match trigger auth
- what "healthy" means for the first cut
- what response shape the app should use for admin feedback

### Outcome

This spike is complete enough to lock the D-263 health-check direction.

Selected for D-263:

- **Endpoint shape:** add a dedicated operator health endpoint
- **Endpoint path:** `GET /healthz`
- **Authentication:** use the same shared-secret HMAC scheme as the trigger endpoint
- **Modes:** support a default shallow check and an explicit deep record-readiness check
- **Default admin behavior:** use the deep record-readiness check from the Nextcloud settings page
- **Readiness primitive:** the deep check should run the same `cassini doctor --target record` path the operator already relies on before recording
- **Explicit non-goal:** do not use `POST /jobs` as a health probe

## Why a dedicated endpoint is needed

### 1. Triggering a job is the wrong way to test setup

The existing operator trigger endpoint:

- consumes record-worker capacity
- creates a persisted job row
- may enqueue real work
- is designed for recording admission, not admin diagnostics

Using it as a health probe would create at least one bad outcome:

- false recordings
- junk job rows
- capacity noise
- unclear error semantics for the admin UI

So D-263 should not overload `POST /jobs` as a setup test.

### 2. Plain reachability is not enough

A shallow HTTP success only tells us:

- a server is listening
- the URL is routable
- auth might be accepted

It does **not** tell us the most important operational thing:

- whether the operator can pass `cassini doctor --target record`

The repo already shows that `doctor` is the meaningful preflight gate for recording:

- `cassini-operator` runs `cassini doctor --target record` before `cassini record`
- `cassini doctor --target record` checks writable directories and free space

So admin setup should be able to surface that readiness explicitly.

### 3. The app needs error classes it can explain

The settings UI needs to distinguish at least:

- cannot reach operator
- shared secret rejected
- operator reachable but not record-ready
- operator reachable and record-ready

A dedicated endpoint can return those states directly without creating side effects.

## Evidence in the current repo

### 1. The operator already treats `doctor --target record` as the recording preflight

Relevant references:

- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/README.md`

Current behavior:

- before live record starts, the operator runs `cassini doctor --target record`
- if doctor fails, the recording does not proceed

That means "operator is healthy for recording" already has a meaningful implementation primitive.

### 2. The doctor path already produces operator-relevant readiness information

Relevant reference:

- `cassini-go-recorder/internal/cassini/doctor.go`

For `--target record`, it currently checks:

- working directory accessibility
- working directory writability
- working directory free space
- temporary directory writability
- temporary directory free space

This is enough for a first-cut operator readiness signal.

### 3. There is no existing health endpoint today

The current operator HTTP surface covers:

- `POST /jobs`
- `POST /jobs/:id/stop`
- `POST /jobs/:id/rerun`
- `GET /jobs`
- `GET /jobs/:id`

There is no existing health or readiness endpoint to reuse.

## Answered questions

| # | Decision | Answer |
|---|----------|--------|
| **D263-H1** | Reuse an existing endpoint or add a health endpoint? | Add a dedicated health endpoint. |
| **D263-H2** | Should health auth match trigger auth? | Yes. Use the same HMAC timestamp/nonce/body-digest scheme. |
| **D263-H3** | Should health be shallow only? | No. Support shallow and deep modes. |
| **D263-H4** | What should the Nextcloud admin UI call by default? | Call the deep record-readiness mode. |
| **D263-H5** | What should deep readiness mean? | Run `cassini doctor --target record` and summarize the result. |
| **D263-H6** | Should D-263 persist health-check history in the operator? | No. Return current status only. |
| **D263-H7** | Should the endpoint create jobs or consume worker slots? | No. It must be side-effect free with respect to job creation and recording admission. |
| **D263-H8** | Should warnings count as healthy? | Yes. `doctor result warn` should be considered reachable-and-ready, but surfaced with warning details. |

## Selected contract

### Request

```http
GET /healthz?check=record
X-Cassini-Timestamp: 1746979200
X-Cassini-Nonce: 0c1d62f69bb34e2d8a5941c7e0a2bb6a
X-Cassini-Signature: <hex-hmac-sha256>
```

### Default shallow request

```http
GET /healthz
X-Cassini-Timestamp: 1746979200
X-Cassini-Nonce: 8d9f9d4ab4c94842a5d3424b2fe74dc5
X-Cassini-Signature: <hex-hmac-sha256>
```

### Response shape

```json
{
  "ok": true,
  "auth": "ok",
  "check": "record",
  "result": "warn",
  "summary": "record doctor completed with warnings",
  "checks": [
    { "status": "ok", "summary": "working directory writable: /srv/cassini" },
    { "status": "warn", "summary": "working directory free space: 6.2 GiB available at /srv/cassini", "advice": "consider freeing space in /srv/cassini to avoid mid-run failures" }
  ]
}
```

### Minimal shallow response

```json
{
  "ok": true,
  "auth": "ok",
  "check": "shallow",
  "result": "ok",
  "summary": "operator reachable and request authenticated"
}
```

## Status semantics

Selected for D-263:

- `200 OK`
  - request authenticated and endpoint processed successfully
  - `result` conveys `ok`, `warn`, or `fail`
- `401 Unauthorized`
  - missing auth headers
  - invalid signature
  - stale timestamp
  - replayed nonce
- `500 Internal Server Error`
  - operator could not execute the requested health check

Important distinction:

- `200` with `"result":"fail"` means the operator is reachable and authenticated, but not ready for recording
- `401` means the shared-secret configuration is wrong or the request was rejected

This gives the settings UI the exact distinction it needs.

## Check modes

### `GET /healthz`

Purpose:

- cheap reachability and auth validation

Semantics:

- verifies signature
- returns that the operator is reachable
- does not run `doctor`

### `GET /healthz?check=record`

Purpose:

- real setup validation for recording

Semantics:

- verifies signature
- runs `cassini doctor --target record`
- returns the `doctor` result as structured health output

Why both modes are useful:

- shallow is cheap and useful for debugging URL/auth issues
- deep is the real admin “is this setup usable?” answer

## Why warnings should still count as healthy

`cassini doctor` already treats warnings as exit-success.
That is the right D-263 interpretation too.

So:

- `result=ok` means fully ready
- `result=warn` means usable but with caveats
- `result=fail` means not ready

This avoids overstating minor capacity warnings as a hard configuration failure.

## HMAC compatibility

The health endpoint should use the same canonical signing strategy selected for the trigger API:

```text
METHOD + "\n" +
PATH_WITH_QUERY + "\n" +
TIMESTAMP + "\n" +
NONCE + "\n" +
HEX(SHA256(BODY))
```

For `GET /healthz`, the body is empty, so the body digest is the SHA256 of empty bytes.

Why reuse the same scheme:

- one auth implementation on the Nextcloud side
- one verification middleware on the operator side
- no separate "lightweight" auth path that can drift or be misconfigured

## Implementation implications

1. Add shared auth verification middleware or helper in `cassini-operator`.
2. Add `GET /healthz` to the operator HTTP mux.
3. Implement:
   - shallow branch: auth + static operator response
   - record branch: auth + `cassini doctor --target record`
4. Return structured doctor checks instead of raw CLI text where practical.
5. Keep the endpoint stateless and non-persistent.

## Why not just shell out to `cassini doctor` from the Nextcloud app?

Because the app cannot reliably inspect:

- the operator's working directory
- the operator's temp directory
- the operator host's free space
- the exact runtime environment the operator uses

The health check has to run where the recording work will run.

## Acceptance

This spike is complete because it resolves the remaining setup-surface ambiguity:

- D-263 should add a dedicated `cassini-operator` health endpoint
- that endpoint should be signed exactly like the trigger API
- it should support both shallow auth/reachability and deep record-readiness
- the deep check should run `cassini doctor --target record`
- the Nextcloud admin settings page should use the deep check by default

## Reassessment

This removes the last major integration-shape ambiguity before slicing.

What remains is no longer "what surface should health use?"
It is:

- whether doctor output should be returned as raw lines or as structured checks
- whether the app settings page also exposes a shallow "debug auth/url only" button

Those are implementation details or UI polish, not unresolved architecture.
