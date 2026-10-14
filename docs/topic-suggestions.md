# Topic suggestions

Cassini can read each meeting's transcript and suggest which of your topics it
was about. A person confirms or rejects every suggestion; only a confirm writes
a tag into the recording. This page is for the administrator who runs it. The
design is in [proposals/automatic-tagging](./proposals/automatic-tagging/README.md).

> **Status.** Topics are suggested on the meeting page and in a review queue
> across meetings. Proposals for new topics come in a later release.

## Words used on this page

- **Topic**: a tag with a written definition, described by the people who tag
  meetings.
- **Passage**: a stretch of 25 to 120 words of one transcript, cut at sentence
  ends, pauses and speaker changes. Each passage gets one vector.
- **Vector** (embedding): 768 numbers that place a text by meaning, so similar
  subjects end up close together.

## Reviewing many meetings

**Needs review** in the rooms list opens the queue: every suggestion waiting
on the meetings you can read (in the selected room, when one is), grouped by
topic, each with a short quote. Confirm or reject one meeting at a time, or
select several (up to 100) and **Confirm selected**: each meeting is decided
exactly as it would be on its own page. From the keyboard, the arrows move
between meetings, Space selects, C confirms and R says "not this meeting".

The queue opens with one line of coverage, for example "Checked 112 of the
130 meetings you can see · 12 waiting · 6 could not be checked (no
transcript) · 9 in languages not yet validated", and its limits: a missing
suggestion does not mean the topic was absent. List rows carry a dashed
"2 suggested" badge. Tag filters and search never match a suggestion. Like
search, the queue lists the meetings your current shares let you read; opening
or deciding one checks your access to that recording again.

## Describing topics

Anyone who can read at least one meeting can describe topics, in **Manage tags
→ Topics** (the tab appears once suggestions are switched on):

- **New topic**: a name and a definition of up to 1,000 characters, written in
  your own words as you would explain the subject to a new colleague. Never
  paste text from a meeting: everyone who can tag sees the name and the
  definition.
- **Describe as topic…** on an existing tag keeps that tag, so meetings that
  already carry it count as the topic's. When the tag is on meetings from only
  one conversation you can read, the editor warns that describing it shows its
  name to everyone.
- **Edit** changes the name, colour or definition. A new name is also written
  into your own meetings, as a rename in the Tags tab is; meetings you cannot
  read keep the old name. A new definition is compared with every checked
  meeting again, from the stored vectors, without reading the meetings again.
- **Retire** stops suggestions for the topic; its tags stay on meetings, and
  **Restore** brings it back. A topic whose name you want again for something
  else can be restored or left retired, in which case a new topic gets a new
  id.
- **History** lists every revision. Whoever wrote a revision, or an
  administrator, can remove it from the history (for text that should never
  have been saved); the current revision cannot be removed.

Limits: at most 200 active topics, and at most 30 new topics or definition
changes per person per hour, because each one is compared with the whole
archive. Counts on the tab ("On 4 meetings · 2 to review") are over the
meetings you can read. **Very common** marks a topic suggested or confirmed on
most of them: its definition is probably too general.

Typing a topic's name in the tag picker uses the topic's tag, with its
colour, rather than starting a second tag with the same name. Renaming,
recolouring or merging a tag in the Tags tab updates its topic too; merging a
topic into another tag retires it into that tag, which takes its definition if
it has none, and keeps the rejections people made.

## Reviewing suggestions

On a meeting's page, after its tags, a **Suggested** group shows the topics
Cassini suggests for it, with a dashed outline. Opening one shows:

- the topic's definition;
- why it was suggested, in words ("Discussed for about 3 minutes across 2
  passages", or "One short, focused discussion");
- up to four passages it rests on, quoted from the transcript, each with its
  time; a click plays from there. While it is open, those passages are
  underlined with dots in the transcript;
- "Suggested from the transcript. Not saved to the recording until someone
  confirms it.", and, until the meeting's language has been validated,
  "Suggestions have not been checked for this meeting's language."

**Confirm** writes the topic into the recording as an ordinary tag on the
whole meeting, credited to you, exactly as if you had added it yourself. Where
the recording already has a tag with the same name, that tag is used, so the
meeting never shows two chips with one name. **Not this meeting** records a
rejection: the topic is never suggested for that meeting again, whatever
changes later (a rerun, a new model, a new definition). Both can be undone
from the message that follows, and a rejection can be **reopened** later from
"Decided for this meeting".

A meeting whose audio changed after it was tagged must have its old tags
sorted out before a topic can be confirmed; rejecting still works.

Scores are never shown, only the rule in words and the number of supporting
words. Suggestions are not in the recording, its copies, search or the public
viewer until someone confirms them.

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
| `annotations.sqlite3` | The topic library: names, definitions, colours and every revision with who made it (shown to administrators only, in the topic suggestions status). |  No. |
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
