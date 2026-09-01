"""
asr_backends.py — the architecture is model-agnostic; this is where that gets tested.

The proposal's claim is that speaker attribution should be a stage over the shared
timeline rather than a property of which decoder ran. If that is right, swapping the
decoder should not change the *shape* of the win. So every backend exposes the same
contract and the same Silero VAD segmentation runs in front of all of them:

    decode(samples16k) -> [(text, start_ms, end_ms), ...]   # segment-relative

A backend that cannot emit per-word times cannot be attributed by energy at all -
which is itself a result worth recording rather than a reason to skip the model.
"""
import numpy as np

SR = 16000


class Backend:
    name = "abstract"
    emits_word_times = True

    def decode(self, samples):
        raise NotImplementedError

    def close(self):
        pass


# ------------------------------------------------------------------ Parakeet
class ParakeetBackend(Backend):
    """sherpa-onnx Parakeet TDT — the production decoder."""
    name = "parakeet-tdt-0.6b-v3-int8"

    def __init__(self, model_dir, provider="cpu", num_threads=8, int8=True, feature_dim=128):
        import sherpa_onnx
        suffix = ".int8.onnx" if int8 else ".onnx"
        self.rec = sherpa_onnx.OfflineRecognizer.from_transducer(
            encoder=f"{model_dir}/encoder{suffix}", decoder=f"{model_dir}/decoder{suffix}",
            joiner=f"{model_dir}/joiner{suffix}", tokens=f"{model_dir}/tokens.txt",
            num_threads=num_threads, sample_rate=SR, feature_dim=feature_dim,
            decoding_method="greedy_search", model_type="nemo_transducer", provider=provider)

    def decode(self, samples):
        from core import tokens_to_words
        st = self.rec.create_stream()
        st.accept_waveform(SR, samples)
        self.rec.decode_stream(st)
        r = st.result
        ws = tokens_to_words(list(r.tokens), list(r.timestamps),
                             list(getattr(r, "durations", []) or []))
        return [(w.text, w.start_ms, w.end_ms) for w in ws]


# ------------------------------------------------------------------ Qwen3-ASR
class Qwen3ASRBackend(Backend):
    """
    Qwen3-ASR via transformers. Word timings come from the companion forced
    aligner rather than from the ASR head, which is the documented path.
    """
    name = "qwen3-asr"

    def __init__(self, model_id="Qwen/Qwen3-ASR-1.7B-hf",
                 aligner_id="Qwen/Qwen3-ForcedAligner-0.6B-hf", device="cuda", dtype="bfloat16"):
        import torch
        from transformers import AutoProcessor, AutoModelForSpeechSeq2Seq
        self.torch = torch
        self.device = device
        td = getattr(torch, dtype)
        self.processor = AutoProcessor.from_pretrained(model_id)
        self.model = AutoModelForSpeechSeq2Seq.from_pretrained(
            model_id, dtype=td).to(device).eval()
        self.aligner_id = aligner_id
        self.aligner = None
        self.aligner_processor = None

    def _ensure_aligner(self):
        if self.aligner is not None:
            return
        from transformers import AutoProcessor, AutoModelForTokenClassification
        self.aligner_processor = AutoProcessor.from_pretrained(self.aligner_id)
        self.aligner = AutoModelForTokenClassification.from_pretrained(
            self.aligner_id, dtype=self.torch.bfloat16).to(self.device).eval()

    def transcribe_text(self, samples):
        inputs = self.processor(audio=samples, sampling_rate=SR, return_tensors="pt")
        inputs = {k: (v.to(self.device) if hasattr(v, "to") else v) for k, v in inputs.items()}
        with self.torch.no_grad():
            ids = self.model.generate(**inputs, max_new_tokens=440)
        return self.processor.batch_decode(ids, skip_special_tokens=True)[0].strip()

    def decode(self, samples):
        text = self.transcribe_text(samples)
        if not text:
            return []
        return align_words(self, samples, text)


