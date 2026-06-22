# Nextcloud Store Readiness - Framing

Status: full reconciliation with main and authenticated PR metadata
Phase: 2
Date: 2026-06-22

## Frame

Cassini should be ready for a real Nextcloud app-store submission, not only a manual AppAPI/ExApp install.

The problem worth solving is trustable distribution. A Nextcloud admin should be able to discover Cassini through the store, understand what it records and where data goes, install a versioned release, verify it works with Talk, and know the limitations before using it for real meetings.

The current repo is much closer than a prototype: it already has an ExApp manifest, AppAPI routes, basic App Store package stubs, a Docker image, embedded UI entries, Talk recording handoff, persistent storage, health/status checks, release image tags, manual HaRP dogfood setup, and CI around the install path. Store readiness is the work that turns that technical shape into a signed, documented, reviewable, and supportable app release.

This pass incorporates the latest `origin/main` state as of `13e9afa`, authenticated GitHub PR metadata available through `gh` on 2026-06-22, and the production-recovery findings now split out to `planning/initiatives/production-exapp-recovery/`.

## Current Answer

Nextcloud's ExApp documentation says ExApps use the same App Store publishing process as regular Nextcloud apps, with additional `<external-app>` metadata in `appinfo/info.xml`. That makes this initiative concrete:

| Area | What Nextcloud Expects | Cassini Impact |
|---|---|---|
| Store submission | Register app id, obtain certificate, upload a signed `tar.gz` release archive by web UI or REST API. | Cassini needs an app-store packaging/signing/release workflow, not only GHCR image publishing. |
| Archive shape | Archive contains exactly one top-level folder named with the app id; it contains `appinfo/info.xml`; `.git` is blacklisted. | Need a staged archive root named `gocassini`, because the repo root is not itself that app folder. |
| Code signing | Code signing is required for all apps on `apps.nextcloud.com`; signing creates `appinfo/signature.json`. | Need app certificate request, secret handling, signing step, and release-manager ownership. |
| Metadata | Store reads metadata from `appinfo/info.xml` and `CHANGELOG.md`. | Need store copy, docs links, screenshots, changelog, support links, and schema validation. |
| ExApp metadata | `<external-app><docker-install>` with registry, image, and image tag is required. | Present in `appinfo/info.xml`; image-tag is pinned to app version. |
| Lifecycle | ExApp must answer `/heartbeat`; `/init` optional but must report progress if used; `/enabled` must support enable/disable. | Implemented by operator lifecycle and init-progress reporter. |
| Authentication | AppAPI auth uses `AUTHORIZATION-APP-API`, `EX-APP-ID`, `EX-APP-VERSION`, and `AA-VERSION`; ExApps must validate their side. | Implemented by `cassini-operator/internal/operator/appapi/middleware.go`. |
| Routes | Manifest-declared route access levels gate proxy traffic; public routes need their own security. | Cassini uses ADMIN, USER, and PUBLIC routes; Talk PUBLIC routes are HMAC-authenticated. |
| UI | Apps must follow Nextcloud design and HTML/CSS guidelines, including theming and accessibility. | Embedded viewer/control panel exist; still needs explicit Nextcloud design/accessibility review. |
| Privacy | If user data leaves the instance, that must be clearly explained and minimized. | LLM summaries are optional but require clear data-egress docs. |
| Compatibility | App releases must make safe Nextcloud compatibility claims and use public APIs. | Manifest currently declares Nextcloud 32-35; this needs review against store rules for the target release date. |

## Repo Baseline

