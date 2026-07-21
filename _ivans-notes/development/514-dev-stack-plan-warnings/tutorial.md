# Tutorial — [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan)

## Prerequisites

For plan-only demonstrations:

- repository checkout;
- Go toolchain from `cassini-go-recorder/go.mod`;
- no Docker daemon or running stack is required.

Run commands from the repository root. The examples unset ambient remote-harness variables so output is deterministic.

## 1. Confirm the default plan remains clean

```bash
env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  ./bin/cassini dev stack plan
```

Expected final line:

```text
validation: ok
```

The default `exapp_image_mode: reuse-local`, `recording.backend: legacy`, and `patch.mode: auto` do not warn because they are defaults, not explicit ignored intent.

## 2. See an overridden profile warning

```bash
env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  SPREED_PROFILE=full \
  ./bin/cassini dev stack plan \
    --services appapi \
    --recording-backend none
```

Expected validation block:

```text
validation:
  warnings:
    - SPREED_PROFILE=full is ignored because service mode appapi forces SPREED_PROFILE=default.
```

The command exits 0 because the final plan is valid.

## 3. See an installed-ExApp recording-path warning

```bash
env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  ./bin/cassini dev stack plan \
    --services full \
    --cassini installed-exapp \
    --recording-backend legacy
```

Expected warning:

```text
Cassini is installed as an ExApp, but Talk recording uses the legacy backend; the installed ExApp will not receive recording callbacks.
```

For installed-ExApp recording, use:

```bash
./bin/cassini dev stack plan \
  --public-mode local-http \
  --services full \
  --cassini installed-exapp \
  --recording-backend installed-exapp \
  --build
```

That combination has no recording-path warning.

## 4. Inspect reset intent without mutating Docker

`--reset` is an `up`-only flag, but the equivalent environment-selected lifecycle mode can be inspected with `plan`:

```bash
env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  CASSINI_HARNESS_EXISTING=reset \
  ./bin/cassini dev stack plan
```

Expected warning:

```text
Existing-resource mode reset will remove and recreate the resolved stack, including Docker Compose volumes.
```

No resources are changed by `plan`.

## 5. Confirm hard failures remain hard

```bash
set +e
env \
  -u CASSINI_HARNESS_PUBLIC_MODE \
  -u CASSINI_HARNESS_PUBLIC_URL \
  -u CASSINI_HARNESS_PUBLIC_HOST \
  -u CASSINI_HARNESS_MEDIA_HOST \
  -u CASSINI_HARNESS_SIGNALING_PUBLIC_URL \
  ./bin/cassini dev stack plan \
    --services appapi \
    --recording-backend direct-operator
code=$?
set -e
printf 'exit=%s\n' "$code"
```

Expected behavior:

```text
dev stack plan: recording backend direct-operator requires service mode full, full-remote, or legacy-default
exit=2
```

No resolved plan or warning block is printed.

## 6. Observe warnings on mutating commands

The warning block is printed to stderr before the harness script runs:

```text
dev stack up: validation warnings:
  - Existing-resource mode reset will remove and recreate ...
```

The following commands are destructive and should only be run when that is your intent:

```bash
./bin/cassini dev stack up --reset
./bin/cassini dev stack down --volumes
./bin/cassini dev stack down --full
```

Warnings do not ask for confirmation and do not change exit codes. The harness script still performs the operation and determines success/failure.

## 7. Run the automated validation

From `cassini-go-recorder/`:

```bash
CXX_BIN="${CXX:-$(go env CXX)}"
LIBSTDCXX="$($CXX_BIN -print-file-name=libstdc++.so.6)"

LD_LIBRARY_PATH="$(dirname "$LIBSTDCXX")${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
  go test -race ./...
```

The explicit `LD_LIBRARY_PATH` is needed on Nix because the recorder test binary links sherpa-onnx and needs the active compiler's `libstdc++.so.6`. On conventional Linux environments, `go test -race ./...` is sufficient.
