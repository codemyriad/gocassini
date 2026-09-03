# Agent access to meeting recordings

How an agent — or any script — running **outside** Nextcloud reads the meetings a
Nextcloud account is allowed to read, with no interactive browser login and no
new server route.

## Goal

Give an agent the three things it needs to reason about your meetings:

| Command | What it answers |
|---------|-----------------|
| `cassini meetings rooms` | Which conversations does this account have recordings from? |
| `cassini meetings list` | Which meetings may this account read — optionally only a room's, or a date range's? |
| `cassini meetings fetch <id>` | Give me that meeting's single portable file. |
| `cassini meetings context <id> [<id> ...]` | Give me those meetings as one document I can read. |

## How it works

Nothing here is a new API. The Cassini app already serves a **per-caller** read
surface for its own viewer, and these commands are a client for it. The app
fetches from Nextcloud Files **as the calling user**, so Nextcloud performs the
authorization and Cassini keeps no separate list of who may see what.

```text
  cassini meetings list
        │
        │  GET https://<nextcloud>/index.php/apps/app_api/proxy/gocassini/published/catalog.json
        │      Authorization: Basic <user>:<app password>
        ▼
  Nextcloud  ── authenticates the app password
        │     ── mints the app-API identity for the session
        ▼
  Cassini app ── PROPFIND / GET Nextcloud Files AS THAT USER
        │
        ├── catalog.json  filtered to what the caller may read
        ├── meetings/<id>.opus   ... or 404
        └── meetings-context?id=…&id=…   the same document `meetings context` prints
```

Three consequences worth internalising before you build on this:

- **A recording you may not read is answered `404`, exactly like one that does
  not exist.** That is deliberate: a recording you cannot see never reveals that
  it exists. The CLI therefore never says "forbidden" or "no such meeting" — it
  says *no recording you can read*.
- **An empty list is ambiguous.** The app answers `200` with no meetings both
  when the account genuinely has none and when the recordings folder is
  mis-provisioned or unreachable. It cannot distinguish them, so neither can the
  CLI; it exits `0` and says so.
- **The surface is read-only.** Only `GET` and `HEAD` reach it. Starting,
  stopping and re-running jobs stays on the operator's admin routes, off the
  agent path entirely.

## Before you begin

You need:

1. **The `cassini` CLI.** From a checkout, `./bin/cassini`. It needs `ffprobe` on
   `PATH` for `meetings context`, which reads the downloaded meeting's metadata.
   `cassini doctor` checks for it.
2. **A Nextcloud account** on an instance running the Cassini app, which can read
   at least one recording. Verify in the browser first: if the account cannot see
   a meeting in the Cassini viewer, the CLI will not see it either. That is the
   whole point.
3. **A Nextcloud app password** for that account — not the login password.

### Create the app password

There is no CLI recipe for this; create it in the Nextcloud web UI.

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
`http://`, so a typo cannot put the app password on the wire in clear text. Add
`--insecure` only against a local harness with a self-signed certificate.

## 2. List what the account may read

```bash
./bin/cassini meetings list
```

Expected:

- A first line summarising the result:
  `meetings=2 caller=alice source=nextcloud-files`
- One line per meeting, newest first, matching the order the viewer shows:
  `meeting=01JZ8K… date=2026-08-11 10:32 room=rm_9f2a1c3d4e5b6a70 title=Daily Standup speakers=3 segments=120 duration_ms=1800000 fetchable=yes`
- Exit code `0`.

`source=nextcloud-files` confirms the bytes came from Nextcloud Files, with
per-caller permissions applied. `source=unknown` means they did not — you are
most likely talking to a development operator serving a local archive, which has
no per-caller access control at all. `source=unrecognised` means the response
claimed some other origin, which is equally not a guarantee. Anything but
`nextcloud-files` also prints a warning on stderr, from all three commands.

`fetchable=no` marks a meeting recorded before the single-file format: it has no
portable `.opus`, so `fetch` and `context` cannot serve it.

For a machine-readable list, add `--json`. Entries are re-emitted exactly as the
server sent them, so the server's payload stays the single contract:

```bash
./bin/cassini meetings list --json | jq -r '.meetings[0].id'
```

The document also carries `skipped`: the number of catalog entries that had no id
and were dropped. A non-zero value means the list is incomplete — the catalog is
malformed rather than short — so check it before treating the list as the whole
truth.

