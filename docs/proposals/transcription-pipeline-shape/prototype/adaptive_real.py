#!/usr/bin/env python3
"""Does the per-meeting threshold estimator find the crosstalk mode on real audio?"""
import argparse, json, numpy as np
from core import Recognizer, probe_mkv, decode_tracks
from pipelines import pipeline_a, track_envelopes, word_gaps, estimate_crosstalk_threshold
ap=argparse.ArgumentParser()
ap.add_argument("--mkv",required=True); ap.add_argument("--model-dir",required=True)
ap.add_argument("--vad",required=True); ap.add_argument("--threads",type=int,default=12)
ap.add_argument("--out",required=True)
a=ap.parse_args()
mk=lambda: Recognizer(a.model_dir,a.vad,provider="cpu",num_threads=a.threads,feature_dim=128,int8=True)
r=pipeline_a(a.mkv,mk,log=lambda *x:None)
tracks=decode_tracks(a.mkv,r["streams"]); n=max(len(x) for x in tracks.values())
envs,floors,peaks,hop=track_envelopes(tracks,n)
g=word_gaps(r["words"],envs,floors,hop)
thr=estimate_crosstalk_threshold(g)
print("words:",len(g))
print("gap percentiles:", {p:round(float(np.percentile(g,p)),1) for p in (50,90,95,97,99,99.5,100)})
print("estimated threshold:", ("%.1f dB"%thr) if thr else "none (no crosstalk population)")
if thr: print("would drop:", int((g>=thr).sum()), "words (%.2f%%)"%(100*(g>=thr).mean()))
for t in (10,15,18,20,25,30):
    print("  at %2d dB -> %4d words" % (t, int((g>=t).sum())))
json.dump(dict(n=len(g), threshold=thr,
               hist=np.histogram(np.clip(g,0,45),bins=90,range=(0,45))[0].tolist()),
          open(a.out,"w"))
