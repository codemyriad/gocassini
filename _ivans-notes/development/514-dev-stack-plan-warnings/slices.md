---
shaping: true
---

# [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) — Slices

## Overview

Three implementation slices. S1 restores the warning data path and plan output without adding broad warning policy. S2 surfaces the same warnings during mutating commands. S3 fills in the selected warning scenarios and table-driven coverage.

```text
S1  warning field + plan output branch
        |
        v
S2  up/down preflight warning renderer
        |
        v
S3  selected scenario collector + tests
```

## Slice table

| Slice | What lands | Demo / validation |
|-------|------------|-------------------|
| **S1 — Plan warning plumbing** | Re-add `ValidationWarnings []string`; add `collectDevStackWarnings` stub called after `validateDevStackPlan`; restore `printDevStackPlan` `validation: ok` vs `warnings:` branch; add tests for default `ok` and direct warning print with a manually populated plan | `go test ./internal/cassini` from `cassini-go-recorder`; `./bin/cassini dev stack plan` still prints `validation: ok` by default |
| **S2 — Mutating command warning surface** | Add command warning renderer for `up`/`down` stderr; hook it after plan resolution and before `runDevScriptWithEnv`; no output for empty warnings; tests mock `runDevScriptExec` and assert warnings do not alter exit code | Unit test proves script is called and returned code is preserved; warning text goes to stderr, not stdout |
| **S3 — Warning scenario collector** | Implement selected W1-W12 warning branches, helper predicates, RFC1918 detection, deterministic table tests; update docs only if needed | Table-driven tests for every selected warning and no-warning defaults; spot-check CLI examples with clean env |

## S1 detail — Plan warning plumbing

### Code changes

| File | Change |
|------|--------|
| `cassini-go-recorder/internal/cassini/dev_stack.go` | Add `ValidationWarnings []string` to `devStackPlan` |
| `cassini-go-recorder/internal/cassini/dev_stack.go` | Add `collectDevStackWarnings(plan, opts, lookup) []string` initially returning nil |
| `cassini-go-recorder/internal/cassini/dev_stack.go` | Call collector immediately after `validateDevStackPlan(plan)` succeeds |
| `cassini-go-recorder/internal/cassini/dev_stack.go` | Restore `printDevStackPlan` warning branch from pre-`191a12d`, with current formatting |
| `cassini-go-recorder/internal/cassini/dev_stack_test.go` | Add `TestPrintDevStackPlanValidationWarnings`; strengthen existing `TestRunDevStackPlanPrintsResolvedPlan` to assert no warnings by default |

### Acceptance

- Default plan still prints exactly one validation terminal state: `validation: ok`.
- A plan manually populated with warnings prints:

```text
validation:
  warnings:
    - first warning
```

- No collector warnings yet means no behavior change except the code path exists.

## S2 detail — Mutating command warning surface

### Code changes

| File | Change |
|------|--------|
| `cassini-go-recorder/internal/cassini/dev.go` | Add `printDevStackCommandWarnings(stderr, command, plan.ValidationWarnings)` before `up`/`down` scripts |
| `cassini-go-recorder/internal/cassini/dev_stack.go` or `dev.go` | Implement renderer with no output for empty warning list |
| `cassini-go-recorder/internal/cassini/dev_stack_test.go` | Mock `runDevScriptExec` to assert warning block on stderr and unchanged exit code |

### Output contract

Suggested stderr block:

```text
dev stack up: validation warnings:
  - ...
```

For `down`:

```text
dev stack down: validation warnings:
  - ...
```

### Acceptance

- If mocked script returns `0`, `runDevStack` returns `0` even with warnings.
- If mocked script returns non-zero, `runDevStack` returns that non-zero code; warning did not mask failure.
- `plan` output remains stdout-only.

## S3 detail — Warning scenario collector

### Selected warning branches

Accepted for execution: implement W1-W12; W13-W17 remain out.

| ID | Trigger | Test case shape |
|----|---------|-----------------|
| W1 | `SPREED_PROFILE=full` + `--services appapi` | env map with `SPREED_PROFILE: full`, args `--services appapi --recording-backend none` |
| W2 | `SPREED_PROFILE=default` + `--services full` | env map with `SPREED_PROFILE: default`, args `--services full` |
| W3 | explicit image mode with `--cassini none` | args `--exapp-image-mode pull` |
| W4 | explicit patch mode with `--cassini none` | args `--patch force` and/or `--patch none` |
| W5 | remote media/signaling values with no media stack | args `--public-mode remote-https --public-host example.test --media-host 100.64.1.2 --services appapi --recording-backend none` |
| W6 | installed ExApp + legacy backend | args `--services full --cassini installed-exapp --recording-backend legacy` |
| W7 | installed ExApp + direct backend | args `--services full --cassini installed-exapp --recording-backend direct-operator` |
| W8 | remote media host RFC1918 | args `--public-mode remote-https --public-host example.test --media-host 192.168.1.10 --services full-remote` |
| W9 | `up --reset` | command `up`, args `--reset` |
| W10 | `down --volumes` | command `down`, args `--volumes` |
| W11 | `down --full` | command `down`, args `--full` |
| W12 | explicit legacy backend with no media stack | args `--services appapi --recording-backend legacy` |

### Helper implementation notes

- Treat env values as present only when non-empty, matching resolver semantics.
- For profile warnings, compare env `SPREED_PROFILE` to `plan.SpreedProfile`; only warn if they differ.
- For explicit image/patch/recording warnings, trigger on explicit flag or non-empty env. Do not warn on default values.
- Use `net/netip` for RFC1918 literal IP detection; no DNS.
- Preserve warning order from the collector policy, not from maps.

### Acceptance

- `TestResolveDevStackPlanValidationWarnings` checks each warning substring.
- `TestResolveDevStackPlanDefaultHasNoWarnings` protects the default plan.
- A multi-warning case pins order (for example: profile override before recording/backend before destructive).
- Existing hard-failure tests still pass unchanged.

## Validation commands

Run from repo root unless noted:

```bash
cd cassini-go-recorder
go test ./internal/cassini
```

Spot checks from repo root with ambient remote env masked for deterministic output:

```bash
env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  ./bin/cassini dev stack plan

env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  SPREED_PROFILE=full \
  ./bin/cassini dev stack plan --services appapi --recording-backend none
```

## Notes for execution

- Warning checklist accepted on 2026-07-17: W1-W12 in, W13-W17 out.
- Do not change shell harness behavior in this task; shell scripts remain consumers of resolved env.
- Do not add warnings that require Docker/network inspection.
- If optional warnings are marked out, remove their tests and collector branches from S3 before implementation.
