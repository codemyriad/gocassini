---
shaping: true
---

# [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) — Frame

## Source

### User request

> I need you to read the ticket [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan), reason about it, investigate it and suggest the implementation.
> Alongside the plan include the thorough suggestions on soft warnings valid of surfacing (as the ticket describes).
> Create a pyramid of detailed scanarios:
> - which are hard failure
> - which are warnings (suggested)
>
> Perform the full planning, and stand by for execution. Ask me open questions, if any, also, if you're suggesting a large surface of warnings, create a checklist which I can mark as in/out.

### Linear [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan)

> PR #111 introduced `cassini dev stack` with a `devStackPlan.ValidationWarnings []string` field and a `printDevStackPlan` branch that printed `validation:` + `warnings:` — but **nothing ever populated the field**, so it always printed `validation: ok`. The dead field + branch were removed during review of #111 (commit `refactor(dev-stack): drop never-populated ValidationWarnings`).
>
> This ticket re-introduces it **properly**: actually collect non-fatal warnings so `dev stack plan` (and `up`) can surface valid-but-probably-unintended configs.

> **Errors** (already done in `validateDevStackPlan`): invalid config → fail early, exit 2, plan never printed.
>
> **Warnings** (this ticket): config is *valid* but likely not what the user meant, or has a silent/surprising consequence. Non-fatal; printed under `validation:` with a `warnings:` list, restoring the meaningful `ok` vs `warnings` distinction.

Ticket candidates are silent overrides, valid-but-likely-unintended combos, and destructive dry-run intent. The full candidate list is captured in `brief.md`.

---

## Pre-work: options landscape

| Option | What it does | Who benefits | Signal strength |
|--------|--------------|--------------|-----------------|
| **A. Restore only the old print branch** | Re-add `ValidationWarnings` and `printDevStackPlan` branch but leave warning population minimal or ad hoc | Nobody long-term; makes the field reachable only if manually filled | Weak — this is exactly what PR #111 had without the collector |
| **B. Resolver-owned warning collector** | Collect warnings in `resolveDevStackPlan` after hard validation, using parsed flag provenance plus env lookup; plan/up/down consume the same `plan.ValidationWarnings` | Users running `plan` before mutation and CI/dev users running `up`/`down` | Strong — matches ticket implementation hint and keeps one source of truth |
| **C. Shell-side warnings in `up.sh`/`down.sh`** | Let harness scripts warn during mutation | Users running `up`/`down` only | Weak — `plan` would still be blind and logic would duplicate the Go resolver |
| **D. Operator/runtime health warnings** | Surface setup health in control panel and recording APIs | Production/sandbox operators | Strong, but owned by [D-502](https://linear.app/code-myriad/issue/D-502/cassini-detect-misconfiguration-and-warnguide-the-operator-recording), not [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) |

**Why B now:** [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) is explicitly about the typed dev-stack plan. The Go resolver is where the final values and the provenance of flags/env/defaults meet; it can distinguish an invalid config (hard fail) from a valid config with ignored user input (warning) without asking the shell scripts to rediscover that state.

The warning path should sit behind validation, not beside it:

```text
CLI args + env
    |
    v
parse flags + remember explicit flags
    |
    v
resolve final plan values
    |
    v
validate hard invariants
    |\
    | \-- invalid --> stderr + exit 2, no plan/warnings
    v
collect warnings for valid-but-surprising plan
    |
    +--> plan: print validation ok/warnings on stdout
    |
    `--> up/down: print warning block, then run script, preserving script exit code
```

## Problem

- `dev stack plan` currently prints `validation: ok` for every valid plan, even when the plan contains values that the harness will ignore or override. Evidence: `printDevStackPlan` ends unconditionally with `validation: ok` in `cassini-go-recorder/internal/cassini/dev_stack.go`.
- Some ignored values are user intent, not harmless defaults: `SPREED_PROFILE` is overridden by explicit `--services`, image/patch modes are skipped when `--cassini none`, and media/signaling remote inputs are unused by `core`/`appapi` service modes.
- Some valid combinations are likely mistakes: installing Cassini as an ExApp while leaving Talk recording on the legacy/direct backend means the installed ExApp is not the recorder.
- Destructive modes (`up --reset`, `down --volumes`, `down --full`) are valid and intentional, but a dry-run plan should make their blast radius visible before mutation.
- A warning system that fires on defaults would be worse than the current dead branch: the default plan (`cassini dev stack plan`) should stay `validation: ok`, not warn that default `reuse-local` image mode is ignored by default `--cassini none`.

## Outcome

- Invalid configurations remain hard failures: exit 2, error on stderr, no printed plan.
- Valid but surprising configurations produce deterministic warning text attached to `devStackPlan.ValidationWarnings`.
- `dev stack plan` restores the meaningful distinction:

```text
validation: ok
```

versus:

```text
validation:
  warnings:
    - ...
```

- `dev stack up` and `dev stack down` surface the same warnings before running their scripts and do not change the command exit code.
- Warning tests cover the no-warning default and every selected warning scenario. Hard-failure tests remain separate and continue proving that invalid configs never degrade to warnings.

## Less about

- Not about making previously invalid configs valid. The hard-failure boundary in `validateDevStackPlan` should stay intact.
- Not about runtime/operator health checks, setup UI, or recording request remediation; that is [D-502](https://linear.app/code-myriad/issue/D-502/cassini-detect-misconfiguration-and-warnguide-the-operator-recording).
- Not about warning on every no-op default. Warnings should require user-provided intent, a derived surprising consequence, or destructive command intent.
- Not about sandbox/prod topology. The sandbox decoupling and production install warning surface remain [D-515](https://linear.app/code-myriad/issue/D-515/decouple-the-demo-sandbox-from-the-cidev-harness)/[D-516](https://linear.app/code-myriad/issue/D-516/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt) territory.

## More about

- A trustworthy dry run. `plan` should tell the user both what will run and which selected inputs will not have the effect they probably expect.
- Single-source semantics. The same resolved plan should drive `plan`, `up`, and `down` warnings so they cannot drift.
- A clear severity pyramid. Hard failures reject impossible/contradictory configs; warnings explain valid configs with ignored inputs, surprising behavior, or destructive effects.
