---
shaping: true
---

# X1 Spike: Recording boundary and recoverable record failures

## Result

Your assumptions are **correct**, with one nuance.

1. **Yes** — record-stage post-processing can produce `recording.mkv`.
   - that MKV may exist in a partial/untrustworthy state if remux failed
   - or it may be a usable recording before `.run` finalization completes
2. **Yes** — the final output of the record job, in current supported product terms, is the **ready `.run` directory**.
   - that is the downstream-ready signal the current build flow understands and trusts

So there are really three relevant boundaries:

1. **Session artifact exists** — live packet truth has been captured, but current operator/build surfaces cannot use it directly.
2. **`recording.mkv` exists** — current code can build from it, but this is **not** the selected first-cut rerun signal.
3. **Ready `.run` exists** — current operator/runtime already knows how to use it, and this is the selected first-cut rerun signal.
   - for the selected first cut, this becomes the job's one canonical reusable recording boundary after the initial successful record pass

**Selected recommendation from this spike:**

- every accepted rerun should create a fresh **build+publish** attempt from the captured recording boundary
- **selected first-cut boundary:** one canonical ready `.run`
- **follow-up boundary:** validated raw `recording.mkv`
- **rejected for now:** session-artifact-only failures, because current operator-facing/product-facing code does not expose a supported remux/adopt step for them

---

## Where the boundary lies in code

### 1. `cassini record` prepares a `.run` bundle and launches the talk recorder

In `cassini-go-recorder/internal/cassini/cli.go` the `.run` path does:

1. `PrepareRunBundle(opts.outDir, opts.simulate)`
2. `UpdateRunBundleStatus(..., preparing, "record", ...)`
3. `runRecorderApp(ctx, cfg)`
4. `FinalizeRunBundle(...)`

So a **ready `.run`** does **not** exist until **after** `runRecorderApp(...)` returns successfully **and** `FinalizeRunBundle(...)` succeeds.

### 2. Inside `runRecorderApp`, live capture ends before final MKV composition

`runRecorderApp` is `app.RunContext`, which calls `talk.Run(...)` in `cassini-go-recorder/internal/app/run.go`.

Inside `cassini-go-recorder/internal/talk/recorder.go`:

- `newSessionCaptureArtifact(...)` creates the session-artifact directory under:
  - `<run-root>/sessions/<id>/session.json`
  - `<run-root>/sessions/<id>/events.ndjson`
  - `<run-root>/sessions/<id>/streams/*.rtplog`
- live capture writes packet truth into that session artifact during the meeting
- when the meeting ends, `cleanup(...)` runs
- `cleanup(...)` calls `composeFinalOutput()`
- `composeFinalOutput()` calls `composeFinalOutputFromSessionArtifact()`
- `composeFinalOutputFromSessionArtifact()` calls `remux.BuildFromSession(...)`
- that remux step writes the final `recording.mkv`

That means the **raw-capture boundary** is **not** the same as the **raw-recording boundary**:

- the **session artifact** is persisted during capture
- the **final MKV** is derived only later, during cleanup/remux

### 3. `.run` finalization happens after the final MKV already exists

After `talk.Run(...)` returns successfully, `FinalizeRunBundle(...)` in `cassini-go-recorder/internal/cassini/run_bundle.go`:

- marks the bundle `ready`
- records `recording.path=recording.mkv`
- records `recording.format=mkv`
- normalizes `sessions/<id>` into `session/`
- writes the final `cassini.json`

So the ordering is:

```text
live capture -> session artifact -> remux to recording.mkv -> finalize .run manifest
```

**Boundary conclusion:**

- **ready `.run` boundary** = after remux **and** after bundle finalization
- **raw `recording.mkv` boundary** = after remux, before bundle finalization
- **session artifact boundary** = during/after capture, before remux

---

## Answers

## X1-Q1 — Which steps happen between live capture start and a ready `.run`, and which can fail after the call is already over?

### Concrete step sequence

1. Create `.run` directory and initial `cassini.json` (`PrepareRunBundle`)
2. Create session-artifact directory (`newSessionCaptureArtifact`)
3. Run live Talk capture into session artifact
4. On shutdown, close/flush session artifact (`sessionCaptureArtifact.close`)
5. Remux session artifact into final `recording.mkv` (`composeFinalOutputFromSessionArtifact -> remux.BuildFromSession`)
6. Return from `runRecorderApp`
7. Finalize the `.run` bundle (`FinalizeRunBundle`)

### Steps that can fail after the live conversation is already over

Yes — several important ones:

- session-artifact close/flush/persist in `cleanup(...)`
- remux/composition of `recording.mkv` in `composeFinalOutputFromSessionArtifact()`
- `.run` finalization in `FinalizeRunBundle(...)`

So a `record`-stage failure is **not** proof that the conversation itself must be re-recorded.

---

## X1-Q2 — For each relevant record failure mode, what durable artifacts are left on disk?

| Failure class | Likely durable artifacts | Notes |
|---|---|---|
| Bootstrap / join / early live failure | `.run/cassini.json` in failed `record` state; maybe partial `sessions/<id>/...` | No trustworthy final boundary yet. |
| Live capture succeeded, but session close or remux failed | failed `.run/cassini.json`; `sessions/<id>/session.json`, `events.ndjson`, `streams/*.rtplog`; maybe partial `recording.mkv` | Session artifact is the durable truth; MKV is not guaranteed usable. |
| Remux succeeded, but later cleanup returned another error | failed `.run/cassini.json`; valid `recording.mkv`; session artifact still present | Recoverable in principle, but not the selected first-cut signal. |
| Remux succeeded, but `.run` finalization failed | failed `.run/cassini.json` with failed `finalize` stage; valid `recording.mkv`; session artifact under `sessions/<id>/` | Strongest raw-recording salvage case, still deferred from the first cut. |

