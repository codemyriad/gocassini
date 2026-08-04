# Managing recording permissions in Nextcloud Files

How **per-participant access control** for Cassini recordings works and how to
operate it. A private recording is visible only to the people who had access to
the Talk room when it was published — that is everyone on the room's attendee
list, including users who were invited but never joined the call, and not only
those present during it. A recording of a public Talk conversation is visible to
every authenticated Nextcloud account. Nextcloud's Team-folder ACLs are the
authority, and administrators can edit them with the normal Files
advanced-permissions UI.

This is the only mode. There is no setting that turns it off and no public
archive to fall back to: an installed Cassini ExApp serves each caller only the
recordings Nextcloud says they may read (D-554).

> **Setup is automatic after two native prerequisites are enabled.** Cassini
> creates the canonical Team folder and its permissions over HTTP; there is no
> `occ groupfolders:*` recipe. An ExApp cannot install PHP apps, so an
> administrator must first enable **Group folders / Team folders** and
> **Everyone Group** (`group_everyone`). The local harness installs both.

> **Who owns the recordings.** The canonical tree is owned by a dedicated
> `cassini` service account, created automatically when the app is enabled. It
> is the only identity that writes recordings and manages their permissions.
> Privileged setup—creating the account, its narrow owner group, and the Team
> folder—is performed as a separately resolved administrator.
>
> That administrator is *probed*, not looked up: an external app cannot be told
> who it is, and every API that would reveal one is admin-gated. Cassini tries
> `CASSINI_NC_ADMIN_USER`, then `admin`, then the instance's own account list
> (up to a bounded number), and uses the first that turns out to be an
> administrator. If none is, provisioning stops and says so rather than acting
> as an account that may not exist. Set `CASSINI_NC_ADMIN_USER` then — see
> [the install guide](./exapp-install.md#administrator-discovery).
>
> The recordings live in shared Team-folder storage rather than a user's home.
> Changing the acting owner from the earlier administrator identity therefore
> requires no data migration: existing Team-folder recordings remain in place.

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

## Automatic setup

On the AppAPI **enabled** edge, Cassini provisions this topology idempotently
and unconditionally — there is no mode in which only part of it runs:

```text
  ├── resolve the provisioning administrator              (OCS API)
  ├── ensure owner group + `cassini` service account       (OCS API)
  ├── verify virtual group `everyone` exists               (OCS API)
  ├── ensure Team folder `Cassini`, default-deny           (Team folders API)
  ├── assign everyone READ + owner group ALL              (Team folders API)
  ├── enable advanced ACL + delegate owner ACL manager    (Team folders API)
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

Nothing turns it on. Whenever the operator runs as an installed ExApp, its
recordings are access-controlled and the Team folder is provisioned on the
enabled edge; the only thing an admin does is enable the two native apps (see
[Prerequisites](#prerequisites)).

To confirm an instance is actually set up, ask the operator:

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
"step": "acl_enable"                   → a call failed; read the nc provision: log
```

`applicable: false` (reported as `state: not_applicable`) means this deployment
does not serve recordings from Nextcloud Files — a standalone operator, or an
ExApp pinned to `CASSINI_PUBLISH_SINK=local` — so there is no substrate to
expect. `state: unknown` means the container was restarted without the app being
re-enabled, so setup has not run in this process; disable and re-enable Cassini.

The full state table and worked examples are in
[the install guide](./exapp-install.md#verifying-the-recordings-substrate).

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

## Upgrading from a pre-access-control install

If a deployment ran before access control was unconditional — when the operator
served the D-529 public archive — its recordings live in the acting owner's own
`Cassini/Recordings` **home** folder. Provisioning creates a *Team folder*
mounted at `Cassini`, and Nextcloud will not mount one over an existing
same-named home folder: it remaps the mount (e.g. `Cassini (2)`), so the operator
writes to a different path and the previously delivered recordings are left
behind in the old one.

Before upgrading such an instance, move or remove the owner's existing `Cassini`
home folder (back it up first) so the Team folder can mount at the canonical
`Cassini` path, then re-enable Cassini; the startup sync re-delivers the archive
into the Team folder. Which account holds that stale home folder depends on the
version you are coming from — `admin` before D-532, `cassini` after it.

There is no way back. `CASSINI_NC_ACCESS_CONTROL` was removed in D-554 and the
public read path with it, so an instance that upgrades is access-controlled.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| `/operator/status` answers 503 | Provisioning did not complete | Read `recordings_access.state` and `.step`. `unavailable` names a thing to install or set; `degraded` means a call failed — the matching `nc provision:` log line has the detail |
| `state=unavailable`, `step=app_missing:<id>` | That native app is not enabled | `occ app:install <id> && occ app:enable <id>`, then re-enable Cassini |
| `state=unavailable`, `step=administrator` | No probed account is an administrator Cassini may act as | Set `CASSINI_NC_ADMIN_USER` to one, then re-enable Cassini |
| `state=unknown` | The container restarted without the app being re-enabled, so setup has not run in this process (D-541) | Disable and re-enable Cassini |
| Publishes fail with "the recordings substrate is not provisioned" | Deliberate: writing into the owner's private home would leave recordings nobody can open | Fix the state above, or set `CASSINI_PUBLISH_SINK=local` to keep recordings on the app's own volume |
| Log says required universal group `everyone` is unavailable | Everyone Group app missing/disabled | Install/enable `group_everyone`, then re-enable Cassini |
| Service-account setup fails | Administrator discovery is wrong or account/group provisioning was rejected | Inspect `nc provision:` logs; set `CASSINI_NC_ADMIN_USER` to the correct administrator and re-enable Cassini |
| `ensure group folder` fails | Team folders app missing/disabled | Install/enable `groupfolders`, then re-enable Cassini |
| Fresh user has no Cassini mount | `group_everyone` disabled or Team-folder mapping missing | Confirm `occ user:info <user>` includes `everyone` and `groupfolders:list` assigns `everyone:1` |
| Root becomes owner-only after upgrade | Legacy leaf/catalog ACL migration failed | Inspect `nc provision:` logs, correct the DAV/ACL error, then re-enable Cassini |
| Everyone sees a private meeting | Leaf lacks an explicit `everyone` deny or ACLs were disabled | Re-enable Cassini to run protection; inspect the leaf's Advanced permissions |
| Granted user sees an empty list | Caller cannot traverse or leaf ACL was not applied | Confirm advanced ACL, `everyone:1` mount, root read, and the participant allow |
| Meeting visible to nobody | Non-Talk job or private room with only non-local participants | Share the `.opus` manually |
| Viewer returns 502 | Nextcloud Files is unreachable | Check ExApp-to-Nextcloud DAV connectivity and owner identity |

## Related

- **D-530** — the design spike this implements (the proposal is not tracked in
  this repo).
- **D-532** — dedicated `cassini` service account and owner group.
- **D-552** — the virtual `everyone` group and public-conversation access.
- **D-554** — made this the only mode and added the `/status` substrate report.
- **D-529** — the underlying Nextcloud Files delivery this builds on.
- **D-552** — authenticated account-wide access for public Talk recordings.
