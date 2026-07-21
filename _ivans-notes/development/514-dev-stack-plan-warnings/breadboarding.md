---
shaping: true
---

# [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) — Breadboarding

## Purpose

Map Shape B (resolver-owned warning collector) into concrete affordances and wiring. The goal is a plan/up/down warning path that is visible to users but cannot change hard-validation semantics.

## Places

| Place | Responsibility |
|-------|----------------|
| **CLI surface** | Accept user args/env and choose subcommand behavior |
| **Plan resolver** | Convert flags/env/defaults into one typed `devStackPlan` |
| **Validation boundary** | Reject invalid plans before warnings |
| **Warning collector** | Attach non-fatal warning text to valid plans |
| **Output renderers** | Print plan validation state or preflight warning blocks |
| **Harness scripts** | Mutate Docker/Nextcloud/AppAPI using plan env; unchanged warning logic |

## UI affordances

| ID | Place | Affordance | User-visible effect | Wires out |
|----|-------|------------|---------------------|-----------|
| U1 | CLI surface | `cassini dev stack plan [options]` | Prints resolved plan and `validation: ok` or `validation: warnings` | N1 |
| U2 | CLI surface | `cassini dev stack up [options]` | Prints warning block to stderr if needed, then starts stack | N1, N7, N8 |
| U3 | CLI surface | `cassini dev stack down [options]` | Prints warning block to stderr for destructive modes, then tears down | N1, N7, N9 |
| U4 | CLI surface | invalid command/flags | Prints error and exits 2 before warning collection | N1, N3 |

## Non-UI affordances

| ID | Place | Affordance | Responsibility | Wires out |
|----|-------|------------|----------------|-----------|
| N1 | Plan resolver | `parseDevStackFlags(command, args)` | Parse flags and record explicit flags in `opts.set` | S1, N2 |
| N2 | Plan resolver | `resolveDevStackPlan(command, args, lookup)` | Resolve final plan values from flag > env > default | N3, N4 |
| N3 | Validation boundary | `validateDevStackPlan(plan)` | Hard-fail impossible/contradictory configs | returns error to U4 or plan to N4 |
| N4 | Warning collector | `collectDevStackWarnings(plan, opts, lookup)` | Build deterministic warning list for valid plan | S2, N5, N6 |
| N5 | Warning collector | `devStackHasMediaStack(plan)` | Mirror shell media predicate | N4 |
| N6 | Warning collector | `isRFC1918IPv4Host(host)` | Identify private literal media IPs for remote warning | N4 |
| N7 | Output renderers | `printDevStackCommandWarnings(stderr, command, warnings)` | Preflight warning block for `up`/`down`; no exit-code change | U2, U3 |
| N8 | Harness scripts | `runDevScriptWithEnv(... up.sh, plan.env())` | Existing stack startup path | S3 |
| N9 | Harness scripts | `runDevScriptWithEnv(... down.sh, plan.env())` | Existing teardown path | S3 |
| N10 | Output renderers | `printDevStackPlan(stdout, plan)` | Existing plan body plus restored validation branch | U1 |

## Data stores

| ID | Store | Contents | Readers |
|----|-------|----------|---------|
| S1 | `devStackFlagOptions` | Parsed flag values, lifecycle booleans, `opts.set` explicit flag map | N2, N4 |
| S2 | `devStackPlan.ValidationWarnings` | Ordered `[]string` warning messages | N7, N10, tests |
| S3 | `plan.env()` | Resolved env passed to harness scripts | N8, N9, existing shell scripts |

## Wiring diagram

```text
                 CLI args                         environment
                    |                                  |
                    v                                  v
          +----------------------+          +----------------------+
          | N1 parse flags       |          | lookup/env function  |
          | - opts.set           |          +----------+-----------+
          +----------+-----------+                     |
                     |                                 |
                     v                                 |
          +----------------------+                     |
          | N2 resolve plan      |<--------------------+
          | flag > env > default |
          +----------+-----------+
                     |
                     v
          +----------------------+
          | N3 hard validation   |
          +----+------------+----+
               |            |
        invalid|            |valid
               v            v
       stderr + exit 2   +----------------------+
                         | N4 collect warnings  |
                         | reads opts + plan    |
                         +----------+-----------+
                                    |
                                    v
                            S2 plan warnings
                                    |
                   +----------------+----------------+
                   |                                 |
                   v                                 v
        +----------------------+          +----------------------+
        | N10 print plan       |          | N7 print command     |
        | validation branch    |          | warnings to stderr   |
        +----------+-----------+          +----------+-----------+
                   |                                 |
                   v                                 v
             stdout plan                     run up/down scripts
                                             with unchanged env/exit
```

## Warning collector internals

The collector should append in a fixed policy order rather than sorting alphabetically. That keeps related warnings grouped and makes tests readable.

```text
collectDevStackWarnings
    |
    +-- profile override warnings       (W1/W2)
    +-- ExApp image mode ignored        (W3)
    +-- patch mode ignored              (W4)
    +-- no-media-stack remote inputs    (W5)
    +-- recording backend surprises     (W6/W7/W12)
    +-- remote media host private IP    (W8)
    `-- destructive lifecycle warnings  (W9/W10/W11)
```

### Profile override branch

```text
SPREED_PROFILE env set?
    |
    v
resolved plan.SpreedProfile differs from env value?
    |
    v
warn: env value ignored because service mode forces resolved profile
```

This covers both `core|appapi -> default` and `full|full-remote -> full`, but only when env intent exists.

### Explicit no-op mode branches

```text
explicit image mode? AND cassini == none AND image mode != build
    -> warn image mode ignored

explicit patch mode? AND cassini == none AND patch mode in {none, force}
    -> warn no patch will run
```

`build` with `cassini none` never reaches this branch because it is H17 hard failure.

### Media/no-media branches

```text
hasMediaStack(plan) == false
    |
    +-- remote media/signaling inputs present or derived
    |       -> warn ignored by no-media service mode
    |
    `-- explicit recording backend != none
            -> warn recording backend will not be configured
```

## Slice mapping

| Slice | Breadboard changes |
|-------|--------------------|
| S1 | Add S2, N4, N5/N6 helpers, restore N10 warning branch, tests for plan output |
| S2 | Add N7 command-warning renderer and hook U2/U3 before N8/N9; tests for exit-code preservation |
| S3 | Apply selected W1-W12 collector branches and table-driven tests; adjust docs if the checklist changes |

## Design checks

- The hard-validation edge is before N4. Invalid configs cannot produce warnings.
- Warning text is stored on the plan, so `plan`, `up`, and `down` cannot drift.
- Harness scripts remain consumers of `plan.env()`; no duplicated warning policy in shell.
- Warnings are output-only. They do not affect `runDevScriptWithEnv` invocation or returned exit code.
