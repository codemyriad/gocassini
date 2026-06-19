---
shaping: true
---

# Play Commands — Shaping

This document shapes first-class Cassini CLI commands for playing speech-like media into a local Nextcloud Talk meeting through the harness.

## Source

> I'd like to be able to programmatically play sounds into a meeting when using the harness (local Nextcloud Talk). 
> There are already scripts that do that and I'd like to utilise them, but pack them into first class commands.
>
> I would like to be able to:
> - play a talking audio track into a meeting (simulating someone talking)
> - differentiate between a short talk, a longer talk, and a multi-person talk
> - specify a config (with sensible defaults)
>
> The config:
> - `--nextcloud-host` (with sensible default) - should also support `CASSINI_HARNESS_HOST`
> - `--room` (provide a sensible default)

Follow-up decisions from Q responses:

- Keep the command under `cassini dev play`.
- `--room` is required.
- If the named room does not exist, create it.
- `--nextcloud-host` accepts sensible defaults and honors `CASSINI_HARNESS_HOST`.
- Replace short/long/multi preset subcommands with two controls:
  - optional `--duration`; if omitted, play the whole clip
  - `--mode=single|full`; default `full`
- Use the pinned showcase fixture.
- Expose only `--duration` and `--mode` beyond the required target config.
- Always use `harness/runtime` for room state.

---

## Investigation summary

The core playback functionality already exists, but it is exposed as lower-level harness player scripts rather than as a target-aware `bin/cassini` command.

| Existing piece | Location | What it already does | Gap for this initiative |
|---|---|---|---|
| Product CLI launcher | `bin/cassini` | Builds and executes `cassini-go-recorder/cmd/cassini`. | New commands should be implemented in the Go CLI under `internal/cassini`, not as separate top-level shell entrypoints. |
| Dev harness namespace | `cassini-go-recorder/internal/cassini/dev.go` | Exposes `cassini dev stack`, `room`, `fixture`, `player`, `smoke`, and `ci-e2e`. | No intention-shaped `play` command with `--room`, `--nextcloud-host`, `--mode`, and `--duration`. |
| Generic Talk media player | `harness/bin/stream-video.sh` | Starts one or more Go Talk rotator clients with `--call-url`, `--users`, `--duration`, media prefixes, names, join delays, sync shifts, and bot durations. | Requires a full call URL and low-level media-prefix knowledge. Its default duration is 20s, so whole-clip single-speaker playback should pass `--duration 0`. |
| Scenario-based meeting player | `harness/bin/stream-synthetic-meeting.sh` | Reads a scenario manifest and calls `stream-video.sh` with names, media prefixes, join delays, and scenario duration. | Good `full` mode mechanism, but still requires call URL and scenario/output-dir choices. |
| Actual Talk publisher | `harness/go-talk-rotator/main.go` | Joins a Talk room as guest bot(s), publishes VP8 IVF video plus Opus OGG audio, rotates audible audio between bots, and exits on duration/EOF/room-empty. | Its public input is only `--call-url`; room and host resolution belong one layer up. |
| Room creation | `harness/bin/create-room.sh` | Creates a Talk room through OCS, writes `harness/runtime/last_room_token` and `harness/runtime/last_call_url`, and prints the call URL. | It creates a room, but does not provide a first-class get-or-create-by-name resolver for required `--room`. |
| Pinned showcase fixture | `harness/media/processed/showcase-lantern-festival-v1/*` | Provides committed/LFS IVF+OGG media for six named participants plus a manifest and reference transcript. | This should become the default speech media source for both `single` and `full` modes. |

Conclusion: the implementation should wrap the existing streamers and add a small target/room resolver. It should not reimplement WebRTC publishing.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | A user can invoke `bin/cassini` to play speech-like media into a local Nextcloud Talk meeting without calling harness scripts directly. | Core goal |
| R1 | The command supports `--mode=single|full`, where `single` plays one speaker and `full` plays all speakers; `full` is the default. | Must-have |
| R2 | The command supports optional `--duration`; when omitted, playback runs for the whole selected clip/fixture. | Must-have |
| R3 | The implementation reuses the existing harness media-player scripts and Go Talk rotator instead of duplicating the WebRTC publisher. | Must-have |
| R4 | The command accepts `--nextcloud-host` and honors `CASSINI_HARNESS_HOST` when the flag is omitted. | Must-have |
| R5 | The command requires `--room`, resolves that room by name, and creates a new local Talk room with that name when it does not exist. | Must-have |
| R6 | Defaults use the pinned showcase speech fixture, not the sine-wave sample media. | Must-have |
| R7 | The stable command surface stays narrow: target config plus only `--duration` and `--mode` for playback behavior. | Must-have |
| R8 | The command stays inside the local harness/dev boundary and always uses `harness/runtime` for room state. | Must-have |