| Status | Evidence | Readiness Meaning |
|---|---|---|
| Ready-ish | `appinfo/info.xml` has `id=gocassini`, app metadata, Nextcloud dependency range, ExApp Docker install metadata, environment variables, and route declarations. | Manifest has the right broad shape but still needs store-grade metadata, HPB-internal env parity, and schema validation. |
| Ready-ish | `appinfo/app.php` and `img/app.svg` exist. | Basic App Store package/icon stubs are present from PR #40; release archive/signing work is still missing. |
| Ready-ish | `<image-tag>` equals `<version>` and CI checks tag/version consistency. | Good release reproducibility baseline. |
| Ready-ish | `deployment/Dockerfile.exapp`, `deployment/Dockerfile.exapp.cuda`, and `.github/workflows/publish-exapp-image.yml` publish CPU/CUDA image tags. | Image path is production-shaped; app-store archive path is missing. |
| Ready-ish | `deployment/exapp-start.sh` handles HaRP FRP env and direct-reachable daemon fallback. | Fits current AppAPI/HaRP direction. |
| Ready-ish | `cassini-operator/internal/operator/appapi/middleware.go` validates AppAPI shared-secret headers. | Core ExApp auth exists. |
| Ready-ish | `cassini-operator/internal/operator/lifecycle.go` handles `/init` and `/enabled`; `/heartbeat` exists. | Core lifecycle exists. |
| Ready-ish | `cassini-operator/internal/operator/exapp.go` registers top-menu UI entries and serves embedded assets. | Store-visible UI exists inside Nextcloud. |
| Ready-ish | `docs/exapp-install.md` documents HaRP install, Talk handoff, rollback, CUDA, persistent storage, access policy, and uninstall. | Strong admin doc baseline, but the Talk handoff section is stale after HPB-internal capture landed. |
| Partial | `docs/exapp-test-locally.md` documents image, manual AppAPI, and production-shaped tiers; `harness/bin/manual-test-setup.sh` provides a local HaRP/reverse-proxy dogfood setup. | Manual HaRP dogfood exists, but HaRP-fronted CI remains in open PR #41 rather than `main`. |
| Partial | CI runs image smoke, transcribe smoke, container e2e, real-entrypoint e2e, real Nextcloud/AppAPI install e2e, and Talk roundtrip CPU/CUDA. | Strong app behavior gates, but no app-store archive/signing dry run or merged HaRP prod-path e2e yet. |
| Partial | Main includes HPB-internal Talk capture (`D-283`) and private/1:1 validation (`D-288-1-1`). | Private/restricted Talk recording is no longer purely future work, but production ExApp configuration and docs are behind the code. |
| Gap | No `LICENSE`, `COPYING`, `CHANGELOG.md`, or `appinfo/signature.json` exists in the working tree. | Store/legal/release blockers despite the basic package stubs being present. |
| Gap | `info.xml` currently uses `<licence>agpl</licence>`. | For Nextcloud 31+ metadata, use a current SPDX value such as `AGPL-3.0-or-later` if that is the intended license. |
| Gap | `repository`, `website`, and `bugs` point to `github.com/codemyriad/gocassini`, and `gh repo view` reports the repository visibility as `PRIVATE`. | Store transparency, certificate request, support links, and issue tracker expectations need a release decision. |
| Gap | `appinfo/info.xml` does not declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. | AppAPI drops undeclared env vars, so the default HPB-internal Talk capture path cannot be configured through normal ExApp registration/admin UI. |
| Gap | `docs/exapp-install.md` still describes public-room-only guest recording. | The install guide is stale after D-283/D-288-1-1 and may lead admins to deploy the wrong expectations or miss required HPB-internal config. |
| Gap | `/operator/status` reports Talk recording secret and backend URL presence, but not internal signaling secret presence. | Admins cannot verify the now-required HPB-internal configuration without shell access. |
| Gap | `/viewer/*` and `/published/*` are `USER` routes, so every logged-in user can browse the archive. | Likely hard blocker for a store-ready recording/transcript product unless deliberately listed as a constrained technical preview. |
| Gap | Cassini volume remains the rich artifact source of truth; Talk raw upload is a delivery side effect. | Store docs must explain backup, retention, Files delivery, and why the full transcript/viewer artifact may not live in Nextcloud Files yet. |
| Gap | Published catalog generation reads only `<work-root>/current/*.meeting`. | Production must verify that current contains all intended meetings, or each publish can collapse the visible archive to the newest meeting. |

## Store Requirements Checklist

### Submission And Ownership

- [ ] Decide the app owner and release manager.
- [ ] Confirm whether `gocassini` is the final public app id.
- [ ] Confirm the repository, issue tracker, and support URLs are public enough for app-store review and users.
- [ ] Generate app certificate key/CSR for `gocassini`.
- [ ] Submit CSR to Nextcloud's app-certificate request process.
- [ ] Register the app id in the Nextcloud App Store after certificate approval.
- [ ] Create and protect an app-store token for release automation.
- [ ] Decide whether first listing is production, beta, or technical preview.

### App Archive, Signing, And Release Automation

