#!/usr/bin/env python3
"""Distribution of the energy gap for words the status quo publishes, split by
whether ground truth says the attribution is right. Determines whether a hard
energy threshold can separate crosstalk from genuine overlap."""
import argparse, json
import numpy as np
from core import probe_mkv, decode_tracks, SR
from pipelines import track_envelopes, pipeline_a

ap=argparse.ArgumentParser()
ap.add_argument("--mkv", required=True); ap.add_argument("--ref")
ap.add_argument("--model-dir", required=True); ap.add_argument("--vad", required=True)
ap.add_argument("--threads", type=int, default=16)
a=ap.parse_args()

from core import Recognizer
mk=lambda: Recognizer(a.model_dir, a.vad, provider="cpu", num_threads=a.threads,
                      feature_dim=128, int8=True)
r=pipeline_a(a.mkv, mk, log=lambda *x: None)
streams=r["streams"]; tracks=decode_tracks(a.mkv, streams)
n=max(len(x) for x in tracks.values())
envs,floors,peaks,hop=track_envelopes(tracks,n)
names=list(envs.keys()); nf=len(next(iter(envs.values())))

truth=None
if a.ref:
    d=json.load(open(a.ref))
    # frame-level ground truth speaker activity from the turn list
    truth={}
    for nm in names: truth[nm]=np.zeros(nf,dtype=bool)
    for t in d["turns"]:
        i0=int(t["start_seconds"]*SR)//hop; i1=min(nf,int(t["end_seconds"]*SR)//hop+1)
        if t["speaker"] in truth: truth[t["speaker"]][i0:i1]=True

gaps_right, gaps_wrong, gaps_all = [], [], []
for w in r["words"]:
    i0=max(0,int((w.start_ms-40)*SR/1000)//hop); i1=min(nf,int((w.end_ms+40)*SR/1000)//hop+1)
    if i1<=i0: i1=min(nf,i0+1)
    sc={}
    for nm in names:
        seg=envs[nm][i0:i1]; k=max(1,len(seg)//2)
        sc[nm]=float(np.mean(np.sort(seg)[-k:])-floors[nm]) if len(seg) else -999.0
    best=max(sc,key=sc.get); gap=sc[best]-sc[w.speaker]
    gaps_all.append(gap)
    if truth is not None:
        spoke = truth[w.speaker][i0:i1].any() if w.speaker in truth else False
        (gaps_right if spoke else gaps_wrong).append(gap)

def desc(name,g):
    if not g: print(f"  {name}: none"); return
    g=np.array(g)
    print(f"  {name}: n={len(g)} p50={np.percentile(g,50):5.1f} p90={np.percentile(g,90):5.1f} "
          f"p95={np.percentile(g,95):5.1f} p99={np.percentile(g,99):5.1f} max={g.max():5.1f}")
print("energy gap (dB) between the loudest track and the attributed track:")
desc("all words        ", gaps_all)
if truth is not None:
    desc("speaker WAS active", gaps_right)
    desc("speaker was SILENT", gaps_wrong)
    for th in (10,12,15,18,20,25):
        fp=sum(1 for g in gaps_right if g>=th); tp=sum(1 for g in gaps_wrong if g>=th)
        print(f"  threshold {th:2d} dB -> would drop {tp}/{len(gaps_wrong)} bad words, "
              f"{fp}/{len(gaps_right)} good words")