---

## CURRENT: lower-level harness player surface

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `bin/cassini` delegates into the Go CLI, whose `dev` namespace is implemented in `cassini-go-recorder/internal/cassini/dev.go`. | |
| **CURRENT2** | `cassini dev player video` wraps `harness/bin/stream-video.sh`; `showcase` wraps the showcase synthetic-meeting path; `three-songs` wraps the music sync test. | |
| **CURRENT3** | `stream-video.sh` can already play one or more clients into a Talk call using IVF+OGG media, display names, durations, join delays, and media prefixes. | |
| **CURRENT4** | `stream-synthetic-meeting.sh` can already play a scenario-defined multi-person meeting by reading a generated or committed manifest. | |
| **CURRENT5** | `create-room.sh` can create a public Talk room with a supplied display name and stores `last_room_token` / `last_call_url` under `harness/runtime`. | |
| **CURRENT6** | Existing player commands accept `--call-url` or rely on `harness/runtime/last_call_url`; they do not expose `--nextcloud-host`, required `--room`, `--mode`, or whole-clip default behavior. | |
| **CURRENT7** | The native harness default host is `127.0.0.1:28080`; VM-friendly host selection exists elsewhere, but current player dispatch does not normalize `CASSINI_HARNESS_HOST`. | |
| **CURRENT8** | Existing names are harness/lab oriented (`video`, `showcase`, `three-songs`), so users must know which script and fixture correspond to single-speaker or multi-speaker talk. | |

---

## A: Single `cassini dev play` command with mode + duration — selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Add `cassini dev play --room <room-name> [--nextcloud-host <host-or-url>] [--mode single|full] [--duration <seconds>]`. | |
| **A2** | Keep `cassini dev player ...` as the lower-level/backcompat namespace for raw harness player flows. | |
| **A3** | Implement a shared target resolver: `--nextcloud-host` > `CASSINI_HARNESS_HOST` > `127.0.0.1`; normalize bare hosts to `http://<host>:28080`; preserve full URLs when supplied. | |
| **A4** | Implement required-room resolution by name: find an existing Talk room with the requested name for the resolved Nextcloud base URL; if not found, create it; write/update `harness/runtime` state. | |
| **A5** | For `--mode=single`, delegate to `stream-video.sh` with one participant from the pinned showcase fixture, using whole-clip playback when `--duration` is omitted. | |
| **A6** | For `--mode=full`, delegate to `stream-synthetic-meeting.sh` with the pinned showcase scenario/output directory and all fixture participants, using full fixture duration when `--duration` is omitted. | |
| **A7** | Expose no low-level script passthrough in the stable command; users needing lab knobs continue using `cassini dev player ...` or the harness scripts directly. | |
| **A8** | Print the resolved room, call URL, mode, media source, and duration behavior before launching the underlying script. | |
| **A9** | Add Go CLI tests around dispatch, target normalization, required room validation, room get-or-create behavior, default mode, duration handling, and script argument construction. | |

### Command sketch for A

```bash
# Full multi-speaker showcase fixture, whole clip.
./bin/cassini dev play --room "Local smoke room"

# Single speaker, whole clip.
./bin/cassini dev play --room "Local smoke room" --mode single

# Single speaker, 20-second short talk.
./bin/cassini dev play --room "Local smoke room" --mode single --duration 20

# Full multi-speaker fixture, 120-second longer excerpt, targeting a VM/local host.
./bin/cassini dev play \
  --nextcloud-host 192.168.252.21 \
  --room "Local smoke room" \
  --mode full \
  --duration 120
```

### Target-resolution sketch for A

1. Resolve Nextcloud base URL:
   - if `--nextcloud-host` is a full URL, trim trailing slash and use it as the base URL
   - if `--nextcloud-host` is a bare host/IP, use `http://<host>:28080`
   - if omitted, repeat the same logic with `CASSINI_HARNESS_HOST`
   - if both are omitted, use `http://127.0.0.1:28080`
