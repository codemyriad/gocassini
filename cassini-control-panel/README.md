# Cassini Control Panel

`cassini-control-panel` is the browser-facing operator UI for Cassini jobs.

Current slice status:

- jobs history from `GET /jobs` kept warm by `GET /events`
- selected run detail from `GET /jobs/:id` updated from the same tagged SSE feed
- start job via `POST /jobs?provider=nextcloud-talk`
- stop eligible record-stage jobs via `POST /jobs/:id/stop`
- snapshot refresh on stream reconnect, with polling fallback while disconnected

## Config

The browser talks to a same-origin operator proxy path, not to the operator origin directly.

- `CASSINI_OPERATOR_BASE_PATH` — public path the browser calls. Defaults to `/operator`.
- `CASSINI_OPERATOR_URL` — upstream operator origin for the Vite dev/preview proxy.

Development example:

```bash
cd cassini-control-panel
export CASSINI_OPERATOR_URL=http://127.0.0.1:4000
npm run dev
```

Optional custom public path:

```bash
export CASSINI_OPERATOR_BASE_PATH=/api/operator
```

## Development

```bash
cd cassini-control-panel
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run dev
```

This serves the app on Vite and proxies `/operator/*` (or your custom base path) to the upstream operator.
The browser never needs cross-origin access to `cassini-operator`.

## Build / preview

```bash
cd cassini-control-panel
npm run build
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run preview
```

For non-Vite hosting (for example Docker behind nginx/caddy/traefik), serve the built assets and proxy `CASSINI_OPERATOR_BASE_PATH` to the operator there as the same-origin route.
