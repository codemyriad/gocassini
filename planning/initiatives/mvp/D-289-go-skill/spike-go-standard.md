---
shaping: true
---

# X3 Spike: Initial Go standards to formalize

## Context

We have already agreed on the system shape:

- `AGENTS.md` stays light
- `.agents/skills/go-coding-standard/SKILL.md` will be the canonical Go standard artifact
- `.agents/prompts/refactor-to-standard.md` will be the generic refactor prompt
- Pi will wire the shared prompt in via `.pi/settings.json`

This spike is about agreeing on the **initial Go standards themselves**.

## Goal

Infer the repo's existing Go conventions, identify meaningful outliers, and propose the first standards to formalize in the Go skill.

## Repo signals gathered for this spike

- The repo currently has **3 Go modules**:
  - `cassini-go-recorder/`
  - `cassini-operator/`
  - `harness/go-talk-rotator/`
- The repo currently has **~130 Go files**.
- There is **no shared root Go lint config** such as `.golangci.yml`.
- There are **no existing `testify` / `assert` / `require` usages** in Go code.
- There are only **4 current `t.Parallel()` usages**, all in isolated unit-style tests.
- There are **118 functions** already taking `context.Context`.
- There are **~520 `%w` error wraps** and **57 `errors.Is` / `errors.As` usages**.
- There are only **5 interface definitions** across the repo Go code.
- There are only **2 `panic(...)` usages** in non-test Go code.

## Proposed conventions to review

### Tooling and validation

**Convention 1: `gofmt` is the mandatory formatter; do not introduce a new lint/format stack in v1.**
- **evidence from the repo:** Current Go files across `cassini-go-recorder/`, `cassini-operator/`, and `harness/go-talk-rotator/` already follow normal `gofmt` layout. There is no `.golangci.yml`, and there are no `gofumpt` or `golangci-lint` references in repo code/docs outside this shaping work.
- **elaboration** The current convention is implicit rather than explicit: the repo already leans on the standard Go formatter, but it has not formalized that as a rule yet. The first standard can codify what the repo is already doing without creating churn.
- **repo outliers** No meaningful formatting outliers found in current Go code. The real outlier is the lack of an explicit written rule.
- **suggested** Require `gofmt -w` on every touched Go file. In v1, do not require `gofumpt`, `golangci-lint`, or a new repo-wide lint configuration.
- **why** This matches the current repo, keeps the barrier low for agentic edits, and avoids introducing a second style migration while we are still defining the system.
- **alternatives** Adopt `gofumpt` immediately; introduce `golangci-lint` now; leave formatting informal and trust the agent.
- **response**

**Convention 2: Validation scope is repo-root-relative and as narrow as reasonably possible.**
- **evidence from the repo:** Existing docs and plans already use multiple validation scopes: `cassini-operator/README.md` uses `go test ./...`, while planning docs such as `planning/initiatives/mvp/D-281-publish-dest-conflict/implementation.md` and `planning/initiatives/mvp/slices/V5-failure-inspection-and-rerun-flow/testing.md` use narrower commands like `go test ./internal/operator` or `go test ./internal/cassini/...`. The repo also spans 3 Go modules, so there is no single monorepo-wide `go test` command.
- **elaboration** The repo's current practice is already scope-sensitive: broader commands for broader change, narrower commands for targeted changes. The standard should formalize that rather than forcing every edit through maximum-scope validation.
- **repo outliers** The outlier is not code style but **policy ambiguity**: some docs use module-wide `./...`, others use package-scoped tests, with no explicit rule for choosing between them.
- **suggested** After Go changes, run `gofmt` on touched files and then run the smallest sensible repo-root-relative `go test` scope that exercises the change. Broaden to module-wide or multi-module validation when shared APIs, cross-package flows, or external behavior changed.
- **why** This keeps feedback fast, respects the multi-module repo shape, and still keeps the agent accountable for real validation.
- **alternatives** Always run `go test ./...` in the touched module; always run all Go modules; only format and skip tests unless explicitly asked.
- **response**

### Testing

**Convention 3: Use the standard `testing` package; do not introduce an assertion library in v1.**
- **evidence from the repo:** Current Go tests across the repo use the standard library `testing` package. A repo-wide search found no `testify`, `assert`, or `require` usage in Go code.
- **elaboration** The existing style is plain Go tests with `t.Fatalf`, `t.Run`, and small helpers. That is already the dominant testing idiom here, so the first standard can preserve it.
- **repo outliers** No current outliers found. This convention is already very consistent.
- **suggested** Standardize on the built-in `testing` package for v1. Do not add `testify`-style dependencies as part of the initial Go standard.
- **why** This matches the repo, avoids extra dependencies, and keeps tests lightweight and easy for agents to edit consistently.
- **alternatives** Introduce `testify` now; allow mixed local preference; reserve assertion helpers for only some modules.
- **response**

