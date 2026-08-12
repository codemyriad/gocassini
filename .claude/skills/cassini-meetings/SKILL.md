---
name: cassini-meetings
description: Read Cassini meeting recordings — list meetings, pull one meeting's transcript and summary as context, or download its portable .opus — using the `cassini meetings` CLI against a Nextcloud instance. Use when the user asks what was said or decided in a meeting, asks you to turn a recorded conversation into a plan, issues, notes or a summary, or refers to "the recording", "the call", "last week's meeting", or a meeting by name or date.
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

```bash
./bin/cassini meetings list
```

Newest first, one line per meeting:

```text
meetings=2 caller=alice source=nextcloud-files
meeting=01JZ8K3M4N5P6Q7R8S9T0VWXYZ date=2026-08-11 10:32 title=Daily Standup speakers=3 segments=120 duration_ms=1800000 fetchable=yes
```

To pick programmatically, use `--json` (newest is first):

```bash
./bin/cassini meetings list --json | jq -r '.meetings[0].id'
```

Check `.skipped` in that document before treating the list as complete: a
non-zero value means some catalog entries were unusable and dropped, so meetings
may be missing that the account can in fact read.

When several meetings could match what the user meant, **show the candidates and
ask** rather than guessing. Titles repeat week to week, so a title alone is
usually ambiguous; the date disambiguates.

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

1. `meetings list` → identify the conversation. Confirm the choice with the user
   if more than one plausibly matches.
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
| `meetings=0` + mis-provisioned note | Either the account may read nothing, or the recordings folder is not set up. The server cannot tell these apart, so neither can you. | Report both possibilities. Suggest checking the same account in the Cassini viewer in a browser. Do not retry. |
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
read. If the user needs any of that, tell them it is an administrator action in
Nextcloud or in the Cassini operator, and stop.

Meeting transcripts are recordings of real people talking, often candidly. Treat
them as confidential: do not copy them into anything the user did not ask you to
write, and do not send them anywhere outside the work at hand.

Full reference, including the app-password walkthrough and every exit code:
[`docs/agent-meeting-access.md`](../../../docs/agent-meeting-access.md).
