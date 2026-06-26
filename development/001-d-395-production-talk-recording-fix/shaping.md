---
shaping: true
---

# D-395 — Production Talk Recording Fix — Shaping

## Requirements (R)

| ID | Requirement | Status |
|---|---|---|
| **R0** | Production-installed Cassini ExApp can be configured for the current default HPB-internal Talk recording path through supported AppAPI deploy options. | Core goal |
| **R1** | The installed ExApp exposes admin-visible diagnosis for all required Talk recording config presence without leaking secret values. | Must-have |
| **R2** | Local validation exercises an actually installed ExApp behind Nextcloud AppAPI/HaRP, not only a standalone operator or direct container. | Must-have |
| **R3** | In `dev-vm`, the validation can record a private 1:1 Talk conversation with admin + Erlich Bachman, process it, and show the transcript in the Cassini viewer. | Must-have |
| **R4** | The harness can be opened in a browser and visibly show Cassini ExApp UI surfaces: Nextcloud app entries, control panel, and viewer. | Must-have |
| **R5** | Publishing a new recording does not remove existing published meetings; validation checks catalog/history preservation. | Must-have |
| **R6** | Documentation explains env-var setup for `deployment/`, the installed ExApp, and the harness, including URL reachability constraints. | Must-have |
| **R7** | Documentation explains production deployment and what to inspect before/after Talk handoff. | Must-have |
| **R8** | Scope stays bounded to production parity and validation; broader storage/ACL/viewer redesign is deferred. | Must-have |

## CURRENT: Existing mixed surfaces

| Part | Mechanism | Flag |
|---|---|:---:|
| **CURRENT1** | `deployment/compose.yml` runs operator + control panel + viewer as separate services and passes Talk env vars directly. | |
| **CURRENT2** | ExApp image runs one operator container that serves `/operator`, `/control-panel`, `/viewer`, `/published`, and Talk backend routes. | |
| **CURRENT3** | AppAPI passes only env vars declared in `appinfo/info.xml`; current manifest omits `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. | |
| **CURRENT4** | Talk-triggered jobs default to `hpb-internal`, whose recorder startup requires `CASSINI_TALK_RECORDING_SECRET` and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. | |
| **CURRENT5** | `/operator/status` reports recording secret + backend URL override presence, but not internal signaling secret presence. | |
| **CURRENT6** | Existing install e2e validates ExApp proxy/UI but not Talk recording through installed AppAPI/HaRP. | |
| **CURRENT7** | Existing Talk roundtrip e2e validates recording/build/publish but points Talk at a direct host-network operator container. | |
| **CURRENT8** | Existing private 1:1 playback validates standalone-operator flow, not installed ExApp flow. | |
| **CURRENT9** | `docs/exapp-install.md` still says public conversations only / guest recorder, which is stale for HPB-internal mode. | |

## Shape options

## A: Minimal production env parity

| Part | Mechanism | Flag |
|---|---|:---:|
| **A1** | Add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to `appinfo/info.xml`. | |
| **A2** | Add status boolean for internal signaling secret. | |
| **A3** | Update production install docs. | |
| **A4** | Validate manually against production or direct local commands. | ⚠️ |

## B: ExApp parity plus installed-harness validation

| Part | Mechanism | Flag |
|---|---|:---:|
| **B1** | Add ExApp manifest env declaration for `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, matching `deployment/` parity. | |
| **B2** | Extend `/operator/status` and tests with boolean-only internal signaling secret presence. | |
| **B3** | Update ExApp install/env docs away from guest/public-only wording and toward HPB-internal setup. | |
| **B4** | Make the local AppAPI/HaRP dogfood harness install the ExApp by default with required Talk env vars and verify UI/proxy/menu surfaces; support reinstallation if straightforward. | |
| **B5** | Add installed-ExApp private 1:1 validation that reuses `cassini dev play-private --conversation admin`, then polls AppAPI-proxied jobs/viewer output. | |
| **B6** | Add archive-preservation assertions across two separate private 1:1 recording jobs. | |
| **B7** | Write final tutorial, env-var guide, and production deployment guide for the exact validation/deploy sequence. | |

## C: Nextcloud-native storage and ACL redesign

| Part | Mechanism | Flag |
|---|---|:---:|
| **C1** | Move or duplicate rich artifacts into owner Nextcloud Files after build. | ⚠️ |
| **C2** | Owner-scope or replace `/published/*` archive behavior. | ⚠️ |
| **C3** | Change viewer data source and access-control model. | ⚠️ |
| **C4** | Validate private recording against new storage/access semantics. | ⚠️ |

## D: Keep direct e2e only

| Part | Mechanism | Flag |
|---|---|:---:|
| **D1** | Keep `ci-e2e-install-exapp.sh` for install/UI. | |
| **D2** | Keep `ci-e2e-talk-record-roundtrip.sh` for Talk recording through direct operator. | |
| **D3** | Document that production install should work by analogy. | |

## Fit Check