**Convention 4: If a test helper takes `*testing.T`, it should call `t.Helper()`.**
- **evidence from the repo:** Helper functions in files such as `cassini-operator/internal/operator/lifecycle_test.go`, `cassini-operator/internal/operator/run_test.go`, `cassini-go-recorder/internal/cassini/dev_play_test.go`, and `cassini-go-recorder/pkg/core/store/index_test.go` already use `t.Helper()`. A scan of lowercase helper functions taking `*testing.T` found no current misses.
- **elaboration** This is already a strong convention in the repo's more helper-heavy tests. The standard can formalize it because it improves failure locations without changing test structure.
- **repo outliers** No current outliers found.
- **suggested** Require `t.Helper()` in test helper functions that accept `*testing.T` and can fail or meaningfully participate in assertions/setup.
- **why** It keeps failure reports pointed at the calling test rather than the helper internals, which is especially valuable when agents generate or refactor helpers.
- **alternatives** Make `t.Helper()` optional; only use it in larger helper functions; ignore helper marking entirely.
- **response**

**Convention 5: Use table-driven tests when there is a meaningful case matrix; keep one-off scenario tests flat when that reads better.**
- **evidence from the repo:** Table-driven tests already appear where behavior varies across multiple cases, for example in `cassini-go-recorder/internal/nextcloud/call_url_test.go`, `cassini-go-recorder/internal/talk/recorder_autostop_test.go`, `cassini-go-recorder/internal/portable/manifest_v2_test.go`, and `cassini-go-recorder/internal/cassini/dev_play_test.go`. At the same time, scenario-style tests such as `harness/go-talk-rotator/main_test.go` and many `cassini-operator/internal/operator/*_test.go` tests remain as separate named tests.
- **elaboration** The repo does not force one structure everywhere. It already prefers tables when inputs/outputs vary systematically, but it keeps explicit test functions when the scenario itself is the main story.
- **repo outliers** `harness/go-talk-rotator/main_test.go` and several operator tests could be converted into table-driven form, but the current repo clearly tolerates flat scenario tests when they are easier to read.
- **suggested** Use table-driven tests plus `t.Run(...)` when a behavior has a real case matrix. Keep tests flat when the scenario is singular and forcing a table would make the test less legible.
- **why** This matches the repo's actual practice and avoids cargo-cult table-driven structure where it adds noise instead of clarity.
- **alternatives** Require table-driven form for almost all tests; avoid tables entirely in favor of one test per case.
- **response**

**Convention 6: `t.Parallel()` is opt-in and should only be used for clearly isolated tests.**
- **evidence from the repo:** There are only 4 current `t.Parallel()` calls, in `cassini-go-recorder/pkg/core/store/index_test.go` and `cassini-go-recorder/internal/signaling/client_test.go`. Many other tests use temp dirs, env vars, subprocesses, HTTP servers, ports, or filesystem artifacts and do not opt into parallel execution.
- **elaboration** The repo is conservative here. It treats parallelism as a performance optimization for obviously isolated tests, not as the default style.
- **repo outliers** Many pure unit tests likely could use `t.Parallel()` but currently do not. The outlier pattern is under-use rather than unsafe over-use.
- **suggested** Only add `t.Parallel()` when a test is clearly isolated from global state, env vars, shared ports, sibling file paths, subprocess state, and timing-sensitive interactions.
- **why** This reduces flakiness while still leaving room for selective speedups.
- **alternatives** Add `t.Parallel()` to all top-level tests by default; ban it entirely.
- **response**

### API, error, and abstraction rules

**Convention 7: Put `context.Context` first on request-scoped, cancelable, network, subprocess, or long-running operations.**
- **evidence from the repo:** A repo scan found 118 functions already taking `context.Context`, including operator runtime functions, Nextcloud/signaling client methods, recorder loops, CLI entrypoints, and harness bot flows. Examples include `cassini-operator/internal/operator/build_runtime.go`, `cassini-go-recorder/internal/nextcloud/ocs_client.go`, `cassini-go-recorder/internal/talk/recorder.go`, and `harness/go-talk-rotator/main.go`.
- **elaboration** This is already a strong boundary convention in the repo. The rule should formalize the existing tendency at orchestration and I/O boundaries without forcing `context.Context` into every pure helper.
- **repo outliers** Clear outliers include long-running or network-heavy functions without context such as `cassini-go-recorder/pkg/core/remux/BuildFromSession`, `cassini-go-recorder/pkg/core/remux/UpgradeLegacyMeetingMKV`, `cassini-go-recorder/internal/transcribe/BuildMeetingSummary`, `cassini-go-recorder/internal/transcribe/ReadableCleanup`, and `cassini-go-recorder/internal/transcribe/chatCompletion`.
- **suggested** If a function participates in a request/task lifecycle, performs network I/O, starts subprocess work, blocks for a meaningful time, or produces heavyweight artifacts, accept `context.Context` as the first parameter. Do not add context to small pure helpers just for ceremony.
- **why** This improves cancellation, timeout handling, and composition across CLI, service, and background-job boundaries.
- **alternatives** Only require context at top-level commands; require context everywhere, including pure helpers; leave the pattern informal.
- **response**

