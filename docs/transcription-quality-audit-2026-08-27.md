# Transcription quality and timing audit — 2026-08-27

## Outcome

The short-interjection problem is not one bug. The audit found four independent
timing/presentation defects and one important false-positive class:

1. live capture timestamped an RTP packet **before** the blocking read;
2. post-processing applied a stream's initial timestamp twice;
3. completed Silero VAD segments were left queued for whole recordings, allowing
   the detector's circular buffer to grow and overflow repeatedly;
4. correctly timed words were packed into long per-speaker blocks that could not
   represent a nested interjection, and the viewer highlighted the first overlap;
5. very quiet leakage on an inactive track can produce a plausible but false
   one- or two-word interjection.

The first four now have focused fixes and regression coverage in the worktree.
The fifth should be handled by an amplitude-ranked verification pass rather than
an unconditional energy threshold.

## Corpus inspected

The production inventory on `george` contains:

- 120 current `.meeting` transcript bundles;
- 43 retained `.run` bundles with raw multitrack MKV/RTP capture data;
- 194 audio streams in those retained runs;
- 114 GB of older recordings under `/mnt/data/cassini/recordings`;
- five March portable meetings used for compact blind-audio samples.

Across the 120 current transcripts, 103 contain cross-speaker overlapping
segments. Ninety-eight contain at least one short segment nested inside another
speaker's block, for 332 nested segments in total. Of 9,593 segments, 4,468 are
at least 15 seconds long and 3,459 hit the 60-word grouping limit. This makes the
ordering defect common rather than exceptional.

## Confirmed defects

### 1. Packet receive time captured before a blocking read

`internal/talk/recorder.go` called `time.Now()` and then blocked in
`TrackRemote.ReadRTP()`. After mute, DTX, or an otherwise sparse interval, the
first resumed packet inherited the time from before the wait. The rtplog's
`RecvMonoNS` later becomes the reconstructed media PTS, so a brief resumed turn
can move back to the previous burst or the first and second packet can be
separated by an artificial discontinuity.

The timestamp is now captured only after a successful read. A channel-blocked
unit test pins that order without a timing sleep.

### 2. Initial stream offset applied twice

The per-track decode filter was:

```text
aresample=async=1:first_pts=0,adelay=<stream.start_time>
```

For Cassini's multitrack MKVs, `aresample=...:first_pts=0` already materializes
the initial packet PTS as silence. `adelay` applied the same offset again. This
affected both the per-speaker ASR input and `meeting.webm`.

In the latest retained run, waveform events moved by exactly the affected
stream's start time:

| Event | Raw track | Old filtered track | Extra shift |
|---|---:|---:|---:|
| Silvio, short word | ~1093.230 s | ~1094.368 s | 1.138 s |
| Alex, short word | ~2159.372 s | ~2160.334 s | 0.962 s |
| Chima, backchannel | ~2314.385 s | ~2319.252 s | 4.867 s |

Thirty of 43 retained runs contain at least one affected stream; the maximum
observed extra shift is 5.669 seconds. Later rotated tracks can also report
`stream.start_time=0` while their first packet PTS is much later, so the metadata
field is not a safe authority.

The filter now relies on packet PTS alone:

```text
aresample=async=1:first_pts=0
```

A real two-stream synthetic fixture asserts that a ten-second input offset is
neither collapsed nor doubled, including the first significant waveform sample.

### 3. VAD queue growth and final-window handling

The initial audit incorrectly attributed this failure to five-second calls not
being divisible by Silero's 512-sample window. Upstream sherpa-onnx retains that
remainder between `AcceptWaveform` calls, so it does not accumulate the claimed
96 ms/minute drift. Its `Flush` method does not evaluate the one final partial
window, however, and—more importantly—completed speech segments were left queued
for the entire recording. On the 51-minute controlled reproduction the old path
repeatedly grew its circular buffer from 960k to 1.92M, 3.84M, and 7.68M samples.

The production recognizer now feeds exact 512-sample calls, zero-pads only the
last partial window, and drains completed segments about every five seconds.
The controlled old-code/current-`stt.go` hybrid removed the false Ivan turns and
increased the same meeting from 6,618 to 7,151 words; this isolates the VAD-path
change, while the upstream source review identifies periodic draining—not a
cumulative remainder drift—as the defensible cause.

### 4. Segment ordering and viewer selection

