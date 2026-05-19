# Configuration

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
| `CASSINI_OPERATOR_STATE_STORAGE` | bind-mount path replacing the default operator state volume |
| `CASSINI_PUBLISHED_SITE_STORAGE` | bind-mount path replacing the default published-site volume |

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

## Operator flags and env vars

Main flags:

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
| `CASSINI_OPERATOR_SITE_ROOT` | site-root path |
| `CASSINI_BIN` | Cassini CLI binary path |
| `CASSINI_MAX_RECORD_WORKERS` | record worker count |
| `CASSINI_MAX_BUILD_WORKERS` | build worker count |
| `WORK_ROOT` | legacy fallback work-root env |
| `SITE_ROOT` | legacy fallback site-root env |
| `MAX_RECORD_WORKERS` | legacy fallback record worker count |
| `MAX_BUILD_WORKERS` | legacy fallback build worker count |

## Control panel settings

The control panel primarily cares about:

| Variable | Purpose |
|---|---|
| `CASSINI_OPERATOR_URL` | upstream operator origin for the Vite dev/preview proxy |
| `CASSINI_OPERATOR_BASE_PATH` | public same-origin path the browser calls |

## Practical advice

- Change ports in `deployment/.env` when you have local conflicts.
- Use bind mounts when you want host-visible state.
- Leave optional capability env vars unset unless you are working on readable cleanup or summary generation.
