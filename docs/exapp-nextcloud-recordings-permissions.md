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

Access control is one of **two** storage modes, chosen in the Cassini app's
**Setup** tab. The rest of this document describes the access-controlled one.

| | Default | Access controlled |
|---|---|---|
| Where recordings live | the `cassini` account's own `Cassini/Recordings` | the `Cassini` Team folder |
| Who can read one | everyone who can open the Cassini app | the people who were in the meeting |
| Nextcloud apps needed | none | Team folders + Everyone Group |
| Other prerequisites | a `cassini` service account | a `cassini` service account, an `everyone` group, and a mapped, ACL-enabled `Cassini` Team folder |

```text
                        storage_settings.json
                   {"access_control_enabled": …}
                                │
          ┌─────────────────────┴─────────────────────┐
          ▼                                           ▼
      DEFAULT                                  ACCESS CONTROLLED
  write: PUT, no ACLs                     write: reserve → deny → PUT → audience
  read:  as `cassini`, whole archive      read:  as the CALLER, Nextcloud decides
  needs: the service account              needs: both apps + the Team folder
```

**Which one an existing install gets.** The flag is absent until something
records it. On the first enable after upgrading, Cassini derives it once from
the instance — and derives `access controlled` only when the *complete*
substrate is already present, which every install built by earlier versions has.
An install already restricting recordings therefore keeps doing so. The derived
value is written to `storage_settings.json` immediately and never derived again.

**Switching.** The Setup tab moves the archive both ways. Opting in moves every
existing recording into the Team folder and leaves it **readable by every
account** — Cassini does not guess who was in a past meeting — so narrow the
ones that matter afterwards from Files → Advanced permissions. Opting out moves
everything back into the service account's own home and drops all access rules:
after it, everyone who can open Cassini can read everything.

> **Cassini can set most of this up for you (D-671).** The Setup tab lists what
> is missing and offers to make it: the `cassini` group and account, the Team
> folder, its group mappings, advanced ACL, and the ACL-manager delegation. It
> performs them **as you**, from your browser, after Nextcloud's own password
> dialog — Cassini never sees, holds or transmits your password.
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
> whether there is a Team folder — and the group-mapping changes a mode switch
> makes are performed as a separately resolved administrator.
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

The recordings live in a system-owned **Team folder** with advanced ACLs and a
default-deny floor. This gives every user the same `Cassini/Recordings` path
while allowing Nextcloud to hide individual leaves from non-participants.

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
- Cassini deployed through AppAPI. There is nothing else to switch on:
  access control is unconditional for an installed ExApp (D-554).

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

Open **Cassini → Setup** and press *Set up access controlled*. Cassini lists
every change first, asks Nextcloud to confirm your password, and then makes them
as you. It cannot install the two apps — see below — and will say so.

The same recipe by hand, in the order the Setup tab lists whatever is missing:

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

The Setup tab prints only the lines this instance still needs, so it is usually
shorter than the block above.

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
not on start), and switch the mode in the Setup tab if it is not already on.

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

In the **default** mode this whole sequence collapses to one thing: MKCOL
`Cassini/Recordings/meetings` in the service account's own home. There is no
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
# acl: true, acl_default_no_permission: true
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

Open **Cassini → Setup**. The mode in force carries a "Current" badge; the other
one is either offered as a switch or shows what it is still missing.

Over HTTP, `GET /operator/storage` answers the same question with the mode, its
source (`configured` when it was recorded, `derived` when Cassini worked it out
from the instance and wrote it), and per-mode availability. To confirm the
substrate underneath it is actually there, ask the operator:

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
  "prerequisites": [
    { "name": "groupfolders", "state": "enabled" },
    { "name": "group_everyone", "state": "enabled" }
  ],
  "checked_at": "2026-08-03T21:14:02Z"
}
```

`state: provisioned` means the Team folder, its ACL floor, the universal
`everyone` mount and the canonical collections are all in place. Anything else
(other than `not_applicable`) makes `/status` answer **503**, so a broken
substrate is visible rather than a silently empty archive.

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
"step": "group_folder_acl"             → occ groupfolders:permissions <id> --enable
"step": "group_folder_manager"         → occ groupfolders:permissions <id> -m --user cassini
"step": "mode_mismatch:group_folder_mount"
                                       → access control is off but a Cassini Team
                                         folder is still mapped; pick a mode in
                                         the Setup tab
"step": "mode_mismatch:group_folder_unknown"
                                       → Cassini could not find out whether one is
                                         mounted, and will not assume there is not
"step": "acl_enable"                   → a call failed; read the nc storage:
                                         and nc provision: log lines
```

