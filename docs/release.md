# Releasing Cassini

A release ships two things, kept in lockstep by `appinfo/info.xml` `<version>`:
the Docker image (`ghcr.io/codemyriad/gocassini:<version>` + `:<version>-cuda`)
and the App Store package (`gocassini.tar.gz`). The git tag `v<version>` and the
manifest's `<image-tag>` must equal `<version>` — CI enforces it.

## Branches

Cassini keeps one branch per minor version and ships from it, as Nextcloud does
with its `stableXX` branches.

- **`main` is the next release** — a minor, or a major if it breaks something
  (see [Patches and majors](#patches-and-majors)). Features land only here.
  Keep it releasable, but only its `-alpha` releases are tagged on main.
- **`release/X.Y` is the X.Y line.** It is branched from main at X.Y's first
  `-beta` (or `-rc`), and every later X.Y tag, `X.Y.0` and each `X.Y.Z` patch,
  is cut from it. `release/0.2` is the exception: 0.2.0 shipped before this
  convention, so the branch starts at the `v0.2.0` tag.
- **Fixes land on main first, then get cherry-picked.** Merge the fix to main,
  then bring it over with `git cherry-pick -x` in a PR to `release/X.Y` (label
  the main PR `backport release/X.Y`). A fix goes straight to the release
  branch only when it doesn't apply to main, and its PR says why.
- **Release branches take fixes only:** bug fixes, security fixes, docs, and
  the release commit itself. No features and no refactors. A change that needs
  a feature waits for the next minor.
- **Only the latest `release/X.Y` gets fixes.** An older branch gets security
  fixes for as long as it is the newest Cassini release for a Nextcloud major
  Cassini still supports, or until the next minor's `.0` ships, whichever is
  later. The App Store serves each server the highest release compatible with
  its Nextcloud version, so dropping a Nextcloud major leaves its servers on
  the previous line. Other branches are frozen.

```text
main          ──●── v0.2.0 ──●──●──●──●──●──   (features: next release)
                       │          fix│     fix│
                       │             ▼ -x     ▼ -x
release/0.2            └─────────────●── v0.2.1 ──●── v0.2.2
```

### Patches and majors

Neither needs a branch of its own.

- **A patch (`X.Y.Z`, Z > 0)** is cut on `release/X.Y`: the branch's previous
  tag plus the fixes cherry-picked since. Patches skip alpha and beta: those
  stages mean features still landing or under test, and a patch has none. Each
  fix already passed CI on main and again in its cherry-pick PR, so go straight
  to stable with `--version X.Y.Z`. What CI doesn't prove is that a fix behaves
  the same on the branch's older code; for a risky one (a migration, the
  recording pipeline, anything CI can't exercise), cut `X.Y.Z-rc.1`, run it on
  staging, then promote it.
- **A security fix** lands on main and is cherry-picked to every branch still
  supported, each getting its own patch tag.
- **A major (`X.0.0`)** is a minor that breaks something, and gets the same
  `release/X.0` branch. Main isn't numbered in advance: whether its next
  release is a minor or a major is decided when the branch is created, from
  what is on main. Anything under *Removed*, or a breaking *Changed* entry,
  makes it a major.
- **Breaking, for Cassini:** dropping a supported Nextcloud major
  (`ci/nextcloud-compatibility.json`); a data or layout migration with no way
  back; removing or changing an operator route, CLI command or settings key;
  a new required install step, such as an AppAPI or Talk prerequisite.
- **While on 0.x**, semver lets any minor break things. Name breaking changes
  in the changelog anyway, so the habit is in place by 1.0.

## Cut a release

One guided command, run on the version's `release/X.Y` branch (see
[Branches](#branches)):

```bash
./scripts/prepare-release.sh --push
```

It previews the pending changes, asks for your choice, confirms, then bumps the
version, folds the changelog, commits, tags `v<version>`, and pushes:

```text
$ ./scripts/prepare-release.sh --push
Current version: 0.2.0

Pending changes (changelog.d/):
  ### Added
  - Auto-retry Talk recordings that drop mid-call.

  patch  → 0.2.1-alpha.1    backward-compatible fixes only
  minor  → 0.3.0-alpha.1    new features, no breaking changes
  major  → 1.0.0-alpha.1    breaking changes

Choose a bump [patch/minor/major] (or a version, q to quit): minor
Preparing release: 0.2.0 -> 0.3.0-alpha.1
Cut v0.3.0-alpha.1 and push it? [y/N] y
```

Then approve the `release` deployment in **Actions** (one click) — that signs the
package, creates the GitHub release, and publishes to the App Store.

**Non-interactive** (scripting) — pass the choice; drop `--push` to stay local:

```bash
./scripts/prepare-release.sh --bump minor --push        # 0.2.0        → 0.3.0-alpha.1
./scripts/prepare-release.sh --promote rc.1 --push      # 0.3.0-beta.1 → 0.3.0-rc.1
./scripts/prepare-release.sh --version 0.3.0 --push     # straight to stable
./scripts/release-preview.sh                            # just show what's pending
```

## What happens after the tag

The tag push triggers two workflows automatically:

1. `publish-exapp-image.yml` — builds and pushes the `:<version>` images.
2. `release.yml` — waits for the exact tagged image workflow and its complete
   compatibility evidence. It checks the commit, manifest, policy, required
   baselines and image digests, validates an unsigned package, then offers the
   existing **one approval**. After approval it rechecks image/tag identities,
   signs the package, attaches compatibility evidence and publishes to the store.
   A failed, skipped, cancelled, expired or mismatched result blocks publication.
   Image availability alone does not authorize a release.

So a release is **one command + one approval**. Declining the approval aborts it;
the images still build. You can also re-run `release.yml` from **Actions** on an
existing tag (`publish_appstore=false` = signed GitHub release only, no store).

## The version ladder

```text
X.Y.Z-alpha.1 → X.Y.Z-beta.1 → X.Y.Z-rc.1 → X.Y.Z-rc.2 → X.Y.Z
```

Every bump restarts the ladder at `-alpha.1`; advance with
`--promote beta|rc.1|rc.2|stable`. You can skip stages: `--version 0.3.0` goes
straight to stable, and `rc.1 → stable` is fine (one RC instead of two).
Patches skip the ladder (see [Patches and majors](#patches-and-majors)).
Suffixed versions upload as App Store pre-releases automatically.

Cassini supports the maintained Nextcloud majors recorded in
[`ci/nextcloud-compatibility.json`](../ci/nextcloud-compatibility.json).
The [generated support table](nextcloud-support-table.md) identifies the exact
minimum and representative tested patches. CI rejects a manifest or generated
table that differs from the inventory. Changes to support happen in a normal PR.
See [compatibility testing](nextcloud-compatibility.md) for qualification and replay.

For a release rehearsal, dispatch `Release` for the intended tag with
`dry_run=true`. It validates evidence and the unsigned package and stops before
signing or publication. Old tags without the evidence format are refused; there
is no automatic legacy bypass. Release assets include `release-evidence.json`,
a readable report, and `release-package.json` binding the final package checksum
to its tested manifest.

## Changelog

Changes are captured per-PR as fragments in
[`changelog.d/`](../changelog.d/README.md), not by editing `CHANGELOG.md`.
`prepare-release.sh` folds them in at release time; a malformed fragment fails
the fold before anything is committed.

## The `release` environment (one-time setup)

**Settings → Environments → `release`**, with **required reviewers** (the
approval gate) and these **environment** secrets:

| Secret | What |
|---|---|
| `APP_PRIVATE_KEY` | App Store code-signing private key (PEM). |
| `APP_PUBLIC_CRT` | Matching certificate from the App Store (PEM). |
| `APPSTORE_TOKEN` | App Store API token for `gocassini`. |

`APP_PRIVATE_KEY` must match `APP_PUBLIC_CRT`, and the cert must be the one
registered for the `gocassini` app id — otherwise the store rejects the upload.
Never commit these (see [CONTRIBUTING](../CONTRIBUTING.md#secrets)).

> **First real publish:** signing is verified end-to-end, but the live store POST
> isn't yet — do the first one on a throwaway pre-release tag and confirm the
> store returns 200/201.

## Rollback

- **Not pushed yet:** `git tag -d v<version> && git reset --hard HEAD~1`.
- **Pushed a bad tag:** `git push origin :refs/tags/v<version>`, fix, re-tag.
- **App Store:** a published release can't be overwritten — cut the next version.

**Never delete a GHCR image tag while a live App Store release pins it.**
`info.xml` pins `<image-tag>` to `<version>`, so deleting the image breaks
installs for every server the store still serves that release to.

## References

- [Nextcloud release process](https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/release_process.html)
  · [code signing](https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/code_signing.html)
  · [App Store API](https://nextcloudappstore.readthedocs.io/en/latest/api/restapi.html)
