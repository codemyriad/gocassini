---
shaping: true
---

# Spike: operator runtime dependencies inside the deployment image

## Context

D-246 needs `cassini-operator` to run as a real containerized service.

That service is unusual because it does not own the record/build/publish logic itself. It shells out to the Cassini CLI, and the publish path shells out again to the exporter runner.

Current baseline from the repo:

- `cassini-operator` validates and runs `CASSINI_BIN`
- `cassini build` / `cassini doctor --target build` require `ffmpeg`, `ffprobe`, writable cache/model paths, and temp space
- `cassini publish` defaults to `cassini-publisher/bin/export-static-meetings.sh`
- that exporter runner currently expects `node`, `npm`, `cassini-viewer/package.json`, and viewer export scripts/dist in a repo-like layout

So the operator image almost certainly needs more than one Go binary.

## Goal

Pin the exact files, executables, and writable paths the operator image must contain so the existing shell boundary still works inside Docker.

## Questions

| # | Question |
|---|----------|
| **X2-Q1** | Which executables must be present in the final image or on `PATH` for `cassini-operator`, `cassini`, `doctor`, `build`, and `publish` to succeed? |
| **X2-Q2** | Which repo files/scripts are required at runtime for the current publish/export path, and can they be copied without the whole repo checkout? |
| **X2-Q3** | Which writable paths must be mounted or created: DB, work root, site root, cache root, temp, and possibly logs? |
| **X2-Q4** | Which optional envs should the operator image pass through for readable cleanup and summary generation? |
| **X2-Q5** | Can bundle v1 keep the current publish boundary, or does the exporter runner need a refactor before the image is practical? |

## Initial findings

1. **The dev wrapper scripts are not the runtime contract.**
   `bin/cassini` and `bin/cassini-operator` build temp binaries on each invocation. The container should instead ship prebuilt executables and point `CASSINI_BIN` at the real in-image `cassini` binary.

2. **`build` needs media tools and writable cache.**
   `cassini doctor --target build` explicitly checks for:
   - `ffmpeg`
   - `ffprobe`
   - writable temp space
   - writable `CASSINI_CACHE_ROOT` / STT model cache path

3. **`publish` currently pulls in Node-based runtime pieces.**
   `cassini publish` resolves the exporter runner from repo root by default, and the runner currently:
   - requires `npm`
   - expects `cassini-viewer/package.json`
   - builds viewer `dist/` if missing
   - runs `node ./scripts/export-static-meetings.mjs`

4. **There is already an env escape hatch for publish.**
   `CASSINI_EXPORTER_RUNNER` can override the default runner path. That may let the image ship a more direct in-image runner path, but the underlying Node/viewer dependency still exists unless we refactor the exporter path.

5. **Optional LLM envs are part of the operator runtime contract if summary/readable cleanup should work in deployment.**
   Relevant envs already in the code:
   - `OPENROUTER_API_KEY`
   - `OPENROUTER_BASE_URL`
   - `LLM_BASE_URL`
   - `LLM_MODEL`
   - `SUMMARY_MODEL`
   - `CASSINI_SUMMARY_DISABLED`
   - `CASSINI_READABLE_STRICT_BATCHES`

## Conclusions

### X2-Q1 — runtime executables to ship

Bundle v1 operator image should ship these runtime executables directly:

- `cassini-operator`
- `cassini`
- `ffmpeg`
- `ffprobe`
- `node`
- `npm`

Notes:
- `cassini-operator` and `cassini` should be built into the image during a multi-stage build.
- The temp-build dev wrappers under `bin/` are not part of the container runtime contract.
- `node`/`npm` are still needed because the current publish/export path depends on the viewer exporter runner.

### X2-Q2 — minimum runtime subtree for publish/export

Bundle v1 can keep the current publish boundary, but the operator image must carry the runtime files that `cassini publish` expects.

Minimum subtree to copy into the image:

- `cassini-publisher/bin/export-static-meetings.sh`
- `cassini-viewer/package.json`
- `cassini-viewer/scripts/export-static-meetings.mjs`
- any viewer scripts/imported files that export script depends on
- `cassini-viewer/dist/` so the runner does not have to build the viewer at runtime

Important consequence:
- we should point `CASSINI_EXPORTER_RUNNER` inside the container to the shipped runner path
- we should make the shipped image self-sufficient rather than relying on repo-root auto-discovery behavior

### X2-Q3 — writable paths / volumes

Bundle v1 operator container needs writable paths for:

- DB root / SQLite file
- work root
- site root
- cache root (`CASSINI_CACHE_ROOT`)
- temp directory

Shaping direction:
- keep the in-container paths fixed
- let compose decide named volume vs bind mount
- operator mounts published site read-write
- viewer mounts the same published site read-only
- logs can stay on stdout/stderr unless a later need forces a dedicated mounted log path

### X2-Q4 — optional env pass-through for summary/readable cleanup

If deployment should support readable cleanup / summary generation, the operator image should honor these existing envs internally:

- `OPENROUTER_API_KEY`
- `OPENROUTER_BASE_URL`
- `LLM_BASE_URL`
- `LLM_MODEL`
- `SUMMARY_MODEL`
- `CASSINI_SUMMARY_DISABLED`
- `CASSINI_READABLE_STRICT_BATCHES`

These are runtime capability envs, not core compose-shaping knobs. They can exist without becoming the main public deployment contract for D-246.

### X2-Q5 — keep current exporter boundary or refactor first?

**Conclusion: keep the current exporter boundary for bundle v1.**

That means:
- build `cassini-operator` and `cassini` into the image
- use in-container env wiring to point `CASSINI_BIN` at the shipped `cassini` binary
- use in-container env wiring to point `CASSINI_EXPORTER_RUNNER` at the shipped exporter runner
- do not expose those bin-path envs as user-facing config

This is heavier than ideal, but it preserves the current shell boundary and avoids turning D-246 into a larger publish-architecture rewrite.

## Acceptance

Spike is complete because:

- we can list the exact runtime executables the operator image must ship
- we can describe the minimum repo/runtime subtree needed for the current publish/export path
- we can name the writable paths/volumes the compose bundle must provide to the operator
- we can say that bundle v1 keeps the current exporter boundary rather than requiring a refactor first