Segments were assembled independently per speaker and only then sorted as
opaque blocks. If speaker A had words at 1–20 seconds and B spoke at 5–6
seconds, the output remained `A(1–20), B(5–6)`. No segment-level renderer can
show that in conversational order.

The merge now flattens canonical words, stable-sorts them by word start, and
rebuilds turns on speaker changes while retaining the 1.5-second/60-word limits.
The example becomes `A / B / A` without losing or duplicating words.

Separately, the viewer used the first active overlapping display block. It now
selects the latest-starting active block, so playback highlights the nested
speaker and returns to the containing turn at the interjection boundary.

### 5. False interjections from quiet-track leakage

One amplitude-ranked Aug-20 sample contained two short phrases attributed to
Ivan inside a Chima segment. In those windows Chima's track peaked around
-5 to -7 dB, while Ivan's peaked around -44 to -46 dB (Ivan's normal speech in
that meeting peaks near -7.5 dB).

Blind Gemini 3.7 passes transcribed one continuous Chima sentence from both the
mix and Chima's isolated track. On Ivan's isolated track Gemini reported no
intelligible direct speech, only distant bleed. A local Gemma 4 check agreed.
This is strong evidence that the nested Ivan phrases are a false ASR turn caused
by leakage/noise, not a real quiet interjection.

This should not become a blanket “drop quiet words” rule: legitimate speakers
have different gains. The robust rule is relative and reviewable—rank short
turns whose owning track is near its noise floor while another track is much
stronger, then verify them against the mix and isolated tracks.

### 6. Thin attributed output was duplicated by the mixed fallback

When fewer than ten attributed words survived, the fallback transcribed the
complete mix and then concatenated that full hypothesis with the surviving
participant words. A nine-word partial result could therefore appear twice and
alternate rapidly between its participant and `merged` speaker labels. The two
hypotheses are now exclusive: the mix replaces the attributed result only when
it contains strictly more words; a tie retains attribution. Regression tests
cover empty, shorter, tied, and overlapping fallback output.

## Audio-model experiment

`OPENROUTER_API_KEY` was available in the environment. The primary blind judge
was therefore `google/gemini-3.7-flash` through OpenRouter, whose live model
catalog reports audio input support. George's private llama.cpp deployment of
Google's audio-native Gemma 4 12B was retained as a local secondary comparison.
The selected WAVs were 7–14 seconds at 16 kHz mono.

The useful protocol was:

- audio content before the text instruction;
- no existing transcript in the blind pass;
- neutral speaker labels and relative timestamps;
- explicit overlap, uncertainty, noise, clipping, echo, and leakage fields;
- a second pass with participant/project spellings only when testing vocabulary.

Gemini 3.7 was materially stronger than local Gemma 4 on these clips: it heard
“All morning” exactly, found heavily masked overlaps, and avoided the suspicious
stock phrases. The seven portable blind calls cost $0.014485. They ran in two
parallel batches whose total wall time was about 20 seconds. Adding three
mix/isolated-track adjudications and one vocabulary A/B brought spend to
$0.017186. Two later repeat adjudications of the vocabulary clips added
$0.003942. One final three-track, 16.7-second adjudication cost $0.002635 and
caught a mismatched audio/transcript pairing before it could contaminate the
evaluation, for $0.023763 across this investigation.

Even Gemini was not treated as unquestioned truth. It sometimes changed speaker
labels when the same voice resumed, missed a genuine overlap, or assigned a
plausible but wrong phrase to muffled speech. Model judgments were accepted only
when corroborated by waveform energy, isolated tracks, or the exact artifact's
embedded transcript.

The seven portable challenge clips produced these adjudications:

| Clip (digest offset) | Blind-model result against the same portable artifact |
|---|---|
| Mar 6, 12:22–12:36 | Correctly recovered the A/B exchanges and both overlaps, including the rapid “Okay” handoff. It did not emit the nested stock phrase present in Cassini. |
| Mar 6, 15:54–16:18 | Heard only Alex's main voice and did not support a 660 ms Chris segment containing Georgian-character garbage. |
| Mar 9, 12:38–13:02 | Correctly recovered the A/B/A sequence, “All morning,” and the continuation “Because for me it seems stuck.” |
| Mar 10, 11:16–11:44 | Found both real overlaps in the dentist/drink exchange; did not support nested “Thanks for watching!” segments. |
| Mar 11, 02:18–02:46 | Heard the main production sentence and a later quiet second-speaker turn as “Turn my video on,” not Cassini's “Thank you for watching.” It missed the earlier simultaneous utterance. |
| Mar 11, 16:41–17:06 | Heard a short response and a following “Uh” but split one participant into multiple anonymous speakers. The tiny `Um` cannot be safely attributed from the mix alone. |
| Mar 11, 23:47–24:16 | Detected a heavily masked third-speaker interjection at the same interval as Ivan's item, supporting a real overlap while leaving its words uncertain. |

