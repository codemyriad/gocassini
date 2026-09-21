---
shaping: true
---

# Nextcloud compatibility — slices

Status: selected shape B implemented; [PR #321](https://github.com/codemyriad/gocassini/pull/321)
holds current qualification results. The user confirmed
two weeks for one engineer and support for maintained majors, currently 33–35,
with explicit retirement of 32. This document follows the authoritative
[requirements, shape and breadboard](shaping.md).

Initial V2 execution passed on 34.0.0 and 35.0.0; 35 is now the reference.
The AppAPI 33 daemon-metadata fix allowed its complete recording/restart path
to pass. Requalification includes clicking the Nextcloud welcome dialog's Close
button and Cassini's actual Play button. The matrix aggregate accepts only a
complete pass, including that browser interaction.

V1 and V3 have offline refusal tests, an exact-attempt ZIP handoff test and an
actual unsigned-package manifest-binding check. V4's real 35 resolver and archived
release-manifest fetch have run locally. The new scheduled workflow becomes
dispatchable after merge. Final signing and publication remain the next normal,
authorized release; this implementation does not publish a test release.

Every slice ends in a maintainer-visible result in PR checks, Actions or release
assets. Those existing interfaces are the UI for this infrastructure project.
No individual coding plans or separate work assignments are needed before the
bet; the builder owns implementation details within these boundaries.

## Slice overview

| ID | Slice | Shape parts | Demonstration |
|---|---|---|---|
| V1 | A support claim has an executable definition | B1 | Change the manifest to an unlisted major and see a named failing check; restore it and see the exact expected matrix and minimum. |
| V2 | Advertised versions run the installed product | B2, B3 | Open Actions and inspect real 33, 34 and 35 results, exact stack identities, embedded-browser evidence and access assertions for one Cassini artifact. |
| V3 | A release must carry matching evidence | B4 | A release dry run accepts complete matching evidence; removing a major or changing the digest blocks before approval or publication. |
| V4 | Upstream drift becomes visible | B5 | Dispatch a moving-patch or preview run, see dependency differences and a result, then reproduce it using the recorded stack and Cassini digest. |

## V1: A support claim has an executable definition

Introduce the inventory outside ignored documentation paths, for example
`ci/nextcloud-compatibility.json`. Use its exact supported set and baseline
entries to drive the validator and the eventual matrix. Initial target: 33–35;
proposed exact minimum 33.0.9, subject to successful qualification. Identify
versioned app archives and checksums along with the server/container digests.

Resolve the current policy drift in the release guide and harness guidance.
Explain the retirement of 32 and exact patch floor in the next release's notes.
The final manifest change must be accompanied by V2 evidence; a passing inventory
check alone does not authorize widening or reasserting support.

| ID | Place | Component | Affordance | Control | Wires Out | Returns To |
|---|---|---|---|---|---|---|
| U1 | P1 | PR diff | Compatibility inventory change | Commit / push | N1 | — |
| U2 | P1 | Required check | Manifest/policy validation result | Render | — | — |
| N1 | P1 | Proposed compatibility inventory validator | Compare inventory, manifest and supported baseline set | Call | N2 in V2 | U2 |

Verification: reject a missing advertised major, a range hole, an untested exact
minimum, a reference outside the supported set and an accidental preview entry
inside required support. These test the support contract rather than YAML text
formatting. Add the validator to an existing required check so enforcement does
not depend on an unconfigured new check name.

Demo: the PR reports “35 advertised but no required baseline,” then shows the
complete expected set after the inventory is corrected. The matrix displayed
at this stage is expected coverage, clearly distinguished from executed results.

## V2: Advertised versions run the installed product

Begin with the riskiest row, 35, on a dedicated hosted runner. Extend the existing
installed CPU scenario rather than creating a second recording implementation.
Make the stack override survive pre-pull, CLI setup, registration and teardown.
Add a minimal Chromium check that signs in to the real Nextcloud instance, opens
the Cassini embedded route, sees an expected app element and can load a published
recording. Keep API-level participant/outsider and admin-route access assertions.

The baseline bootstrap installs the inventory's pinned app archives, checks
their hashes and enabled versions, and fails on silent substitutions. Record
server/app/image observations before cleanup, including failures. Reuse the
same built Cassini image across rows. On PRs, retain the current image-artifact
handoff rather than publishing PR images to GHCR.

Run the reference and exact minimum on ordinary relevant PRs, all baselines for
compatibility/integration changes, and all baselines on main and release tags.
Deduplicate the reference CPU scenario while keeping the existing protected
`Faithful installed ExApp Talk artifact (CPU)` context through an aggregate job.
That aggregate requires every expected row; docs-only applicability is explicit
and is never eligible as release evidence. Preserve the other required checks.

| ID | Place | Component | Affordance | Control | Wires Out | Returns To |
|---|---|---|---|---|---|---|
| U3 | P2 | Actions summary | Per-version results, observed stack and diagnostic links | Render | — | — |
| U4 | P2 | Workflow dispatch | Run baseline or preview qualification | Submit | N2 | — |
| N2 | P2 | Proposed shared compatibility runner | Resolve requested stack; load exact Cassini image; run installed scenario and browser assertion | Call / schedule in V4 | N3 | — |
| N3 | P2 | Proposed evidence collector / aggregator | Observe versions; validate required rows and existing product jobs; write result/index even on failure | Call / job finalization | S1 | U3 |
| S1 | P2 | Actions artifacts | Run evidence keyed by source run and attempt | N3 writes | — | N4 in V3 |

Verification: obtain positive runs for every advertised major, deliberately
break an essential route/assertion and observe failure, and confirm a wrong
installed image or missing observation cannot pass. Check cancellation/cleanup
and that the environment is isolated from another stack. Measure warm/cold
duration and runner-minutes; do not widen timeouts or add success-seeking retries
to disguise product failures.

Demo: each version row opens to a report containing actual Nextcloud and app
versions, a real installed-product result and the same Cassini artifact identity.
Screenshots/traces demonstrate embedded rendering; HTTP 200 alone is insufficient.

## V3: A release must carry matching evidence

Use the mechanism established in [the release-handoff spike](spike-release-handoff.md).
Add an evidence index to the source image workflow, then replace release.yml's
availability-only authorization with a verifier of the exact tagged run and
artifact set. Its required jobs include existing architecture, CUDA and container
checks as well as the new Nextcloud rows. Policy comes from the release tag,
including workflow_dispatch runs started from a different execution ref.

Preserve the existing protected signing/publishing step. Recheck identities
after approval, validate the package's manifest against the qualified manifest,
and attach compact JSON and human-readable evidence alongside its final checksum.
Expired evidence or old releases without this format get an explanatory refusal;
an implicit compatibility bypass is outside this bet.

| ID | Place | Component | Affordance | Control | Wires Out | Returns To |
|---|---|---|---|---|---|---|
| U5 | P3 | Release input | Select existing tag or receive tag-push event | Submit / event | N4 | — |
| U6 | P3 | Release summary | Eligible or blocked, with evidence/reason | Render | — | — |
| U7 | P3 | Protected environment | Existing approve-release control | Approve | N5 | — |
| U8 | P4 | Release assets | Compatibility report and exact artifact identities | Render / download | — | — |
| N4 | P3 | Proposed release evidence verifier | Select exact trusted tag run; check completion, artifact identities, policy and complete required evidence | Call | N5 preflight on success | U6 |
| N5 | P3 | Existing protected publish job, extended | Preflight exposes existing approval; after approval recheck tag/digests, then sign, validate, attach evidence and publish | Dependency success / approval | S2 | U6 |
| S2 | P4 | Release assets and store metadata | Published compatibility evidence and product artifacts | N5 writes | — | U8 |

Verification must cover: matching success; omitted major; wrong SHA, manifest,
policy or image; mixed platform/index identities; failed, skipped, timed-out or
cancelled required jobs; docs-only success; an unrelated/PR source run; expired
evidence; rerun ambiguity; tag movement and retagging during approval. The
decision logic can exercise these cases with small captured/synthetic records.

Demo: a dry run visibly reports “blocked: required Nextcloud 35 evidence missing”
and cannot enter publishing. A complete record is eligible and produces a
reviewable report and unsigned candidate-package validation result. Use the next
normally authorized release to verify final signed assets; do not publish a
throwaway app-store release just to demonstrate the gate.

## V4: Upstream drift becomes visible

Extend U4/N2/N3 with scheduled and manual canary modes. Daily runs use a known
qualified Cassini image digest and resolve newer supported patch/dependency
inputs; previews are explicitly configured as they become available. Capture
the resolved inputs before executing so a failure can be replayed. Reuse the
baseline fixture and assertions. Keep preview-manifest overrides visible and
ineligible for production release evidence.

| ID | Place | Component | Affordance | Control | Wires Out | Returns To |
|---|---|---|---|---|---|---|
| U4 | P2 | Workflow dispatch | 🟡 Run baseline or preview qualification | Submit | N2 | — |
| N2 | P2 | Shared compatibility runner | 🟡 Resolve requested stack; load exact Cassini image; run installed scenario and browser assertion | Call / schedule | N3 | — |
| N3 | P2 | Evidence collector / aggregator | 🟡 Observe versions; validate required rows and existing product jobs; write result/index even on failure | Call / job finalization | S1 | U3 |

Rows retain the authoritative breadboard identity; 🟡 marks the scheduled/preview
behavior added in this slice. No new interface is required.

Verification: run a changed upstream stack against the frozen Cassini image,
replay the exact captured inputs, and show that a failed canary neither blocks
ordinary PR merging nor counts as a successful release baseline. Use a known
version override to demonstrate the mechanism if 36 is unavailable. Keep an
unavailable 36 preview distinct from a test that executed and failed.

Demo: the Actions report names which upstream versions moved, links diagnostics,
and supplies a reproducible invocation. A reviewed inventory update is the
promotion step; a passing canary never changes public support automatically.
The maintainer on release duty reviews failures using existing GitHub workflow
notifications. Automatic issue creation and update PRs are optional later work.

## Order and circuit breaker

Start the 35 execution proof during V1 rather than waiting for polished inventory
tooling. Start the V3 evidence verifier as soon as V2 emits one real report. By
the midpoint, demonstrate installed execution on 35 and both acceptance and
refusal in the release verifier. Complete remaining rows and the schedule after
those risks are understood.

The four scopes are the initial bet. Stop expanding them at two weeks. Report
completed demonstrations, unresolved compatibility failures and any unfinished
scope for re-betting; a green partial matrix never becomes the release contract.
Automatic update PRs and report polish can be cut. Per-major release evidence,
the exact minimum, access assertions and refusal behavior cannot.

The follow-on [upgrade investigation](spike-upgrades.md) is separate. It must
produce a retained-fixture demonstration before upgrade implementation is sliced.
Actual 36 support follows the promotion procedure in shaping.md, including a
35→36 upgrade qualification and a fresh run with the production manifest.
