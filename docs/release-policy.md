# Cassini Release Policy

This is the canonical description of how Cassini is released: the branch/tag
model, versioning, the changelog workflow, GitHub Releases, and Nextcloud App
Store publishing. If a script or workflow disagrees with this document, one of
the two has a bug — fix it rather than working around it.

## TL;DR — the release flow

```text
PRs to main (each with a changelog.d/ fragment)
  └─> release-prep PR: bump version + summarize fragments into CHANGELOG.md
        └─> human review + merge to main
              └─> ./scripts/tag-release.sh X.Y.Z   (validated annotated tag push)
                    ├─> publish-exapp-image.yml: GHCR images :X.Y.Z, :X.Y.Z-cuda, …
                    └─> release.yml: GitHub Release (notes from CHANGELOG.md)
                          └─> build + validate + attach gocassini-X.Y.Z.tar.gz,
                              then App Store upload (guarded — skipped until
                              certificate/registration exist)
```

The tag is pushed from a human machine (not by CI with `GITHUB_TOKEN`) on
purpose: GitHub suppresses workflow triggers for refs pushed with the
workflow token, so a CI-pushed tag would silently publish nothing.

## Branch and tag model

Cassini releases are **tag-driven**. There is no permanent release branch.

- `main` is the only permanent branch and the development integration branch.
- A release is prepared through a normal, reviewed release-prep PR
  (`release-prep/<version>`) merged into `main`.
- The release identity is the **annotated tag `v<version>`** on that merge
  commit. Everything published — Docker images, the GitHub Release, the App
  Store tarball — derives from the tag.
- Tags are immutable. A broken release is fixed by cutting a new version,
  never by moving or deleting a tag.
- Temporary `release/<version>` branches are allowed for stabilization
  windows or backports (main has moved on, a candidate needs fixes only).
  Even then, the release is still the tag cut from that branch.

## Versioning

- Cassini uses **SemVer** (`X.Y.Z`, optionally `X.Y.Z-<prerelease>` such as
  `0.2.0-alpha.1`) — the same shape the App Store `info.xsd` semver type
  accepts.
- These must always agree, and CI rejects a release where they don't:
  - `appinfo/info.xml` `<version>`
  - `appinfo/info.xml` `<docker-install><image-tag>` (reproducible installs:
    AppAPI pulls the exact build the release was cut from)
  - the git tag `v<version>`
  - the release archive name `gocassini-<version>.tar.gz`
  - the `CHANGELOG.md` heading `## [<version>]`
- Bump the first two together with `scripts/bump-exapp-version.sh <version>`.

### Pre-releases are not nightlies

SemVer-suffixed versions (`-alpha.N`, `-beta.N`, `-rc.N`) are **pre-releases**:

- GitHub Release: marked *prerelease*.
- Nextcloud App Store: uploaded with **`nightly: false`**. The suffix alone
  puts the release on the store's beta channel. Do not copy generic release
  automation that maps GitHub prerelease → store `nightly:true`; a nightly is
  a different store concept (rolling dev build) and would be wrong for
  Cassini's alphas.

## Changelog workflow

The changelog has two halves.

### Front half — capture at PR time (automated gate)

Every PR that changes shipping behavior adds a fragment under `changelog.d/`
(see `changelog.d/README.md` for naming and format). The `Changelog` workflow
(`changelog-check.yml`) enforces this; `skip-changelog` is the escape hatch
for PRs with nothing release-note-worthy.

### Back half — summarize at release time (agent, locally, reviewed)

Fragments are folded into `CHANGELOG.md` **by an agent working locally in the
release-prep PR** — not by CI. CI never authors changelog content; it only
validates the result (`scripts/check-release-ready.sh`). Rationale: the
summary becomes the public release notes, so it must exist as a reviewable
diff *before* anything is published; an agent has repo and issue-tracker
context; and a wording problem is a PR edit, not a broken release job.

The standard instruction to hand an agent:

> Prepare release `<version>`. Run `scripts/prepare-release.sh <version>`.
> Summarize the `changelog.d/*.md` fragments into a coherent
> `## [<version>] - <date>` section in `CHANGELOG.md` (Keep-a-Changelog
> headings: Added / Changed / Deprecated / Removed / Fixed / Security; merge
> duplicates, write operator/user-facing language), delete the consumed
> fragment files, re-run `scripts/check-release-ready.sh <version>`, and open
> a PR titled `release: <version>` against `main`.

A human reviews that PR like any other change: the exact public release note
and version bump are the diff.

## Release procedure

1. **Prepare** (agent or human, locally on `release-prep/<version>`):
   - `scripts/prepare-release.sh <version>` — bumps
     `appinfo/info.xml`, prompts for the changelog fold, runs
     `scripts/check-release-ready.sh`, builds and validates the App Store
     tarball locally.
   - Open the `release: <version>` PR; get it reviewed and merged.
