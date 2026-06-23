---
shaping: true
---

# D-288.1 — Private 1:1 Playback Validation

## Local harness used

- Nextcloud/Talk: `http://192.168.252.21:28080`
- Operator: `http://192.168.252.21:4000`
- Viewer: `http://192.168.252.21:8765`

## Commands

```bash
./bin/cassini dev play-private --nextcloud-host 192.168.252.21 --scaffold-only
./bin/cassini dev play-private --conversation synthetic --duration 60
./bin/cassini dev play-private --conversation admin --duration 60
```

## Expected behavior

- Scaffold creates/reuses:
  - `cassini-erlich`
  - `cassini-monica`
  - synthetic 1:1: `cassini-erlich` ↔ `cassini-monica`
  - admin 1:1: `admin` ↔ `cassini-erlich`
- Synthetic playback:
  - starts recording through Nextcloud Talk
  - waits for recording active before media starts
  - publishes authenticated Erlich + Monica media
  - stops recording through Nextcloud Talk
- Admin playback:
  - starts recording as `admin`
  - keeps admin audio muted
  - publishes authenticated Erlich media
  - stops recording through Nextcloud Talk

## Validation results

- Synthetic target: operator job `01KT1JNGAY629B9G1X55NYPA9N`
  - state: `succeeded`
  - viewer transcript showed non-empty transcription, including: `V P eight, Opus, F F M Peg, and J Son.`
- Admin target: operator job `01KT3M99QBA9T3E4T1RS4ENKQA`
  - state: `succeeded`
  - viewer transcript showed non-empty transcription, including: `Exactly. Finally, somebody in this room respects systems thinking...`

## Common failure modes

- Missing scaffold state: rerun `cassini dev play-private --scaffold-only`.
- Stale host in scaffold state: rerun scaffold with the intended `--nextcloud-host`.
- Synthetic password mismatch: set `CASSINI_PLAY_SCAFFOLD_PASSWORD` to the scaffolded value or rerun scaffold.
- Recording backend unavailable: Talk recording can start but no operator job will complete.
- Admin 1:1 not visible to Erlich: rerun scaffold; scaffold now fills the admin conversation from Erlich's side too.