2. Resolve required room:
   - fail fast if `--room` is missing or blank
   - treat `--room` as a room display name
   - query Talk rooms for the resolved Nextcloud base URL and match the room display name
   - if a match exists, use its token and build `<base-url>/call/<token>`
   - if no match exists, call the room creation path with `NEXTCLOUD_URL=<base-url>` and `--name <room-name>`
   - always write/update `harness/runtime/last_room_token` and `harness/runtime/last_call_url`
3. Launch existing streamer:
   - set `CALL_URL=<resolved-call-url>` and `NEXTCLOUD_URL=<base-url>` in the environment
   - pass `--call-url <resolved-call-url>` explicitly to the selected script

### Duration semantics for A

| Mode | `--duration` omitted | `--duration N` supplied |
|------|----------------------|--------------------------|
| `single` | Call `stream-video.sh` with showcase `mira` and `--duration 0` so the Go rotator runs until EOF. | Call `stream-video.sh` with showcase `mira` and `--duration N`. |
| `full` | Call `stream-synthetic-meeting.sh` without a duration override so it uses the manifest/fixture duration. | Call `stream-synthetic-meeting.sh --duration N`. |

---

## B: Preset subcommands under `cassini dev play` — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Add `cassini dev play short-talk`, `long-talk`, and `multi-talk` subcommands. | |
| **B2** | Map short/long to fixed durations and multi-talk to the full fixture. | |
| **B3** | Keep `--room` and `--nextcloud-host` target resolution as in A. | |

B was the initial recommendation, but it is no longer selected because the accepted simplification makes `--duration` the short/long control and `--mode` the single/full control.

---

## C: Top-level `cassini play` with native Go media publishing — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Add a product-root command such as `cassini play ...`. | |
| **C2** | Move or package `harness/go-talk-rotator` code so the main Cassini Go CLI can publish media directly instead of shelling out to scripts. | ⚠️ |
| **C3** | Recreate media-fixture selection, scenario parsing, target resolution, and room creation as Go-level product code. | ⚠️ |

C gives a cleaner product-looking command, but it is larger than needed and weakens the current boundary where the harness owns local Talk lab mechanics.

---

## Fit Check

| Req | Requirement | Status | CURRENT | A | B | C |
|-----|-------------|--------|---------|---|---|---|
| R0 | A user can invoke `bin/cassini` to play speech-like media into a local Nextcloud Talk meeting without calling harness scripts directly. | Core goal | ✅ | ✅ | ✅ | ✅ |
| R1 | The command supports `--mode=single|full`, where `single` plays one speaker and `full` plays all speakers; `full` is the default. | Must-have | ❌ | ✅ | ❌ | ✅ |
| R2 | The command supports optional `--duration`; when omitted, playback runs for the whole selected clip/fixture. | Must-have | ❌ | ✅ | ❌ | ✅ |
| R3 | The implementation reuses the existing harness media-player scripts and Go Talk rotator instead of duplicating the WebRTC publisher. | Must-have | ✅ | ✅ | ✅ | ❌ |
| R4 | The command accepts `--nextcloud-host` and honors `CASSINI_HARNESS_HOST` when the flag is omitted. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R5 | The command requires `--room`, resolves that room by name, and creates a new local Talk room with that name when it does not exist. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R6 | Defaults use the pinned showcase speech fixture, not the sine-wave sample media. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R7 | The stable command surface stays narrow: target config plus only `--duration` and `--mode` for playback behavior. | Must-have | ❌ | ✅ | ❌ | ❌ |
| R8 | The command stays inside the local harness/dev boundary and always uses `harness/runtime` for room state. | Must-have | ✅ | ✅ | ✅ | ❌ |

**Notes:**
- CURRENT fails R1, R2, R4, R5, R6, and R7 because the existing surface is script-shaped and call-URL/media-prefix oriented.
- B fails R1, R2, and R7 because fixed preset subcommands are not the selected mode/duration interface.
- C fails R3 because native publishing duplicates or relocates the already-working harness player mechanics.
- C fails R7 and R8 because it turns local harness playback into a broader product-looking command and would either expose too many implementation knobs or hide useful lab behavior.

---

## Why A is selected

Shape A matches the accepted simplification:

