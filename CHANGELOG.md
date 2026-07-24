# Changelog

All notable user-facing changes to Cassini will be documented in this file.

This project follows semantic versioning for Nextcloud App Store releases.
The version in `appinfo/info.xml`, the ExApp Docker image tag, and the release
archive version must match.

## [Unreleased]

Unreleased changes are collected as fragments in `changelog.d/`. Versioned
sections below are script-managed: `scripts/fold-changelog.sh` (run by
`scripts/prepare-release.sh`) folds those fragments into this file under the new
version and removes the consumed fragments. Edit released sections only to fix
mistakes; add new entries as fragments. See [`docs/release.md`](docs/release.md).

## [0.2.0-alpha.4] - 2026-07-24

### Added
- Added a unified `cassini dev stack up|down|status|plan` workflow with explicit `--services` topology modes, including support for remote harness runs and a visible stack plan.
- Restored the operator (recording-control) UI inside the single "Cassini" Nextcloud app as an **admin-gated surface** (D-420 V3): admins now get a **Browse | Operator** top nav, while non-admins keep the browse-only experience unchanged. The active surface is deep-linkable and back/forward-navigable via the URL fragment (`#surface=operator`), with no pathname router. Whether the operator surface is shown is decided by probing the operator API (403 → hidden); the operator JSON API stays admin-only in `info.xml` (the real security boundary), so a non-admin can never drive recordings even if they force the surface.
- Added negative controls (`harness/bin/test-session-artifact-packet-guard.sh`, `harness/bin/test-av-drift-pair-requirement.sh`) that feed the real verifiers synthetic artifacts and assert they go red on captures with no media and green on healthy ones. They run on every pull request in the fast lint job — no Docker, no stack, no media tooling — so the guards' behavior is demonstrated rather than assumed.
- `cassini dev stack plan`, `up`, and destructive `down` commands now surface non-fatal warnings for ignored settings, surprising recording paths, unreachable private media addresses, and resource-removing lifecycle options.

### Changed
- Standardized dev and CI harness execution around the same stack commands so e2e entrypoints and manual flows now use explicit, deterministic startup/topology flags.
- Reworked the viewer to load meeting data through a single `DataProvider` seam instead of hardcoded static fetches, decoupling the viewer UI from its data source (groundwork for unifying the viewer and operator into one in-Nextcloud app). No user-visible behaviour change.
- Collapsed the two Cassini Nextcloud top-menu entries ("Cassini" viewer + "Cassini Admin" control-panel) into a **single "Cassini" entry** backed by one in-Nextcloud app (`cassini-app`, which consumes the viewer as a layer). Every logged-in user gets the meeting-browsing app; the operator/recording-control surface returns as an admin-gated surface *inside* the one app in a follow-up (D-420 V3), and the operator JSON API stays admin-only throughout (unchanged security boundary). No change for viewer users.
- Split the in-Nextcloud viewer into a shell (`App`) plus reusable `MeetingList` and `MeetingView` components — the app-shell layer for unifying the viewer and operator into one in-Nextcloud app (`MeetingView` is also the future standalone single-meeting share surface). No user-visible behaviour change.
- Release CI now verifies that the exact CPU ExApp image can be installed through AppAPI/HaRP, record a Talk call, and publish a non-empty viewer transcript before image or harness changes merge.
- Reworked the Cassini operator panel for the in-Nextcloud audience it is now shown to: the recordings list and run detail work as a master/detail pair, each run's attempts collapse into accordions so a long retry history opens at a readable summary, starting a recording is an on-demand composer rather than a permanently parked URL field, and the API-implementation captions are replaced with plain descriptions.
- Rebuilt the connection indicator to state the connection first: a one-word status (Live / Connecting / Reconnecting / Offline) with the refresh cadence beside it, replacing a label that never said whether the panel was connected.
- Restyled the transcription settings as three matching sections — detected hardware, quality, and device and model overrides — shown side by side on wide screens, with the overrides no longer hidden behind a collapse.
- You can now link directly to a recording: the selected run is kept in the URL, and the back button returns to the list.

