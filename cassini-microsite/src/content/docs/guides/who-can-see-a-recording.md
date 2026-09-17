---
title: Who can see a recording
description: Two audiences for published recordings — anyone with a Nextcloud account, or only the people who were in the call — what each needs, and how to switch.
source: docs/exapp-nextcloud-recordings-permissions.md
copied: "2026-09-17"
---

Recordings are ordinary files in Nextcloud Files, so Nextcloud decides who may
open them. Which of two audiences is in force is one setting in the Cassini app,
under **Operator › Settings › Who can see recordings**. This page covers the two
answers, what each needs, how to move between them, and what a recording you may
not read looks like.

## The two audiences

| | Everyone with a Nextcloud account | Meeting participants |
|---|---|---|
| Who can see a recording | anyone with an account on this Nextcloud | only the people who were in the call |
| Where recordings live | the `cassini` account's own `CassiniNoACL/Recordings` | `Cassini/Recordings`, inside the `Cassini` Team folder |
| Nextcloud apps needed | none | Team folders + Everyone Group |
| Other prerequisites | a `cassini` service account | a `cassini` service account, an `everyone` group, and a mapped, ACL-enabled `Cassini` Team folder |
| On the wire | `"mode": "default"` | `"mode": "access_controlled"` |

Each audience has its own root, and neither can shadow the other:

```text
  the `cassini` service account's Files
  ─────────────────────────────────────
  CassiniNoACL/            its OWN directory. Nothing is mounted over it and no
    Recordings/            other account has a mount of it, which is the whole
      meetings/<id>.opus   of this audience's privacy argument.
      catalog.json

  Cassini/                 the `Cassini` Team folder: `everyone` READ, `cassini`
    Recordings/            ALL, advanced ACLs on, default-deny floor.
      meetings/<id>.opus   MEETING PARTICIPANTS lives here.
      catalog.json
```

Both keep the same shape inside, so everything below the root — the catalog, the
per-meeting `.opus`, the viewer's URLs — is identical either way.

Neither is anonymous. A recording readable by "anyone with a Nextcloud account"
is readable by every signed-in account and by nobody else; Cassini never makes a
public link.

## Which one an install gets

Cassini resolves it when the app is enabled, from what is already on the
instance, and records what it resolved. Nothing is asked and no install waits to
be told:

- Recordings in `Cassini/Recordings` — **meeting participants**, adopted.
  Nothing moves, and an archive in the other root is reported as
  `stranded_recordings`.
- Anything else — **anyone with a Nextcloud account**. A fresh install records
  from the moment the `cassini` account exists.

**Cassini never widens an existing archive on its own.** Every branch either
keeps the audience the recordings already have, or starts an empty archive at
the audience a fresh install gets. Widening is an administrator's deliberate
act, in the settings section, and the confirmation says how many recordings it
is about to make visible to everyone.

## How "meeting participants" is enforced

Under this audience the recordings live in a system-owned Team folder with
advanced ACLs and a default-deny floor. Every account has the same
`Cassini/Recordings` path, and Nextcloud hides individual files from
non-participants.

- A **private** recording is readable only by the people who had access to the
  Talk room when it was published — that is everyone on the room's attendee
  list, including people who were invited but never joined, not only those
  present on the call.
- A recording of a **public** Talk conversation is readable by every signed-in
  account. Publicness is read from Talk at record time and frozen with the
  recording: making a conversation public later does not widen an earlier
  private recording, and making it private does not narrow an earlier public
  one.
- Guest, email and federated participants have no local principal to grant, so a
  private conversation containing only those remains owner-only. Share it as a
  normal Nextcloud file if somebody needs it.

Cassini keeps no separate permission list. The rules are on the files, and an
administrator edits them with the ordinary Files advanced-permissions UI.

### Why a recording is created empty

A file that states no rules of its own inherits the folder's `everyone: READ`,
so a recording uploaded and *then* protected would be readable by every account
for as long as the gap between the two requests lasts. Nextcloud offers no
atomic create-with-ACL, so Cassini closes the gap from the other end: it creates
the file empty, denies it, and only then uploads the audio.

```text
  PUT <id>.opus (empty)  →  PROPPATCH owner-only deny  →  PUT <id>.opus (audio)
                                                       →  PROPPATCH audience
                                                       →  catalog.json
```

The catalog is written last, so a meeting whose ACL did not land is never
advertised.

## Setting up "meeting participants"

Open **Cassini**, then **Operator › Settings › Who can see recordings**, and pick
**Meeting participants**. Cassini checks the two apps first, lists every change
it is about to make, asks Nextcloud to confirm your password, and then makes
them **as you**. It never sees, holds or transmits your password.