- one first-class local harness command path: `cassini dev play`
- one required target room by name
- one optional host override with `CASSINI_HARNESS_HOST` support
- one mode switch for single-speaker vs all-speaker playback
- one duration override for short/long excerpts, while omitted duration means whole clip
- existing harness scripts remain the playback implementation

Shape B was rejected because fixed short/long/multi subcommands are less flexible than `--duration` + `--mode`.

Shape C was rejected because this is still harness-specific and should not become a top-level product media publisher yet.

---

## Detail A: concrete mechanisms

| Part | Mechanism |
|------|-----------|
| **A1.1** | Add `play` to `runDev` dispatch and `printDevUsage`. |
| **A1.2** | Implement `runDevPlay(ctx, repoRoot, args, stdout, stderr)` as a flag-driven command, not a subcommand family. |
| **A1.3** | Accept only `--room`, `--nextcloud-host`, `--mode`, and `--duration`; reject unknown arguments with usage. |
| **A1.4** | Validate `--mode` as `single` or `full`, defaulting to `full`. |
| **A1.5** | Validate `--duration` as non-negative seconds; absent means whole clip, while zero can explicitly mean whole clip too. |
| **A2.1** | Keep existing `runDevPlayer` unchanged for lower-level raw harness player flows. |
| **A3.1** | Define `devPlayTargetOptions` with `nextcloudHost`, `roomName`, and derived `baseURL` / `callURL`. |
| **A3.2** | Normalize `--nextcloud-host` / `CASSINI_HARNESS_HOST` values that include or omit scheme/port. |
| **A4.1** | Treat `--room` as a room display name, not as a token, because non-existent named rooms can be created while arbitrary tokens cannot. |
| **A4.2** | Add or reuse an OCS helper to list existing Talk rooms for the admin harness account and match by display name. |
| **A4.3** | If a matching room is found, use its token to build the call URL. |
| **A4.4** | If no matching room is found, call `harness/bin/create-room.sh --name <room-name>` with `NEXTCLOUD_URL=<base-url>` and capture the created call URL. |
| **A4.5** | Persist the selected token/call URL to `harness/runtime/last_room_token` and `harness/runtime/last_call_url` regardless of whether the room was found or created. |
| **A5.1** | `--mode=single`: call `harness/bin/stream-video.sh --call-url <callURL> --users 1 --media-prefix <showcase-dir>/mira --names "Mira Chen" --skip-prepare`. |
| **A5.2** | In `single` mode, pass `--duration 0` when no CLI duration is provided; pass `--duration N` when provided. |
| **A6.1** | `--mode=full`: call `harness/bin/stream-synthetic-meeting.sh --call-url <callURL> --scenario <showcase-scenario> --output-dir <showcase-dir> --skip-prepare`. |
| **A6.2** | In `full` mode, omit `--duration` when no CLI duration is provided; pass `--duration N` when provided. |
| **A7.1** | Do not add low-level passthrough in this first cut; direct advanced usage remains available through `cassini dev player ...` or harness scripts. |
| **A8.1** | Print a launch line such as `play -> room="Local smoke room" call=http://.../call/<token> mode=full duration=whole media=showcase-lantern-festival-v1`. |
| **A9.1** | Refactor script execution in `dev.go` behind an injectable function so CLI tests can verify generated script paths, arguments, and environment without launching Docker/WebRTC. |
| **A9.2** | Test host normalization, `CASSINI_HARNESS_HOST` fallback, missing-room validation, default mode, whole-clip duration behavior, and generated script args for both modes. |
| **A9.3** | Test room get-or-create with fake OCS responses: existing matching room, no match leading to creation, and OCS/create failures. |

---

## Validation

Recorded VM validation is defined in:

- `planning/initiatives/mvp/D-288-play-commands/validation.md`

The validation creates/resolves a room, starts a Cassini operator recording job, runs a 20-second `cassini dev play --duration 20` feed, and verifies the operator/viewer outputs.

---

## Deferred from this cut

- fixed `short-talk`, `long-talk`, and `multi-talk` subcommands
- room-token input as the primary `--room` contract
- implicit last-room default when `--room` is omitted
- VM runtime lookup under `harness/vm/runtime`
- low-level script passthrough flags
- top-level `cassini play`
- native Go WebRTC publishing inside the main Cassini CLI
