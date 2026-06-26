# Deployment Tarball Packaging Brief

Status: task scaffold
Initiative: `planning/initiatives/nextcloud-store-readiness/`
Date: 2026-06-22

## What This Task Is

This task turns the Nextcloud marketplace release mechanics into a concrete, repeatable packaging path for Cassini.

The task covers:

- exploring the exact directory layout that must be staged before compression
- deciding which repository files belong in the Nextcloud app archive and which stay only in the source repo or Docker image
- producing the first deployment tarball instance for the current app version
- documenting the process that creates a signed, store-uploadable `tar.gz`
- identifying the remaining blockers that keep an initial dry-run tarball from being publishable

This task does not add shaping artifacts. The deliverable is this `brief.md` plus, when implemented, the packaging automation and first tarball artifact it describes.

## Why This Matters

Cassini is already installable as an AppAPI ExApp image, but the Nextcloud marketplace does not accept a Docker image alone as the app release.

Nextcloud expects an app release archive. For Cassini that archive is a small Nextcloud app package whose `appinfo/info.xml` points AppAPI at the versioned ExApp Docker image. The archive is what the marketplace downloads, validates, signs against, and uses for app metadata.

Until Cassini can produce this archive repeatably, the marketplace readiness initiative cannot move from investigation to a real submission path.

## Confirmed Publishing Constraints

Current Nextcloud documentation and the initiative notes agree on these constraints:

- ExApps follow the normal Nextcloud App Store publishing process.
- ExApps additionally require `<external-app>` metadata in `appinfo/info.xml`.
- The uploaded release artifact is a `tar.gz` archive.
- The archive must contain exactly one top-level folder.
- The top-level folder name must match the app id from `appinfo/info.xml`.
- Cassini's app id is currently `gocassini`, so the archive root must be `gocassini/`.
- `gocassini/appinfo/info.xml` must exist inside the archive.
- App Store metadata is read from `appinfo/info.xml` and `CHANGELOG.md`.
- `CHANGELOG.md` must live at the app top level, for Cassini `gocassini/CHANGELOG.md`.
- The changelog entry for the uploaded release must match the app version in `info.xml`.
- Code signing is required for applications on `apps.nextcloud.com`.
- Code signing writes `appinfo/signature.json` inside the staged app directory.
- Files must be removed before signing; changing files after signing invalidates `signature.json`.
- The archive upload also needs a release-archive signature generated from the app private key.
- Uploaded archives are validated to reject at least `.git` in the archive.

## Current Cassini Baseline

Repo-local facts as of this task scaffold:

- `appinfo/info.xml` exists.
- `appinfo/info.xml` declares `<id>gocassini</id>`.
- `appinfo/info.xml` declares `<version>0.1.0</version>`.
- `appinfo/info.xml` declares `<image-tag>0.1.0</image-tag>`.
- `appinfo/info.xml` points AppAPI at `ghcr.io/codemyriad/gocassini:0.1.0`.
- `appinfo/app.php` exists as the minimal Nextcloud app bootstrap stub.
- `img/app.svg` exists as the app icon for the archive.
- `scripts/bump-exapp-version.sh` keeps `info.xml` version and Docker image tag in lock-step.
- `.github/workflows/publish-exapp-image.yml` already validates XML well-formedness and version/image-tag consistency.
- No root `CHANGELOG.md` exists yet.
- No root `LICENSE` or `COPYING` exists yet.
- No `appinfo/signature.json` exists yet.
- No app-store packaging/signing script exists yet.
- No Nextcloud app certificate/private key workflow exists yet.

## First Tarball Instance

The first deployment tarball instance should be based on the current manifest version unless a release version bump lands before implementation.

Initial artifact identity:

| Field | Value |
|---|---|
| App id | `gocassini` |
| Initial version | `0.1.0` |
| Archive root | `gocassini/` |
| Dry-run artifact path | `build/appstore/artifacts/gocassini-0.1.0.tar.gz` |
| Store-facing release asset name | `gocassini.tar.gz` or `gocassini-0.1.0.tar.gz`; choose one and keep it stable in automation |
| Referenced CPU image | `ghcr.io/codemyriad/gocassini:0.1.0` |
| Referenced CUDA image convention | `ghcr.io/codemyriad/gocassini:0.1.0-cuda` |
| Referenced ROCm image convention | `ghcr.io/codemyriad/gocassini:0.1.0-rocm` |