| Req | Requirement | Status | A | B | C | D |
|---|---|---|---|---|---|---|
| R0 | Production-installed Cassini ExApp can be configured for the current default HPB-internal Talk recording path through supported AppAPI deploy options. | Core goal | ✅ | ✅ | ✅ | ❌ |
| R1 | The installed ExApp exposes admin-visible diagnosis for all required Talk recording config presence without leaking secret values. | Must-have | ✅ | ✅ | ✅ | ❌ |
| R2 | Local validation exercises an actually installed ExApp behind Nextcloud AppAPI/HaRP, not only a standalone operator or direct container. | Must-have | ❌ | ✅ | ✅ | ❌ |
| R3 | In `dev-vm`, the validation can record a private 1:1 Talk conversation with admin + Erlich Bachman, process it, and show the transcript in the Cassini viewer. | Must-have | ❌ | ✅ | ✅ | ❌ |
| R4 | The harness can be opened in a browser and visibly show Cassini ExApp UI surfaces: Nextcloud app entries, control panel, and viewer. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R5 | Publishing a new recording does not remove existing published meetings; validation checks catalog/history preservation. | Must-have | ❌ | ✅ | ✅ | ❌ |
| R6 | Documentation explains env-var setup for `deployment/`, the installed ExApp, and the harness, including URL reachability constraints. | Must-have | ✅ | ✅ | ✅ | ❌ |
| R7 | Documentation explains production deployment and what to inspect before/after Talk handoff. | Must-have | ✅ | ✅ | ✅ | ❌ |
| R8 | Scope stays bounded to production parity and validation; broader storage/ACL/viewer redesign is deferred. | Must-have | ✅ | ✅ | ❌ | ✅ |

**Notes:**

- A fails R2-R5 because it fixes likely config but does not produce the installed-ExApp validation the user asked for.
- C fails R8 because it expands D-395 into product storage/ACL redesign.
- D fails the core issue because direct e2e bypasses AppAPI env declaration and installed ExApp routing.

## Selected shape

**Select B: ExApp parity plus installed-harness validation.**

Shape B is the smallest complete shape that fixes the known production config mismatch, gives admins diagnosis/docs, and proves the end-to-end behavior through the same installed ExApp topology production uses.

## Detail B: mechanisms to build/change

| Part | Mechanism |
|---|---|
| **B1. Manifest env parity** | Add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to `appinfo/info.xml` `<environment-variables>` with clear display name and description. |
| **B2. Status parity** | Add `signaling_internal_secret_configured` (name exact during implementation) to `statusTalk`; source it from process env or runtime config without exposing value; update status tests. |
| **B3. Docs env/prod alignment** | Update `docs/exapp-install.md`, `docs/exapp-test-locally.md`, and/or new D-395 docs to describe HPB-internal default, both secrets, AppAPI-declared env behavior, URL reachability, rollback, and the fact that changing Talk config does not mutate a running ExApp container env. |
| **B4. Install-by-default harness** | Extend `harness/bin/manual-test-setup.sh` or add a sibling helper so normal setup builds/tags, starts Nextcloud/AppAPI/HaRP, registers Cassini with env vars, cycles/validates enable, and leaves a browser-openable installed ExApp. Use AppAPI `--test-deploy-mode` for repeat local setup/reinstallation if straightforward; keep an opt-out only if existing manual behavior must be preserved. |
| **B5. Private installed-ExApp validation** | Add a script/runbook that scaffolds private users/conversations, runs `cassini dev play-private --conversation admin`, waits for the installed ExApp job to complete through AppAPI proxy, and checks the viewer transcript. |
| **B6. Archive preservation assertion** | Validation runs two separate private 1:1 recording jobs and asserts both remain visible in the viewer/catalog. |
| **B7. Final docs** | Finalize `tutorial.md`, `env-vars.md`, and `production-deployment.md` in this development dir after execution. |

## Non-goals / deferred

- Owner-scoped archive or Nextcloud-Files-native rich artifact delivery.
- Replacing AppAPI persistent storage as source of truth.
- Redesigning viewer data model, search, ACLs, or organization UX.
- Making direct `deployment/` compose the production install path.
- Runtime/dynamic secret management through AppAPI app config. If live secret updates without ExApp redeploy are required, open a separate task.

## Resolved questions / decisions

User responses received 2026-06-26 and persisted here.

| ID | Decision | Planning impact |
|---|---|---|
| **Q1** | Include D-403 in D-395, after understanding Nextcloud/Talk/AppAPI config flow. | `spike-x3-nextcloud-talk-appapi-config-flow.md` documents the flow. Execution includes manifest declaration and status parity, plus docs explaining redeploy/secret rotation. |
| **Q2** | Keep the storage model as-is, but configure services so they can communicate and the generated deployment appears in the viewer. If that cannot be done without breaking the model, stop and open a new task. | Validation focuses on env/routing/viewer visibility. A storage-model change is a blocker, not an implementation step. |
| **Q3** | Installing the app should be the default when starting the harness. Reinstallation for code updates is desirable and can be deferred unless extremely straightforward. | Harness setup should register Cassini by default, likely using AppAPI `--test-deploy-mode` for idempotent local re-runs. |
| **Q4** | Use `/home/ubuntu/dev/workspace`. | VM commands and docs use that mount path. |
| **Q5** | Run two separate jobs. | Installed-ExApp validation must record twice and assert both outputs remain visible. |
