"""
core.py — shared primitives for both pipelines.

Ports the production Go code in cassini-go-recorder/internal/transcribe
(audio.go, stt.go) closely enough that Pipeline A is a faithful stand-in for
the status quo: same ffmpeg filters, same Silero VAD parameters, same
55s/10s/0.5s chunking and tail-padding rules, same token->word logic.
"""
import json, subprocess, math, time
import numpy as np
import sherpa_onnx

SR = 16000

# ---------------------------------------------------------------- probing
def probe_mkv(path):
    """Port of ProbeMKV (audio.go)."""
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries",
         "stream=index,codec_type,channels,start_time:stream_tags=title:format=duration",
         "-of", "json", path], capture_output=True, text=True, check=True).stdout
    p = json.loads(out)
    dur_ms = int(round(float(p["format"]["duration"]) * 1000))
    streams, n = [], 0
    for s in p["streams"]:
        if s.get("codec_type") != "audio":
            continue
        label = (s.get("tags", {}) or {}).get("title", "").strip() or f"Speaker {n+1}"
        st = s.get("start_time", "0")
        try:
            start_ms = max(0, int(round(float(st) * 1000)))
        except (TypeError, ValueError):
            start_ms = 0
        streams.append(dict(index=s["index"], speaker=label, start_ms=start_ms))
        n += 1
    return streams, dur_ms


def _sparse_filter(start_ms):
    """Port of sparseTimelineAudioFilter (audio.go)."""
    f = "aresample=async=1:first_pts=0"
    if start_ms > 0:
        f += f",adelay={start_ms}:all=1"
    return f


def _pcm(args):
    raw = subprocess.run(["ffmpeg", "-v", "error", "-y"] + args +
                         ["-f", "s16le", "-acodec", "pcm_s16le", "pipe:1"],
                         capture_output=True, check=True).stdout
    return np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0


def extract_speaker_floats(mkv, stream, sr=SR):
    """Port of ExtractSpeakerFloats (audio.go)."""
    return _pcm(["-i", mkv, "-map", f"0:{stream['index']}", "-vn", "-sn", "-dn",
                 "-af", _sparse_filter(stream["start_ms"]), "-ac", "1", "-ar", str(sr)])


def decode_tracks(mkv, streams, sr=SR):
    """Decode every participant track once, on the shared meeting timeline.

    Pipeline B needs these buffers twice - to build the ASR mix and to build the
    energy envelopes for attribution - so they are decoded once and reused.
    """
    return {s["speaker"]: extract_speaker_floats(mkv, s, sr) for s in streams}


def mix_from_tracks(tracks):
    """Lossless in-memory sum of the decoded tracks.

    Summing in numpy rather than through ffmpeg's amix is not just simpler: on
    real sparse Talk recordings `amix` over `aresample=async=1` inputs aborts
    with `Assertion best_input >= 0 failed` (ffmpeg 7.1, ffmpeg_filter.c:2122).

    NOTE this is deliberately NOT meeting.webm: production's merged fallback
    calls ExtractMixedFloats(webmPath), i.e. it transcribes the 64 kbps Opus
    delivery artifact rather than the source audio.
    """
    n = max(len(x) for x in tracks.values())
    acc = np.zeros(n, dtype=np.float64)
    for x in tracks.values():
        acc[:len(x)] += x
    peak = float(np.abs(acc).max())
    if peak > 1.0:            # only touch it if the sum actually clips
        acc /= peak
    return acc.astype(np.float32)


def extract_mix_floats(mkv, streams, sr=SR):
    return mix_from_tracks(decode_tracks(mkv, streams, sr))


# ---------------------------------------------------------------- ASR
class Word:
    __slots__ = ("text", "start_ms", "end_ms", "speaker")
    def __init__(self, text, start_ms, end_ms, speaker=None):
        self.text, self.start_ms, self.end_ms, self.speaker = text, start_ms, end_ms, speaker
    def as_dict(self):
        return dict(text=self.text, start_ms=self.start_ms, end_ms=self.end_ms, speaker=self.speaker)


# Production uses vadChunkSamples = 16000*5. Feeding Silero blocks larger than
# its native window_size quantises segment onsets to the feed size: measured
# first-speech onset is 0.93 s at 512, 1.67 s at 16000 and 4.68 s at 80000
# against a 0.93 s energy ground truth. Both pipelines here feed 512 so the
# architecture comparison is not contaminated by that defect.
VAD_CHUNK = 512                 # Silero native window_size
MAX_SAFE_SEG = SR * 55          # maxSafeSegmentSamples
TAIL_PAD_MIN_S = 10             # decoderTailPadMinSeconds