- [ ] Create a staging layout whose top-level folder is exactly `gocassini`.
- [x] Include basic package stubs: `appinfo/info.xml`, `appinfo/app.php`, and `img/app.svg` exist.
- [ ] Include docs metadata, `CHANGELOG.md`, license files, screenshots, and only production-required files in the final archive.
- [ ] Exclude `.git`, tests, local configs, development-only files, secrets, and generated caches from the store archive.
- [ ] Add an app signing step that writes `appinfo/signature.json` after final file cleanup.
- [ ] Build `gocassini.tar.gz` from the signed staged folder.
- [ ] Generate the App Store release-archive signature with the app private key.
- [ ] Upload the release archive as a GitHub release artifact or other stable download URL.
- [ ] Upload release metadata to the App Store by web UI or REST API.
- [ ] Add a release dry-run job that builds the exact archive without publishing.
- [ ] Store signing credentials in a protected release environment with required reviewers.

### Manifest And Store Metadata

- [x] `id`, `name`, `summary`, `description`, `version`, `author`, `category`, `website`, `bugs`, and `repository` exist.
- [x] `<external-app><docker-install>` exists with `ghcr.io/codemyriad/gocassini` and version-pinned image tag.
- [x] Basic App Store package/icon files exist: `appinfo/app.php` and `img/app.svg`.
- [x] Environment variables are declared for Talk secret, LLM config, backend URL override, and direct operator token.
- [x] ADMIN, USER, and PUBLIC routes are declared in `info.xml`.
- [ ] Declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` for the default HPB-internal recorder path.
- [ ] If PR #79 or equivalent lands, declare and document `CASSINI_DELIVER_MEETING_ARTIFACTS`.
- [ ] Replace deprecated license shorthand with the intended SPDX license identifier.
- [ ] Add a root license file matching the manifest license.
- [ ] Add `CHANGELOG.md` with a release entry whose version matches `info.xml`.
- [ ] Add `documentation` links for user/admin/developer docs if public docs are available.
- [ ] Add a `discussion` or support URL instead of relying on the default forum behavior.
- [ ] Add screenshot URLs or decide where store screenshots will be hosted.
- [ ] Validate `info.xml` against `https://apps.nextcloud.com/schema/apps/info.xsd`, not just XML well-formedness.
- [ ] Confirm the Nextcloud min/max compatibility range for the first store release.
- [ ] Confirm categories; `integration` is plausible, `multimedia` may also fit.

### ExApp Runtime Compliance