Important classification:

- An unsigned dry-run tarball can be produced first to validate layout and contents.
- The first marketplace-submittable tarball must wait for `CHANGELOG.md`, a root license file, approved app certificate material, file signing, and archive signing.
- A tarball without `appinfo/signature.json` is useful for local packaging validation but should not be described as marketplace-ready.

## Staged Directory Layout

The packaging process should create a clean staging directory outside the source tree's app root and then archive only the staged app folder.

Recommended working layout:

```text
build/appstore/
  stage/
    gocassini/
      appinfo/
        app.php
        info.xml
        signature.json
      img/
        app.svg
      CHANGELOG.md
      LICENSE or COPYING
      README.md
  artifacts/
    gocassini-0.1.0.tar.gz
    gocassini-0.1.0.tar.gz.sha512sig.txt
```

The archive contents should start like this:

```text
gocassini/
gocassini/appinfo/
gocassini/appinfo/app.php
gocassini/appinfo/info.xml
gocassini/appinfo/signature.json
gocassini/img/
gocassini/img/app.svg
gocassini/CHANGELOG.md
gocassini/LICENSE
gocassini/README.md
```

`signature.json` appears only after the signing step. For an unsigned dry-run layout check, it will be absent.

## Initial Include List

The first staged archive should be intentionally small because the ExApp runtime lives in the Docker image referenced by `info.xml`.

Required files:

- `appinfo/info.xml`
- `appinfo/app.php`
- `img/app.svg`
- `CHANGELOG.md`
- `LICENSE` or `COPYING`
- `appinfo/signature.json`, generated after final cleanup

Recommended files:

- `README.md`, if it is acceptable as public package documentation
- short packaged documentation files only if they are maintained and useful from the installed app folder

Do not include by default:

- `.git/`
- `.github/`
- `.claude/`
- `.pi/`
- `.ruff_cache/`
- local `.env`, `.envrc`, `.env.local`, or secret files
- `runs/`
- `tmp/`
- `test-results/`
- `harness/runtime/`
- development media captures
- source monorepo directories such as `cassini-go-recorder/`, `cassini-operator/`, `cassini-publisher/`, `cassini-readable/`, `cassini-transcriber/`, `cassini-viewer/`, and `cassini-control-panel/`, unless a later investigation proves the App Store package needs them
- Dockerfiles and deployment scripts, unless they are intentionally shipped as documentation rather than runtime material
- frontend source, node modules, package manifests, Vite caches, Go build outputs, Python caches, and tests

Reasoning:

- The marketplace archive installs the Nextcloud app shell and metadata.
- AppAPI uses `<external-app><docker-install>` to pull and run the ExApp container.
- Cassini's actual service binaries, embedded UI assets, transcriber pieces, recorder pieces, and publisher pieces are built into the Docker image, not executed from the installed Nextcloud app directory.
- Shipping the whole monorepo would make signing slower, leak development-only files, and increase review noise without making ExApp installation work better.

## Packaging Process

The implementation should automate this as a script or release job. The following process is the target behavior.

1. Read the app id and version from `appinfo/info.xml`.

```bash
APP_ID="$(xmllint --xpath 'string(/info/id)' appinfo/info.xml)"
VERSION="$(xmllint --xpath 'string(/info/version)' appinfo/info.xml)"
IMAGE_TAG="$(xmllint --xpath 'string(/info/external-app/docker-install/image-tag)' appinfo/info.xml)"
```

2. Validate release identity before staging.

```bash
test "$APP_ID" = "gocassini"
test "$VERSION" = "$IMAGE_TAG"
test -n "$VERSION"
```

3. Build or verify the referenced Docker images for the same version.

```bash
docker pull "ghcr.io/codemyriad/gocassini:${VERSION}"
docker pull "ghcr.io/codemyriad/gocassini:${VERSION}-cuda"
docker pull "ghcr.io/codemyriad/gocassini:${VERSION}-rocm"
```

If the first release only commits to CPU support, the CUDA and ROCm pulls should become optional checks and the listing must not overclaim GPU support.

4. Recreate a clean staging directory.

```bash
rm -rf build/appstore/stage build/appstore/artifacts
mkdir -p "build/appstore/stage/${APP_ID}/appinfo" "build/appstore/stage/${APP_ID}/img" build/appstore/artifacts
```

5. Copy only approved files into the staged app folder.

