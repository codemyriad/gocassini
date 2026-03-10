# Cassini Recorder

`cassini-recorder` is the recorder product surface of the Cassini suite.

It owns the capture-facing entry points that turn a live Talk room into a
meeting artifact.

The current implementation is provided by `cassini-go-recorder`, but this
package is the preferred suite-level surface when you want to record meetings
without thinking about the implementation package layout.

## Tools

- `bin/record-talk.sh`: convenience wrapper for live Talk capture.
- `bin/simulate.sh`: convenience wrapper for simulate mode.

## Typical flows

Record a live room:

```bash
./cassini-recorder/bin/record-talk.sh \
  --call-url "https://cloud.example.com/call/<ROOM_TOKEN>" \
  --name "CassiniRecorder" \
  --output /tmp/meeting.mkv
```

Generate a simulated archive:

```bash
./cassini-recorder/bin/simulate.sh \
  --output /tmp/gocassini.csr
```

## Notes

- The wrappers are intentionally thin and delegate to `cassini-go-recorder`.
- Lower-level inspection, remux, upgrade, and validation flows live under
  `cassini-diagnostics`.