The local Gemma comparison was useful but weaker. Across six blind clips it ran
at a mean 8.810 seconds/clip, misheard “All morning” as “Oh, morning,” missed
several real overlaps, invented backchannels, and once claimed severe clipping
that PCM statistics contradicted. This comparison predates the GPU-only
operational constraint below; it is evidence, not a recommendation to run
local CPU inference again.

### CUDA Parakeet baseline

The current recorder worktree was also built against sherpa-onnx's CUDA 12 /
cuDNN 9 runtime on `george` and run with the fp32 Parakeet 0.6B v3 model. A
seven-stream fixture held all seven challenge clips, so one recognizer load
covered about 85 seconds of aggregate audio. With ASR on CUDA, one-thread CPU
Silero VAD, stream concurrency one, an 8 GiB host-memory cgroup limit, and swap
disabled for the job, the warm-cache end-to-end build completed in 6.08 seconds
(about 14x aggregate real time). Peak GPU memory was 3.64 GiB, peak host RSS was
about 1.3 GiB, and the job used no swap.

The baseline was accurate on clean speech but failed precisely where a targeted
verifier adds value:

| Clip | CUDA Parakeet result versus blind Gemini adjudication |
|---|---|
| Mar 6, clip `_0742` | Recovered the fast A/B exchange and avoided the stock-phrase hallucination, but heard `RAM spikes` as `gram spikes` and lost words in overlap. |
| Mar 6, clip `_0961` | Preserved `LinkedIn` and `brand agent`, but omitted the earlier low/muffled question. |
| Mar 9, clip `_0765` | Reduced “Codex this morning? / All morning” to “Codex. Yeah” and changed “seems stuck” to “seems tough.” |
| Mar 10, clip `_0684` | Heard the dentist opening but lost “Way cheaper” and the later drink exchange. |
| Mar 11, clip `_0145` | Rendered the quiet “Turn my video on” approximately as “Do my video code.” |
| Mar 11, clip `_1008` | Recovered the main comment only partially and omitted short responses. |
| Mar 11, clip `_1434` | Recovered the main shoulder exchange but not the heavily masked third speaker. |

Forcing Silero VAD onto CUDA as well was counterproductive: the first 11-second
run took 51 seconds because 32 ms VAD windows became tiny GPU dispatches. It
still proved that CUDA was active (3.69 GiB peak VRAM), but the production-safe
configuration is CUDA for speech recognition and the small one-thread CPU VAD.
The default CUDA host-thread count is now one, avoiding a large CPU thread pool
alongside GPU inference.

In a Gemini vocabulary A/B, approved participant spellings changed “Sylvia” to
“Silvio” and retained `LinkedIn`/`brand agent`, but unrelated wording did not
improve and one phrase became worse. Vocabulary is valuable for names and jargon,
not a substitute for acoustic evidence.

### Parallel Vast.ai, Voxtral, and hotword matrix

The initial exploratory matrix used independent Vast.ai RTX 5060 Ti 16 GiB
workers so model installation and decoding ran in parallel without contending
with the production GPU. Speech recognition was CUDA-only; each runner failed
if CUDA was absent, and the Parakeet runner additionally required a live NVIDIA
process allocation after recognizer initialization. The private WAVs and model
runtime were copied only to the owned instances. Results were hash-checked
locally and the instances were destroyed; the final account query returned no
instances.

The useful results were:

| Model/condition | Accuracy against Gemini on the two judged clips | RTF | Peak CUDA memory | Finding |
|---|---:|---:|---:|---|
| Voxtral Mini 4B Realtime, 480 ms delay | 27.7% micro WER; 72.3% aligned-token recall | 0.435 | 8.85 GiB reserved | Better coverage than offline Voxtral, but a flat transcript with no speaker/overlap times. |
| Voxtral Mini 4B Realtime, 2400 ms delay | 27.7% micro WER; 75.9% aligned-token recall | 0.420 | 8.85 GiB reserved | Recovered more of the muffled opening handoff, but did not dominate the 480 ms result on the fast exchange. |
| Voxtral Mini 3B offline | 45.8% micro WER; 54.2% aligned-token recall | 0.109 on the judged pair; 0.050 across eight single-track clips | 9.26 GiB reserved | Much faster, but omitted competing speech and cannot be trusted as an attribution judge. |
| Parakeet modified beam, no hotwords | Same 43 words as every hotword condition | 0.086 decode after CUDA warm-up | about 3.46 GiB | Lost the important tail of the RAM-spike exchange. |
| Parakeet modified beam, hotword scores 0.5/1.0/1.5 | Byte-for-byte identical transcript text at all scores | 0.084–0.086 decode | about 3.46 GiB | No vocabulary benefit; do not enable. |

The first Parakeet greedy condition also exposed a benchmark-order trap: its
first CUDA decode included a roughly 77-second one-time kernel/runtime warm-up,
while all later conditions decoded the same 19.7 seconds of VAD speech in about
1.7 seconds. The reusable runner performs an unscored warm-up before timed
conditions, so a decoder choice is not confused with process cold start. The
already measured resident production path remains the relevant speed baseline.

Voxtral Realtime recovered more words, but neither delay emitted speakers,
timestamps, or overlap markers; explicit overlap recall was therefore 0/5
against Gemini's judged intervals. Both delays reduced the 4.5-second disputed
window to only `I`. It is not a replacement for the primary recognizer. If a
self-hosted second opinion is useful, it should be limited to the top
waveform-mined 10–20 second windows on a resident 12+ GiB GPU; the delay choice
is not stable enough to justify a production default from this small sample.

The isolated-track experiment was even more decisive. The disputed Chima track
had mean/peak levels of -23.0/-1.2 dBFS, while Ivan's bleed-only track was
-54.0/-15.1 dBFS. In the shorter window the corresponding levels were
-21.4/-0.8 and -42.1/-20.7 dBFS. Despite those 21–31 dB mean-level gaps and
Gemini's no-direct-speech adjudication, offline Voxtral emitted `I'm sorry` and
`Yeah!` on Ivan's tracks. Its three-track prompt then asserted that Ivan spoke
for 0–15 seconds, and on the short clip returned placeholder text saying it
would need to listen to the audio. Multi-audio Voxtral prompting is therefore
not safe for speaker correction. Relative waveforms select and constrain the
review window; Gemini remains the stronger adjudicator.

A later clean-room rerun exercised the committed orchestration path end to end:
three workers launched concurrently (two RTX 5060 Ti workers for offline suites
and one RTX 5070 Ti worker for the realtime grid), all returned exit code zero,
their results were collected locally, and exact-label recovery confirmed that
all three instance IDs were absent after teardown. The complete lifecycle took
855.8 seconds and the recorded estimate was $0.07766 at $0.3267/hour aggregate.
This repeat exposed useful host/runtime variance and makes the preliminary RTFs
above inappropriate as universal speed claims:

| Clean-room condition | Gemini-reference result | Aggregate decode RTF | Peak CUDA reserve |
|---|---:|---:|---:|
| Offline baseline, eight tracks | 45.8% WER / 54.2% aligned recall on the two positive reference clips; 2/2 bleed-only tracks falsely non-empty | 0.069 | 9,062 MiB |
| Offline vocabulary prompts, four cases | 62.7% WER / 37.3% aligned recall on the two positive reference clips | 0.247 | 9,310 MiB |
| Realtime, 240 ms | 26.5% WER / 77.1% aligned recall | 1.309 | 8,852 MiB |
| Realtime, 480 ms | 27.7% WER / 72.3% aligned recall | 1.261 | 8,852 MiB |
| Realtime, 960 ms | 30.1% WER / 69.9% aligned recall | 1.259 | 8,852 MiB |
| Realtime, 2400 ms | 27.7% WER / 75.9% aligned recall | 1.287 | 8,852 MiB |

The 240 ms condition won aggregate WER on these two clips, but all four delays
were approximately equal in speed on this rental and none supplied speaker or
overlap timing. The structured three-track prompt did not rescue attribution:
one response contained placeholder-like fields with empty direct-speech
intervals, and the short case produced truncated, invalid JSON. The standalone
offline baseline again hallucinated `I'm sorry` and `Yeah!` on the two
Gemini-adjudicated bleed-only tracks. The repeat therefore strengthens the
waveform-gating conclusion rather than selecting Voxtral as the judge.

