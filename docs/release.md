# Releasing Cassini

A release ships two things, kept in lockstep by `appinfo/info.xml` `<version>`:
the Docker image (`ghcr.io/codemyriad/gocassini:<version>` + `:<version>-cuda`)
and the App Store package (`gocassini.tar.gz`). The git tag `v<version>` and the
manifest's `<image-tag>` must equal `<version>` — CI enforces it.

## Cut a release

One guided command:

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
2. `release.yml` — validates, waits for the images, then behind **one approval**
   (the `release` environment) signs the tarball, creates the GitHub release, and
   POSTs it to apps.nextcloud.com.

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
Suffixed versions upload as App Store pre-releases automatically.

Cassini targets **one Nextcloud major per release**. The supported range lives in
`info.xml` (`<nextcloud min-version max-version>`) and changes in a normal PR, not
at release time.

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
