# Nextcloud compatibility testing

The [inventory](../ci/nextcloud-compatibility.json) is the support contract.
The [generated table](nextcloud-support-table.md) lists its exact minimum and
required baseline patches. These are representative tested patches; the report
does not claim every historical patch was tested. Nextcloud 32 is retired from
the next release. Existing published releases and their images are preserved.

Each baseline pins all eight stack images by digest. Talk and ACL apps have
explicit archive versions and SHA-256 checksums; AppAPI is bundled in the server
image and its version and metadata checksum are checked before enabling it.
There is no fallback to newer app-store packages during a baseline run.

## Run and reproduce

Use a dedicated Docker host with no retained Cassini fixture. The runner refuses
existing fixture containers, volumes and network because the underlying harness
resets and tears down its own resources. Other Docker workloads are not removed.
Install the tools listed in `.github/actions/compatibility-tools/action.yml`
and load the exact Cassini image to test first. Choose a new `LOG_DIR` for every
attempt; an existing evidence directory is refused to prevent stale results.

```bash
IMAGE_REF=ghcr.io/codemyriad/gocassini@sha256:THE_TESTED_DIGEST \
LOG_DIR=/tmp/cassini-nc35 \
  ./harness/bin/ci-nextcloud-compatibility.sh nc35
```

`compatibility.json` records the source workflow/run/attempt, policy and manifest
hashes, full requested stack, observed server/app versions and image identities,
individual assertion results, elapsed time and a link to the run. Browser
screenshots and its result sit beside it. The script collects observations
before the fixture is destroyed. A missing observation cannot pass.

The installed product must record real Talk audio, transcribe on CPU, publish to
Nextcloud Files, preserve participant access and outsider denial, and repeat
after restart. Chromium then signs into the real instance, opens the embedded
app, renders a transcript and plays the new recording. No API responses are
mocked in this browser check.

## CI and releases

Ordinary relevant PRs run the reference and the exact minimum. Inventory,
workflow, harness and integration-boundary changes run all baselines. Main and
tag pushes run all baselines. The existing required CPU check aggregates the
expected rows and refuses missing, failed or mismatched evidence. Other required
architecture, CUDA and lifecycle checks retain their roles.

Tag runs produce a release-evidence index only after all required image/product
checks pass. `release.yml` verifies the exact trusted tag run, attempt, commit,
policy and immutable artifact identities before the existing approval and again
after it. Index, platform-manifest and config/image digests are recorded as
different identities, not compared interchangeably. Compact evidence is attached
to the release; routine diagnostics expire after 14 days and the handoff artifact
after 90 days. Expired handoff evidence requires requalification.

Requalification means rerunning **all jobs** of the exact tagged image workflow,
then restarting release verification. A failed-jobs-only rerun mixes attempts
and cannot supply a complete matching evidence set. Old tags that predate this
format are refused before approval; publish a newly qualified release instead.

## Update the policy

Edit baseline locks in a PR, then regenerate and validate the table:

```bash
python3 scripts/nextcloud_compatibility.py docs > docs/nextcloud-support-table.md
python3 scripts/nextcloud_compatibility.py validate
python3 -m unittest discover -s scripts -p 'test_*compatibility*.py'
```

Change `appinfo/info.xml` bounds in the same PR. The exact minimum must have a
baseline of its own; if a newer patch is added to that major, retain the floor's
baseline until deliberately raising the minimum. Match the local Compose default
to the reference when promoting a new reference. Qualify changed locks before
merging; editing the inventory does not itself establish compatibility.

## Upstream canaries and Nextcloud 36

The daily/manual `Nextcloud upstream canary` workflow resolves newer Nextcloud
patches and compatible stable Talk/ACL app releases against the frozen Cassini
`canary_image` digest. Infrastructure images remain pinned to isolate that
release train. The resolved `candidate.json` is retained even if the product
test fails. A retained change report compares server/app versions, app checksums
and image digests with that major's baseline (or the reference for previews).
Missing images or compatible stable dependencies are unavailable,
never a compatibility pass. GitHub reports failures through normal workflow
notifications; the maintainer on release duty reviews them.

To replay a downloaded candidate, use it as `COMPAT_STACK`, set `COMPAT_MODE=canary`,
and use the recorded Cassini digest as `IMAGE_REF`. Prepare the frozen release's
manifest with `scripts/prepare-compatibility-preview.py` and pass its path as
`D453_MANIFEST_PATH`. Set `GH_TOKEN` for the manifest fetch. The canary preserves
the released image's AppAPI version and records any temporary support-bound
override; its evidence cannot authorize publication.

The preview list is initially empty. Add an explicit `{ "major": 36,
"image": "nextcloud:36" }` entry when an appropriate candidate is available;
use the published prerelease tag in `image` when testing an earlier candidate.
The lock retains Nextcloud's exact preview label. A prerelease server without
compatible stable app dependencies remains unavailable. Do not widen public
support automatically from canary results.
Qualify fresh installation, the separately planned persisted 35→36 upgrade, and
then the actual production manifest before adding 36 to required support.

Persisted upgrades are a [separate follow-on](proposals/nextcloud-compatibility/spike-upgrades.md).
The existing restart scenario is not evidence of a Nextcloud major upgrade.

## CI timing and maintenance

Installed scenarios publish `phase-timings.jsonl` (schema version 1) alongside
the compatibility record, plus a phase table in the Actions summary. Log groups
separate stack pulls (including extraction), host CLI/installation, identity
checks, recording/publication/access/restart, browser, observations and cleanup.
Failed phases retain their exit code; timings are diagnostic and cannot qualify
a release. Tool setup and Cassini image transfer/load have separate Actions steps.

The shared tool action caches both host Go modules by their `go.sum` files.
CUDA-base preparation runs only when that content-addressed base needs building.
Both CUDA builds reclaim disk below a conservative 40 GiB free-space threshold;
that threshold does not impose a minimum space requirement on the build.
GPU smoke checks bundled models, actual GPU use, no fallback and transcript quality
against one fresh transcription; short-clip regression remains a separate scenario.

`Registry housekeeping` runs daily or manually, independently of qualification.
Manual runs default to a dry run. It retains named release tags, the ten newest
rolling image versions, their child manifests, a two-hour untagged upload grace,
and five CUDA bases; it verifies named tags and their children remain pullable.
Maintenance failures have their own workflow notifications and do not invalidate
product evidence. A newly added scheduled workflow becomes active after merge.

See the [CI performance investigation](proposals/nextcloud-compatibility/ci-performance.md)
for the measured baseline and validation method.
