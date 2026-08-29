#!/usr/bin/env python3
"""How much of the speech in a corpus is simultaneous? The single-pass-over-the-mix
architecture lives or dies on this number, so it has to be measured, not assumed."""
import argparse, json
import numpy as np
from core import probe_mkv, decode_tracks, SR

ap=argparse.ArgumentParser()
ap.add_argument("--mkv"); ap.add_argument("--ground-truth")
a=ap.parse_args()

if a.ground_truth:
    d=json.load(open(a.ground_truth))
    total=d["duration_seconds"]; res=0.01
    n=int(total/res)
    grid=np.zeros(n,dtype=np.int16)
    for t in d["turns"]:
        i0,i1=int(t["start_seconds"]/res),int(t["end_seconds"]/res)
        grid[i0:i1]+=1
    speech=(grid>=1).sum(); ov=(grid>=2).sum(); ov3=(grid>=3).sum()
    print(f"ground truth: speech={speech*res:.0f}s of {total:.0f}s; "
          f"overlapped(>=2)={ov*res:.0f}s = {ov/max(speech,1):.1%} of speech; "
          f">=3 speakers = {ov3/max(speech,1):.1%}")

if a.mkv:
    streams,dur=probe_mkv(a.mkv)
    tracks=decode_tracks(a.mkv,streams)
    n=max(len(x) for x in tracks.values())
    fr,hop=512,256; nf=1+(n-fr)//hop
    act=[]
    for name,x in tracks.items():
        if len(x)<n: x=np.concatenate([x,np.zeros(n-len(x),dtype=np.float32)])
        w=np.lib.stride_tricks.sliding_window_view(x[:n],fr)[::hop][:nf]
        db=20*np.log10(np.sqrt((w.astype(np.float64)**2).mean(1))+1e-12)
        floor=np.percentile(db,20); peak=np.percentile(db,97)
        act.append(db-floor > 0.35*max(6.0,peak-floor))
    A=np.stack(act); cnt=A.sum(0)
    speech=(cnt>=1).sum(); ov=(cnt>=2).sum(); ov3=(cnt>=3).sum()
    s=hop/SR
    print(f"measured    : speech={speech*s:.0f}s of {n/SR:.0f}s; "
          f"overlapped(>=2)={ov*s:.0f}s = {ov/max(speech,1):.1%} of speech; "
          f">=3 = {ov3/max(speech,1):.1%}")
