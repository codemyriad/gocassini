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

## Correction (optional)

- SR/abs-capture-time observations can be used to refine `pts` with a slow,
  bounded adjustment.
- corrections must never make output timestamps go backwards.

## Current implementation status

- in-progress: `pkg/core/timeline/estimator.go` contains a segment-oriented
  baseline estimator
- SR correction and explicit anchor-based AV synchronization are planned in the next phase
