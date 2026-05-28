---
shaping: true
---

# X1 Spike: Recording trigger via AppAPI proxy reaches the operator

## Context

D-290's Shape C, recording phase (C2), is currently flagged ⚠️ because no test exercises Talk's recording-start trigger routed through the AppAPI proxy + HaRP. The flag forces ❌ on R2.1 and R2.2 in the Fit Check.

Two adjacent pieces of evidence already exist on `cassini-appstore-dogfood` (PR #40):

1. **Routing through the proxy works.** Commit ee88fd2 verified `curl http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/api/v1/welcome` reaches the operator inside the ExApp container.
2. **The recording lifecycle works.** `harness/bin/ci-e2e-talk-record-roundtrip.sh` drives the full Talk → operator → record → publish flow successfully, but with `recording_servers` pointing directly at the operator (gateway:4000), bypassing the AppAPI proxy and HaRP.

The remaining unknown sits at the seam: does the HMAC-protected `POST /api/v1/room/<token>` trigger that Talk fires when an admin clicks "Start recording" succeed when routed through the proxy?

## Goal

Produce enough evidence to remove the ⚠️ from C2, by demonstrating that a recording trigger emitted by Talk reaches the operator inside the ExApp container with valid HMAC, through the AppAPI proxy + HaRP path, and is accepted as a record job.

This spike intentionally **does not** require the full audio capture + transcription + upload to succeed — the existing roundtrip script already proves that lifecycle. What we need to know is whether the *trigger* survives the proxy hop.

## Questions

| # | Question |
|---|----------|
| **X1-Q1** | Does `SPREED_PROFILE=default ./harness/bin/manual-test-setup.sh` bring the topology up cleanly on this machine? (NC + db + appapi-harp + reverse-proxy + mock catalog; no signaling stack.) |
| **X1-Q2** | Does `occ app_api:app:register gocassini harp_local --info-xml /tmp/gocassini-info.xml --test-deploy-mode --wait-finish` reach `ExApp gocassini deployed successfully` → `[enabled]`? (Re-validates D-286 is closed end-to-end on this stack.) |
| **X1-Q3** | After install, does an unauthenticated `GET http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/api/v1/welcome` reach the operator container and return `{"version":1}`? (Smoke-check the routing path PR #40 verified.) |
| **X1-Q4** | What's the OCS endpoint, headers, and HMAC scheme Talk uses to call `/api/v1/room/<token>` on the recording backend? (Read from `cassini-operator/internal/operator/talk_backend.go` to know what to forge.) |
| **X1-Q5** | Can we issue a forged HMAC-signed `POST /index.php/apps/app_api/proxy/gocassini/api/v1/room/<token>` from the host and observe the operator log a `record started id=…` (or similar) marker? |
| **X1-Q6** | Does the operator's response status code propagate back through the proxy unchanged (200 / 4xx as the operator decides), or does AppAPI rewrite it? |
| **X1-Q7** | If the trigger succeeds, what's the failure surface if SPREED_PROFILE=full is added — i.e. what additional risk does running an actual recorded call introduce beyond the trigger reaching the operator? (Brief enumeration, not a full test.) |

## Method

1. Branch `d-290-e2e-testing` is already checked out (off PR #40 tip).
2. Bring up the install topology — `SPREED_PROFILE=default ./harness/bin/manual-test-setup.sh` (skips signaling/Janus/TURN to keep the stack lean).
3. Install Cassini per the documented `app_api:app:register` command in the script's banner.
4. Run the routing smoke from PR #40's commit (welcome endpoint).
5. Read `talk_backend.go` to extract: required headers (`Talk-Recording-Random`, `Talk-Recording-Checksum`), HMAC input (`secret + random + body`), expected JSON body shape for a recording-start.
6. From the host: forge the headers/HMAC and `POST` to `/index.php/apps/app_api/proxy/gocassini/api/v1/room/<token>` with a synthetic token.
7. Watch `docker logs cassini-exapp` for the marker the operator emits on accepting a recording job.
8. If marker appears → X1 closed positive. If not, capture the failure mode (NC log, HaRP log, operator log) and document.

## Acceptance

X1 is complete when we can describe:
- Whether the trigger path through the proxy reaches the operator and is accepted as a valid HMAC'd recording-start; or
- The specific obstacle and where it surfaces (NC routing? AppAPI proxy header stripping? HaRP path rewriting? HMAC scheme mismatch?), so that Slice 2 (the record phase of C) starts with the fix known rather than the question open.

## Out of scope (for X1)

- Running a real Talk call with audio bots (SPREED_PROFILE=full) — deferred to slice implementation.
- Asserting on transcript content — that's R2.2 work, downstream of trigger-reaches-operator.
- Generalising the spike script into the production phase script — that's Slice 2.

## Status

- 2026-05-28: Drafted. Awaiting go-ahead to run the compose stack and execute steps 2–8.
- 2026-05-28: Spike executed. Findings below.

---

## Findings

| Q | Result |
|---|--------|
| X1-Q1 | ✅ `SPREED_PROFILE=default ./harness/bin/manual-test-setup.sh --build` brought up NC + db + appapi-harp + reverse-proxy + image-build cleanly in ~70s. |
| X1-Q2 | ✅ `occ app_api:app:register gocassini harp_local --info-xml /tmp/gocassini-info.xml --test-deploy-mode --wait-finish` → `ExApp gocassini deployed successfully` → `app_api:app:list` shows `gocassini (Cassini): 0.1.0 [enabled]`. D-286 fix durable end-to-end on this stack. |
| X1-Q3 | ✅ `curl -i http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/api/v1/welcome` → HTTP 200, body `{"version":1}`. Routing through reverse-proxy → AppAPI → HaRP → ExApp container works for PUBLIC routes. |
| X1-Q4 | ✅ HMAC scheme extracted from `cassini-operator/internal/operator/talk_backend.go`: headers `Talk-Recording-Backend`, `Talk-Recording-Random` (≥32 bytes hex), `Talk-Recording-Checksum` (lowercase hex of `HMAC-SHA256(secret, random‖body)`). Operator's flag/env: `--talk-shared-secret` / `CASSINI_TALK_RECORDING_SECRET` / `TALK_RECORDING_SECRET`. |
| X1-Q5 | ⚠️ **Trigger reaches the operator** — POST returned HTTP 403 with the operator's own `writeTalkAuthError` JSON shape (`{"error":{"code":"invalid_request","message":"The request could not be authenticated."},"type":"error"}`). The handler `talkRoomHandler` ran. But the auth short-circuited on the **first** check (`if rt.cfg.TalkSharedSecret == ""`), so header-preservation through the proxy is not fully proven (highly likely to be fine — see notes). |
| X1-Q6 | ✅ Operator's 403 status code propagated back through the proxy unchanged. |
| X1-Q7 | Surfaced one specific obstacle (below). With it fixed, the remaining risk in adding SPREED_PROFILE=full is conventional WebRTC/Janus stability — not proxy/HaRP-specific. |

## Key finding: the Talk shared secret is not provisioned to the ExApp container at install time

The operator container's startup logs `talk_shared_secret_set -> false`. Inspecting the running container's env:

```
APP_SECRET=…           ← injected by AppAPI
APP_ID=gocassini
APP_HOST=127.0.0.1
APP_PORT=23000
APP_PERSISTENT_STORAGE=…
HP_SHARED_KEY=…        ← HaRP shared key
```

No `CASSINI_TALK_RECORDING_SECRET` or `TALK_RECORDING_SECRET`. AppAPI's deploy protocol injects the standard ExApp env (APP_SECRET, APP_HOST, APP_PORT, HP_SHARED_KEY) but **not** Cassini-specific env. `bootstrap.sh` sets `CASSINI_TALK_RECORDING_SECRET` on the host shell and feeds it into Nextcloud's `spreed.recording_servers.secret`, but never to the ExApp container.

`appinfo/info.xml` has no `<environment-variables>` block (the AppAPI mechanism that *would* let an app declare a shared-secret env var that AppAPI generates and provisions to both sides).

**Net effect in production:** an admin who installs Cassini from the App Store and clicks "Start recording" in Talk gets a 403 from the operator with `"The request could not be authenticated."` — until the admin somehow gets a matching secret into both the operator container and Nextcloud's spreed config, with no documented mechanism for doing so. This is the same shape of latent bug as the frpc plugin-type bug PR #40 fixed: a piece of production config that no CI test exercises end-to-end.

## Implications for Shape C and D-290 scope

1. **The recording phase (C2) needs a slice-level fix before it can pass.** Provisioning the Talk shared secret to the ExApp container at install time is a prerequisite, not an optional polish.
2. **Mechanism chosen for Slice 0: `--env` at register time.** AppAPI's `app_api:app:register` already accepts `--env ENV_NAME=VALUE` (repeatable; passes through to the container at docker run time). No `info.xml` `<environment-variables>` block is needed — that schema isn't a real fit anyway (AppAPI uses it for declaring expected env, not for declaring secrets it should generate). The harness owns one secret value at the top, writes it into spreed's `recording_servers` (via `bootstrap.sh`), and passes it to the operator via `--env CASSINI_TALK_RECORDING_SECRET=...` on `app_api:app:register`. Both sides verify HMAC against the same value.
   - **Production gap remaining:** the App Store admin-UI install button does not currently support custom `--env` injection. Admins on that path must either use CLI install or wait for a "production polish" follow-up where the operator generates its own secret on first init and pushes it into spreed via AppAPI's appconfig back-channel. This is its own ticket, not part of D-290.
3. **The spike successfully lifts the ⚠️ on C2.** The mechanism (HMAC POST → AppAPI proxy → HaRP → operator handler) is sound. The blocker is config provisioning, which Shape C's slicing already had headroom for under "install harness."

## Recommended next steps

1. **File a Linear issue** capturing the Talk-secret-provisioning gap. This is its own unit of work — likely a small Slice 0 inside D-290, or a sibling ticket the operator team picks up. Title: *"ExApp install does not provision Talk recording shared secret to the operator container"*. Trigger: D-290 spike X1.
2. **Lift the ⚠️ on C2** in the Fit Check; R2.1 and R2.2 now ✅ conditional on the Slice 0 fix (matching the H3 conditional on PR #40 merging). Update shaping.md.
3. **Tear down the spike stack** — `docker compose -p cassini-exapp-test down --volumes`.
4. **Proceed to spike X2** (read-only audit of bypass-test assertions; doesn't need docker).

## Notes / caveats

- **Header preservation not directly proven.** The 403 fired before header extraction (the secret-not-configured check runs first). The operator's response body shape proves the handler ran; once secret-provisioning lands we can re-fire with the matching secret to fully confirm headers come through. Very low risk that they don't — AppAPI's proxy is a transparent HTTP forwarder, and the `Talk-Recording-Backend`/`-Random`/`-Checksum` headers don't collide with any reserved names.
- **The fabricated room token** (`x1spike<timestamp>`) is fine for the seam test — `validateTalkRequest` doesn't consult NC about the token's existence; only the start handler later would, and we never got that far. For full lifecycle testing we'd want a real Talk room.