**Convention 8: Add context to errors with `%w`, inspect errors with `errors.Is` / `errors.As`, and keep error strings lowercase unless a leading acronym/env var is semantically required.**
- **evidence from the repo:** A repo scan found roughly 520 `%w` wraps and 57 `errors.Is` / `errors.As` usages. Most current error messages are lowercase and contextual, such as `open source file`, `create site parent dir`, or `decode stored request JSON`.
- **elaboration** The repo already values layered, contextual errors. The main thing missing is a written rule that also clarifies capitalization and the small number of `%v` / capitalized message exceptions.
- **repo outliers** Notable outliers include `cassini-go-recorder/internal/cassini/serve.go` using `%v` in `server did not become ready on %s: %v`, and capitalized messages such as `LLM not configured` / `LLM returned an empty summary` in `cassini-go-recorder/internal/transcribe/llm.go` and `summary.go`. Messages that intentionally start with tokens like `CASSINI_REPO_ROOT=...` are more defensible exceptions.
- **suggested** When adding context to an error, wrap with `%w`. Use `errors.Is` / `errors.As` instead of string matching for branching logic. Keep error text lowercase unless the message must begin with a protocol token, acronym, or env var name.
- **why** This preserves rich error chains, keeps error handling idiomatic, and makes generated/refactored code more predictable.
- **alternatives** Allow `%v`-style wrapping; rely on string matching; ignore capitalization consistency.
- **response**

**Convention 9: Prefer concrete types and small consumer-side interfaces; do not invent interfaces before the seam exists.**
- **evidence from the repo:** There are only 5 interface definitions across repo Go code. The strongest examples are narrow seams such as `rowScanner` in `cassini-operator/internal/operator/run.go`, `LifecycleStore` in `cassini-operator/internal/operator/lifecycle.go`, and `rtpWriter` in `cassini-go-recorder/pkg/core/depacket/write.go`.
- **elaboration** The repo mostly avoids blanket abstraction layers. That is a useful convention to preserve because it keeps refactors straightforward and makes ownership clearer.
- **repo outliers** `cassini-go-recorder/pkg/core/timeline/Estimator` is defined, but current call sites directly use `NewSegmentEstimator()` and the concrete type. `cassini-go-recorder/pkg/core/mux/Muxer` appears as a producer-side abstraction with no visible repo consumers. `NewFileLifecycleStore` in `cassini-operator/internal/operator/lifecycle.go` returns the interface type rather than the concrete implementation.
- **suggested** Default to concrete types. Introduce interfaces only when a consumer actually needs interchangeability or a focused test seam. Keep interfaces small, and prefer constructors returning concrete types unless returning an interface materially improves the call site.
- **why** This avoids premature abstraction and makes agent-driven refactors less brittle.
- **alternatives** Define interfaces up-front for major components; return interface types from constructors by default; ban custom interfaces except in tests.
- **response**

**Convention 10: Avoid `panic` in normal production control flow.**
- **evidence from the repo:** A repo scan found only 2 `panic(...)` usages in non-test Go code, both in `cassini-operator/internal/operator/talk_backend.go`. Most of the codebase returns errors instead of panicking.
- **elaboration** The repo already treats panic as exceptional. The first standard can formalize that posture while still leaving narrow escape hatches for truly impossible conditions or must-not-fail helpers.
- **repo outliers** `talkRandom()` panics if `rand.Read` fails, and `marshalJSONBody()` panics if `json.Marshal` fails.
- **suggested** Return errors in normal runtime, I/O, protocol, and orchestration paths. Allow `panic` only for clearly documented impossible invariants, process-fatal bootstrap cases, or tiny helper paths where propagating failure would meaningfully worsen the API.
- **why** This keeps services more recoverable and makes behavior easier to reason about during refactors.
- **alternatives** Ban `panic` entirely; allow panic freely inside internal packages.
- **response**

## Decision

The user accepted the full proposed initial convention set and asked to use the repo's current conventions as the first written standard.

That means the initial Go standard for this repo is:

1. `gofmt` is mandatory.
2. Validation uses the narrowest sensible repo-root-relative test scope.
3. Go tests use the standard `testing` package.
4. Test helpers with `*testing.T` use `t.Helper()`.
5. Table-driven tests are used when there is a real case matrix.
6. `t.Parallel()` is opt-in and only for clearly isolated tests.
7. `context.Context` goes first on cancelable, I/O-heavy, request-scoped, or long-running operations.
8. Errors are wrapped with `%w`, inspected with `errors.Is` / `errors.As`, and written in lowercase unless a leading token must stay uppercase.
9. Concrete types are preferred; interfaces stay small and consumer-driven.
10. `panic` is avoided in normal production control flow.

## Result

These conventions should now be treated as the v1 Go standard and copied into the canonical skill:

- `.agents/skills/go-coding-standard/SKILL.md`

The remaining follow-up is iterative refinement over time, not further MVP shaping.
