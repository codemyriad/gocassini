# Repository consistency pass — 2026-09-19

Reviewed the public README and microsite, installation and development guides,
privacy/access claims, CLI examples, current app and operator routing, Compose
services, tracked large files, relative Markdown links and changelog formatting.
Implementation and checked-in deployment configuration were the reference for
current behavior. Historical proposals and investigation notes remain historical.

## Corrected

- **Recording access:** the README and public introduction promised participant-only
  access unconditionally. Fresh installs use the everyone audience; private-room
  restrictions require enabling participant access, and public-room recordings
  remain visible to signed-in accounts. Permission tables now include invitees
  and the public-room exception.
- **Privacy:** the documentation index omitted insights from operations that send
  text to an LLM. The privacy guide now distinguishes the roster captured during
  recording for later restriction from the audience resolved at initial publication.
  It also describes the existing Operator action for narrowing eligible recordings.
- **CPU transcription:** the install guide still called the portable image an
  unsupported inference configuration; quick start expected a readiness failure
  on CPU. Both now describe supported CPU builds and explicit CUDA overrides.
- **Navigation and deployment:** current guides referenced a removed Cassini Admin
  menu, `cassini-control-panel` directory, Compose service and port 4173. They now
  describe one Cassini app with an Operator section, the two-service Compose bundle,
  and the separate Vite development server.
- **Development proxy:** the default root-mounted proxy omitted status, setup,
  settings, storage, AI-provider and Talk-provisioning routes. This prevented the
  admin probe from reaching the operator and could hide the Operator section.
  Added routing and request-level tests for root and prefixed APIs.
- **Homepage example:** replaced nonexistent `record --room` with `--call` and
  `--out`; made the example output filename agree with the command.
- **Processing and storage descriptions:** removed the promise that post-recording
  transcription can never compete with a live call, corrected the production
  publish destination to Nextcloud Files, and updated obsolete claims that the
  app viewer cannot edit annotations or display artifact/log paths.
- **Legacy documentation:** marked the removed Python transcriber architecture as
  historical and linked the active Go pipeline. Corrected a changelog sentence
  that presented sherpa-onnx and Parakeet as alternative CPU/GPU engines.
- **Generated binary:** removed the tracked 15 MB `three-song-rotator` executable
  and ignored future local builds. Both harness launch scripts use `go run .`;
  source and media fixtures remain present.
- **Release notes:** three existing changelog fragments lacked required headings
  and prevented the release-note validator from completing. Fixed their formatting
  while preserving their content; added a fragment for this pass.

## Verification

- App test suite: **520 tests passed**, including the new proxy requests.
- Standalone and embedded app builds passed, including the single-bundle check.
- Microsite build passed: **11 pages**; generated local page/asset targets resolved.
- Tracked Markdown relative-link file targets resolved (external URLs and anchors
  were not checked).
- All changelog fragments passed `fold-changelog.sh --version 0.2.0-beta.7 --check`.
  This was validation only, not a version bump or release.
- `git diff --check` passed.

## Limits

This was a source and documentation consistency pass, not a live conference-demo
rehearsal or an exhaustive code/security audit. No Nextcloud installation,
recording round trip, GPU transcription, deployment or publishing was performed.
Builds still report existing Svelte accessibility/unused-code warnings and a
microsite syntax-highlighting fallback for `caddyfile`; these do not fail builds.

## Follow-up review

A second code review found and reproduced four additional failures:

- Root-mounted development proxies still omitted `/readiness` and
  `/readiness/check`, breaking recording readiness checks. Both now reach the
  backend; request-level coverage also exercises `PUT /talk/setup`.
- Insights, annotations and prefixed operator proxies matched unrelated paths
  beginning with the API name. They now require a path boundary and escape
  regex characters in custom prefixes, leaving similarly named assets to Vite.
- Operator detection accepted any HTTP 200, including a login page or SPA
  fallback. Successful responses must now contain the operator status payload,
  just like unhealthy responses; genuine operator 503 diagnoses remain visible.
- After installing prerequisite apps, storage setup could reuse the old plan
  when the refreshed response omitted the selected mode. It now stops with an
  actionable error before issuing browser writes from that stale plan.

Regression tests failed before the fixes and passed afterward. The full app
suite passed **538 tests across 33 files**; standalone and embedded app builds
passed, including the single-bundle assertion. Existing non-failing Svelte
warnings remain. These checks did not include a live Nextcloud round trip.

## Go and documentation follow-up — 2026-09-20

Merged current `main` (`df09afa9`) before final verification.

- Replaced four hand-written integer min/max helpers with Go built-ins, and
  made workflow-registry reads accept a context without an HTTP request or
  unused Runtime receiver.
- Shortened repetitive comments around workflow validation, search aliases,
  artifact promotion and digests, retaining the relevant contracts and
  credential/isolation constraints.
- Changed atomic writes to use unique temporary files with cleanup on failure.
  Regression tests reproduced collisions between concurrent writers and the
  removal of an unrelated existing `.tmp` file before the fix.
- Removed two scratch reproduction tests: one only printed diagnostics; the
  other reimplemented the old, broken setup order. The actual Go setup-plan
  and frontend setup orchestration tests remain.
- Corrected architecture, mental-model, operator, API and component docs that
  still described three services, separate apps, a viewer with no backend
  calls, or all setup writes passing through the operator. Added missing active
  modules to the architecture table, corrected build/publish example paths and
  included the viewer shell in examples intended for standalone serving.
- Removed the resource-isolation claim from the docs index and moved the sole
  remaining Python-transcriber document into `docs/history/`, updating links.

Checks on the combined branch: operator `go test -race ./...` and `go vet ./...`,
transcription package tests, **537 app tests**, **904 viewer tests**, both app
builds, the embedded bundle assertion, and the **12-page** microsite build all
passed. The app count is one lower because the scratch reproduction was removed.
All **329** checked relative Markdown file links resolve. Changelog validation
and diff whitespace checks passed. Existing Svelte warnings remain; no live
Nextcloud recording round trip was performed. The previously published HTML
report describes the original September 19 pass, not these follow-up changes.
