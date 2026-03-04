# Timeline Estimation

## Goals

- deterministic remuxing from the same rtplog input
- monotonic PTS output even with reordered or missing packets
- bounded behavior in the face of SSRC churn

## Baseline

Per stream segment:

- `t0 = recvMonoNS(first packet)`
- `rtp0 = rtp_ts(first packet)`
- `pts = t0 + (rtp_ts - rtp0) * 1e9 / clock_rate`

This gives stable long-term drift characteristics because timestamps are anchored in recorder time.

## Correction (SR-based)

- SR/abs-capture-time observations can be used to refine `pts` with a slow,
  bounded adjustment.
- corrections must never make output timestamps go backwards.

## Current implementation status

- `pkg/core/timeline/estimator.go` now includes SR-aware slope/intercept correction
  on top of the receiver-time canonical mapping
- corrections are bounded per SR update and PTS output is hard-clamped monotonic
- explicit anchor-based cross-track A/V sync remains future work

## Current behavior (implemented)

- `SegmentEstimator` tracks per-SSRC state and unwraps RTP timestamps when needed.
- `ObserveRTP` now accepts `clockRate` and will seed/update stream clock rate metadata.
- `SetClockRate` can be used if clock rate is only known after first packet.
- `ObserveRTCP` parses RTCP Sender Reports and slowly corrects RTP->PTS slope using
  observed SR deltas against local receive time.
- SR correction applies bounded intercept adjustments (`max 5ms` per SR observation)
  so timeline adaptation is gradual.
- PTS output is anchored on first-seen receive timestamp and made monotonic:
  - first `PTS` call for a stream returns the baseline `startMonoNS + (rtp-ts delta / clock_rate)` result
  - subsequent calls are clamped upward if a timestamp would go backwards or stay equal
- Default clock fallback is 90 kHz if none was provided.