### Narrowing the list

Three optional filters, which combine — a meeting must satisfy all of the ones
you pass:

```bash
./bin/cassini meetings list --room rm_9f2a1c3d4e5b6a70
./bin/cassini meetings list --from 2026-08-01 --to 2026-08-31
./bin/cassini meetings list --room rm_9f2a1c3d4e5b6a70 --from 2026-08-01 --json
```

`--from` and `--to` take the dates the catalog itself prints — `2026-08-11`,
`2026-08-11 14:30`, or `2026-08-11 14:30:05`. They carry **no timezone**,
because a meeting's `dateLabel` does not: it is a wall clock, and asserting a
zone here would claim an instant the data cannot justify. A bare date covers the
whole day at both ends, so `--from 2026-08-01 --to 2026-08-31` means all of
August. A range whose ends are backwards is rejected (exit `2`) rather than
returning an empty list that would be indistinguishable from having no
recordings at all.

`--room` takes the `room=` value printed by `cassini meetings rooms` — see
below. Matching is exact; there is no substring search.

When any filter is in effect the output gains a line naming it and counting what
it removed, so a short list is never mysterious:

```text
filter=from:2026-08-01 00:00:00 room:rm_9f2a1c3d4e5b6a70 excluded=37
```

In `--json` the same information is a `filter` object plus `excluded` and
`excludedUndated` counts. `excluded` is deliberately **not** folded into
`skipped`: `skipped` means the catalog is malformed, which a filter doing its
job is not.

A filter that matches nothing says so explicitly, and does not repeat the
mis-provisioned note — that would send you to debug a provisioning failure that
does not exist.

Meetings whose `dateLabel` cannot be parsed are left out of any dated range, in
both directions, and counted in a note. Their timestamp is unknown, so including
them would make the same meeting appear or vanish depending on which end of the
range you typed.

## 2b. List the rooms

```bash
./bin/cassini meetings rooms
```

```text
rooms=2 caller=alice source=nextcloud-files
room=rm_9f2a1c3d4e5b6a70 name=Weekly Sync meetings=12 latest=2026-08-11 10:32 earliest=2026-05-05 10:30
room=rm_11bb22cc33dd44ee name=Old Standup meetings=3 latest=2026-07-02 09:00 earliest=2026-06-18 09:00
```

The rooms are derived from the catalog you may already read, not fetched from
Talk, so a room you have no readable recording from does not appear. This
discloses nothing that `list` does not.

The `room=` column is the value `--room` accepts. It is a **derived id**, and
deliberately not the Talk conversation token: for a public conversation that
token is also the link that joins it, so publishing it alongside a recording
every signed-in account may read would turn "may read a past recording" into
"may join the live conversation". The id is a one-way function of the room's
identity — deterministic, so a room always derives the same id, and not
reversible into the token.

Set `CASSINI_ROOM_ID_PEPPER` on the app to a stable deployment-wide secret. A
Talk token is short, so an unpeppered derivation can be reversed by enumerating
the token space offline; with a pepper it cannot. Choose it once — changing it
changes every id, and already-published meetings keep the ids they were written
with. Re-running `scripts/backfill-catalog-rooms.sh --apply` re-derives every
meeting whose operator job row survives, so a rotation is a re-run rather than a
manual merge for most of an archive.

**Two rows can share a display name.** A recording this installation has *no job
row* for — one imported from elsewhere, or older than the operator's job store —
has no token anywhere, so `scripts/backfill-catalog-rooms.sh` derives its id
from the room *name* instead. That derivation cannot agree with a token-derived
one, and only a person knows the two are the same conversation;
`scripts/reattribute-catalog-room.sh` is how that person says so, once, and
merges them.

It is a much smaller set than it used to be. The catalog entry's id is the
operator's job id, and the operator's job database still holds the Talk room
token for every job it ran — so for anything this installation produced, the
backfill recovers the *real* id rather than a name-derived stand-in. That is
also why the reattribution tool now **refuses** a meeting with a recorded room
binding: for those the truth is recoverable, and asserting an id instead would
leave a recording whose lineage and published room permanently disagree.

A trailing note may report meetings that carry **no room at all** — a non-Talk
job, or an old recording whose file holds no usable room name either. They are
real, `list` shows them, and no `--room` value reaches them.

