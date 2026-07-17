---
shaping: true
---

# [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) — Shaping

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | Reintroduce dev-stack validation warnings only for configs that already pass hard validation | Core goal |
| R1 | Preserve all existing hard failures: invalid configs still exit 2 and do not print a plan | Must-have |
| R2 | Avoid default-noise: the default plan should continue to print `validation: ok` | Must-have |
| R3 | Warnings must be provenance-aware: distinguish explicit flag/env intent from resolver defaults | Must-have |
| R4 | `dev stack plan` must restore the meaningful `validation: ok` versus `validation: warnings` output | Core goal |
| R5 | `dev stack up` and destructive `down` invocations must surface warnings without changing exit codes | Must-have |
| R6 | Selected warning scenarios must be deterministic, ordered, and covered by table-driven tests | Must-have |
| R7 | Warning collection must stay in the Go resolver path, not duplicated in shell scripts | Must-have |
| R8 | Runtime/operator health warning scope remains out of [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) | Must-have |

## CURRENT: no warning producer

`devStackPlan` currently has no warning field, and `printDevStackPlan` always ends with `validation: ok`.

```text
args/env
  |
  v
parseDevStackFlags      (remembers opts.set, but warning code no longer exists)
  |
  v
resolveDevStackPlan
  |
  v
validateDevStackPlan    (hard failures only)
  |
  +--> invalid: stderr + exit 2
  |
  `--> valid plan
          |
          +--> plan: printDevStackPlan -> validation: ok  (always)
          |
          +--> up/down/status: run harness script with plan.env()
```

The removed branch from `191a12d` was correct as a printer but unreachable because nothing populated `ValidationWarnings`.

## A: Restore old field and manually append warnings from print path

| Part | Mechanism | Flag |
|------|-----------|:----:|
| A1 | Re-add `ValidationWarnings []string` to `devStackPlan` | |
| A2 | Add ad hoc warning checks inside `printDevStackPlan` based only on final `plan` values | |
| A3 | Do not surface warnings for `up`/`down` | |

## B: Resolver-owned warning collector (selected)

| Part | Mechanism | Flag |
|------|-----------|:----:|
| B1 | Re-add `ValidationWarnings []string` to `devStackPlan` | |
| B2 | Add `collectDevStackWarnings(plan, opts, lookup) []string`, called after `validateDevStackPlan(plan)` succeeds | |
| B3 | Add helper predicates for `flagOrEnvSet`, `envSet`, `devStackHasMediaStack`, `devStackPatchApplies`, and RFC1918 media hosts | |
| B4 | Restore `printDevStackPlan` warning branch exactly at the existing `validation:` seam | |
| B5 | Add `printDevStackWarnings(w, command, warnings)` for `up`/`down` stderr preflight blocks; no output when warnings are empty | |
| B6 | Add table-driven tests for default `ok`, each selected warning, multiple warnings order, and `up` warning exit-code preservation | |

## C: Shell-side warning collection

| Part | Mechanism | Flag |
|------|-----------|:----:|
| C1 | Leave Go plan output unchanged | |
| C2 | Add warnings to `harness/bin/up.sh` / `down.sh` using shell env values after `harness_stack_init` | |
| C3 | Test via shell or command-output integration tests | ⚠️ |

## Fit Check

| Req | Requirement | Status | A | B | C |
|-----|-------------|--------|---|---|---|
| R0 | Reintroduce dev-stack validation warnings only for configs that already pass hard validation | Core goal | ✅ | ✅ | ✅ |
| R1 | Preserve all existing hard failures: invalid configs still exit 2 and do not print a plan | Must-have | ✅ | ✅ | ❌ |
| R2 | Avoid default-noise: the default plan should continue to print `validation: ok` | Must-have | ❌ | ✅ | ✅ |
| R3 | Warnings must be provenance-aware: distinguish explicit flag/env intent from resolver defaults | Must-have | ❌ | ✅ | ❌ |
| R4 | `dev stack plan` must restore the meaningful `validation: ok` versus `validation: warnings` output | Core goal | ✅ | ✅ | ❌ |
| R5 | `dev stack up` and destructive `down` invocations must surface warnings without changing exit codes | Must-have | ❌ | ✅ | ✅ |
| R6 | Selected warning scenarios must be deterministic, ordered, and covered by table-driven tests | Must-have | ❌ | ✅ | ❌ |
| R7 | Warning collection must stay in the Go resolver path, not duplicated in shell scripts | Must-have | ✅ | ✅ | ❌ |
| R8 | Runtime/operator health warning scope remains out of [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) | Must-have | ✅ | ✅ | ❌ |

Notes:

- A fails R2/R3 because final plan values alone cannot tell default `exapp_image_mode: reuse-local` from a user explicitly selecting it under `cassini: none`.
- C fails R4/R7 because `plan` would stay blind and shell logic would duplicate resolver semantics. It also risks weakening R1 by warning after shell defaults rather than after Go hard validation.
- B is the only shape that uses all the facts the resolver already has: final values, flag provenance, and env lookup.

## Decision

Select **B: Resolver-owned warning collector**.

The collector runs only after validation succeeds:

```text
resolve final values
    |
    v
