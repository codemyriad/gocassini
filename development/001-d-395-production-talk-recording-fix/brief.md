---
shaping: true
---

# D-395 — Production Talk Recording Fix — Brief

Date: 2026-06-26
Status: planning / shaping only; execution not started
Branch: `feat/d-395-production-talk-reconciliation`
Primary Linear issue: [D-395](https://linear.app/code-myriad/issue/D-395/make-the-production-cassini-work-fully-with-talk-recording)

## User brief

Production Cassini is installed as a Nextcloud AppAPI ExApp, but the deployed ExApp does not fully work with Talk recording. The development container suite under `deployment/` is the proven working surface and should be used as the parity reference.

The exploration/planning goal is to determine:

- what differs between the proven `deployment/` surface and the installed ExApp surface;
- how to fix those mismatches;
- how to use the local Nextcloud harness to simulate production: an actually installed ExApp behind AppAPI/HaRP, not a standalone direct operator;
- how to validate this in `dev-vm`, editing code on the host and running harness/tests inside the VM through `multipass`.

Target post-implementation validation:

- open the harness and see the Cassini ExApp installed;
- see the control panel and viewer;
- run two separate 1:1 private Talk meeting tests with Erlich Bachman from fixtures + admin;
- have both recordings process and see both transcripts in the viewer;
- verify the second published job does not remove the first or any existing recordings.

Required deliverables:

- `tutorial.md` with manual validation steps;
- env-var setup documentation for app/harness/deployment;
- production deployment documentation describing what to configure and inspect.

## Existing artefacts found and moved

Existing D-395 planning artefacts were found under:

```text
planning/initiatives/mvp/D-395-production-talk-recording-fix/
```

They were moved into this task directory under `legacy/`:

```text
development/001-d-395-production-talk-recording-fix/legacy/brief.md
development/001-d-395-production-talk-recording-fix/legacy/exapp-suite-mismatch-exploration.md
development/001-d-395-production-talk-recording-fix/legacy/production-failure-hypothesis.md
development/001-d-395-production-talk-recording-fix/legacy/archive-overwrite-hypothesis.md
development/001-d-395-production-talk-recording-fix/legacy/work-plan.md
```

Those legacy files remain important source material, but the current task documents in this directory supersede them for D-395 execution.

## Repo scan summary

Key current repo facts:

- `deployment/compose.yml` passes `CASSINI_TALK_RECORDING_SECRET`, `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, and `CASSINI_TALK_BACKEND_URL` directly into the proven standalone operator suite.
- `appinfo/info.xml` declares `CASSINI_TALK_RECORDING_SECRET` and `CASSINI_TALK_BACKEND_URL`, but does **not** declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`; AppAPI drops undeclared deploy env vars.
- `cassini-operator/internal/operator/talk_backend.go` creates Talk-triggered jobs with `TalkAuthMode: hpb-internal`.
- `cassini-go-recorder/internal/talk/recorder.go` rejects `hpb-internal` mode unless both the Talk recording secret and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` are present.
- `/operator/status` (`cassini-operator/internal/operator/status.go`) reports recording-secret and backend-url presence, but not internal signaling-secret presence.
- `docs/exapp-install.md` is stale: it still documents a public-room guest-recorder limitation, while current code defaults to HPB-internal capture.
- `harness/bin/manual-test-setup.sh` is the production-shaped local AppAPI/HaRP dogfood path, but its printed registration command currently omits the Talk env vars needed for the installed ExApp to record. User preference: starting the harness should install Cassini by default.
- `harness/bin/ci-e2e-install-exapp.sh` validates install/proxy/UI against Nextcloud/AppAPI, but uses a directly run container and does not validate full Talk recording through an installed ExApp.
- `harness/bin/ci-e2e-talk-record-roundtrip.sh` validates Talk recording, upload, build, and publish, but points Talk at a directly run host-network operator container, not an AppAPI-installed ExApp.
- `cassini dev play-private` and `harness/runtime/play-private-vm-validation-report.md` prove a 1:1 private flow can work in the VM harness with the standalone operator, but not yet against an installed ExApp.
- AppAPI source inspection in `dev-vm` shows deploy env is container-creation-time config: changing Nextcloud Talk's `spreed.recording_servers.secret` does not update an existing ExApp container env, and `app_api:app:update` has no `--env` option.

## VM setup snapshot

Checked during planning:

```text
multipass list → dev-vm Running, IP 192.168.252.29
Docker → 29.1.3
Docker Compose → 2.40.3
```

Actual Multipass mounts differ slightly from the task note:

```text
/Users/ivan/dev/cassini => /home/ubuntu/cassini
/Users/ivan/dev/cassini => /home/ubuntu/dev/workspace
```

`/home/dev/ubuntu/workspace` was not present. The plan uses `/home/ubuntu/dev/workspace` or `/home/ubuntu/cassini` inside VM commands unless you ask me to normalize the VM differently.

Current VM had `spreedtest-vm-*` and `deployment-*` containers already running; execution should either reuse intentionally or tear down/recreate explicitly before validation.

## Related Linear tickets

Included in D-395 scope:

| Ticket | Status | Why included |
|---|---:|---|
| [D-395](https://linear.app/code-myriad/issue/D-395/make-the-production-cassini-work-fully-with-talk-recording) | Planning, Urgent | Primary work item: production ExApp Talk recording must work end to end. |
| [D-403](https://linear.app/code-myriad/issue/D-403/manifest-declare-cassini-talk-signaling-internal-secret-in-infoxml) | Todo, High | Exact manifest/config parity bug: declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` and surface it in status. Fold into D-395, with docs explaining AppAPI deploy env filtering and redeploy requirements. |
| [D-288](https://linear.app/code-myriad/issue/D-288/create-scripts-for-easy-manual-testing) | In Progress | Reuse `cassini dev play-private` for the Erlich/admin 1:1 validation. D-395 should not reimplement the player; it should wire it into installed-ExApp validation. |
| [D-290](https://linear.app/code-myriad/issue/D-290/e2e-testing) | In Progress | D-395 needs a production-shaped validation harness. We include the installed-ExApp Talk path but do not take over broader CI optimization. |

Adjacent / deferred:

| Ticket | Status | Decision |
|---|---:|---|
| [D-416](https://linear.app/code-myriad/issue/D-416/cassini-in-nextcloud-viewer-fuller-meeting-app-shared-embed) | Planning | Keep as adjacent. D-395 verifies current viewer visibility/transcript; it should not redesign the viewer data model or ACLs. |
| [D-420](https://linear.app/code-myriad/issue/D-420/unify-viewer-and-operator-in-the-exapp) | Planning | Related surface, but the current ExApp already serves operator/viewer/control-panel. D-395 only fixes production recording/config/validation gaps. |
| D-347/D-348/D-349/D-373/D-381 | Done | Historical ExApp readiness context: env declarations, PUBLIC Talk routes, persistent storage, navigation, embedded viewer. Use as baseline, not new scope. |
| D-265 | Backburner | Broader artifact push/storage product decision. D-395 keeps Cassini AppAPI persistent storage authoritative and validates no archive loss; Nextcloud-Files-native rich artifact delivery stays deferred. |
