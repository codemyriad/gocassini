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
- `bin/prepare-showcase-meeting.sh`: generate the more natural showcase fixture.
- `bin/roundtrip-showcase-meeting.sh`: full recorder-to-publisher roundtrip on
  the showcase meeting.
- `bin/ci-e2e.sh`: baseline CI-style local integration scenario.

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

Generate the showcase meeting fixture:

```bash
./cassini-lab/bin/prepare-showcase-meeting.sh
```

Run the baseline CI-style integration scenario:

```bash
./cassini-lab/bin/ci-e2e.sh
```

## Notes

- The current implementation is intentionally thin and delegates to `test/bin/`.
- `test/` remains the implementation home for the lab harness, fixtures, and
  local stack internals.
- The current default synthetic fixture is still harness-oriented. Use the
  showcase meeting wrappers when you want something closer to a demo or cleanup
  evaluation sample.
- More specialized harness flows such as the old synthetic fixture roundtrip,
  the three-song capture flow, and the extra CI variants remain available under
  `test/bin/`.