### Important nuance

`remux.BuildFromSession(...)` writes directly to the target `recording.mkv` path via ffmpeg.
If remux fails, a file may exist at that path, but current code does **not** make that an atomic “known good” boundary.
So **file existence alone is not enough** to prove a usable recording.

---

## X1-Q3 — Which artifacts are already consumable by current downstream code?

| Artifact | Current downstream support | Evidence |
|---|---|---|
| Ready `.run` bundle | **Yes** | `cassini build` accepts `.run`, but `resolveBuildInput(...)` requires the bundle to be `ready`. |
| Raw `recording.mkv` | **Yes** | `cassini build /path/to/meeting.mkv --out ...` is a supported path in README and `resolveBuildInput(...)`. |
| Failed `.run` bundle | **No** | `resolveBuildInput(...)` rejects non-ready `.run` bundles. |
| Session artifact dir / `session.json` | **No**, not through `cassini build` | Current product build accepts only `.run` or `.mkv`. |
| Session artifact dir / `session.json` via low-level remux tool | **Yes**, but only through `gocassini-remux` | `pkg/core/remux.BuildFromSession(...)` is wired to `cmd/gocassini-remux`, not the operator/product CLI surface. |

### Separate-module consequence

`cassini-operator` and `cassini-go-recorder` are separate Go modules.
So operator cannot just reach into recorder internals like `transcribe.ProbeMKV` or `adoptRunBundle(...)` without either:

- adding a new module dependency / shared package arrangement, or
- duplicating logic, or
- exposing a new CLI/product command boundary

---

## X1-Q4 — If a failed record attempt left `recording.mkv` or session artifacts but no ready `.run`, what adoption/finalization step would be required?

### Case A — `recording.mkv` exists

This is **possible in current code**, but it is **not** the selected first-cut rerun signal.

We can already do one of these without rewriting record logic:

1. **Build directly from MKV**
   - run `cassini build /path/to/recording.mkv --out <meeting>`
   - this bypasses the need for a ready `.run`

2. **Adopt/finalize the failed run bundle first**
   - recorder-side code already has `adoptRunBundle(...)` / `FinalizeRunBundle(...)` logic used by the portable resume flow
   - but that adoption path is **not** exposed for plain `.run` outputs or operator rerun today

**Implication:** raw-MKV salvage does **not** require record-logic rewrite; it requires operator/runtime support for using or validating that MKV.

### Case B — only session artifacts exist, no usable `recording.mkv`

This is **not** supported through current operator/product CLI flow.

To salvage this case we would need a new supported step such as:

- expose `gocassini-remux` through a product/operator command boundary, or
- add a `cassini` command that finalizes/adopts a failed run bundle from session artifacts, or
- teach `cassini build` to accept a session artifact / failed run bundle and perform the remux first

**Implication:** session-artifact-only salvage is a real follow-up.

---

## X1-Q5 — What exact operator eligibility rule should separate “recording broken” from “recording captured”? 

## Selected first-cut rule

1. **If the job has a canonical ready `.run`** → rerun from `build` using that bundle
2. **Else** → reject rerun

### Why this matches the current shaping direction

- capture happens only once
- only the initial successful record pass can ever create the reusable `.run`
- every accepted rerun still creates a fresh attempt that owns its own `build` + `publish`
- publish-only reruns are avoided
- the selected signal is the one current downstream flow already treats as ready and trustworthy
- raw-MKV salvage and session-artifact salvage stay explicit follow-ups instead of implicit first-cut behavior

---

## Recommendation

### Recommendation for shaping

Use this contract:

- canonical ready `.run` → build rerun
- else reject

And keep two larger follow-ups explicit:

- **raw-MKV salvage** — support rerun from validated `recording.mkv`
- **session-artifact salvage** — support finishing/remuxing a failed record attempt when live capture succeeded but only session artifacts exist

### Why this recommendation

- it satisfies the “capture once, rerun downstream from the recording” rule
- it does not require rewriting record logic
- it picks the most conservative and currently supported downstream-ready boundary
- it keeps the operator honest about partial artifacts that current product/operator surfaces cannot yet recover

---

## Follow-ups to capture

### F1 — Support rerun from validated raw `recording.mkv`

Desired outcome:

- if a failed record/finalize attempt has a validated usable raw recording, operator can create a new attempt at `build` without requiring a ready `.run`

### F2 — Support session-artifact salvage without re-recording

Desired outcome:

- if live capture succeeded but the final MKV was never successfully produced, operator can finish/remux from session artifacts instead of rejoining the meeting

### F3 — Optional adopt/finalize path for failed `.run` bundles

Desired outcome:

- operator or product CLI can convert a failed bundle that already has a valid `recording.mkv` into a ready `.run`
- this is optional if build-from-raw-MKV later becomes sufficient for rerun semantics

---

## Bottom line

- **Yes:** a record-stage post-processing pass can produce `recording.mkv`
- **Yes:** the final downstream-ready output of the record job is the ready `.run` directory
- **Yes:** raw-MKV salvage appears possible in current code
- **No:** raw-MKV salvage is not the selected first-cut signal
- **No:** session-artifact-only recovery is not supported through the current operator/product CLI path
- **Therefore:** the right current first-cut shape is **ready `.run` only; raw-MKV and session-artifact salvage are explicit follow-ups**
