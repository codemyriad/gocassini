# User Review Notes: Current Cassini Surface

Date: 2026-03-10

## What I Think The Project Does

Cassini is now understandable as one product:

- record a Nextcloud Talk meeting into a `.run` bundle
- build that recording into a `.meeting` bundle with transcript/viewer artifacts
- publish one or more meetings into a static `.site`

That top-level story is much clearer than the older subsystem-oriented shape.

## What I Tried

- read the root [README.md](README.md)
- ran `./bin/cassini --help`
- ran `./bin/cassini doctor`
- ran `./bin/cassini record --simulate --out ./runs/review.run`
- ran `./bin/cassini inspect ./runs/review.run`
- ran `./bin/cassini inspect ./media/daily-meeting--2026-03-04--12:36:53.mkv`
- ran `./bin/cassini build ./runs/review.run --out ./meetings/review.meeting`
- ran `./bin/cassini build ./media/daily-meeting--2026-03-04--12:36:53.mkv --out ./meetings/sample.meeting`
- ran `./bin/cassini publish ./meetings --out ./site-review`
- ran `./bin/cassini serve ./cassini-viewer/exports/static-meetings --addr 127.0.0.1:8766`
- read deeper docs in:
  - [cassini-go-recorder/README.md](cassini-go-recorder/README.md)
  - [cassini-transcriber/README.md](cassini-transcriber/README.md)
  - [harness/README.md](harness/README.md)

## What Worked Well

- The root README and `cassini --help` finally explain the product in one sentence and one artifact flow.
- `cassini record --simulate` worked cleanly and produced a readable `.run` bundle.
- `cassini inspect` on the simulated run and the bundled sample `.mkv` produced useful summaries.
- `cassini build` now runs environment validation first and stopped before a long expensive failure.
- `cassini serve` worked against the existing static viewer export once the server was fully up.

## What Was Difficult, Underdocumented, Or Unintuitive

### 1. The deeper docs still split the repo back into old subsystem products

The root README says "use `./bin/cassini`", but once I open subdirectory docs I am back in the old world:

- [cassini-go-recorder/README.md](cassini-go-recorder/README.md) teaches `go run ./cmd/gocassini`
- [cassini-transcriber/README.md](cassini-transcriber/README.md) teaches `python3 cassini-transcriber/build-meeting-artifact.py`
- [harness/README.md](harness/README.md) still presents `cassini-player`, `cassini-diagnostics`, and `cassini-lab` as preferred entry points

Result: the top-level product story is coherent, but the repo stops feeling like one product as soon as I go one level deeper.

### 2. Failed `.meeting` / `.site` states are opaque

When `build` failed on doctor validation, it still left product-shaped output directories behind:

- `meetings/review.meeting/`
- `meetings/sample.meeting/`

But then:

- `cassini inspect ./meetings/sample.meeting` failed with `read archive: ... is a directory`
- `cassini publish ./meetings --out ./site-review` said `no meeting bundles found`

Result: I can end up with `.meeting` and `.site` paths on disk that look real but are not inspectable and are not explained as partial or invalid.

### 3. `doctor` explains the failure but not the fix

`cassini doctor` correctly stopped the flow with:

- `fail whisper lock directory is not writable: /home/silvio/.cache/cassini-transcriber/whisper/.locks`

That is a big improvement over a late transcriber crash, but it still does not tell me how to fix it:

- delete the directory?
- `chmod` it?
- rebuild the container?
- set a different cache root?

Result: the error is legible, but recovery still depends on repo archaeology.

### 4. There is still no true "try it now end to end" path from a fresh checkout

The root README gives me a good safe start:

- `cassini --help`
- `cassini doctor`
- `cassini record --simulate`

But it does not give me a documented way to reach a browsable final result without already having a working transcriber environment or knowing where sample outputs live.

I only found these by searching the repo:

- sample input MKV: [media/daily-meeting--2026-03-04--12:36:53.mkv](media/daily-meeting--2026-03-04--12:36:53.mkv)
- existing static export: [cassini-viewer/exports/static-meetings](cassini-viewer/exports/static-meetings)

Result: I can understand the architecture, but I still cannot reliably experience the full product without insider discovery work.

### 5. `inspect --help` does not reinforce the new artifact model

`cassini inspect --help` mentions:

- `.run`
- `.mkv`
- `session.json`
- `.csr`

It does not mention `.meeting` or `.site`.

Result: one of the most important product concepts is missing from the command help that should teach it.

### 6. The new bundle surface still leaks old archive vocabulary

The simulate path creates:

- `runs/review.run/capture.csr`
- `runs/review.run/cassini.json`

And `inspect` reports `recording=capture.csr`.

Result: the new `.run` abstraction is better, but it still exposes legacy internal format names to the user in the first easy demo path.

### 7. `serve` prints a URL before it feels fully ready

`cassini serve` printed:

- `url -> http://127.0.0.1:8766/`

but my first request failed immediately after startup, and a second request a few seconds later returned HTTP 200.

Result: minor issue, but it creates a small "did it actually start?" moment.

## Overall

The project now makes sense much faster from the repo root. The main remaining UX problem is not "too many entry points" anymore. It is "the new product story is only fully true at the top layer".

The next round of cleanup should focus on:

- making deeper docs obey the same `cassini` product boundary
- making partial bundle states explicit and inspectable
- turning doctor failures into actionable remediation
- exposing a real sample/demo path all the way to a browsable site
