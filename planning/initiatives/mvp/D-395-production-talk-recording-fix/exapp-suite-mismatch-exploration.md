---
exploration: true
---

# Container Suite vs Deployed ExApp Mismatch — Exploration

Last updated: 2026-06-22, full pass with authenticated PR metadata

## Purpose

Capture the current understanding of why the production ExApp path does not feel like the dev/operator suite path, especially around:

- persistent storage and published meeting archives;
- the Talk recording-backend contract that stores files back into Nextcloud Files;
- HPB-internal/private meeting capture configuration;
- whether Cassini should keep its own storage as the source of truth or move built artifacts into Nextcloud storage after the operator pipeline completes.

This note is based on local `main`, the production-facing install docs, authenticated GitHub PR metadata/diffs for open/recent PRs, and code inspection of the current ExApp/runtime path. It does **not** include direct access to the production AppAPI Docker host.

Related notes:

- `planning/initiatives/production-exapp-recovery/production-failure-hypothesis.md` covers the active production-failure hypothesis and validation plan.
- `planning/initiatives/production-exapp-recovery/archive-overwrite-hypothesis.md` covers the separate archive-overwrite hypothesis.

Reconciliation note, 2026-06-22: `origin/main` now includes PR #40, selected ExApp readiness PRs #53-#78, PR #42 (`feat/d-283-nextcloud-internal-audio-capture`), PR #43, and PR #45 (`feat/d-288-1-1`). The App Store package stubs, manual HaRP dogfood setup, embedded UI entries, status endpoint, HPB-internal code path, and private/1:1 harness validation are no longer speculative branch work. The remaining ExApp readiness gaps are signed archive/release automation, public release/support surfaces, `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` manifest/docs/status parity, stale install docs, storage/source-of-truth, and viewer ACLs.

---

## Executive summary

The ExApp is not deployed like the standalone dev/operator suite. In dev compose, Cassini is three services with explicit volumes and direct env pass-through. In production AppAPI, Cassini is one AppAPI-managed container that serves the operator API, control panel, viewer, and published assets itself, and it relies on AppAPI's `APP_PERSISTENT_STORAGE` volume.

The storage model is mostly equivalent in intent — both keep a Cassini-owned operator work root plus a published-site root — but the ExApp currently has several product/setup mismatches:

