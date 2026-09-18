# TDT legal-blank diagnostic experiment

`sherpa-onnx-v1.13.7-tdt-legal-blank-experiment.patch` targets the NeMo modified
beam decoder in sherpa-onnx **v1.13.7**. It is an opt-in native experiment, not a
production fix or an upstream patch validated on a general speech benchmark.

The original decoder selects one duration for all token candidates. When this
duration is zero and the token is blank, it advances by one frame but retains
the zero-duration probability in the path score. A blank therefore receives a
score for a transition it did not take.

Run the standalone counterexample from the repository root:

```sh
python3 harness/bin/test_tdt_legal_blank.py
```

The synthetic one-frame model gives these paths:

| Path | Probability |
| --- | ---: |
| Original decoder's empty output: blank scored with duration 0, advanced by 1 | 0.36 |
| Legal empty path: blank with duration 1 | 0.04 |
| Emit A at duration 0, then blank at duration 1 | 0.2916 |

Original beam search chooses empty output; corrected beam and greedy emit A.
This proves a scoring mismatch, not that it caused a particular recording's
omission. NeMo's reference TDT beam implementation explicitly excludes zero
duration for blank and scores the selected positive-duration transition.
[Reference implementation](https://github.com/NVIDIA-NeMo/Speech/blob/main/nemo/collections/asr/parts/submodules/tdt_beam_decoding.py)

After applying the patch and rebuilding the native library:

- With `CASSINI_TDT_LEGAL_BLANK` unset, decoding remains unchanged.
- `CASSINI_TDT_LEGAL_BLANK=1` chooses the highest-probability positive duration
  for blank and uses that duration's log probability. Non-blank duration choices,
  unknown-token handling, beam width, symbol limit, merging, and final ranking
  remain unchanged. The experiment retains the existing contiguous duration-bin
  assumption used by Parakeet (`0,1,2,3,4`).
- `CASSINI_TDT_TRACE=1` emits one summary per decoded utterance to stderr:
  expansions, zero-duration choices, affected blank candidates, total score
  corrections, and winning token count/score. It emits no transcript.

This is a minimal legal-blank correction, **not full joint token-duration beam
search**. Reference beam search also explores alternate durations and merges
equivalent hypotheses. Comparing candidates at different frame offsets alone
does not prove an implementation defect; the reference search also retains
future-frame hypotheses.

For a controlled test, hold frontend, PCM, chunking, bias, model and runtime
constant and toggle only this flag. Record summary traces for the failing crop,
then evaluate omissions, insertions and false speech on independent excerpts.
Do not select the longest output or infer a recording's cause solely from the
synthetic example.

## Separate final-ranking experiment

Apply `sherpa-onnx-v1.13.7-tdt-final-ranking-experiment.patch` **after** the
legal-blank patch. It adds `CASSINI_TDT_SCORE_NORM=1`, independently of the
legal-blank flag. Both remain off by default.

This changes only final TDT hypothesis ranking to raw log score divided by
`token_count + 1`. NeMo's `BeamTDTInfer` defaults `score_norm` to true and its
`sort_nbest` divides by sequence length; its sequence includes the initial blank,
which sherpa's token list omits. This is a reference scoring policy, not a rule
to choose the most words. It cannot recover hypotheses already pruned earlier.
[NeMo ranking](https://github.com/NVIDIA-NeMo/Speech/blob/main/nemo/collections/asr/parts/submodules/tdt_beam_decoding.py#L814)

With `CASSINI_TDT_TRACE=1`, the layered patch additionally reports every surviving
final hypothesis (normally four): token count, frame offset, raw and normalized
scores, and at most eight initial token IDs. It does not print transcript text.
If all survivors lack the speech, investigate earlier candidate expansion or
pruning instead of changing the final ranking. NeMo also searches alternate
durations and merges equivalent paths; this patch implements neither operation.

## Recording results and limits — 17 September 2026

The legal-blank correction produced identical normalized output token sequences
in all 32 tested cases: 16 excerpts under each of two frontend variants, compared
with the same frontend's unmodified beam search. It did **not** restore the
reported Chris or Silvio omissions. The synthetic proof establishes a scoring
inconsistency, not the confirmed cause of these recording failures.

The 48-condition direct-crop experiment further showed:

- Chris's 9.308-second crop with a 500 ms synthetic tail had an empty final
  hypothesis at raw score −15.7268 and a 17-token partial hypothesis at −20.2669.
  Final length normalization selected the partial hypothesis; it could not
  restore the missing prefix, which was absent from every surviving candidate.
- Silvio's crop with the corrected frontend still produced “Yeah” under beam
  search. None of the final hypotheses contained the full phrase.

Neither patch is therefore a demonstrated repair for these omissions. The
traces distinguish final ranking from earlier candidate generation/pruning or
acoustic-input effects, but do not establish which earlier mechanism discarded
the desired speech path. Keep these patches experimental and defaults off.
