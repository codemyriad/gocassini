# Production ExApp Recovery — Brief

Status: split from Nextcloud Store readiness
Phase: 2
Date: 2026-06-22

## What This Initiative Is

This initiative is about getting the currently deployed Cassini Nextcloud ExApp working reliably in production.

It is separate from `planning/initiatives/nextcloud-store-readiness/`. Store readiness keeps the marketplace checklist: signed archive, app metadata, public support links, release process, screenshots, privacy docs, and review gates. Production ExApp recovery handles the operational failure path: why the deployed AppAPI/HaRP ExApp does not behave like the container suite, what is probably failing, and what to validate/fix first.

## Problem

The production ExApp path differs from the standalone container suite in ways that can make a Talk recording fail even when local compose or harness flows work.

The leading symptoms and risks are:

- Talk-triggered production recordings can fail after the room empties or during recorder startup.
- The production ExApp cannot currently receive `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` through normal AppAPI configuration because `appinfo/info.xml` does not declare it.
- Main now defaults Talk-triggered jobs to `hpb-internal`, and the recorder fails closed unless both the Talk recording secret and the signaling internal secret are set.
- The production install docs still describe the older guest/public-room behavior.
- The status endpoint does not report internal signaling secret presence, so admins cannot diagnose the HPB-internal configuration through the UI/API.
- Published archives may collapse to the newest meeting if the ExApp's `<work-root>/current/*.meeting` input contains only the latest bundle.

## Leading Hypothesis

The strongest current hypothesis is configuration parity failure:

`appinfo/info.xml` omits `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, AppAPI drops undeclared deploy env vars, and Talk-triggered jobs now run with `TalkAuthMode: hpb-internal`. The child recorder then exits with:

```text
talk auth mode hpb-internal requires CASSINI_TALK_SIGNALING_INTERNAL_SECRET to be set
```

Other plausible production-only failures are documented in `production-failure-hypothesis.md`.

## Outcome

This initiative is successful when:

- production logs confirm or disprove the missing-secret hypothesis;
- required HPB-internal env/config can be supplied through AppAPI;
- `/operator/status` reports all required Talk config presence without leaking secrets;
- `docs/exapp-install.md` matches the current HPB-internal capture model;
- the production ExApp can record through the Talk record button using the AppAPI/HaRP path;
- archive preservation is verified or repaired for `$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting` and `$APP_PERSISTENT_STORAGE/site/published/catalog.json`.

## Documents

- `production-failure-hypothesis.md` — active failure hypothesis and read-only validation plan.
- `exapp-suite-mismatch-exploration.md` — exploration of the standalone container suite versus the deployed ExApp.
- `archive-overwrite-hypothesis.md` — published archive collapse hypothesis.
- `work-plan.md` — recovery tracks and checklist.
