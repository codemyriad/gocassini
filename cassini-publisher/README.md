# Cassini Publisher

`cassini-publisher` is the orchestration and packaging layer of the Cassini
suite.

It owns the opinionated flows that turn lower-level tool outputs into something
you can hand to humans:

- transcribe one meeting and optionally bundle a browsable static site,
- export a directory of meeting artifacts into one publishable meeting library.

It does not:

- capture calls live: that is `cassini-go-recorder`,
- run ASR or generate transcript artifacts: that is `cassini-transcriber`,
- render the browser UI at runtime: that is `cassini-viewer`.

## Tools

- `bin/process-meeting.sh`: transcribe one meeting MKV into an artifact
  directory, with optional static viewer bundle output.
- `bin/export-static-meetings.sh`: package one or more existing artifact
  directories into a static meeting library using the viewer build.

## Typical flows

Transcribe one meeting and build a static site for it:

```bash
./cassini-publisher/bin/process-meeting.sh \
  --input /path/to/meeting.mkv \
  --output-root /tmp/cassini-results \
  --bundle-viewer
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
