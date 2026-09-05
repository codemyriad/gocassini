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

`/operator/capture/transfer/{room}/{recording}/{session}` identifies a session.
Identity is in the path because AppAPI drops query parameters when rebuilding
multipart POSTs. The authenticated account defines ownership; supplied participant
IDs cannot choose a storage directory. The server verifies that the recording
belongs to the room and checks the caller's current room membership.

1. GET returns acknowledged piece hashes and whether the session is committed.
2. POST to `.../{SHA256}` accepts a single multipart file, at most 4 MiB.
   The server verifies the hash and writes, syncs and renames the piece before
   acknowledgement. Identical retries are idempotent.
3. POST to `.../commit` accepts a JSON sidecar and a map from each segment
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
It delegates all OPFS work to `capture-storage-worker.js` through a
MessageChannel. Both workers are launched
from the Talk page, avoiding nested-worker restrictions in proxy response CSP.
Slow synchronous writes and flushes cannot block forwarding. The storage worker serializes
messages and retains the existing alternating recovery checkpoints.

The encoded transform still needs its existing startup/recovery protections.
This change does not replace RTP timing with `getStats()`: browser statistics
would need measured support and drift accuracy across mute, device changes and
long calls before making that substitution.

## Clock measurement and placement correction

The recording-identity GET returns millisecond `serverReceiveWallMs` and
`serverSendWallMs` timestamps after checking the authenticated account's room
membership. An authorized request with no unique live recording still returns
these timestamps with HTTP 409, so stopping a recording permits a final clock
probe without authorizing another capture. Responses are never cached.

The browser records its send/receive wall times and monotonic elapsed time at
startup, during the existing recording-status polls, and before final upload.
`clockSamples` is retained in OPFS recovery checkpoints and the immutable
manifest: the first observation plus a rolling tail, bounded to 128 samples.
Older servers and legacy buffers remain readable without measurements.

Intake estimates client-ahead skew using the four-timestamp offset calculation
in [RFC 5905 section 8](https://www.rfc-editor.org/rfc/rfc5905#section-8):
`((clientSend - serverReceive) + (clientReceive - serverSend)) / 2`.
Half the round trip after subtracting server processing, plus 2 ms for timestamp
quantization, bounds each observation's path-asymmetry uncertainty. The lowest
uncertainty observation supplies the constant correction. A usable observation
must have at most 250 ms uncertainty (nearly 500 ms of network/proxy round trip).
The nearest usable observation must reach within 90 seconds of each end of the
capture. Probes entirely outside the session, allowing five seconds for startup
and stop requests, do not select its offset or contribute to its stability check.
This prevents an inherited recording-identity probe from rejecting a later
session that has fresh coverage.

Variation is measured separately: the offset spread among probes within 50 ms
of the fastest network round trip must not exceed 150 ms. All relevant probes,
including slower ones, also check for offset disagreements outside their own
and the selected observation's uncertainty bounds plus 50 ms. Those conflicts,
wall-clock steps during a probe, missing coverage, or excessive uncertainty
make the capture unreliable. A slow response consistent with its delay bound
does not become clock movement merely because its bound is large.

The 250 ms admission limit accommodates remote participants but permits a larger
placement error. Low offset scatter does not remove the uncertainty of a stable
asymmetric path. Uncertainty describes the selected observation; variation
describes the fastest retained observations across the session. Neither proves
what the clock did between probes. These are admission limits, not a claim of
sample-accurate alignment. Tests replay the three timing traces from real-proxy
CI run 33964161554 with additional 130/300/400 ms network RTT, both symmetric
and fully asymmetric, and separately test jitter, steps and insufficient coverage.

The operator subtracts that correction from every call, segment, anchor and mute
wall timestamp **once**, after hashing the original immutable request. Audio
bytes and RTP timestamps stay intact. It stores the raw observations alongside
server-owned `clockStatus`, `clockCorrectionMs` (positive means client ahead),
`clockUncertaintyMs` (selected observation's asymmetry bound, including offset
rounding), and `clockVariationMs` (spread among the fastest probes, omitted when
zero). Uncertainty is rounded upward to three decimal places in milliseconds.
Adding the correction back recovers the original wall timestamps. Request
replays compare the original digest and cannot apply the correction twice.

Operator logs use `capture clock:` with owner, recording, session, status,
`client_ahead_ms`, uncertainty, variation and sample count for corrected captures;
unreliable captures log the available estimate, refusal reason and explicit
`action=retain_recorded_audio`. The real-proxy CI harness prints these decision
logs and asserts `clockStatus == "corrected"` for every committed session.
Unreliable audio is retained and acknowledged,
but excluded from both rebuild input selection and recorder ingestion; the
recorder logs why it retained the recorded track. Unmeasured legacy sessions
keep their existing placement behavior. No upload-time skew is substituted for
missing capture-time observations.

## Boundaries

Recording identity solves association. Measured stable client clock skew is
corrected into operator wall time before placement. Operator and recorder must
share a synchronized server clock (the ordinary same-host deployment does).
Separate server hosts with unsynchronized clocks, clock steps between probes,
and encoder/capture latency still require further alignment work. High-latency
or poorly observed sessions conservatively retain the recorded track; old
unmeasured captures still rely on synchronized client clocks. The original recorded track
remains the fallback, and playback and transcription consume the same rendered
splice. Guest/mobile capture, participant erasure controls, and browser-storage
eviction are not solved by the transfer protocol.

Tests cover content/metadata digest changes, receipt replay/recovery, hash
verification, owner/recording isolation, immutable manifest conflicts, browser
reloads, interrupted acknowledgements, and a deliberately stalled storage worker.
The installed ExApp server leg retains legacy compatibility coverage; the real
Talk browser leg exercises the new protocol through AppAPI/HaRP and reconciles
both reloaded sessions against the stored audio.
