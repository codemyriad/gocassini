# Topic suggestions

Cassini can read each meeting's transcript and suggest which of your topics it
was about. A person confirms or rejects every suggestion; only a confirm writes
a tag into the recording. This page is for the administrator who runs it. The
design is in [proposals/automatic-tagging](./proposals/automatic-tagging/README.md).

> **Status.** The model and the work queue are in place. Describing topics and
> reviewing suggestions come in later releases, so for now Cassini only reads
> meetings and stores what it needs to score them later. With no topics
> described it suggests nothing.

## Words used on this page

- **Topic**: a tag with a written definition, described by the people who tag
  meetings.
- **Passage**: a stretch of 25 to 120 words of one transcript, cut at sentence
  ends, pauses and speaker changes. Each passage gets one vector.
- **Vector** (embedding): 768 numbers that place a text by meaning, so similar
  subjects end up close together.

## Switching it on

1. In **Settings**, download the **Topic suggestions** model (289 MB, 319 MB on
   disk, Apache 2.0). Cassini checks it on this server's CPU after the
   download. Offline hosts can use `cassini models pack` and `cassini models
   import`.
2. Turn on **Suggest topics after each meeting**. The switch stays disabled
   until the model's check passes.
3. Optionally, **Check past meetings**, with a start date if you want only the
   recent ones. Switching suggestions on does not do this by itself, because it
   costs about 10 s of CPU per meeting on a host shared with Nextcloud and Talk.

From then on, every published meeting is read once. A rerun with the same
transcript is not read again, and adding marks never makes a meeting count as
changed.

## What runs, and when

- **One meeting at a time, after recordings.** A recording that starts stops
  the reading at once; the meeting goes back to the queue with nothing lost.
  A past meeting does not start while a build is queued; one already running
  finishes first.
- **Memory.** A reading needs about 1.5 GB free on top of the build headroom.
  When it is not free, the meeting waits five minutes and tries again.
- **The inference lock.** Transcription, model checks and voice separation take
  the same lock. A reading waits for it as long as it takes; that wait is not
  a failure. Once it holds the lock, a reading has 10 minutes.
- **Threads.** 4 by default, or fewer on a smaller host. `CASSINI_TOPICS_THREADS`
  or the `threads` setting choose 1 to 16; it changes only the speed.

A meeting that fails is tried again after 1 minute, doubling up to 6 hours. After
5 failures it is **blocked**, and Settings lists it with the error and a
**Retry** button. A meeting with no transcript, an empty one, or one whose bytes
do not match their checksum is recorded as **could not be checked**, with the
reason. It never counts as a meeting with no topics.

`cassini-operator backfill-topics [--since YYYY-MM-DD] [--limit N]` queues past
meetings from a shell, like the Settings button. It only queues; the running
operator does the work.

## What is stored

| Where | What | Disposable? |
|---|---|---|
| `topics.sqlite3`, beside the operator database | For each checked meeting: passage times, word counts and vectors. No text, titles, rooms or user ids. About 280 KiB per meeting. | Yes. If it is deleted, Cassini reads every meeting again. |
| `annotations.sqlite3` | The work queue: which meetings were checked, when, why one could not be, and which model read it. The vectors of the built-in control texts. | No: back it up with the rest of the annotations database. |

Settings shows the size of `topics.sqlite3` in the status.

**Upgrading to this version is one-way.** It moves the annotations database to
schema 8, which an older Cassini refuses to open, and that takes tagging down.
Back up `annotations.sqlite3` before upgrading.

## What Settings shows

Settings shows counts of meetings by work state, never what was suggested: an
administrator sees no more meetings than anyone else, and a count of
suggestions across the archive would say something about meetings they cannot
read.

- **Checked**, **waiting**, **could not be checked** (with the reason) and
  **blocked** meetings.
- Whether meetings from before the switch was turned on are included.
- **Paused** when the model is missing, or its check failed. A check that
  fails takes readiness away; a check that only waited for memory or the lock
  leaves it as it was. Cassini checks the model again after every restart while
  suggestions are on.

## Changing models

A new model, or a change to how transcripts are cut or embedded, makes the
stored vectors stale. Cassini queues every checked meeting to be read again,
after new recordings, and never compares vectors from two models.

## When a meeting is deleted

Retention deletes a retired meeting's vectors and every topic row derived from
it, in both databases.
