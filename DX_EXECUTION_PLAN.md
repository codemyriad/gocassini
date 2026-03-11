# Cassini DX Execution Plan

Date: 2026-03-10

This plan turns the DX rearchitecture into a delivery sequence that can start
inside the current repository without pretending the current boundaries are
already correct.

## Guiding rule

The first slice must introduce the new product boundary.
It must not add more top-level wrappers or more public subsystem entry points.

## Milestones

### Milestone 1: Introduce `cassini` and the `run` bundle

Deliverables:

- new `cassini` CLI inside the existing Go module
- `cassini record`
- `cassini record --help` and `cassini --help` exiting `0`
- `run` bundle manifest contract
- recording output redirected into a bundle root instead of loose sibling files

Non-goals:

- replacing the transcriber UX yet
- replacing the publisher UX yet
- moving the whole repository layout yet
- eliminating all old wrappers in one step

Implementation notes:

- place `cmd/cassini` in `cassini-go-recorder/` first because that is the only
  current Go module
- wrap existing recorder internals instead of forking capture logic
- write bundle metadata to `cassini.json`
- normalize the session artifact into a stable in-bundle location

Acceptance criteria:

- `cassini record --simulate --out ./runs/demo.run` produces one directory
- `cassini record --out ./runs/live.run --call ...` writes all recorder output
  under that directory
- no user-facing command in this slice depends on `cd`
- no primary path defaults to `/tmp`

### Milestone 2: Add `cassini inspect` and `cassini doctor`

Deliverables:

- `cassini inspect <path>`
- `cassini doctor`
- run-bundle aware path detection
- early disk/cache/writability validation

Acceptance criteria:

- users can inspect a `run` bundle directly
- common failure modes are caught before long-running work starts

### Milestone 3: Add `cassini build`

Deliverables:

- `cassini build <run-bundle> --out <meeting-bundle>`
- `meeting` bundle manifest contract
- product-level progress output for transcription/build

Acceptance criteria:

- users no longer need to run Python entry points directly
- meeting output is always one directory with one manifest

### Milestone 4: Add `cassini publish` and `cassini serve`

Deliverables:

- `cassini publish <meeting-dir> --out <site-dir>`
- `cassini serve <site-dir>`
- embedded viewer assets in release packaging

Acceptance criteria:

- publishing a site does not require local Node for end users

### Milestone 5: Move the harness behind `cassini dev`

Deliverables:

- `cassini dev stack ...`
- `cassini dev fixture ...`
- `cassini dev smoke`
- `harness/` established as the canonical local-stack directory

Acceptance criteria:

- local stack and fixture generation remain available
- they are no longer part of the product onboarding path

### Milestone 6: Remove the old public surface

Deliverables:

- deprecated wrapper shims
- wrapper docs removed
- root README rewritten around `cassini`

Acceptance criteria:

- new users encounter one product, not a suite of sibling tools

## First slice detail

Milestone 1 should be implemented in this order:

1. Add a small internal package for bundle manifests and bundle finalization.
2. Add `cmd/cassini` with a real subcommand parser.
3. Implement `cassini record`.
4. Route recorder output into a bundle root.
5. Normalize session artifacts into a stable in-bundle location.
6. Add focused tests for help behavior and bundle finalization.

## Why this sequence

This sequence fixes the most damaging DX leak first:

- the lack of a single product entry point
- the lack of a single recording output boundary

Once those two are in place, the remaining work becomes additive within the new
boundary instead of additive on top of the old one.
