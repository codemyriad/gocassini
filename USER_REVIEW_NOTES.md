# User Review Notes

Date: 2026-03-10

## What This Project Appears To Do

`gocassini` is a meeting-artifact pipeline centered on Nextcloud Talk.

- `cassini-recorder` / `cassini-go-recorder`: record one Talk room into one multitrack `.mkv`, plus session artifacts.
- `cassini-transcriber`: turn that recording into a browser-friendly artifact set (`meeting.webm`, transcript JSON, captions, manifest).
- `cassini-publisher`: orchestrate transcription and export static meeting libraries.
- `cassini-viewer`: browse published meetings in the browser.
- `cassini-player` and `cassini-lab`: generate media, drive rooms, and run local end-to-end tests.
- `cassini-diagnostics`: inspect and validate artifacts.

The overall product story makes sense: record a real meeting, transcribe it, publish it, then browse it as a static site.

## What I Tried

Commands I ran from repo root:

```bash
./cassini-recorder/bin/record-talk.sh --help
./cassini-recorder/bin/simulate.sh --help
./cassini-recorder/bin/simulate.sh --output /home/silvio/dev/gocassini/.tmp-review/gocassini-user-review.csr
./cassini-diagnostics/bin/inspect-artifact.sh /home/silvio/dev/gocassini/.tmp-review/gocassini-user-review.csr
./cassini-diagnostics/bin/inspect-artifact.sh /home/silvio/dev/gocassini/media/daily-meeting--2026-03-04--12:36:53.mkv
DURATION=5 USERS=1 ./cassini-lab/bin/smoke.sh
./cassini-publisher/bin/export-static-meetings.sh --source-dir /home/silvio/dev/gocassini/cassini-viewer/exports/static-meetings --output-dir /home/silvio/dev/gocassini/.tmp-review/static-export
cd cassini-viewer && npm test
cd cassini-viewer && npm run build
./cassini-publisher/bin/process-meeting.sh --input /home/silvio/dev/gocassini/media/daily-meeting--2026-03-04--12:36:53.mkv --output-root /home/silvio/dev/gocassini/.tmp-review/publisher-output --device cpu
```

What worked:

- recorder simulate mode worked once I stopped writing to `/tmp`
- diagnostics inspection of both the simulated `.csr` and the sample `.mkv` worked
- viewer build worked
- static export from existing viewer artifacts worked

What failed:

- `--help` on some wrappers printed usage but exited non-zero
- the documented smoke path failed before reaching media flow
- the first transcriber/publisher attempt failed on a writable-cache problem inside the mounted Whisper cache
- `cassini-viewer` tests failed here because the machine's `/tmp` is full

## What Felt Difficult, Underdocumented, Or Unintuitive

### 1. The repo has many entry points, but the user journey is still harder to see than it should be

The root README explains the suite, but as a new user I still had to infer:

- which commands are for normal product use
- which commands are for local testing only
- which commands are thin wrappers around `test/bin`
- which commands need real Nextcloud credentials vs local Docker vs only local files

The project model becomes clear after reading several READMEs, not after reading one.

### 2. `--help` is not reliably "safe discovery"

Both:

```bash
./cassini-recorder/bin/record-talk.sh --help
./cassini-recorder/bin/simulate.sh --help
```

printed useful usage text, but exited with status `1` because the Go command reports `flag: help requested` as an error. As a user, I expect `--help` to exit successfully.

The publisher export wrapper is rougher:

```bash
./cassini-publisher/bin/export-static-meetings.sh --help
```

did not show help at all. It ran the Node exporter and crashed with a stack trace about a missing source directory.

### 3. Wrapper scripts changing directories makes relative paths surprising

`cassini-diagnostics/bin/inspect-artifact.sh media/...mkv` failed because the wrapper `cd`s into `cassini-go-recorder` before running the Go binary. That means repo-relative paths from the place where I launched the command are not preserved.

Absolute paths work. Repo-relative paths do not always work. That is easy to trip over.

### 4. The docs rely on `/tmp` heavily, but the tooling does not defend against `/tmp` problems

The docs frequently use `/tmp/...` paths. On this machine `/tmp` is full, and that surfaced in multiple ways:

- `simulate.sh --output /tmp/...csr` failed with `no space left on device`
- the local stack smoke flow failed because Docker healthcheck execs could not write temporary runtime files, which surfaced only as `db failed to start` / `container ... is unhealthy`
- viewer tests failed with a generic `ENOSPC`

The project is not responsible for my machine state, but the current errors do not make the real cause obvious to a first-time user.

### 5. The local-stack commands hide important prerequisites one layer below the advertised entry point

The top-level `cassini-lab` and `cassini-player` wrappers are extremely thin. That is clean structurally, but operationally it means the real prerequisites live in `test/bin` and `test/README.md`.

Examples:

- Python / `uv` expectations for synthetic fixture generation are described in `test/README.md`, not where I first looked
- the lab README says it delegates to `test/bin`, but if I want to understand what `smoke.sh` actually does or what it needs, I still have to dig

This makes the repo feel more "developer harness first" than "new user first."

### 6. The transcriber first-run path is not very transparent

My first real publisher attempt did real work, but the experience was opaque:

- there was no early visible progress while Docker and the containerized pipeline started
- the first detailed output I inspected was a giant raw `ffmpeg` command line, which is not meaningful progress feedback for most users
- the run eventually failed on:

```text
PermissionError: [Errno 13] Permission denied: '/models/whisper/.locks/models--Systran--faster-whisper-large-v3'
```

That failure happened after time had already been spent generating intermediate audio. It would be better to validate cache writability before long preprocessing work begins.

### 7. The default transcriber setup is heavier than the docs make it feel

The transcriber README is detailed, but the real first-run implications are easy to underestimate:

- Docker image build or pull
- Whisper model download
- cache directory permissions
- potentially long CPU runtime on a sample meeting

The docs mention model and backend selection, but the practical "first run may be large, slow, and cache-sensitive" experience could be stated more plainly.

### 8. There is some terminology overhead

The project uses several related concepts:

- final meeting `.mkv`
- session artifact directory
- meeting artifact directory
- static export
- viewer bundle
- readable transcript
- digest timeline vs source timeline

These all make sense eventually, but a compact glossary near the top-level README would reduce the time to orient.

## Net Takeaway

The product concept is coherent and stronger than the repo's first-run ergonomics suggest.

Once I got onto the happy path:

- recorder simulate mode was straightforward
- diagnostics output was concrete and useful
- the sample meeting artifact made the end result legible
- static export from existing artifacts worked cleanly

The main friction is not "the project seems broken." The main friction is that first-time use still feels too dependent on internal structure knowledge, absolute paths, and environment troubleshooting.