### Fixed
- Fixed harness teardown and bring-up edge cases so repeated runs no longer leave stale containers/networks or duplicate `signaling-public-proxy` aliases.
- Cassini shell polish (D-420 review follow-ups): switching between the **Browse** and **Operator** surfaces now keeps the open meeting in the URL (deep-link no longer dropped); the operator surface uses the same light/dark theme as browse (stored preference, then OS) instead of only the OS setting; and the in-Nextcloud mount targets the innermost `#content` more robustly.
- Added the missing `operator/settings` proxy route so the operator surface's STT-quality Settings panel works through the Nextcloud AppAPI proxy (it previously had no route and would have 403'd/404'd), and dropped the now-dead `control-panel` proxy routes left over from the collapsed second entry.
- Talk recorder: stabilized subscriber negotiation behind intermittent empty/blank recordings ("no remuxable streams found in session artifact", silent MKVs, and 0-word transcripts). A fresh in-call participant previously caused two empty-SID `requestoffer` messages; the second could make the MCU tear down and recreate its Janus subscriber, rotating the SID while the first offer was still being answered. Initial and exhausted-peer recovery now issue exactly one request, repeated in-call snapshots keep the valid response throttle, and inbound offers cannot trigger recovery in front of themselves. The recorder also sends its answer without waiting for full ICE gathering, queues trickled candidates behind the matching answer, and emits end-of-candidates only after gathering completes (D-454).
- Stopping a recording no longer fails with "job is not stoppable" when the operator has just reported the job as running. The stop registration now brackets the whole time the job is advertised as recording, so a stop is accepted from the moment the job goes running until it leaves the record stage, and one arriving before the recorder has finished starting up is delivered as soon as it does.
- A stop requested after the recording has already ended on its own (while the operator is still uploading to Talk and queueing the build) is now accepted and recorded as a stop request instead of being rejected; the job's stop reason continues to report why the recording actually ended.
- The release pipeline's required "Faithful installed ExApp" gate now decides whether to run from a deny-list (run unless a change is purely docs/notes/assets) instead of an allow-list of enumerated product paths, so a pull request touching a build input nobody thought to enumerate can no longer report the required check green without actually building and recording.
- Talk recorder: closed two gaps that could still lose media on a call the recorder appeared to be recording normally. A participant whose handshake completed but whose media never arrived was left untouched for up to ~45 seconds, so short calls (a ~30s 1:1) could still finalize as empty ("no remuxable streams"); such a peer is now rebuilt about 12 seconds after its answer, but only while its connection is not actually established, so a slow-but-healthy participant is never torn down. A participant who turned their camera on mid-call could also be wedged permanently by a dropped signaling answer — their audio kept recording while the newly added video was silently lost for the rest of the call; the recorder now retransmits that answer as the signaling connection recovers, within a bounded budget, instead of skipping the participant forever (D-509).
- Fixed the CI session-artifact guard passing recordings that captured no media. It verified headers and index framing but never counted packets, so a capture with a valid header and a 0-byte index reported "session artifact verification passed" — the exact empty-capture class (D-454) the per-PR e2e leg is relied on to catch. The guard now walks each stream log and requires real RTP media packets per captured stream, counted kind-aware so a stream carrying only RTCP no longer passes for having non-empty bookkeeping.
- Fixed the A/V drift check reporting success without measuring anything. It exited 0 when no audio/video pair was found, and also when pairs were found but every one was too short to compare — printing "av drift check passed" after zero comparisons. Drift runs now require at least one pair to be actually compared, and the rejoin leg, which legitimately records video-less, opts down explicitly.
- Fixed the e2e leg never producing a drift pair the check could measure. The publisher bots play the sample fixture once and stop at its end, and the default fixture is 15s — capping the measurable pair at ~11s against the 15s minimum regardless of the configured publish window. The leg now prepares a fixture sized to the publish window. The fixture is also rendered with a keyframe at least every 0.5s (libvpx's default 128-frame interval delayed decodable video up to ~4.3s behind audio, which the elapsed-difference metric misread as drift), and the leg's tolerance carries a documented budget for the remaining structural skew.
- Fixed panels, cards and form fields losing their borders and shadows inside Nextcloud, which left content areas with nothing to separate them.
- Fixed high-contrast themes leaving those same areas undefined, and the selected recording unmarked.
- Fixed Nextcloud's app chrome applying twice, which added a second header-height gap, doubled the side margins, and clipped the bottom of the app.
- Fixed the shell tab bar, the operator scrollbar gutter, and the detected-hardware values rendering with the wrong colours under some themes.
- Fixed the recordings list not appearing on narrow screens.
- Fixed the settings Save button offering to save when nothing had changed.
- Fixed the meeting filter drawing a second focus ring inside its own.
- Fixed the meeting load-in fade stalling and then snapping into place.

## [0.2.0-alpha.3] - 2026-07-10

### Added
- Cassini marketing and docs microsite (`cassini-microsite/`): homepage, changelog page, and docs section with sidebar navigation.
- The Cassini microsite is now published at https://gocassini.codemyriad.io.
- Release tooling for the Nextcloud App Store: a version-ladder CLI
  (`scripts/release-version.sh`), changelog folder (`scripts/fold-changelog.sh`),
  local release-prep orchestrator (`scripts/prepare-release.sh`), App Store
  package builder/validator, and a manual **Release** workflow that signs and
  publishes `gocassini.tar.gz`. See `docs/release.md`.

### Changed
- Nextcloud toolbar icon updated to the Cassini brand mark with white fills and a tighter viewBox for correct rendering in NC ExApp containers.
- Viewer restyled with the Saturn theme (ice blue and Saturn gold) and now shows a favicon. A previously saved light/dark choice resets once and follows your system setting until you set it again.

### Fixed
- Documentation pages on the microsite now show the site footer.
- The microsite changelog timeline no longer draws a trailing line below the last entry.
- Viewer meeting dates no longer append a timezone (e.g. "GMT+1") they cannot actually justify. Meeting date labels carry no timezone, so a UTC-derived time could be shown an hour off with a wrong-but-confident zone suffix. The viewer now renders the label's own wall-clock time and makes no timezone claim.
- The operator control panel no longer flashes to loading spinners every couple of seconds while watching an active recording. Background refreshes (the 2s polling fallback and event-stream reconnects) now update the jobs list and run detail in place instead of re-showing the first-load placeholder each tick.

## [0.2.0-alpha.2] - 2026-07-06

### Added
- The viewer's meeting list has a filter box for narrowing meetings by name or date.

### Fixed
- Talk recordings now show their conversation name and actual recording date in the viewer, instead of the raw job id as both name and date. When the name cannot be resolved, the meeting is shown as "Untitled meeting" with the recording date.
- Meetings whose date could not be read no longer scatter through the list; the list stays newest-first and undated entries sort last.
- Recordings publish to the viewer again. The static-site export crashed on every meeting — a regression that shipped with the D-462 meeting-name/date change (merged in #112) — so no recording reached the viewer. Publishing now completes normally.

## [0.2.0-alpha.1] - 2026-07-03

First public alpha.

### Added
- Added public project metadata: AGPLv3 license, changelog, security policy,
  and contribution notes.

### Security
- Marked deterministic harness credentials as development-only values and
  expanded ignore rules for local dependency and signing artifacts.

## [0.1.0] - 2026-07-01

- Initial Nextcloud AppAPI ExApp packaging for Cassini.
- Add AppAPI manifest metadata, Docker image install metadata, route access
  declarations, and documented production install flow.
- Include admin control panel, viewer, published meeting archive, and Talk
  recording backend routes in the ExApp surface.
