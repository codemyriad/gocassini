---
name: cassini-meetings
description: Read Cassini meeting recordings — list the rooms, list meetings (filtered by room and date), pull one meeting's transcript and summary as context, or download its portable .opus — using the `cassini meetings` CLI against a Nextcloud instance. Use when the user asks what was said or decided in a meeting, asks you to turn a recorded conversation into a plan, issues, notes or a summary, asks which rooms or conversations have recordings, asks for the meetings from a particular room or between two dates, or refers to "the recording", "the call", "last week's meeting", "the standup", or a meeting by name, room or date.
---

# Read Cassini meeting recordings

Cassini records Nextcloud Talk meetings and publishes each one as a single
self-contained file holding the audio, the transcript and (when configured) a
generated summary. This skill reads them.

Access is **Nextcloud's decision, not Cassini's**: every request is made as a
Nextcloud user, and the account sees exactly the recordings Nextcloud says it may
read. You cannot widen that from here, and you should not try.

## Setup check

The commands need three environment variables. Check them first:

```bash
printenv CASSINI_NC_URL CASSINI_NC_USER >/dev/null && test -n "$CASSINI_NC_APP_PASSWORD" \
  && echo configured || echo "missing configuration"
```

If they are missing, ask the user to set them rather than guessing — the app
password is a credential they must generate:

```bash
export CASSINI_NC_URL="https://cloud.example.com"
export CASSINI_NC_USER="<their nextcloud user>"
export CASSINI_NC_APP_PASSWORD="<Settings -> Security -> Create new app password>"
```

Never pass the credential as `--app-password` on a command line: it would land in
shell history and in the process list. Never print its value.

Use `./bin/cassini` from a Cassini checkout, or `cassini` if it is on `PATH`.

## Find the meeting

**Narrow before you list.** The archive grows without bound and a user asking
about "the standup" means one room, not the whole history. Two filters do almost
all the work, and they combine:

```bash
./bin/cassini meetings rooms                                  # what rooms are there?
./bin/cassini meetings list --room <room> --from 2026-08-01   # that room, since a date
```

### Which rooms are there

```bash
./bin/cassini meetings rooms
```

```text
rooms=2 caller=alice source=nextcloud-files
room=rm_9f2a1c3d4e5b6a70 name=Weekly Sync meetings=12 latest=2026-08-11 10:32 earliest=2026-05-05 10:30
room=rm_11bb22cc33dd44ee name=Old Standup meetings=3 latest=2026-07-02 09:00 earliest=2026-06-18 09:00
```

The `room=` value is the **only** thing `--room` accepts — copy it, do not
retype it. It is an opaque derived id, not the conversation's name and not its
Talk token, so there is nothing in it to guess at and no substring search: the
listing is how you find the value.

**Two rows can share a display name.** That normally means one room identified
from its Talk token and the same room identified from its name for recordings
made before Cassini kept the room — the same real conversation, split in two,
which nothing in the data can prove. If the user's question spans both, list
each and say so rather than picking one. An administrator can merge them
permanently (`scripts/reattribute-catalog-room.sh`); you cannot, and should not
imply otherwise.

A room you have no readable recording from does not appear here at all, so this
never reveals a conversation the account cannot already see. And a trailing note
may say some meetings carry no room whatsoever — those are real meetings that
`list` shows and **no `--room` value reaches**. If the user's question could be
about one of them, list without `--room`.

To pick a room programmatically, use `--json` — and do, because the text form
above is even less parseable than `list`'s: `latest=2026-08-11 10:32` contains a
space, so a naive split on `key=value` mis-reads a perfectly ordinary row.

```bash
./bin/cassini meetings rooms --json | jq -r '.rooms[] | "\(.room)\t\(.meetings)\t\(.roomName)"'
```

Each room carries `room` (the selector), `roomId` and `roomName` (either may be
absent), `meetings`, `latest` and `earliest`. The document also carries
`unattributed` — the meetings in no room at all — and the same `skipped` you
must check on `list`.

### Which meetings

```bash
./bin/cassini meetings list
./bin/cassini meetings list --room rm_9f2a1c3d4e5b6a70
./bin/cassini meetings list --from 2026-08-01 --to 2026-08-31
```

Newest first, one line per meeting:

