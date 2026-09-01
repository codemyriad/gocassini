"""
pipelines.py — the two architectures under test.

A (status quo)  : N ASR passes, one per isolated participant track.
                  Speaker identity == which decoder pass produced the word.

B (proposal)    : 1 ASR pass over the lossless mix, then attribute each word
                  to a track by relative energy. Speaker identity is computed
                  against a shared timeline, decoupled from decoding.
"""
import time
import numpy as np
from core import (SR, probe_mkv, extract_speaker_floats, extract_mix_floats,
                  decode_tracks, mix_from_tracks, Recognizer, Word)

# ------------------------------------------------------------------ A
def pipeline_a(mkv, make_rec, log=print):
    """Status quo: per-track VAD + ASR. One recognizer, streams in sequence."""
    streams, dur_ms = probe_mkv(mkv)
    rec = make_rec()
    t0 = time.time()
    words = []
    for s in streams:
        samples = extract_speaker_floats(mkv, s)
        ws = rec.transcribe(samples, use_vad=True)
        for w in ws:
            w.speaker = s["speaker"]
        log(f"    [A] {s['speaker']:14s} {len(ws):4d} words")
        words.extend(ws)
    words.sort(key=lambda w: (w.start_ms, w.end_ms))
    return dict(words=words, streams=streams, seconds=time.time() - t0,
                asr_passes=len(streams), duration_ms=dur_ms)


# ------------------------------------------------------------------ energy attribution
FRAME_MS, HOP_MS = 32, 16          # D-683 asks for 20-32 ms log-RMS frames


