# Doctor previews — the design environment

The gallery renders `RecordingSetup.svelte`, the Doctor panel the product ships,
against fixture `RecordingReadiness` payloads. No Nextcloud, no Talk, no
recording, no administrator account, no network. Every state the panel can reach
is one click away, including the ones that need a broken server to produce.

## Start it

```sh
npm run design --workspace cassini-app
```

That opens the scenario index on <http://localhost:5199>. Edits to the panel,
its copy or its styles hot-reload. The header carries three controls that stay
on screen while you work:

- **Scenario** — switch states without going back to the index.
- **Theme** — page theme (what Nextcloud supplies), Cassini light, Cassini dark.
- **Width** — full, 760px, 420px. The panel ships inside Nextcloud, where
  nobody resizes a browser to check the narrow layout, so it is here instead.

`#doctor-preview=<id>` is a direct link to one scenario, and
`#doctor-preview=` is the index — both shareable in a review.

## What you are looking at

The component is the real one. The data is not: checks, credential saves, test
preparation and re-indexing run in tab-local memory and reset on reload. Fixture
links point at `preview.invalid`. Links into Nextcloud's own settings are absent
here because the panel derives them from the page serving it.

Rows are ordered and worded as the operator sends them, and the fixtures are
kept in step with it by hand — the producing code is
`cassini-operator/internal/operator/recording_readiness.go` (plus
`recording_test_tool.go` and `search_readiness.go`), and
`cassini-go-recorder/internal/talk/readiness.go` for the Talk connection and
backend findings. **When you change a message, a code or an action there, update
`cassini-app/src/preview/doctorScenarios.ts` in the same commit.** Three tests
in `doctorScenarios.test.ts` guard the shape of this: row order, no row the
checklist no longer has, and no test recording offered where its prerequisites
do not hold.

What the fixtures deliberately do NOT do is run the operator's logic. A blocked
row is written as a blocked row, rather than computed by the dependency rules in
`readiness_prerequisites.go`. The gallery shows the operator's output; the Go
tests own the rules that produce it.

## The scenarios

| id | what it is for |
| --- | --- |
| `healthy` | everything passing, including a test recording somebody played back |
| `first-run` | a fresh install: nothing established, nothing claimed |
| `no-backend` | the common failure. One row to act on; three below it wait |
| `missing-secret` | the credential form, and the two places the value can be read |
| `rejected-secret` | a saved credential the backend refuses |
| `missing-hpb` | Talk has no signaling server; the row points at Nextcloud's docs |
| `connection-unreachable` | a check that ran and could not reach the server |
| `recording-handoff` | Talk does not know Cassini as a recording backend yet |
| `test-waiting` | the test is armed, waiting for somebody to press record in Talk |
| `test-published` | published; only a person pressing play can finish the check |
| `test-failed` | the one check whose failure is a finding, not a missing verdict |
| `storage-blocked` | recording refused on stored storage status |
| `setup-unreadable` | the persistent configuration cannot be read |
| `search-partial` | archive search is incomplete, with a repair button |
| `search-running` | a re-index in progress, with no duplicate repair button |
| `search-failed` | the last re-index failed and says so, retry available |
| `old-findings` | passing checks two days old: age beside the verdict, not inside it |
| `refresh-error` | a failed refresh keeps the findings and reports itself once |

## Shipping it to a reviewer

Ordinary builds eliminate the gallery and its fixtures: the whole thing is
behind `VITE_CASSINI_DOCTOR_PREVIEWS` in `AppEntry.svelte`. For a review
deployment:

```sh
VITE_CASSINI_DOCTOR_PREVIEWS=true npm run build:all --workspace cassini-app
```

It adds no operator endpoint and changes no server diagnostics, so it is safe on
a review host — it is still a build that renders simulated data, and should not
be what an administrator is given.
