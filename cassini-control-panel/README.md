# Cassini Control Panel

`cassini-control-panel` is the browser-facing operator UI for Cassini jobs.

Current slice status:

- jobs history from `GET /jobs` kept warm by `GET /events`
- selected run detail from `GET /jobs/:id` updated from the same tagged SSE feed
- start job via `POST /jobs?provider=nextcloud-talk`
- stop eligible record-stage jobs via `POST /jobs/:id/stop`
- snapshot refresh on stream reconnect, with polling fallback while disconnected

## Config

Set `CASSINI_OPERATOR_URL` before starting the dev server or build:

```bash
cd cassini-control-panel
export CASSINI_OPERATOR_URL=http://127.0.0.1:19080
npm run dev
```

If `CASSINI_OPERATOR_URL` is missing, the app fails clearly on load.

## Development

```bash
cd cassini-control-panel
npm run dev
```

## Build

```bash
cd cassini-control-panel
npm run build
```