## 3. Read meetings as context

```bash
./bin/cassini meetings context 01JZ8K3M4N5P6Q7R8S9T0VWXYZ
./bin/cassini meetings context 01JZ8K3M… 01K2R4N7… 01K5T9P2…
```

Expected: a markdown document on stdout — for each meeting, its identity and
duration, the summary if one was generated, and the transcript as
speaker-attributed paragraphs. Use `--out FILE` to write it to a file instead,
or `--json` for the structured form (`cassini.meetings.context.v1`).

**Several ids produce one document**, holding the meetings in the order you
named them and separated by a `---` rule. In `--json` they arrive as a
`meetings` array whose every entry is exactly the single-meeting document the
command has always produced; one id is still that bare document, unwrapped. The
whole run is fetched against a single view of what you may read, and **an id you
cannot read fails all of it** — a bundle that quietly dropped a meeting would
answer a question asked of all of them using only some of them, and look right
doing it. The failing id is named on stderr, because the 404 wording cannot be.

`--timestamps` cites each passage's start time beside the speaker, as `MM:SS` or
`H:MM:SS` past an hour. Off by default, because it changes bytes consumers have
pinned. Use it when the answer has to point at where something was said; the
`--json` form always carries the raw timings whether or not it is set.

**Read this before quoting the transcript.** The transcript is *assembled from
the recording's word timings*, and both output modes label it
`derived-from-words`. A published meeting carries word-level timings and no
separately cleaned-up transcript, so paragraph breaks are inferred from pauses
and speaker changes. The words are verbatim; the punctuation and paragraphing are
not editorial. Do not present it as an edited or approved transcript.

If the meeting has no summary you get `_No summary was generated for this
meeting._` and a note on stderr. That is normal, not a failure: summaries are
generated only when the deployment has a summariser configured.

`--keep-opus FILE` keeps the download for one meeting; it is refused for several,
because one file cannot hold them. So is the same id given twice.

### Reading meeting files you already have

```bash
./bin/cassini meetings context --local ./meetings/01JZ8K3M….opus
./bin/cassini meetings context --local --catalog ./catalog.json ./meetings/*.opus
```

`--local` reads portable `.opus` files off disk instead of fetching ids from
Nextcloud, and needs none of the connection settings above. It is the same
reader, the same prose derivation and the same document — the file is simply
already there.

Two things follow from that, and both matter if you want the two paths to agree:

- **A meeting's id is its file's basename without the `.opus`.** That is how a
  published archive names a recording (`meetings/<id>.opus`), so files kept by
  `meetings fetch --out "<id>.opus"` keep the archive's ids for free.
- **The room is not in the file.** A catalog entry's room id is what the
  operator keeps current, and a room's display name lives only in the catalog
  because a name is editable and a sealed recording is not. `--catalog` points
  at a `catalog.json` to read both from, matched on the id, and a local run with
  one produces the same bytes as an id run over the same meetings. Without it a
  meeting renders with whatever room id its file was tagged with and no room
  name. A `--catalog` that does not list a named meeting is refused rather than
  quietly rendering it roomless.

## 4. The same document, served to the Cassini app

```text
GET published/meetings-context?id=<id>&id=<id>[&format=json][&timestamps=true]
```

The app needs the same bundle the CLI prints, and "the same" is only true by
construction if there is one implementation. So the operator does not assemble
one: it resolves what you may read exactly as it resolves `catalog.json`,
fetches each recording from Nextcloud Files **as you**, and runs
`cassini meetings context --local` over the files — the CLI's own bytes, relayed.

It is a sibling of `catalog.json` under the same USER-level `published/` route,
so it is `GET`/`HEAD` only and needs no new server route.

| Answer | Means |
|---|---|
| `200` | The document, as `text/markdown` or `application/json` |
| `400` | The query is wrong — no ids, a repeated id, more than 20, or an unreadable `format`/`timestamps`. Nothing was fetched. |
| `404` | One of the ids is not in your readable set. Absent and denied are the same answer, as everywhere else here. |
| `502` | The request could not be answered: Nextcloud Files was unreachable, the caller could not be identified, or the render failed. **Never** an empty document. |

At most 20 meetings per request, because every id costs a whole recording
download and an uncapped set is a way to make the operator pull the archive.

