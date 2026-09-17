---
title: Agent access via the CLI
description: How an agent or a script outside Nextcloud reads the meetings an account is allowed to read, and tags them, with no browser login.
source: docs/agent-meeting-access.md
copied: "2026-09-17"
---

`cassini meetings` reads published recordings from outside Nextcloud, as a
Nextcloud user with an app password. It sees exactly what that account can see
in the app, and nothing more.

```bash
cassini meetings list --from 2026-08-01
cassini meetings search "offer" --tag hiring   # finds where it was said
cassini meetings context <meeting-id>          # transcript and summary, ready for an agent
cassini meetings fetch <meeting-id> --out standup.opus
```

An agent skill ships at `.claude/skills/cassini-meetings/SKILL.md`. It teaches a
coding agent when and how to use these commands. Claude Code loads it from a
checkout; for other agents, point them at the file.

| Command | What it answers |
|---------|-----------------|
| `cassini meetings rooms` | Which conversations does this account have recordings from? |
| `cassini meetings search "<words>"` | Where was this said, across the meetings I may read? |
| `cassini meetings list` | Which meetings may this account read — optionally only a room's, a date range's, or a tag's? |
| `cassini meetings fetch <id>` | Give me that meeting's single portable file. |
| `cassini meetings context <id> [<id> ...]` | Give me those meetings as one document I can read. |
| `cassini meetings tags` | Which tags are on the meetings I may read? |
| `cassini meetings annotations <id>` | What is marked in that meeting, and by whom? |
| `cassini meetings annotate <id> --ops <file>` | Mark that meeting, or remove marks, in one batch. |

## How it works

What the commands return depends on
[who can see recordings](/docs/guides/who-can-see-a-recording) on that Nextcloud.
Where recordings are visible to **anyone with a Nextcloud account**, every
account gets every recording, fetched as the `cassini` service account. Where
they are visible to **meeting participants**, the app fetches from Nextcloud
Files **as the calling user**, so Nextcloud decides per recording. Either way
Cassini keeps no separate list of who may see what, and never returns more than
the account is entitled to.

```text
  cassini meetings list --from 2026-08-01
        │
        │  GET https://<nextcloud>/index.php/apps/app_api/proxy/gocassini/published/meetings-list?from=...
        │      Authorization: Basic <user>:<app password>
        ▼
  Nextcloud  ── authenticates the app password
        │     ── mints the app-API identity for the session
        ▼
  Cassini app ── GET catalog.json as the recordings owner   (what exists)
        │     ── PROPFIND meetings/ AS THE CALLING USER     (what they may read)
        │     ── intersect, then narrow by the query
        │
        ├── the meeting list, filtered to what the caller may read
        ├── meetings/<id>.opus   ... or 404
        └── meetings-context?ids=…,…     the same document `meetings context` prints
```

Three consequences worth internalising before you build on this:

- **A recording you may not read is answered `404`, exactly like one that does
  not exist.** That is deliberate: a recording you cannot see never reveals that
  it exists. The CLI therefore never says "forbidden" or "no such meeting" — it
  says *no recording you can read*.
- **An empty list means an empty list.** A failure to reach the archive is an
  error with a non-zero exit, not a `200` with nothing in it, so an agent can
  act on the difference.
- **One command writes, and it writes only marks.** `meetings annotate` posts a
  batch of tags for one meeting. Everything else is `GET` and `HEAD`. Starting,
  stopping and re-running jobs stays on the operator's admin routes, off the
  agent path entirely.

`search` answers with references — meeting, speaker, and where in the recording —
and never with transcript text. Reading what was actually said goes through
`meetings context`, which fetches the recording **as you**, so Nextcloud checks
the permission on the words themselves. Every search also reports how many of
your readable meetings it actually covered, so "nothing found" never quietly
means "never looked".

## Before you begin

You need:

1. **The `cassini` CLI.** From a checkout, `./bin/cassini`. It needs `ffprobe` on
   `PATH` for `meetings context`, which reads the downloaded meeting's metadata.
   `cassini doctor` checks for it.
2. **A Nextcloud account** on an instance running the Cassini app, which can read
   at least one recording. Verify in the browser first: if the account cannot see
   a meeting in the app, the CLI will not see it either. That is the whole point.
3. **A Nextcloud app password** for that account — not the login password.

### Create the app password

1. Log in as the account the agent will act as.
2. Go to **Settings → Security → Devices & sessions**.
3. Under *Create new app password*, name it (for example `cassini-agent`) and
   confirm.
4. Copy the generated password immediately — Nextcloud shows it once.

Use an app password rather than the login password: it is scoped, revocable from
that same page without changing the account password, and it keeps working on an
instance that enforces two-factor authentication, where a login password over
Basic auth does not.

## 1. Point the CLI at your Nextcloud

Export the connection settings once. Passing the credential by environment
variable keeps it out of your shell history and out of the process list, where
`--app-password` would put it.