1. **Env var parity gap:** the dev compose path passes `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, but `appinfo/info.xml` does not declare it. AppAPI drops undeclared env vars, so the production ExApp cannot be configured for the now-default `hpb-internal` recorder path through the normal AppAPI UI/registration surface.
2. **Docs/code mismatch:** `docs/exapp-install.md` still describes the old public-conversation/guest-recorder limitation, while merged D-283 code defaults Talk recording jobs to `hpb-internal` and requires the signaling internal secret.
3. **Storage expectation mismatch:** native Talk recording expects files to land in the recording owner's Nextcloud Files (`Talk/Recording/<token>`). Cassini currently treats the Cassini operator volume as source-of-truth and uploads the raw `.mkv` to Talk as a compatibility/delivery side effect. The rich artifacts (`meeting.webm`, transcript, captions, summary, published viewer bundle) remain in Cassini storage on `main`.
4. **Access mismatch:** `main` still exposes `/published/*` as USER-visible archive content. Any logged-in user admitted by the AppAPI proxy can reach the published archive. Open PR #79 changes this to owner-scoped and adds post-build Nextcloud delivery for selected rich artifacts.
5. **Deployment topology mismatch:** standalone viewer/control-panel services are separate containers in dev compose; ExApp bundles those assets and serves them from the operator. This is current `main` behavior, including AppAPI top-menu entries and embedded-page assets, so troubleshooting must start from the ExApp operator container, not from separate viewer/control-panel services.

If the desired behavior is "the ExApp should behave like the standalone operator suite for now", then the near-term target is: keep Cassini-owned storage as the source of truth, make ExApp env/config/storage parity match compose, and treat Talk store uploads as delivery side effects. The longer-term target may instead be Nextcloud-native storage after build; PR #79 is already a concrete spike/partial implementation of that direction.

---

## Current runtime shapes

### A. Standalone/dev operator suite (`deployment/compose.yml`)

Services:

- `cassini-operator` — Go operator + recorder/publisher subprocesses.
- `cassini-control-panel` — separate admin UI service.
- `cassini-viewer` — separate Nginx viewer service.

Storage:

- `cassini_operator_state` mounted at `/var/lib/cassini-operator`.
  - `jobs.sqlite3`
  - `jobs/` attempt directories
  - canonical `jobs/current/*.run` and `jobs/current/*.meeting`
- `cassini_published_site` mounted at `/srv/cassini-site`.
  - operator writes `/srv/cassini-site/published`
  - viewer reads `/srv/cassini-site:ro`

Config/env:

- Compose passes all relevant operator/recorder env directly, including:
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
  - `CASSINI_TALK_BACKEND_URL`
  - LLM envs
- The operator API binds to port 4000, viewer to 8765, control panel to 4173.
- No AppAPI proxy/middleware is involved unless a harness specifically installs the ExApp.

Operational consequence: this is a Cassini-owned storage model. Nextcloud Files is not the authoritative store for the rich meeting archive.

### B. Production ExApp/AppAPI path (`appinfo/info.xml`, `deployment/Dockerfile.exapp`, `deployment/exapp-start.sh`)

Services:

- One AppAPI-managed Docker container.
- The container runs `cassini-operator` only.
- The operator serves:
  - `/operator/*` admin JSON/SSE API;
  - `/control-panel/*` admin static UI;
  - `/viewer/*` user viewer SPA;
  - `/published/*` published meeting files;
  - `/api/v1/welcome` and `/api/v1/room/*` public Talk recording-backend endpoints.

Storage:

- AppAPI creates/mounts one persistent volume and exposes it as `APP_PERSISTENT_STORAGE`.
- The entrypoint/operator redirect default paths under that volume:
  - `$APP_PERSISTENT_STORAGE/operator/jobs.sqlite3`
  - `$APP_PERSISTENT_STORAGE/operator/jobs`
  - `$APP_PERSISTENT_STORAGE/site/published`
  - `$APP_PERSISTENT_STORAGE/operator/app-state.json`
- The Docker image also bakes fallback defaults:
  - `CASSINI_OPERATOR_DB_PATH=/var/lib/cassini-operator/jobs.sqlite3`
  - `CASSINI_OPERATOR_WORK_ROOT=/var/lib/cassini-operator/jobs`
  - `CASSINI_OPERATOR_SITE_ROOT=/srv/cassini-site/published`
- Under AppAPI, those exact fallback defaults are treated as "not explicitly configured" and redirected under `APP_PERSISTENT_STORAGE`.

Config/env:

- AppAPI injects `APP_*`, `HP_*`, `NEXTCLOUD_URL`, and admin-supplied envs **only if declared in `appinfo/info.xml`**.
- Current `info.xml` declares:
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_BACKEND_URL`
  - `OPENROUTER_API_KEY`
  - `LLM_BASE_URL`
  - `LLM_MODEL`
  - `CASSINI_OPERATOR_API_TOKEN`
- Current `info.xml` does **not** declare:
  - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
  - `CASSINI_OPERATOR_DB_PATH`
  - `CASSINI_OPERATOR_WORK_ROOT`
  - `CASSINI_OPERATOR_SITE_ROOT`
  - `CASSINI_DELIVER_MEETING_ARTIFACTS` (added by PR #79, not on `main`)

Operational consequence: storage persistence is automatic, but storage overrides and HPB-internal capture secrets are not configurable through the normal ExApp surface today.

### C. Harness/prod-path ExApp testing (`harness/bin/manual-test-setup.sh`, PR #41)

The harness is not the standalone operator suite. It can provision Nextcloud, AppAPI/HaRP, reverse proxying, signaling/Janus/NATS in full-profile runs, and install the ExApp through the same proxy/HMAC shape production uses.

Current `main` already has PR #40's manual dogfood path:

- `harness/bin/manual-test-setup.sh` builds/tags the ExApp image using `info.xml`'s pinned image tag.
- It starts Nextcloud, database, AppAPI HaRP, and a reverse proxy.
- It registers a HaRP deploy daemon and maps `ghcr.io` to the local image so AppAPI can consume `info.xml` verbatim.
- It installs by `occ app_api:app:register gocassini harp_local --info-xml /tmp/gocassini-info.xml --test-deploy-mode --wait-finish`, which is the development equivalent of the store Install button's registration path.
- It wires Talk's `recording_servers` at the AppAPI proxy URL for Cassini.
- It can use `SPREED_PROFILE=full` to bring up signaling/Janus/TURN for real Talk record-button testing.

What is still not on `main`: an automated HaRP-fronted CI job. `docs/exapp-test-locally.md` still accurately says the production-shaped HaRP tier is not automated, but it should be updated to point at the newer manual dogfood script.

PR #41 (`D-290`) is the important open prod-path e2e work. Authenticated PR metadata confirms it is still an open draft. Its PR body reports:

- AppAPI/HaRP install path is green in CI.
- `/api/v1/welcome` through the proxy is green.
- Talk record-button trigger reaches the operator through proxy/HMAC and the operator accepts the job.
- The full recording lifecycle is still red in the branch, currently around recorder/Nextcloud `participants/active` behavior.

It also surfaced real production install requirements:

- `CASSINI_TALK_RECORDING_SECRET` must be provisioned and match Talk `recording_servers.secret`.
- `CASSINI_TALK_BACKEND_URL` must be a URL the ExApp container can dial back to Nextcloud.
- Nextcloud `overwrite.cli.url` must point at a URL reachable by the ExApp/recorder.

This PR is valuable because it exercises the ExApp shape rather than the dev compose shape, but it should not be described as landed CI coverage yet.

---

## Talk recorder protocol vs Cassini storage

### What native Talk expects

Nextcloud Talk's recording backend protocol includes a store endpoint:

```text
POST /ocs/v2.php/apps/spreed/api/v1/recording/<token>/store
```

Cassini calls that endpoint with HMAC headers and multipart fields including `owner` and `file`. Spreed then files the upload in the recording owner's Nextcloud Files area, under the Talk recording folder for the room/token.

That is the native Talk expectation: recording artifacts end up in Files, under Nextcloud's quota/backup/sharing/audit model.

### What Cassini does on `main`

Cassini currently has two storage flows:

1. **Cassini-owned source-of-truth storage**
   - Raw run bundle: `recording.mkv` and session artifacts under operator work root.
   - Built meeting bundle: `meeting.webm`, transcripts, captions/summary when available, manifest, etc.
   - Published static site: `catalog.json` plus meeting assets under site root.

2. **Talk delivery side effect**
   - After recording succeeds and the run bundle is promoted, the operator sends Talk `stopped` and uploads raw `recording.mkv` to the Talk store endpoint.
   - This is best-effort and retried; failure leaves `talk_delivered_at` unset and reruns can retry it.
   - It intentionally does not make the record/build/publish pipeline fail because the canonical run bundle is already safe in Cassini storage.

Important ordering on `main`:

```text
record raw media
→ promote canonical run bundle in Cassini work root
→ Talk stopped callback + raw .mkv upload to Nextcloud Files
→ build rich meeting bundle in Cassini work root
→ publish static archive from Cassini current/*.meeting
```

So the raw `.mkv` reaches Nextcloud Files before build, but the clean/rich artifacts do not.

### The disconnect

From a Nextcloud admin's perspective, the official recorder's storage is Nextcloud Files. From Cassini's current architecture, Nextcloud Files is only a delivery target for the raw file; Cassini storage is the durable source of truth for the rich archive.

That disconnect is tolerable only if we state it clearly and make the ExApp behave like the dev/operator suite:

- Cassini storage is authoritative;
- Nextcloud Files upload is a protocol/delivery compatibility feature;
- backups/retention for transcripts and published meetings must include the ExApp persistent volume;
- Files will not contain the full Cassini experience unless/until we add post-build delivery.

---

## Discrepancy matrix

| Area | Standalone/dev operator suite | ExApp/AppAPI production | Current discrepancy / risk |
|---|---|---|---|
| Service topology | 3 services: operator, control panel, viewer | 1 container: operator serves APIs and static assets | Troubleshooting and storage sharing differ; no separate viewer container in ExApp. |
| Operator base path | Usually `/` in compose | Baked `CASSINI_OPERATOR_BASE_PATH=/operator` for AppAPI routes | Correct for ExApp; can confuse direct curl/testing if not using `/operator/*`. |
| Persistent work root | `/var/lib/cassini-operator` volume | `$APP_PERSISTENT_STORAGE/operator/*` via redirect | Equivalent intent, different paths. Need production verification of effective paths. |
| Published site root | `/srv/cassini-site/published`, shared with viewer volume | `$APP_PERSISTENT_STORAGE/site/published`, served by operator at `/published/*` | Equivalent intent, but no separate viewer service. Archive overwrite/debug must inspect ExApp path. |
| Storage overrides | Compose can mount/override volumes directly | Env overrides exist in code but are not declared in `info.xml` | Admin cannot set `CASSINI_OPERATOR_*` through AppAPI UI/registration today. |
| Talk recording secret | Passed by compose | Declared in `info.xml` | Parity OK. |
| HPB signaling internal secret | Passed by compose as `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | Not declared in `info.xml` | Major gap: default `hpb-internal` capture cannot be configured via AppAPI. Likely cause of Talk recording failures in ExApp after D-283 unless secret was injected by some nonstandard path. |
| Talk backend URL | Passed by compose | Declared in `info.xml` | Parity OK, but must be reachable from ExApp container. PR #41 found this is a production requirement. |
| Capture mode | D-283 default `hpb-internal`, fallback `guest-participant` | Operator also forces Talk-backend jobs to `hpb-internal` | ExApp docs still describe guest/public-only limitation; env declaration does not support default mode. |
| Talk raw upload | Best-effort upload of `recording.mkv` after record | Same code path | Upload to Files is not the Cassini source-of-truth; UI may still show Talk-level failure if callbacks/upload fail. |
| Rich artifact delivery | None on `main` | None on `main` | Transcripts/summaries/site stay only in Cassini volume. PR #79 adds selected post-build delivery. |
| Published archive access | Direct viewer service, effectively whoever can reach it | USER route through AppAPI, any logged-in user can reach `/published/*` on `main` | Org-wide archive exposure on `main`. PR #79 owner-scopes it. |
| Backup/retention | Docker volumes outside Nextcloud Files | AppAPI volume outside Nextcloud Files | Neither is covered by Nextcloud Files backup/quota unless admin backs up the volume separately. |
| Archive publish source | `cassini publish <work-root>/current` | Same | Publish only includes bundles present in `current`; if current collapses to latest bundle, archive collapses. See overwrite hypothesis doc. |

---

## Likely reason for manual Talk "recording failed" after room-empty auto-stop

The immediate recording-failure symptom still needs production logs, but the strongest setup mismatch found locally is:

- `cassini-operator/internal/operator/talk_backend.go` creates Talk-triggered jobs with `TalkAuthMode: hpb-internal`.
- `cassini-go-recorder/internal/talk/recorder.go` validates that `hpb-internal` has both:
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
- `deployment/compose.yml` passes `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
- `appinfo/info.xml` does **not** declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, so AppAPI will not pass it from admin deploy options.

Expected log if this is the failure:

```text
talk auth mode hpb-internal requires CASSINI_TALK_SIGNALING_INTERNAL_SECRET to be set
```

Other possible causes remain:

- `CASSINI_TALK_BACKEND_URL` points to a URL unreachable from the ExApp container;
- Nextcloud `overwrite.cli.url` advertises an unreachable URL to the recorder;
- Talk `stopped` callback or `/store` upload fails after room-empty stop;
- final MKV composition fails (`compose final output failed`), causing the operator to notify Talk `failed`.

Production validation should inspect the ExApp container env presence and latest job/attempt logs before changing behavior.

---

## Storage/product decisions to make

### D1 — What is the authoritative storage layer?

Options:

1. **Keep Cassini storage authoritative for MVP**
   - ExApp AppAPI volume / compose volumes remain the source of truth.
   - Nextcloud Files receives raw `.mkv` and maybe selected exports as delivery side effects.
   - Pros: closest to current dev/operator behavior; least rewrite; preserves static archive model.
   - Cons: admin surprise; backups/quota/deletion are outside standard Nextcloud Files; duplicate storage.

2. **Move built artifacts to Nextcloud Files as the source of truth**
   - Operator work root becomes staging/cache only.
   - After build, deliver a complete meeting artifact/bundle to owner's Files.
   - Viewer resolves a file/share reference instead of a global `/published` catalog.
   - Pros: aligns with native Talk storage expectations, Nextcloud ACLs, backup/quota/sharing.
   - Cons: requires viewer redesign, deletion/retention semantics, bundle format choice, migration path.

3. **Hybrid transition**
   - Keep Cassini archive for now.
   - Add post-build delivery of clean/rich artifacts to Files.
   - Owner-scope `/published` to reduce privacy exposure.
   - Later retire global archive if/when per-file viewer exists.
   - Pros: incremental; PR #79 already implements much of it.
   - Cons: two copies remain; user may see raw `.mkv`, clean `.webm`, and viewer archive as separate things.

Recommended near-term stance if we want ExApp parity with container services: **Option 1 with explicit docs**, plus selected Option 3 improvements only if we accept the hybrid model.

### D2 — Should raw `.mkv` still upload immediately after recording?

Current behavior uploads raw `.mkv` before build because that matches the Talk recording-backend contract and lets Talk complete its native recording lifecycle.

If we choose "upload only after build", we need to verify whether Talk can tolerate a delayed or absent store upload after the `stopped` callback. Risk: Talk UI may show recording failed or hang if the backend does not provide a file in the expected lifecycle.

Safer split:

- keep raw `.mkv` upload immediately after record for Talk protocol compatibility;
- deliver clean/rich Cassini artifacts after build;
- later decide whether to suppress/supersede raw `.mkv` if Talk supports it.

### D3 — What gets delivered to Nextcloud Files after build?

Candidate artifacts:

- `meeting.webm` clean playable audio/video/audio-only asset;
- `summary.md` when LLM produced one;
- `transcript.words.v1.json` and/or readable transcript;
- captions (`captions.vtt`);
- a portable/self-contained bundle (`.zip`, `.cassini`, or single HTML);
- a pointer/link into the Cassini viewer.

PR #79 delivers only `meeting.webm` and `summary.md`. That is useful but not the full Cassini experience.

### D4 — What happens to `/published/*`?

Options:

- keep global archive as-is (current `main`, privacy risk);
- owner-scope it (PR #79);
- retire it and make viewer per-meeting/per-file.

If Cassini storage remains authoritative, owner-scoping is the minimum privacy fix. If Nextcloud Files becomes authoritative, `/published/*` should likely stop being a global archive.

### D5 — Should ExApp expose storage path overrides?

Code supports `CASSINI_OPERATOR_DB_PATH`, `CASSINI_OPERATOR_WORK_ROOT`, and `CASSINI_OPERATOR_SITE_ROOT`, but ExApp does not declare them.

Decision:

- If we want simple App Store installs: keep automatic `APP_PERSISTENT_STORAGE` only and document that backups must include the AppAPI volume.
- If we want parity with advanced container deployment: declare the storage override envs in `info.xml` and document the risks.

### D6 — How do we handle deletion/retention?

Neither storage model is complete without:

- owner/admin delete semantics;
- pruning/retention policy;
- what happens to raw `.mkv` vs clean/rich artifacts;
- whether deleting a Files artifact should also delete Cassini work/published copies.

---

## Spike: Can we use Nextcloud storage as intended, but only after build?

### Spike hypothesis

Cassini can keep the operator work root as staging during record/build, then deliver built artifacts into Nextcloud Files only after the build stage succeeds. This would preserve pipeline determinism and avoid exposing partial/failed artifacts in Files.

### Proposed lifecycle

```text
Talk start
→ record into Cassini work root
→ promote canonical run bundle
→ send Talk stopped + upload raw .mkv if required by Talk protocol
→ build rich meeting bundle in Cassini work root
→ deliver selected built artifacts to owner's Nextcloud Files
→ mark rich delivery complete/idempotent
→ publish/update Cassini archive if we are still keeping it
```

If the product decision is "no Files upload before build", then this spike must first answer whether Talk permits skipping the raw upload at record stop. Until proven, keep raw upload for protocol compatibility and treat post-build delivery as the storage-modernization path.

### Delivery mechanisms to compare

1. **Talk store endpoint reuse**
   - Already implemented for raw `.mkv`.
   - PR #79 reuses it for `meeting.webm` and `summary.md`.
   - Pros: files land in the Talk recording folder and inherit Talk's share-offer behavior.
   - Cons: endpoint may be optimized for individual files, not structured bundles.

2. **WebDAV upload as owner/app**
   - Could upload a folder/bundle directly to Files.
   - Pros: more control over layout.
   - Cons: needs auth/token model and ACL decisions; less "native Talk recorder" semantics.

3. **Single portable bundle**
   - Upload one `.zip`, `.html`, or custom bundle file after build.
   - Pros: works with single-file store endpoints; avoids folder consistency problems.
   - Cons: viewer UX and preview/open behavior need design.

### Acceptance questions

- Does Talk mark a recording failed if the store upload is delayed until after build or omitted?
- Can Talk's store endpoint accept multiple files with meaningful names/kinds without confusing the Talk UI?
- Where exactly do files land for multiple uploads, and how does Talk present/share them?
- Can rerun idempotently replace or version delivered artifacts?
- What does deletion mean across Files and Cassini work root?
- What should the viewer open: a Cassini-served `/published` route, a Files URL, or a self-contained artifact?

---

## Open/recent PR reconnaissance

### Merged ExApp readiness baseline — PR #40 and selected PRs #53-#78

Status: merged to `main`.

Relevant outcomes now on `main`:

- PR #40 added basic App Store package files (`appinfo/app.php`, `img/app.svg`), AppAPI PUBLIC Talk routes, HaRP/frpc fixes, and the local App Store dogfood setup in `harness/bin/manual-test-setup.sh`.
- PR #53 hardened the production image/entrypoint path.
- PR #54 declared ExApp environment variables and moved init-progress reporting to the v2 endpoint.
- PR #55 defaulted data roots under `APP_PERSISTENT_STORAGE`.
- PR #60 rewrote `docs/exapp-install.md` as the production install/Talk handoff guide, though its Talk limitation section is now stale after D-283.
- PR #62 pinned release image tags to app versions.
- PR #68 added `/operator/status` doctor output and handoff hardening.
- PR #70 added Nextcloud top-menu entries.
- PR #76 and PR #77 embedded the viewer and control panel on AppAPI pages.
- PR #78 isolated embedded viewer/control-panel CSS in Shadow DOM.

Planning implication: do not treat those as future work. The remaining readiness work is release packaging/signing, public release/support surfaces, current HPB-internal env/docs/status parity, privacy/storage decisions, and store-grade validation/docs.

### PR #79 — `feat: owner-scoped recordings + Nextcloud-native delivery of clean audio & summary`

Status: open, not draft.

This is directly relevant to the storage/access question.

What it adds:

- Owner-scoped published archive: `/published/catalog.json` is filtered to the calling Nextcloud user; another owner's per-meeting assets 404.
- Derives owner from persisted `talk_binding`; no new owner migration needed for scoping.
- Adds post-build delivery of rich artifacts to Nextcloud Files:
  - `meeting.webm` always;
  - `summary.md` when present.
- Delivery uses the same Talk store endpoint as raw recording upload.
- Delivery happens after build and before publish.
- Adds `talk_artifacts_delivered_at` migration for idempotency.
- Adds `CASSINI_DELIVER_MEETING_ARTIFACTS`, default true, declared in `info.xml` in the PR.
- Updates docs/manifest language away from org-wide archive.

Why it matters:

- It is the clearest existing implementation of the proposed spike: built artifacts are delivered after the operator pipeline produces them, not before.
- It chooses the hybrid path: keep Cassini archive but owner-scope it, and also deliver selected Files artifacts.

Open concerns:

- It does not deliver the full transcript/caption/viewer bundle to Files.
- It keeps raw `.mkv` plus clean `.webm`, so users may see duplicates.
- It does not retire `/published`; it scopes it.
- Needs review/rebase against current `main` and product decision on whether hybrid is desired.

### PR #41 — `D-290: full-path e2e for Cassini ExApp install + recording`

Status: open draft.

Relevant because it tests the AppAPI/HaRP production shape instead of bypassing it.

Findings from the PR body and current PR state:

- AppAPI/HaRP install path is green in CI on the branch.
- Welcome route through the proxy is green.
- Talk record-button trigger reaches the operator through proxy/HMAC and the operator accepts the job.
- Full recording lifecycle is still red in the branch, around recorder/Nextcloud `participants/active` behavior.
- Real production requirements surfaced:
  - recording secret must be provisioned;
  - Talk backend URL must be reachable from ExApp container;
  - `overwrite.cli.url` must be reachable from ExApp container.

Use this PR to harden the ExApp path after fixing env parity, but do not count it as landed CI coverage yet.

### PR #42 — `D-283 Nextcloud internal audio capture`

Status: merged.

Relevant because it changed the default capture mode to HPB-internal.

Key outcome:

- Adds `hpb-internal` mode beside `guest-participant`.
- Makes `hpb-internal` the default for Talk recording jobs.
- Requires standalone signaling/HPB and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
- Supports restricted/private meetings in principle by joining as an internal client rather than as a guest participant.

ExApp gap left behind:

- The compose/harness paths pass the internal secret.
- `appinfo/info.xml` on `main` does not declare it, so AppAPI production cannot configure it normally.

### PR #45 — `Feat/d 288 1 1`

Status: merged to `origin/main` as of 2026-06-22.

Relevant to private meeting recordings and harness/dev validation.

What it explores/implements:

- `cassini dev play-private` surface.
- Authenticated Nextcloud users for simulated private participants.
- Idempotent 1:1 Talk conversation creation via `roomType=1&invite=<user>`.
- Recording gate: join/activate call, start Talk integrated recording through Nextcloud, wait until `callRecording == 1`, then start media.
- Validation doc reports successful synthetic and admin 1:1 jobs with non-empty transcripts.

Why it matters:

- It proves the Nextcloud API path for private 1:1 playback/recording can work without directly calling the operator API.
- It is harness/dev tooling, not yet a production ExApp storage/access fix.

### PR #80 and PR #81 — D-386 capture reliability

Status: open, not draft.

Relevant indirectly.

- #80 surfaces in-call participants with no/low captured audio as visible warnings. It is detection-only.
- #81 retries/rebuilds subscribers that captured no media. It is the recovery side of the D-386 fix.

These do not solve storage mismatch, but they reduce the risk that a Talk recording "succeeds" while missing a participant.

---

## Recommended next actions

1. **Production read-only validation**
   - Check effective `APP_PERSISTENT_STORAGE`, work root, site root.
   - Check whether `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is set in the ExApp container without printing the value.
   - Inspect latest failed Talk job logs for the expected missing-secret or callback/upload errors.

2. **Fix ExApp env parity for HPB-internal**
   - Declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` in `appinfo/info.xml`.
   - Update `docs/exapp-install.md` to match D-283: HPB-internal is default; guest/public-only is fallback/old limitation, not the current target.
   - Ensure status/doctor endpoint reports presence/absence of the internal secret without exposing it.

3. **Decide storage source-of-truth for MVP**
   - If keeping Cassini storage: document backups/retention, keep `/published`, and make ExApp behavior match compose.
   - If moving toward Nextcloud storage: use PR #79 as the starting point, but decide full bundle/viewer/delete semantics before declaring it the final model.

4. **If hybrid is accepted, review/land or port PR #79**
   - Owner-scope `/published` as an immediate privacy fix.
   - Deliver `meeting.webm`/`summary.md` after build.
   - Then open a follow-up for full transcript/viewer bundle delivery.

5. **Keep raw Talk upload until protocol behavior is proven**
   - Do not remove/delay raw `.mkv` upload unless a spike proves Talk will not show failure without it.
   - Treat post-build delivery as additive first.

6. **Fold private 1:1 validation into the prod-path harness**
   - Use PR #45's API sequence and PR #41's AppAPI/HaRP test topology.
   - This should catch ExApp-only config gaps, not just dev compose gaps.

7. **Archive overwrite follow-up**
   - Independently verify `$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting` vs `$APP_PERSISTENT_STORAGE/site/published/catalog.json` on production.
   - The publish step reads only `<work-root>/current`; any storage model must preserve or intentionally replace that invariant.
