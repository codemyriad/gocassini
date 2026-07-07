# Releasing Cassini

This is the **maintainer release guide**. It covers cutting a version, folding
the changelog, publishing a GitHub release, and — optionally — pushing the
signed package to the Nextcloud App Store.

Cassini is an AppAPI ExApp, so a "release" is really two artifacts kept in
lockstep:

1. the **Docker image** (`ghcr.io/codemyriad/gocassini:<version>` and
   `:<version>-cuda`), built and pushed by
   [`publish-exapp-image.yml`](../.github/workflows/publish-exapp-image.yml) when
   a `v<version>` tag lands; and
2. the **App Store package** (`gocassini.tar.gz`), signed and attached to the
   GitHub release — and optionally published to apps.nextcloud.com — by
   [`release.yml`](../.github/workflows/release.yml).

The single source of truth for the version is `appinfo/info.xml` `<version>`.
The Docker `<image-tag>` and the git tag (`v<version>`) must always equal it;
CI rejects any manifest where they disagree.

All `occ …` commands below are shorthand for however your deployment invokes
occ (e.g. `sudo -u www-data php occ …`).

## The version ladder

Cassini follows the Nextcloud prerelease ladder. For a target stable version
`X.Y.Z`:

```text
X.Y.Z-alpha.1  →  X.Y.Z-beta.1  →  X.Y.Z-rc.1  →  X.Y.Z-rc.2  →  X.Y.Z
```

- **Prereleases** carry an `-alpha.N` / `-beta.N` / `-rc.N` suffix. The App
  Store treats any suffixed version as a pre-release; we never publish App Store
  *nightlies*.
- **Stable** has no suffix. A stable release does **not** require a prior
  `rc.2` — `rc.1 → stable` is allowed when a cycle needs only one candidate.

### Choosing patch / minor / major

Bumps always restart the ladder at `-alpha.1` and re-target the base version:

| Command | From `A.B.C` (or any prerelease of it) | Use when |
|---|---|---|
| `bump patch` | `A.B.(C+1)-alpha.1` | backward-compatible fixes only |
| `bump minor` | `A.(B+1).0-alpha.1` | new features, no breaking changes |
| `bump major` | `(A+1).0.0-alpha.1` | breaking changes |

Cassini targets **one Nextcloud stable major per release** and does not maintain
multiple Nextcloud versions in parallel. The supported Nextcloud range lives in
`appinfo/info.xml` (`<nextcloud min-version=… max-version=…>`) and is updated in
a normal PR when compatibility actually changes — it is **not** part of release
prep.

## Changelog fragments

User- and operator-facing changes are captured at PR time as fragments under
`changelog.d/` (see [`changelog.d/README.md`](../changelog.d/README.md)), not by
editing `CHANGELOG.md` directly. At release time the fragments are folded into
`CHANGELOG.md` by `scripts/fold-changelog.sh` — `prepare-release.sh` does this
for you. A malformed fragment (unknown heading, prose outside a heading) fails
the fold before anything is committed.

## Cut a release locally

`scripts/prepare-release.sh` is the one command that turns a version decision
into a local release commit and tag. It **never pushes** — you inspect the diff
and push deliberately.

```bash
# Pick exactly one of:
./scripts/prepare-release.sh --bump minor        # e.g. 0.2.0 → 0.3.0-alpha.1
./scripts/prepare-release.sh --promote rc.1      # e.g. 0.3.0-beta.1 → 0.3.0-rc.1
./scripts/prepare-release.sh --promote stable    # e.g. 0.3.0-rc.2 → 0.3.0
./scripts/prepare-release.sh --version 0.3.0-beta.1   # explicit target
```

It runs pre-flight checks (clean worktree, well-formed `info.xml`, tag not
already present, target greater than the latest release tag, at least one
changelog fragment unless `--allow-empty-changelog`), then:

- bumps `<version>` **and** `<image-tag>` in `appinfo/info.xml`,
- folds `changelog.d/` into `CHANGELOG.md` under `## [<version>] - <date>`,
- writes release notes to `build/release/gocassini-<version>-notes.md`,
- commits `release: <version>` and tags `v<version>`.

Inspect, then push the branch and tag intentionally:

```bash
git show                       # review the release commit
git push origin <branch> v<version>
```

Pushing the **tag** is what triggers `publish-exapp-image.yml` to build and push
`:<version>` and `:<version>-cuda` to GHCR. Wait for that to finish before
running the release workflow.

