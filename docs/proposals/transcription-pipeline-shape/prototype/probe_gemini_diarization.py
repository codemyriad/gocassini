import json,subprocess,sys,time
import numpy as np
sys.path.insert(0,"./")
from asr_backends import GeminiTranscribeBackend, SR
import os
SP=os.environ.get("BENCH_WORK","."); FX=f"{SP}/fx/fixture"
def pcm(p):
    raw=subprocess.run(["ffmpeg","-v","error","-i",p,"-ac","1","-ar","16000","-f","s16le","pipe:1"],
                       capture_output=True,check=True).stdout
    return np.frombuffer(raw,dtype="<i2").astype(np.float32)/32768.0
leo, ana = pcm(f"{FX}/leo.wav"), pcm(f"{FX}/ana.wav")
n=max(len(leo),len(ana)); room=np.zeros(n); room[:len(leo)]+=leo; room[:len(ana)]+=ana
room=(room/max(1.0,np.abs(room).max())).astype(np.float32)
print(f"shared mic: leo + ana on ONE track, {n/SR:.1f}s")

gt=json.load(open(f"{FX}/ground-truth.json"))
turns=[t for t in gt["turns"] if t["speaker"] in ("leo","ana")]
b=GeminiTranscribeBackend(key_file=f"{SP}/gemini.key", diarize=True)
st=time.time(); rows=b.annotations(room); el=time.time()-st
labels=sorted({r[3] for r in rows})
print(f"{el:.1f}s, {b.calls} call, {b.audio_tokens} audio tokens, {len(rows)} words, speakers found: {labels}")

from collections import Counter, defaultdict
hit=defaultdict(Counter)
for t in turns:
    mid=(t["start_seconds"]+t["end_seconds"])/2
    best=None
    for (w,s,e,spk) in rows:
        if s/1000.0 <= mid <= e/1000.0: best=spk; break
    if best is None:  # nearest word
        cand=min(rows,key=lambda r: abs((r[1]/1000.0)-mid), default=None)
        best=cand[3] if cand else None
    hit[t["speaker"]][best]+=1
print("ground-truth speaker -> gemini label:", {k:dict(v) for k,v in hit.items()})
dom={k:v.most_common(1)[0][0] for k,v in hit.items()}
pure=sum(v.most_common(1)[0][1] for v in hit.values()); tot=len(turns)
print(f"dominant mapping: {dom}   turn purity {pure}/{tot}   collapsed={len(set(dom.values()))<len(dom)}")
