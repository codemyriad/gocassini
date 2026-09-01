# How the capture payload reaches Talk's page

Date: 2026-09-01

Status: **decision open.** The service-worker route is implemented (PR #228);
the companion-app route is specified here and not built.

This document exists because PR #228 shipped one answer and its review
concluded that answer is not the right long-term one. The code is written as if
the delivery mechanism is replaceable, and it is — this says what replacing it
costs and what the replacement looks like.

## The problem

Cassini is an AppAPI ExApp: a container. No PHP of ours runs inside Nextcloud.
The capture payload has to execute on Talk's call page, in that page's origin,
with access to the outgoing `RTCRtpSender`. Something has to put it there.

## Route A — service worker (implemented)

The operator serves a service worker; the Cassini page registers it at Talk's
call scopes; from a page it controls it serves Talk's own bundle with the
payload appended. See [source-audio-capture.md](source-audio-capture.md).

It works, it is tested, and it needs no PHP. Its problems are structural rather
than fixable:

- It rewrites another application's JavaScript. Every defence in `sw.ts` —
  status, MIME, sentinel, range, same-origin, destination — exists because
  getting that wrong breaks Talk rather than Cassini.
- **Turning it off does not turn it off.** A 404 on the worker script does not
  deactivate an installed service worker. That single fact is why the payload
  polls an operator endpoint every 30 seconds, why the permission answer is
  cached, and why "not answered yet" has to count as no.
- It must coexist with core's Files preview worker at `/`, so the scopes are
  narrow and the reasoning about them is load-bearing.
- It would not survive Nextcloud app store review.

Appropriate for a deployment whose operator controls it. Not a foundation.

## Route B — companion PHP app (specified, not built)

Talk dispatches `OCP\Collaboration\Resources\LoadAdditionalScriptsEvent` from
its `PageController` — on the call page and again on the public/guest page
(verified in spreed `main`, `lib/Controller/PageController.php`, the two
`dispatchTyped(new LoadAdditionalScriptsEvent())` calls). It is core public
API: `OCP` namespace, `@since 25.0.0`, and its docblock states its purpose is
for apps to register their own frontend scripts.

This is the sanctioned hook. An earlier claim in this repo's history that Talk
offers no such extension point was wrong — it was true of Talk's *JavaScript*
APIs and false for a PHP app.

### Shape

A second, deliberately tiny Nextcloud app (`cassini_capture`), containing no
business logic:

1. `Application.php` implementing `IBootstrap`, registering a listener for
   `LoadAdditionalScriptsEvent`.
2. The listener **scopes itself to Talk**, and this is the one real trap: Files,
   activity, polls and files_sharing dispatch the same event, so an unscoped
   listener loads the payload on all of them. Check the resolved route before
   adding anything. (Verify the current route names against spreed `main` —
   they are no longer in `appinfo/routes.php`, which has moved to attribute
   routing.)
3. `Util::addScript()` ships the existing payload as an ordinary app script,
   loaded under the page's own nonce.
4. `IInitialState` provides the ExApp proxy base and whether capture is enabled.

The ExApp is unchanged: upload intake, ingestion, the operator, both admin
switches.

### What it buys

- **The bundle rewriting disappears entirely**, and with it every failure mode
  `sw.ts` defends against.
- **Deactivation becomes real.** Disable the app and the script stops loading.
- **The startup race disappears.** `IInitialState` puts the permission answer on
  the page before any code runs, so `serverAllowsCapture` and its "not answered
  yet counts as no" edge case are replaced by a value that is simply present. A
  poll is still wanted for revocation *during* a call; the startup path stops
  needing one.
- No `Service-Worker-Allowed`, no scope coexistence with core's Files worker.

### What it costs

A second app-store listing and release cadence, a version-compatibility matrix
between the two apps, PHP in an otherwise Go/TypeScript codebase, and admins
installing two things.

## Route C — upstream

A documented call-page extension point in spreed, or Talk growing local
recording itself. The only version where this stops being Cassini's problem.
Multi-quarter; not exclusive with B, which is the bridge to it.

## Migration brief (A → B)

The capture, timing, upload and ingestion code is identical under either route.
Only delivery changes.

**Delete**
- `cassini-app/src/capture/sw.ts` and `sw.test.ts`
- the service-worker branch of `cassini-operator/internal/operator/capture_assets.go`
  (the `Service-Worker-Allowed` header and `capture-sw.js`), and its tests
- service-worker registration in `cassini-app/src/capture/register.ts`
- the `ui/capture-sw.js` route in `appinfo/info.xml`
- `capture-sw` from `cassini-app/scripts/build-capture.mjs`, and with it the
  payload-inlining step — the payload becomes a normal script again

**Add**
- the `cassini_capture` PHP app
- its release/packaging job

**Simplify**
- `payload.ts`: replace `serverAllowsCapture` + `refreshCapturePermission`
  bootstrap with the injected initial state. Keep the periodic check for
  mid-call revocation and keep it failing closed.

**Keep unchanged**
- `payload.ts` capture logic, `worker.ts`, `protocol.ts`
- `capture_upload.go`, `capture_assets.go` (payload + worker assets)
- `internal/transcribe/sourceaudio.go` and the whole ingestion path
- five of the six browser tests: the fixture serves the payload with a script
  tag instead of through a worker. The sixth (bundle rewriting) goes with
  `sw.ts`.

**Verify**
- the listener does not fire on Files, activity or polls pages
- disabling the app stops the payload loading, with no stale worker anywhere
- the encoded transform still attaches before negotiation (this is what broke
  when the permission check moved later in PR #228 — the recordings had no
  timing anchors at all, and the browser test caught it)

## Recommendation

Keep A behind its default-off switches while the audio thesis is being proven —
it is built and tested and nothing about it is load-bearing for that question.
Build B before this is offered to anyone who did not write it.