> The individual steps are also available standalone:
> `scripts/release-version.sh` (bump/promote/set the version),
> `scripts/fold-changelog.sh` (fold, with `--check` for a dry run), and
> `scripts/extract-release-notes.sh` (print a version's notes). All are covered
> by `scripts/test-release-tooling.sh` and gated in CI by
> [`release-tooling.yml`](../.github/workflows/release-tooling.yml).

## Publish the release (GitHub Actions)

Once `v<version>` is pushed and its images are on GHCR, run the **Release**
workflow: **Actions → Release → Run workflow**.

Inputs:

- **version** — the version without a leading `v` (e.g. `0.3.0-alpha.1`); must
  match an existing tag.
- **publish_appstore** — leave `false` to produce only the signed GitHub release
  asset; set `true` to also publish to apps.nextcloud.com.

The workflow:

1. **validate** — the checked-out tag, `info.xml`, and `CHANGELOG.md` all agree
   with the requested version; extracts the release notes.
2. **verify-images** — `:<version>` and `:<version>-cuda` exist on GHCR.
3. **package** — builds the `gocassini/` staging tree, signs it with
   `occ integrity:sign-app`, archives and validates `gocassini.tar.gz`.
4. **github-release** — creates or updates a **draft** release `v<version>` from
   the changelog notes and attaches `gocassini.tar.gz` + `.sha256`.
5. **publish-appstore** — only when `publish_appstore=true`: signs the tarball
   and POSTs it to the App Store API with `nightly: false`.

Steps 3 and 5 touch secrets, so they run in the protected **`release`
environment** and pause for approval. `publish_appstore=false` still yields a
signed GitHub asset; it just stops before the App Store. The GitHub release is
created as a **draft** — review it and publish deliberately.

## Required GitHub configuration

Configure a repository **environment** named `release` (Settings → Environments)
with required reviewers, holding these secrets:

| Secret | What it is |
|---|---|
| `APP_PRIVATE_KEY` | The app's App Store code-signing **private key** (PEM). |
| `APP_PUBLIC_CRT` | The matching **certificate** issued by the Nextcloud App Store. |
| `APPSTORE_TOKEN` | An App Store API token for the `gocassini` app. |

The keypair comes from the Nextcloud App Store code-signing process (a CSR you
submit once per app); see the references below. Treat all three as secrets:
never commit keys, certificates, CSRs, or tokens (see
[`CONTRIBUTING.md`](../CONTRIBUTING.md#secrets)). The workflow materializes the
key/cert only inside a `mktemp` directory that is removed on exit, so signing
material never lands in the workspace or an artifact.

## App Store prerelease behavior

`nightly` is hard-coded `false` in the workflow. The App Store infers
pre-release status from the semver suffix, so `0.3.0-rc.1` uploads as a
pre-release and `0.3.0` as stable through the exact same path — no separate
nightly channel.

## Previous releases and retention

The App Store manages release history for you — there is almost nothing to do by
hand:

- **Releases accumulate.** Every published stable release is kept; a new release
  does not overwrite older ones.
- **Nextcloud is served the newest *compatible* release.** Each release records
  its supported Nextcloud range (`info.xml` `<nextcloud min-version max-version>`).
  When a server on Nextcloud N asks the store for apps, it is offered the highest
  release whose range covers N. So servers on different Nextcloud versions can be
  served different Cassini releases automatically, from the same history.

Because Cassini targets **one Nextcloud major per release** and does not maintain
versions in parallel, this means old releases simply keep serving older servers
until those servers upgrade Nextcloud — no backports, no re-publishing.

**The one real obligation — do not delete GHCR images that a live release
pins.** `info.xml` pins `<image-tag>` to `<version>`, so every published App
Store release points at a specific `ghcr.io/codemyriad/gocassini:<version>` (and
`:<version>-cuda`) image. An older release stays installable only while that
image tag still exists. **Never garbage-collect an image tag while a release
referencing it is still live in the App Store** — doing so silently breaks
installs and reinstalls for every server the store still serves that release to.

Deleting a release (`DELETE /api/v1/apps/gocassini/releases/<version>`, owners
and co-maintainers only) is for pulling a **broken** release, not routine
housekeeping. Normally you leave history intact and cut the next version on the
ladder. GitHub releases and tags likewise just accumulate as the distribution
anchor and audit trail.

## Rollback and fixes

- **Before pushing:** `prepare-release.sh` only made a local commit and tag.
  Undo with `git tag -d v<version>` and `git reset --hard HEAD~1`.
- **After pushing a bad tag:** delete it locally and remotely
  (`git push origin :refs/tags/v<version>`), fix, and re-tag. Note that GHCR may
  already hold `:<version>` images from the first push.
- **GitHub release:** it is created as a draft, so a bad build can be deleted
  before anyone sees it. Re-running the workflow re-attaches assets
  (`gh release upload --clobber`).
- **App Store:** a published App Store release cannot be silently overwritten —
  cut the next version on the ladder rather than trying to replace one.

## Known caveat: signing on a runner

The **package** job runs `occ integrity:sign-app` inside a throwaway
`nextcloud:stable` container. This is the least-proven step in the pipeline.
Validate it on a real runner against a throwaway prerelease tag (with
`publish_appstore=false`) before relying on the flow for a real release; the
`occ` bootstrap may need adjustment for the runner environment.

## Deferred: one-click release from the GitHub UI

Starting a release from the Actions UI (pick a branch, pick a bump/promote
action, let CI create and push the commit + tag) is intentionally **not** built
yet. It needs an identity to push back to the repo, and pushing a tag with the
default `GITHUB_TOKEN` does not trigger `publish-exapp-image.yml`. When it is
built, it will use a **`codemyriad`-owned GitHub App** minting a short-lived
token per run — not a personal access token. Until then, cut releases locally as
above.

## References

- Nextcloud release process:
  <https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/release_process.html>
- Nextcloud code signing:
  <https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/code_signing.html>
- App Store API:
  <https://nextcloudappstore.readthedocs.io/en/latest/api/restapi.html>