Every `unavailable` step's `detail` carries the exact command, so nothing above
has to be looked up.

`applicable: false` (reported as `state: not_applicable`) means this deployment
does not serve recordings from Nextcloud Files — a standalone operator, or an
ExApp pinned to `CASSINI_PUBLISH_SINK=local` — so there is no substrate to
expect. `state: unknown` means the container was restarted without the app being
re-enabled, so the preflight has not run in this process. Disable and re-enable
Cassini — publishing is refused until then, because nothing has verified where
recordings would land, and in the **default** mode reading is too: serving the
whole archive as its owner is only safe once something has confirmed that no
Team folder is mounted over the canonical path. Under access control reads are
unaffected, because Nextcloud is the one deciding.

The full state table and worked examples are in
[the install guide](./exapp-install.md#verifying-the-recordings-substrate).

You do not have to curl for it. Whenever the state is not `provisioned` or
`not_applicable`, opening **Cassini** shows an administrator what stopped — the
missing app and its `occ` lines where there is one, the log to read where there
is not — along with the operator's own `detail` sentence, and shows everyone
else that Cassini is not set up plus a link to hand to an administrator. For
`unavailable` and `degraded` that message takes the place of the meeting list;
for `unknown` the list stays, because a restarted container can still serve
every recording it already has. See [What people see when setup is not
finished](./exapp-install.md#what-people-see-when-setup-is-not-finished).

Provisioning does not block startup, but it does gate **delivery**. A
`nextcloud-files` publish writes the per-meeting ACL as part of publishing
(D-549), so a recording whose audience cannot be written is not published — and
since D-585 a publish is refused outright while the substrate is not
`provisioned`, rather than writing recordings into the `cassini` account's
private home where nobody can reach them. If publishes start failing on an
instance where they did not before, read `recordings_access` first.

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
```

If alice sees an EMPTY list, her per-caller scan cannot traverse the folder:
confirm with `occ groupfolders:list` that the folder has advanced ACL and the
`everyone: read` mount, and that `occ user:info alice` reports `everyone`.

The playback check is the security floor. Catalog scans fail closed to an empty
list rather than returning the unfiltered owner catalog — a mis-tuned recipe
degrades to "no meetings", never to "everyone's meetings".

## Switching modes on an instance that already has recordings

The Setup tab does the move. What it has to work around is a collision, and the
mechanics below were measured against a live Nextcloud 34 + Group Folders 22
rather than inferred.

**A mounted Team folder wins the canonical path.** Creating a `Cassini` Team
folder while the service account already has a `Cassini` home directory does not
remap the mount — the mount takes `Cassini` and the server **renames the home
directory**, physically, to a name of its own choosing (`Cassini (1)` in the
bench; treat the suffix as the server's, not as a contract). There is no
wrong-tree-write window: DAV mounts are set up per request, so the first request
after the folder exists already sees both.

So the opt-in does not need you to move anything out of the way first — earlier
revisions of this document said it did, and had the direction of the rename
backwards. It needs to *find* the renamed tree, which it does by listing the
service account's root rather than by assuming a suffix.

```text
  OPT IN                                      OPT OUT
  ──────                                      ───────
  find the stranded `Cassini (N)`/Recordings  make `Cassini-optout`/Recordings
  MOVE each leaf into the Team folder         PROPPATCH each leaf public
    (Overwrite: F, never T)                   MOVE each leaf out of the folder
  PROPPATCH each leaf `everyone: READ`        verify the folder is empty
  merge the two catalogs                      unmap its `everyone`/`cassini` groups
  widen the root                              MOVE each leaf back to `Cassini`
  delete the emptied source                   delete the emptied staging tree
```

Two properties are load-bearing:

- **`Overwrite` is never `T`.** For a file it destroys the destination's id and
  with it every ACL row keyed to that id; for a *directory* the server deletes
  the entire destination tree first. A collision fails as one recording (412)
  instead of as the archive.
- **The opt-out empties the folder before unmapping it.** Unmapping removes the
  mount from every account including the one doing the moving, so a failure
  after that point would leave the archive unreachable rather than merely
  unmoved. If anything is still in the folder, the switch stops and says so.

**Migrated recordings are public.** Opting in leaves every moved recording
readable by every account, deliberately: the room's attendee list today is not
evidence of who was in a call last quarter, and the archive carries nothing
better. Narrow the ones that matter from Files → Advanced permissions.

If a switch fails part-way, nothing is lost — the recordings are in the source
or the staging tree named in the error, and re-running the switch resumes.

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

It changes nothing when `Cassini/Recordings/` already holds recordings, or when
there is no older archive on the volume, so it is safe to run when you are
unsure whether it is needed. Under access control, migrated recordings are
readable only by the `cassini` service account — grant access from the Files UI,
or pass `--public` to restore the org-wide readability a pre-access-control
archive had. See [`docs/exapp-install.md`](exapp-install.md) for the full
procedure.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `/operator/status` answers 503 | The storage the selected mode needs is not ready | Read `recordings_access.state` and `.step`. `unavailable` names a thing to install or set and carries the command in `.detail`; `degraded` means a call failed — the matching `nc storage:` or `nc provision:` log line has it |
| `state=unavailable`, `step=app_missing:<id>` | That native app is not enabled | `occ app:install <id> && occ app:enable <id>`, then re-enable Cassini |
| `state=unavailable`, `step=administrator` | No probed account is an administrator Cassini may act as | Set `CASSINI_NC_ADMIN_USER` to one, then re-enable Cassini |
| `state=unknown` | The container restarted without the app being re-enabled, so the preflight has not run in this process (D-541) | Disable and re-enable Cassini |
| Publishes fail with "the recordings storage is not ready" | Deliberate: writing somewhere the read path is not looking would leave recordings nobody can open | Fix the state above, or set `CASSINI_PUBLISH_SINK=local` to keep recordings on the app's own volume |
| A recording is refused in Talk, with "The recording failed" | A prerequisite of the selected mode is missing, so the call would be captured and then not publishable | Open Cassini → Setup; the missing thing and its command are there |
| `state=unavailable`, `step=owner_account` | The `cassini` service account does not exist | `occ group:add cassini` and `occ user:add --group=cassini cassini`, then re-enable Cassini |
| `state=unavailable`, `step=mode_mismatch:…` | The recorded storage mode and the instance disagree — e.g. access control is off but a `Cassini` Team folder is still mapped | Pick a mode in Cassini → Setup, which moves the archive to match it |
| Log says required universal group `everyone` is unavailable | Everyone Group app missing/disabled | Install/enable `group_everyone`, then re-enable Cassini |
| The probe cannot see an account or folder you created | Administrator discovery picked the wrong account, so the admin-gated reads answer 401/403 | Inspect `nc storage:` logs; set `CASSINI_NC_ADMIN_USER` to the correct administrator and re-enable Cassini |
| `step=group_folder` | There is no `Cassini` Team folder; Cassini does not create one | `occ groupfolders:create Cassini`, map its groups and enable its ACL (see Setup), then re-enable Cassini |
| Fresh user has no Cassini mount | `group_everyone` disabled or Team-folder mapping missing | Confirm `occ user:info <user>` includes `everyone` and `groupfolders:list` assigns `everyone:1` |
| Root becomes owner-only after upgrade | Legacy leaf/catalog ACL migration failed | Inspect the `nc provision:` logs, correct the DAV/ACL error, then re-enable Cassini |
| Everyone sees a private meeting | Leaf lacks an explicit `everyone` deny or ACLs were disabled | Re-enable Cassini to run protection; inspect the leaf's Advanced permissions |
| Granted user sees an empty list | Caller cannot traverse or leaf ACL was not applied | Confirm advanced ACL, `everyone:1` mount, root read, and the participant allow |
| Meeting visible to nobody | Non-Talk job or private room with only non-local participants | Share the `.opus` manually |
| Viewer returns 502 | Nextcloud Files is unreachable | Check ExApp-to-Nextcloud DAV connectivity and owner identity |
| Cassini shows "Cassini is not set up yet" instead of the meeting list | `state` is `unavailable` or `degraded`; the app now says so in the UI rather than failing the archive fetch | Open Cassini **as an administrator** — the same page names what stopped and the commands. See [What people see when setup is not finished](./exapp-install.md#what-people-see-when-setup-is-not-finished) |
| Cassini shows "Cassini has not finished setting itself up" above a working meeting list | `state` is `unknown` — the container restarted without the app being re-enabled. Reads are fine; publishing is refused | Disable and re-enable Cassini |

## Related

- **D-530** — the design spike this implements (the proposal is not tracked in
  this repo).
- **D-532** — dedicated `cassini` service account and owner group.
- **D-552** — the virtual `everyone` group and public-conversation access.
- **D-554** — made this the only mode and added the `/status` substrate report.
- **D-529** — the underlying Nextcloud Files delivery this builds on.
- **D-552** — authenticated account-wide access for public Talk recordings.
