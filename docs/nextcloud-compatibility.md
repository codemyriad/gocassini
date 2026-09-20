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
and load the exact Cassini image to test first.

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
architecture, CUDA, lifecycle and manual-install checks retain their roles.

Tag runs produce a release-evidence index only after all required image/product
checks pass. `release.yml` verifies the exact trusted tag run, attempt, commit,
policy and immutable artifact identities before the existing approval and again
after it. Index, platform-manifest and config/image digests are recorded as
different identities, not compared interchangeably. Compact evidence is attached
to the release; routine diagnostics expire after 14 days and the handoff artifact
after 90 days. Expired handoff evidence requires requalification.

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
test fails. Missing images or compatible stable dependencies are unavailable,
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
an available prerelease server without compatible stable app dependencies will
remain unavailable. Do not widen public support automatically from canary results.
Qualify fresh installation, the separately planned persisted 35→36 upgrade, and
then the actual production manifest before adding 36 to required support.

Persisted upgrades are a [separate follow-on](proposals/nextcloud-compatibility/spike-upgrades.md).
The existing restart scenario is not evidence of a Nextcloud major upgrade.