```bash
cp appinfo/info.xml "build/appstore/stage/${APP_ID}/appinfo/info.xml"
cp appinfo/app.php "build/appstore/stage/${APP_ID}/appinfo/app.php"
cp img/app.svg "build/appstore/stage/${APP_ID}/img/app.svg"
cp CHANGELOG.md "build/appstore/stage/${APP_ID}/CHANGELOG.md"
cp LICENSE "build/appstore/stage/${APP_ID}/LICENSE"
cp README.md "build/appstore/stage/${APP_ID}/README.md"
```

If the project chooses `COPYING` instead of `LICENSE`, copy that file and make the manifest/license wording match.

6. Validate the staged metadata before signing.

```bash
xmllint --noout "build/appstore/stage/${APP_ID}/appinfo/info.xml"
xmllint --noout --schema https://apps.nextcloud.com/schema/apps/info.xsd "build/appstore/stage/${APP_ID}/appinfo/info.xml"
```

The second command depends on network access unless the XSD is cached in CI.

7. Verify required top-level files exist.

```bash
test -f "build/appstore/stage/${APP_ID}/appinfo/info.xml"
test -f "build/appstore/stage/${APP_ID}/appinfo/app.php"
test -f "build/appstore/stage/${APP_ID}/img/app.svg"
test -f "build/appstore/stage/${APP_ID}/CHANGELOG.md"
test -f "build/appstore/stage/${APP_ID}/LICENSE" -o -f "build/appstore/stage/${APP_ID}/COPYING"
```

8. Sign the staged app files after all file cleanup is complete.

```bash
php /path/to/nextcloud/occ integrity:sign-app \
  --privateKey="$HOME/.nextcloud/certificates/${APP_ID}.key" \
  --certificate="$HOME/.nextcloud/certificates/${APP_ID}.crt" \
  --path="$(pwd)/build/appstore/stage/${APP_ID}"
```

Expected output of this step:

```text
build/appstore/stage/gocassini/appinfo/signature.json
```

9. Create the compressed archive from the parent of the staged app folder so the archive root is exactly `gocassini/`.

```bash
tar -C build/appstore/stage -czf "build/appstore/artifacts/${APP_ID}-${VERSION}.tar.gz" "${APP_ID}"
```

10. Generate the App Store release-archive signature.

```bash
openssl dgst -sha512 \
  -sign "$HOME/.nextcloud/certificates/${APP_ID}.key" \
  "build/appstore/artifacts/${APP_ID}-${VERSION}.tar.gz" \
  | openssl base64 -A \
  > "build/appstore/artifacts/${APP_ID}-${VERSION}.tar.gz.sha512sig.txt"
```

The text in `gocassini-0.1.0.tar.gz.sha512sig.txt` is the signature value requested by the App Store release upload form/API.

11. Inspect the final archive contents.

```bash
tar -tzf "build/appstore/artifacts/${APP_ID}-${VERSION}.tar.gz"
```

The listing must show one top-level folder only: `gocassini/`.

## Dry-Run Tarball Process Before Certificate Approval

The task should support a local dry-run mode before marketplace credentials exist.

Dry-run mode should:

- create the same staged directory
- copy the same files that are available
- validate `info.xml`
- verify that missing `CHANGELOG.md`, missing license, and missing signing credentials are reported as blockers
- optionally produce `build/appstore/artifacts/gocassini-0.1.0.unsigned.tar.gz`
- mark the artifact as unsigned in its filename or accompanying output
- never upload the unsigned archive to the marketplace

Dry-run mode should not:

- generate fake `signature.json`
- generate fake archive signatures
- silently skip missing `CHANGELOG.md` or license files
- make the unsigned artifact look store-ready

## Publishable Tarball Requirements

The tarball is publishable only when all of these are true:

- `appinfo/info.xml` validates against the Nextcloud app schema.
- `info.xml` app id equals the archive root folder name, `gocassini`.
- `info.xml` version equals the Docker image tag.
- The corresponding GHCR release image is public or otherwise reachable by store reviewers and AppAPI installs.
- The versioned Docker image tags are immutable for the release.
- `CHANGELOG.md` exists at `gocassini/CHANGELOG.md` and has an entry for the release version.
- A root license file exists and matches the manifest license.
- `appinfo/signature.json` is generated by `occ integrity:sign-app` after final staging.
- The final `tar.gz` is generated after `signature.json` exists.
- The release-archive signature is generated from the final `tar.gz`.
- The archive has exactly one top-level directory.
- No blacklisted, secret, local, cache, test-result, or runtime files are present.

