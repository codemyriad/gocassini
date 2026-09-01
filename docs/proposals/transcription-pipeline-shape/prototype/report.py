import json, sys
for path in sys.argv[1:]:
    d = json.load(open(path))
    print(f"===== {d['corpus']}  ({d['audio_seconds']:.0f}s, {d['provider']}/{d['precision']})")
    for k, v in d["systems"].items():
        print("  == %s  hyp=%d ref=%d attributed=%d unattr=%d  WER=%.3f cpWER=%.3f  RTF=%.3f passes=%d"
              % (k, v["n_hyp_words"], v["n_ref_words"], v["attributed_words"],
                 v["unattributed"], v["wer"], v["cpwer"], v["rtf"], v["asr_passes"]))
        if v.get("shared_tracks") is not None:
            print("     shared tracks:", v["shared_tracks"])
        for s, p in sorted(v["per_speaker"].items()):
            print("       %-7s ref=%4d hyp=%4d wer=%.3f  <- %s"
                  % (s, p["ref"], p["hyp"], p["wer"], p["assigned_to"]))