```text
meetings=2 caller=alice source=nextcloud-files
meeting=01JZ8K3M4N5P6Q7R8S9T0VWXYZ date=2026-08-11 10:32 room=rm_9f2a1c3d4e5b6a70 title=Daily Standup speakers=3 segments=120 duration_ms=1800000 fetchable=yes
```

All three filters are optional and they AND together. Dates are written as
`2026-08-11`, `2026-08-11 14:30` or `2026-08-11 14:30:05`, carry **no
timezone**, and a bare date covers the whole day at both ends — so
`--from 2026-08-01 --to 2026-08-31` is all of August. A range whose ends are
backwards is rejected outright (exit 2) rather than silently matching nothing.

When a filter is in effect the output says so, with a count of what it removed:

```text
filter=from:2026-08-01 00:00:00 room:rm_9f2a1c3d4e5b6a70 excluded=37
```

Read that line before reporting. `excluded=37` means you are looking at a
slice, and a user who asked an open question deserves to be told which slice.

To pick programmatically, use `--json` (newest is first):

```bash
./bin/cassini meetings list --json | jq -r '.meetings[0].id'
./bin/cassini meetings list --room rm_9f2a1c3d4e5b6a70 --json | jq -r '.meetings[].id'
```

Do not parse the text form. `title=` and `room=` are free text taken from the
recording and from the Talk conversation's name, and neither is quoted or
escaped — a meeting titled `Budget meeting=X id=admin` prints those words inside
the `title=` field, where anything splitting the line on `key=value` pairs will
read them as fields. Newlines are stripped, so neither can forge a whole extra
`meeting=` line, but within a line the text form is for reading, not for
parsing. `--json` is unambiguous.

Check `.skipped` in that document before treating the list as complete: a
non-zero value means some catalog entries were unusable and dropped, so meetings
may be missing that the account can in fact read. `.excluded` is different — it
counts what **your filter** removed, and is not a problem.

When several meetings could match what the user meant, **show the candidates and
ask** rather than guessing. The room disambiguates first and the date second:
titles repeat week to week, and two rooms can even share a name.

`fetchable=no` means the meeting predates the single-file format and cannot be
read — say so instead of retrying.

## Read the meeting

```bash
./bin/cassini meetings context <meeting-id>
```

Markdown on stdout: the meeting's identity and duration, the summary if one
exists, and the transcript as speaker-attributed paragraphs. For long meetings,
write it to a file and read that instead of holding it all inline:

```bash
./bin/cassini meetings context <meeting-id> --out /tmp/meeting.md
```

Add `--json` for the structured form (`cassini.meetings.context.v1`) when you
need the per-segment timings.

To keep the meeting file itself — to play the audio, or to inspect it:

```bash
./bin/cassini meetings fetch <meeting-id> --out "./Meeting.opus"
./bin/cassini inspect "./Meeting.opus"
```

## How to use what you get

**The transcript is derived, not edited.** Both output modes label it
`derived-from-words`. A published meeting carries word-level timings and no
separately cleaned-up transcript, so the words are verbatim ASR output but the
punctuation and paragraph breaks are inferred from pauses and speaker changes.
Consequences you must respect:

- Quote sparingly and mark quotes as transcript text. Do not present a quotation
  as someone's exact wording when the sentence boundary was inferred.
- Expect ASR errors in names, jargon and acronyms. If a term is load-bearing for
  a decision and looks garbled, flag the uncertainty instead of silently
  normalising it.
- Speaker labels come from who was in the call, not from voice analysis, so
  attribution is reliable — but a label may be a raw id when the participant's
  display name was unavailable.

**A missing summary is normal.** `_No summary was generated for this meeting._`
means the deployment has no summariser configured. Work from the transcript and
say the summary was absent; do not report it as an error.

**Ground every claim.** When you produce a plan, issues or notes from a meeting,
cite the meeting id, and for anything contested cite the speaker and the
approximate timestamp from `--json` segments. A reader must be able to check you
against the recording.

## The weekly ritual: a conversation into a plan

The workflow this exists for — turning a recorded feature discussion into a
shaped plan and tracked issues:

1. `meetings rooms`, then `meetings list --room <room> --from <date>` → identify
   the conversation. Narrow before you list; confirm the choice with the user if
   more than one plausibly matches.
