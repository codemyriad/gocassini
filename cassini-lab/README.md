# Cassini Lab

`cassini-lab` is the local-stack and end-to-end harness surface of the Cassini
suite.

It owns the reproducible local Talk environment, fixture-oriented roundtrips,
and CI-style validation entry points used to exercise the rest of the suite.

This package is intentionally separate from `cassini-player`:

- `cassini-player` injects media into rooms,
- `cassini-lab` brings up the local environment and drives validation flows.

## Tools

- `bin/up.sh`, `bin/down.sh`, `bin/status.sh`: local Talk stack lifecycle.
- `bin/create-room.sh`: create a room in the local stack.
- `bin/smoke.sh`: one-command local smoke run.
- `bin/prepare-synthetic-meeting.sh`: generate the synthetic meeting fixture.
- `bin/prepare-youtube-set.sh`: prepare the three-song sync fixture set.
- `bin/roundtrip-synthetic-meeting.sh`: full recorder-to-publisher roundtrip on
  the synthetic meeting.
- `bin/record-three-songs.sh`: record the three-song player scenario.
- `bin/ci-e2e.sh`, `bin/ci-e2e-mute.sh`, `bin/ci-e2e-rejoin.sh`: CI-style local
  integration scenarios.

## Typical flows

Bring up the local stack and create a room:

```bash
./cassini-lab/bin/up.sh
CALL_URL="$(./cassini-lab/bin/create-room.sh --name "Local room" | tail -n1)"
```

Run the local smoke flow:

```bash
./cassini-lab/bin/smoke.sh
```

Run the baseline CI-style integration scenario:

```bash
./cassini-lab/bin/ci-e2e.sh
```

## Notes

- The current implementation is intentionally thin and delegates to `test/bin/`.
- `test/` remains the implementation home for the lab harness, fixtures, and
  local stack internals.
