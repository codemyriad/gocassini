# Managing recording permissions in Nextcloud Files

How **per-participant access control** for Cassini recordings works and how to
operate it. A private recording is visible only to the people who had access to
the Talk room when it was published — that is everyone on the room's attendee
list, including users who were invited but never joined the call, and not only
those present during it. A recording of a public Talk conversation is visible to
every authenticated Nextcloud account. Nextcloud's Team-folder ACLs are the
authority, and administrators can edit them with the normal Files
advanced-permissions UI.

## Two storage modes (D-616)

Who can see a recording is one setting in the Cassini app, **Operator ›
Settings › Who can see recordings**, with two answers. The rest of this document
describes the second one.

| | Everyone with a Nextcloud account | Meeting participants |
|---|---|---|
| Who can see a recording | anyone with an account on this Nextcloud | only the people who were in the call |
| Where recordings live | the `cassini` account's own `CassiniNoACL/Recordings` | `Cassini/Recordings`, inside the `Cassini` Team folder |
| Nextcloud apps needed | none | Team folders + Everyone Group |
| Other prerequisites | a `cassini` service account | a `cassini` service account, an `everyone` group, and a mapped, ACL-enabled `Cassini` Team folder |
| On the wire | `"mode": "default"` | `"mode": "access_controlled"` |

