# How the capture payload reaches Talk's page

Date: 2026-09-01

Status: **Route B implemented.** The unsupported service-worker prototype has
been removed. Source capture is delivered by the small `cassini_capture` native
Nextcloud companion app.

## The problem

Cassini is an AppAPI ExApp, so its container cannot add PHP or scripts to
another Nextcloud app's page. The capture payload nevertheless has to run in
Talk's origin, before Talk negotiates its publishing peer connection, to attach
the outgoing encoded transform in time.

## The implemented route

Talk dispatches the public
`OCP\Collaboration\Resources\LoadAdditionalScriptsEvent` while rendering an
authenticated call. The event is part of the public `OCP` API since Nextcloud
25 and exists specifically so apps can add frontend scripts.

`cassini_capture` does only four things:

1. Registers a listener for `LoadAdditionalScriptsEvent`.
2. Allows only Talk call routes (`showCall`, `authenticatePassword`, and
   `index`). Route names are compared case-insensitively because Nextcloud's
   generated route casing changed; recording playback, Files, activity, polls,
   and every other dispatcher of the shared event are excluded.
3. Uses `Util::addScript()` to load the existing capture payload as an ordinary
   nonce-authorized script. No Talk asset is intercepted or rewritten.
4. Injects `enabled` and the ExApp proxy base through `IInitialState` before the
   script runs. The ExApp synchronizes its `CASSINI_SOURCE_CAPTURE` value into
   AppAPI's ExApp config store on lifecycle edges, and the companion reads that
   store through `ExAppConfigService` (ordinary `IAppConfig` cannot see ExApps).
   That service is resolved from the server container while the event is being
   handled, not type-hinted in the constructor: a constructor parameter is
   resolved by the container inside Talk's dispatcher, where an install without
   AppAPI would raise past every catch of ours and take the call page with it.
   Resolved late, a missing AppAPI is just a switch that answers no.

The payload is deliberately loaded before Talk's bundle. Its `addTrack` wrapper
therefore attaches an inert `RTCRtpScriptTransform` synchronously, before
negotiation. It creates no audio file or OPFS directory until Talk's confirmed
recording-active status; confirmed recording-off seals and uploads immediately.
The browser e2e asserts that ordering against a real `RTCPeerConnection`.

The ExApp remains responsible for the worker, OPFS capture, upload intake, the
periodic fail-closed revocation check, and ingestion. The companion contains no
audio or storage logic.

## Why the service-worker route was removed

The prototype claimed Talk's call scopes and served Talk's own JavaScript with
the payload appended. It was rejected as a shipping mechanism because a capture
bug could corrupt Talk's bundle, it competed with browser worker scope, an
installed worker survived a server-side disable, and it was not an App Store
foundation.

The migration removes `sw.ts`, `Service-Worker-Allowed`, the service-worker
route, registration code, and bundle inlining. The Cassini page and the new
payload both unregister the old worker by its exact script URL, without touching
Files- or Talk-owned workers. The server gate and periodic poll still protect a
client whose obsolete worker has not yet been retired.

## Authenticated calls only

An earlier version of this document misread Talk's two event dispatches as the
authenticated and guest call paths. They are the authenticated call page and
recording playback. Current Talk's `guestEnterRoom()` and invited-email path do
not dispatch `LoadAdditionalScriptsEvent`.

That is not merely a delivery omission: Cassini's upload route is USER-level and
binds an upload to the AppAPI-authenticated participant. Supporting anonymous
guests safely needs both an upstream call-page hook and a short-lived,
attendance-bound upload credential. Until both exist, guest source capture is
intentionally unsupported.

## Packaging and compatibility

`scripts/build-capture-companion.sh` builds the TypeScript payload and produces
`cassini_capture.tar.gz` with the native app at the archive root. CI lints and
contract-tests the PHP listener, runs the browser chain, packages the app, and
uploads it as a workflow artifact. Tagged releases attach the companion tarball
and checksum beside the ExApp package. Release tooling and the release gate keep
the companion manifest at the same version as `gocassini` so an administrator
installs a matched pair.

Disabling the companion stops injection on new page loads. To disable collection
operationally, turn off `CASSINI_SOURCE_CAPTURE` first; that reaches already-open
calls through the 30-second poll and makes uploads fail closed, then disable the
companion app.
