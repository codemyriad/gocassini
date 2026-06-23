---
shaping: true
---

# Publish destination conflict — Implementation

This document records what was implemented for the `cassini-operator` publish destination conflict fix.

## Outcome

The operator no longer publishes directly into the live shared `site_root`.

Instead:

1. each publish attempt writes into a retained attempt-local site bundle
2. successful attempt-local site bundles are promoted into the live shared `site_root`
3. failed publishes leave the live shared site untouched
4. the live site manifest records which job/attempt produced the active deployment

This fixes the repeated publish failure caused by `cassini publish` requiring an empty output directory.

## What changed

### 1. Attempt-local `.site` bundles are now first-class retained artifacts

Implemented a new attempt path helper:

- `cassini-operator/internal/operator/attempt_paths.go`

Added:

- `attemptSitePath(workRoot, jobID, attemptNumber)`

This stores publish outputs under:

- `<work-root>/runs/<job-id>--attempt-XXX.site`

That puts site artifacts on the same footing as retained attempt `.run` and `.meeting` bundles.

### 2. Publish now targets the retained attempt site, not the live deploy root

Updated:

- `cassini-operator/internal/operator/publish_runtime.go`

Behavior now:

- `executePublishCLI()` runs:
  - `cassini publish <work-root>/current --out <work-root>/runs/<job-id>--attempt-XXX.site`
- the publish worker treats that retained attempt-local `.site` as the primary output of the publish subprocess
- on success, the worker promotes that retained attempt site into the live shared `site_root`
- on failure, the worker extracts failure detail from the retained attempt site bundle and does not touch the live shared site

### 3. Live site promotion now uses a staged swap with rollback protection

Updated:

- `cassini-operator/internal/operator/artifact_promotion.go`

Added:

- `promoteSiteBundle(...)`
- staging near `site_root` via `site.staging`
- backup/rollback handling for live site replacement

Live deployment flow now is:

1. copy retained attempt `.site` into staging near `site_root`
2. write lineage into the staged `cassini.json`
3. move current live `site_root` aside as backup if it exists
4. rename staged site into `site_root`
5. remove the backup after success

Important detail:

- `site.staging` is not a retained artifact model like `runs/...site`
- it is only a transient swap workspace for safely replacing the live deployment

### 4. Job summary and attempt history now store different site paths

Updated:

- `cassini-operator/internal/operator/publish_store.go`

Before:

- job summary and attempt row both stored the same `artifact_site_path`

Now:

- `jobs.artifact_site_path` stores the live shared deployed site path:
  - `<site-root>`
- `job_attempts.artifact_site_path` stores the retained attempt-local site path:
  - `<work-root>/runs/<job-id>--attempt-XXX.site`

This was implemented for both success and failure paths.

Notable behavior:

- initial publish failure:
  - job summary `artifact_site_path` stays unset
  - attempt row keeps the retained partial `.site`
- failed rerun after earlier successful deploy:
  - job summary keeps pointing at the previously deployed live `site_root`
  - failed rerun attempt row points at its own retained partial `.site`

### 5. Live site lineage is written into `cassini.json`

Updated:

- `cassini-operator/internal/operator/sitebundle.go`
- `cassini-go-recorder/internal/cassini/site_bundle.go`
- `cassini-go-recorder/internal/cassini/cli.go`

Added optional manifest fields:

- `published_by_job_id`
- `published_by_attempt_number`
- `published_at_utc`

These are written by the operator during live-site promotion.

This means the active deployed bundle can answer:

- which job produced it
- which attempt produced it
- when it became live

The recorder-side inspect output was also updated so this lineage can be surfaced when inspecting a site bundle.

### 6. README and shaping were updated to match the implemented behavior

Updated:

- `cassini-operator/README.md`
- `planning/initiatives/mvp/publish-dest-conflict/shaping.md`

The docs now describe:

- retained attempt-local `.site` bundles
- promotion into the live shared `site_root`
- live-site lineage fields
- the distinction between retained attempt artifacts and the live deployed site

## Tests updated

Updated:

- `cassini-operator/internal/operator/run_test.go`

Coverage added/adjusted for:

- successful publish retains attempt-local `.site`
- live shared site path is persisted on the job summary
- live site manifest contains lineage for the publishing job/attempt
- rerun success updates live-site lineage to the winning attempt
- initial publish failure keeps attempt-local partial `.site` and leaves job-summary site unset
- failed rerun preserves the previously deployed live site while retaining the failed rerun attempt `.site`

## Validation run

Validated with:

```bash
cd cassini-operator && go test ./internal/operator
cd cassini-go-recorder && go test ./internal/cassini/...
```

Both passed at implementation time.

## Files changed

### Operator runtime

- `cassini-operator/internal/operator/attempt_paths.go`
- `cassini-operator/internal/operator/publish_runtime.go`
- `cassini-operator/internal/operator/publish_store.go`
- `cassini-operator/internal/operator/artifact_promotion.go`
- `cassini-operator/internal/operator/sitebundle.go`
- `cassini-operator/internal/operator/build_runtime.go`
- `cassini-operator/internal/operator/run_test.go`

### Recorder/shared site manifest model

- `cassini-go-recorder/internal/cassini/site_bundle.go`
- `cassini-go-recorder/internal/cassini/cli.go`

### Docs

- `cassini-operator/README.md`
- `planning/initiatives/mvp/publish-dest-conflict/shaping.md`

## Final behavior summary

After the implementation:

- every publish attempt produces a retained `.site` bundle under `runs/`
- successful attempts are promoted into the live shared `site_root`
- failed attempts do not disturb the live site
- attempt history preserves its own `.site` paths
- the active live site records deployment lineage in `cassini.json`