## 5. Download the meeting file itself

```bash
./bin/cassini meetings fetch 01JZ8K3M4N5P6Q7R8S9T0VWXYZ --out "./Daily Standup.opus"
```

Expected: `portable_meeting -> ./Daily Standup.opus bytes=…`, and a file that is
byte-identical to the published one. It is a single self-contained meeting —
audio plus the embedded transcript and summary — playable in any Opus player and
readable by `cassini inspect`:

```bash
./bin/cassini inspect "./Daily Standup.opus"
```

An interrupted download never lands at the destination: the transfer is staged
alongside it and moved into place only once complete, and an empty (0-byte) reply
is refused rather than saved as a `.opus` that fails when something reads it.

The file is created **readable by you only**. It holds a private meeting's audio
and transcript, and Nextcloud decided who may see it — so it is not published to
every account on a shared host. `chmod` it yourself if you need it wider.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success — including a `list` that found no readable meetings |
| `1` | Runtime failure: credentials rejected, nothing readable at that id, Nextcloud Files unavailable, unreadable meeting file |
| `2` | Usage or configuration error: a missing flag, a bad argument, an unparseable `--from`/`--to`, or a date range whose ends are backwards |

## Troubleshooting

**`Nextcloud rejected the credentials for user "alice"`** — the app password is
wrong, revoked, or belongs to a different account. Generate a new one. The error
names where the credential was read from (`env CASSINI_NC_APP_PASSWORD` or
`flag --app-password`) so you know which to fix; it never echoes the value.

**`meetings=0` with the mis-provisioned note** — either the account genuinely has
no readable recordings, or the recordings folder is not set up. Check the same
account in the Cassini viewer in a browser: if it sees nothing there either, this
is a provisioning question, not a CLI one.

**`your filter excluded all N readable meeting(s)`** — not a permissions or
provisioning problem: the account can read N meetings and your filter matched
none of them. Widen it, or run `cassini meetings rooms` for a `room=` value that
exists. `--room` matches exactly and takes the printed value verbatim.

**A meeting shows `room=-` and no `--room` value finds it** — it records no room
at all, which is what every recording published before Cassini kept the room
looks like. List it without `--room`, and ask an administrator to run
`scripts/backfill-catalog-rooms.sh` on the installation: it recovers the real
room from the operator's own job history where that survives, and from the
published file's name where it does not.

**`no recording you can read at that id`** — the id is absent from *this
account's* catalog. It may not exist, or it may exist and belong to someone else;
these are answered identically on purpose. Run `meetings list` to see what this
account can read.

**`Nextcloud Files is unavailable`** — an outage on the Nextcloud side, not a
permissions problem. Retrying later is reasonable; re-checking the app password
is not.

**`refusing to follow a redirect`** — the CLI will not follow redirects, because
the Nextcloud credentials would travel to wherever the redirect points. Set
`--nextcloud-url` to the URL your instance actually serves on (most often this
means `https://` rather than `http://`).

**`the catalog points outside the Nextcloud you configured`** — a catalog entry
named a different host. That is refused for the same reason: the request carries
your app password. If it is not an attack it is a misconfigured export, and it is
worth reporting.

**`read the downloaded meeting: … not a portable meeting`** — the fetched file is
not a Cassini portable `.opus`. Keep it with `--keep-opus FILE` and run
`cassini inspect` on it to see what arrived.

**`ffprobe … executable file not found`** — `meetings context` needs `ffprobe` on
`PATH` to read the meeting's metadata, on both the id path and `--local`.
`meetings list` and `meetings fetch` do not. Install ffmpeg, or use `fetch` and
inspect elsewhere.

**`context failed on meeting id "…"; a bundle is all N of its meetings or none`**
— one id of several could not be read, so no document was produced. It is absent
or it belongs to someone else; these are answered identically on purpose. Drop
it, or run `meetings list` to see what this account can read.

## Related

- [Portable meeting format](./portable-meeting-format.md) — the contract for the
  `.opus` file this fetches.
- [Nextcloud recordings permissions](./exapp-nextcloud-recordings-permissions.md)
  — who can read which recording, and how that is configured.
- [Operator API](./reference/api.md) — the full HTTP surface, including the admin
  routes this deliberately does not touch.
