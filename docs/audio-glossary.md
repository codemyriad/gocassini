# Audio & Media-Pipeline Glossary

A short tour of the concepts that show up across the Cassini recording / remux / transcription flow. Each entry has a one- or two-sentence definition, a "in Cassini" line pointing at where it appears, and a link for deeper reading.

This is orientation, not a tutorial. If you want a tutorial, the linked specs and texts will go into far more depth than makes sense to duplicate here.

---

## Containers and codecs

### Container vs. codec
A **codec** compresses raw samples into a bitstream (e.g. Opus, VP9, H.264). A **container** wraps one or more codec bitstreams together with timing and metadata so a player can find them (e.g. MKV, WebM, MP4). The same Opus audio can live inside a `.webm`, an `.ogg`, or an `.mkv` — the codec bytes are the same, only the wrapper changes.
*Further reading:* [Wikipedia: Comparison of video container formats](https://en.wikipedia.org/wiki/Comparison_of_video_container_formats).

### PCM (Pulse-Code Modulation)
Uncompressed digital audio: a stream of sample values at a fixed rate. The "raw" form before any codec touches it. PCM has a **sample rate** (how often samples are taken — e.g. 48 kHz means 48,000 samples per second), a **channel count** (mono = 1, stereo = 2), and a **bit depth / format** (16-bit signed little-endian = `s16le` is the Cassini default).
*In Cassini:* [`audio.go`](../cassini-go-recorder/internal/transcribe/audio.go) decodes WebM to PCM floats for the STT model; the SHA-256 of the decoded PCM is computed for [integrity tracking](../cassini-go-recorder/internal/transcribe/transcribe.go#L57).
*Further reading:* [Wikipedia: Pulse-code modulation](https://en.wikipedia.org/wiki/Pulse-code_modulation).

### Sample rate
How many audio samples per second the signal is captured at. Telephony historically used 8 kHz; CDs use 44.1 kHz; WebRTC and modern codecs use 48 kHz. Higher rates capture more frequency detail at the cost of more data. STT models are usually trained at a specific rate (16 kHz is common) so audio is resampled before recognition.
*In Cassini:* meeting WebM is mono 48 kHz Opus ([`transcribe.go:24-28`](../cassini-go-recorder/internal/transcribe/transcribe.go#L24-L28)); the STT model declares its own expected rate via `modelPaths.SampleRate` ([`transcribe.go:100`](../cassini-go-recorder/internal/transcribe/transcribe.go#L100)) and the audio is resampled to match.

### Opus
A modern, royalty-free audio codec designed for both speech and music, optimized for low-latency real-time use. Standard for WebRTC and the default audio codec for WebM. It packs 20 ms frames by default and adapts smoothly between speech and music modes.
*In Cassini:* every audio track ends up as Opus — both during capture (Nextcloud Talk delivers Opus over RTP) and in the final mixed `meeting.webm` ([`transcribe.go:50-54`](../cassini-go-recorder/internal/transcribe/transcribe.go#L50-L54)).
*Further reading:* [Opus website](https://opus-codec.org/), [RFC 6716](https://datatracker.ietf.org/doc/rfc6716/).

### WebM
A container format that is a subset of Matroska, restricted to a small set of royalty-free codecs (Opus and Vorbis for audio; VP8/VP9/AV1 for video). Designed for the open web. An audio-only `.webm` is just an Opus stream in a Matroska wrapper.
*In Cassini:* `meeting.webm` is the per-meeting mixed audio artifact, written by [`MixDownToWebM`](../cassini-go-recorder/internal/transcribe/audio.go).
*Further reading:* [webmproject.org](https://www.webmproject.org/).

### Matroska / MKV
The general-purpose container that WebM is a subset of. Cassini uses MKV for the multitrack recording (one track per participant) because it accepts any codec and supports rich metadata via Tags and Attachments. See [`mkv-format.md`](../cassini-go-recorder/docs/mkv-format.md).
*Further reading:* [matroska.org technical specs](https://www.matroska.org/technical/elements.html).

### EBML (Extensible Binary Meta Language)
The binary tree-of-elements encoding that Matroska is built on. Tagged, length-prefixed, designed to be parseable while streaming. You'll rarely interact with EBML directly — Matroska tooling (mkvtoolnix, ffmpeg) hides it.
*Further reading:* [RFC 8794](https://datatracker.ietf.org/doc/rfc8794/).

---

## Real-time transport (how audio arrives during a call)

### RTP (Real-time Transport Protocol)
The standard protocol for carrying audio/video over IP networks in real time. Each RTP packet has a header (sequence number, timestamp, SSRC, payload type) followed by a chunk of codec data — for Opus, typically a 20 ms frame. RTP itself is unreliable; loss and reordering are expected.
*In Cassini:* every audio packet arriving from Nextcloud Talk is an RTP packet. Capture writes them verbatim to `streams/*.rtplog` ([`formats.md`](../cassini-go-recorder/docs/formats.md)).
*Further reading:* [RFC 3550](https://datatracker.ietf.org/doc/rfc3550/).

### RTCP (RTP Control Protocol)
The companion protocol to RTP: out-of-band statistics, sync info, and reception reports. The two messages that matter most for Cassini are **Sender Report (SR)** — used to align RTP timestamps with wall-clock time — and **Receiver Report (RR)** — loss/jitter from the receiver's view.
*In Cassini:* RTCP is recorded alongside RTP in the same `.rtplog` files; SR data drives the [SR-aware drift correction](../cassini-go-recorder/docs/muxing.md) used during remux.
*Further reading:* [RFC 3550 Sec. 6](https://datatracker.ietf.org/doc/rfc3550/).

### SSRC (Synchronization Source identifier)
A 32-bit ID identifying one source of media within an RTP session. One participant typically has one SSRC per track (one for audio, one for video). When a participant disconnects and rejoins, or simulcast layers switch, the SSRC may change — Cassini uses SSRC as a primary key for stream-segment boundaries.
*Further reading:* [RFC 3550 Sec. 5.1](https://datatracker.ietf.org/doc/rfc3550/#section-5.1).

### Payload type (PT)
A small integer in each RTP header pointing at a codec/format definition negotiated in the SDP. PT 111 is conventionally Opus in WebRTC, but values are dynamically assigned per session. A PT change mid-stream means the codec changed and a new stream segment must start.
*In Cassini:* PT and SSRC together drive the segment-rotation logic in capture.
*Further reading:* [RFC 3551](https://datatracker.ietf.org/doc/rfc3551/), [WebRTC SDP guide](https://webrtchacks.com/sdp-anatomy/).

### Clock rate (RTP clock)
The clock used in RTP timestamps for a given codec. Opus uses 48,000 ticks per second; most video codecs use 90,000. Note that Opus's *RTP clock* is 48 kHz even when its frames represent media at 16 kHz internally — the timestamp is in clock ticks, not sample counts.
*In Cassini:* the clock rate per stream is captured from the SDP and persisted in the session; remux uses it to convert RTP timestamps to nanoseconds.

### Sender Report (SR)
An RTCP message a sender emits periodically that says, "at this NTP wall-clock time, my RTP timestamp was X." This is the bridge between an RTP stream's internal clock and real-world time, and the only mechanism that lets a receiver detect drift between sender clock and receive clock.
*In Cassini:* SR data is what the timeline estimator uses to apply bounded per-stream offsets during remux ([`muxing.md`](../cassini-go-recorder/docs/muxing.md)).

### WebRTC track / MID / RID
**Track** is the WebRTC abstraction for one media flow (one audio or one video). **MID** (Media Identification) is the SDP-level identifier of a transceiver/m-line. **RID** (Restriction ID) identifies one simulcast layer within a single track. Together they let recorders distinguish "Alice's audio" vs "Alice's screen-share video" vs "Alice's medium-quality simulcast."
*In Cassini:* both are recorded as per-stream MKV tags ([`mkv-format.md`](../cassini-go-recorder/docs/mkv-format.md)) and in the embedded JSON report.
*Further reading:* [W3C WebRTC spec](https://www.w3.org/TR/webrtc/).

---

## Timestamps and timing

### PTS (Presentation Timestamp) / DTS (Decode Timestamp)
**PTS** is when a frame should be *displayed*; **DTS** is when it should be *decoded*. For audio they are usually equal. For video with B-frames they can differ: a frame may need to be decoded before the frame it's shown after.
*In Cassini:* the `mux.Muxer` interface accepts `ptsNS` per sample ([`mux.go`](../cassini-go-recorder/pkg/core/mux/mux.go)).

### Wall clock vs. monotonic clock vs. RTP clock
- **Wall clock** is what `date` shows; it can jump (NTP corrections).
- **Monotonic clock** never goes backwards; ideal for measuring intervals locally.
- **RTP clock** is each stream's own internal counter, increments per codec ticks (e.g. 48,000/sec for Opus).
Three different clocks, all useful for different things.
*In Cassini:* every captured packet has `recvMonoNS` (local monotonic receive time) so offline remux can replay exact arrival timing without depending on wall-clock NTP shifts ([`formats.md`](../cassini-go-recorder/docs/formats.md)).

### Drift
When two clocks tick at slightly different real-world rates, their values diverge over time — that's drift. In a meeting recording, the sender's encoder clock can drift relative to the recorder's receive clock; over a long meeting this becomes audible as A/V getting out of sync.
*In Cassini:* SR-aware correction in the timeline estimator applies bounded per-stream offsets to keep drift small ([`muxing.md`](../cassini-go-recorder/docs/muxing.md), [`pkg/core/timeline`](../cassini-go-recorder/pkg/core/timeline/)).

### `-itsoffset`
An ffmpeg flag that shifts an input's timestamps by a fixed offset before muxing, without re-encoding. Used during remux to apply the bounded drift corrections from above.
*In Cassini:* see the merge step in [`artifact.go:266-334`](../cassini-go-recorder/pkg/core/remux/artifact.go#L266-L334).
*Further reading:* [ffmpeg main options](https://ffmpeg.org/ffmpeg.html#Main-options).

### `-copyts` and `-genpts`
ffmpeg flags that control how timestamps are handled. `-copyts` preserves source timestamps; `-genpts` invents fresh ones. For Cassini single-track intermediates, `-copyts` is preferred so sparse packet timelines (gaps from mute periods) survive intact instead of being collapsed.
*In Cassini:* used in `composeSingleTrackMKV` ([`artifact.go:247-264`](../cassini-go-recorder/pkg/core/remux/artifact.go#L247-L264)).

### Receive monotonic timestamp (`recvMonoNS`)
Each RTP packet stored in `.rtplog` is annotated with the recorder's monotonic-clock receive time in nanoseconds. This is the durable truth from which everything else (timeline reconstruction, drift estimation, remux) is derived.

---

## From packets to playable media

### Depacketization
Reversing the RTP packetization: stitching a sequence of RTP payloads back into a continuous codec elementary stream (e.g. assembling Opus frames from a series of RTP packets).
*In Cassini:* [`pkg/core/depacket`](../cassini-go-recorder/pkg/core/depacket/) handles Opus + VP8/VP9/H264 depacketization.

### Elementary stream
The raw codec bitstream stripped of any container or transport. An "Opus elementary stream" is just frame-after-frame of Opus data, with no Matroska, no WebM, no RTP. It's the format you mux into a final container.

### Mux / remux
**Mux** = combine elementary streams into a container. **Remux** = read a container, change container or metadata, write a new container — typically *without* re-encoding the codec data (`-c copy`). Cassini's final MKV is produced by *remuxing* depacketized elementary streams together with metadata. No bits of audio or video are altered; only timing offsets and tags.
*Further reading:* [ffmpeg muxers/demuxers](https://ffmpeg.org/ffmpeg-formats.html).

### Mixdown
Combining multiple input audio tracks into fewer output channels — e.g. five participants' separate tracks summed into one mono mix. Lossy in the sense that you lose per-speaker isolation, but useful for an audio-only deliverable.
*In Cassini:* `MixDownToWebM` produces the single mono `meeting.webm` from per-participant streams in the source MKV ([`transcribe.go:49-54`](../cassini-go-recorder/internal/transcribe/transcribe.go#L49-L54)).

---

## Speech processing

### VAD (Voice Activity Detection)
A small model that classifies short audio windows as "speech" vs "non-speech." Used to skip silent regions during transcription, both for speed (don't run the big STT model on silence) and quality (most STT models hallucinate text on long silences).
*In Cassini:* `EnsureVAD` downloads a VAD model that the recognizer uses to gate STT ([`transcribe.go:74`](../cassini-go-recorder/internal/transcribe/transcribe.go#L74)).
*Further reading:* [Silero VAD](https://github.com/snakers4/silero-vad), the most commonly bundled VAD with sherpa-onnx.

### STT / ASR (Speech-to-Text / Automatic Speech Recognition)
Models that turn audio into text. Modern ASR returns word-level timing too, not just a transcript. Two big families today: **CTC/transducer** models (Parakeet, Conformer-CTC) — fast, deterministic, English-leaning; and **encoder-decoder / sequence-to-sequence** models (Whisper) — multilingual, slower, more prone to hallucination.
*In Cassini:* Parakeet is the default ([`models.go`](../cassini-go-recorder/internal/transcribe/models.go)). The recognizer is sherpa-onnx (see below).
*Further reading:* [Wikipedia: Speech recognition](https://en.wikipedia.org/wiki/Speech_recognition).

### Sherpa-ONNX
An open-source speech toolkit that runs ONNX-format models (Parakeet, Zipformer, Whisper, etc.) without a Python runtime. Cassini uses its Go bindings.
*Further reading:* [k2-fsa/sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx).

### Word-level timestamps
ASR output that includes a `(start_ms, end_ms)` for every word, not just the full text. Required for caption rendering, click-to-seek transcripts, and any UX that needs to align audio playback with text.
*In Cassini:* the `Word` and `Segment` types in [`stt.go`](../cassini-go-recorder/internal/transcribe/stt.go) carry these; `transcript.words.v1.json` is the canonical artifact contract.

### Speaker diarization
The problem of "who spoke when" — segmenting audio by speaker without prior identity. Notoriously hard on a single mixed track. Cassini sidesteps it: the recorder receives one RTP stream per participant, so each speaker is already on a distinct track and no diarization is needed at transcription time.
*Further reading:* [pyannote.audio](https://github.com/pyannote/pyannote-audio) is the reference open-source toolkit if you ever need it.

### Segment / utterance
A contiguous block of speech from one speaker. Cassini's `AssembleSegments` ([`format.go:206`](../cassini-go-recorder/internal/transcribe/format.go#L206)) groups consecutive words into a segment when the gap is short and the word count is bounded — a pragmatic substitute for sentence boundary detection.

---

## Captions

### WebVTT (`.vtt`)
A simple text-based caption format used by HTML5 video. Each "cue" is `START --> END` followed by lines of text. Optional positioning, styling, and speaker tags. Browser-native: `<track src="captions.vtt">` works without JS.
*In Cassini:* `WriteCaptionsVTT` ([`format.go:98`](../cassini-go-recorder/internal/transcribe/format.go#L98)) emits the cleaned-text VTT alongside the readable transcript.
*Further reading:* [W3C WebVTT spec](https://www.w3.org/TR/webvtt1/), [MDN tutorial](https://developer.mozilla.org/en-US/docs/Web/API/WebVTT_API).

### SRT (briefly, for context)
The older caption format. Same idea as VTT but with sequence numbers and a slightly different timestamp syntax. Cassini doesn't use it; mentioned because most caption-aware tools accept either.

---

## Audio integrity

### SHA-256 of decoded PCM
Cassini hashes the *decoded PCM samples*, not the encoded container bytes. Why: the same audio can be muxed into different containers or re-encoded with the same Opus settings, producing different bytes that decode to the same waveform. Hashing PCM means "did the actual audible content change?" survives container shuffling.
*In Cassini:* `PCMsha256FromWebM` ([`transcribe.go:57`](../cassini-go-recorder/internal/transcribe/transcribe.go#L57)). The hash is embedded in `transcript.words.v1.json` as `media.sha256` so a transcript can be matched to the exact audio it was generated from.

### Sample-exact / bit-exact matching
A pair of audio files match *sample-exactly* if their decoded PCM is identical at every sample. Stronger than container-byte equality; weaker than file-byte equality. The right notion when comparing recordings across different containers or remuxers.

---

## Further reading (curated)

- **RTP / RTCP fundamentals:** [RFC 3550](https://datatracker.ietf.org/doc/rfc3550/) is the canonical reference. Long but the introduction is friendly.
- **Opus deep dive:** [Opus codec website](https://opus-codec.org/) and [RFC 6716](https://datatracker.ietf.org/doc/rfc6716/).
- **WebRTC end-to-end:** [webrtc-for-the-curious.com](https://webrtcforthecurious.com/) — free book, the most readable overview.
- **WebRTC + recording specifics:** [webrtcHacks](https://webrtchacks.com/) — practitioner blog, covers exactly the kinds of edge cases the Cassini recorder has to handle.
- **Matroska:** [matroska.org/technical](https://www.matroska.org/technical/elements.html).
- **WebVTT:** [W3C spec](https://www.w3.org/TR/webvtt1/), [MDN tutorial](https://developer.mozilla.org/en-US/docs/Web/API/WebVTT_API).
- **Speech recognition state-of-the-art:** [Hugging Face ASR leaderboard](https://huggingface.co/spaces/hf-audio/open_asr_leaderboard) — current model rankings on common benchmarks.
- **ffmpeg:** [official docs](https://ffmpeg.org/ffmpeg.html); [trac.ffmpeg.org/wiki](https://trac.ffmpeg.org/wiki) is more practical, especially the [Map](https://trac.ffmpeg.org/wiki/Map) and [How to fix non-monotonic DTS errors](https://trac.ffmpeg.org/wiki) pages.
- **Inside the recorder:** [`docs/architecture-overview.md`](../cassini-go-recorder/docs/architecture-overview.md), [`formats.md`](../cassini-go-recorder/docs/formats.md), [`muxing.md`](../cassini-go-recorder/docs/muxing.md), [`mkv-format.md`](../cassini-go-recorder/docs/mkv-format.md), [`transcription-pipeline.md`](../cassini-go-recorder/docs/transcription-pipeline.md).
