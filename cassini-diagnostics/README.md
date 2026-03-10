# Cassini Diagnostics

`cassini-diagnostics` is the supporting diagnostics surface of the Cassini
suite.

It owns the artifact inspection, verification, remux, and compatibility
recovery entry points that sit beside the main happy path:

`Recorder -> Transcriber -> Readable -> Publisher -> Viewer`

This package is intentionally not part of the primary meeting flow. It exists
for debugging, CI validation, recovery, and compatibility work.

## Tools

- `bin/inspect-artifact.sh`: inspect a meeting MKV, session artifact, session
  directory, or legacy `.csr` archive.
- `bin/remux-session.sh`: rebuild a meeting MKV from a session artifact.
- `bin/upgrade-meeting-mkv.sh`: upgrade an older meeting MKV plus legacy report
  into the current MKV contract.
- `bin/verify-session-artifact.sh`: validate that a produced recording has a
  usable session artifact beside it.
- `bin/verify-av-drift.sh`: compare paired audio/video elapsed time in a final
  MKV.
- `bin/verify-sync-from-report.sh`: compare final MKV stream offsets against the
  legacy recorder report.

## Typical flows

Inspect a recording or artifact:

```bash
./cassini-diagnostics/bin/inspect-artifact.sh /tmp/meeting.mkv
```

Rebuild a meeting MKV from session artifacts:

```bash
./cassini-diagnostics/bin/remux-session.sh \
  --session /tmp/sessions/<session_id>/session.json \
  --output /tmp/session-artifact-remux.mkv
```

Validate recorder output:

```bash
./cassini-diagnostics/bin/verify-session-artifact.sh \
  --final-output /tmp/meeting.mkv
```

## Notes

- The recorder-native implementations still live in `cassini-go-recorder/cmd/`.
- The verification scripts still live under `test/bin/` because they are shared
  with the lab and CI harness.
- `verify-sync-from-report.sh` still depends on the legacy external recorder
  report, so it is a compatibility tool rather than part of the long-term MKV
  contract.
