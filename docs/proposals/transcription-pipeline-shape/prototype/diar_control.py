#!/usr/bin/env python3
"""Negative control: does the diarization stage invent extra people on real
single-participant tracks? Run on the real meeting, where every track is one
person at a laptop."""
import argparse, json, time
from core import probe_mkv, decode_tracks, SR
from pipelines import track_envelopes
from diarize import make_diarizer, split_multi_speaker_tracks

ap = argparse.ArgumentParser()
ap.add_argument("--mkv", required=True); ap.add_argument("--seg", required=True)
ap.add_argument("--emb", required=True); ap.add_argument("--out", required=True)
ap.add_argument("--threads", type=int, default=12)
ap.add_argument("--threshold", type=float, default=0.6)
ap.add_argument("--owned-only", action="store_true")
a = ap.parse_args()

streams, dur = probe_mkv(a.mkv)
tracks = decode_tracks(a.mkv, streams)
d = make_diarizer(a.seg, a.emb, num_speakers=-1, threshold=a.threshold,
                  provider="cpu", num_threads=a.threads)
t0 = time.time()
kw = {}
if a.owned_only:
    n = max(len(v) for v in tracks.values())
    e, f, pk, hp = track_envelopes(tracks, n)
    kw = dict(envs=e, floors=f, peaks=pk, hop=hp)
info = split_multi_speaker_tracks(tracks, d, **kw)
el = time.time() - t0
out = {}
for k, v in info.items():
    out[k] = dict(n_clusters=v["n_speakers"], flagged_shared=v["shared"],
                  speech_seconds=round(v.get("total", 0.0), 1),
                  top_durations=sorted([round(x,1) for x in v.get("durations",{}).values()], reverse=True)[:4])
    print(f"  {k:24s} clusters={v['n_speakers']:2d} shared={str(v['shared']):5s} "
          f"speech={v.get('total',0):7.1f}s top={out[k]['top_durations']}")
print(f"diarization of {len(tracks)} tracks x {dur/1000:.0f}s took {el:.0f}s (RTF {el/(dur/1000*len(tracks)):.3f} per track-second)")
json.dump(dict(seconds=el, audio_seconds=dur/1000.0, n_tracks=len(tracks),
               threshold=a.threshold, tracks=out), open(a.out,"w"), indent=1)