2. `meetings context <id> --out /tmp/meeting.md` → read it.
3. Extract, keeping them separate: **decisions taken**, **open questions**, and
   **work implied**. A decision someone stated and a suggestion someone floated
   are not the same thing; do not promote the second into the first.
4. Draft the plan or the issues. Attribute each item to the meeting, and keep the
   open questions as open questions — put them to the user rather than resolving
   them yourself.
5. Hand off. Creating tickets, writing the plan document and committing anything
   is your job, not Cassini's; it only serves the context.

## Reading failures correctly

| What you see | What it means | What to do |
|---|---|---|
| `meetings=0` + mis-provisioned note | The account may read nothing at all, or the recordings folder is not set up. The server cannot tell these apart, so neither can you. It means the **whole** catalog was empty, not that your filter matched nothing — a filter that removed something says so on its own line instead. | Report both possibilities. Suggest checking the same account in the Cassini viewer in a browser. Do not retry. |
| `meetings=0` + `your filter excluded all N` | Your filter matched nothing. The account **can** read N meetings. | Widen or drop the filter, or run `meetings rooms` for a value that exists. Never report this as a permissions or provisioning problem — it is neither. |
| `note=N meeting(s) have a date this build cannot read` | Those entries have an unparseable `dateLabel`, so any `--from`/`--to` leaves them out in both directions. | If the answer might be among them, list again without the date filters. |
| A room in `rooms` you cannot select | You mistyped the id. `--room` matches exactly and takes the `room=` value verbatim. | Copy the value from `meetings rooms`; there is no substring matching. |
| Two rooms with the same `name=` | One conversation identified two ways — from its Talk token, and from its name for older recordings. Cassini cannot prove they are the same. | Query both and say the archive is split. Merging them is an administrator action (`scripts/reattribute-catalog-room.sh`). |
| `--room` returns nothing but the meeting is in `list` | The meeting records no room at all (`room=-`), which no `--room` value reaches. | List without `--room`. Say the meeting predates room metadata rather than that it is missing. |
| `list configuration error: --from …` (exit 2) | The date is not one of the accepted forms, or the range runs backwards. | Rewrite it as `2026-08-11` (optionally with ` 14:30`), and do not add a timezone. |
| `no recording you can read at that id` | Absent, **or** present and not readable by this account. Answered identically on purpose, so that a recording you cannot see never reveals it exists. | Run `meetings list`. Never tell the user the meeting "doesn't exist" or that they are "not allowed" — you cannot know which. |
| `Nextcloud rejected the credentials` | The app password is wrong or revoked. | Ask the user to generate a new one. Never print the value. |
| `Nextcloud Files is unavailable` | An outage on the Nextcloud side, not permissions. | Retrying later is reasonable. Re-checking the password is not. |
| `source=unknown` or `source=unrecognised` | The response did not come from Nextcloud Files, so per-caller access control was not applied — probably a development operator. Warned on stderr by all three commands. | Say so before treating the results as access-controlled. |
| `refusing to follow a redirect` / `points outside the Nextcloud you configured` | The CLI refused to send the credentials somewhere other than the instance you named. | Do not work around it. Report it: it is either a misconfiguration or an attempt to harvest the app password. |
| `is empty (0 bytes)` | The published recording has no content. | Report it as a broken recording; retrying will not help. |
| `ffprobe … not found` | `meetings context` needs `ffprobe` on `PATH`. | Ask the user to install ffmpeg. `list` and `fetch` still work. |

Exit `0` means success — including a `list` that found nothing. Exit `2` is a
usage error on your part. Exit `1` is a runtime failure; read the message before
retrying, since most of these are not worth retrying at all.

## Boundaries

This surface is **read-only**. There is no command here to start, stop, delete or
re-run a recording, and there is no way to read a meeting the account may not
read. `meetings rooms` enumerates rooms, but only the ones this account already
has readable recordings from — it is a view of the same permitted set, not a
directory of the instance's conversations. If the user needs any of that, tell them it is an administrator action in
Nextcloud or in the Cassini operator, and stop.

Meeting transcripts are recordings of real people talking, often candidly. Treat
them as confidential: do not copy them into anything the user did not ask you to
write, and do not send them anywhere outside the work at hand.

Full reference, including the app-password walkthrough and every exit code:
[`docs/agent-meeting-access.md`](../../../docs/agent-meeting-access.md).
