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
| `OPENROUTER_API_KEY` | summary capability configuration |
| `OPENROUTER_BASE_URL` | summary capability configuration |
| `LLM_BASE_URL` | summary capability configuration |
| `LLM_MODEL` | summary model (SUMMARY_MODEL overrides) |
| `SUMMARY_MODEL` | summary generation model |
| `CASSINI_SUMMARY_DISABLED` | disable summary generation |
| `CASSINI_STT_BACKEND` | speech-to-text engine id (default `sherpa-onnx`; unknown ids fail the build loudly) |
| `CASSINI_ATTRIBUTION_DISABLED` | skip the cross-track speaker-attribution measurement |
| `CASSINI_ATTRIBUTION_DROP` | delete words the attribution evidence contradicts instead of annotating them |

The base URL is what enables these steps; the API key is optional, so a
self-hosted OpenAI-compatible endpoint with no authentication works.

### Per-step endpoints

Each LLM step can be pointed at its own endpoint, overriding the shared
variables above. The steps are `SUMMARY` (the summary written when a meeting
publishes) and `INSIGHT` (`cassini insight run`):

| Variable | Purpose |
|---|---|
| `<STEP>_BASE_URL` | this step's endpoint, replacing the shared one |
| `<STEP>_API_KEY` | this step's key |
| `<STEP>_MODEL` | this step's model |
| `<STEP>_TIMEOUT_SEC` | this step's request timeout, replacing `CASSINI_LLM_TIMEOUT_SEC` |
| `<STEP>_MAX_TOKENS` | this step's response token limit, replacing `CASSINI_LLM_MAX_TOKENS` |

An endpoint override brings its own key: setting `<STEP>_BASE_URL` without
`<STEP>_API_KEY` sends no key at all rather than the shared one, because a key
must never travel to a host it was not issued for. A model or a bound on its
own keeps whatever endpoint it is layered over.

`INSIGHT_*` layers over `SUMMARY_*`, not just over the shared variables, so a
deployment that only ever configured a summary endpoint can still run insights
— they simply run on the summary endpoint. Set `INSIGHT_BASE_URL` when insights
should go elsewhere, which is the case worth having: a small local model can
write every meeting's summary while a larger hosted one answers a question you
ask by hand. The bounds are per step for the same reason — a CPU-bound local
model needs a far longer leash than a hosted API.

`CASSINI_SUMMARY_DISABLED` turns off the summary written at publish time. It
does not affect `cassini insight run`: that is a document someone asked for by
name, and turning insights off means removing the endpoint.

When the recorder is run by the operator, these LLM variables only seed the
operator's own LLM settings on its first start (`llm-settings.json` beside the
job database; `GET`/`PUT /settings/llm`). After that the persisted settings —
not the environment — are what every build receives, so endpoints and models
change without a redeploy. The operator emits the per-step variables itself
from that file; an insight step with no endpoint of its own emits nothing,
which is how the fallback above takes effect. Each step also names the workflow
it runs (`summary.template` / `insight.template`); empty means the workflow
Cassini ships.

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
| `--sink` | where published meetings are delivered (`local` or `nextcloud-files`; an installed ExApp defaults to `nextcloud-files`, otherwise `local`) |
| `--artifact-retention` | which attempt payloads under `runs/` are pruned (`all`, `superseded`, `sealed`; default `sealed`) |
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
| `CASSINI_PUBLISH_SINK` | publish sink name; `--sink` wins over it. Declared in `appinfo/info.xml` so AppAPI injects it |
| `CASSINI_ARTIFACT_RETENTION` | artifact retention policy; `--artifact-retention` wins over it |
| `SITE_ROOT` | fallback site-root env |
| `CASSINI_BIN` | Cassini CLI binary path |
| `CASSINI_MAX_RECORD_WORKERS` | record worker count |
| `MAX_RECORD_WORKERS` | fallback record worker count |
| `CASSINI_MAX_BUILD_WORKERS` | build worker count |
| `MAX_BUILD_WORKERS` | fallback build worker count |

### Artifact retention policies

| Policy | Prunes |
|---|---|
| `all` | nothing — the behaviour before this policy existed, and the escape hatch |
| `superseded` | the `.run`, `.meeting`, `.site` and `.seal` of attempts a rerun has replaced |
| `sealed` **(default)** | `superseded`, plus a succeeded attempt's `.run`, `.meeting` and `.site`, keeping its sealed `.opus` |

Nothing in `current/`, no attempt `.logs` directory and no published recording is
ever pruned, and every removal is guarded on the artifact that replaces it
existing. A successfully delivered attempt's `.site` is the one exception to
`all`: it is removed once the sink accepts it regardless of policy, because
keeping it leaves a full copy of the recording on the app's own volume, outside
the access model (D-550). An unrecognised policy name is rejected at startup with
exit code 2 and the valid names listed — the same rule `--sink` follows. See
[Artifacts and filesystem](./artifacts-and-filesystem.md#retention).

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
- Leave capability env vars unset unless you are specifically working on summary generation.

## See also

- [Running the local developer stack](../local-developer-stack.md)
- [Artifacts and filesystem](./artifacts-and-filesystem.md)
