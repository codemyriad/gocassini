# Runbook: repair the published meeting archive

**Ticket:** [D-739](https://linear.app/code-myriad/issue/D-739) — urgent.
**Symptom:** every branch preview fails; the `deploy-ui` job cannot read a single
meeting out of `main`'s published archive.

This runbook is for a person with the R2 credentials and access to the GPU host.
It fixes data, not code, so nothing here can be done from a pull request.

---

## 1. What is actually broken

A Cassini meeting is published as a single `.opus` file with the transcript and
metadata embedded in its tags. One tag records how the audio integrity digest was
computed:

| Value | Digest covers | Status |
|-------|---------------|--------|
| `exact-pcm` | decoded PCM samples | retired on 2026-09-02 by `218c3f6b` |
| `exact-opus-audio-v1` | the compressed Opus bytes | current |

`218c3f6b` ("portable: support only the published v1 format") deleted support for
the first. The archive was never migrated, so every published meeting is now a
file the code refuses to open.

```text
  cassini-raw/daily/            cassini-processed/main/meetings/
  YYYY-MM-DD.mkv        ──STT──▶  YYYY-MM-DD.opus          ──export──▶  catalog.json
  the source of truth             39 files, ALL exact-pcm       ✗ every one rejected
  (never reprocessed)             2026-03-05 .. 2026-05-11
```

Raw recordings are untouched by this. They are the source of truth, and the
repair is to run them through the current pipeline again.

## 2. What the failing job is for

`deploy-preview.yml` publishes a viewer per branch at
`https://view.meetings.codemyriad.io/<branch>/`, so a UI change can be looked at
before merging. Slashes in a branch name become `--`.

It has two paths:

- **`deploy-ui`** — the common one, and the one that is broken. It runs on a free
  GitHub runner, downloads `main`'s already-processed meetings, rebuilds the
  catalog, and publishes a per-branch viewer pointed at main's recordings. It
  never runs speech-to-text. This is what fails: it reads the archive.
- **`deploy-gpu`** — opt-in. It downloads raw `.mkv` recordings, runs
  speech-to-text on the self-hosted GPU box, publishes fresh meeting files, and
  then exports. It is serialized on the single GPU.

`deploy-gpu` is also the repair tool: pointed at `main`, it rewrites
`cassini-processed/main/meetings/` with meetings in the current format.

## 3. Diagnose before repairing

Do not skip this. Anything absent from the raw bucket cannot be regenerated.

```sh
# Credentials, as the workflow sets them.
export RCLONE_CONFIG_CODEMYRIAD_TYPE=s3
export RCLONE_CONFIG_CODEMYRIAD_PROVIDER=Cloudflare
export RCLONE_CONFIG_CODEMYRIAD_ACCESS_KEY_ID=...      # R2_ACCESS_KEY_ID
export RCLONE_CONFIG_CODEMYRIAD_SECRET_ACCESS_KEY=...  # R2_SECRET_ACCESS_KEY
export RCLONE_CONFIG_CODEMYRIAD_ENDPOINT=...           # R2_S3_ENDPOINT

# What is published today, and what raw material exists.
rclone lsf codemyriad:cassini-processed/main/meetings/ | grep '\.opus$' | sort > /tmp/published.txt
rclone lsf codemyriad:cassini-raw/daily/ | grep -E '^[0-9]{4}-[0-9]{2}-[0-9]{2}.*\.mkv$' | sort > /tmp/raw.txt
wc -l /tmp/published.txt /tmp/raw.txt

# Published meetings with NO raw recording behind them. These cannot be
# re-processed and will be lost by a repair that only reprocesses raw.
comm -23 \
  <(sed 's/\.opus$//' /tmp/published.txt) \
  <(sed -E 's/\.mkv$//' /tmp/raw.txt)
```

**If that last command prints nothing,** every published meeting can be
regenerated. Continue.

**If it prints anything,** stop and decide what to do about those meetings
first. Reprocessing will not bring them back, and they are only reachable today
by restoring `exact-pcm` on the read side (option 2 in D-739). Do not delete
anything from `cassini-processed/` until that is settled.

Confirm the diagnosis on one file, if you want to see it directly:

```sh
rclone copy codemyriad:cassini-processed/main/meetings/2026-03-05.opus /tmp/
ffprobe -v error -show_entries format_tags=CASSINI_AUDIO_MATCH_POLICY \
  -of default=nw=1 /tmp/2026-03-05.opus
# expect: CASSINI_AUDIO_MATCH_POLICY=exact-pcm
```

## 4. Repair

`N` is the number of most-recent raw recordings to process. Use the count from
`/tmp/raw.txt` to cover the whole archive. Budget roughly one to six minutes per
recording on the GPU, so 39 recordings is somewhere between forty minutes and
four hours, and it holds the single GPU for that time.

```sh
gh workflow run deploy-preview.yml --ref main -F run_processing=39
gh run watch "$(gh run list --workflow deploy-preview.yml --branch main --limit 1 --json databaseId -q '.[0].databaseId')"
```

Start smaller if you want to prove the pipeline before committing the GPU:

```sh
gh workflow run deploy-preview.yml --ref main -F run_processing=1
```

Equivalently, land a commit on `main` whose message contains `[preview:39]`.

### What the run does

1. Builds the GPU `cassini-bin`.
2. Wipes its per-branch scratch directories.
3. Selects the newest `N` raw `.mkv`, downloads them.
4. Runs `scripts/process-recordings.sh` with `DEVICE=cuda`. No summaries: the
   workflow deliberately configures no LLM endpoint.
5. Fails the run if it produced no `.opus` at all.
6. Uploads to `cassini-processed/main/meetings/`.
7. Re-exports the catalog and publishes.

### Two things to know before you run it

- **Upload is a copy, not a sync.** Files are overwritten by name, so a
  reprocessed `2026-03-05.opus` replaces the old one. But a published meeting
  with no raw counterpart is *not* removed and will stay in the bucket as an
  unreadable `exact-pcm` file. Section 3 tells you whether any exist.
- **The catalog is rewritten from what was just processed.** If you run with a
  small `N`, the published catalog will list only those `N` meetings. Use the
  full count for the real repair.

## 5. Verify

```sh
rclone copy codemyriad:cassini-processed/main/meetings/2026-03-05.opus /tmp/check/
ffprobe -v error -show_entries format_tags=CASSINI_AUDIO_MATCH_POLICY \
  -of default=nw=1 /tmp/check/2026-03-05.opus
# expect: CASSINI_AUDIO_MATCH_POLICY=exact-opus-audio-v1
```

Then re-enable the preview job — see the next section — and confirm a branch
preview publishes with meetings in it, at
`https://view.meetings.codemyriad.io/<encoded-branch>/`.

## 6. Re-enable `deploy-ui`

It is disabled in `.github/workflows/deploy-preview.yml`, in the `deploy-ui`
job's `if:`, with a comment pointing at D-739. Remove that clause, push, and
watch one preview go green. Re-enabling it is part of closing the ticket.

## 7. If reprocessing is not the answer

If section 3 turned up meetings with no raw recording, or the GPU time is not
available, the alternative is to accept `exact-pcm` on the read side only:
readers tolerate it, writers keep emitting `exact-opus-audio-v1`. That is the
same shape as the `CASSINI_PAYLOAD_SCHEMA` tolerance in PR #281 — see
`portable.AcceptedPayloadSchema` and the two TypeScript readers alongside it.

It reopens a decision `218c3f6b` made deliberately, so it is a call to make
explicitly rather than by drift. It is also the only option that makes the
existing files readable rather than replacing them, which matters if any of them
cannot be regenerated.