validate hard invariants
    |\
    | \-- error --> exit 2 path, no warnings
    v
collect warnings
    |
    v
return plan{ValidationWarnings: []string{...}}
```

### Warning policy

A warning is in [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) if it meets at least one of these criteria:

1. **Ignored or overridden user input:** a flag/env value was supplied and the resolved stack will not use it as the user likely expects.
2. **Valid but surprising combination:** the plan is internally valid but the named mode implies a different path than the label suggests.
3. **Destructive dry-run intent:** the command is valid and mutating/destructive; `plan`/preflight should disclose the blast radius.

A warning is out of [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) if it requires runtime probing, belongs to operator health ([D-502](https://linear.app/code-myriad/issue/D-502/cassini-detect-misconfiguration-and-warnguide-the-operator-recording)), or is likely to warn on intentional documented workflows.

### Selected warning set

Accepted on 2026-07-17: W1-W12 are in; W13-W17 are out. The user's “This makes sense. Please go ahead and implement this” confirms the suggested checklist (`affirmative (implied)`).

| ID | Scenario | Suggested state |
|----|----------|-----------------|
| W1 | `SPREED_PROFILE` ignored by `core|appapi` service mode | IN |
| W2 | `SPREED_PROFILE` ignored by `full|full-remote` service mode | IN |
| W3 | explicit `exapp-image-mode pull|reuse-local` ignored because `--cassini none` | IN |
| W4 | explicit `patch force|none` ignored because `--cassini none` | IN |
| W5 | remote media/signaling values ignored because service mode has no Talk media stack | IN |
| W6 | installed ExApp selected but recording backend is `legacy` | IN |
| W7 | installed ExApp selected but recording backend is `direct-operator` | IN |
| W8 | `remote-https` media host is RFC1918/private IPv4 | IN |
| W9 | `up --reset` / `CASSINI_HARNESS_EXISTING=reset` destructive intent | IN |
| W10 | `down --volumes` destructive intent | IN |
| W11 | `down --full` destructive intent | IN |
| W12 | explicit non-`none` recording backend ignored because no media stack | IN |
| W13 | default `legacy` backend ignored because no media stack | OUT |
| W14 | `patch none` with installed ExApp may CSP-block UI | OUT |
| W15 | explicit local mode masks ambient remote env | OUT |
| W16 | private/RFC1918 public URL/host | OUT |
| W17 | installed ExApp pull mode uses published image | OUT |

The mutating-command output proposal is also accepted (`affirmative (implied)`): `up`/`down` print a small warning block to stderr before invoking the harness script and preserve the script exit code.

## Implementation shape

### Data model

```go
type devStackPlan struct {
    ...
    ValidationWarnings []string
}
```

### Collector placement

Inside `resolveDevStackPlan`, after hard validation:

```go
if err := validateDevStackPlan(plan); err != nil {
    return plan, rest, err
}
plan.ValidationWarnings = collectDevStackWarnings(plan, opts, lookup)
return plan, rest, nil
```

### Helper predicates

| Helper | Purpose |
|--------|---------|
| `envValue(name string) (string, bool)` | Reads non-empty env values through the supplied lookup |
| `flagOrEnvSet(flagName, envName string) bool` | Detects user-provided intent for warning triggers |
| `devStackHasMediaStack(plan devStackPlan) bool` | Mirrors shell `harness_media_selected`: service full/full-remote or resolved profile full |
| `devStackPatchApplies(plan devStackPlan) bool` | True only for `cassini installed-exapp` today |
| `isRFC1918IPv4Host(host string) bool` | Parses literal IPv4 only; no DNS; warns for 10/8, 172.16/12, 192.168/16 |

### Output shape

Plan with no warnings:

```text
validation: ok
```

Plan with warnings:

```text
validation:
  warnings:
    - SPREED_PROFILE=full is ignored because service mode appapi forces SPREED_PROFILE=default.
```

`up`/`down` preflight warnings to stderr, before script execution:

```text
dev stack up: validation warnings:
  - up --reset will remove and recreate Docker Compose resources for project 'spreedtest'; installed ExApp state is also removed in installed-exapp mode.
```

`up`/`down` still return the script's exit code. A warning-only plan still succeeds.

### Testing shape

Add tests in `dev_stack_test.go`:

| Test | Purpose |
|------|---------|
| `TestResolveDevStackPlanDefaultHasNoWarnings` | protects R2 |
| `TestResolveDevStackPlanValidationWarnings` | table-driven selected W1-W12 triggers |
| `TestPrintDevStackPlanValidationWarnings` | exact `validation:` output branch |
| `TestRunDevStackUpPrintsWarningsWithoutChangingExitCode` | warnings to stderr, mocked script returns 0 |
| `TestRunDevStackDownPrintsDestructiveWarnings` | down warnings to stderr, mocked script still called |
| Existing hard-failure tests | unchanged; prove errors stay errors |

## Decisions confirmed for execution

- **Q1:** W1-W12 in; W13-W17 out (`affirmative (implied)`).
- **Q2:** `up`/`down` stderr preflight warning blocks accepted (`affirmative (implied)`).