def tokens_to_words(tokens, timestamps, durations=None):
    """Port of tokensToWords + splitMultiWordTokens (stt.go)."""
    if not tokens:
        return []
    words, cur, w_start, last_end = [], [], -1.0, 0.0

    def flush():
        nonlocal cur, w_start, last_end
        text = "".join(cur).strip()
        if text:
            words.append(Word(text, int(w_start), int(max(last_end, w_start))))
        cur, w_start = [], -1.0

    for i, tok in enumerate(tokens):
        ts = float(timestamps[i]) * 1000 if i < len(timestamps) else 0.0
        dur = float(durations[i]) * 1000 if durations and i < len(durations) else 0.0
        end = ts + dur
        if tok.startswith("▁") or tok.startswith(" ") or tok == "<space>":
            flush()
            clean = tok.lstrip("▁").lstrip(" ")
            if clean:
                if w_start < 0:
                    w_start = ts
                cur.append(clean)
                last_end = max(last_end, ts, end)
        else:
            if w_start < 0:
                w_start = ts
            cur.append(tok)
            last_end = max(last_end, ts, end)
    flush()

    # splitMultiWordTokens
    out = []
    for w in words:
        parts = w.text.split()
        if len(parts) <= 1:
            out.append(w); continue
        span = w.end_ms - w.start_ms or len(parts)
        for i, p in enumerate(parts):
            s = w.start_ms + (span * i) // len(parts)
            e = w.start_ms + (span * (i + 1)) // len(parts)
            out.append(Word(p, s, max(e, s)))
    return out


class Recognizer:
    """Port of Recognizer (stt.go): Parakeet transducer + Silero VAD."""

    def __init__(self, model_dir, vad_path, provider="cpu", num_threads=4,
                 feature_dim=128, int8=True):
        suffix = ".int8.onnx" if int8 else ".onnx"
        self.rec = sherpa_onnx.OfflineRecognizer.from_transducer(
            encoder=f"{model_dir}/encoder{suffix}",
            decoder=f"{model_dir}/decoder{suffix}",
            joiner=f"{model_dir}/joiner{suffix}",
            tokens=f"{model_dir}/tokens.txt",
            num_threads=num_threads, sample_rate=SR, feature_dim=feature_dim,
            decoding_method="greedy_search", model_type="nemo_transducer",
            provider=provider,
        )
        cfg = sherpa_onnx.VadModelConfig()
        cfg.silero_vad.model = vad_path
        cfg.silero_vad.threshold = 0.5
        cfg.silero_vad.min_silence_duration = 0.5
        cfg.silero_vad.min_speech_duration = 0.25
        cfg.silero_vad.window_size = 512
        cfg.silero_vad.max_speech_duration = 25.0
        cfg.sample_rate = SR
        cfg.num_threads = 1
        cfg.provider = "cpu"      # vadProvider(): always CPU
        self.vad_cfg = cfg
        self.vad = sherpa_onnx.VoiceActivityDetector(cfg, 60.0)

    def _decode(self, chunk):
        st = self.rec.create_stream()
        st.accept_waveform(SR, chunk)
        self.rec.decode_stream(st)
        r = st.result
        return list(r.tokens), list(r.timestamps), list(getattr(r, "durations", []) or [])

    def transcribe_segment(self, samples, off_ms):
        """Port of transcribeSegment: 55s split + 0.5s tail pad below 10s."""
        out = []
        for start in range(0, len(samples), MAX_SAFE_SEG):
            chunk = samples[start:start + MAX_SAFE_SEG]
            if len(chunk) < TAIL_PAD_MIN_S * SR:
                chunk = np.concatenate([chunk, np.zeros(SR // 2, dtype=np.float32)])
            toks, ts, dur = self._decode(chunk)
            ws = tokens_to_words(toks, ts, dur)
            delta = off_ms + start * 1000 // SR
            for w in ws:
                w.start_ms += delta; w.end_ms += delta
            out.extend(ws)
        return out

    def transcribe(self, samples, use_vad=True):
        """Port of Transcribe (stt.go), VAD path."""
        if len(samples) == 0:
            return []
        if not use_vad:
            return self.transcribe_segment(samples, 0)
        self.vad.reset()
        words = []

        def drain():
            while not self.vad.empty():
                seg = self.vad.front
                # Copy the samples BEFORE pop(): the segment's buffer is owned
                # by the VAD queue and pop() invalidates it. Reading it after
                # pop() yields garbage (measured rms 2.5e10), which the decoder
                # turns into a run of <unk>.
                buf = np.array(seg.samples, dtype=np.float32, copy=True)
                start = int(seg.start)
                self.vad.pop()
                if len(buf) == 0:
                    continue
                words.extend(self.transcribe_segment(buf, start * 1000 // SR))

        # Drain after every chunk. The VAD is constructed with a 60 s buffer;
        # production feeds the whole track before popping, so on any recording
        # longer than the buffer the earliest speech is silently evicted.
        # That is D-679, and it is fixed here so that Pipeline A is compared on
        # its merits rather than on a known bug.
        for off in range(0, len(samples), VAD_CHUNK):
            self.vad.accept_waveform(samples[off:off + VAD_CHUNK])
            drain()
        self.vad.flush()
        drain()
        return words
