# Source capture transfer and recovery

D-733: https://linear.app/code-myriad/issue/D-733/simplify-source-capture-with-durable-input-identity-and-immutable

This supersedes the whole-capture transport and reload-adoption design in
source-audio-capture.md. The native companion still injects the payload through
Talk's additional-scripts event. Capture still follows Talk's recording status,
the participant's live outgoing track, and the administrator switch.

## Recording identity and local sessions

The authenticated browser asks `GET /operator/capture/recording?room=TOKEN`
for the unique live operator recording. The endpoint verifies room membership
and returns the job ID; unavailable or ambiguous recordings do not authorize a
new session. Startup races retry through the existing recording-status poll.
The browser creates a random session ID and stores both IDs in its OPFS
checkpoints and final manifest.

A reload creates a new directory and session for the same recording. It never
reopens or extends the previous document's audio. Both sessions remain buffered
while recording is active, and are uploaded when it stops. Web Locks prevent a
page from uploading another page's live directory. A different account cannot
upload the previous account's buffers. A failed upload remains buffered with
bounded exponential retry delay; refreshing the page can retry immediately.

Legacy OPFS manifests remain readable and use the legacy multipart endpoint.
They also upload independently after a reload. The legacy endpoint is retained
for installed clients and existing buffers; its replacement machinery is not
used by the new protocol. An older server that does not advertise
`uploadProtocol: 2` continues to receive legacy uploads.

## Bounded, immutable transfer

`/operator/capture/transfer` takes `room`, `recording`, and `session` query
parameters. The authenticated account defines ownership; supplied participant
IDs cannot choose a storage directory. The server verifies that the recording
belongs to the room and checks the caller's current room membership.

1. GET returns acknowledged piece hashes and whether the session is committed.
2. POST with `piece=SHA256` accepts a single multipart file, at most 4 MiB.
   The server verifies the hash and writes, syncs and renames the piece before
   acknowledgement. Identical retries are idempotent.
3. POST with `op=commit` accepts a JSON sidecar and a map from each segment
   filename to its ordered piece hashes. These are transport byte ranges of one
   media file, not independently playable MediaRecorder chunks.
4. The server validates the complete manifest, reserves assembly capacity,
   verifies the pieces again, assembles and syncs the audio, then atomically
   exposes `capture.json`. A committed manifest cannot be replaced by a
   different manifest under the same session identity.
5. The accepted response names the session directory and accounts for the
   stored segment count and bytes. Only then does the browser remove its local
   directory. A lost response is recovered through inventory and an idempotent
   commit retry.

Incomplete sessions and committed captures share the existing
`room/owner/session` hierarchy, so per-owner/global quotas, the free-space
floor, and retention cover both. The session limit remains 512 MiB, with at
most 1024 referenced pieces. Assembly temporarily needs space for the pieces
and the assembled audio; admission reserves that extra space. Pieces are
removed after commit. A failed assembly removes its partial outputs while
keeping acknowledged pieces for retry.

## Durable rebuild notification

The committed sidecar contains a server-issued receipt ID. Recording the
receipt and incrementing the job's upload generation happen in one SQLite
transaction. Replaying a receipt cannot increment the generation twice.
The scheduler reconciles committed sidecars before considering rebuilds, so a
process crash or database failure after file promotion does not strand audio.

Build input digests include audio contents and placement metadata. Equal-size
replacement audio on the legacy route, and metadata-only corrections, therefore
cannot be mistaken for already-built inputs. Quiet-period coalescing, generation
tracking and the existing three-rebuild ceiling remain.

The operator passes `--source-audio-recording JOB_ID` to the recorder.
New sessions match that identity directly. Old captures still use the existing
window-based association. Selection continues to refuse ambiguous overlapping
sessions of one participant; an explicit recording ID does not make simultaneous
microphones safe to mix twice.

## Separate timing and storage workers

`capture-worker.js` forwards outgoing encoded frames and samples timing anchors.
It delegates all OPFS work to `capture-storage-worker.js`. Slow synchronous
writes and flushes cannot block forwarding. The storage worker serializes
messages and retains the existing alternating recovery checkpoints.

The encoded transform still needs its existing startup/recovery protections.
This change does not replace RTP timing with `getStats()`: browser statistics
would need measured support and drift accuracy across mute, device changes and
long calls before making that substitution.

## Boundaries

Recording identity solves association, not audio alignment. Placement still
uses client wall time for offset; unsynchronized clocks remain a known
limitation pending measured offset refinement. The original recorded track
remains the fallback, and playback and transcription consume the same rendered
splice. Guest/mobile capture, participant erasure controls, and browser-storage
eviction are not solved by the transfer protocol.

Tests cover content/metadata digest changes, receipt replay/recovery, hash
verification, owner/recording isolation, immutable manifest conflicts, browser
reloads, interrupted acknowledgements, and a deliberately stalled storage worker.
The installed ExApp server leg retains legacy compatibility coverage; the real
Talk browser leg exercises the new protocol through AppAPI/HaRP and reconciles
both reloaded sessions against the stored audio.
