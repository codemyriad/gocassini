---
shaping: true
---

# Atomic publish swap for `cassini-operator` — Shaping

This document shapes a fix for repeated publish failures in the operator pipeline.

## Source

> The publish functionality in `cassini-operator` pipeline fails for every publish (except the first one).
> The issue is that the first publish step generates a `./site` output. Every subsequent publish will fail because the dest (`./site`) already exists.
>
> To address this, I would like to:
> - create a temp publish output (per job)
> - after successful, replace the existing published `./site` with the successful publish run
> - remove the temp dir
>
> If publish fails, the old one is still available, with job being flagged as failed (in the jobs DB).
> It would be nice to have lineage for the active published bundle (know which job created it)

## Selected implementation shape

The selected shape is:

- keep `cassini publish` as the site-bundle builder
- stop publishing directly into the active shared `site_root`
- publish each job attempt into an attempt-scoped site directory named like the other retained attempt artifacts
- keep every attempt's `.site` bundle for inspection, including failed partial site bundles
- after a successful attempt-local publish, copy/promote that retained attempt site into the shared `site_root`
- if publish fails, extract lightweight failure detail from the attempt-local site bundle and leave the current live site untouched
- keep the shared `site_root` as the stable served path
- add active-site lineage metadata for the bundle that actually became live

This is **Shape A**, selected.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Repeated operator publishes must succeed even when the shared `site_root` already contains a previously published site. | Core goal |
| R1 | A failed publish must not delete, partially overwrite, or corrupt the currently live shared site; the last successful site must remain available. | Must-have |
| R2 | The fix must preserve the current responsibility split: `cassini-operator` owns pipeline orchestration and promotion of shared outputs, while `cassini publish` remains the bundle-generation subprocess. | Must-have |
| R3 | The externally visible published site contract must stay the same: consumers still read a directory at the configured shared `site_root`. | Must-have |
| R4 | Publish output must be attempt-scoped and retained per attempt as `<job>--attempt-XXX.site`, while the shared `site_root` remains the separately promoted live site. | Must-have |
| R5 | Publish success/failure must still be reflected honestly in the jobs DB and API surfaces. | Must-have |
| R6 | It should be possible to tell which job/attempt produced the currently active shared site bundle. | Nice-to-have |
| R7 | Scope stays narrow: no second publisher implementation, no retained failed temp bundles by default, and no new deploy/serve indirection model. | Must-have |

---

## Repo exploration notes

Key repo evidence:

- `cassini-operator/internal/operator/publish_runtime.go`
  - current runtime invokes `cassini publish <work-root>/current --out <site-root>`
  - on failure it currently extracts detail from the output path it just wrote into
- `cassini-go-recorder/internal/cassini/publish.go`
  - `runPublish()` starts with `PrepareSiteBundle(opts.outDir)`
- `cassini-go-recorder/internal/cassini/site_bundle.go`
  - `PrepareSiteBundle()` calls `ensureEmptyDir(outDir)`
- `cassini-go-recorder/internal/cassini/run_bundle.go`
  - `ensureEmptyDir()` rejects any non-empty output directory
- `cassini-operator/internal/operator/artifact_promotion.go`
  - the operator already has a staging/promote pattern for canonical `.run` and `.meeting` outputs
  - this is the closest existing mechanism to reuse for site promotion
- `cassini-operator/internal/operator/publish_store.go`
  - the publish store currently treats summary and attempt `artifact_site_path` as the same value
  - this needs to split so attempt history can keep `<job>--attempt-XXX.site` while the job summary keeps the live shared `site_root`
- `cassini-operator/internal/operator/sitebundle.go`
  - current site manifest model has `source_path`, `catalog_path`, and `meeting_count`, but no publisher lineage fields