The same three-worker matrix was then rerun through an optimized lifecycle.
Vast's `onstart` hook (not cloud-init) began an idempotent, secret-free prewarm
during provider boot. Each role used a pinned package overlay, exact model
commit and Transformers-only file allowlist; the duplicate 8.9–9.4 GB
`consolidated.safetensors` representation was not downloaded. After a matching
fingerprinted sentinel, inference was network-offline. Fixture upload overlapped
the prewarm, completed workers were validated and atomically collected while
siblings continued, and interruption now kills/reaps active SSH/SCP processes
before default exact-label teardown.

That operational A/B took 395.99 seconds and an estimated $0.03443, versus
855.8 seconds and $0.07766 for the immediately preceding clean run: 53.7% less
wall time and 55.7% less estimated cost. The two offline bundles were available
locally after 286.7 and 311.4 seconds; the full realtime grid after 392.0
seconds. Cold role prewarming itself took 113.5–118.7 seconds. Runner-reported
model-load time fell from 80–97 seconds to 2.6–4.6 seconds because model bytes
were already local and validated; offline baseline RTF improved from 0.069 to
0.064, prompted offline from 0.247 to 0.194, and the four realtime conditions
from 1.259–1.309 to 1.048–1.111 on the new hosts.

This was not a matched-host laboratory comparison, so the entire improvement
cannot be assigned to `onstart` alone. The lifecycle timestamps do directly
show boot and download overlapping, however, and filtered snapshots remove a
known duplicate transfer. The aggregate accuracy report was identical: 37.6%
WER / 64.5% aligned recall across scored references, 58.3% glossary recall,
zero forbidden-term injections, and the same two bleed-only false-speech
failures. The baseline texts were byte-stable; one multi-track prompted response
and one tiny realtime output varied across GPUs while leaving scores unchanged,
another reason to treat model output as evidence rather than deterministic
truth. Exact-label recovery after teardown found all three IDs absent.

All eight private fixtures now have a committed manifest containing duration,
sample count, whole-file hash, decoded-PCM hash, role, expected terms, and
bleed-only controls. The audio itself stays ignored. `harness/transcription-bench`
verifies streaming and normalized WAV headers, runs selectable CUDA model
subsets, emits structured JSON, scores glossary recall and forbidden/bleed
injections, and can plan/launch/wait/dispatch/collect/destroy independent Vast
workers concurrently. Exact owned labels and persisted instance IDs gate
destruction, and teardown is the default even on a failed worker. A collected
run can be rescored without enumerating workers via
`make score RESULTS_DIR=results/vast/<run-id>`; discovery accepts only known
benchmark schemas and the false-speech/forbidden-term gates remain active.

The March portable files intentionally use silence-compacted digest timelines.
Their build logs show reductions of 56.977–181.479 seconds, and their embedded
transcript is already remapped to the compacted audio. Comparing a portable clip
against a later full-timeline reprocessing produces a large but false “drift.”
All final comparisons use audio and transcript from the same artifact.

| Portable | Source duration | Digest/audio duration | Removed silence |
|---|---:|---:|---:|
| Mar 6 | 1,102.070 s | 1,045.093 s | 56.977 s |
| Mar 9 | 1,268.738 s | 1,106.433 s | 162.305 s |
| Mar 10 | 1,046.252 s | 864.773 s | 181.479 s |
| Mar 11 | 1,899.518 s | 1,788.833 s | 110.685 s |

Portable v1 embeds neither `sourceDurationMs` nor the digest-to-source timeline
map, and the successful build deletes the only map in `.cassini-work`. Future
portable payloads should retain both so a digest timestamp can always be mapped
back to its raw source.

Current Google documentation makes a useful production distinction:

- Gemini 3.7 Flash supports general audio analysis, diarization, and prompted
  timestamped transcription;
- Gemini 3.5 Transcribe is the dedicated path for word timestamps, speaker
  diarization, and custom vocabulary (up to 1,000 phrases, with a much smaller
  targeted list recommended).

For Cassini, the best use is a verifier on mined 10–20 second windows, not a
replacement for fast per-track Parakeet transcription.

