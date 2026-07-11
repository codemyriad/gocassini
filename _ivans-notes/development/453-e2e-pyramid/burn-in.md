# Faithful installed-ExApp CPU burn-in

Date: 2026-07-10–11 UTC.

## Fixed inputs

- Product/harness commit: `b7642bc` (`test(e2e): record through a HaRP-installed ExApp`)
- Source ref: `cassini-exapp:d453-current`
- Source and installed image ID: `sha256:415502ef36bbaf195541ed2e47a753ad076d7a3b4a27612608c5505543e6ba7f`
- Manifest/version: `appinfo/info.xml`, `0.2.0-alpha.3`
- Media: LFS-tracked `showcase-lantern-festival-v1/mira.{ivf,ogg}`
- Topology: local HTTP, full services, AppAPI `docker-install` through HaRP, installed ExApp recording backend
- Command contract: one invocation of `ci-e2e-installed-exapp-talk.sh` and one recording run per attempt; the sequence stopped on any non-zero result. No recording retries, best-of-N, `continue-on-error`, or assertion masking were used.

The durations below run from the validator's pre-recording baseline timestamp through the final top-level summary write. Stack boot time occurred before that interval.

## Consecutive pass ledger

| # | Started UTC | Result | Job ID | Segments | Words | Validation-to-summary | Cleanup | Evidence |
|---:|---|---|---|---:|---:|---:|---|---|
| 1 | 2026-07-10 23:44:04 | pass | `01KX76NNZKAWBQ3XJCAZNC0DRA` | 16 | 16 | 63s | pass | `/tmp/d453-v5-positive/summary.json` |
| 2 | 2026-07-10 23:48:50 | pass | `01KX76YD7Q8D79490DB9CN14EF` | 16 | 16 | 64s | pass | `/tmp/d453-burn-in-2/summary.json` |
| 3 | 2026-07-10 23:51:12 | pass | `01KX772QSGAK7FV0RC3YHRE0HP` | 15 | 15 | 63s | pass | `/tmp/d453-burn-in-3/summary.json` |
| 4 | 2026-07-10 23:53:36 | pass | `01KX7774DWBXBF06EJGX0NM8HV` | 16 | 16 | 63s | pass | `/tmp/d453-burn-in-4/summary.json` |
| 5 | 2026-07-10 23:55:57 | pass | `01KX77BDNJGT9QBKQP36FN565B` | 16 | 16 | 63s | pass | `/tmp/d453-burn-in-5/summary.json` |
| 6 | 2026-07-10 23:58:18 | pass | `01KX77FQRNA51Q514J2P7K13SA` | 16 | 16 | 63s | pass | `/tmp/d453-burn-in-6/summary.json` |
| 7 | 2026-07-11 00:00:39 | pass | `01KX77M121874ZF6S20JMB39GW` | 15 | 15 | 63s | pass | `/tmp/d453-burn-in-7/summary.json` |
| 8 | 2026-07-11 00:02:59 | pass | `01KX77R9SAJTJ9YJ03DDH1JZR4` | 16 | 16 | 63s | pass | `/tmp/d453-burn-in-8/summary.json` |
| 9 | 2026-07-11 00:05:23 | pass | `01KX77WPGTRMCSE9HGH610CDJE` | 16 | 16 | 63s | pass | `/tmp/d453-burn-in-9/summary.json` |
| 10 | 2026-07-11 00:07:44 | pass | `01KX78109H66HCZTA91SZDY3Q5` | 16 | 16 | 63s | pass | `/tmp/d453-burn-in-10/summary.json` |

Result: **10 consecutive product passes**. Every run proved the exact installed image ID, AppAPI/HaRP ownership, both manifest-gated Talk secrets, one newly succeeded job, positive recorder segments, an authenticated portable viewer artifact, decoded words, and complete owned-resource cleanup. There were no product failures or infrastructure outages in this sequence.

The localhost artifact URLs captured in each validator summary were live only for that attempt because successful cleanup removes the stack and its volumes. The observational GitHub job uploads the complete evidence directory with 14-day retention, giving CI attempts durable run and artifact links.

## Sensitivity and cleanup controls

| Control | Expected signal | Observed result | Cleanup |
|---|---|---|---|
| Manifest copy with `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` declaration removed | installed status reports `signaling_internal_secret_configured=false`; stack verification fails before recording | detected as `sensitivity-control-passed` | pass |
| Forced failure immediately after stack setup | non-zero harness result | failed as injected; no recording attempted | pass |
| Stale/ambiguous/failed/zero-segment/corrupt/zero-word artifact fixtures | strict V4 validator rejects each case | all rejected | hermetic |

Control evidence remains at `/tmp/d453-manifest-control/evidence/summary.json` and `/tmp/d453-cleanup-control/summary.json` on the validation host.
