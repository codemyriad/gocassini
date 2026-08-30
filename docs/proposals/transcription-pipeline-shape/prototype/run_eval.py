#!/usr/bin/env python3
"""run_eval.py — run both architectures over a corpus and score them."""
import argparse, json, sys, time, os
import numpy as np
from core import Recognizer, probe_mkv, extract_mix_floats
from pipelines import pipeline_a, pipeline_b, pipeline_c, pipeline_d, pipeline_e, pipeline_f, pipeline_g
from diarize import make_diarizer
import score as S

MODELS = "/work/models"

def make_rec_factory(provider, threads, precision):
    d = (f"{MODELS}/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8" if precision == "int8"
         else f"{MODELS}/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3")
    def f():
        return Recognizer(d, f"{MODELS}/silero_vad.onnx", provider=provider,
                          num_threads=threads, feature_dim=128, int8=(precision == "int8"))
    return f

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mkv", required=True)
    ap.add_argument("--clean-mkv", required=True, help="corpus with isolated tracks, for ground-truth activity")
    ap.add_argument("--ref", required=True)
    ap.add_argument("--provider", default="cpu")
    ap.add_argument("--threads", type=int, default=16)
    ap.add_argument("--precision", default="int8", choices=["int8", "fp32"])
    ap.add_argument("--out", required=True)
    ap.add_argument("--speaker-map", default="", help="json mapping hyp speaker -> ref speaker")
    ap.add_argument("--with-diarization", action="store_true")
    ap.add_argument("--diar-threshold", type=float, default=0.6)
    a = ap.parse_args()

    smap = json.loads(a.speaker_map) if a.speaker_map else None
    turns, meta = S.load_reference(a.ref)
    mk = make_rec_factory(a.provider, a.threads, a.precision)

    print(f"== {os.path.basename(a.mkv)}  provider={a.provider} precision={a.precision} threads={a.threads}")
    mkdiar = lambda: make_diarizer(
        f"{MODELS}/sherpa-onnx-pyannote-segmentation-3-0/model.onnx",
        f"{MODELS}/nemo_en_titanet_small.onnx",
        num_speakers=-1, threshold=a.diar_threshold,
        provider=a.provider, num_threads=a.threads)

    systems = [("A_status_quo", pipeline_a), ("B_mix_single_pass", pipeline_b),
               ("D_adaptive_threshold", pipeline_d),
               ("D18_fixed_18db", lambda m,f,log=print: pipeline_d(m,f,log=log,margin_db=18.0))]
    if a.with_diarization:
        systems.append(("C_mix_plus_diarization",
                        lambda m, f, log=print: pipeline_c(m, f, mkdiar, log)))
        systems.append(("E_pertrack_energy_plus_diarization",
                        lambda m, f, log=print: pipeline_e(m, f, mkdiar, log)))
        systems.append(("G_corroborated_plus_diarization",
                        lambda m, f, log=print: pipeline_g(m, f, mkdiar, log)))
    res = {}
    for name, fn in systems:
        print(f"  -- {name}")
        r = fn(a.mkv, mk)
        res[name] = r
        print(f"    {name}: {r['seconds']:.1f}s wall, {r['asr_passes']} ASR pass(es)")

    # ground-truth activity from the clean isolated tracks
    n_samples = len(extract_mix_floats(a.clean_mkv, probe_mkv(a.clean_mkv)[0]))
    activity, ghop = S.reference_activity(a.clean_mkv, n_samples)

    report = dict(corpus=os.path.basename(a.mkv), provider=a.provider,
                  precision=a.precision, threads=a.threads,
                  audio_seconds=res["A_status_quo"]["duration_ms"] / 1000.0, systems={})
    for name, r in res.items():
        w, nref, nhyp = S.plain_wer(turns, r["words"])
        cp, per, mapping = S.cp_wer(turns, r["words"], smap)
        fsr, bad, tot = S.false_speaker_rate(r["words"], activity, ghop)
        audio_s = r["duration_ms"] / 1000.0
        report["systems"][name] = dict(
            wer=w, cpwer=cp, per_speaker=per, speaker_mapping=mapping,
            false_speaker_rate=fsr, false_speaker_words=bad, attributed_words=tot,
            n_ref_words=nref, n_hyp_words=nhyp,
            seconds=r["seconds"], rtf=r["seconds"] / audio_s, asr_passes=r["asr_passes"],
            unattributed=sum(1 for x in r["words"] if x.speaker is None),
            shared_tracks=r.get("shared_tracks"), diar_info=r.get("diar_info"),
            dropped=r.get("dropped"), quiet_kept=r.get("quiet_kept"),
            threshold_db=r.get("threshold_db"),
            words=[x.as_dict() for x in r["words"]],
        )
        print(f"    {name}: WER={w:.4f} cpWER={cp:.4f} FSR={fsr:.4f} ({bad}/{tot}) RTF={r['seconds']/audio_s:.3f}")

    json.dump(report, open(a.out, "w"), indent=1)
    print(f"  wrote {a.out}")

if __name__ == "__main__":
    main()
