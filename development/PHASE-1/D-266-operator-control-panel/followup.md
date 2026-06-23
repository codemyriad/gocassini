# D-266 — Follow-up work still not done

This file enumerates work that was still desired by the Linear ticket, framing, or shaping, but was **not** completed in this unit of work.

The delivered v1 was intentionally narrowed to:

- start
- stop
- observe history/detail
- live updates

## 1. Rerun control in the UI

**Status:** not done

The original Linear ticket explicitly mentioned:

- "manually trigger jobs, reruns, etc."

Current state:

- `cassini-operator` already supports `POST /jobs/:id/rerun`
- the control panel shows `rerun_count`
- the control panel does **not** expose a rerun button or rerun flow

Follow-up work:

- add rerun action in selected-job detail
- define eligibility UX for failed jobs
- reconcile rerun responses into history/detail state

## 2. Richer manual trigger controls beyond URL-only start

**Status:** not done

Current state:

- the control panel exposes the minimal shaped trigger flow: paste meeting URL, click Start
- the UI does **not** expose the operator's richer request knobs such as guest name, duration, or room-empty behavior

Follow-up work:

- decide which advanced trigger fields deserve UI controls
- expose only the fields that matter operationally without overcomplicating the first screen

## 3. High-level stages only; no substages / fine-grained progress

**Status:** not done

This was explicitly called out in framing/shaping as a nice-to-have and first cutline.

Current state:

- the panel shows high-level stages: `record`, `build`, `publish`, `done`
- live events are emitted from persisted operator stage/state writes
- no finer-grained internal substage telemetry is shown

Not done:

- record substeps
- build substeps
- publish substeps
- richer in-flight progress markers inside each stage

Follow-up work:

- define substage vocabulary
- expose additional operator events or derived read-model fields
- render richer progress timelines in selected-job detail

## 4. Debug / failure-simulation interactions

**Status:** not done

This was the ticket's explicit "shoot for the stars" scope:

- simulate different failure modes
- observe recovery
- inspect error reporting
- exercise reruns

Current state:

- no simulation/debug controls exist in the control panel
- the UI only reflects real operator state

Follow-up work:

- define safe debug-only controls
- add failure injection/simulation modes
- make recovery and rerun behavior observable from the same surface

## 5. Richer failure inspection and recovery UX

**Status:** partially done, but not finished

Current state:

- selected-job detail shows top-level error text
- attempt history is shown
- some stop/error metadata is shown
- artifact and log paths are visible as raw values

What is still missing:

- dedicated log inspection UX
- clearer failure diagnosis views
- guided recovery actions
- tighter rerun-after-failure workflow in the UI

Follow-up work:

- expose meaningful failure panels rather than raw field dumps
- decide whether to link to logs/artifacts directly or proxy them through operator endpoints
- combine inspection + rerun flows

## 6. Production-ready deployment packaging

**Status:** not done

The Linear ticket asked for something portable and ready for production eventually.

Current state:

- the architecture is now proxy-friendly and same-origin friendly
- the app can be served behind Vite dev/preview or a reverse proxy
- no production packaging/deployment artifact was created as part of this work

What is still missing:

- Docker/container deployment story
- reverse-proxy deployment examples
- environment/setup documentation for hosted deployment
- any auth/session/deployment hardening needed for non-local operation

Follow-up work:

- define deployment target(s)
- add production serving docs/config examples
- package the control panel for real hosted use

## 7. Nextcloud integration / embedding

**Status:** not done

This was explicitly called out as a nice-to-have, not a first-cut goal.

Current state:

- the control panel is a standalone web app
- no Nextcloud shell/embed integration was built

Follow-up work:

- decide whether integration means iframe/embed, app wrapper, shared auth, or navigation integration
- define what must remain portable outside Nextcloud while supporting an embedded deployment

## 8. Visual polish closer to the GitHub Actions inspiration

**Status:** not done

The ticket notes cited GitHub Actions as inspiration, but styling was deliberately deferred.

Current state:

- the UI is functional
- there is no styling/polish pass aimed at a true GitHub-Actions-like dashboard experience

What is still missing:

- stronger information hierarchy
- denser run-history ergonomics
- richer stage visualization
- polished empty/loading/error states

Follow-up work:

- do a dedicated UX/styling pass after functionality stabilizes

## 9. Any broader production/runtime hardening beyond the v1 reconnect model

**Status:** not done

Current state:

- reconnect behavior is simple: refresh snapshots and resume listening
- polling fallback exists while disconnected

Still not done:

- stronger live-feed durability/replay semantics
- production-grade stream resume/cursor behavior
- any multi-operator or access-control story

This was not required for the first cut, but it remains part of the gap between the delivered dev-facing panel and a more production-ready control surface.

## Suggested next order

If work continues on D-266, the most natural order is:

1. rerun control
2. richer failure inspection / recovery UX
3. substage visibility
4. production packaging + deployment docs
5. Nextcloud integration
6. debug/failure simulation tooling
7. styling/polish pass