- [x] `/heartbeat` answers unauthenticated `GET`/`HEAD` with `{"status":"ok"}`.
- [x] `/init` answers and reports progress via `PUT /ocs/v2.php/apps/app_api/ex-app/status`.
- [x] `/enabled` accepts enable/disable and registers UI entries on enable.
- [x] AppAPI middleware verifies `AUTHORIZATION-APP-API`, app id, and version.
- [x] AppAPI persistent storage defaults are used when `APP_PERSISTENT_STORAGE` is present.
- [x] HaRP FRP client is bundled and starts when `HP_*` env is present.
- [x] Talk PUBLIC routes use independent HMAC authentication.
- [ ] Bring ExApp env parity in line with HPB-internal capture by exposing `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
- [ ] Update status/doctor output to report HPB-internal config presence without leaking the secret.
- [ ] Update install docs and controlled-test steps for HPB-internal/private-room capable recording, with guest/public fallback only if explicitly supported.
- [ ] Review direct-container exposure assumptions and default bearer-token behavior for non-proxied operator APIs.
- [ ] Decide whether to harden the image with a non-root runtime process while staying AppAPI/HaRP-compatible.
- [ ] Confirm image logs are useful for admins and never include secrets or private transcript content in routine paths.

### Images And Platform Policy

- [x] CPU image is built and published for `linux/amd64`.
- [x] CUDA image is built and published, with GPU smoke/Talk roundtrip gates.
- [x] ROCm tag currently aliases CPU so ROCm daemons do not fail to pull.
- [ ] Decide whether ROCm should be documented as unsupported instead of advertised.
- [ ] Decide whether first store release needs `linux/arm64` images.
- [ ] Confirm GHCR package visibility and retention policy for release tags.
- [ ] Confirm old SHA-tag pruning never deletes semver release tags or CUDA base images used by releases.
- [ ] Decide whether image digests can or should be documented alongside semver tags.
- [ ] Review image size and bundled model install expectations.
- [ ] Produce third-party license notices for FFmpeg, sherpa-onnx, onnxruntime, CUDA pieces, models, Go modules, Python/Node assets, and bundled media fixtures.

### UI, UX, And Store Presentation

- [x] Viewer and control panel are available as Nextcloud top-menu entries.
- [x] Viewer and control-panel embedded builds use Shadow DOM CSS isolation.
- [x] Admin and user navigation entries are separated.
- [ ] Review UI against Nextcloud design, HTML/CSS, accessibility, high-contrast, dark/light, mobile, and RTL expectations.
- [ ] Prepare screenshots for the viewer, control panel, status/doctor, and Talk recording flow.
- [ ] Prepare store short summary, long description, feature list, limitations, and release notes.
- [ ] Document that recordings/transcripts are sensitive and define expected user-facing labels for private/public/owner-access behavior.
- [ ] Decide whether app name, icon, screenshots, and copy can use current branding.

### Security, Privacy, And Product Trust

- [x] AppAPI auth middleware exists for proxied non-public routes.
- [x] Talk recording protocol validates Talk HMAC headers.
- [x] Status endpoint reports secret presence only, not secret values.
- [ ] Implement or deliberately defer per-recording viewer access control.
- [ ] Gate direct `/published/*` asset access by recording authorization if access control is required.
- [ ] Decide whether PR #79's owner-scoped archive is an acceptable interim privacy boundary or whether invited-user ACLs are required before listing.
- [ ] Decide whether Cassini-owned storage, Nextcloud Files, or a hybrid model is authoritative for rich artifacts.
- [ ] Document that raw `.mkv` upload to Talk currently preserves native Talk lifecycle while post-build rich artifact delivery is separate.
- [ ] Verify archive overwrite behavior by comparing `$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting` with `$APP_PERSISTENT_STORAGE/site/published/catalog.json` on production.
- [ ] Document exactly what Cassini stores: raw runs, clean audio, transcripts, summaries, captions, manifests, logs, DB rows, and published files.
- [ ] Document where data is stored under AppAPI persistent storage.
- [ ] Document who can start/stop recordings and who can view outputs.
- [ ] Document recording consent, Talk visibility, and bot participant behavior.
- [ ] Document optional LLM egress: which fields leave the Nextcloud host, which provider endpoint is used, and default behavior without keys.
- [ ] Define retention and deletion behavior beyond `app_api:app:unregister --rm-data`.
- [ ] Define secret rotation instructions for Talk shared secret, AppAPI secret, LLM API key, and optional operator API token.
- [ ] Add security contact/support process.
- [ ] Add rate-limit/bruteforce review for public Talk routes and any direct operator entry points.

### Testing And CI Gates

- [x] CI checks XML well-formedness and version/image-tag consistency.
- [x] CI builds CPU/CUDA images.
- [x] CI runs image smoke, container e2e, real-entrypoint e2e, manual AppAPI install e2e, and Talk roundtrip CPU/CUDA.
- [x] A manual HaRP/reverse-proxy App Store dogfood setup exists in `harness/bin/manual-test-setup.sh`.
- [ ] Add XSD schema validation for `appinfo/info.xml`.
- [ ] Add signed archive dry-run validation.
- [ ] Add App Store release artifact contents check: top folder, no `.git`, no secrets, `CHANGELOG.md`, license, signature.
- [ ] Add HaRP-fronted production install e2e if store submission depends on HaRP behavior.
- [ ] Incorporate D-290 prod-path e2e findings: ExApp-reachable backend URL, `overwrite.cli.url`, and proxied Talk trigger checks.
- [ ] Fold private/1:1 validation into a production-path AppAPI/HaRP harness rather than relying only on dev tooling.
- [ ] Track D-386 capture reliability PRs (#80/#81) as recording quality gates if the store listing promises reliable multi-participant capture.
- [ ] Add viewer ACL e2e gates if per-recording access control is a store blocker.
- [ ] Add docs consistency checks for release version, image tags, install paths, and known limitations.
- [ ] Add dependency/license inventory or SBOM generation for the image and app archive.

## Pull Request Evidence

Authenticated GitHub CLI metadata was available for this pass. Current relevant state on 2026-06-22:

| PR | State | Relevance To Store Readiness | Current Planning Interpretation |
|---|---|---|---|
| `#40` App Store packaging + dogfood | Merged 2026-06-12 | Added `appinfo/app.php`, `img/app.svg`, AppAPI PUBLIC Talk routes, HaRP/frpc fixes, and a local App Store dogfood setup. | Basic package stubs and manual HaRP dogfood are already on `main`; signed archive/release automation is still missing. |
| `#53`-`#55`, `#60`, `#62`, `#68`, `#70`, `#76`-`#78` | Merged | Production image hardening, manifest env declarations, AppAPI persistent-storage defaults, production install docs, pinned release tags, status endpoint, top-menu entries, embedded viewer/control panel, and Shadow DOM CSS isolation. | These are baseline `main` capabilities now, not future readiness work. Remaining gaps are narrower: signing/archive, current HPB-internal env parity, docs/status freshness, privacy/storage, and release assets. |
| `#42` D-283 internal audio capture | Merged 2026-06-17 | Internal Talk auth mode and HPB/internal playback path for non-public conversations. | Changed default capture path; leaves ExApp `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` manifest/docs/status parity work. |
| `#43` VM harness and play command | Merged 2026-06-19 | Development harness/play-command enablement. | Useful validation tooling; not itself a store blocker. |
| `#45` D-288 1:1 | Merged 2026-06-19 | Private/1:1 playback, authenticated rotator users, synthetic private recording validation. | Useful validation evidence, but should still be folded into production-shaped AppAPI/HaRP e2e before broad private-meeting claims. |
| `#41` D-290 prod-path e2e | Open draft | Full-path AppAPI/HaRP install and recording CI. | Important readiness CI. PR body says install/proxy/HMAC start seam works, but the full recording lifecycle remains red in the branch. |
| `#79` owner-scoped recordings + Files delivery | Open | Owner-scoped published archives plus post-build delivery of `meeting.webm` and `summary.md` to owner Files. | Strong interim privacy/storage candidate; not invitee-based ACL and not full transcript/caption/viewer bundle delivery. |
| `#80` D-386 capture-gap warnings | Open | Surfaces in-call participants captured with no/low audio. | Reliability/supportability improvement; detection only, not a store mechanics blocker. |
| `#81` D-386 capture recovery | Open | Retries/rebuilds subscribers whose capture never starts. | Reliability improvement if external users will depend on multi-participant capture. |
| `#44` demo sandbox | Open against `d-290-e2e-testing` | Deployable Nextcloud sandbox workflow. | Useful for demos/screenshots; not a core submission blocker. |
| `#35` GPU transcript backfill | Open | GPU transcript backfill action. | Not a store blocker unless CUDA/backfill is promised in the listing. |
| `#46` Go skill initial pass | Open draft | Internal dev-skill/scaffolding work. | Not relevant to store readiness. |

## Blocker Classification

Likely hard blockers before a public listing:

- App-store certificate, app registration, signed archive, and release-upload path are not implemented.
- Public/open-source distribution and support links are unresolved because `codemyriad/gocassini` is currently private.
- License metadata is not store-ready: no root license file and `info.xml` uses deprecated `agpl` shorthand despite targeting Nextcloud 32+.
- No `CHANGELOG.md` release entry exists.
- No app-store screenshots, store copy, public docs links, or support contact are prepared.
- Default HPB-internal Talk capture cannot be configured through the ExApp manifest/admin UI because `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is undeclared.
- `docs/exapp-install.md` is stale and still documents public-room/guest limitations that no longer match main's default capture path.
- Admin status/doctor output does not verify internal signaling secret presence.
- Viewer/published archive access is org-wide for all logged-in users unless access-control work lands or the first listing is explicitly constrained.
- Storage/source-of-truth behavior is not product-settled: rich artifacts remain in Cassini storage, raw Talk upload goes to Files, and PR #79 only partially bridges that gap.
- Production archive preservation needs validation because publish rebuilds from `<work-root>/current/*.meeting`.
- Security/privacy docs are incomplete for recording data, transcripts, summaries, LLM egress, retention, deletion, and secret rotation.
- Store archive/signing dry-run and XSD validation are missing from CI.

Likely acceptable limitations if explicitly documented and approved for a preview listing:

- Private/group Talk support can remain bounded by deployment prerequisites and validation scope if the listing explains HPB-internal requirements.
- CUDA as advanced/optional deployment support rather than default path.
- ROCm unsupported despite pull-compatible alias tags.
- `linux/amd64` only if arm64 is not required by target deployment promises.
- Optional external LLM summaries disabled by default unless credentials are configured.

Decisions needed before slicing implementation:

- Is the first listing a beta/technical preview or a production app?
- Must viewer access control land before store submission?
- Is the merged HPB-internal/private-room work enough for the first listing, or must it be proven through prod-path AppAPI/HaRP e2e first?
- Must owner delivery to Nextcloud Files land before store submission?
- Is PR #79's owner-scoped archive/hybrid delivery direction acceptable, or is invited-user ACL required before listing?
- Is Cassini-owned storage acceptable as the source of truth for rich artifacts in the first store release?
- Should the public listing advertise CUDA at all?
- Should the repo, issue tracker, and docs be made public before certificate request?
- Who owns app-store credentials, signing keys, and release approval?

## Suggested Slice Boundaries

1. Store submission path spike

Confirm the App Store account, app id, certificate request, repo visibility requirements, and exact upload mechanism. Output should be a verified runbook and a decision about public repo/support URLs.

2. Manifest and metadata cleanup

Update license identifier, add license/changelog/docs metadata, decide categories, prepare copy, screenshots, support links, and run XSD validation.

3. App-store package/sign dry run

Create a staged `gocassini` archive, remove dev files, sign it, produce `gocassini.tar.gz`, verify contents, and wire a CI dry run without publishing.

4. Privacy and access-control gate

Decide whether `planning/initiatives/viewer-access-control/` is a hard blocker. If yes, implement enough owner/per-recording access control and direct asset authorization before store release.

5. ExApp production parity and Talk configuration

Declare HPB-internal env vars, update the status endpoint, align install docs with current capture mode, and prove private/restricted Talk capture through the production-shaped AppAPI/HaRP path.

6. Storage and delivery decision

Decide Cassini volume versus Nextcloud Files versus hybrid. If hybrid is accepted, review/land or port PR #79 and define follow-ups for transcript/caption/viewer bundle delivery, deletion, retention, and backup semantics.

7. Security/privacy/admin documentation

Write store-grade data-processing, storage, retention, deletion, LLM egress, recording consent, secret rotation, install, upgrade, rollback, and uninstall docs.

8. Pre-submission release rehearsal

Run the complete release checklist from a clean tag: build images, pull images, build/sign archive, validate metadata, run e2e, review limitations, and either submit or record blockers.

## References

- `planning/initiatives/nextcloud-store-readiness/brief.md`
- `planning/initiatives/nextcloud-store-readiness/work-plan.md`
- `planning/initiatives/production-exapp-recovery/brief.md`
- `planning/initiatives/production-exapp-recovery/production-failure-hypothesis.md`
- `planning/initiatives/production-exapp-recovery/exapp-suite-mismatch-exploration.md`
- `planning/initiatives/production-exapp-recovery/archive-overwrite-hypothesis.md`
- `planning/initiatives/viewer-access-control/brief.md`
- `planning/installable-nextcloud-app.md`
- `planning/initiatives/mvp/venture.md`
- `planning/initiatives/mvp/slices.md`
- `docs/exapp-install.md`
- `docs/exapp-test-locally.md`
- `appinfo/info.xml`
- `appinfo/app.php`
- `img/app.svg`
- `harness/bin/manual-test-setup.sh`
- `.github/workflows/publish-exapp-image.yml`
- Nextcloud App Developer Guide: `https://nextcloudappstore.readthedocs.io/en/latest/developer.html`
- Nextcloud app-store rules: `https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/publishing.html`
- Nextcloud code signing: `https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/code_signing.html`
- Nextcloud release process: `https://docs.nextcloud.com/server/latest/developer_manual/app_publishing_maintenance/release_process.html`
- Nextcloud app metadata: `https://docs.nextcloud.com/server/latest/developer_manual/app_development/info.html`
- Nextcloud ExApp development steps: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppDevelopmentSteps.html`
- Nextcloud ExApp overview: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppOverview.html`
- Nextcloud ExApp lifecycle: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppLifecycle.html`
- Nextcloud ExApp installation flow: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/tech_details/InstallationFlow.html`
- Nextcloud ExApp authentication: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/tech_details/Authentication.html`
- Nextcloud HaRP ExApp integration: `https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppHarpIntegration.html`
