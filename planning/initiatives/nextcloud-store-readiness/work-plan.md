# Nextcloud Store Readiness — Work Plan

Status: full reconciliation with main and authenticated PR metadata
Phase: 2
Date: 2026-06-22

## Purpose

This file enumerates what must be investigated, prepared, and verified before Cassini can be submitted to the Nextcloud app marketplace. The current researched checklist and blocker classification live in `planning/initiatives/nextcloud-store-readiness/framing.md`.

## Known Findings To Plan Around

- ExApps use the same App Store publishing process as regular Nextcloud apps, with additional required `<external-app>` metadata in `appinfo/info.xml`.
- Store release requires app id registration, certificate approval, signed app files, a signed release archive, and app-store upload through the web UI or REST API.
- The archive must contain one top-level folder matching the app id and an `appinfo/info.xml` inside it.
- Store metadata comes from `appinfo/info.xml` and `CHANGELOG.md`; `info.xml` should be schema-validated against the app-store XSD.
- Cassini already has a strong ExApp runtime and image path, plus basic App Store package stubs (`appinfo/app.php`, `img/app.svg`) and a manual HaRP/reverse-proxy dogfood harness from PR #40.
- Cassini still lacks the app-store archive/signing/release workflow, root license file, `CHANGELOG.md`, `appinfo/signature.json`, screenshots, public support/discussion links, and release automation.
- Main now includes HPB-internal capture (`D-283`) and private/1:1 validation (`D-288-1-1`), so public-room-only should no longer be treated as the intended capture model.
- ExApp env parity is behind that code: `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is required for HPB-internal but is not declared in `appinfo/info.xml`, so AppAPI drops it from normal deploy options.
- The install guide still describes the old guest/public-room-only limitation and needs to be updated before the app can be store-ready.
- The admin status endpoint should report internal signaling secret presence the same way it reports Talk recording secret presence.
- The storage source-of-truth is unsettled: Cassini-owned AppAPI volume still holds rich artifacts; Talk raw upload lands in Files; PR #79 adds partial post-build Files delivery and owner-scoped archive behavior.
- Viewer and published assets are currently logged-in-user-wide; this is likely the largest product/security blocker for a recording/transcript app.
- Published archive preservation needs production validation because `cassini publish` rebuilds from `<work-root>/current/*.meeting`; if that input collapses, the visible archive collapses too.
- Authenticated GitHub PR metadata is now available: PR #79, #80, and #81 are open; PR #41 is open draft; PR #40 and selected readiness PRs #53-#78 are merged into `main`.
- The repository itself is currently private, so public repo/issues/support and GHCR visibility need an explicit store-release decision.

## Work Tracks

| Track | Outcome | Why It Matters |
|---|---|---|
| Submission-path investigation | Confirm the exact app-store path for AppAPI ExApps. | We need to know whether we submit a classic app package, an ExApp manifest, a signed archive, or another artifact. |
| Manifest readiness | `appinfo/info.xml` satisfies store and AppAPI expectations. | The manifest is the store-facing contract and the AppAPI install contract. |
| Release artifacts | A reproducible release can be built from a git tag. | Store submissions must point to immutable, tested artifacts. |
| Image publication | CPU/CUDA/ROCm/arm64 image policy is explicit. | AppAPI install behavior depends on image tags and daemon compute-device selection. |
| Security and privacy | Recording, transcripts, summaries, LLMs, secrets, and storage are documented. | Meeting data is sensitive; review and users need clarity. |
| Admin install UX | A normal Nextcloud admin can install, configure, verify, and roll back. | Store discovery is useless if setup requires insider knowledge. |
| Product-blocker triage | Decide which Phase 2 product gaps block listing. | Avoid submitting an app that is technically installable but not trustable. |
| CI/release gates | Automated checks prevent inconsistent or broken releases. | Version and image drift are easy to introduce without gates. |
| Store presentation | Copy, screenshots, icons, categories, links, and support details are ready. | The store listing is the first product surface many admins will see. |
| Legal/dependency notices | License and third-party components are accurately represented. | The app bundles or depends on media, ML, container, and frontend assets. |

## Investigation Checklist

### Nextcloud Store And ExApp Submission

- Confirm current Nextcloud app-store support for AppAPI ExApps. Initial finding: yes, ExApps follow the regular App Store process with ExApp-specific `info.xml` fields.
- Determine the exact artifact submitted to the store. Initial finding: signed `tar.gz` app archive with a single top-level `gocassini` folder.
- Determine whether `krankerl` is required, optional, or irrelevant for ExApps. Initial finding: signing/archive/upload are required; `krankerl` is a tool choice, not yet confirmed as mandatory.
- Determine whether app signing is required and how signing works for ExApps. Initial finding: code signing is required for apps on `apps.nextcloud.com`.
- Determine whether store review installs the Docker image automatically or only validates metadata.
- Determine whether store validation can access GHCR images and whether private/public registry settings matter.
- Determine whether store releases can point to semver tags, image digests, or both.
- Determine whether AppAPI route declarations and environment variable declarations have additional review requirements.
- Confirm whether the source repo, issue tracker, and support links must be public before the certificate request and first listing.
- Confirm whether App Store review has any additional expectations for ExApps that require a Talk HPB/signaling internal secret.

### App Store Package And Signing

- Create a staged release folder named `gocassini`.
- Decide which files belong in the app-store archive versus only in the source repo or Docker image.
- Keep existing `appinfo/app.php`, `appinfo/info.xml`, and `img/app.svg` in the staged archive.
- Add root `LICENSE`/`COPYING` and `CHANGELOG.md` files to the staged archive.
- Remove `.git`, tests, local env files, secrets, caches, and development-only files from the staged archive.
- Generate `appinfo/signature.json` after final file cleanup.
- Build `gocassini.tar.gz` from the signed staged folder.
- Generate the release archive signature required by the App Store upload form/API.
- Add CI dry-run validation for archive shape, signing, and contents before publishing.
- Define protected handling for `APP_PRIVATE_KEY`, `APP_PUBLIC_CRT`, and `APPSTORE_TOKEN`.

### Manifest And Metadata

- Review required `appinfo/info.xml` fields for store submission.
- Confirm `id`, `name`, `summary`, `description`, `version`, `license`, `author`, `namespace`, `category`, `website`, `bugs`, and `repository` values.
- Confirm whether the private GitHub repository and issue tracker can remain private for certificate request, review, and listed support links; otherwise make public release surfaces available.
- Replace deprecated `agpl` license shorthand with the intended SPDX identifier if Cassini targets Nextcloud 31+.
- Add public documentation links for user/admin/developer documentation if available.
- Add support or discussion link instead of relying on default forum behavior.
- Add screenshot URLs or define where app-store screenshots will be hosted.
- Add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to `<environment-variables>` with safe display/help text.
- Decide whether `CASSINI_DELIVER_MEETING_ARTIFACTS` belongs in the manifest if the hybrid delivery path lands.
- Confirm `dependencies` min/max Nextcloud versions are safe and defensible.
- Confirm AppAPI `<external-app>` fields are current for the target Nextcloud/AppAPI versions.
- Confirm route access levels are accurate after viewer access-control changes.
- Confirm public Talk routes are acceptable and clearly documented as HMAC-authenticated by Talk.
- Confirm environment variables have clear admin-facing display names and descriptions.
- Decide whether extra store metadata files are needed outside `info.xml`.

### Release Artifact And Versioning

- Define the single source of truth for app version.
- Confirm `scripts/bump-exapp-version.sh` covers every version field needed for store releases.
- Require git tag, `info.xml` version, image tag, and docs to agree.
- Decide whether image digest pinning is needed in addition to semver tags.
- Decide how release candidates are represented, if at all.
- Define rollback behavior for a bad store release.
- Define how migrations behave across upgrade and downgrade scenarios.

### Docker Image Policy

- Confirm CPU image contents and size are acceptable for AppAPI install.
- Confirm CUDA image publication should be advertised in the listing or kept in advanced docs.
- Decide what to do with ROCm tags: unsupported, CPU alias, or deferred real image.
- Decide whether arm64 is required before listing.
- Confirm GHCR repository visibility and retention policy.
- Confirm old SHA-tag pruning does not delete release artifacts.
- Confirm model downloads or bundled model assets comply with licenses and practical install expectations.

### Security And Privacy

- Document what data Cassini stores: raw recordings, clean audio, transcripts, summaries, captions, manifests, job logs, app state, and DB rows.
- Document where data is stored under AppAPI persistent storage.
- Document how Talk recording consent and visibility work.
- Document who can start/stop recordings.
- Document who can view recordings after access-control changes.
- Document admin/super-admin override behavior.
- Document how secrets are supplied and rotated: Talk recording secret, Talk signaling internal secret, AppAPI secret, LLM API keys, optional operator API token.
- Document whether any data leaves the Nextcloud host when LLM cleanup/summaries are enabled.
- Document default behavior when LLM credentials are absent.
- Document deletion, uninstall, and `--rm-data` implications.
- Document log hygiene: no secret values, no transcript text in routine logs if possible.
- Document backup implications of Cassini-owned AppAPI persistent storage versus Nextcloud Files delivery.
- Document whether raw `.mkv`, clean `meeting.webm`, transcript, captions, summary, and viewer bundle are stored in Cassini storage, Nextcloud Files, or both.

### Permissions And Routes

- Review every AppAPI route and its access level.
- Confirm `/api/v1/welcome` and `/api/v1/room/*` must remain `PUBLIC` for Talk and are independently HMAC-authenticated.
- Confirm `/viewer/*`, `/published/*`, and `/ui/viewer.*` access levels after the access-control initiative.
- Confirm admin-only routes cannot be reached by normal users through the proxy.
- Confirm direct container access is either not exposed or separately protected by bearer auth.
- Confirm embedded AppAPI pages and proxy asset routes work under CSP.
- Confirm HPB-internal Talk capture can be configured entirely through declared ExApp env vars.
- Confirm status/doctor reports all required Talk config presence without exposing secret values.

### Installation And Admin UX

- Verify the install docs match the final store path, not only manual `occ app_api:app:register`.
- Document AppAPI and HaRP prerequisites clearly.
- Document Docker daemon, GPU, CUDA, and remote GPU deployment expectations.
- Document the Talk handoff and rollback path for current HPB-internal capture, including any guest/public fallback if it remains supported.
- Document health/status checks after install.
- Document common failure modes: AppAPI daemon failure, image pull failure, missing Talk recording secret, missing Talk signaling internal secret, bad recording backend config, unreachable backend URL, wrong `overwrite.cli.url`, GPU unavailable, storage missing, LLM failure.
- Decide whether admin configuration can be done entirely through the Nextcloud UI or still requires `occ` commands.

### Testing And CI

- Keep manifest well-formedness and version consistency checks.
- Add store-schema validation if a store-specific schema/tool exists.
- Keep image smoke, transcribe smoke, container e2e, real-entrypoint e2e, install e2e, and Talk roundtrip gates.
- Keep the manual HaRP/reverse-proxy dogfood setup usable until it is replaced by CI.
- Decide whether a HaRP-fronted install e2e is required before store submission.
- Add access-control e2e gates if viewer ACLs are a blocker.
- Add release dry-run workflow that builds exactly what a store submission would reference.
- Add checks that release docs mention the same version and image tags.
- Confirm CI secrets and self-hosted GPU runner setup are safe for public-release workflows.
- Fold D-290 prod-path e2e requirements into readiness: ExApp-reachable Talk backend URL, reachable `overwrite.cli.url`, proxied welcome, proxied HMAC trigger, and non-admin route assertions.
- Fold D-288 private/1:1 validation into production-shaped AppAPI/HaRP e2e before making store claims about private meetings.
- Track D-386 capture warning/recovery PRs (#80/#81) as reliability gates if the listing claims robust multi-participant capture.

### Store Presentation

- Prepare short summary.
- Prepare long description.
- Prepare feature list.
- Prepare limitations section.
- Prepare screenshots for viewer, control panel, admin status, and Talk recording flow if allowed.
- Prepare icon and branding assets.
- Choose categories/tags.
- Provide website, repository, issue tracker, support contact, and license links.
- Prepare privacy/data-processing note suitable for admins.
- Prepare release notes for the first public version.

### Legal And Dependency Review

- Confirm project license and store metadata use the correct spelling and identifier.
- Add a root license file matching the manifest.
- Add `CHANGELOG.md` with a release entry matching the manifest version.
- Review licenses for bundled frontend dependencies.
- Review licenses for Go/Python/native dependencies if distributed in the image.
- Review FFmpeg/ffprobe implications.
- Review sherpa-onnx / onnxruntime / CUDA runtime implications.
- Review bundled or downloaded speech model licenses.
- Review sample/demo media licensing and ensure no private meeting data is shipped.
- Review use of OpenRouter/OpenAI-compatible APIs and what needs to be disclosed.

## Product Gaps To Classify

These are not automatically blockers, but the initiative must classify them.

| Gap | Possible Classification |
|---|---|
| Viewer is org-wide for all logged-in users | Likely blocker unless access-control initiative lands first. |
| HPB-internal ExApp env parity missing | Blocker for production Talk recording because default capture needs `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. |
| Install docs still describe guest/public-only recording | Blocker for admin trust; docs must match main's HPB-internal capture model. |
| Private/group Talk conversations not production-path validated | Blocker for broad claim until AppAPI/HaRP e2e covers it; no longer purely future code after D-283/D-288. |
| Owner delivery to Nextcloud Files incomplete | Depends on product promise in listing. |
| Storage source-of-truth unclear | Likely blocker for privacy/admin docs if backups, deletion, retention, and Files delivery remain ambiguous. |
| Published archive can collapse if `current/*.meeting` contains only latest | Requires production validation and possible repair before store readiness. |
| Retention/deletion policy incomplete | Likely blocker for privacy readiness if no clear admin behavior exists. |
| ROCm is not real acceleration | Should not be advertised as supported. |
| arm64 missing | Depends on expected store install base and AppAPI review expectations. |
| LLM summaries rely on external API | Acceptable if optional and documented clearly. |
| HaRP production install not fully automated in CI | Decide if manual verification is enough for first listing. |

## Store/Release Gaps To Close

| Gap | Why It Matters |
|---|---|
| No app-store archive/signing workflow | The store accepts signed app archives, not GHCR images alone. |
| No root license file and deprecated license shorthand | Store metadata and legal review need a clear SPDX license and matching file. |
| No `CHANGELOG.md` | Store release metadata imports changelog text from the app archive. |
| No public screenshots/store copy/support link | Required for a useful listing and likely review. |
| Private GitHub repo/support links | Store reviewers/admins need support and transparency surfaces that do not require private organization access. |
| GHCR package visibility unclear | Store reviewers/admins must be able to fetch release images by the manifest tag. |
| No XSD validation in CI | Current CI checks XML well-formedness, but not full app-store schema validity. |
| No privacy/data-processing document | Recording/transcript/LLM behavior needs explicit administrator-facing disclosure. |
| No viewer ACL gate yet | Meeting recordings and transcripts are sensitive; org-wide USER access is likely not store-ready. |
| No HPB-internal ExApp env declaration | Current default Talk capture cannot be configured through normal AppAPI deploy options. |
| Stale install guide | User-facing docs still describe public-room-only guest capture after HPB-internal merged. |
| No storage/source-of-truth decision | Store docs cannot truthfully explain where meeting artifacts live, how to back them up, or how deletion works. |

## Release Checklist Draft

This checklist should become executable or at least mechanically reviewable.

1. Version bump

Run the version bump script and verify `appinfo/info.xml` version and image tag agree.

2. Manifest validation

Validate XML, required fields, route declarations, environment variables, and dependency range.

3. Build images

Build CPU and CUDA release images from the release commit.

4. Test images

Run image smoke, container e2e, real-entrypoint e2e, install e2e, and Talk roundtrip.

5. Verify access policy

Run viewer access-control tests if that initiative is a release gate.

6. Verify Talk production configuration

Confirm HPB-internal env is declared/configured, status reports required Talk config presence, backend URL and `overwrite.cli.url` are reachable from the ExApp, and private/1:1 capture passes the production-shaped harness if advertised.

7. Verify storage and archive behavior

Confirm the chosen source-of-truth model, Files delivery behavior, and that `<work-root>/current/*.meeting` plus `site/published/catalog.json` preserve all intended meetings.

8. Push immutable tags

Publish semver image tags and any compute variants required by AppAPI.

9. Verify image pull

Pull release images by tag from a clean environment.

10. Verify docs

Confirm install, upgrade, rollback, uninstall, privacy, and known limitations docs match the release.

11. Prepare store assets

Confirm screenshots, icon, description, support links, categories, and release notes.

12. Dry-run submission

Run whatever local validation or store dry-run mechanism exists.

13. Submit or record blockers

Submit the app or produce a short blocker list with owners.

## Likely Slice Boundaries

These are candidate slices, not final tickets.

1. **Store path spike**

Investigate and document the exact Nextcloud store submission process for ExApps.

2. **Manifest and metadata cleanup**

Bring `info.xml`, app assets, descriptions, env declarations, and route declarations into store-ready shape.

3. **Release pipeline hardening**

Add missing validation, release dry-run, image pull checks, and docs consistency checks.

4. **ExApp env parity and HPB-internal docs**

Track the production recovery dependency in `planning/initiatives/production-exapp-recovery/`: declare the internal signaling secret, update status/doctor, align install docs with D-283, and validate Talk handoff through production-shaped AppAPI/HaRP before making store claims that depend on it.

5. **Storage/source-of-truth decision**

Decide Cassini volume, Nextcloud Files, or hybrid. If hybrid is selected, review/land or port PR #79 and define follow-ups for transcripts, captions, viewer bundle, deletion, retention, and backups.

6. **Security/privacy documentation**

Write the data-processing, storage, retention, secrets, LLM, and recording-consent docs.

7. **Install UX polish**

Align docs and admin checks with the actual store install path.

8. **Pre-submission review**

Execute the checklist, classify remaining product gaps, and decide whether to submit.

## Open Questions For The Next Session

- Should this initiative wait for viewer access control before any store work starts, or proceed in parallel with a blocker flag?
- What minimum product claim do we want the first listing to make?
- Is the listing for a technical preview, beta, or production-ready app?
- Who is the target first installer: Cassini contributor, friendly pilot admin, or any Nextcloud admin?
- Are we willing to list before private/1:1 capture is proven through production-shaped AppAPI/HaRP, or is merged dev/harness validation enough?
- Are we willing to list with optional external LLM summaries?
- What support and security-contact commitments are we ready to make?
- Do we need a formal privacy policy before submission?
- Do we need a release branch policy, or is tag-from-main enough?
- Is the first storage promise Cassini-owned archive, Nextcloud Files delivery, or hybrid?