# ------------------------------------------------------------------ Voxtral
class VoxtralBackend(Backend):
    """
    Voxtral Mini via transformers. The 2026-08-27 audit records that Voxtral
    returns a flat transcript with no speaker or overlap times; if no per-word
    timing is available the words are spread evenly across the segment and the
    backend is flagged so the scoring can say so.
    """
    name = "voxtral-mini-3b"
    emits_word_times = False

    def __init__(self, model_id="mistralai/Voxtral-Mini-3B-2507", device="cuda", dtype="bfloat16"):
        import torch
        from transformers import AutoProcessor, VoxtralForConditionalGeneration
        self.torch = torch
        self.device = device
        self.processor = AutoProcessor.from_pretrained(model_id)
        self.model = VoxtralForConditionalGeneration.from_pretrained(
            model_id, dtype=getattr(torch, dtype), device_map=device).eval()
        self.model_id = model_id

    def transcribe_text(self, samples):
        inputs = self.processor.apply_transcription_request(
            language="en", audio=[samples], model_id=self.model_id, sampling_rate=SR)
        inputs = inputs.to(self.device, dtype=self.torch.bfloat16)
        with self.torch.no_grad():
            ids = self.model.generate(**inputs, max_new_tokens=440)
        out = self.processor.batch_decode(
            ids[:, inputs.input_ids.shape[1]:], skip_special_tokens=True)
        return (out[0] if out else "").strip()

    def decode(self, samples):
        text = self.transcribe_text(samples)
        return spread_words(text, len(samples))


# ------------------------------------------------------------------ helpers
def spread_words(text, n_samples):
    """
    Last resort for a backend with no word timings: distribute words evenly over
    the segment. This is explicitly NOT good enough for energy attribution - it
    is here so such a model still produces a scoreable transcript, and so the
    resulting attribution error is visible instead of hidden.
    """
    parts = text.split()
    if not parts:
        return []
    dur_ms = int(n_samples * 1000 / SR)
    step = dur_ms / len(parts)
    return [(p, int(i * step), int((i + 1) * step)) for i, p in enumerate(parts)]


def align_words(backend, samples, text):
    """Word timings for a text hypothesis using Qwen3-ForcedAligner."""
    backend._ensure_aligner()
    torch = backend.torch
    proc, model = backend.aligner_processor, backend.aligner
    words = text.split()
    if not words:
        return []
    try:
        inputs = proc(audio=samples, sampling_rate=SR, text=text, return_tensors="pt")
        inputs = {k: (v.to(backend.device) if hasattr(v, "to") else v) for k, v in inputs.items()}
        with torch.no_grad():
            out = model(**inputs)
        spans = getattr(out, "word_timestamps", None)
        if spans is None and hasattr(proc, "decode_timestamps"):
            spans = proc.decode_timestamps(out, text)
        if spans:
            return [(w, int(float(s) * 1000), int(float(e) * 1000))
                    for (w, s, e) in spans][:len(words)]
    except Exception as exc:          # aligner unavailable / API drift
        print(f"      [align] forced aligner unusable ({type(exc).__name__}: {exc}); "
              f"falling back to even spread")
    return spread_words(text, len(samples))


