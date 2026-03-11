# Cassini Player

`cassini-player` is the room-streaming component of the Cassini suite.

For normal use from this repo checkout, prefer the root product CLI:

```bash
./bin/cassini dev player ...
```

It joins a Talk room and plays deterministic media into it for demos, smoke
tests, sync validation, and full-suite roundtrips.

This package exposes the preferred suite-level player entry points. The current
implementation stays intentionally thin and delegates to the existing lab
scripts under `harness/bin/`, which still own the local stack, fixture generation,
and E2E harness behavior.

## Tools

- `bin/stream-video.sh`: generic one-or-more-client room player using local
  sample media or explicit media prefixes.
- `bin/stream-showcase-meeting.sh`: play the more natural showcase meeting
  fixture into a room.
- `bin/stream-three-songs.sh`: deterministic three-client sync and mute-rotation
  scenario.

## Typical flows

Stream a simple local media set into a room:

```bash
./cassini-player/bin/stream-video.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --duration 20
```

Play the showcase meeting fixture into a room:

```bash
./cassini-player/bin/stream-showcase-meeting.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

Run the three-song sync scenario:

```bash
./cassini-player/bin/stream-three-songs.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>"
```

## Notes

- The player currently reuses the Go rotator implementation and media
  preparation flows from `harness/`.
- `harness/` remains the lab and local-stack harness. `cassini-player` is the
  preferred human-facing entry point when you want to drive room media without
  thinking about the rest of the harness layout.
- The older synthetic fixture and the continuous three-song soak loop remain
  available under `harness/bin/` as harness-level flows rather than curated
  suite-level commands.
