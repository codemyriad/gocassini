# Same account in multiple browsers

## Problem and implemented plan

A Nextcloud account is speaker attribution, but it is not a unique audio source.
Two browsers can join the same room under that account at the same instant and
send different audio. Previously, capture paths could collide, selection treated
their overlapping uploads as ambiguous, and recorder/transcription grouping could
merge their tracks.

The implementation separates these identities throughout the pipeline:

1. Generate a random `captureId` per browser capture. Include it in OPFS names,
   upload storage paths, and expected-upload database keys. Preserve it when a
   reload adopts buffered audio. Existing captures without an ID keep their legacy
   paths and retry behavior.
2. Read Talk's **public** `hello.sessionid` and stamp it onto each segment as
   `sessionId`. A changed signaling session rotates the segment. Never collect
   `resumeid`, which is a credential. The registration also reports the current
   session, but segment metadata is authoritative for pre-reload audio.
3. Preserve the recorder's corresponding remote session ID on logical tracks,
   remux plans, and MKV stream metadata (`remote_session_id`). Keep participant
   identity unchanged for attribution.
4. Select and render audio by authenticated account plus public session ID, within
   the existing room and recording-window constraints. Give each session its own
   mix replacement and transcription WAV. Suppress duplicate transcription only
   within that session. A missing upload leaves that session's server recording
   in use and does not suppress a sibling browser's speech.
5. Show capture IDs for uploads and session IDs for build use in Cassini. Wait for
   each registered capture independently, using the existing fixed two-minute
   deadline. Store build evidence per attempt.

Session metadata is a correlation key, not authorization: the server still checks
Nextcloud authentication and room membership and joins only against tracks owned
by that authenticated account. A retry cannot relabel already-stored segment
identity. Migration 0010 preserves existing expectation rows while extending
uniqueness with the capture ID.

## Compatibility

The updated recorder and capture companion must both be installed. Reload Talk
pages after updating the companion. Legacy captures can still be used for one
unambiguous recorded account/session; missing session identity cannot safely
separate an older concurrent-browser recording. Ambiguous captures retain the
recorded audio. Within a single session, overlapping competing captures continue
to be refused.

## Verification

Automated coverage includes:

- Two uploads with identical account and start time but different capture IDs;
  neither overwrites the other, both remain selected, and both registrations wait
  independently. A retry that changes segment session identity is rejected.
- Two recorded tracks with one participant identity and different remote session
  IDs survive the recorder and MKV probe as separate tracks.
- Real ffmpeg audio tests use different tones for concurrent sessions. Both source
  signals reach the mix and distinct transcription inputs; with one upload missing,
  the other session's recorded signal remains. Speaker attribution stays shared.
- Browser tests retain capture identity across reload while recording each
  segment's current public session ID, including a microphone switch.
- Operator UI tests distinguish multiple sessions under one account.

A local Nextcloud run on 2026-09-10 used two independent Chromium processes logged
in as `alice`, with different synthetic microphone audio. Both started captures at
exactly the same millisecond. One browser switched microphones and reloaded.
Both uploads were accepted, both expected captures became stored, and build 1
published with three independently matched session reports: 29.640 seconds from
one browser, and 17.580 plus 9.801 seconds from the browser that reloaded. All three
reported inclusion in meeting audio and use for per-participant transcription.
A real browser check also verified the capture/session labels in Cassini.

Local evidence is in `harness/runtime/same-user/` (`browser/result.json`,
`detail.json`, `ui.txt`, and `participant-audio.png`). The recording job is
`01M25EJNVM1BEDM22CSA9BJ8F3`, room `teq8ywyf`. Synthetic tones verify routing and
mixing; they do not establish speech-recognition accuracy.