def track_envelopes(tracks, n_samples):
    """Per-track log-RMS envelope on the shared timeline, plus each track's own
    noise floor and speech level. numpy only - the decode already happened."""
    frame, hop = int(SR * FRAME_MS / 1000), int(SR * HOP_MS / 1000)
    n_frames = max(1, 1 + (n_samples - frame) // hop)
    envs, floors, peaks = {}, {}, {}
    for name, x in tracks.items():
        if len(x) < n_samples:
            x = np.concatenate([x, np.zeros(n_samples - len(x), dtype=np.float32)])
        x = x[:n_samples]
        win = np.lib.stride_tricks.sliding_window_view(x, frame)[::hop][:n_frames]
        rms = np.sqrt(np.mean(win.astype(np.float64) ** 2, axis=1) + 1e-12)
        db = 20 * np.log10(rms + 1e-12)
        envs[name] = db
        # Per-track adaptive levels: relative to this track's OWN distribution,
        # never one global dB threshold (D-683 requirement).
        floors[name] = float(np.percentile(db, 20))
        peaks[name] = float(np.percentile(db, 97))
    return envs, floors, peaks, hop


def attribute_words(words, envs, floors, peaks, hop,
                    margin_db=6.0, active_frac=0.25, pad_ms=40):
    """
    Assign each word to the track that owns it.

    Score for track t over the word's window is the mean of the loudest 50% of
    frames, expressed as (level - t's own noise floor) normalised by t's own
    speech dynamic range. Everything is per-track relative, so a participant
    recording 30 dB hotter than another cannot win on absolute loudness alone.

    A word whose best track does not beat the runner-up by margin_db, or whose
    best track is not meaningfully above its own floor, is left unattributed
    rather than guessed.
    """
    names = list(envs.keys())
    rng = {n: max(6.0, peaks[n] - floors[n]) for n in names}
    n_frames = len(next(iter(envs.values())))
    out = []
    for w in words:
        a = max(0, int((w.start_ms - pad_ms) * SR / 1000) // hop)
        b = min(n_frames, int((w.end_ms + pad_ms) * SR / 1000) // hop + 1)
        if b <= a:
            b = min(n_frames, a + 1)
        scores = {}
        for n in names:
            seg = envs[n][a:b]
            if len(seg) == 0:
                scores[n] = -999.0; continue
            k = max(1, len(seg) // 2)
            top = np.sort(seg)[-k:]
            scores[n] = float(np.mean(top) - floors[n])
        ranked = sorted(scores.items(), key=lambda kv: -kv[1])
        best, bs = ranked[0]
        second = ranked[1][1] if len(ranked) > 1 else -999.0
        if bs < active_frac * rng[best] or (bs - second) < margin_db:
            w.speaker = None
        else:
            w.speaker = best
        out.append(w)
    return out


# ------------------------------------------------------------------ B
def pipeline_b(mkv, make_rec, log=print):
    """Proposal: one ASR pass over the lossless mix + energy attribution."""
    streams, dur_ms = probe_mkv(mkv)
    rec = make_rec()
    t0 = time.time()
    tracks = decode_tracks(mkv, streams)
    mix = mix_from_tracks(tracks)
    words = rec.transcribe(mix, use_vad=True)
    log(f"    [B] mix ({len(mix)/SR:.1f}s) -> {len(words)} words in one pass")
    envs, floors, peaks, hop = track_envelopes(tracks, len(mix))
    words = attribute_words(words, envs, floors, peaks, hop)
    words.sort(key=lambda w: (w.start_ms, w.end_ms))
    unk = sum(1 for w in words if w.speaker is None)
    log(f"    [B] attributed {len(words)-unk}/{len(words)} words ({unk} left unattributed)")
    return dict(words=words, streams=streams, seconds=time.time() - t0,
                asr_passes=1, duration_ms=dur_ms,
                envs=envs, floors=floors, peaks=peaks, hop=hop)


# ------------------------------------------------------------------ C
def pipeline_c(mkv, make_rec, make_diar, log=print):
    """
    Proposal + diarization: identical to B, plus a second attribution stage that
    splits any track carrying more than one person.

    The ASR side is untouched - still one pass over the mix. Diarization only
    refines the LABEL, which is the whole point: because speaker identity is
    computed against the timeline instead of being the decoder-pass index, a new
    "who spoke when" provider can be composed in without touching decoding.
    """
    import time
    from diarize import split_multi_speaker_tracks, refine_speaker
    from core import decode_tracks, mix_from_tracks

    streams, dur_ms = probe_mkv(mkv)
    rec = make_rec()
    t0 = time.time()
    tracks = decode_tracks(mkv, streams)
    mix = mix_from_tracks(tracks)
    words = rec.transcribe(mix, use_vad=True)
    envs, floors, peaks, hop = track_envelopes(tracks, len(mix))
    words = attribute_words(words, envs, floors, peaks, hop)

    diar = make_diar()
    info = split_multi_speaker_tracks(tracks, diar, envs=envs, floors=floors,
                                      peaks=peaks, hop=hop)
    shared = [k for k, v in info.items() if v["shared"]]
    log(f"    [C] diarizer: shared tracks = {shared or 'none'}")
    for w in words:
        if w.speaker is not None:
            w.speaker = refine_speaker(w, w.speaker, info)
    words.sort(key=lambda w: (w.start_ms, w.end_ms))
    return dict(words=words, streams=streams, seconds=time.time() - t0,
                asr_passes=1, duration_ms=dur_ms, shared_tracks=shared,
                diar_info={k: dict(n_speakers=v["n_speakers"], shared=v["shared"])
                           for k, v in info.items()})


# ------------------------------------------------------------------ D
# The margin that separates crosstalk from genuine simultaneous speech.
#
# Chosen from the ground-truth gap distribution, not by taste. On the crosstalk
# fixture, words whose speaker was genuinely talking sit at a median gap of
# 0.0 dB (p95 = 5.5); words whose speaker was silent - pure bleed - sit at a
# median of 34.2 dB. The two populations barely touch. At 18 dB the rule drops
# 140 of 140 bleed words and 2 of 586 real ones.
#
# It also matches the real meeting: the 84 words the status quo publishes there
# against the acoustic evidence have a minimum gap of 19.6 dB and a median of
# 31.7 dB. An earlier 6 dB margin sat inside the *good* population's tail and
# deleted ~5% of correct words, which is what made variant D look bad on clean
# audio.
CROSSTALK_MARGIN_DB = 18.0


def word_gaps(words, envs, floors, hop, pad_ms=40):
    """For every word: how far (dB) the loudest track sits above the track the
    word was attributed to, each measured against its own noise floor."""
    names = list(envs.keys())
    n_frames = len(next(iter(envs.values())))
    out = []
    for w in words:
        a = max(0, int((w.start_ms - pad_ms) * SR / 1000) // hop)
        b = min(n_frames, int((w.end_ms + pad_ms) * SR / 1000) // hop + 1)
        if b <= a:
            b = min(n_frames, a + 1)
        sc = {}
        for nm in names:
            seg = envs[nm][a:b]
            k = max(1, len(seg) // 2)
            sc[nm] = float(np.mean(np.sort(seg)[-k:]) - floors[nm]) if len(seg) else -999.0
        best = max(sc, key=sc.get)
        out.append(sc[best] - sc[w.speaker])
    return np.asarray(out)


def estimate_crosstalk_threshold(gaps, min_db=8.0, min_mass=0.005,
                                 max_upper_spread_db=5.0, min_separation_db=12.0):
    """
    Estimate this meeting's crosstalk threshold from its own gap distribution,
    rather than hard-coding a dB constant.

    Where crosstalk lands is a property of the room and the gain staging - ~34 dB
    on our synthetic fixture, 34.4-37.5 dB in D-683's measurement, and much lower
    for a speakerphone in a shared office. A constant tuned on one corpus
    mis-fires on the next deployment.

    Two clusters are fitted to the gaps and the split is accepted only when the
    upper one actually looks like a crosstalk mode: enough mass, well separated,
    and TIGHT. The tightness test is what stops it firing on cleanly isolated
    tracks, where the gap distribution is a monotone tail rather than a second
    mode - an earlier Otsu version happily split that tail and made things worse
    (cpWER 0.088 -> 0.109 on the clean corpus).

    Returns None for "no crosstalk population here". On the real 51-minute
    meeting it returns None too, because real crosstalk is *diffuse* (roughly 1%
    of words spread over 6-64 dB, since every participant pair has a different
    bleed path) rather than a tight mode. That is a true answer about the audio,
    and it is the reason this proposal recommends annotating rather than
    deleting - see the README.
    """
    g = np.asarray(gaps, dtype=np.float64)
    g = g[np.isfinite(g)]
    if g.size < 50:
        return None
    hi = g[g >= min_db]
    if hi.size < 5 or hi.size / g.size < min_mass:
        return None
    c = np.array([hi.min(), hi.max()], dtype=np.float64)
    if c[1] - c[0] < 1e-6:
        return None
    for _ in range(50):
        lab = np.abs(g[:, None] - c[None, :]).argmin(1)
        for j in (0, 1):
            if (lab == j).any():
                c[j] = g[lab == j].mean()
        c.sort()
    lab = np.abs(g[:, None] - c[None, :]).argmin(1)
    up, lo = g[lab == 1], g[lab == 0]
    if up.size < 5 or up.size / g.size < min_mass or lo.size == 0:
        return None
    if c[1] - c[0] < min_separation_db:
        return None
    if float(up.std()) > max_upper_spread_db:     # a tail, not a mode
        return None
    thr = max(float((c[0] + c[1]) / 2.0), min_db)
    if int((g >= thr).sum()) < 5:
        return None
    return round(thr, 1)


def pipeline_d(mkv, make_rec, log=print, margin_db=None):
    """
    Keep per-track ASR, but stop letting the decoder pass BE the speaker.

    A single pass over the mix turns out to lose a large share of words when
    people talk over each other (measured below), so decoding stays per-track -
    that is where the multitrack capture genuinely pays. What changes is that
    the word's speaker is no longer "whichever pass emitted it": every word is
    re-checked against the per-track energy evidence, and a word whose producing
    track was decisively quieter than another track at that instant is dropped
    as crosstalk rather than published as speech.

    Bleed makes the same utterance appear on several tracks at once, so the copy
    on the owning track survives and the ghost copies are removed. Attribution
    is a stage over a shared timeline, which is what lets a diarizer compose in.
    """
    import time
    from core import decode_tracks

    streams, dur_ms = probe_mkv(mkv)
    rec = make_rec()
    t0 = time.time()
    tracks = decode_tracks(mkv, streams)
    n = max(len(x) for x in tracks.values())
    envs, floors, peaks, hop = track_envelopes(tracks, n)

    words = []
    for s in streams:
        ws = rec.transcribe(tracks[s["speaker"]], use_vad=True)
        for w in ws:
            w.speaker = s["speaker"]
        words.extend(ws)
        log(f"    [D] {s['speaker']:14s} {len(ws):4d} words")

    if margin_db is None:
        gaps = word_gaps(words, envs, floors, hop)
        est = estimate_crosstalk_threshold(gaps)
        if est is None:
            log("    [D] no crosstalk population in this meeting - dropping nothing")
            words.sort(key=lambda w: (w.start_ms, w.end_ms))
            return dict(words=words, streams=streams, seconds=time.time() - t0,
                        asr_passes=len(streams), duration_ms=dur_ms, dropped=0,
                        threshold_db=None, envs=envs, floors=floors, peaks=peaks, hop=hop)
        margin_db = est
        log(f"    [D] estimated crosstalk threshold {margin_db:.1f} dB from this meeting")

    kept, dropped = [], 0
    names = list(envs.keys())
    n_frames = len(next(iter(envs.values())))
    for w in words:
        a = max(0, int((w.start_ms - 40) * SR / 1000) // hop)
        b = min(n_frames, int((w.end_ms + 40) * SR / 1000) // hop + 1)
        if b <= a:
            b = min(n_frames, a + 1)
        sc = {}
        for nm in names:
            seg = envs[nm][a:b]
            k = max(1, len(seg) // 2)
            sc[nm] = float(np.mean(np.sort(seg)[-k:]) - floors[nm]) if len(seg) else -999.0
        best = max(sc, key=sc.get)
        if best != w.speaker and (sc[best] - sc[w.speaker]) >= margin_db:
            dropped += 1
            continue
        kept.append(w)
    kept.sort(key=lambda w: (w.start_ms, w.end_ms))
    log(f"    [D] dropped {dropped} crosstalk words at {margin_db:.1f} dB, kept {len(kept)}")
    return dict(words=kept, streams=streams, seconds=time.time() - t0,
                asr_passes=len(streams), duration_ms=dur_ms, dropped=dropped,
                threshold_db=margin_db,
                envs=envs, floors=floors, peaks=peaks, hop=hop, tracks_decoded=True)


# ------------------------------------------------------------------ E
def pipeline_e(mkv, make_rec, make_diar, log=print):
    """D + diarization: per-track ASR, energy re-attribution, then split any
    track the diarizer shows is carrying more than one person.

    This is the shape the measurements actually support: keep the decoding where
    it is accurate, move the SPEAKER decision out of the decoder, and compose a
    diarizer onto the speaker decision.
    """
    import time
    from diarize import split_multi_speaker_tracks, refine_speaker
    from core import decode_tracks

    t0 = time.time()
    r = pipeline_d(mkv, make_rec, log=log)
    tracks = decode_tracks(mkv, r["streams"])
    diar = make_diar()
    info = split_multi_speaker_tracks(tracks, diar, envs=r["envs"], floors=r["floors"],
                                      peaks=r["peaks"], hop=r["hop"])
    shared = [k for k, v in info.items() if v["shared"]]
    log(f"    [E] diarizer: shared tracks = {shared or 'none'}")
    for w in r["words"]:
        if w.speaker is not None:
            w.speaker = refine_speaker(w, w.speaker, info)
    r["words"].sort(key=lambda w: (w.start_ms, w.end_ms))
    r["seconds"] = time.time() - t0
    r["shared_tracks"] = shared
    r["diar_info"] = {k: dict(n_speakers=v["n_speakers"], shared=v["shared"]) for k, v in info.items()}
    return r


# ------------------------------------------------------------------ F
def pipeline_f(mkv, make_rec, log=print, margin_db=CROSSTALK_MARGIN_DB, time_tol_ms=400):
    """
    D, but a word is only dropped when the evidence is *corroborated*.

    Dropping every word whose track was quieter than another is too blunt: when
    two people genuinely talk at once, the quieter one's real words get deleted.
    Measured on the isolated-track fixture that costs more than bleed costs on
    the crosstalk fixture.

    Bleed has a signature that genuine overlap does not: it DUPLICATES an
    utterance. The same word appears at the same instant on the loud speaker's
    track and, 30-something dB down, on everyone else's. So a word is dropped
    only when the louder track independently produced the same word at the same
    time. A quiet interjection nobody else uttered has no duplicate, and
    survives - which is the D-684 "recover quiet interjections" requirement and
    the D-683 "do not invent a false Okay from bleed" requirement at once.
    """
    import time
    from core import decode_tracks

    streams, dur_ms = probe_mkv(mkv)
    rec = make_rec()
    t0 = time.time()
    tracks = decode_tracks(mkv, streams)
    n = max(len(x) for x in tracks.values())
    envs, floors, peaks, hop = track_envelopes(tracks, n)

    per_track = {}
    for s in streams:
        ws = rec.transcribe(tracks[s["speaker"]], use_vad=True)
        for w in ws:
            w.speaker = s["speaker"]
        per_track[s["speaker"]] = ws
        log(f"    [F] {s['speaker']:14s} {len(ws):4d} words")

    def norm(t):
        return "".join(c for c in t.lower() if c.isalnum())

    index = {name: sorted(ws, key=lambda w: w.start_ms) for name, ws in per_track.items()}

    def said_by(name, text, start_ms, end_ms):
        key = norm(text)
        if not key:
            return False
        for w in index[name]:
            if w.end_ms < start_ms - time_tol_ms:
                continue
            if w.start_ms > end_ms + time_tol_ms:
                break
            if norm(w.text) == key:
                return True
        return False

    names = list(envs.keys())
    n_frames = len(next(iter(envs.values())))
    kept, dropped, quiet_kept = [], 0, 0
    for name, ws in per_track.items():
        for w in ws:
            a = max(0, int((w.start_ms - 40) * SR / 1000) // hop)
            b = min(n_frames, int((w.end_ms + 40) * SR / 1000) // hop + 1)
            if b <= a:
                b = min(n_frames, a + 1)
            sc = {}
            for nm in names:
                seg = envs[nm][a:b]
                k = max(1, len(seg) // 2)
                sc[nm] = float(np.mean(np.sort(seg)[-k:]) - floors[nm]) if len(seg) else -999.0
            louder = [nm for nm in names
                      if nm != name and (sc[nm] - sc[name]) >= margin_db]
            if louder and any(said_by(nm, w.text, w.start_ms, w.end_ms) for nm in louder):
                dropped += 1
                continue
            if louder:
                quiet_kept += 1
            kept.append(w)
    kept.sort(key=lambda w: (w.start_ms, w.end_ms))
    log(f"    [F] dropped {dropped} corroborated crosstalk duplicates; "
        f"kept {quiet_kept} quiet words that had no duplicate; {len(kept)} total")
    return dict(words=kept, streams=streams, seconds=time.time() - t0,
                asr_passes=len(streams), duration_ms=dur_ms, dropped=dropped,
                quiet_kept=quiet_kept, envs=envs, floors=floors, peaks=peaks, hop=hop)


# ------------------------------------------------------------------ G
def pipeline_g(mkv, make_rec, make_diar, log=print):
    """The proposal, complete: per-track ASR, corroborated crosstalk removal,
    then diarizer refinement of any track carrying more than one person."""
    import time
    from diarize import split_multi_speaker_tracks, refine_speaker
    from core import decode_tracks

    t0 = time.time()
    r = pipeline_f(mkv, make_rec, log=log)
    tracks = decode_tracks(mkv, r["streams"])
    info = split_multi_speaker_tracks(tracks, make_diar(), envs=r["envs"],
                                      floors=r["floors"], peaks=r["peaks"], hop=r["hop"])
    shared = [k for k, v in info.items() if v["shared"]]
    log(f"    [G] diarizer: shared tracks = {shared or 'none'}")
    for w in r["words"]:
        if w.speaker is not None:
            w.speaker = refine_speaker(w, w.speaker, info)
    r["words"].sort(key=lambda w: (w.start_ms, w.end_ms))
    r["seconds"] = time.time() - t0
    r["shared_tracks"] = shared
    r["diar_info"] = {k: dict(n_speakers=v["n_speakers"], shared=v["shared"]) for k, v in info.items()}
    return r
