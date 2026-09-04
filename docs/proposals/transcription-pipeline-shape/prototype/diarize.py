"""
diarize.py — the capability the status quo cannot express.

In the status-quo architecture a speaker IS a track (`speakerIDFromLabel` off
the stream title), so two people sharing one microphone are structurally
un-nameable: there is no place to put the second one.

Once attribution is a stage over a shared timeline, "who spoke when" becomes a
provider, and providers compose:

    track energy  -> which PARTICIPANT DEVICE  (and therefore the real name)
    diarizer      -> how many PEOPLE on that device, and which one

The diarizer here is sherpa-onnx's OfflineSpeakerDiarization. That matters for
cost: sherpa-onnx is already a gocassini dependency, and
SherpaOnnxCreateOfflineSpeakerDiarization is already present in the C API of the
exact version the recorder links (v1.13.7). Only the Go wrapper is
missing. No new runtime dependency - two extra ONNX files on the existing
EnsureModel download path.
"""
import numpy as np
import sherpa_onnx
from core import SR


def make_diarizer(seg_model, emb_model, num_speakers=-1, threshold=0.5,
                  provider="cpu", num_threads=4):
    cfg = sherpa_onnx.OfflineSpeakerDiarizationConfig(
        segmentation=sherpa_onnx.OfflineSpeakerSegmentationModelConfig(
            pyannote=sherpa_onnx.OfflineSpeakerSegmentationPyannoteModelConfig(model=seg_model),
            provider=provider, num_threads=num_threads),
        embedding=sherpa_onnx.SpeakerEmbeddingExtractorConfig(
            model=emb_model, provider=provider, num_threads=num_threads),
        clustering=sherpa_onnx.FastClusteringConfig(
            num_clusters=num_speakers, threshold=threshold),
        min_duration_on=0.25,
        min_duration_off=0.4,
    )
    if not cfg.validate():
        raise RuntimeError("invalid diarization config")
    return sherpa_onnx.OfflineSpeakerDiarization(cfg)


def diarize_track(diar, samples):
    """-> [(start_s, end_s, local_speaker_int)] for one track's audio."""
    if diar.sample_rate != SR:
        raise RuntimeError(f"diarizer wants {diar.sample_rate} Hz")
    res = diar.process(samples).sort_by_start_time()
    return [(s.start, s.end, s.speaker) for s in res]


def owned_audio(tracks, envs, floors, peaks, hop, name, margin_db=6.0, active_frac=0.25):
    """
    The portion of one track's audio that the energy stage says this track
    actually OWNS - i.e. frames where it beats every other track relative to
    each track's own noise floor. Everything else is muted.

    This matters. Run naively on a raw participant track, the diarizer sees the
    crosstalk of every other participant and reports them as extra people on
    that device: on the real 51-minute meeting it flagged 3 of 4 non-empty
    single-person tracks as shared. Diarizing only the owned regions removes the
    confound, because bleed is by definition audio some OTHER track owns.
    """
    x = tracks[name]
    rng = max(6.0, peaks[name] - floors[name])
    others = [n for n in envs if n != name]
    own = envs[name] - floors[name]
    keep = own >= active_frac * rng
    if others:
        best_other = np.max(np.stack([envs[n] - floors[n] for n in others]), axis=0)
        keep &= (own - best_other) >= margin_db
    out = np.zeros_like(x)
    frame = hop * 2
    for i in np.flatnonzero(keep):
        a = i * hop
        b = min(len(x), a + frame)
        out[a:b] = x[a:b]
    return out, float(keep.mean())


def split_multi_speaker_tracks(tracks, diar, min_share=0.12, min_seconds=8.0,
                               envs=None, floors=None, peaks=None, hop=None):
    """
    Run the diarizer on every participant track and report which ones actually
    carry more than one person.

    A track counts as shared only when a second cluster holds at least
    `min_share` of that track's speech and at least `min_seconds` of audio, so a
    couple of stray embeddings on a single-speaker track do not invent a
    phantom participant.
    """
    out = {}
    for name, x in tracks.items():
        if envs is not None:
            x, kept = owned_audio(tracks, envs, floors, peaks, hop, name)
        spans = diarize_track(diar, x)
        if not spans:
            out[name] = dict(spans=[], n_speakers=0, shared=False)
            continue
        dur = {}
        for s, e, k in spans:
            dur[k] = dur.get(k, 0.0) + (e - s)
        total = sum(dur.values())
        ranked = sorted(dur.items(), key=lambda kv: -kv[1])
        shared = (len(ranked) > 1 and ranked[1][1] >= min_seconds
                  and ranked[1][1] >= min_share * total)
        out[name] = dict(spans=spans, n_speakers=len(ranked), shared=shared,
                         durations=dur, total=total)
    return out


def refine_speaker(word, track_name, diar_info):
    """
    Second stage: a word already assigned to a track gets a sub-speaker when
    that track is shared. Returns the final label.
    """
    info = diar_info.get(track_name)
    if not info or not info.get("shared"):
        return track_name
    mid = (word.start_ms + word.end_ms) / 2000.0
    best, best_ov = None, 0.0
    for s, e, k in info["spans"]:
        ov = min(e, word.end_ms / 1000.0) - max(s, word.start_ms / 1000.0)
        if ov > best_ov:
            best_ov, best = ov, k
        if s <= mid <= e and best is None:
            best = k
    return f"{track_name}#{best}" if best is not None else track_name
