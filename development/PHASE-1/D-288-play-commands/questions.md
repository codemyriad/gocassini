Q1:
- **Question:** Should the user-facing command be `cassini dev play ...` rather than a top-level `cassini play ...` command?
- **Suggestion:** Use `cassini dev play ...` and keep `cassini dev player ...` as the lower-level harness/player namespace.
- **Rationale:** This is explicitly for the local harness, so it should stay under `dev`, but `play` is still a first-class Cassini command path rather than a raw script.
- **Alternatives:** Add top-level `cassini play ...`; extend existing `cassini dev player ...`; add only shell aliases around the scripts.
- **Response:** Yes, that's right.
- **Decision:** Use `cassini dev play ...`.

Q2:
- **Question:** What should `--room` mean, and what should happen when it is omitted?
- **Suggestion:** Originally suggested optional room with last-room fallback or auto-create.
- **Rationale:** The underlying player needs a call URL, but room handling should be simpler than passing a full call URL.
- **Alternatives:** Require `--room`; treat `--room` as a room display name and always create/find by name; expose `--call-url` instead of or in addition to `--room`.
- **Response:** Require the room; if the room doesn't exist, a new one should be created.
- **Decision:** `--room` is required. Treat it as a room display name so the command can find or create that named room. No omitted-room default.

Q3:
- **Question:** What should `--nextcloud-host` accept and how should it default?
- **Suggestion:** Accept either a bare host/IP or a full base URL. Default to `CASSINI_HARNESS_HOST` if set, otherwise `127.0.0.1`; map bare hosts to `http://<host>:28080`.
- **Rationale:** This matches the native and VM harness defaults while letting the common case stay short.
- **Alternatives:** Only accept full URLs; use `NEXTCLOUD_URL` as the primary config instead; name the flag `--nextcloud-url` rather than `--nextcloud-host`.
- **Response:** Yes, that's right.
- **Decision:** Use the suggested `--nextcloud-host` behavior.

Q4:
- **Question:** What should the preset durations be?
- **Suggestion:** Originally suggested fixed `short-talk = 20s`, `long-talk = 120s`, and `multi-talk = full showcase fixture` presets.
- **Rationale:** Fixed presets would make short/long/multi easy to invoke.
- **Alternatives:** Use 15s/60s/full; use 30s/180s/full; make all presets run until media EOF unless `--duration` is set.
- **Response:** Simplify: accept optional `--duration`; if not provided, play the whole clip. Additionally accept `--mode=single|full` defaulting to `full`; `single` means one speaker and `full` means all speakers.
- **Decision:** Do not add short/long/multi subcommands. Use `--duration` for short/long excerpts and `--mode=single|full` for speaker count.

Q5:
- **Question:** Which media should the presets use by default?
- **Suggestion:** Use the pinned showcase fixture under `harness/media/processed/showcase-lantern-festival-v1`: `mira` for single-speaker mode and all participants for full mode.
- **Rationale:** It is already in the repo, already wired to existing player scripts, and avoids requiring TTS generation before the first run.
- **Alternatives:** Use the synthetic Pied Piper fixture; generate fresh Kokoro TTS media every time; use the LibriSpeech `parakeet-smoke.mkv` clip for short-talk and TTS fixtures for longer cases.
- **Response:** If that would work, go with the suggestion.
- **Decision:** Use the pinned showcase fixture. Use `mira` for `single`; use all showcase participants for `full`.

Q6:
- **Question:** How much low-level script customization should the first-class commands expose?
- **Suggestion:** Originally suggested common flags plus optional passthrough for rare harness-script options.
- **Rationale:** The commands should read as user-intention presets, not as a mirror of every lab knob, while still leaving an escape hatch.
- **Alternatives:** Mirror all `stream-video.sh` and `stream-synthetic-meeting.sh` flags; expose no passthrough and require direct scripts for advanced cases; split advanced options into a separate `cassini dev player` workflow only.
- **Response:** Just `--duration` and `--mode=single|full`.
- **Decision:** Expose only target config (`--room`, `--nextcloud-host`) plus `--duration` and `--mode`. No passthrough in the first cut.

Q7:
- **Question:** Which runtime directory should the default room lookup use when both native and VM harness state may exist?
- **Suggestion:** Originally suggested preferring `harness/vm/runtime` when `CASSINI_HARNESS_VM` is enabled, otherwise `harness/runtime`.
- **Rationale:** Runtime state can otherwise point at stale call URLs.
- **Alternatives:** Always use native `harness/runtime`; always create a fresh room when `--room` is omitted; require `--room` whenever `--nextcloud-host` or `CASSINI_HARNESS_HOST` points away from localhost.
- **Response:** Always use `harness/runtime`; room is required.
- **Decision:** Always use `harness/runtime`. Do not use `harness/vm/runtime`; do not default from the last room when `--room` is missing.
