# Configuration reference

This page collects the main configuration knobs for local development and deployment packaging.

## Deployment bundle (`deployment/.env`)

The checked-in deployment bundle exposes these main public knobs:

| Variable | Purpose | Default |
|---|---|---|
| `CASSINI_OPERATOR_PORT` | host port for operator API | `4000` |
| `CASSINI_CONTROL_PANEL_PORT` | host port for control panel UI | `4173` |
| `CASSINI_VIEWER_PORT` | host port for viewer UI | `8765` |
| `CASSINI_OPERATOR_BASE_PATH` | same-origin browser path used for operator requests | `/` |
| `CASSINI_MAX_RECORD_WORKERS` | concurrent live recording slots | `1` |
| `CASSINI_MAX_BUILD_WORKERS` | concurrent build workers | `1` |

Optional storage overrides:

| Variable | Purpose |
|---|---|
| `CASSINI_OPERATOR_STATE_STORAGE` | bind-mount path to replace the default operator state volume |
| `CASSINI_PUBLISHED_SITE_STORAGE` | bind-mount path to replace the default published-site volume |

Optional capability pass-through:

| Variable | Purpose |
|---|---|
| `OPENROUTER_API_KEY` | readable/summary capability configuration |
| `OPENROUTER_BASE_URL` | readable/summary capability configuration |
| `LLM_BASE_URL` | readable/summary capability configuration |
| `LLM_MODEL` | readable cleanup model |
| `SUMMARY_MODEL` | summary generation model |
| `CASSINI_SUMMARY_DISABLED` | disable summary generation |
| `CASSINI_READABLE_STRICT_BATCHES` | readable cleanup behavior |

Those capability variables affect optional build layers. They are not required just to bring the base stack up.

## Operator process flags and env vars

The operator supports these main flags:

| Flag | Purpose |
|---|---|
| `--bind` | HTTP bind address |
| `--base-path` | HTTP route prefix |
| `--db` | SQLite DB path |
| `--work-root` | per-job artifact root |
| `--site-root` | live published site root |
| `--cassini-bin` | Cassini CLI binary path |
| `--max-record-workers` | recording slot count |
| `--max-build-workers` | build worker count |

Important env vars:

| Env var | Purpose |
|---|---|
| `CASSINI_OPERATOR_BIND_ADDR` | operator bind address |
| `CASSINI_OPERATOR_BASE_PATH` | operator base path |
| `CASSINI_OPERATOR_DB_PATH` | SQLite path |
| `CASSINI_OPERATOR_WORK_ROOT` | work-root path |
| `WORK_ROOT` | fallback work-root env |
| `CASSINI_OPERATOR_SITE_ROOT` | site-root path |
| `SITE_ROOT` | fallback site-root env |
| `CASSINI_BIN` | Cassini CLI binary path |
| `CASSINI_MAX_RECORD_WORKERS` | record worker count |
| `MAX_RECORD_WORKERS` | fallback record worker count |
| `CASSINI_MAX_BUILD_WORKERS` | build worker count |
| `MAX_BUILD_WORKERS` | fallback build worker count |

Local non-containerized defaults resolve under:

```text
cassini-operator/runtime/
```

Typical local defaults:

- DB: `cassini-operator/runtime/jobs.sqlite3`
- work root: `cassini-operator/runtime/jobs`
- site root: `cassini-operator/runtime/site`

## Control panel development config

The control panel mainly cares about two settings:

| Variable | Purpose |
|---|---|
| `CASSINI_OPERATOR_URL` | upstream operator origin for the Vite dev/preview proxy |
| `CASSINI_OPERATOR_BASE_PATH` | public same-origin path the browser calls |

Development example:

```bash
cd cassini-control-panel
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run dev
```

One nuance:

- the standalone control-panel README often uses `/operator` as the base-path example
- the checked-in deployment bundle currently defaults to `/`

Both are valid as long as the proxying and browser path agree.

## Viewer development config

The viewer’s most common local development need is demo data.

Typical workflow:

```bash
cd cassini-viewer
npm install
npm run demo-data:pull
npm run dev
```

Before pulling demo data, set `DEMO_DATA_URL` in a local shell or gitignored `.envrc`.

## Practical advice

- Change ports in `deployment/.env` when you have local conflicts.
- Use bind mounts when you want to inspect state from the host filesystem.
- Leave capability env vars unset unless you are specifically working on readable cleanup or summary generation.

## See also

- [Running the local developer stack](../local-developer-stack.md)
- [Artifacts and filesystem](./artifacts-and-filesystem.md)
