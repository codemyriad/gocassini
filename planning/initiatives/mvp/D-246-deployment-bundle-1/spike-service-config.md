---
shaping: true
---

# Spike: service config contract for the deployment bundle

## Context

D-246 wants a deployable three-service compose bundle:

- `cassini-operator`
- `cassini-control-panel`
- `cassini-viewer`

The first thing to settle is how the services discover each other and how that maps onto env configuration.

Current baseline:

- `cassini-control-panel` uses browser-side `CASSINI_OPERATOR_BASE_PATH` and Vite proxy-side `CASSINI_OPERATOR_URL`
- `cassini-operator` binds via `--bind` and still uses mixed env names like `WORK_ROOT` / `SITE_ROOT`
- `cassini-viewer` is static-build friendly and naturally fits a shared published-site root
- the operator assumes a same-origin proxy and does not currently solve browser CORS

The earlier suggestion to split `CASSINI_OPERATOR_URL` into `CASSINI_OPERATOR_HOST` + `CASSINI_OPERATOR_PORT` has now been refined: operator bind host/port inside the container should be fixed defaults, while deployment config should focus on host-published ports, worker counts, and compose-managed shared volumes.

## Goal

Define the smallest clear `CASSINI_*` config contract that lets:

- compose publish the three services on the host cleanly
- the control panel know how to proxy to the operator
- the viewer know what to serve and on which port
- shared volumes be managed at compose level without turning every in-container path into a deployment knob

## Questions

| # | Question |
|---|----------|
| **X1-Q1** | Which settings are actually consumed by each service today: operator process, control-panel server, browser bundle, viewer server, and compose port publishing? |
| **X1-Q2** | If control-panel → operator traffic should go through the host-published operator port, what exact mechanism should the control-panel container use to reach that port while preserving same-origin browser behavior? |
| **X1-Q3** | Which current operator envs actually need to become canonical user-facing `CASSINI_*` knobs, and which should stay fixed in-container defaults? |
| **X1-Q4** | Which control-panel settings are build-time today versus runtime-at-container-start, and does that force any runtime config injection work? |
| **X1-Q5** | What is the minimum compose-managed shared-storage contract when DB/work/site/cache roots are fixed inside the containers? |

## Initial findings

1. **Current code and desired bundle behavior are slightly different.**
   Today `CASSINI_OPERATOR_BASE_PATH` is only a control-panel-side API path setting, while the operator serves routes at `/jobs`, `/events`, etc. with no configurable prefix.

2. **Selected shaping direction: make one shared API base path affect both sides.**
   In the deployment bundle, the same setting should:
   - prefix operator HTTP routes
   - tell the control panel which same-origin API path to call/proxy

   Example with default `/`:
   - `POST <host>:<port>/jobs`

   Example with `/operator`:
   - `POST <host>:<port>/operator/jobs`

3. **That means the current name is now acceptable if it becomes truly shared, but the behavior must change to match.**
   The important thing is the contract: one setting, used by both operator and control panel, defaulting to `/`.

4. **Operator bind can be treated as a fixed in-container default.**
   The shaping direction is now to hardcode `0.0.0.0:4000` in the operator container instead of exposing bind host/port as user-facing deployment config.

4. **The public/deployment contract can be much narrower than the full runtime contract.**

2. **Operator bind can be treated as a fixed in-container default.**
   The shaping direction is now to hardcode `0.0.0.0:4000` in the operator container instead of exposing bind host/port as user-facing deployment config.

3. **The public/deployment contract can be much narrower than the full runtime contract.**
   The main user-facing knobs appear to be:
   - host-published ports for operator, control panel, and viewer
   - worker counts
   - same-origin operator base path

4. **Shared paths do not need to be env-driven in v1.**
   Fixed in-container DB/work/site/cache paths are acceptable if compose controls whether they land on named volumes or bind mounts.

5. **The viewer side is still the cleanest boundary.**
   The operator already owns the published site root, so the natural bundle contract is:
   - operator mounts it read-write
   - viewer mounts it read-only

6. **Selected v1 answer for host-port reachability: use host networking where needed.**
   If we keep same-origin proxying and still route through the host-published operator port, host networking is an acceptable first-pass answer and avoids inventing an extra internal discovery contract just for the bundle.

## Direction to validate

A likely v1 contract is now much narrower:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH` (shared API route prefix, default `/`)
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

Likely non-configurable in v1:

- operator bind host = `0.0.0.0`
- operator in-container port = `4000`
- fixed in-container DB/work/site/cache roots

The control-panel app mount path remains a separate concern and should stay fixed unless we explicitly shape it later.

With host networking as the v1 answer, the internal upstream can stay simple and derived from the host-published operator port rather than becoming a separate user-facing discovery knob.

## Conclusions

- user-facing env contract can stay narrow:
  - `CASSINI_OPERATOR_PORT`
  - `CASSINI_CONTROL_PANEL_PORT`
  - `CASSINI_VIEWER_PORT`
  - `CASSINI_OPERATOR_BASE_PATH` (shared API route prefix, default `/`)
  - `CASSINI_MAX_RECORD_WORKERS`
  - `CASSINI_MAX_BUILD_WORKERS`
- fixed in-container defaults:
  - operator bind host `0.0.0.0`
  - operator in-container port `4000`
  - fixed DB/work/site/cache roots
- shared API base path affects both sides:
  - operator route prefix
  - control-panel request/proxy path
- root-base-path special case:
  - when `CASSINI_OPERATOR_BASE_PATH=/`, the control-panel proxy cannot be a catch-all `/` forwarder
  - it must proxy only the operator route set (`/jobs`, `/jobs/*`, `/events`) so the control-panel app/assets still serve from `/`
- selected v1 container reachability answer:
  - use host networking where needed so control-panel → operator can go through the host-published operator port without inventing a second internal service-discovery contract
- control-panel app mount path stays out of scope and fixed for now

## Acceptance

Spike is complete because:

- we can name the final user-facing `CASSINI_*` env contract for the three-service bundle
- we can describe which settings stay fixed in-container versus which are exposed for compose/runtime control
- we have pinned the v1 control-panel → operator host-port mechanism: host networking
- we can keep the shared API base-path setting simple in v1