2. **Cut** (human, one command):
   ```bash
   git switch main && git pull --ff-only
   ./scripts/tag-release.sh X.Y.Z
   ```
   The script refuses to tag unless the worktree is clean, HEAD equals
   `origin/main`, `check-release-ready.sh` passes, and the tag doesn't exist
   yet; then it creates the annotated tag `vX.Y.Z` and pushes it.
3. **Publish** (CI, automatic on the tag):
   - `publish-exapp-image.yml` builds and publishes the GHCR image tags.
   - `release.yml` re-validates, creates the GitHub Release with notes
     extracted from the matching `CHANGELOG.md` section (prerelease flag when
     the version has a suffix), builds and validates the App Store tarball,
     and attaches it to the Release. App Store upload runs only in activated
     mode (below).

## Manual ↔ CI split

| Step | Actor |
|---|---|
| Changelog fragment per PR | PR author (human/agent) |
| Fragment presence gate | CI (`changelog-check.yml`) |
| Decide to release, pick the version | Human |
| Release prep (bump, summarize, delete fragments) | Agent, locally, in a reviewed PR |
| Release-prep PR review | Human |
| Cut the tag | Human (`scripts/tag-release.sh`, self-validating) |
| GitHub Release creation | CI (`release.yml` on the tag push) |
| Docker images | CI (`publish-exapp-image.yml`) |
| Tarball build/validate/attach | CI (`release.yml`) |
| App Store upload | CI, doubly guarded: explicit dispatch input **and** `release` environment approval |
| Post-registration activation (one-time) | Human, per the runbook below |

Principle: humans and agents **decide and author**, CI **validates and
publishes**. Anything public happens from CI off an immutable tag; anything
authored happens in a reviewable PR.

## Nextcloud App Store publishing

The App Store never touches the Docker image — it consumes a source tarball
(`gocassini-<version>.tar.gz`: one top-level `gocassini/` folder with
`appinfo/info.xml`, `CHANGELOG.md`, `LICENSE`, `README.md`, `img/app.svg`)
downloaded from an HTTPS URL (our GitHub Release asset), plus a SHA-512
signature over the archive.

Store metadata validation applies `pre-info.xslt` **before** the XSD — the
transform strips elements the store doesn't know, including
`<external-app><routes>`. Our validator (`scripts/validate-appstore-tarball.sh`)
replicates XSLT→XSD; raw XSD validation false-fails on Cassini and must not
be used as a gate.

`release.yml`'s packaging half has two modes:

### Current mode (registration pending)

Runs on every release: build tarball → validate → attach to the GitHub
Release. The store-upload job is skipped with an explicit summary line
("App Store upload skipped: …"). Nothing in this path requires the signing
certificate.

### Activated mode (after certificate + registration)

Requires all of: `upload_appstore=true` on a manual dispatch, the `release`
environment approval, and secrets `APP_PRIVATE_KEY`, `APP_PUBLIC_CRT`,
`APPSTORE_TOKEN`. The job then:

1. signs the staged app directory (`occ integrity:sign-app` →
   `appinfo/signature.json`) and re-packs the tarball;
2. re-validates with `--require-signature`;
3. overwrites the GitHub Release asset (explicit `overwrite_asset=true`);
4. signs the archive (`openssl dgst -sha512 -sign … | openssl base64`) and
   POSTs `{download, signature, nightly:false}` to apps.nextcloud.com.

### Secrets

| Secret | Status | Used for |
|---|---|---|
| `APP_PRIVATE_KEY` | present | app-dir signing, archive signature |
| `APPSTORE_TOKEN` | present | App Store API authentication |
| `APP_PUBLIC_CRT` | **pending** — arrives with the signed certificate | app-dir signing, app id registration |

The store-upload job runs in the GitHub **`release` environment**; configure
required reviewers on that environment in the repo settings so a public
upload always takes a second explicit human approval. Secrets are written to
`chmod 600` temp files, never echoed, and removed by cleanup traps.

## Post-registration activation runbook

One-time tail, blocked on the Nextcloud certificate request
(nextcloud/app-certificate-requests#1073):

1. When the cert PR merges, retrieve the signed `gocassini.crt`.
2. Add it as the `APP_PUBLIC_CRT` repository secret.
3. Register the app id at <https://apps.nextcloud.com/developer/apps/new>:
   paste the certificate and the signature over the app id:
   ```bash
   echo -n "gocassini" \
     | openssl dgst -sha512 -sign ~/.nextcloud/certificates/gocassini.key \
     | openssl base64
   ```
4. Publish the existing alpha (re-signs and overwrites the current asset):
   ```bash
   gh workflow run release.yml \
     -f version=0.2.0-alpha.1 \
     -f upload_appstore=true \
     -f overwrite_asset=true
   ```
5. Verify on apps.nextcloud.com: the release is listed, shows as a
   pre-release (beta channel), is **not** a nightly, and a sandbox install
   from the store works.
