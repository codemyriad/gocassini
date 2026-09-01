#!/usr/bin/env python3
"""Can a hosted chat model produce word timestamps good enough for energy attribution?

Ground truth here is exact: the fixture's turn boundaries are known, and the clip is cut
on a turn so the first and last word times are pinned. We measure whether the model's
word times are a measurement or a plausible-looking generation.
"""
import json, sys, time, subprocess
import numpy as np
sys.path.insert(0, "./")
from asr_backends import OpenRouterBackend, SR

FX = "{SP}/fx/fixture"
gt = json.load(open(f"{FX}/ground-truth.json"))
turn = [t for t in gt["turns"] if t["speaker"] == "mira"][0]
t0, t1 = turn["start_seconds"], turn["end_seconds"]

raw = subprocess.run(["ffmpeg","-v","error","-ss",str(t0),"-to",str(t1),
                      "-i",f"{FX}/mira.wav","-ac","1","-ar","16000","-f","s16le","pipe:1"],
                     capture_output=True, check=True).stdout
x = np.frombuffer(raw, dtype="<i2").astype(np.float32)/32768.0
dur = len(x)/SR
ref_words = turn["text"].split()
print(f"clip: mira {t0:.2f}-{t1:.2f} ({dur:.2f}s), {len(ref_words)} reference words\n")

for model in ["google/gemini-3.5-flash", "google/gemini-3.7-flash"]:
    b = OpenRouterBackend(model)
    st = time.time()
    words = b.decode(x)
    el = time.time() - st
    ok = b.emits_word_times
    txt = " ".join(w for w,_,_ in words)
    print(f"== {model}")
    print(f"   {el:.1f}s, {b.calls} call, tokens in/out {b.prompt_tokens}/{b.completion_tokens}")
    print(f"   schema honoured: {ok}   words: {len(words)} (ref {len(ref_words)})")
    print(f"   text: {txt[:110]}")
    if words:
        first_s, last_e = words[0][1]/1000.0, words[-1][2]/1000.0
        print(f"   first word starts {first_s:.2f}s (truth ~0.0), last ends {last_e:.2f}s (clip {dur:.2f}s)")
        mono = all(words[i][1] <= words[i+1][1] for i in range(len(words)-1))
        inrange = sum(1 for _,s,e in words if 0 <= s <= dur*1000+500 and 0 <= e <= dur*1000+500)
        print(f"   monotonic: {mono}   within clip bounds: {inrange}/{len(words)}")
        # duration-coverage sanity: do the spans tile the clip plausibly?
        span = (last_e - first_s)
        print(f"   covered span {span:.2f}s vs clip {dur:.2f}s  -> ratio {span/dur:.2f}")
    print()