```bash
export CASSINI_NC_URL="https://cloud.example.com"
export CASSINI_NC_USER="alice"
export CASSINI_NC_APP_PASSWORD="xxxxx-xxxxx-xxxxx-xxxxx-xxxxx"
```

| Variable | Flag | Default |
|----------|------|---------|
| `CASSINI_NC_URL` | `--nextcloud-url` | *required* |
| `CASSINI_NC_USER` | `--user` | *required* |
| `CASSINI_NC_APP_PASSWORD` | `--app-password` | *required* |
| `CASSINI_NC_APP_ID` | `--app-id` | `gocassini` |

A flag always wins over the environment. A bare host becomes `https://`, never
`http://`, so a typo cannot put the app password on the wire in clear text.

## 2. List what the account may read

```bash
cassini meetings list
```

Expected: a first line summarising the result
(`meetings=2 caller=alice source=nextcloud-files`), then one line per meeting,
newest first, in the order the app shows them:

```text
meeting=01JZ8K… date=2026-08-11 10:32 room=rm_9f2a1c3d4e5b6a70 title=Daily Standup speakers=3 segments=120 duration_ms=1800000 fetchable=yes
```

`source=nextcloud-files` confirms the bytes came from Nextcloud Files, under
whichever audience that instance is set to. Anything else also prints a warning
on stderr. `fetchable=no` marks a meeting recorded before the single-file format:
it has no portable `.opus`, so `fetch` and `context` cannot serve it.

Add `--json` for the machine-readable form; entries are re-emitted exactly as the
server sent them, so the server's payload stays the single contract.

### Narrowing the list

Three optional filters, which combine:

```bash
cassini meetings list --room rm_9f2a1c3d4e5b6a70
cassini meetings list --from 2026-08-01 --to 2026-08-31
cassini meetings list --tag hiring --from 2026-08-01
```

`--from` and `--to` take the dates the catalogue itself prints — `2026-08-11`,
`2026-08-11 14:30`, or `2026-08-11 14:30:05`. They carry **no timezone**, because
a meeting's date label does not: it is a wall clock. A bare date covers the whole
day at both ends. A range whose ends are backwards is rejected rather than
returning an empty list.

`--room` takes the `room=` value `cassini meetings rooms` prints; matching is
exact. When any filter is in effect the output gains a line naming it and
counting what it removed, so a short list is never mysterious.

## 3. List the rooms

```bash
cassini meetings rooms
```

```text
rooms=2 caller=alice source=nextcloud-files
room=rm_9f2a1c3d4e5b6a70 name=Weekly Sync meetings=12 latest=2026-08-11 10:32 earliest=2026-05-05 10:30
```

The rooms are derived from the catalogue you may already read, not fetched from
Talk, so a room you have no readable recording from does not appear.

The `room=` column is a **derived id**, deliberately not the Talk conversation
token: for a public conversation that token is also the link that joins it, so
publishing it beside a recording every signed-in account may read would turn "may
read a past recording" into "may join the live conversation". The id is a one-way
function of the room's identity — deterministic, and not reversible into the
token. Administrators should set `CASSINI_ROOM_ID_PEPPER` once, at install.

## 4. Read meetings as context

```bash
cassini meetings context 01JZ8K3M4N5P6Q7R8S9T0VWXYZ
cassini meetings context 01JZ8K3M… 01K2R4N7… 01K5T9P2…
```

Expected: a markdown document on stdout — for each meeting, its identity and
duration, the summary if one was generated, and the transcript as
speaker-attributed paragraphs. Use `--out FILE` to write it to a file, or
`--json` for the structured form.

**Several ids produce one document**, holding the meetings in the order you named
them. The whole run is fetched against a single view of what you may read, and
**an id you cannot read fails all of it** — a bundle that quietly dropped a
meeting would answer a question asked of all of them using only some of them, and
look right doing it.

`--timestamps` cites each passage's start time beside the speaker. It is off by
default because it changes bytes consumers have pinned.

**Read this before quoting the transcript.** The transcript is *assembled from
the recording's word timings*, and both output modes label it
`derived-from-words`. Paragraph breaks are inferred from pauses and speaker
changes. The words are verbatim; the punctuation and paragraphing are not
editorial. Do not present it as an edited or approved transcript.

If the meeting has no summary you get `_No summary was generated for this
meeting._` and a note on stderr. That is normal, not a failure.

`--local` reads portable `.opus` files off disk instead of fetching ids from
Nextcloud, and needs none of the connection settings above. A meeting's id is its
file's basename without the `.opus`; the room name lives only in the catalogue,
so pass `--catalog ./catalog.json` if you want a local run to produce the same
bytes as an id run.

## 5. Download the meeting file itself

```bash
cassini meetings fetch 01JZ8K3M4N5P6Q7R8S9T0VWXYZ --out "./Daily Standup.opus"
cassini inspect "./Daily Standup.opus"
```

