#!/usr/bin/env python3
"""
build_fixture.py — synthesize a multi-speaker meeting fixture with EXACT
ground truth (speaker, start, end, text) for every turn.

The repo scenario's hand-written start times assume utterances shorter than the
TTS actually produces, so a participant can be scheduled to start talking before
their previous turn has finished. harness/bin/prepare-synthetic-meeting.py
aborts on that ("scenario overlaps two turns for participant ben"), which is why
the committed showcase fixture only contains real speech for 2 of 6 speakers.

Here a turn keeps its scripted start unless its own speaker is still talking, in
which case it slides to just after. Cross-speaker overlap is preserved on
purpose - interruptions are the interesting part of the corpus - and the ground
truth records where every turn actually landed.
"""
import argparse, json
import numpy as np
import soundfile as sf
from kokoro_onnx import Kokoro

LANG = {"a": "en-us", "b": "en-gb"}

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenario", required=True)
    ap.add_argument("--model", required=True)
    ap.add_argument("--voices", required=True)
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--gap", type=float, default=0.25, help="min gap between a speaker's own turns")
    ap.add_argument("--noise-dbfs", type=float, default=-62.0)
    a = ap.parse_args()

    sc = json.load(open(a.scenario))
    parts = {p["id"]: p for p in sc["participants"]}
    k = Kokoro(a.model, a.voices)

    rendered = []
    for t in sorted(sc["turns"], key=lambda x: x["start_seconds"]):
        p = parts[t["speaker"]]
        samples, sr = k.create(t["text"], voice=p["voice"], speed=1.0,
                               lang=LANG.get(p.get("lang_code", "a"), "en-us"))
        rendered.append(dict(speaker=t["speaker"], text=t["text"],
                             wanted=float(t["start_seconds"]),
                             audio=np.asarray(samples, dtype=np.float32), sr=sr))
        print(f"  {t['speaker']:6s} {len(samples)/sr:5.1f}s  {t['text'][:52]}")

    sr = rendered[0]["sr"]
    free = {}
    laid = []
    for r in rendered:
        start = max(r["wanted"], free.get(r["speaker"], 0.0))
        dur = len(r["audio"]) / sr
        free[r["speaker"]] = start + dur + a.gap
        laid.append(dict(speaker=r["speaker"], text=r["text"], start=start,
                         end=start + dur, wanted=r["wanted"],
                         slipped=round(start - r["wanted"], 2), audio=r["audio"]))

    total = max(x["end"] for x in laid) + 1.0
    n = int(total * sr)
    rms = 10 ** (a.noise_dbfs / 20.0)
    rng = np.random.default_rng(7)
    tracks = {}
    for spk in parts:
        # every real track carries a noise floor; an all-zero buffer drives
        # log-mel to -inf and the decoder emits a run of <unk>
        tracks[spk] = (rng.standard_normal(n).astype(np.float32) * rms)
    for x in laid:
        i = int(x["start"] * sr)
        tracks[x["speaker"]][i:i + len(x["audio"])] += x["audio"]

    import os
    os.makedirs(a.out_dir, exist_ok=True)
    for spk, buf in tracks.items():
        peak = float(np.abs(buf).max())
        if peak > 0.99:
            buf = buf / peak * 0.99
        sf.write(f"{a.out_dir}/{spk}.wav", buf, sr)
    gt = dict(scenario_id=sc["scenario_id"] + "-rebuilt", sample_rate=sr,
              duration_seconds=round(total, 3),
              participants=[dict(id=p["id"], display_name=p["display_name"],
                                 voice=p["voice"], lang_code=p.get("lang_code", "a"))
                            for p in sc["participants"]],
              turns=[dict(speaker=x["speaker"], start_seconds=round(x["start"], 3),
                          end_seconds=round(x["end"], 3), text=x["text"],
                          scripted_start=x["wanted"], slipped_seconds=x["slipped"])
                     for x in sorted(laid, key=lambda z: z["start"])])
    json.dump(gt, open(f"{a.out_dir}/ground-truth.json", "w"), indent=1)
    slipped = [t for t in gt["turns"] if t["slipped_seconds"] > 0.01]
    print(f"\n{len(gt['turns'])} turns, {total:.1f}s, {len(tracks)} speakers; "
          f"{len(slipped)} turns slid to avoid self-overlap")

if __name__ == "__main__":
    main()
