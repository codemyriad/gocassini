# Cassini Publisher

`cassini-publisher` is the orchestration and packaging layer of the Cassini
suite.

It owns the opinionated flow that turns existing Cassini meeting outputs into
something you can hand to humans:

- export a directory of meeting artifacts or processed portable meetings into
  one publishable meeting library.

It does not:

- capture calls live: that is `cassini-go-recorder`,
- run ASR or generate transcript artifacts: that now happens inside
  `cassini build` via the native Go pipeline,
- render the browser UI at runtime: that is `cassini-viewer`.

## Tools

- `bin/export-static-meetings.sh`: package one or more existing artifact
  directories into a static meeting library using the viewer build.

## Typical flows

First build meetings with the product CLI, then publish them:

```bash
./bin/cassini build /path/to/meeting.mkv --out /tmp/meetings/weekly-sync.meeting
./cassini-publisher/bin/export-static-meetings.sh \
  --source-dir /tmp/meetings \
  --output-dir /tmp/static-meetings
```

Publish an existing artifact library:

```bash
./cassini-publisher/bin/export-static-meetings.sh \
  --source-dir /path/to/meeting-artifacts \
  --output-dir /tmp/static-meetings
```

## Notes

- The publisher is intentionally thin. It orchestrates existing component
  contracts instead of reimplementing them.
- The static export implementation still lives in `cassini-viewer/scripts/`
  because it packages the viewer build output itself. The publisher provides the
  preferred suite-level entry points.