The one thing it cannot do is install the two Nextcloud apps: those routes
require your password on the request itself, so Cassini tries through its own
backend and otherwise sends you to Nextcloud's Apps page.

The same recipe by hand:

```bash
occ group:add cassini
occ user:add --group=cassini cassini
occ app:install groupfolders && occ app:enable groupfolders
occ app:install group_everyone && occ app:enable group_everyone
occ groupfolders:create Cassini            # note the id it prints
occ groupfolders:group <id> cassini read write share delete
occ groupfolders:group <id> everyone read
occ groupfolders:permissions <id> --enable
occ groupfolders:permissions <id> -m --user cassini
```

Cassini prints only the lines your instance still needs, under **Details for
administrators**.

**The Everyone Group app is instance-wide.** Its `everyone` group can appear in
Files and other Nextcloud sharing pickers, letting users who may share to groups
select the whole instance. It also covers every Nextcloud account, including
guest-app and external-backend accounts that may sign in. Accept that, or adjust
Nextcloud's broader sharing policy, before installing it.

## Switching

The settings section moves the archive both ways, and it does it by **copying**
into the other audience's root and emptying the source afterwards — never by
moving. A copy means that at every instant the recorded audience names a root
holding a complete archive, including the instant the container is killed
mid-switch.

Two things to know before you press it:

- **Switching to meeting participants does not retroactively restrict
  anything.** Recordings that already existed are copied into the Team folder
  readable by every signed-in account: Cassini does not guess who was in a past
  meeting. Narrowing them is a deliberate act, per recording, from the Files
  app.
- **Switching the other way drops every access rule.** A copy outside a Team
  folder has no per-file rules, so afterwards everyone who can open Cassini can
  read every recording, including ones that had been restricted to a call's
  participants.

Opting out empties the `Cassini` Team folder but leaves it in place, with its
group mappings untouched, so switching back later is immediate.

If a switch stops part way, both roots hold a copy. The app says so and offers
**Resume**; the leftover copy keeps whatever audience it already had.

## Day-to-day permission changes

Access is frozen at publish and stays editable in Nextcloud:

- Open the Team folder → the recording's `.opus` → sharing panel → **Advanced
  permissions**, to add or remove user, group or team read rules.
- Keep the explicit `everyone` rule: deny means private, read means public.
- Removing participant rules leaves a private recording owner-only.
- **Edits survive a re-publish.** Re-delivering a meeting replaces its audio and
  leaves its rules exactly as they are. The one exception is a recording that
  never got an audience at all, where the first publish failed part way, which
  the next publish finishes.

## A recording you may not read looks like one that does not exist

This is deliberate, and it is the same everywhere:

- **In the app**, the meeting list is the intersection of the catalog and what
  your account can actually open. A recording you may not read is simply not in
  your list, and asking for it directly answers 404.
- **From the CLI**, `cassini meetings` answers *no recording you can read* rather
  than "forbidden" or "no such meeting". A recording you cannot see never
  reveals that it exists. See
  [Agent access via the CLI](/docs/guides/agent-access).

Catalog scans fail closed: a mis-configured instance degrades to "no meetings",
never to "everyone's meetings". If somebody who should see a recording gets an
empty list, their account cannot traverse the folder — check with
`occ groupfolders:list` that the folder has advanced ACL and the `everyone: read`
mount, and that `occ user:info <user>` reports `everyone`.

## Checking which audience is in force

- In the app: the audience in force is marked **Current** in the settings
  section, and everybody else sees a chip beside the meeting count reading
  "Visible to anyone with a Nextcloud account" or "Visible to meeting
  participants".
- Over HTTP: `GET /operator/storage` reports the mode, where the decision came
  from, and what is happening at the other root — `migration_clean` when a
  switch did not finish tidying up, and `stranded_root` /
  `stranded_recordings` for a settled instance whose other root still holds
  recordings.

The provisioning internals, the ACL layering, the full state machine behind a
switch and the traversal validation checklist stay in the repository:
[`docs/exapp-nextcloud-recordings-permissions.md`](https://github.com/codemyriad/gocassini/blob/main/docs/exapp-nextcloud-recordings-permissions.md).

## Related

- [Install on Nextcloud](/docs/getting-started/install) — the prerequisites for
  each audience.
- [Privacy and data processing](/docs/guides/privacy) — what is stored, where,
  and what leaves your infrastructure.
- [Troubleshooting](/docs/guides/troubleshooting) — the storage states and what
  clears them.