## CURRENT: direct publish into shared `site_root`

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | The publish worker is already serialized to one worker in `cassini-operator`. | |
| **CURRENT2** | `executePublishCLI()` runs `cassini publish <work-root>/current --out <site-root>` directly against the live shared site path. | |
| **CURRENT3** | `cassini publish` prepares its output by requiring the destination directory to be empty. | |
| **CURRENT4** | Because the active shared site remains in `<site-root>`, the second publish hits the non-empty-dir guard and fails before it can replace the active site. | |
| **CURRENT5** | The operator already knows how to promote attempt-local outputs into stable shared paths for `.run` and `.meeting` bundles via staging + remove + rename. | |
| **CURRENT6** | Site bundle metadata does not currently record which job/attempt produced the active published site. | |

## A: Operator-owned temp publish + promote shared site — selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | The publish worker computes an attempt-scoped temporary site output path instead of targeting the live `site_root` directly. | |
| **A2** | `cassini publish` runs exactly as today, but with `--out <attempt-temp-site>` rather than `--out <site-root>`. | |
| **A3** | On successful temp publish, the operator promotes the completed temp site bundle into the stable shared `site_root` using the same high-level promotion pattern already used for `.run` and `.meeting`. | |
| **A4** | On publish failure, the operator reads failure detail from the retained attempt-local site bundle, marks the job/attempt failed, and leaves the live `site_root` untouched. | |
| **A5** | The stable artifact path for successful publishes remains the shared `site_root`, while each attempt row retains its own `<job>--attempt-XXX.site` path. | |
| **A6** | The active site bundle is annotated with lineage metadata identifying the job id and attempt number that produced the live site. | |

## B: Teach `cassini publish` to replace non-empty output dirs itself — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Keep operator runtime mostly unchanged and make `cassini publish --out <site-root>` internally stage and replace an existing non-empty destination. | ⚠️ |
| **B2** | Push active-site replacement semantics into the shared CLI instead of keeping them in the operator orchestration layer. | ⚠️ |
| **B3** | Add lineage either inside the CLI or as a follow-on operator mutation. | ⚠️ |

## C: Versioned site directories + pointer/symlink switch — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Publish each run into a durable versioned site directory and switch the active site by updating a symlink or pointer. | ⚠️ |
| **C2** | Treat the active published site as an indirection layer rather than a stable directory replacement. | ⚠️ |
| **C3** | Use the versioned directories themselves as lineage history. | ⚠️ |

---

## Why A is selected

Shape A is the best fit for the existing repo:

- it fixes the immediate non-empty `site_root` failure without changing the external `site_root` contract
- it matches the operator's existing artifact-promotion pattern for canonical outputs
- it keeps `cassini publish` focused on building a site bundle rather than taking on operator-specific live-site replacement semantics
- it makes failure behavior honest: failed publishes do not touch the live site, and removed temp dirs are not exposed as durable artifacts
- it gives us a clean place to add active-site lineage at the promotion boundary

Shape B was rejected because it moves operator deployment/promotion behavior into the shared CLI layer.

Shape C was rejected because it changes the published-site contract more than this fix needs.

---

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | Repeated operator publishes must succeed even when the shared `site_root` already contains a previously published site. | Core goal | ✅ | ✅ | ✅ |
| R1 | A failed publish must not delete, partially overwrite, or corrupt the currently live shared site; the last successful site must remain available. | Must-have | ✅ | ✅ | ✅ |
| R2 | The fix must preserve the current responsibility split: `cassini-operator` owns pipeline orchestration and promotion of shared outputs, while `cassini publish` remains the bundle-generation subprocess. | Must-have | ✅ | ❌ | ✅ |
| R3 | The externally visible published site contract must stay the same: consumers still read a directory at the configured shared `site_root`. | Must-have | ✅ | ✅ | ❌ |
| R4 | Publish output must be attempt-scoped and retained per attempt as `<job>--attempt-XXX.site`, while the shared `site_root` remains the separately promoted live site. | Must-have | ✅ | ❌ | ❌ |
| R5 | Publish success/failure must still be reflected honestly in the jobs DB and API surfaces. | Must-have | ✅ | ✅ | ✅ |
| R6 | It should be possible to tell which job/attempt produced the currently active shared site bundle. | Nice-to-have | ✅ | ❌ | ✅ |
| R7 | Scope stays narrow: no second publisher implementation, no retained failed temp bundles by default, and no new deploy/serve indirection model. | Must-have | ✅ | ❌ | ❌ |