## Current Blockers To Resolve In This Task Or Prerequisites

Hard blockers for a marketplace-submittable artifact:

- Create a root `CHANGELOG.md` and include a `0.1.0` release entry, or bump to the actual first release version and document that version instead.
- Add a root `LICENSE` or `COPYING` file.
- Update `appinfo/info.xml` license from deprecated `agpl` shorthand to the intended SPDX value, likely `AGPL-3.0-or-later` if that is the project license.
- Obtain or configure the app certificate files for `gocassini`.
- Define secret handling for `APP_PRIVATE_KEY`, `APP_PUBLIC_CRT`, and `APPSTORE_TOKEN` in CI.
- Add the signing step that writes `appinfo/signature.json` into the staged app folder.
- Add archive-signature generation for App Store upload.
- Decide whether the GitHub repository, issue tracker, documentation, and GHCR package visibility must be public before release.

Important related readiness blockers outside this narrow packaging task:

- `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is not declared in `appinfo/info.xml` yet.
- Install docs still need to match the HPB-internal/default Talk capture path.
- Viewer and published recording access are still broad for logged-in users unless access-control work lands or the listing is constrained.
- Privacy, data storage, LLM egress, deletion, retention, and support docs are still incomplete for a store listing.

## Validation Checklist

The implementation should make these checks mechanical.

- `tar -tzf` shows exactly one top-level folder, `gocassini/`.
- `gocassini/appinfo/info.xml` exists in the archive.
- `gocassini/appinfo/app.php` exists in the archive.
- `gocassini/img/app.svg` exists in the archive.
- `gocassini/CHANGELOG.md` exists in the archive.
- `gocassini/LICENSE` or `gocassini/COPYING` exists in the archive.
- Signed mode includes `gocassini/appinfo/signature.json`.
- Unsigned dry-run mode clearly marks the artifact unsigned.
- The archive does not contain `.git/`.
- The archive does not contain `.env`, `.envrc`, `.env.local`, keys, certificates, CSRs, tokens, logs, runtime captures, caches, `runs/`, `tmp/`, or `test-results/`.
- `info.xml` id is `gocassini`.
- `info.xml` version is valid semver.
- `info.xml` image tag equals the version.
- `info.xml` schema validation passes.
- `CHANGELOG.md` contains a release entry matching the version.
- The release archive signature file exists in signed mode.

## Implementation Deliverables

This task should result in:

- a documented staged archive layout for Cassini's Nextcloud app-store package
- a packaging script or release job that builds the staged directory
- a dry-run tarball for the initial app version, currently `gocassini-0.1.0.unsigned.tar.gz` if signing credentials are not yet available
- a signed tarball path, `gocassini-0.1.0.tar.gz`, once certificate material exists
- a generated archive signature value for marketplace upload
- CI validation that catches wrong archive root, missing metadata, version drift, missing changelog/license, blacklisted files, and unsigned publish attempts

## Done Criteria

This task is done when:

- The repository can produce the initial deployment tarball from a clean checkout.
- The produced tarball contains exactly `gocassini/` as its top-level directory.
- The tarball content list matches the approved include/exclude policy.
- The tarball version matches `appinfo/info.xml` and the Docker image tag.
- Dry-run packaging works without signing credentials and labels the artifact unsigned.
- Signed packaging works when certificate material is provided.
- The archive signature required by the App Store can be generated from the final tarball.
- The task leaves clear output paths for GitHub release upload and App Store release creation.

## References

- `planning/initiatives/nextcloud-store-readiness/brief.md`
- `planning/initiatives/nextcloud-store-readiness/framing.md`
- `planning/initiatives/nextcloud-store-readiness/work-plan.md`
- `appinfo/info.xml`
- `appinfo/app.php`
- `img/app.svg`
- `scripts/bump-exapp-version.sh`
- `.github/workflows/publish-exapp-image.yml`
- Nextcloud App publishing and maintenance: `https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/index.html`
- Nextcloud release process: `https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/release_process.html`
- Nextcloud code signing: `https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/code_signing.html`
- Nextcloud App Developer Guide: `https://nextcloudappstore.readthedocs.io/en/latest/developer.html`
- Nextcloud ExApp development: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppDevelopmentSteps.html`
- Nextcloud ExApp overview: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppOverview.html`
