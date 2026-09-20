---
shaping: true
---

# B4 spike: connect release publication to tested artifacts

Status: read-only investigation complete, 2026-09-20. No workflow, registry,
branch protection or release was changed.

## Context and goal

The release workflow currently establishes image availability. Learn how its
existing handoff can also establish successful tests on the artifact it publishes,
without redesigning the complete build pipeline.

## Questions and findings

| ID | Question | Finding and concrete change |
|---|---|---|
| B4-Q1 | Where do builds expose immutable identities? | `publish-exapp-image.yml` builds platform images by digest and its `build-image` job exposes a multi-architecture digest. Extend the evidence index with both index/platform digests, config IDs and the CUDA identity. PRs use the existing artifact handoff and cannot qualify a public release. |
| B4-Q2 | How can a release locate the correct completed test run? | GitHub exposes workflow runs, jobs and artifacts through the Actions API. Match repository, workflow ID/path, source commit, tag ref, event, and run attempt; then retrieve its evidence. Retain the existing two-workflow structure and add `actions: read` to release.yml. Resolve the input release tag independently of the workflow_dispatch execution ref. |
| B4-Q3 | What currently authorizes publication? | `publish` depends on `validate` and `verify-images`; the latter only polls Docker manifests. Replace availability-only authorization with a bounded wait for the matched source run plus evidence validation. A completed failure fails immediately; unavailable, cancelled, expired or mismatched evidence blocks with a reason. |
| B4-Q4 | How do reruns and approval delays affect artifact identity? | Freeze the selected run ID/attempt and digest set. A rerun is new evidence, never an implicit replacement. Re-resolve tag/commit and image digests immediately after the protected approval, before signing/uploading. Changed identity requires requalification; never use an older green record to bless newly tagged bytes. |
| B4-Q5 | How does the current installed harness prove its image? | It compares the supplied local image ID, canonical reused image, manifest's production-tag image and installed container image ID. Extend this existing check with the registry index/platform mapping rather than substituting a tag-string comparison. |
| B4-Q6 | How can this be validated without publishing? | Extract the decision into a verifier that can consume captured/synthetic evidence. A release dry-run path builds/validates an unsigned candidate package and reports eligibility before the protected signing step. Exercise missing majors, wrong SHA/digest/policy, skipped/cancelled jobs, expired evidence and retagging as refusal cases. |

## Accepted mechanism

The source image workflow runs all required compatibility rows for tags and
publishes an index only after all required product/build jobs pass. The index
contains source run identity, policy/manifest hashes, exact platform artifacts
and references to the per-major evidence. A finalizer may write failed evidence
for diagnosis, but its result cannot qualify a release.

`release.yml` checks the matched run's conclusion and index, validates completeness
against the release tag's inventory, checks current registry identities, and
only then offers the existing approval. Preserve required container, ARM64 and
CUDA checks; version coverage on CPU is not a substitute for those checks.

The release workflow does not rebuild Cassini. The package must carry the same
manifest whose hash qualified, apart from explicitly validated packaging outputs
such as its signature. Attach compact evidence as release assets and reference
the signed tarball checksum from the final report.

For old tags lacking this new evidence format, stop with an explanation. A
separate explicit backfill procedure would need to test the archived image with
identified validation tooling; do not add an automatic legacy bypass.

## Acceptance and remaining validation

The questions are answered well enough to describe implementation boundaries.
GitHub API wiring, cold-run behavior and negative controls still require execution
in V3; this read-only spike is not operational proof. Test publication refusal
without access to signing or app-store secrets.

Repository evidence: [release.yml](../../../.github/workflows/release.yml),
[publish-exapp-image.yml](../../../.github/workflows/publish-exapp-image.yml),
[installed image checks](../../../harness/bin/ci-e2e-installed-exapp-talk.sh).
Official API reference: [workflow runs](https://docs.github.com/en/rest/actions/workflow-runs).
