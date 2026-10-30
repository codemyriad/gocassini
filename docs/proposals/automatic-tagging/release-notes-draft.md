# Topic suggestions: release notes draft

Topic suggestions ship as an unannounced preview: locked on every install
until `cassini-operator topics-preview on` (see the README, "Rollout"). Their
changelog fragments were taken out of `changelog.d/` so the release notes do
not announce them. Restore them when the feature is announced: each section
under "Changelog fragments" is one file, named by its heading, with its text
unchanged.

The schema note in `d-847.topic-suggestions-worker.md` ships meanwhile, without
the word "topics", as `changelog.d/d-847.annotations-schema-9.md`; drop that
fragment's line here, or that file, when restoring. The
`CASSINI_TOPICS_THREADS` line goes with the `<variable>` declaration in
`appinfo/info.xml` and the row in `docs/reference/configuration.md`, both taken
out for the same reason; restore them together, with the docs index link to
`docs/topic-suggestions.md` and its API sections, moved there from
`docs/reference/api.md`, the topic rows of `docs/privacy.md` and the topic
commands in `cassini-operator --help` (both under "Not in the release" below).

## Not in the release

### `docs/privacy.md`

The privacy page is an administrator's reference for vetting a release, and the
source of the microsite's copy, so it says nothing about topic suggestions
while they are locked. That is accurate: locked, nothing is read or stored
beyond the empty schema 9 tables. Restore these three pieces when announcing.

In the **Summary** table, before "Publishing the archive":

```markdown
| Topic suggestions (optional, off until an admin switches them on) | Local (EmbeddingGemma 2 on the CPU) | No |
```

Under **What Cassini stores**, after "Room audience":

```markdown
- **Topic suggestion vectors** — only while topic suggestions are switched on:
  for each checked meeting, its transcript cut into passages, stored as passage
  start and end times, word counts and one embedding vector each (768 numbers)
  in `topics.sqlite3` beside the operator database. No transcript text, titles,
  rooms or user ids. The file is disposable: deleting it only means every
  meeting is read again. The work queue (which meetings were checked, when, and
  why one could not be) lives in the annotations database. See
  [Topic suggestions](./topic-suggestions.md).
- **Topic review decisions** — who confirmed or rejected which topic on which
  meeting, and when. Shown to the people who can read that meeting; deleted
  with the meeting. A confirm is also an ordinary tag in the recording,
  credited to that person. A topic with enough decisions gets its own
  suggestion bar fitted to them (numbers only, in the annotations database).
  `cassini-operator topics-export` writes the decisions and their scores, by
  meeting file name, with no transcript text, topic names or user ids, for
  someone with shell access on the server.
- **Topic proposals** — for recurring themes no topic covers: the time ranges
  of the passages they draw on, their average vector, a draft name and a few
  words from those meetings' transcripts. Shown only to people who can read
  every meeting a proposal draws on; hidden at once and rebuilt when one of
  those meetings is deleted.
- **Topic library** — the names and definitions people write for topics, and
  every revision of them with who saved it. Everyone who can read at least one
  meeting sees the names and definitions; who wrote them is shown to
  administrators only. A past revision can be removed from the history by its
  author or an administrator.
```

Under **Deletion & uninstall**, after "Insight documents are ordinary
Nextcloud files":

```markdown
- **Retiring a meeting removes its topic rows and vectors** from both topic
  stores, and expires the answers of requests that named it.
```

If the lock stays after announcing, say so in the summary row and the vectors
entry ("unlocked on the server and switched on").

### `cassini-operator --help`

`backfill-topics`, `topics-export`, `topics-eval` and `topics-preview` work
but are not listed in the `Commands:` block of `Run`'s usage or in its
"unknown command" error (`cassini-operator/internal/operator/run.go`), and
`TestTopicCommandsUnlistedButDispatched` checks that. Restore the error's
arguments and these usage lines, dropping the last if the lock goes, and
invert or delete that test:

```text
  `+backfillTopicsCommand+`      queue past meetings for topic suggestions
  `+topicsExportCommand+`        write topic verdicts with their scores, no text
  `+topicsPreviewCommand+`       unlock or lock the topic suggestions preview
```

## Changelog fragments

### `changelog.d/d-847.topic-controls-languages.md`