# ------------------------------------------------------------------ hosted API
class OpenRouterBackend(Backend):
    """
    Any audio-capable model behind an OpenAI-compatible chat endpoint
    (google/gemini-3.5-flash, google/gemini-3.7-flash, openai/gpt-audio,
    mistralai/voxtral-small-24b-2507, …).

    Two things make a hosted model a poor fit for the DECODER slot, and both are
    properties of the architecture rather than of any particular model:

      * energy attribution needs per-word times, and a chat model has to be
        *asked* for them in text - they are a generation, not a measurement, so
        their accuracy has to be established before they can be trusted;
      * the decoder is called once per VAD segment per track, which for a
        51-minute five-track meeting is thousands of calls of private audio to a
        third party.

    The slot a hosted model actually fits is the VERIFIER (see OpenRouterVerifier):
    bounded calls, no timestamp requirement, and it only ever adjudicates
    candidates that the local energy stage already flagged. That is what D-683
    specifies and what the 2026-08-27 audit did with blind Gemini passes.
    """
    name = "openrouter"

    def __init__(self, model, api_key=None, base_url="https://openrouter.ai/api/v1",
                 timeout=180, want_timestamps=True):
        import os
        self.model = model
        self.name = f"openrouter:{model}"
        self.api_key = api_key or os.environ["OPENROUTER_API_KEY"]
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.want_timestamps = want_timestamps
        self.calls = 0
        self.prompt_tokens = 0
        self.completion_tokens = 0

    # -- audio packing ------------------------------------------------
    @staticmethod
    def _wav_b64(samples, sr=SR):
        import base64, io, wave
        pcm = np.clip(np.asarray(samples, dtype=np.float32), -1.0, 1.0)
        pcm = (pcm * 32767.0).astype("<i2")
        buf = io.BytesIO()
        with wave.open(buf, "wb") as w:
            w.setnchannels(1); w.setsampwidth(2); w.setframerate(sr)
            w.writeframes(pcm.tobytes())
        return base64.b64encode(buf.getvalue()).decode("ascii")

    def _chat(self, parts, max_tokens=2000, temperature=0.0):
        import json, urllib.request
        body = json.dumps({
            "model": self.model,
            "messages": [{"role": "user", "content": parts}],
            "max_tokens": max_tokens,
            "temperature": temperature,
        }).encode()
        req = urllib.request.Request(
            f"{self.base_url}/chat/completions", data=body,
            headers={"Authorization": f"Bearer {self.api_key}",
                     "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=self.timeout) as r:
            payload = json.loads(r.read())
        self.calls += 1
        usage = payload.get("usage") or {}
        self.prompt_tokens += usage.get("prompt_tokens", 0) or 0
        self.completion_tokens += usage.get("completion_tokens", 0) or 0
        return payload["choices"][0]["message"]["content"]

    TS_PROMPT = (
        "Transcribe this audio verbatim in English. Return ONLY a JSON array, no prose and no "
        "code fence, of objects {\"w\": <word>, \"s\": <start seconds>, \"e\": <end seconds>}. "
        "One object per spoken word, in order, times relative to the start of THIS clip. "
        "If nothing is said, return []."
    )
    PLAIN_PROMPT = (
        "Transcribe this audio verbatim in English. Return only the transcript text, "
        "no commentary. If nothing is said, return an empty string."
    )

    def decode(self, samples):
        parts = [
            {"type": "text",
             "text": self.TS_PROMPT if self.want_timestamps else self.PLAIN_PROMPT},
            {"type": "input_audio",
             "input_audio": {"data": self._wav_b64(samples), "format": "wav"}},
        ]
        try:
            raw = self._chat(parts)
        except Exception as exc:
            print(f"      [openrouter] {type(exc).__name__}: {exc}")
            return []
        if not self.want_timestamps:
            return spread_words(raw.strip(), len(samples))
        words = parse_timed_json(raw)
        if words is None:
            # model ignored the schema — keep the text, flag the timing as unusable
            self.emits_word_times = False
            return spread_words(strip_fence(raw), len(samples))
        return words


def strip_fence(text):
    t = (text or "").strip()
    if t.startswith("```"):
        t = t.split("\n", 1)[-1]
        if t.endswith("```"):
            t = t[:-3]
    return t.strip()


def parse_timed_json(raw):
    """-> [(word, start_ms, end_ms)] or None when the reply is not the schema."""
    import json, re
    t = strip_fence(raw)
    m = re.search(r"\[.*\]", t, re.S)
    if not m:
        return None
    try:
        arr = json.loads(m.group(0))
    except Exception:
        return None
    if not isinstance(arr, list):
        return None
    out = []
    for item in arr:
        if not isinstance(item, dict):
            return None
        w = item.get("w") or item.get("word")
        s, e = item.get("s", item.get("start")), item.get("e", item.get("end"))
        if w is None or s is None or e is None:
            return None
        try:
            out.append((str(w), int(float(s) * 1000), int(float(e) * 1000)))
        except (TypeError, ValueError):
            return None
    return out


# ------------------------------------------------------------------ Gemini Transcribe
class GeminiTranscribeBackend(Backend):
    """
    google gemini-3.5-transcribe via the Interactions API.

    Unlike a general chat model, this one *measures* rather than generates: it
    returns `word_info` annotations carrying `start_offset`, `end_offset` and a
    `speaker` label per word. That makes it the first backend that satisfies the
    architecture's contract natively - and the speaker field means it can also
    fill the stage-3 slot, splitting a shared microphone without a separate
    diarizer.

    Privacy: this sends audio to Google. Fine for the synthetic TTS corpus; for
    real meeting audio it is a data-processing decision (see docs/privacy.md),
    not a technical one, and the local sherpa path remains the option that keeps
    audio in-house.
    """
    name = "gemini-3.5-transcribe"
    emits_word_times = True
    emits_speakers = True

    ENDPOINT = "https://generativelanguage.googleapis.com/v1beta/interactions"

    def __init__(self, api_key=None, key_file=None, model="gemini-3.5-transcribe",
                 diarize=True, timeout=300):
        import os
        if api_key is None and key_file:
            api_key = open(key_file).read().strip()
        self.api_key = api_key or os.environ["GEMINI_API_KEY"]
        self.model = model
        self.name = model
        self.diarize = diarize
        self.timeout = timeout
        self.calls = 0
        self.audio_tokens = 0
        self.last_speakers = []

    def _post(self, samples):
        import base64, io, json, urllib.request, wave
        pcm = np.clip(np.asarray(samples, dtype=np.float32), -1.0, 1.0)
        pcm = (pcm * 32767.0).astype("<i2")
        buf = io.BytesIO()
        with wave.open(buf, "wb") as w:
            w.setnchannels(1); w.setsampwidth(2); w.setframerate(SR)
            w.writeframes(pcm.tobytes())
        mode = {"type": "verbatim", "timestamp_granularities": ["word"]}
        if self.diarize:
            mode["diarization_mode"] = "speaker"
        body = {
            "model": self.model,
            "input": [{"type": "audio",
                       "data": base64.b64encode(buf.getvalue()).decode(),
                       "mime_type": "audio/wav"}],
            "generation_config": {"transcription_config": {"mode": mode}},
        }
        req = urllib.request.Request(
            self.ENDPOINT, data=json.dumps(body).encode(),
            headers={"x-goog-api-key": self.api_key, "Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=self.timeout) as r:
            payload = json.loads(r.read())
        self.calls += 1
        usage = payload.get("usage") or {}
        for m in usage.get("input_tokens_by_modality") or []:
            if m.get("modality") == "audio":
                self.audio_tokens += m.get("tokens", 0) or 0
        return payload

    @staticmethod
    def _secs(v):
        if v is None:
            return None
        s = str(v)
        return float(s[:-1]) if s.endswith("s") else float(s)

    def annotations(self, samples):
        """-> [(word, start_ms, end_ms, speaker_label)]"""
        try:
            payload = self._post(samples)
        except Exception as exc:
            print(f"      [gemini] {type(exc).__name__}: {exc}")
            return []
        out = []
        for step in payload.get("steps") or []:
            for content in step.get("content") or []:
                for a in content.get("annotations") or []:
                    if a.get("type") != "word_info":
                        continue
                    s, e = self._secs(a.get("start_offset")), self._secs(a.get("end_offset"))
                    if s is None or e is None:
                        continue
                    out.append((a.get("text", ""), int(s * 1000), int(e * 1000),
                                a.get("speaker")))
        out.sort(key=lambda r: r[1])
        return out

    def decode(self, samples):
        rows = self.annotations(samples)
        self.last_speakers = [r[3] for r in rows]
        return [(w, s, e) for (w, s, e, _spk) in rows]