**Notes:**
- B fails R2 and R7 because it broadens the shared CLI into active-site replacement logic that is specific to operator-managed publishing.
- B fails R4 because the temp output would be internal CLI behavior, not an explicit attempt-scoped operator artifact with clear cleanup semantics.
- C fails R3 and R7 because it introduces a new indirection model for the active site.

## Detail A: concrete changes

| Part | Mechanism |
|------|-----------|
| **A1.1** | Add an attempt-scoped site path helper under the retained attempt artifacts, for example `<work-root>/runs/<job-id>--attempt-XXX.site`. |
| **A1.2** | Keep live-site swap staging on the same filesystem as `site_root` so promotion can reuse rename-based staging safely. |
| **A2.1** | Change `executePublishCLI()` in `cassini-operator/internal/operator/publish_runtime.go` to pass the temp site path to `cassini publish`. |
| **A2.2** | Keep the publish log-path behavior unchanged: publish stdout/stderr still goes to the attempt-scoped `publish.log`. |
| **A3.1** | Reuse or extend `artifact_promotion.go` with a `promoteSiteBundle()` helper that promotes a completed temp site into the shared `site_root`. |
| **A3.2** | Only mark publish success after the temp site both exists and has been successfully promoted into `site_root`. |
| **A4.1** | On failure, extract lightweight error detail from the temp site bundle manifest, not from the live `site_root`. |
| **A4.2** | Failed attempts should retain their attempt-local `.site` path so partial site manifests remain inspectable through attempt history. |
| **A4.3** | The job summary should preserve the previous durable `jobs.artifact_site_path` when a rerun fails after an earlier success, while still marking the current attempt as failed. |
| **A5.1** | Successful publishes should persist `jobs.artifact_site_path = <site_root>` while the successful attempt row stores its retained `<job>--attempt-XXX.site` path. |
| **A5.2** | Failed initial publishes should typically leave the job-summary `artifact_site_path` unset, while the failed attempt row still points at its retained partial `.site` bundle. |
| **A6.1** | Add active-site lineage metadata with at least `published_by_job_id`, `published_by_attempt_number`, and `published_at_utc`. |
| **A6.2** | Store that lineage in the active site bundle manifest (`<site-root>/cassini.json`) as optional fields written by the operator during promotion into the live site. |
| **A7.1** | Update operator tests to cover: second publish success, failed publish preserves old live site, failed publish does not persist a dead temp path, and active-site lineage is present after success. |
| **A7.2** | Add/adjust README notes in `cassini-operator/README.md` to describe temp publish + promote semantics and lineage behavior. |

## Resolved decisions

| ID | Decision |
|----|----------|
| D1 | Active-site lineage lives inside `site/cassini.json` as optional fields. |
| D2 | Every attempt keeps its own retained `.site` bundle under the attempt-artifact area, following the same naming scheme as `.run` and `.meeting`. |
| D3 | Failed publish attempts keep their retained `.site` bundle for inspection; the live shared `site_root` remains untouched. |

## Suggested implementation cut

If we proceed immediately after answering the open questions, the smallest coherent cut is:

1. add attempt-site path helper(s)
2. change operator publish runtime to publish into retained attempt `.site` paths
3. promote/copy the successful attempt site into shared `site_root` only on success
4. split publish persistence so job summary tracks live `site_root` while attempts track retained `.site` paths
5. add active-site lineage metadata
6. update unit tests and README