```markdown
### Changed
- Topic suggestions compare passages with the built-in control texts in seven languages (English, Italian, German, French, Spanish, Portuguese, Dutch), so the bar a passage must clear follows the language it is in. Every checked meeting is rescored once, without being read again.
```

### `changelog.d/d-847.topic-draft-formats.md`

```markdown
### Fixed
- Topic suggestions read a recording published in a pre-version-1 draft of the format from its job's bundle, and record "published in an older file format" when the bundle is gone, instead of retrying it until it is blocked.
```

### `changelog.d/d-847.topic-gates.md`

```markdown
### Added
- Topic suggestions fit a stricter or looser bar per topic once it has 20 reviewed suggestions on its current definition, 5 of them rejected, and keep it only when it beats the shared bar on meetings left out of the fit.
- `cassini-operator topics-export` writes topic decisions with their scores, without text or user ids, for offline analysis.
- `cassini-operator topics-eval --labels` reports labeller agreement, precision and estimated recall per detected language.
```

### `changelog.d/d-847.topic-library.md`

```markdown
### Added
- Manage tags has a **Topics** tab once topic suggestions are on. Anyone who can tag describes topics there, from scratch or from an existing tag, edits, retires and restores them, and sees each topic's history; a definition change is scored again against checked meetings without reading them again. Typing a topic's name in the tag picker reuses the topic's tag.
```

### `changelog.d/d-847.topic-preview.md`

```markdown
### Added
- Topic suggestions are marked **Preview** in Settings, on the meeting page, in **Needs review** and in Manage tags → **Topics** and **New topics**, with one line saying they can be wrong or miss topics and have been tested on English meetings only. Their quality was measured with model labellers, not people, and arm64 and the CUDA image are untested.
```

### `changelog.d/d-847.topic-proposals.md`

```markdown
### Added
- Manage tags → **New topics** proposes themes that recur in a room's meetings and no topic covers, with a draft name from those meetings' own words. People create a topic from one in their own words, link it to an existing topic, or dismiss it. A proposal is shown only to people who can read every meeting it comes from.
```

### `changelog.d/d-847.topic-review-queue.md`

```markdown
### Added
- **Needs review** in the rooms list gathers topic suggestions across the meetings a person can read, grouped by topic, with a coverage line. Several meetings can be confirmed or rejected at once, and from the keyboard. List rows show how many suggestions wait on each meeting.
```

### `changelog.d/d-847.topic-review.md`

```markdown
### Added
- Meetings show **Suggested** topics after their tags, with the passages each rests on. Confirming one adds it as an ordinary tag credited to the person; "Not this meeting" keeps it from being suggested there again. Both can be undone, and a rejection can be reopened.
```

### `changelog.d/d-847.topic-suggestions-model.md`

```markdown
### Added
- Settings offers an optional Topic suggestions model (EmbeddingGemma 2, int8, about 275 MiB, Apache-2.0). It installs and is checked like Voice separation, through Settings or `cassini models install|pack|import`, and always runs on the CPU. Installing it switches nothing on yet.
- `CASSINI_TOPICS_THREADS` sets how many CPU threads the topic suggestions model uses.
```

### `changelog.d/d-847.topic-suggestions-worker.md`

```markdown
### Added
- Settings can switch topic suggestions on once their model is installed. Cassini then reads each newly published meeting's transcript on the CPU and stores passage vectors in `topics.sqlite3`; with no topics described yet it suggests nothing. **Check past meetings** queues older meetings, and blocked meetings can be retried. `cassini-operator backfill-topics` does the same from a shell.

### Changed
- The annotations database moves to schema 9. Back up `annotations.sqlite3` before upgrading: an older Cassini refuses the upgraded database, which takes tagging down.
```

### `changelog.d/d-847.topic-tuning.md`

```markdown
### Changed
- Topic suggestions were tuned on 800 labelled meeting–topic pairs: passages and definitions are embedded with EmbeddingGemma's retrieval prompts, and a passage supports a topic at a similarity of 0.68 with a margin of 0.06 over the control texts, counting every supporting word. On English meetings this finds about 60% of the topics a meeting is about, where it found 45%, with about 3 suggestions in 4 right. English is now a validated language. Proposals cluster at 0.83 on the new vectors.
```
