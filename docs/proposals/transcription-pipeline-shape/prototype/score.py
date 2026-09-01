"""
score.py — metrics. Both pipelines are scored identically.

  WER    : plain word error rate over the whole time-ordered transcript.
           Measures recognition only.
  cpWER  : concatenated-speaker WER. Per speaker, the reference words that
           speaker said vs the hypothesis words attributed to them. This is the
           standard meeting-transcription metric because it charges BOTH a
           misrecognised word and a misattributed one. It is the number that
           decides this proposal.
  FSR    : false-speaker rate — share of hypothesis words attributed to a
           speaker who was not speaking at that time (the bleed failure).
"""
import json, re, unicodedata
import numpy as np
import jiwer

_PUNCT = re.compile(r"[^\w\s']")

def norm(text):
    t = unicodedata.normalize("NFKC", text).lower()
    t = t.replace("-", " ").replace("_", " ").replace("/", " ")
    t = _PUNCT.sub(" ", t)
    return " ".join(t.split())

def norm_words(text):
    return norm(text).split()


def load_reference(path):
    d = json.load(open(path))
    turns = [dict(speaker=t["speaker"], start=float(t["start_seconds"]),
                  words=norm_words(t["text"])) for t in d["turns"]]
    turns.sort(key=lambda t: t["start"])
    return turns, d


def wer(ref_words, hyp_words):
    if not ref_words:
        return 0.0 if not hyp_words else 1.0
    return jiwer.wer(" ".join(ref_words), " ".join(hyp_words) or "@@empty@@")


def plain_wer(turns, words):
    ref = [w for t in turns for w in t["words"]]
    hyp = [x for w in words for x in norm_words(w.text)]
    return wer(ref, hyp), len(ref), len(hyp)


def cp_wer(turns, words, speaker_map=None):
    """
    Concatenated-speaker WER under the OPTIMAL assignment of hypothesis speaker
    labels to reference speakers (scipy linear_sum_assignment over a per-pair
    WER cost). This is the standard meeting metric and it is deliberately
    generous to every system: a system is never punished for naming a speaker
    "room-laptop#1" instead of "ana", only for putting the wrong WORDS under a
    speaker. What it does punish is two reference speakers collapsing into one
    hypothesis label - the second one then has no label left to claim, and its
    whole reference is scored as deletions. That is exactly the shared-microphone
    failure, and it is a property of the architecture, not of the naming.
    """
    from scipy.optimize import linear_sum_assignment
    speakers = sorted({t["speaker"] for t in turns})
    ref = {spk: [w for t in turns if t["speaker"] == spk for w in t["words"]]
           for spk in speakers}

    hyp = {}
    for w in words:
        got = w.speaker
        if speaker_map:
            got = speaker_map.get(got, got)
        if got is None:
            continue
        hyp.setdefault(got, []).extend(norm_words(w.text))
    hyp_labels = sorted(hyp)

    # cost[i][j] = word errors if reference speaker i is served by hyp label j
    n, m = len(speakers), len(hyp_labels)
    size = max(n, m)
    cost = np.zeros((size, size))
    for i, spk in enumerate(speakers):
        r = ref[spk]
        for j in range(size):
            h = hyp[hyp_labels[j]] if j < m else []
            cost[i][j] = wer(r, h) * max(len(r), 1)
        if n <= i:
            continue
    for i in range(n, size):
        cost[i][:] = 0.0
    rows, cols = linear_sum_assignment(cost)

    tot_err, tot_len, per, mapping = 0.0, 0, {}, {}
    for i, j in zip(rows, cols):
        if i >= n:
            continue
        spk = speakers[i]
        label = hyp_labels[j] if j < m else None
        h = hyp[label] if label is not None else []
        e = wer(ref[spk], h)
        per[spk] = dict(wer=e, ref=len(ref[spk]), hyp=len(h), assigned_to=label)
        mapping[spk] = label
        tot_err += e * len(ref[spk]); tot_len += len(ref[spk])
    return (tot_err / tot_len if tot_len else 0.0), per, mapping


def reference_activity(clean_mkv, n_samples, hop_ms=16, frame_ms=32, sr=16000):
    """
    Ground-truth 'who spoke when', taken from the CLEAN isolated tracks by
    energy. The TTS fixture has digital silence outside each speaker's turns,
    so this is exact, not estimated.
    """
    from core import probe_mkv, extract_speaker_floats
    streams, _ = probe_mkv(clean_mkv)
    frame, hop = int(sr * frame_ms / 1000), int(sr * hop_ms / 1000)
    n_frames = max(1, 1 + (n_samples - frame) // hop)
    act = {}
    for s in streams:
        x = extract_speaker_floats(clean_mkv, s)
        if len(x) < n_samples:
            x = np.concatenate([x, np.zeros(n_samples - len(x), dtype=np.float32)])
        x = x[:n_samples]
        win = np.lib.stride_tricks.sliding_window_view(x, frame)[::hop][:n_frames]
        db = 20 * np.log10(np.sqrt(np.mean(win.astype(np.float64) ** 2, axis=1)) + 1e-12)
        act[s["speaker"]] = db > -60.0        # far below any real speech, above digital silence
    return act, hop


def false_speaker_rate(words, activity, hop, sr=16000, tol_ms=200):
    """
    Share of attributed words whose assigned speaker was silent at that time.
    This is the bleed failure D-683 documents: a word invented on, or moved to,
    a track that was not speaking.
    """
    n = len(next(iter(activity.values())))
    bad = tot = 0
    for w in words:
        if w.speaker is None or w.speaker not in activity:
            continue
        a = max(0, int((w.start_ms - tol_ms) * sr / 1000) // hop)
        b = min(n, int((w.end_ms + tol_ms) * sr / 1000) // hop + 1)
        tot += 1
        if b > a and not activity[w.speaker][a:b].any():
            bad += 1
    return (bad / tot if tot else 0.0), bad, tot
