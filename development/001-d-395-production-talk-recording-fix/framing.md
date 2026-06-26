---
shaping: true
---

# D-395 — Production Talk Recording Fix — Frame

## Source

### User request (2026-06-26)

> "The main issue is that, the production deployment, using Nextcloud and installed ExApp doesn't fully work. The ExApp is missing some config, and development container suite (found under deployment/) should be treated as proven working surface."

> "I need you to explore:
> - the mismatches and how we fix it
> - how we can use the local Nextcloud harness to simulate the production environment (installed ExApp)
> - plan how we can validate this -- I'd use the dev-vm for this, run the harness (with Talk) from inside it, and run the test scripts (1-1 test for instance)"

> "After this is done, I'd like to be able to:
> - open the harness and see Cassini ExApp installed
> - see the control panel and the viewer
> - run the 1-1 private meeting (Erlich Bachman from fixture + admin) test script, have it process and see the transcript in the viewer"

> "NOTE on in-VM development:
> - edit the code in the repo (on host machine)
> - the repo is mounted to dev-vm under `/home/dev/ubuntu/workspace`
> - run the harness / tests / validations inside the dev-vm (using `multipass (shell|exec)`)"

> "IMPORTNAT: Stand by for execution until I explicitly state we should go forward."

### Linear D-395

> "Make sure we can run Cassini as a talk backend in production and no artefacts get lost"

> "We can start Cassini recorder by clicking the `Record` button in Nextcloud Talk.
> When the job is started, we can see the task running in the control panel.
> After the job is finished, we can see the meeting transcript in Cassini Viewer.
> A newly published job doesn't affect the existing recordings (older recordings aren't deleted)"

### Legacy D-395 exploration now in `legacy/`

> "The strongest current hypothesis is configuration parity failure: `appinfo/info.xml` omits `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, AppAPI drops undeclared deploy env vars, and Talk-triggered jobs now run with `TalkAuthMode: hpb-internal`."

> "The ExApp is not deployed like the standalone dev/operator suite. In dev compose, Cassini is three services with explicit volumes and direct env pass-through. In production AppAPI, Cassini is one AppAPI-managed container that serves the operator API, control panel, viewer, and published assets itself, and it relies on AppAPI's `APP_PERSISTENT_STORAGE` volume."

---

## Pre-work: options landscape

| Option | What it does | Who benefits | Signal strength |
|---|---|---|---|
| **A. ExApp config parity repair** | Make the installed ExApp receive the same HPB-internal Talk secrets/config that `deployment/` receives directly. | Production admins; D-395 record-button flow. | Very strong: user explicitly names missing config; Linear D-403 isolates the same bug; code confirms mismatch. |
| **B. Installed-ExApp harness validation** | Run a local Nextcloud/AppAPI/HaRP harness with Cassini installed as an ExApp, then drive the Talk record flow through that installed surface. | Developers validating production-like behavior before deployment. | Very strong: user explicitly requests it; existing direct e2e does not cover installed ExApp Talk recording. |
| **C. Storage/source-of-truth redesign** | Move rich artifacts into Nextcloud Files or owner-scope the full published archive. | Future Nextcloud-native product experience and privacy model. | Real but broader: legacy exploration and D-416/D-265 discuss it; D-395 only requires no lost artifacts and viewer transcript. |
| **D. Production read-only diagnosis only** | Inspect production env/logs and manually patch deployment. | Immediate operations. | Useful, but insufficient: the user needs repo fixes, harness simulation, and repeatable validation. |
| **E. Direct container-suite parity only** | Keep validating the existing `deployment/` suite and direct host-network e2e. | Local dev speed. | Insufficient: it bypasses AppAPI/HaRP and the installed ExApp env declaration surface where the bug lives. |

**Why A+B now:** The acute failure is that production-installed ExApp recording does not work while the proven `deployment/` suite does. The highest-confidence cause is ExApp config parity, and the highest-leverage guardrail is a harness path that installs the ExApp and validates the real Talk record-button flow. This solves the current production path without boiling the ocean into storage/ACL redesign.

**Why not C now:** Storage/source-of-truth needs product choices (Cassini volume vs Nextcloud Files vs hybrid). D-395 needs to preserve existing published recordings and show transcripts in the current viewer; it does not require replacing the storage model.

---

## Problem

- The installed AppAPI ExApp cannot be configured like the working `deployment/` suite because AppAPI only passes declared env vars and `appinfo/info.xml` currently omits the HPB-internal signaling secret required by the default Talk recording path.
- Admin diagnosis is incomplete: `/operator/status` does not report whether the internal signaling secret is configured, so a missing production prerequisite is invisible from the UI/API.
- The production install docs still describe the old public-room/guest-recorder behavior, while current code defaults Talk-triggered jobs to HPB-internal mode.
- Existing validation is split: one path validates ExApp install/UI proxying, another validates Talk recording through a direct container, and a third validates 1:1 playback through the standalone operator. None proves the installed ExApp can record a private 1:1 call and publish a transcript.
- Archive preservation is not explicitly asserted in the installed-ExApp validation path, leaving the D-395 success criterion "newly published job doesn't affect existing recordings" under-tested; user requested two separate jobs for this validation.

## Outcome

- Installed ExApp registration can supply all env vars required for HPB-internal Talk recording through supported AppAPI deploy options.
- The status/control panel surface reports required Talk config presence without leaking secret values.
- Local harness can install Cassini as an ExApp behind AppAPI/HaRP with Talk configured to the AppAPI proxy path.
- In `dev-vm`, a reproducible validation records private 1:1 Talk calls with admin + Erlich Bachman, processes them, and shows transcripts in the viewer.
- Validation runs two separate jobs and asserts published catalog/history does not collapse when the second recording is published.
- Documentation explains env-var setup, local validation, and production deployment checks.

## Less about

- Replacing Cassini's current AppAPI persistent-volume source-of-truth with a Nextcloud-Files-native artifact model.
- Rebuilding the viewer, changing ACL strategy, or creating a full meeting library product.
- Making the standalone `deployment/` suite the production deployment mechanism.

## More about

- Making the production ExApp behave like the proven `deployment/` surface for the current Talk recording contract.
- Exercising the same AppAPI/HaRP/env declaration/proxy path that a real installed ExApp uses.
- Providing an operator-grade validation runbook that can catch production-only config gaps before deployment.
- Documenting that Nextcloud Talk secret changes do not live-update ExApp container env; secret rotation requires coordinated config + redeploy.