The result is byte-identical to the published file: a single self-contained
meeting — audio plus the embedded transcript and summary — playable in any Opus
player. See [The meeting file](/docs/guides/meeting-file).

An interrupted download never lands at the destination, and an empty reply is
refused rather than saved as a `.opus` that fails when something reads it. The
file is created **readable by you only**: it holds a private meeting's audio and
transcript, and Nextcloud decided who may see it.

## 6. Tags and marks

A **tag** is a label such as `hiring`. A **mark** puts a tag on a whole meeting,
or on a stretch of it. Marks are stored inside the recording's `.opus` file, so
everyone who can read a meeting sees its marks, and a downloaded copy keeps them.

```bash
cassini meetings tags
cassini meetings annotations 01JZ8K3M4N5P6Q7R8S9T0VWXYZ
```

```text
tags=2 caller=alice indexed=12 of 12 meeting(s) you can read
tag=tag_k3v9q2m7x4d8w1pz meetings=4 marks=9 label=hiring
```

Only tags on meetings you can read appear. When `indexed` is lower than the
number of meetings you can read, a note says so: the app's tag index has not read
those meetings yet.

To write marks, send a batch as an `{"ops": [...]}` document:

```json
{"ops": [
  {"op": "mark", "tag": {"label": "hiring"}, "target": {"kind": "meeting"}},
  {"op": "mark", "tag": {"label": "hiring"},
   "target": {"kind": "time-range", "startMs": 869000, "endMs": 884000}}
]}
```

```bash
cassini meetings annotate 01JZ8K3M4N5P6Q7R8S9T0VWXYZ --ops ./ops.json
```

| Op | Effect |
|---|---|
| `mark` | Adds a mark. `tag` is `{"label": …}`, or `{"id": …}` for an existing tag. `target` is `{"kind": "meeting"}` or `{"kind": "time-range", "startMs": …, "endMs": …}` |
| `unmark` | Removes one mark, by its `itemId` |
| `unmark-tag` | Removes every mark of one `tagId` on this meeting |
| `undo-operation` | Removes every mark one batch added, by its `operationId` |
| `relabel` | Renames a tag on this meeting only |
| `merge-tag` | Moves every mark of one `tagId` on this meeting into another |

Before an agent writes marks, know that:

- **Every mark carries your account.** The app stamps each new mark with the
  Nextcloud user you authenticate as; the command never sends a user id, so it
  cannot claim to be anyone else. `--actor-kind` says whether a person or an
  agent made the marks, and defaults to `agent`.
- **Tags are shared.** Anyone who can read a meeting can mark it, and everyone
  who can read it sees the marks.
- **Retrying is safe.** A mark identical to an existing one is not added again,
  and an unknown `itemId` is reported rather than failing the batch.
- **Undo is one op.** Every batch has an operation id, and `undo-operation`
  removes everything that batch added. The success output prints the op to send.
- **`--expect-revision N` guards against someone else's edit.** The batch is
  refused unless the meeting's marks are still at revision `N`.

A batch is at most 64 KiB and 200 ops.

## The agent skill

The skill teaches an agent how to read the output as well as which command to
run, so "what did we decide about the pricing change?" turns into a
`meetings search` followed by a `meetings context` rather than a guess. It is
written for Claude; the commands themselves are not, and any harness that can
run a binary can use them.

A worked example — an agent given a month of meetings and asked what changed —
is being written up, and will be linked here when it is.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success — including a `list` that found no readable meetings |
| `1` | Runtime failure: credentials rejected, nothing readable at that id, Nextcloud Files unavailable, unreadable meeting file |
| `2` | Usage or configuration error: a missing flag, a bad argument, an unparseable date |
| `3` | `annotate` only: `--expect-revision` did not match |
| `4` | `annotate` only: the app refused the ops, or the batch is malformed or too large |
| `5` | `annotate` only: the meeting's marks were made against different audio |

## Troubleshooting

**`Nextcloud rejected the credentials for user "alice"`** — the app password is
wrong, revoked, or belongs to a different account. Generate a new one. The error
names where the credential was read from, so you know which to fix; it never
echoes the value.

**`meetings=0`** — either the account genuinely has no readable recordings, or
the recordings folder is not set up. Check the same account in the Cassini app in
a browser: if it sees nothing there either, this is a provisioning question, not
a CLI one.

**`no recording you can read at that id`** — the id is absent from *this
account's* catalogue. It may not exist, or it may exist and belong to somebody
else; these are answered identically on purpose. Run `meetings list` to see what
this account can read.

The full list, including the redirect and host guards, the tag-index states and
the `ffprobe` requirement, is in
[`docs/agent-meeting-access.md`](https://github.com/codemyriad/gocassini/blob/main/docs/agent-meeting-access.md).

## Related

- [The meeting file](/docs/guides/meeting-file) — the contract for the `.opus`
  file `fetch` downloads.
- [Who can see a recording](/docs/guides/who-can-see-a-recording) — who may read
  which recording, and how that is configured.
