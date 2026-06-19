---
shaping: true
---

# D-288.1 — Private 1:1 Meeting Playback Shaping

This continuation shapes the next D-288 unit: use a better synthetic fixture and make D-288 playback useful for private Nextcloud Talk 1:1 meetings with Nextcloud-native recording.

## Source

> First we shold replace the lantern festival with pied piper synthetic meeting. If the fixture exists, great, if not it's synthesised from the source (check existing usage for pied piper - we should follow the same one with caching - if it exists, great!)
>
> Furthermore I would like to be able to use the same command as implemented in the existing D-288 work (`cassini dev play (...)`) to simulate a private (1:1) meeting.
>
> I would like to be able to do it e2e in (almost) a single script.
>
> The way I see myself using this:
> - there's a script to run that will:
>     - create two Nextcloud users (first two speakers from the pied piper meeting)
>     - create conversations (not calls, just conversations):
>         - between the two synthetic users
>         - between `admin` and the first speaker
>     - the script can be something like `cassini dev play-scaffold`
> - I can run some `cassini play (...)` command to:
>     - start a private call between the two synthetic users (with auto recording), or
>     - start a private call between the `admin` and the first synthetic user (with auto recording)
>     - NOTE: the script has to start the recording from nextcloud (if there's a param to pass or something, NOT use the operator API) -- the purpose of the simulator is to test the Nextcloud integrated recorder e2e

## Current shaping state

Shape **A** is selected as revised: add a separate `cassini dev play-private` command so the private 1:1 flow does not pollute the existing public-room `cassini dev play` surface.

Completed shaping artifacts:

- `spike_pied_piper_fixture.md` — confirmed the Pied Piper fixture exists, identified the first two speakers, and described the cached generation path.
- `spike_nextcloud_private_api.md` — confirmed the Nextcloud/Talk API sequence for authenticated 1:1 conversation creation, private call join, and Nextcloud-native recording start.
- `breadboarding.md` — maps the selected shape into concrete command, scaffold, playback, recording, and cleanup affordances.
- `slices.md` — defines the implementation slice order from fixture swap through admin private playback validation.

Open questions are tracked in `open_questions.md`; there are currently no open questions.

---

## Resolved questions

Q1:
- **Question:** How should `cassini dev play` select a scaffolded private conversation while preserving the existing public `--room` flow?
- **Suggestion:** Add `--conversation synthetic|admin` as an alternative to `--room`. `synthetic` targets the first-speaker ↔ second-speaker conversation, and `admin` targets `admin` ↔ first-speaker. Keep `--room` required only for public-room playback.
- **Rationale:** This keeps the existing D-288 command intact, avoids overloading `--room` with scaffold aliases, and gives a short repeatable manual command.
- **Alternatives:** Overload `--room` with reserved scaffold names; add `--private synthetic|admin`; require `--call-url` / room token from scaffold output.
- **Response:** Use `cassini dev play-private` as the base command. The command should use private rooms and all related flags should be attached there to avoid polluting the existing flow. Use `--conversation` for `synthetic` / `admin`. The scaffold should be run before the command if not set up already. Add `cassini dev play-private --scaffold-only` to just run the scaffold.

Q2:
- **Question:** Should private playback always start Nextcloud integrated recording, or should recording be controlled by a flag?
- **Suggestion:** For scaffolded private conversations, start Nextcloud integrated recording by default and add `--no-record` only if a non-recorded private playback escape hatch is needed.
- **Rationale:** The stated purpose of the simulator is to test the Nextcloud integrated recorder end-to-end, so forgetting a `--record` flag would make the common path less useful.
- **Alternatives:** Require an explicit `--record` flag; put recording only in a wrapper script; leave recording manual in the Nextcloud UI.
- **Response:** Always start the recording; this is the main purpose.

Q3:
- **Question:** What deterministic user IDs and passwords should `play-scaffold` create for the first two Pied Piper speakers?
- **Suggestion:** Use harness-only deterministic IDs `cassini-erlich` and `cassini-monica`, with display names from the manifest and a default password from `CASSINI_PLAY_SCAFFOLD_PASSWORD` falling back to a documented dev-only value.
- **Rationale:** Deterministic IDs make the scaffold idempotent and easy to clean up, while an env override avoids baking a secret-looking value into code for every environment.
- **Alternatives:** Generate random users each run and store credentials in `harness/runtime`; use Nextcloud app passwords per run; require users to be pre-created manually.
- **Response:** LGTM.

Q4:
- **Question:** Which private conversation should be the default if `cassini dev play` supports a private target but no target is specified?
- **Suggestion:** Do not add an implicit private default; require `--conversation synthetic` or `--conversation admin` for private playback, and keep `--room` required for public playback.
- **Rationale:** Explicit target selection prevents accidentally recording the admin conversation when the synthetic-speaker conversation was intended.
- **Alternatives:** Default to `synthetic`; default to the last scaffolded conversation in runtime state; prompt interactively when running in a TTY.
- **Response:** Non-issue under the Q1 decision: `cassini dev play-private` owns the private target flags.

---

## Requirements (R)

| ID | Requirement | Status |
|----|-------------|--------|
| R0 | A developer can simulate a private 1:1 Nextcloud Talk meeting through `cassini dev play-private`. | Core goal |
| R1 | Playback uses the Pied Piper synthetic meeting fixture instead of Lantern Festival, reusing existing cached generation when media is missing. | Must-have |
| R2 | A scaffold flow creates two Nextcloud users from the first two Pied Piper speakers and prepares private Talk conversations for them. | Must-have |
| R3 | The scaffold prepares two conversations, not active calls: first synthetic user ↔ second synthetic user, and `admin` ↔ first synthetic user. | Must-have |
| R4 | `cassini dev play-private --conversation synthetic|admin` can start a private call for the selected scaffolded conversation and feed Pied Piper speaker media into it. | Must-have |
| R5 | Recording is always started through Nextcloud/Talk integrated recording, not through the Cassini operator API. | Must-have |
| R6 | The workflow is repeatable in almost one script: scaffold idempotently, then run a play-private command for the target private conversation. | Must-have |
| R7 | The existing public-room `cassini dev play --room ...` flow remains compatible and does not inherit private-meeting flags. | Must-have |
| R8 | The continuation stays in the local harness/dev boundary and uses harness runtime/cache conventions rather than becoming a production product surface. | Must-have |

---

## Fixture spike summary

| Fact | Finding |
|---|---|
| Pied Piper scenario | `harness/scenarios/synthetic-pied-piper.v1.json` |
| Processed media dir | `harness/media/processed/synthetic-pied-piper-v1` |
| Existing lower-level defaults | `prepare-synthetic-meeting.sh` and `stream-synthetic-meeting.sh` already default to Pied Piper. |
| Cache behavior | `prepare-synthetic-meeting.py` skips synthesis when `manifest.json` exists and all referenced media files are present, unless `--force` is passed. |
| First synthetic user | `erlich` / `Erlich Bachman` |
| Second synthetic user | `monica` / `Monica Hall` |
| D-288 change needed | Replace hard-coded Lantern Festival constants in `dev_play.go` with Pied Piper fixture descriptor and add an ensure step for single-mode playback. |

---

## Nextcloud private API spike summary

| Need | API/mechanism |
|---|---|
| Authenticate playback clients | One OCS client and cookie jar per participant; send Basic Auth on each participant's OCS requests; reuse the cookie jar for `participants/active`, signaling settings, call join, and recording start. |
| Create private 1:1 conversation | `POST /ocs/v2.php/apps/spreed/api/v4/room` with `roomType=1&invite=<target-user-id>` as the creator user. Existing one-to-one rooms return `200`; new ones return `201`. |
| Join private conversation | `POST /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants/active` as the logged-in user. This creates/stores the Talk session id in the Nextcloud session. |
| Join private call | `POST /ocs/v2.php/apps/spreed/api/v4/call/{token}` with `flags=7`, `silent=false`, `recordingConsent=true`, `silentFor=[]`. |
| Start Nextcloud-native recording | `POST /ocs/v2.php/apps/spreed/api/v1/recording/{token}` with `status=1` as a logged-in owner/moderator participant. This makes Nextcloud call the configured recording backend; the simulator does not call the operator API. |
| Avoid clipping the fixture start | Activate the call, start recording, poll `GET /ocs/v2.php/apps/spreed/api/v4/room/{token}` until `callRecording == 1`, then start/unmute media playback. |
| Finalize recording cleanly | After playback exits or fails, stop recording with `DELETE /ocs/v2.php/apps/spreed/api/v1/recording/{token}` and leave the preflight starter call/session through Nextcloud APIs. |

---

## CURRENT: initial D-288 public-room playback

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **CURRENT1** | `cassini dev play --room <name>` resolves or creates a Talk room by display name through admin OCS calls. | |
| **CURRENT2** | Full mode delegates to `harness/bin/stream-synthetic-meeting.sh` with the Lantern Festival scenario/output directory and `--skip-prepare`. | |
| **CURRENT3** | Single mode delegates to `harness/bin/stream-video.sh` with Lantern Festival `mira` media and guest display name `Mira Chen`. | |
| **CURRENT4** | The Go Talk rotator joins the call as guest bot clients using public call URL mechanics. | |
| **CURRENT5** | The command writes `harness/runtime/last_room_token` and `harness/runtime/last_call_url`. | |
| **CURRENT6** | There is no scaffold for Nextcloud users or private conversations. | |
| **CURRENT7** | There is no Nextcloud-native recording trigger; validation starts recording separately through the operator API. | |

---

## A: `cassini dev play-private` with scaffolded private-conversation targets — selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **A1** | Replace D-288's Lantern Festival fixture descriptor with Pied Piper: scenario `synthetic-pied-piper.v1.json`, media dir `synthetic-pied-piper-v1`, single speaker `erlich`, display name `Erlich Bachman`. | |
| **A2** | Add a fixture ensure helper before playback: verify Pied Piper `manifest.json` and needed media files; if missing, run `prepare-synthetic-meeting.sh` and rely on its cache check. | |
| **A3** | Add `cassini dev play-private --scaffold-only` to idempotently ensure deterministic users and one-to-one conversations, then write scaffold state under `harness/runtime`. | |
| **A4** | Add `cassini dev play-private --conversation synthetic|admin [--duration N]` to target one scaffolded private conversation and always start Nextcloud-native recording. | |
| **A5** | Scaffold deterministic users from the first two Pied Piper participants: `cassini-erlich` / `Erlich Bachman` and `cassini-monica` / `Monica Hall`, using `CASSINI_PLAY_SCAFFOLD_PASSWORD` or a documented harness default. | |
| **A6** | Scaffold conversations with Talk one-to-one API: `cassini-erlich` invites `cassini-monica` for `synthetic`; `admin` invites `cassini-erlich` for `admin`; persist returned tokens/call URLs. | |
| **A7** | Extend the player/rotator path to support authenticated Nextcloud users per participant while preserving existing guest behavior for public-room playback. | |
| **A8** | Add a recording gate: activate the call as the recording starter, call `POST /recording/{token}` through Nextcloud with `status=1`, poll until `callRecording == 1`, then start/unmute fixture media. | |
| **A9** | For `--conversation synthetic`, publish the first two Pied Piper speaker tracks as `cassini-erlich` and `cassini-monica`. | |
| **A10** | For `--conversation admin`, start the admin ↔ first-speaker private call with admin as the recording starter/silent participant, and publish first-speaker Pied Piper media as `cassini-erlich`. | |
| **A11** | Keep `cassini dev play --room ...` public-room behavior unchanged except for the shared Pied Piper fixture descriptor swap. | |
| **A12** | Stop recording and leave the preflight starter session through Nextcloud APIs after playback exits or fails, so the recording backend can finalize without direct operator API calls. | |
| **A13** | Add command/unit tests with fake Nextcloud/Talk endpoints for scaffold idempotency, authenticated target resolution, recording start/polling, cleanup, and script/rotator argument construction. | |

### Command sketch for A

```bash
# Idempotently create harness users and private conversations from Pied Piper.
./bin/cassini dev play-private --scaffold-only

# Play/record the first two synthetic users in their private conversation.
./bin/cassini dev play-private --conversation synthetic

# Play/record admin with the first synthetic user.
./bin/cassini dev play-private --conversation admin

# Public-room flow remains available and separate.
./bin/cassini dev play --room "Local smoke room" --duration 20
```

### Scaffold state sketch

```json
{
  "fixture": {
    "scenario": "harness/scenarios/synthetic-pied-piper.v1.json",
    "mediaDir": "harness/media/processed/synthetic-pied-piper-v1"
  },
  "users": {
    "first": { "id": "cassini-erlich", "displayName": "Erlich Bachman", "speakerId": "erlich" },
    "second": { "id": "cassini-monica", "displayName": "Monica Hall", "speakerId": "monica" }
  },
  "conversations": {
    "synthetic": { "token": "...", "callURL": "http://127.0.0.1:28080/call/..." },
    "admin": { "token": "...", "callURL": "http://127.0.0.1:28080/call/..." }
  }
}
```

---

## B: Script-only harness flow outside `cassini dev play-private` — not selected

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **B1** | Add a shell script such as `harness/bin/play-private-1-1.sh` that creates users, conversations, starts recording, and runs low-level stream scripts. | |
| **B2** | Leave the Go dev CLI focused on public-room playback. | |
| **B3** | Use Pied Piper lower-level script defaults directly. | |

B is useful as an internal prototype but does not satisfy the requested command workflow.

---

## C: Use operator API orchestration for recording — rejected by requirement

| Part | Mechanism | Flag |
|------|-----------|:----:|
| **C1** | Add a private playback command, but start recording by creating a Cassini operator job before or during playback. | |
| **C2** | Reuse the D-288 validation pattern that posts to `/jobs?provider=nextcloud-talk`. | |

C is explicitly out because this continuation is meant to test the Nextcloud integrated recorder end-to-end.

---

## Fit Check

| Req | Requirement | Status | CURRENT | A | B | C |
|-----|-------------|--------|---------|---|---|---|
| R0 | A developer can simulate a private 1:1 Nextcloud Talk meeting through `cassini dev play-private`. | Core goal | ❌ | ✅ | ❌ | ✅ |
| R1 | Playback uses the Pied Piper synthetic meeting fixture instead of Lantern Festival, reusing existing cached generation when media is missing. | Must-have | ❌ | ✅ | ❌ | ✅ |
| R2 | A scaffold flow creates two Nextcloud users from the first two Pied Piper speakers and prepares private Talk conversations for them. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R3 | The scaffold prepares two conversations, not active calls: first synthetic user ↔ second synthetic user, and `admin` ↔ first synthetic user. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R4 | `cassini dev play-private --conversation synthetic|admin` can start a private call for the selected scaffolded conversation and feed Pied Piper speaker media into it. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R5 | Recording is always started through Nextcloud/Talk integrated recording, not through the Cassini operator API. | Must-have | ❌ | ✅ | ✅ | ❌ |
| R6 | The workflow is repeatable in almost one script: scaffold idempotently, then run a play-private command for the target private conversation. | Must-have | ❌ | ✅ | ✅ | ✅ |
| R7 | The existing public-room `cassini dev play --room ...` flow remains compatible and does not inherit private-meeting flags. | Must-have | ✅ | ✅ | ✅ | ✅ |
| R8 | The continuation stays in the local harness/dev boundary and uses harness runtime/cache conventions rather than becoming a production product surface. | Must-have | ✅ | ✅ | ✅ | ✅ |

**Notes:**

- A now passes after the private API spike resolved authenticated private conversation creation, call join, and Nextcloud-native recording start.
- B still fails R0 because it avoids the requested command surface, though it may remain useful as a prototype implementation detail.
- C fails R5 because operator API orchestration bypasses the Nextcloud integrated recorder path that this simulator is meant to prove.

---

## Why A is selected

A matches the command-surface decision and the API evidence:

- private-meeting flags live under `cassini dev play-private`, not the existing public-room `cassini dev play`
- `--scaffold-only` gives an idempotent setup step
- `--conversation synthetic|admin` names the two shaped private targets
- recording is always started through Nextcloud's Talk recording API
- the operator may be called by Nextcloud as the configured recording backend, but the simulator never calls the operator API directly
- Pied Piper replaces Lantern Festival as the playback fixture

---

## Implementation breakdown

The selected shape is detailed in:

- `breadboarding.md` — concrete affordances, stores, and wiring
- `slices.md` — implementation slice order and acceptance criteria

The planned slice order is:

1. Pied Piper fixture foundation
2. private scaffold command and runtime state
3. authenticated media player foundation
4. synthetic private recording playback
5. admin private recording target and validation polish