**Each mode has its own root, and neither can shadow the other.** Both addressed
`Cassini/Recordings` until D-616's followups. That was cheap for writes and
expensive for everything that has to reason about *where* the archive is: a
mounted Team folder wins that path and Nextcloud physically renames the colliding
home directory out of the way, so the two archives can never exist at the same
time (measured — [Why the roots are separate](#why-the-roots-are-separate)). That
one fact forced the switch between modes to be a MOVE, the source to be
discovered by a regex against a server-chosen name, a staging directory to hold
the archive in transit, and the opt-out to unmap the Team folder — the one call
in the whole feature that Nextcloud refuses to an ExApp.

```text
  the `cassini` service account's Files
  ─────────────────────────────────────
  CassiniNoACL/            its OWN directory. Nothing is mounted over it and no
    Recordings/            other account has a mount of it, which is the whole of
      meetings/<id>.opus   the DEFAULT mode's privacy argument.
      catalog.json

  Cassini/                 the `Cassini` Team folder: `everyone` READ, `cassini`
    Recordings/            ALL, advanced ACLs on, default-deny floor.
      meetings/<id>.opus   ACCESS CONTROLLED lives here.
      catalog.json
```

Both keep the same internal shape, so everything below the root — the catalog,
the per-meeting `.opus`, the viewer's URLs — is identical either way. Only the
first segment differs, and it is chosen once per delivery and once per read.

```text
                        storage_settings.json
        {"access_control_enabled": …, "source": …, "migration_clean": …}
                                │
          ┌─────────────────────┴─────────────────────┐
          ▼                                           ▼
   ANYONE WITH AN ACCOUNT                     MEETING PARTICIPANTS
  root:  CassiniNoACL/Recordings           root:  Cassini/Recordings
  write: PUT, no ACLs                      write: reserve → deny → PUT → audience
  read:  as `cassini`, whole archive       read:  as the CALLER, Nextcloud decides
  needs: the service account               needs: both apps + the Team folder
```

**Which one an install gets.** Cassini resolves it on the AppAPI enabled edge,
from what is already on the instance, and records what it resolved. Nothing is
asked, and no install waits to be told:

```text
  storage_settings.json records one   ─▶ use it verbatim. Nothing re-opens it,
                                         ever, for the life of the install.
  CASSINI_STORAGE_MODE declares one   ─▶ that mode, for a DEVELOPMENT or CI
                                         deployment, and only where the instance
                                         matches it. Written down after the
                                         gates, not before.
  recordings in `Cassini/Recordings`  ─▶ MEETING PARTICIPANTS, adopted. Nothing
  (with or without an archive in the     moves, and an archive in the other root
   other root)                           is reported as `stranded_recordings`.
  anything else                       ─▶ ANYONE WITH A NEXTCLOUD ACCOUNT. A
                                         fresh install records from the moment
                                         the `cassini` account exists.
```

**Cassini never widens an existing archive on its own.** That is the whole of
the safety argument above: every branch either keeps the audience the recordings
already have, or starts an empty archive at the audience a fresh install gets.
Widening is an administrator's deliberate act, in the settings section, and it
says how many recordings it is about to make visible to everyone.

The earlier build fell back to `default` and wrote that down on the first healthy
enable, whatever it found: that made "anyone with an account can see every
recording" a decision nobody had taken, on an instance whose Team folder was
full of recordings that nobody could see before. The branch above is the narrow
version — it reads the archive that exists rather than guessing from the
presence of a folder.

**"Recorded" and "chosen" are different things.** `storage_settings.json` carries
a `source` field, and it is what tells them apart:

| `source` | Chosen? | What it means |
|---|---|---|
| `user` | yes | An administrator picked it in **Operator › Settings › Who can see recordings** |
| `resolved_on_enable` | yes | Cassini resolved it when the app was enabled, from the archive the instance already held |
| `env` | yes | `CASSINI_STORAGE_MODE` declared it and the instance matched |
| `migrating` | no | A switch was interrupted; this is where the recordings are, not what anybody wanted |
| `default` / `derived` | no | An older version decided on its own |
| absent | no | Written before the field existed |

An **unconfirmed** mode still governs — the archive really is at that root, and
reads and writes both work — and it no longer refuses anything. The distinction
is provenance, kept so the audit trail says which build wrote the file; every
earlier release reported all of these as `configured`, so a fallback and a click
were indistinguishable on the wire, and that is what the field exists to end.

A settings file that exists but cannot be parsed is **not** "no decision": Cassini
keeps access control on, says so in the log, and writes nothing over a file it
could not read. One bad byte falling through to the open model would publish the
next recording where every account can read it. It is reported as unconfirmed,
and an administrator leaves that state by picking an audience in the settings
section, which writes the file afresh.

**The deploy option is for development and CI.** It is believed only where it
fits: a declared mode whose prerequisites are missing, or that meets recordings
in **both** folders, or the same recording in both, is refused at enable time
with `storage_mode_declared_conflict` and **not** recorded. The whole reason a
harness declares a mode is that it knows what it built, so a declaration that
disagrees with the instance is a bug in the stack rather than an instruction —
and recording it would outlive every fix, because a recorded mode is never
reconsidered. The first pass wrote it down before any gate, so that an
administrator could read back what they had declared; that was the right trade
for a production affordance and the wrong one for this.

**Switching.** The settings section relocates the archive both ways, and it does
it by **copying** into the other mode's root and emptying the source afterwards —
never by moving. A copy means that at every instant the recorded mode names a
root holding a complete archive, including the instant the container is killed
mid-switch. Opting in
leaves every copied recording **readable by every account** — Cassini does not
guess who was in a past meeting — so narrow the ones that matter afterwards from
Files → Advanced permissions. Opting out drops every access rule, so afterwards
everyone who can open Cassini can read everything, and it leaves the `Cassini`
Team folder mounted and empty rather than unmapping it. See [Switching modes on
an instance that already has
recordings](#switching-modes-on-an-instance-that-already-has-recordings).

**Switching always replaces the destination archive with the source archive.**

Normally the destination is empty and the switch copies the source, flips the
mode, then clears the source. If the destination contains recordings or a
catalog, Cassini stops before writing and the request is refused until it comes
back with `confirm_overwrite: true`; it then clears the destination, copies the
complete source archive, and clears the source. There is no keep, skip, merge,
or newest-copy mode. The app does not reach that guard: recordings in both roots
are resolved before a switch is offered, by the banner that counts them and
offers to move them in. Asking for the audience already in force only records
it; it moves and overwrites nothing.

The operator repeats this guard under its provisioning lock: a `PUT /storage`
request without `confirm_overwrite: true` is refused if the destination contains
artefacts, even when an earlier preview was empty or stale.

> **Cassini can set most of this up for you (D-671).** Picking **Meeting
> participants** in the settings section lists what is missing and offers to make
> it: the `cassini` group and account, the Team folder, its group mappings,
> advanced ACL, and the ACL-manager delegation. It performs them **as you**, from
> your browser, after Nextcloud's own password dialog — Cassini never sees, holds
> or transmits your password.
>
> The exception is installing the two Nextcloud apps. Those routes require your
> password on the request itself, which Cassini will not handle, so it tries via
> its own backend (which works on some releases) and otherwise sends you to
> Nextcloud's own Apps page. Everything is also listed as `occ` commands for an
> administrator who would rather run them.
>
> Until the prerequisites for the selected mode are there, recordings are
> refused rather than captured and lost.

> **Who owns the recordings.** Both modes store them as a dedicated `cassini`
> service account. It is the only identity that writes recordings and, under
> access control, manages their permissions. Create it with
> `occ group:add cassini` and `occ user:add --group=cassini cassini`.
>
> Privileged *reads* — which apps are enabled, whether the account exists,
> whether there is a Team folder and what is mapped to it — are performed as a
> separately resolved administrator. A mode switch makes no privileged writes at
> all any more: it is WebDAV as the service account from end to end, in both
> directions.
>
> That administrator is *probed*, not looked up: an external app cannot be told
> who it is, and every API that would reveal one is admin-gated. Cassini tries
> `CASSINI_NC_ADMIN_USER`, then `admin`, then the instance's own account list
> (up to a bounded number), and uses the first that turns out to be an
> administrator. If none is, the preflight stops and says so rather than acting
> as an account that may not exist. Set `CASSINI_NC_ADMIN_USER` then — see
> [the install guide](./exapp-install.md#administrator-discovery).

> **Public conversations.** Publicness is read from Talk at record time and
> frozen with the recording. Making a conversation public later does not widen
> an earlier private recording, and making it private does not narrow an earlier
> public recording. If room lookup fails, Cassini treats the recording as
> private. A public recording is still sign-in-only; no anonymous link is made.

> **Audience resolution.** The Talk attendee list is read at publish time after
> bounded retries and an owner fallback. A private conversation containing only
> guest, email, or federated participants has no local principal to grant, so it
> remains owner-only. A public conversation remains readable to every account
> even when all attendees joined as guests.

## How it works

```text
  PUBLISH (write)                                    VIEW (read)
  ───────────────                                    ───────────
  `cassini` owner delivers <id>.opus to NC Files     browser opens Cassini
        │                                                  │
        │  enumerate Talk audience                          │  authenticated caller
        ▼                                                  ▼
  PROPPATCH leaf nc:acl-list:                       operator proxy acts AS CALLER:
    private → everyone DENY + participants READ       catalog → owner copy filtered by
    public  → everyone READ                             caller's own PROPFIND result
    both    → recordings owner ALL                    audio   → caller-side DAV GET
        │                                                  │
        ▼                                                  ▼
  decision frozen at publish, editable in Files      Nextcloud enforces the leaf ACL;
                                                      denied and absent both become 404
```

Under access control the recordings live in a system-owned **Team folder** with
advanced ACLs and a default-deny floor. This gives every user the same
`Cassini/Recordings` path while allowing Nextcloud to hide individual leaves from
non-participants. Which identity the bytes are fetched as and which root they are
fetched from are one decision, not two: reading the ACL'd root as the *caller* is
what makes Nextcloud enforce the per-file rule, and reading the private root as
the *owner* is what makes a folder nobody has a mount of readable at all. Pairing
either with the other's root is the disclosure the guard around it exists to
prevent, so neither is chosen without the other.

### Why a recording is created empty

A leaf that states no rules of its own inherits the container's `everyone: READ`,
so a recording uploaded and *then* protected is readable by every account for as
long as the gap between the two requests lasts. Nextcloud offers no atomic
create-with-ACL for a file, so Cassini closes the gap from the other end: it
creates the leaf empty, denies it, and only then uploads the audio. An
overwriting PUT keeps the file's id and Group Folders keys its rules by that id,
so the deny written against the empty file still covers the audio that replaces
it. The only thing ever briefly visible without rules is a zero-byte file.

```text
  PUT <id>.opus (empty)  →  PROPPATCH owner-only deny  →  PUT <id>.opus (audio)
                                                       →  PROPPATCH audience
                                                       →  catalog.json
```

The catalog is written last, so a meeting whose ACL did not land is never
advertised. If a delivery is interrupted, the next publish repairs the leaf: a
recording found carrying no `everyone` rule is denied, removed, and re-created.
The deny before the removal is deliberate — a deleted leaf keeps its rules in the
Team folder trash, and one with no rules stays readable there.

### Permission layers

Team-folder mount permissions are a capability ceiling; advanced ACLs cannot
raise a read-only mount to write access. Therefore the virtual universal group
and the owner group remain separate:

```text
  TEAM-FOLDER MOUNTS                    ADVANCED ACLS
  ──────────────────                    ─────────────
  everyone             READ             Cassini root:
  cassini owner group  ALL                everyone        READ
       │                                    owner          ALL
       │
       └── owner group must stay narrow   private leaf:
                                             everyone      DENY
                                             participants  READ
                                             owner         ALL

                                          public leaf:
                                             everyone      READ
                                             owner         ALL
```

Assigning `everyone: ALL` at the mount and relying only on advanced ACLs would
work during normal operation, but disabling advanced ACLs would make the whole
folder writable by every account. Cassini deliberately retains a narrow owner
group with `ALL` and gives `everyone` only `READ`.

- **Every account has the directory from account creation.** The Everyone Group
  app supplies a virtual `everyone` group through Nextcloud's group backend.
  There are no materialized memberships, OCS user sweeps, or convergence delay.
- **Private recordings stay participant-only.** A leaf-level `everyone` deny
  overrides inherited root traversal. Participant user/group/team allows at the
  same path restore read only for that audience.
- **Public recordings are account-wide.** Their leaf-level `everyone` rule is a
  read allow. The route is still authenticated.
- **The authoritative catalog remains owner-only.** Cassini scans the caller's
  visible leaves and returns only matching catalog entries.

## Prerequisites

- Nextcloud **32+**.
- **Group folders** (displayed as **Team folders**) installed and enabled.
- **Everyone Group** (`group_everyone`) installed and enabled. Use a release
  compatible with the installed Nextcloud major version. It creates the fixed
  virtual group ID `everyone`.
- Cassini deployed through AppAPI, with **Meeting participants** in force:
  already recorded, picked in the settings section, resolved when the app was
  enabled because the `Cassini` Team folder already held recordings, or — on a
  development or CI stack — declared by
  `CASSINI_STORAGE_MODE=access_controlled`. It was unconditional between D-554
  and D-616; it is now one of two audiences, and an install nobody has told
  records as **anyone with a Nextcloud account**.

Both native apps must be enabled before Cassini's enabled lifecycle callback.
Cassini checks that `everyone` exists and refuses to create the recording ACL
substrate when it does not. It never creates an ordinary empty group named
`everyone`, because that would silently restore the new-account race.

### Global Everyone Group behavior

`group_everyone` is instance-wide, not Cassini-specific. Its group can appear in
Files and other Nextcloud sharing pickers, allowing users who may share to groups
to select the whole instance. It also covers every Nextcloud `IUser`, including
guest-app or external-backend accounts that are allowed to sign in. Installers
must accept that global behavior or configure Nextcloud's broader sharing policy
accordingly.

## Setup: what you do, and what Cassini does

Open **Cassini**, then **Operator › Settings › Who can see recordings**, and
pick **Meeting participants**. Cassini checks the two apps first, lists every change
it is about to make, asks Nextcloud to confirm your password, and then makes them
as you. It cannot install the two apps — see below — and will say so.

The same recipe by hand, in the order Cassini lists whatever is missing:

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

Cassini prints only the lines this instance still needs, under **Details for
administrators**, so it is usually shorter than the block above.

### Why Cassini can do all of that except install an app

Nextcloud guards these writes with password confirmation, and it has two
strengths. The ordinary one is satisfied by a recently confirmed **session** —
which is what Nextcloud's own dialog produces, and why Cassini can create the
account, the folder and its permissions from your browser. Installing an app is
annotated `strict`, and strict requires the password **on the request itself**;
no session, however recently confirmed, satisfies it. Nextcloud's own Apps page
meets that by attaching your password to that one request. Cassini declines to
handle your password at all, so it hands those two steps back to you.

Cassini's *operator* cannot make any of these writes: its requests carry no
login token, so Nextcloud refuses them outright on every current release. That
is why the setup happens in your browser and not in the app's backend. It does
attempt the two app installs from the backend, because that path still works on
Nextcloud 32.x, 33.0.0–33.0.5 and 34.0.0, and on any release where an
administrator has set `allowed_no_password_confirmation_ranges`.

Then disable and re-enable the app (setup runs on the AppAPI **enabled** edge,
not on start), and pick **Meeting participants** in the settings section if it
is not already in force.

On that edge Cassini probes the instance read-only, resolves the storage mode,
and — only once every prerequisite above is confirmed present — arranges its own
tree inside the folder you provided, idempotently:

```text
  ├── resolve the administrator to act as                 (OCS API)
  ├── check both native apps are enabled                  (OCS API)
  ├── check the `cassini` account exists                  (OCS API)
  ├── check the virtual group `everyone` exists            (OCS API)
  ├── check Team folder `Cassini`: mapped, ACL on,         (Team folders API)
  │   `cassini` delegated as ACL manager
  ├── temporarily narrow the root to owner ALL            (DAV PROPPATCH)
  ├── migrate/protect every existing recording leaf       (DAV PROPFIND/PROPPATCH)
  │     recording-viewers READ → everyone READ
  │     recording-viewers DENY → everyone DENY
  │     legacy admin owner     → cassini owner
  │     no broad rule          → everyone DENY
  ├── protect an existing catalog for owner-only access   (DAV PROPPATCH)
  ├── grant everyone READ at the root                     (DAV PROPPATCH)
  ├── remove legacy viewer/admin mappings + admin manager (Team folders API)
  └── materialize Cassini/Recordings/meetings             (DAV MKCOL)
```

If any check fails, none of the writes below it run. The step is recorded, the
app shows it with the command that fixes it, and publishing is refused — rather
than half-building a substrate that later reads as healthy.

When recordings are visible to anyone with a Nextcloud account, this whole
sequence collapses to two things: MKCOL
`CassiniNoACL/Recordings/meetings` in the service account's own home, and a
one-time adoption of any archive left at the pre-split path by an older version
(see [Upgrading an install from before the roots were
split](#upgrading-an-install-from-before-the-roots-were-split)). There is no
folder, no floor and no leaf rule, because there is no other account with a path
to any of it.

The root remains owner-only if leaf or catalog migration fails, preventing a
partially migrated private file from inheriting broad read. Re-enabling Cassini
retries the idempotent sequence. The legacy ordinary `recording-viewers` group
itself is left untouched in case an administrator reused it elsewhere; Cassini
no longer reads or writes its memberships. The old `admin: ALL` mount mapping is
also removed after `cassini: ALL` has been installed and the migrated root has
been written successfully. The old `admin` ACL-manager delegation is removed at
the same point, leaving the dedicated service account as the ACL authority and
its owner group as the narrow write principal.

The dedicated `cassini` service account is the recording owner and ACL manager.
It is the only member Cassini adds to the narrow `cassini` owner group, whose
`ALL` Team-folder mapping is required for writes. The virtual `everyone` group
remains `READ`; ACL-manager status cannot elevate that mount capability.

Verify the result:

```bash
occ app:list | grep -E 'groupfolders|group_everyone'
occ groupfolders:list --output=json_pretty
# acl: true, acl_default_no_permission: false
#   `true` there is a folder made by an older Cassini: that flag pins every
#   path at READ, so no recording can ever be deleted or moved by anyone,
#   and /status reports it as degraded / legacy_deny_floor (D-612). Nothing
#   creates it any more, and Group Folders has no setter to clear it.
# groups: cassini=31, everyone=1; manage: user cassini

occ user:info cassini
# groups includes: cassini (narrow write-capable owner group)

occ user:info <any-user>
# groups includes: everyone (virtual; no membership reconciliation required)
```

### New accounts

The virtual backend reports `everyone` from the moment an account exists and
emits Nextcloud's user-added event for mount/share cache invalidation. A Team
folder already assigned to `everyone` is therefore present on the account's
first filesystem request. Cassini does not mutate group memberships when the
viewer opens and does not proxy around a missing mount; a missing mount is a
broken prerequisite and fails closed with an empty catalog.

## Is it on?

Open **Cassini**, then **Operator › Settings › Who can see recordings**. The
audience in force is marked **Current**; the other one is either offered as a
switch or shows what it is still missing. Everyone else sees it without the
settings page: the meeting list carries a chip reading "Visible to anyone with a
Nextcloud account" or "Visible to meeting participants".

Over HTTP, `GET /operator/storage` answers the same question with the mode, its
source (`configured` when it was read from the settings file, `env` when
`CASSINI_STORAGE_MODE` declared it, `resolved_on_enable` when Cassini resolved it
from the archive it found), and per-mode availability. The file itself records a
little more — `user` when an administrator switched in the settings section,
`derived` on installs from the build that inferred the mode — but anything read
back off disk reports as `configured`, because that is what it now is. It also
reports what is happening at the *other* root:

| Field | Meaning |
|---|---|
| `migration_clean` | false when a mode switch stopped before it finished tidying up. The archive is still complete at the mode's own root; the other one holds leftovers |
| `pending_cleanup` | the root holding those leftovers. Empty when `migration_clean` |
| `stranded_root`, `stranded_recordings` | a *settled* instance whose other mode's root still holds recordings. Not a failure — publishing and reading both work — but the symptom is "my recordings are gone" and the cause is a mode nobody switched |

To confirm the substrate underneath it is actually there, ask the operator:

```bash
curl -sS -u admin:<pass> \
  "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status" \
  | jq .recordings_access
```

```json
{
  "applicable": true,
  "publish_sink": "nextcloud-files",
  "state": "provisioned",
  "ok": true,
  "admin_user": "admin",
  "mode": "access_controlled",
  "mode_source": "configured",
  "root": "Cassini/Recordings",
  "migration_clean": true,
  "prerequisites": [
    { "name": "groupfolders", "state": "enabled" },
    { "name": "group_everyone", "state": "enabled" }
  ],
  "checked_at": "2026-08-03T21:14:02Z"
}
```

`state: provisioned` means the mode in force has everything it needs — under
access control the Team folder, its ACL floor, the universal `everyone` mount
and the canonical collections; in the default mode the service account and its
own `CassiniNoACL/Recordings/meetings`, and nothing else. Anything else
(other than `not_applicable`) makes `/status` answer **503**, so a broken
substrate is visible rather than a silently empty archive.

`root` is where *this* mode keeps recordings, so a monitor does not have to know
which constant goes with which mode. `migration_clean: false` is deliberately
**not** a health failure and does not make `/status` answer 503: the archive is
complete at `root`, publishing and recording are unaffected, and the only
consequence is a stale copy at the other root that nothing reads. It is here so a
monitor notices; the settings section has the **Resume** button.

`state` distinguishes the two things an administrator does differently:
**`unavailable`** means a named thing is absent — install it, or set it —
while **`degraded`** means a call failed and there is nothing specific to
install. `step` is machine-readable, so a monitor or an install check can key on
it rather than matching prose.

```json
"step": "app_missing:group_everyone"   → install/enable the Everyone Group app
"step": "app_missing:groupfolders"     → install/enable the Team folders app
"step": "administrator"                → set CASSINI_NC_ADMIN_USER
"step": "owner_account"                → occ user:add --group=cassini cassini
"step": "universal_group"              → the Everyone Group app produced no group
"step": "group_folder"                 → occ groupfolders:create Cassini
"step": "mount_mapping:everyone"       → occ groupfolders:group <id> everyone read
"step": "mount_mapping:cassini"        → occ groupfolders:group <id> cassini read
                                         write share delete
"step": "group_folder_acl"             → occ groupfolders:permissions <id> --enable
"step": "group_folder_manager"         → occ groupfolders:permissions <id> -m --user cassini
"step": "mode_mismatch:default_root_shadowed"   // something IS mounted there
"step": "mode_mismatch:default_root_unknown"    // nobody could say (writes only)
                                       → a Team folder is mounted at CassiniNoACL,
                                         which is where recordings visible to
                                         anyone with an account are kept; unmap or
                                         rename it, or switch the audience to
                                         meeting participants in the settings
"step": "storage_mode_declared_conflict"
                                       → CASSINI_STORAGE_MODE named a model this
                                         instance does not match. Nothing was
                                         written down. Fix the stack, or remove the
                                         variable and let the enabled edge resolve
                                         the audience
"step": "acl_enable"                   → a call failed; read the nc storage:
                                         and nc provision: log lines
```

A **mounted `Cassini` Team folder is no longer a mismatch on its own.** It was in
the first pass, because the default mode wrote to the path that folder occupies.
It does not any more, and an emptied `Cassini` folder left mounted is exactly what
a completed opt-out looks like — so refusing there would make every opted-out
instance permanently unpublishable. The only mount that disqualifies the default
mode is one over `CassiniNoACL` itself, which nothing Cassini does can create.

Every `unavailable` step's `detail` carries the exact command, so nothing above
has to be looked up.

`applicable: false` (reported as `state: not_applicable`) means this deployment
does not serve recordings from Nextcloud Files — a standalone operator, or an
ExApp pinned to `CASSINI_PUBLISH_SINK=local` — so there is no substrate to
expect. `state: unknown` means no preflight has completed in this process yet.
A plain container restart no longer parks an install there: one that has already
recorded a mode re-proves it at startup (D-669), so the state settles a few
seconds after the container comes up. It persists only where there is nothing
recorded to re-prove — an install whose enabled edge has never got through,
including one held by the upgrade latch below — and there the fix is to disable
and re-enable Cassini. Publishing is refused while it stands, because nothing
has verified where recordings would land. **Reading is not affected in either
mode.** Under access
control Nextcloud is the one deciding, and in the default mode the recorded mode
is loaded from `storage_settings.json` at startup and the private root is a path
nothing Cassini does could have mounted anything over — so an unasked question is
not evidence of a hazard there, and a restarted container serves its archive
again instead of showing everyone an empty list until the next enable. The one
thing that does stop owner-identity reads is a probe that positively saw a Team
folder mounted at `CassiniNoACL`, which is the `mode_mismatch:default_root_shadowed`
case above.

The full state table and worked examples are in
[the install guide](./exapp-install.md#verifying-the-recordings-substrate).

You do not have to curl for it. Whenever the state is not `provisioned` or
`not_applicable`, opening **Cassini** tells an administrator in one sentence that
recordings cannot be saved right now, names the cause in words, and offers **Try
again** and **Show details** — the missing app and its `occ` lines where there is
one, the log to read where there is not, and the operator's own `detail`
sentence, all inside the details disclosure. Everyone else gets the same first
sentence and no buttons. For `unavailable` and `degraded` that message takes the
place of the meeting list; for `unknown` the list stays, because a restarted
container can still serve every recording it already has. See [What people see
when setup is not
finished](./exapp-install.md#what-people-see-when-setup-is-not-finished).

Provisioning does not block startup, but it does gate **delivery**. A
`nextcloud-files` publish writes the per-meeting ACL as part of publishing
(D-549), so a recording whose audience cannot be written is not published — and
since D-585 a publish is refused outright while the substrate is not
`provisioned`, rather than writing recordings somewhere the read path is not
looking. If publishes start failing on an instance where they did not before,
read `recordings_access` first.

## Day-to-day permission changes

Access is frozen at publish and remains editable in Nextcloud:

- Open Team folder → recording `.opus` → sharing panel → **Advanced
  permissions** to add/remove user, group, or team read rules.
- Keep the explicit `everyone` rule: deny means private; read means public.
- Removing participant rules leaves a private recording owner-only.
- **Edits survive a re-publish.** Re-delivering a meeting replaces its audio and
  leaves its rules exactly as they are, so a recording you widened or narrowed by
  hand stays that way. The one exception is a recording that never got an
  audience at all — where the first publish failed partway — which the next
  publish finishes.
- Guest/email/federated participants without a local account need a separate
  normal share or public link, outside Cassini's managed model.
- Non-Talk/dev recordings have no participant audience and remain owner-only.

## Validation scenarios

```text
  [ ] Private meeting with alice + bob:
      alice and bob list/play it; carol does not list it and direct playback 404s.

  [ ] Public meeting:
      an unrelated account lists/plays it.

  [ ] Create a new account after publishing the public meeting:
      its first Cassini visit lists and plays the meeting immediately.

  [ ] Inspect the fresh account before opening Cassini:
      `occ user:info` already reports `everyone`; there is no recording-viewers
      membership and no operator membership write.

  [ ] Switch to the default mode with recordings published:
      every meeting is listed and plays afterwards; Cassini/Recordings/meetings
      is empty, the `Cassini` Team folder is still in `occ groupfolders:list`,
      and /status reports root=CassiniNoACL/Recordings, migration_clean=true.

  [ ] Switch back:
      the same meetings are listed; each leaf under Cassini/Recordings/meetings
      carries an `everyone: read` rule, and CassiniNoACL/Recordings/meetings is
      empty.

  [ ] Kill the container mid-switch, then restart it:
      the mode on /status still names a root that lists every meeting, the
      settings section says "A switch didn't finish" and offers Resume, and
      pressing it changes nothing anyone can see.
```

If alice sees an EMPTY list, her per-caller scan cannot traverse the folder:
confirm with `occ groupfolders:list` that the folder has advanced ACL and the
`everyone: read` mount, and that `occ user:info alice` reports `everyone`.

The playback check is the security floor. Catalog scans fail closed to an empty
list rather than returning the unfiltered owner catalog — a mis-tuned recipe
degrades to "no meetings", never to "everyone's meetings".

## Switching modes on an instance that already has recordings

The settings section does it, in one synchronous request, and it is a **copy**: every
recording the destination does not already have is copied into the other mode's
root, the mode is flipped only once the copy has been verified, and the source is
emptied last. Nothing is moved and nothing is deleted before the destination
provably holds the archive.

Before it runs, the app asks the operator what the switch *would* do: how many
recordings would be copied, how many are already at the destination, whether the
source could be read at all. The confirmation states the outcome in three
sentences — what it means for new recordings, that the existing ones stay
visible to everyone who can see them today, and that recording pauses while the
switch runs — and the direction that widens the audience carries the number of
recordings in its own button. A source it could not read is reported as exactly
that, never as "there is nothing to move".

### The invariant, and the state machine that keeps it

> Whichever mode `access_control_enabled` names, **that** root holds a complete
> archive. `migration_clean: false` means the *other* root holds leftovers that
> nothing reads.

Everything below exists to keep that sentence true at every instant, including
the instant the container is killed. `storage_settings.json` carries three
fields — the mode, where the decision came from, and whether the last migration
finished tidying up:

```json
{
  "access_control_enabled": true,
  "source": "user",
  "migration_clean": true
}
```

`migration_clean` is a pointer in the operator, so **absent means clean**: every
file written before the field existed describes an install that is not
mid-migration, and reading those as dirty would offer every upgrading instance a
cleanup it does not need — one that DELETEs from a root, which is not a button to
arm on a guess.

```text
  state: mode = X, clean          src = root(X)          dst = root(Y)
     │
     │  PUT /storage {"access_control_enabled": Y}
     ▼
  0. sanity-check Y and inspect its archive.        nothing written yet
     Stop and list artefacts if Y is populated;      wait for explicit overwrite
     continue only after confirmation.
  ─────────────────────────────────────────────────────────────────────────
  1. WRITE {mode: X, clean: false}                 before any byte moves
  2. MKCOL the destination tree                    into the Team folder: the
                                                   owner-only floor on the mount
                                                   FIRST, so nothing is reachable
                                                   through the broad container
                                                   grant before it states its own
                                                   rules
  3. CLEAR the confirmed destination, then COPY     Overwrite: F, always
     every src/meetings/*                          into the Team folder: PROPPATCH
                                                   the public rule set on the
                                                   DESTINATION leaf afterwards
                                                   out of it: nothing (see below)
  4. REPLACE dst/catalog.json with src/catalog.json
  5. WIDEN the container ACL (opt-in only),        the verification is what
     then VERIFY every source recording is         licenses step 6
     at the destination
  6. WRITE {mode: Y, clean: false}                 THE FLIP. One write.
  7. DELETE the contents of src                    the collections themselves stay
  8. WRITE {mode: Y, clean: true}                  settled
     ▼
  state: mode = Y, clean
```

Steps 1, 6 and 8 are the only places the recorded mode or the clean flag change,
and each is one atomic file rewrite. Steps 1 and 6 are the two a crash can be
caught between; step 8 only records that there is nothing left to tidy, and the
recovery below writes exactly the same thing when it did not run. Step 1 is
deliberately *before* the first MKCOL rather than after it: killed a moment
earlier and nothing has happened at all; killed a moment later and the leftovers
are already claimed by the recovery.

The switch, the preview, the recovery and the enabled-edge preflight all take the
same lock, so none of them can interleave, and the switch re-runs the preflight
inside its own critical section — `/status`, the settings section and the publish gate
describe the archive as it is *now* rather than as it was before. What the lock
does not cover is a publish that slipped in before it was taken, which is what
step 5 is for: it lists both trees again and refuses to flip if the source grew.

**The opt-out writes no rules, by construction.** A copy into the service
account's own home gets a new file id outside any group folder, and Group Folders
keys its rules by file id — so the copy has no rules and there is nothing to
clear. Writing one would fail anyway: `nc:acl-list` is not settable outside a Team
folder (a 500 with `groupfolders` installed, a *false* 207 without it — measured).

**`Overwrite` is never `T`.** For a file it destroys the destination's id and with
it every ACL row keyed to that id; for a *directory* the server deletes the whole
destination tree first. Cassini instead lists the destination before it writes,
requires confirmation if it finds artefacts, clears that confirmed archive, then
copies each source entry with `Overwrite: F` as a final guard.

**Migrated recordings are public.** Opting in leaves every copied recording
readable by every account, deliberately: the room's attendee list today is not
evidence of who was in a call last quarter, and the archive carries nothing
better. Narrow the ones that matter from Files → Advanced permissions.

### Where the archive is if the switch dies

| Died after | Recorded state | The complete archive is at | Left over | What to do |
|---|---|---|---|---|
| 1 | X, dirty | `root(X)` | nothing | Finish the switch (a no-op clear), then ask for the switch again |
| 2–5 | X, dirty | `root(X)` — only ever read | a partial copy at `root(Y)` | Finish the switch, which discards it; then ask for the switch again |
| 6, but the file could not be written | X, dirty | both — `root(Y)` was verified complete and `root(X)` was only read | the copy at `root(Y)` | The switch answers with an error naming `root(Y)`. Make the volume writable and ask for the switch again; finishing instead is safe too, and simply discards the copy |
| 6 | Y, dirty | `root(Y)` — verified at step 5 | the whole of `root(X)` | Finish the switch, which clears it |
| 7 | Y, dirty | `root(Y)` | whatever step 7 had not deleted | Finish the switch |
| 8 | Y, dirty | `root(Y)` | nothing | Finish the switch (it only rewrites the flag) |

In every row the recorded mode names a root holding a complete archive, and in
every row the repair is **the same single action**. That is the point of the
copy-then-flip ordering: there is no branch on which half failed, which is what
makes it safe to offer as a button.

One thing the table does not repair by itself: an opt-in that died between step 2
and step 5 leaves the `Cassini` container narrowed to owner-only, so nothing in
the Team folder is traversable by anybody else until the widen at step 5 runs.
Asking for the opt-in again re-applies both ends of that, and so does re-enabling
Cassini while access control is in force.

### Finishing an interrupted switch

Three doors, one action behind them:

```text
  Settings section         POST /storage           PUT /storage
  "A switch didn't         {"action":              {"access_control_enabled":
   finish." · Resume        "finish_migration"}     <the mode already in force>}
        │                        │                          │
        └────────────────────────┴──────────────────────────┘
                                 ▼
                    verify every recording at the stale root
                    is also at the active one
                                 │
                 ┌───────────────┴───────────────┐
                 ▼                               ▼
              REFUSE                     clear the stale root's
    "those recordings are not in         contents, WRITE clean: true
     <active root>, so removing
     them would lose them"
```

The stale root is simply the one the recorded mode does **not** name — no
discovery, no regex, no branch. The settings section says where the recordings
*are* before it offers to clear anything, because this is a tidy-up and the
alarming reading is data loss.

The `PUT` door matters because it is the one the UI's own mode buttons use. It
used to short-circuit whenever the requested mode already equalled the recorded
one, which is exactly the state a switch that died after the flip leaves behind —
so the one action that would repair it was unreachable from the app. Now a
request for the mode already in force is a no-op only when the instance is
settled; when it is dirty it performs the repair. A request for the *other* mode
finishes the dirty state first, so a copy never inherits somebody else's
leftovers at its destination.

**What it refuses.** The recovery deletes, so it verifies first: every recording
at the stale root must already be at the active one. The invariant says that is
true in every failed-migration case, and proving it rather than trusting it costs
one PROPFIND pair — but it closes the one case where the invariant does *not*
hold, a pre-split archive whose adoption has not finished carrying it across,
where the active root is genuinely the partial one. There the honest answer is to
refuse and name both roots. It is also a no-op on an install that is already
clean, including every install that predates the flag.

### Why the roots are separate

The collision below was measured against a live Nextcloud 34 + Group Folders 22
rather than inferred, and it is still true. It no longer *governs* anything,
because nothing Cassini writes can collide any more — but it is why an upgrade
may find a `Cassini (N)` tree, and it is why the default mode refuses to run with
a Team folder mounted at `CassiniNoACL`.

**A mounted Team folder wins the path.** Creating a `Cassini` Team folder while
the service account already has a `Cassini` home directory does not remap the
mount — the mount takes `Cassini` and the server **renames the home directory**,
physically, to a name of its own choosing (`Cassini (1)` in the bench; treat the
suffix as the server's, not as a contract). There is no wrong-tree-write window:
DAV mounts are set up per request, so the first request after the folder exists
already sees both.

D-660 read that as benign and kept one path constant for both modes. It was right
about writes and wrong about everything that has to reason about where the archive
is: two modes that can never occupy their path at the same time make every
operation spanning them a recovery exercise. The copy above, the preview that can
count, and an opt-out that needs no privileged call are all things the split
bought.

```text
  WHAT THE SPLIT RETIRED                       WHY IT EXISTED
  ──────────────────────                       ──────────────
  MOVE, not copy                               the source path was about to
                                               become the destination path
  find the archive by regex (`Cassini (N)`)    the server had renamed it
  a `Cassini-optout` staging tree              the archive needed somewhere to
                                               sit while the mount came down
  unmapping the Team folder's groups           the canonical path had to resolve
                                               to the home directory again
  "a mounted Cassini folder" = mode_mismatch   default-mode writes would have
                                               landed in the shared folder
```

The unmapping is the one worth naming twice. `DELETE
/index.php/apps/groupfolders/folders/{id}/groups/{group}` carries
`#[PasswordConfirmationRequired]`, and an AppAPI act-as request has a PHP session
but no login token — so it 403s on Nextcloud 33.0.6–33.0.7 and 34.0.1+ (the
harness pins 34.0.0, the one release whose middleware returns instead of throwing,
which is why CI was blind to it). The fix was not to route that call through the
browser. It was that **nothing needs to unmap anything any more**: the mount only
had to come down so `Cassini/Recordings` would resolve to the home directory, and
the default mode does not want that path. The transition now has zero
password-confirmation-guarded calls in either direction.

So **the opt-out leaves the `Cassini` Team folder mounted and empty.** Deleting it
is irreversible and guarded identically; leaving it means opting back in later is
immediate, and an empty mounted folder is not a hazard — the default mode reads
somewhere else. The confirmation copy says so before you press the button.

### Upgrading an install from before the roots were split

An install created by the first pass keeps its default-mode archive at
`Cassini/Recordings` — the path the Team folder also wants — or at whatever
`Cassini (N)` the server renamed that tree to, or under a `Cassini-optout` staging
tree if a first-pass opt-out never finished. Splitting the roots would stand those
recordings up somewhere nothing reads, so the enabled edge carries them across,
once, in default mode only:

```text
  default mode, enabled edge
      │
      ├─ nothing mounted at `Cassini`, and Cassini/Recordings holds meetings
      │        └─▶ adopt it
      ├─ Cassini-optout/Recordings holds meetings       ─▶ adopt it
      ├─ Cassini (N)/Recordings holds meetings          ─▶ adopt it
      └─ none of the above                              ─▶ nothing to do
                     │
                     ▼
        if CassiniNoACL/Recordings is empty, copy into it,
        verify, then empty the source
```

The staging name is looked at before the renamed tree because it is *ours*, so
finding it is unambiguous evidence about which transition left it. Automatic
adoption runs only into an empty `CassiniNoACL/Recordings`; if it finds files
there, it stops and logs their names rather than overwriting them while Cassini
is being enabled.

It never adopts from a **mounted** Team folder. That is not a stranded default
archive; it is the access-controlled mode, and copying it into a private home tree
would be a silent mode change. An access-controlled install therefore moves
nothing on upgrade: its root did not change.

The adoption deliberately does *not* set `migration_clean`, and that is a safety
property rather than a shortcut. A mode switch can flip which root is
authoritative; an adoption cannot — the default mode already reads
`CassiniNoACL/Recordings`, so *during* an adoption the active root is the
incomplete one, and marking the instance dirty would arm the recovery against the
very tree still holding the recordings. Instead the source is the state: the
source is emptied only once the copy is verified. A later enabled edge can retry
an interrupted adoption only while its destination remains empty; it refuses to
overwrite any artefacts it finds there.

**What an upgrade meets.** An install that upgrades into this build has nothing
recorded, so the enabled edge resolves an audience from the archive it finds, and
records it. Nothing is asked and nothing is refused.

```text
  nothing recorded, nothing declared
                              │
                              ▼
      recordings in Cassini/Recordings?
              │                        │
             yes                       no
              ▼                        ▼
      MEETING PARTICIPANTS      ANYONE WITH A NEXTCLOUD ACCOUNT
      adopted, nothing moves    recorded, and an empty archive
      (an archive in the        starts open
       other root is reported
       as stranded)
```

An earlier build fell back to `default` here whatever it found, and caught the
obvious mistake with a latch — `mode_mismatch:access_controlled_archive`, which
fired when the `Cassini` Team folder was mounted and still held recordings. Both
are gone, and a monitor keyed on that step will now see nothing. The latch caught
one shape; reading the archive itself covers every shape, and it never widens an
archive that already exists.

Picking **Meeting participants** in the settings section is a switch like any
other: it checks the destination first. If the private archive is not empty, the
operator refuses until the request comes back with `confirm_overwrite: true`,
then replaces that archive, flips the mode, and records it.

A **recorded** or **declared** `default` on an instance whose Team folder still
holds recordings is an administrator's decision plus a tidy-up, not a
misconfiguration: `/storage` reports it as `stranded_root` /
`stranded_recordings`, Cassini says how many there are and offers to move them
in, and nothing is refused. Switching to the mode that holds them makes them the
active archive; switching back afterwards carries them into the other root. A mounted but
**empty** `Cassini` folder is never stranded — that is exactly what a completed
opt-out leaves behind.

### Upgrading from a pre-Nextcloud-Files install

An archive published by a version that kept recordings on the app's own volume
is not migrated by any of the above. Cassini used to converge it on every
enabled edge by re-uploading all of it; that was unbounded work under a fixed
deadline and it failed by silently freezing the recording list, so it was
removed (D-613). Run the migration explicitly instead, once:

```bash
./scripts/backfill-nc-files.sh --dry-run   # check first
./scripts/backfill-nc-files.sh
```

It applies only where recordings are visible to **meeting participants**, and
writes into `Cassini/Recordings/` — the Team folder's root. On an instance whose
recordings are visible to anyone with a Nextcloud account it reads nothing and
writes nothing, and says so: switch the audience in the settings section instead,
which is what relocates an archive that is already published.
It also changes nothing when `Cassini/Recordings/` already holds recordings, or
when there is no older archive on the volume, so it is safe to run when you are
unsure whether it is needed. Migrated recordings are readable only by the
`cassini` service account — grant access from the Files UI, or pass `--public` to
restore the org-wide readability a pre-access-control archive had. See
[`docs/exapp-install.md`](exapp-install.md) for the full procedure.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `/operator/status` answers 503 | The storage the selected mode needs is not ready | Read `recordings_access.state` and `.step`. `unavailable` names a thing to install or set and carries the command in `.detail`; `degraded` means a call failed — the matching `nc storage:` or `nc provision:` log line has it |
| `state=unavailable`, `step=app_missing:<id>` | That native app is not enabled | `occ app:install <id> && occ app:enable <id>`, then re-enable Cassini |
| `state=unavailable`, `step=administrator` | No probed account is an administrator Cassini may act as | Set `CASSINI_NC_ADMIN_USER` to one, then re-enable Cassini |
| `state=unknown` | No preflight has completed in this process. An install with a recorded mode re-proves it at startup (D-669), so a restart clears this by itself within seconds; it sticks only when nothing has ever been recorded | Look again after a few seconds. If it stays, disable and re-enable Cassini |
| Publishes fail with "the recordings storage is not ready" | Deliberate: writing somewhere the read path is not looking would leave recordings nobody can open | Fix the state above, or set `CASSINI_PUBLISH_SINK=local` to keep recordings on the app's own volume |
| A recording is refused in Talk, with "The recording failed" | A prerequisite of the audience in force is missing, so the call would be captured and then not publishable | Open Cassini as an administrator; the notice names the cause, and **Show details** carries the missing thing and its command |
| `state=unavailable`, `step=owner_account` | The `cassini` service account does not exist | `occ group:add cassini` and `occ user:add --group=cassini cassini`, then re-enable Cassini |
| `state=unavailable`, `step=mode_mismatch:default_root_unknown` | Nobody could say whether anything is mounted at `CassiniNoACL`. Publishing is refused, because the default mode's safety argument is that this tree is private and nothing confirmed it. **Reading is unaffected** — the archive still lists — which is the deliberate asymmetry: an unconfirmed write puts recordings somewhere they may not belong, an unconfirmed read serves a tree that in this mode is open by design | Check that Nextcloud is answering (this usually means the apps list or the Team-folder list failed), then disable and re-enable Cassini |
| `state=unavailable`, `step=mode_mismatch:default_root_shadowed` | A Team folder is mounted at `CassiniNoACL`, which is where the default mode keeps recordings. A mounted folder wins that path, so writes would land in a shared folder and owner-identity reads would serve it to everyone mapped there | `occ groupfolders:list`, then `occ groupfolders:group <id> <group> --delete` to unmap it (or rename the folder) — or switch the audience to meeting participants in **Operator › Settings › Who can see recordings** if that is what this instance was meant to be |
| `state=unavailable`, `step=storage_mode_declared_conflict` | `CASSINI_STORAGE_MODE` named a model this instance does not match: recordings in both folders, the same recording in both, or a folder that could not be read. Nothing was written down | Fix the stack, or remove the variable and let the enabled edge resolve the audience. It is a development and CI option |
| The settings section says "A switch didn't finish" | `migration_clean` is false: a switch stopped between copying and tidying up. The archive is complete at the audience shown; the other root holds a copy nothing reads | Press **Resume** (or `POST /operator/storage {"action":"finish_migration"}`). It refuses only if the leftovers are not also at the active root |
| Finishing the switch refuses with "those recordings are not in …" | The active root does not hold everything the stale one does — an adoption of a pre-split archive that has not finished carrying it across | Re-enable Cassini (or switch modes) to finish the copy first; nothing was deleted |
| Cassini says N recordings aren't shown yet | The instance is settled, but the root the audience in force does *not* name still has an archive. Publishing and reading work; those recordings are simply not read | Press **Move them in**, which copies them across; or leave them, they are not at risk |
| Log says required universal group `everyone` is unavailable | Everyone Group app missing/disabled | Install/enable `group_everyone`, then re-enable Cassini |
| The probe cannot see an account or folder you created | Administrator discovery picked the wrong account, so the admin-gated reads answer 401/403 | Inspect `nc storage:` logs; set `CASSINI_NC_ADMIN_USER` to the correct administrator and re-enable Cassini |
| `step=group_folder` | There is no `Cassini` Team folder. The operator's own backend cannot create one — its requests carry no login token | Pick **Meeting participants** in **Operator › Settings › Who can see recordings**, which creates it and maps it **as you**, from your browser; or run `occ groupfolders:create Cassini`, map its groups and enable its ACL (see Setup), then re-enable Cassini |
| Fresh user has no Cassini mount | `group_everyone` disabled or Team-folder mapping missing | Confirm `occ user:info <user>` includes `everyone` and `groupfolders:list` assigns `everyone:1` |
| Root becomes owner-only after upgrade, or after a failed opt-in | Legacy leaf/catalog ACL migration failed, or an opt-in stopped between narrowing the container to owner-only and widening it again | Inspect the `nc provision:` / `nc storage:` logs, correct the DAV/ACL error, then re-enable Cassini or ask for the switch again — either re-applies both ends |
| Everyone sees a private meeting | Leaf lacks an explicit `everyone` deny or ACLs were disabled | Re-enable Cassini to run protection; inspect the leaf's Advanced permissions |
| Granted user sees an empty list | Caller cannot traverse or leaf ACL was not applied | Confirm advanced ACL, `everyone:1` mount, root read, and the participant allow |
| Meeting visible to nobody | Non-Talk job or private room with only non-local participants | Share the `.opus` manually |
| Viewer returns 502 | Nextcloud Files is unreachable | Check ExApp-to-Nextcloud DAV connectivity and owner identity |
| Cassini says it can't save recordings right now, instead of listing meetings | `state` is `unavailable` or `degraded`; the app says so in the UI rather than failing the archive fetch | Open Cassini **as an administrator** — the same page names the cause in words, and **Show details** carries the step and the commands. See [What people see when setup is not finished](./exapp-install.md#what-people-see-when-setup-is-not-finished) |
| Cassini shows "Cassini has not finished setting itself up" above a working meeting list | `state` is `unknown` — no preflight has completed in this process. Reads are fine; publishing is refused | Reload after a few seconds: a restart re-proves a recorded mode on its own (D-669). If it persists, nothing has ever been recorded — disable and re-enable Cassini |

## Related

- **D-530** — the design spike this implements (the proposal is not tracked in
  this repo).
- **D-532** — dedicated `cassini` service account and owner group.
- **D-552** — the virtual `everyone` group and public-conversation access.
- **D-554** — made this the only mode and added the `/status` substrate report.
- **D-529** — the underlying Nextcloud Files delivery this builds on.
- **D-552** — authenticated account-wide access for public Talk recordings.
- **D-616** — the storage-mode opt-in: two modes, the settings file, and its
  followups — one root per mode, the copy-based switch, and the recovery.
- **D-751** — the audience resolved when the app is enabled, the one-time
  dialog, and **Operator › Settings › Who can see recordings** in place of the
  Setup tab.
- **D-660** — the bench that measured the mount/home collision quoted above.
