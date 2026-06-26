# Production ExApp Recovery — Work Plan

Status: recovery plan split from store readiness
Phase: 2
Date: 2026-06-22

## Goal

Make the deployed Nextcloud AppAPI/HaRP ExApp record, build, publish, and preserve meetings reliably in production.

This is not the store submission checklist. It is the operational path required before store claims about production Talk recording can be trusted.

## Work Tracks

| Track | Outcome | Why It Matters |
|---|---|---|
| Production read-only diagnosis | Confirm the actual failure from container env, logs, Talk config, and persisted paths. | Avoid fixing the wrong layer based on local assumptions. |
| HPB-internal config parity | AppAPI can configure the same secrets the compose/harness paths pass. | Main defaults to HPB-internal; production must be able to supply its required secret. |
| Admin observability | `/operator/status` reports all required Talk config presence without leaking values. | Admins need to diagnose setup without shell access. |
| Install-doc alignment | Production docs explain the current HPB-internal path and known fallback behavior. | Current docs still describe old guest/public-only behavior. |
| Prod-path validation | Record through Talk button via AppAPI/HaRP, not through direct operator shortcuts. | This is the path production and the store install use. |
| Storage/archive validation | Verify AppAPI persistent storage paths and published catalog preservation. | A successful recording is not enough if the archive collapses or artifacts live somewhere admins do not back up. |

## Recovery Checklist

### 1. Read-Only Production Snapshot

- Record running image tag and app version.
- Confirm `APP_PERSISTENT_STORAGE` path.
- Confirm presence/absence, not values, for:
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
  - `CASSINI_TALK_BACKEND_URL`
- Fetch `/operator/status` through the AppAPI proxy.
- Capture latest failed job id, attempt id, and attempt logs.
- Capture Nextcloud `spreed.recording_servers`, `spreed.call_recording`, and `overwrite.cli.url`.

### 2. Confirm Or Disprove H1

- Search attempt logs for the expected missing-secret error.
- If H1 is confirmed, implement env parity before deeper recorder changes.
- If H1 is disproven, proceed through H2-H5 in `production-failure-hypothesis.md`.

### 3. Fix HPB-Internal ExApp Parity

- Add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to `appinfo/info.xml`.
- Decide whether storage override envs stay intentionally hidden from AppAPI or should become advanced options.
- Re-register/redeploy the ExApp with the internal secret provided through AppAPI.

### 4. Improve Admin Diagnosis

- Extend `/operator/status` with `talk.signaling_internal_secret_configured` or equivalent.
- Keep the response boolean-only; never return secret values.
- Include a failing status/detail if HPB-internal is the active/default mode and the secret is missing.

### 5. Align Install Docs

- Replace the public-room-only controlled test with an HPB-internal/default capture test.
- Document required standalone signaling/HPB internal secret.
- Document `CASSINI_TALK_BACKEND_URL` and `overwrite.cli.url` reachability requirements.
- Keep guest/public fallback wording only if the fallback is intentionally supported and tested.

### 6. Production-Shaped Test

- Use the AppAPI proxy URL as Talk's recording backend.
- Start recording from the Talk UI.
- Verify operator accepts the HMAC-authenticated request.
- Verify recorder joins through HPB-internal mode.
- Verify stop/empty-room lifecycle.
- Verify raw Talk upload and Cassini build/publish complete.
- Verify viewer/catalog output through the ExApp route.

### 7. Archive Preservation Check

- Compare `$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting` with `$APP_PERSISTENT_STORAGE/site/published/catalog.json`.
- If `current` contains only one meeting, decide whether to repair promotion/current retention or accept a one-meeting archive.
- If PR #79's hybrid direction is adopted, verify owner-scoped archive behavior and selected Files delivery separately from catalog preservation.

## Exit Criteria

- Root cause is confirmed from production evidence.
- Production ExApp can be configured without undeclared env injection.
- Status endpoint exposes enough config presence to diagnose Talk setup.
- Install docs match current capture behavior.
- A production-shaped Talk recording passes record → build → publish.
- Archive behavior is either verified as preserving intended meetings or explicitly fixed/deferred with a known limitation.
