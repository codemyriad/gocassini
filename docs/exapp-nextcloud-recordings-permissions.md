# Managing recording permissions in Nextcloud Files

How to turn on and operate **per-participant access control** for Cassini
recordings (D-534). With it enabled, a recording is visible in the Cassini
viewer **only to the people who were in the Talk room when it was published** —
access is governed entirely by Nextcloud's own file permissions, and admins
change it with Nextcloud's normal sharing/ACL tools. With it **off** (the
default), recordings behave as before D-534: any authenticated user can browse
and play every meeting.

> **Status.** The write side (freezing a meeting's audience) and the read side
> (serving each caller only what they may see) are implemented and shipped
> behind a flag. The exact group-folder ACL rule recipe that makes a per-user
> scan return only the caller's meetings is validated per instance — see
> [Validate on your instance](#validate-on-your-instance). The **playback**
> path is always ACL-enforced by Nextcloud, so a caller can never *play* a
> recording they were not granted, regardless of list tuning.

## How it works

```text
  PUBLISH (write)                                    VIEW (read)
  ───────────────                                    ───────────
  operator delivers <id>.opus to NC Files (D-529)    browser opens the Cassini viewer
        │                                                  │  GET /published/catalog.json
        │  enumerate Talk participants (as owner)          │  GET /published/meetings/<id>.opus
        ▼                                                  ▼
  PROPPATCH nc:acl-list on <id>.opus (+ sidecar):    operator proxy acts AS THE CALLER:
    +read for each participant (users/groups/           • catalog.json → the authoritative list,
    circles). Guests/email/federated are skipped.         filtered to the meetings the caller's
        │                                                   own PROPFIND scan can see
        ▼                                                  • meetings/<id>.opus → fetched as the
  access FROZEN at publish, editable later in NC        caller; Nextcloud enforces the ACL (404 if
                                                          not granted — existence never leaks)
```

The recordings live in a **group folder** (a system-owned "Team folder") with
**advanced ACL** and a **default-deny** floor. That is the only topology where
the same tree is visible, at the same path, in every member's Files — which is
what lets the operator read it *as each caller*. A non-admin caller cannot read
another user's home, so a plain per-user home (pre-D-534) cannot support this.

## Prerequisites

- Nextcloud **32+** with the **Group folders** ("Team folders") app enabled.
- The Cassini ExApp deployed and enabled (AppAPI).
- `occ` access on the Nextcloud server (or the Group folders admin UI).

## One-time setup

The operator writes and reads over WebDAV as an existing Nextcloud user (the
**owner / ACL manager**). The first pass uses the built-in `admin` account; a
dedicated `cassini` service account is tracked in **D-532** and is a drop-in
replacement (change `ncRecordingsOwner`).

Run these once. The current implementation uses the exact owner `admin` and
viewer group `recording-viewers`. The commands below match Group folders 22 on
Nextcloud 34; use each command's `--help` when configuring another version.

```bash
# 1. Create the Team folder with its default-deny floor from the start.
#    Nextcloud 34 cannot toggle this creation option on an existing folder.
occ groupfolders:create --acl-no-default-permission Cassini  # prints <folder_id>

# 2. Mount it read-only for every potential viewer. Give the owner/admin group
#    a separate write-capable mount mapping; ACL-management permission alone
#    does NOT grant WebDAV create/write access.
occ groupfolders:group <folder_id> recording-viewers read
occ groupfolders:group <folder_id> admin read write delete share

# 3. Enable advanced ACL and delegate admin as its manager.
occ groupfolders:permissions <folder_id> --enable
occ groupfolders:permissions <folder_id> --manage-add --user admin

# 4. Grant the broad group read on the root containers so callers can traverse
#    Cassini/Recordings/meetings. Cassini writes an explicit recording-viewers
#    deny on every leaf and participant allows on top, so this does not grant
#    non-participants access to recording files.
occ groupfolders:permissions <folder_id> / --group recording-viewers +read

# 5. Grant the owner full root ACL in addition to the mount mapping. It needs
#    create/update to synchronize Files and manage ACLs on existing recordings.
occ groupfolders:permissions <folder_id> / --user admin \
  +read +write +create +delete +share

# The owner may also be a viewer/participant in harness meetings.
occ group:adduser recording-viewers admin
```

Verify the resulting configuration:

```bash
occ groupfolders:list --output=json_pretty
occ groupfolders:permissions <folder_id> --output=json_pretty
occ groupfolders:permissions <folder_id> / --test --user admin
```

The folder JSON must show `acl_default_no_permission: true`, the `admin` group
with permissions `31`, and `recording-viewers` with permissions `1`. The admin
ACL test must report `+read, +write, +create, +delete, +share`. If an existing
empty `Cassini` folder was created without the default-deny option, delete and
recreate it with the commands above. Do not delete a non-empty folder without
backing it up first.

Then enable the feature on the ExApp and restart it:

```bash
CASSINI_NC_ACCESS_CONTROL=true
```

Set it in the ExApp's environment — e.g. the deploy/compose env or the AppAPI
deploy config. Any value Go's `strconv.ParseBool` accepts works (`true`, `1`).
The local installed-ExApp harness declares and passes this value automatically,
defaulting it to `true`:

```bash
./bin/cassini dev stack up \
  --cassini installed-exapp \
  --nc-access-control=true \
  ...
```

Use `--nc-access-control=false` or `CASSINI_NC_ACCESS_CONTROL=false` to test the
legacy public-archive behavior. Production remains off when the deploy value is
not supplied.

```text
  who can be a viewer?                who can see meeting X?
  ────────────────────                ──────────────────────
  member of recording-viewers   AND   granted +read on X.opus at publish
        │                                     │
        └── folder mounts in their Files      └── advanced ACL rule (frozen at publish)
            (default-deny: empty until granted)
```

## Day-to-day: editing a meeting's permissions

Access is **frozen at publish** to the room's participants, then fully editable:

- **In the Files UI:** open the group folder → the meeting's `.opus` → the
  sharing panel's **Advanced permissions** tab → add or remove users/groups and
  toggle their read access. Do the same on the `<id>.manifest.json` sidecar so
  the list entry stays consistent.
- **Revoke / grant to more people:** add or remove read rules on `<id>.opus`
  (and its sidecar). Removing all rules makes the meeting visible only to the
  ACL manager.
- **Guests / email / federated participants** are skipped automatically at
  publish (they have no local account to grant). To share with them, use a
  normal Nextcloud share/public link on the `.opus` (out of scope for the
  managed model).
- **Non-Talk / dev recordings** have no participant list, so publish grants them
  to no one — they stay manager-only until you share them by hand.

## Validate on your instance

Because Group folders ACL inheritance is version-sensitive, confirm the rule
recipe once on your instance:

```text
  [ ] Create a test meeting; publish it with two Talk participants (alice, bob)
      and a non-participant (carol) all in recording-viewers.
  [ ] As alice: the Cassini viewer lists the meeting and plays it.
  [ ] As carol: the Cassini viewer does NOT list it, and a direct
      /published/meetings/<id>.opus returns 404.
  [ ] If alice sees an EMPTY list, the per-caller scan can't traverse the folder:
      confirm the root recording-viewers `+read` traversal rule and the
      recording's explicit recording-viewers deny + participant allows. Do not
      grant the broad group read directly on an `.opus` file.
```

The playback check is the security floor and must always hold. The list check is
the visibility tuning; the operator **fails closed** (shows an empty list) rather
than ever leaking, so a mis-tuned recipe degrades to "no meetings", never to
"everyone's meetings".

## Turning it off

Unset `CASSINI_NC_ACCESS_CONTROL` (or set it to `false`) and restart. In the
local harness, whose default is `true`, explicitly pass
`--nc-access-control=false`. The operator reverts to the D-529 public behavior
immediately (it serves as the owner again). Existing ACLs on the files remain
but are not consulted by the public read path. The group folder and its
contents are unaffected.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Everyone still sees every meeting | Flag not actually set, or operator not restarted | Confirm `CASSINI_NC_ACCESS_CONTROL=true` in the running container's env |
| A granted user sees an empty list | Per-caller scan can't traverse the container folders, or the leaf ACL was not applied | Confirm the root traversal rule and the leaf's broad-group deny + participant allows; re-publish to retry |
| A meeting is visible to no one | Non-Talk job, or all participants were guests/federated | Share the `.opus` (+ sidecar) manually |
| Viewer errors instead of empty list | Nextcloud Files unreachable (502) | Check the ExApp → Nextcloud WebDAV connectivity and the owner account |
| Files delivery reports `MKCOL Cassini/Recordings -> 403` | Owner has ACL-management permission but no write-capable Team-folder mapping/root ACL | Add the `admin` group mapping and explicit admin root ACL from the one-time setup, then re-publish |
| Publish succeeds but no ACL applied | Best-effort ACL step failed (logged, non-fatal) | Check operator logs for `nc files access …`; re-publish to retry |

## Related

- `docs/proposals/nextcloud-files-access-and-index.md` — the design and the
  spike this implements.
- **D-532** — dedicated `cassini` service account (replaces the `admin` interim).
- **D-529** — the underlying Nextcloud Files delivery this builds on.