### Resource-safety constraint

No further speech recognition should run on CPU on this host. Local ASR must be
explicitly configured with `CASSINI_STT_DEVICE=cuda`; CUDA recognizer concurrency
must remain one, and a run should be skipped unless both host `MemAvailable` and
GPU free memory have comfortable headroom. A CUDA initialization failure is a
hard failure, not permission to retry on CPU. OpenRouter Gemini inference is
remote and does not load an audio model into this host's RAM or VRAM.

The operator now rejects new CPU settings, pins every admitted build to CUDA
and one host thread, fails closed when free VRAM cannot be measured, and
durably defers a job when the GPU or RAM launch floor is unavailable. A
process-wide build gate keeps recognition serial even if
`CASSINI_MAX_BUILD_WORKERS` is greater than one. Stream concurrency is also
pinned to one, inherited additional-model passes are stripped, and explicit
model overrides are limited to the audited fp32 Parakeet v3 graph. There is no
whole-recognizer CPU fallback. Readiness now derives from the current effective
settings on every request rather than a cached process environment: it reports
the concrete CUDA/model policy and returns 503 for an unusable GPU or a legacy
CPU override instead of claiming a healthy CPU configuration whose jobs would
all be deferred.
Silero VAD is a separate, small one-thread detector, not the speech recognizer;
it remains on CPU because the measured per-window GPU dispatch was dramatically
slower while offering no transcription benefit.

PCM extraction no longer keeps both the full ffmpeg `s16le` byte buffer and a
second full `float32` copy. It streams fixed 64 KiB chunks directly into one
duration-sized float slice, reducing live audio data from about 330 MiB to
220 MiB per meeting-hour per active stream. This is a substantial peak-memory
reduction but remains duration-linear; a future fully streaming recognizer or
disk-backed sample store would remove that residual. GPU binary builds also
use `go build -p 2` so compilation respects the container's memory envelope.

### Deployment hard limit (separate from application admission)

The RAM/VRAM probes are launch admission checks, not hard resource limits: host
conditions can change after a build starts, and the application cannot reserve
the measured headroom. Production must therefore also apply a memory cgroup to
the Cassini ExApp/container itself (and disable or tightly cap its swap), sized
from the measured model working set plus margin. The audited 8 GiB per-container
limit is a tested starting point for the bundled fp32 model, not a universal
host setting; model or runtime changes must be remeasured. If a CPU quota is
required, apply it to that same workload cgroup rather than changing a
host-wide limit. The application-level governor remains useful for deferring a
launch before that hard boundary is approached.

The repository's GPU build guard also had a false negative: with shell
`pipefail`, `strings ... | grep -q` could report failure after `grep` closed the
pipe early, and it inspected the ONNX Runtime core rather than the split CUDA
provider. The guard now verifies `libonnxruntime_providers_cuda.so` directly;
the corrected script built and ran the audited CUDA binary successfully.

## Identity and vocabulary

Participant identity was already present in MKV tags but `ProbeMKV` read only
the title. It now reads `PARTICIPANT_ID` and `PARTICIPANT_NAME`, uses the stable
ID for speaker identity, and retains the display name separately. Rejoin streams
therefore map to one logical speaker. Legacy title-only recordings still derive
identity from their old title metadata, while newly generated filesystem/track
speaker keys use a readable sanitized slug plus a deterministic 96-bit SHA-256
suffix of the exact identity. That changes the generated ID format on rebuild,
but prevents punctuation variants and non-ASCII names from collapsing into one
speaker. Manifest `speakerCount` now counts unique logical speakers rather than
physical/rejoin streams.

The raw Parakeet path still has no safe prompt input. sherpa-onnx exposes
hotwords only with modified beam search for this model family, so enabling them
without a speed/WER benchmark would violate the speed requirement. The safer
increment is:

1. automatically collect participant display names and the Talk room/project
   title;
2. let an operator add a short preferred-spellings glossary;
3. feed those terms to configured LLM readable cleanup now;
4. benchmark Parakeet modified-beam hotwords before enabling them for raw ASR;
5. use an audio model to propose—not automatically accept—new terms from mined
   clips, with play buttons for mix and isolated tracks.

## Recommended challenge miner

Decode each participant with the corrected gap-preserving filter and calculate
20–32 ms log-RMS/VAD frames. Rank compact windows when any of these occurs:

1. a 0.2–2.5 second activity island is nested in another speaker's turn;
2. a word's midpoint has low energy on its attributed track but strong energy on
   another track;
3. two tracks overlap for at least 200 ms;
4. a clear activity island has no recognized words;
5. track envelopes are highly correlated, suggesting echo/playback leakage.

Send only the top 10–20 second windows to the verifier, with the mix, relevant
isolated tracks, participant labels, room name, and glossary. Score short-turn
recall, speaker-attributed WER, word timestamp median/P95 error, overlap recall,
and real-time factor. Keep waveform-derived flags and model proposals in an
audit sidecar so users can review corrections instead of silently rewriting the
canonical transcript.

`cmd/cassini-waveform-challenges` now makes this the fast inner loop without
rerunning ASR. Given an MKV and its `transcript.words.v1.json`, it probes the
shared timeline, streams one 32 ms RMS frame at a time, and atomically writes
the same review-only sidecar. A format-compatible 51-minute, five-track local
recording completed in 7.95 seconds warm (11.33 CPU-seconds), emitted a 33 KB
sidecar with the capped 20 candidates, and repeated byte-for-byte after removing
`generatedAt`.

Standalone reruns now invalidate an old regular-file or symlink output before
reading either input, so a failed or cancelled attempt cannot leave stale
evidence looking current. Successful sidecars record the exact MKV and
transcript basenames plus SHA-256 digests; the MKV hash is streamed in bounded
chunks before mining without loading the recording into memory.

That timing run deliberately is not counted as accuracy evidence: the available
local April recording and cached August transcript had similar duration and
participants but different speech. A blind Gemini check of the top window
exposed the mismatch immediately. The exact August source was not in the
read-only recordings mount, and an idle-priority copy was stopped when George
showed elevated I/O wait. Correctness conclusions continue to use only clips
whose audio, isolated tracks, and transcript provenance match exactly.

## Verification

At the time of this report:

- `GOMAXPROCS=2 go test -p 2 ./...` passes for both
  `cassini-go-recorder` and `cassini-operator`; no full-tree race run was used;
- before the GPU-only constraint was set, one warm CPU/int8 sanity check of an
  11.008-second audited clip completed in 4.304 seconds including startup,
  model load, mix, transcription, and artifact writing; it was not repeated,
  and no further CPU ASR benchmark is authorized;
- the bounded CUDA seven-clip build completed in 6.08 seconds with 3.64 GiB peak
  VRAM, about 1.3 GiB peak host RSS, and zero swap;
- the delayed-stream regression passes for extraction and mixdown;
- interruption merge tests pass with no word loss/duplication;
- the capture timestamp-order test passes repeatedly;
- the benchmark harness verifies all eight audio/PCM hashes and has 61 passing
  fixture, scorer, prewarm, runner, cancellation, collection-integrity,
  secret-redaction, endpoint, recovery, and exact-destruction tests;
  Python/Bash syntax and ShellCheck pass;
- the viewer has 158 passing tests and builds successfully; the control panel
  has 26 passing tests and builds successfully;
- `git diff --check` passes.

## External references

- [Gemini audio understanding](https://ai.google.dev/gemini-api/docs/audio)
- [Gemini audio transcription and custom vocabulary](https://ai.google.dev/gemini-api/docs/transcribe)
- [OpenRouter audio input](https://openrouter.ai/docs/guides/overview/multimodal/audio)
- [Voxtral Mini 3B model card](https://huggingface.co/mistralai/Voxtral-Mini-3B-2507)
- [Voxtral Mini 4B Realtime model card](https://huggingface.co/mistralai/Voxtral-Mini-4B-Realtime-2602)
- [Vast.ai instance search and creation](https://docs.vast.ai/api-reference/creating-instances-with-api)
- [Vast.ai create-instance/onstart CLI reference](https://docs.vast.ai/cli/reference/create-instance)
- [Vast.ai instance destruction](https://docs.vast.ai/api-reference/instances/destroy-instance)
- [Hugging Face revision-pinned and filtered downloads](https://huggingface.co/docs/huggingface_hub/guides/download)
- [Gemma 4 model card](https://huggingface.co/google/gemma-4-12B)
- [llama.cpp multimodal support](https://github.com/ggml-org/llama.cpp/blob/master/docs/multimodal.md)
