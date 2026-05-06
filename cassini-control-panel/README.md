# Cassini Control Panel

`cassini-control-panel` is the browser-facing operator UI for Cassini jobs.

Current slice status:

- snapshot-driven jobs history from `GET /jobs`
- snapshot-driven selected run detail from `GET /jobs/:id`
- meeting URL input shell (submit action lands in the next slice)

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
