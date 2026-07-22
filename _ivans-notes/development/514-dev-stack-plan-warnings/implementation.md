# Implementation — [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan)

## Summary

Implemented resolver-owned, non-fatal validation warnings for `cassini dev stack`.

The final flow is:

```text
CLI flags + environment
          |
          v
resolve typed devStackPlan
          |
          v
validate hard invariants
      |             |
   invalid         valid
      |             |
 stderr + exit 2    v
 no plan       collect warnings
                    |
             plan.ValidationWarnings
                    |
          +---------+----------+
          |                    |
          v                    v
  plan stdout block     up/down stderr preflight
                              |
                              v
                    run harness script unchanged
                    and preserve its exit code
```

Hard failures are unchanged. Warning collection occurs only after `validateDevStackPlan` succeeds.

## Commits and slices

| Slice | Commit | Result |
|-------|--------|--------|
| Plan | `5f85a60` | Shaped the warning policy, scenario pyramid, breadboard, and slices |
| S1 | `228fd33` | Restored `ValidationWarnings`, the collector seam, and `validation: ok` / `warnings:` plan output |
| S2 | `a6f8b05` | Added `up`/`down` stderr preflight warning rendering without changing script execution or exit codes |
| S3 | `2cb1333` | Implemented accepted W1-W12 warnings, provenance/RFC1918 helpers, documentation, and comprehensive tests |

## Code changes

### Resolver and warning collection

`cassini-go-recorder/internal/cassini/dev_stack.go` now:

- stores `ValidationWarnings []string` on `devStackPlan`;
- calls `collectDevStackWarnings(plan, opts, lookup)` after hard validation;
- uses `opts.set` plus non-empty env lookup to distinguish explicit intent from defaults;
- mirrors the harness media-stack predicate through `devStackHasMediaStack`;
- detects literal RFC1918 IPv4 media hosts without DNS via `net/netip`;
- appends warnings in a fixed policy order;
- restores the plan warning output branch removed in commit `191a12d`.

### Mutating commands

`cassini-go-recorder/internal/cassini/dev.go` now prints a compact warning block to stderr before `up` and `down` scripts:

```text
dev stack up: validation warnings:
  - ...
```

The script is still invoked with the same resolved environment and its return code remains authoritative.

### Documented warning surface

`harness/README.md` now documents the hard-failure versus warning boundary and the warning categories.

## Implemented warnings

| ID | Warning |
|----|---------|
| W1/W2 | `SPREED_PROFILE` is overridden by explicit service topology |
| W3 | Explicit pull/reuse-local ExApp image mode is ignored with `cassini none` |
| W4 | Explicit force/none patch mode is ignored with `cassini none` |
| W5 | Media/signaling remote values are unused by a topology without Talk media services |
| W6/W7 | Installed ExApp is bypassed by legacy/direct-operator recording backend |
| W8 | `remote-https` media host is a literal RFC1918 IPv4 address |
| W9 | Existing-resource reset removes/recreates the stack and volumes, plus installed ExApp state when applicable |
| W10 | `down --volumes` removes current-project and installed ExApp state volumes |
| W11 | `down --full` removes all known harness-owned Compose/ExApp resources and volumes |
| W12 | Explicit non-`none` recording backend will not be configured without a media stack |

Default-derived no-op values do not warn. The explicitly deferred W13-W17 scenarios are pinned by negative tests so they do not begin warning accidentally.

## Test coverage

`cassini-go-recorder/internal/cassini/dev_stack_test.go` covers:

- the default plan having no warnings;
- each accepted warning, including flag and env provenance variants;
- deterministic multi-warning order;
- all three RFC1918 IPv4 ranges and relevant non-private boundaries;
- deferred scenarios remaining warning-free;
- hard failures collecting no warnings and printing no plan;
- plan warning rendering;
- `up` warnings preserving both zero and non-zero script exit codes;
- destructive `down` warning rendering and script argument forwarding.

## Validation

Validated with:

```text
go test ./internal/cassini                         PASS
go test -race ./...                               PASS
manual default plan -> validation: ok             PASS
manual profile override -> validation warnings    PASS
manual installed-ExApp bypass -> warning          PASS
manual reset dry-run via env -> warning            PASS
```

On Nix, direct Go test binaries need the active compiler's `libstdc++.so.6` directory in `LD_LIBRARY_PATH`; `./bin/cassini` already handles that automatically.
