# D-288 Play Commands — Implementation

## Status

The initial D-288 play-command work was implemented in commit `e421ba647c18361717e54d9b3b09a597833cc276` (`D-288 impl (full)`).

The delivered scope adds a first-class local-harness playback command:

```bash
./bin/cassini dev play --room <name> [--nextcloud-host <host-or-url>] [--mode single|full] [--duration <seconds>]
```

## What changed

### 1. Dev CLI dispatch

`cassini-go-recorder/internal/cassini/dev.go` now dispatches `cassini dev play` to a dedicated Go implementation while keeping the lower-level `cassini dev player ...` namespace intact for raw harness player flows.

The script runner was also made injectable through `runDevScriptExec`, which lets tests verify generated script paths, arguments, and environment without launching the harness.

### 2. Target and room resolution

`cassini-go-recorder/internal/cassini/dev_play.go` added the target resolver for local Nextcloud Talk playback:

- `--room` is required and is treated as a Talk room display name.
- `--nextcloud-host` accepts a bare host/IP, host:port, or full URL.
- If the host flag is omitted, `CASSINI_HARNESS_HOST` is used; if that is also omitted, the default is `127.0.0.1:28080`.
- Existing Talk rooms are listed through OCS and matched by display name.
- Missing rooms are created through the Talk OCS room API.
- The selected room token and call URL are persisted to:
  - `harness/runtime/last_room_token`
  - `harness/runtime/last_call_url`

The OCS helper uses the harness admin credentials by default (`ADMIN_USER` / `ADMIN_PASSWORD`, defaulting to `admin` / `admin`).

### 3. Playback modes over existing harness scripts

The command reuses the existing harness media player scripts rather than reimplementing WebRTC publishing.

Implemented behavior:

- `--mode full` (default) delegates to `harness/bin/stream-synthetic-meeting.sh` with:
  - `harness/scenarios/showcase-lantern-festival.v1.json`
  - `harness/media/processed/showcase-lantern-festival-v1`
  - `--skip-prepare`
- `--mode single` delegates to `harness/bin/stream-video.sh` with:
  - one user
  - media prefix `showcase-lantern-festival-v1/mira`
  - display name `Mira Chen`
  - `--skip-prepare`
- Omitted or zero duration means whole fixture/clip.
- Positive `--duration N` limits playback to `N` seconds.

The command prints the resolved room, whether the room was existing or created, call URL, mode, duration behavior, and media fixture before launching playback.

### 4. Test coverage

`cassini-go-recorder/internal/cassini/dev_play_test.go` covers the core command behavior with fake OCS responses and captured script invocations:

- default `full` mode against an existing room
- `single` mode with explicit duration
- missing-room creation and runtime-state writes
- required-room and mode validation
- host/base URL normalization

## Delivered files

Implementation files changed by commit `e421ba6`:

- `cassini-go-recorder/internal/cassini/dev.go`
- `cassini-go-recorder/internal/cassini/dev_play.go`
- `cassini-go-recorder/internal/cassini/dev_play_test.go`

Planning/validation artifacts for this initial unit now live under:

- `planning/initiatives/mvp/D-288-play-commands/shaping.md`
- `planning/initiatives/mvp/D-288-play-commands/questions.md`
- `planning/initiatives/mvp/D-288-play-commands/validation.md`

## What was intentionally not solved in the initial cut

- The default media remained the Lantern Festival showcase fixture.
- Private 1:1 meeting simulation was not implemented.
- Recording orchestration stayed outside `cassini dev play`; validation starts recording separately before playback.
- No top-level `cassini play` command was added.
- No low-level passthrough flags were added to the stable `play` surface.
