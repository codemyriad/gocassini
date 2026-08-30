#!/usr/bin/env python3
"""
run_real.py — both architectures on a real multitrack meeting.

There is no reference transcript for a real meeting, so this does not report
WER. It reports the things that do not need one:

  * cost      : wall clock and ASR passes
  * bleed     : words whose owning track was NOT the loudest track at that
                instant, relative to each track's own noise floor. For
                Pipeline A that is a fabricated/misattributed word; the
                energy evidence is recorded so a human can adjudicate.
  * agreement : how much text the two architectures share.
"""
import argparse, json, os, time
import numpy as np
from core import Recognizer, probe_mkv, extract_speaker_floats, extract_mix_floats, SR
from pipelines import pipeline_a, pipeline_b, pipeline_d, pipeline_f, track_envelopes

def bleed_audit(words, envs, floors, peaks, hop, margin_db=6.0, pad_ms=40):
    """For each word, rank tracks by level above their own floor."""
    names = list(envs.keys()); n = len(next(iter(envs.values())))
    rows = []
    for w in words:
        if w.speaker is None or w.speaker not in envs:
            continue
        a = max(0, int((w.start_ms - pad_ms) * SR / 1000) // hop)
        b = min(n, int((w.end_ms + pad_ms) * SR / 1000) // hop + 1)
        if b <= a: b = min(n, a + 1)
        sc = {}
        for nm in names:
            seg = envs[nm][a:b]
            k = max(1, len(seg)//2)
            sc[nm] = float(np.mean(np.sort(seg)[-k:]) - floors[nm]) if len(seg) else -999.0
        rank = sorted(sc.items(), key=lambda kv: -kv[1])
        best, bs = rank[0]
        own = sc[w.speaker]
        if best != w.speaker and (bs - own) >= margin_db:
            rows.append(dict(text=w.text, start_ms=w.start_ms, end_ms=w.end_ms,
                             attributed=w.speaker, evidence_best=best,
                             own_db=round(own,1), best_db=round(bs,1),
                             gap_db=round(bs-own,1)))
    return rows

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mkv", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--provider", default="cpu")
    ap.add_argument("--threads", type=int, default=12)
    ap.add_argument("--model-dir", required=True)
    ap.add_argument("--vad", required=True)
    ap.add_argument("--limit-seconds", type=float, default=0)
    a = ap.parse_args()

    mk = lambda: Recognizer(a.model_dir, a.vad, provider=a.provider,
                            num_threads=a.threads, feature_dim=128, int8=True)
    src = a.mkv
    if a.limit_seconds:
        src = "/tmp/_clip.mkv"
        os.system(f"ffmpeg -v error -y -t {a.limit_seconds} -i {a.mkv} -map 0:a -c:a copy {src}")

    streams, dur_ms = probe_mkv(src)
    print(f"== {os.path.basename(a.mkv)}: {len(streams)} tracks, {dur_ms/1000:.0f}s")
    out = dict(corpus=os.path.basename(a.mkv), audio_seconds=dur_ms/1000.0,
               tracks=[s["speaker"] for s in streams], provider=a.provider, systems={})

    ra = pipeline_a(src, mk); print(f"  A: {ra['seconds']:.0f}s, {len(ra['words'])} words, {ra['asr_passes']} passes")
    rb = pipeline_b(src, mk); print(f"  B: {rb['seconds']:.0f}s, {len(rb['words'])} words, {rb['asr_passes']} pass")
    rd = pipeline_d(src, mk); print(f"  D: {rd['seconds']:.0f}s, {len(rd['words'])} words, dropped {rd['dropped']}")
    rf = pipeline_f(src, mk); print(f"  F: {rf['seconds']:.0f}s, {len(rf['words'])} words, dropped {rf['dropped']}, quiet-kept {rf['quiet_kept']}")

    envs, floors, peaks, hop = rb["envs"], rb["floors"], rb["peaks"], rb["hop"]
    for nm, r in (("A_status_quo", ra), ("B_mix_single_pass", rb), ("D_pertrack_energy_drop", rd), ("F_pertrack_energy_corroborated", rf)):
        rows = bleed_audit(r["words"], envs, floors, peaks, hop)
        att = sum(1 for w in r["words"] if w.speaker is not None)
        out["systems"][nm] = dict(
            seconds=r["seconds"], rtf=r["seconds"]/(dur_ms/1000.0),
            asr_passes=r["asr_passes"], n_words=len(r["words"]), attributed=att,
            unattributed=len(r["words"])-att,
            contradicted_words=len(rows),
            contradicted_rate=(len(rows)/att if att else 0.0),
            per_speaker={s: sum(1 for w in r["words"] if w.speaker == s) for s in out["tracks"]},
            dropped=r.get("dropped"),
            bleed_examples=sorted(rows, key=lambda x: -x["gap_db"])[:40],
            words=[w.as_dict() for w in r["words"]],
        )
        print(f"    {nm}: attributed={att} contradicted-by-energy={len(rows)} ({100*len(rows)/max(att,1):.1f}%)")
    json.dump(out, open(a.out, "w"), indent=1)
    print("  wrote", a.out)

if __name__ == "__main__":
    main()
