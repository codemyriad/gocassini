## V1 Spike: Separate `cassini-operator` package, CLI reuse, and failure reporting

### Context

We have now made a firm packaging decision for V1:

- the operator should live as a separate package at `<reporoot>/cassini-operator`
- the operator should remain minimal
- V1 should continue to use the existing Cassini build orchestration behavior, including doctor checks inside the build pipeline

This creates a concrete integration constraint:

- the current build/publish orchestration mostly lives in `cassini-go-recorder/internal/cassini`
- that logic is not exported as a public package boundary
- a separate package outside `cassini-go-recorder` cannot directly import `internal/cassini`

That makes the apparent options:

1. **Refactor shared orchestration into exported packages**
   - strongest long-term reuse story
   - highest immediate shaping / implementation cost
   - conflicts with the goal of keeping V1 minimal

2. **Invoke the Cassini CLI from `cassini-operator`**
   - preserves the existing orchestration behavior with minimal refactor
   - naturally includes current build flow steps like doctor
   - raises questions around failure reporting, binary location, and usage ergonomics

3. **Fallback simplification**
   - call the CLI
   - treat non-zero exit as failure
   - keep failure reason minimal or absent in V1 if richer extraction becomes too costly

We should only choose the fallback if the richer CLI-based path is still too complex.

### Current read

Current preferred direction for V1:

- keep `<reporoot>/cassini-operator` separate
- invoke the existing `cassini` CLI for build and publish
- do **not** refactor shared orchestration wrappers now
- resolve the Cassini binary via configured `CASSINI_BIN` when set; otherwise default to `<reporoot>/bin/cassini` in dev
- expose `cassini operator start` as a pure launcher that resolves the operator binary and `exec`s it with args forwarded unchanged
- launcher resolution is strict fail-fast: use `CASSINI_OPERATOR_BIN` when set, otherwise `<reporoot>/bin/cassini-operator`, and error if the chosen path is missing or not executable
- use operator stdout/stderr as the sensible V1 logging default
- on failure, inspect partial bundle `cassini.json` manifests from known output paths and use manifest `stage` + manifest `error` as the primary V1 failure-reporting contract
- use generic non-zero-exit failure only as a fallback when no partial manifest is available

### Goal

Identify the simplest viable design for a separate `cassini-operator` package that:

- keeps the operator separate
- reuses the existing Cassini build/publish orchestration behavior
- avoids substantial refactoring of build orchestration wrappers
- still gives the operator first-class enough failure information for V1 if that can be achieved simply

### Questions

| # | Question |
|---|----------|
| **V1-OP1** | What should the package/module shape of `<reporoot>/cassini-operator` be, given that `cassini-go-recorder` is its own Go module today? |
| **V1-OP2** | Given the selected convenience surface `cassini operator start`, what should operator usage and launcher behavior look like for local/dev and self-hosted contexts? |
| **V1-OP3** | Given the selected strategy—configured `CASSINI_BIN` in production-like environments and `<reporoot>/bin/cassini` by default in dev—what exact resolution order and validation should the operator implement? |
| **V1-OP4** | Can CLI invocation still provide usable failure reasons without refactoring build/publish orchestration, by reading the partial bundle `cassini.json` manifests and capturing stderr/stdout logs? |
| **V1-OP5** | For build failures, what is the minimum failure-reporting contract the operator can extract cheaply: exit code only, manifest `stage` + `error`, stderr tail, or a combination? |
| **V1-OP6** | For publish failures, can the operator use the same cheap pattern: known output dir + partial site bundle manifest + process stderr capture? |
| **V1-OP7** | What exact boundary should remain deferred to later work if we keep V1 minimal: typed error APIs, exported orchestration packages, richer structured failure causes, or all of the above? |

### Acceptance

This spike is complete when:

- we can describe the simplest package/module shape for `<reporoot>/cassini-operator`
- we can describe the intended operator usage shape and binary lookup strategy
- we can state clearly whether V1 should invoke the CLI or refactor shared orchestration now
- we can describe the simplest failure-reporting path available if the operator invokes the CLI
- we can describe what V1 deliberately does **not** solve yet if we choose the CLI path

### Recommended defaults

These are the current suggested defaults for V1 unless we find a strong reason to deviate.

#### 1. Package / module shape

- Create a separate package at `<reporoot>/cassini-operator`
- Give it its own Go module so the package boundary stays explicit
- Keep `cassini-go-recorder` unchanged as the owner of the existing Cassini CLI and orchestration

#### 2. Operator usage shape

- Primary implementation lives in the separate `cassini-operator` binary/package
- Expose `cassini operator start` as a pure launcher from the existing Cassini CLI surface
- The launcher does not define a second contract; it resolves the operator binary and `exec`s it with args forwarded unchanged

#### 3. Operator binary resolution

Recommended resolution order for `cassini operator start`:

1. `CASSINI_OPERATOR_BIN` if set
2. otherwise `<reporoot>/bin/cassini-operator`

Validation:

- fail fast if the selected path does not exist
- fail fast if the selected path is not executable
- print one dev-friendly launch line before exec, e.g. `operator -> /abs/path/to/cassini-operator`

#### 4. Cassini CLI resolution inside `cassini-operator`

Recommended resolution order:

1. `CASSINI_BIN` if set
2. otherwise `<reporoot>/bin/cassini`

Validation:

- fail fast at operator startup if the selected Cassini CLI path does not exist or is not executable
- keep the resolution logic explicit and small; no extra fallback chain in V1

#### 5. Build / publish invocation strategy

- Keep `cassini-operator` separate
- Invoke the existing `cassini build` CLI for build stage
- Invoke the existing `cassini publish` CLI for publish stage
- Do not refactor shared orchestration wrappers in V1
- Keep doctor inside the build path by reusing the CLI behavior rather than reconstructing the flow

#### 6. Failure reporting contract

Recommended V1 failure detail sources, in priority order:

1. partial bundle `cassini.json` manifest from known output path
2. manifest `stage`
3. manifest `error`
4. generic non-zero exit fallback if no manifest exists

Concretely:

- for build failure, inspect the known meeting output dir for `cassini.json`
- for publish failure, inspect the known site output dir for `cassini.json`
- use operator stdout/stderr as the sensible V1 logging default
- store lightweight failure detail in `jobs.error`
- do not attempt typed/structured failure APIs in V1
- do not extend the `jobs` schema for log-path metadata in V1

#### 7. Fixture acquisition and record placeholder

Recommended V1 fixture defaults:

- `FIXTURE_PATH` must end with `.mkv`
- default `FIXTURE_PATH` to `harness/runtime/operator-fixture.mkv`
- fetch lazily on first record-stage use, not at startup
- guard acquisition with a process-local mutex
- if local fixture exists, reuse it
- otherwise download to `FIXTURE_PATH.part` and atomically rename to `FIXTURE_PATH`
- create per-job run bundle with:
  - `PrepareRunBundle(..., false)`
  - copy fixture to `recording.mkv`
  - `FinalizeRunBundle(..., RunManifest{SourceMode: "talk", RecorderName: "CassiniOperatorFixture"})`

#### 8. What V1 deliberately defers

- exported shared orchestration packages
- typed build/publish error APIs
- retry / rerun semantics
- fixture versioning / checksum refresh logic
- request-selected fixture variants
- richer auth/protection beyond external protection assumptions

### Remaining open questions

- whether manifest `stage` + manifest `error` proves sufficient V1 failure detail in practice
- whether the separate `cassini-operator` module needs any additional repo/workspace convenience beyond the selected pure-launcher approach
