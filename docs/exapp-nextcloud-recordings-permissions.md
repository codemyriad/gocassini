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

Run these once, substituting your viewer group and owner. Command flags vary
slightly across Group folders versions — confirm with
`occ groupfolders:permissions --help` and the **Files → (folder) → Advanced
permissions** UI, which can do every step below if you prefer clicking.

```bash
# 1. Create the group folder whose mount point is "Cassini".
occ groupfolders:create Cassini                       # prints a numeric <folder_id>

# 2. Assign a BROAD viewer group — every potential recording viewer must be a
#    member, because the group folder only mounts for members of an assigned
#    group. (Create the group first if needed: occ group:add recording-viewers)
occ groupfolders:group <folder_id> recording-viewers

# 3. Turn on advanced ACL and the default-deny floor, so members see NOTHING in
#    the folder until a per-meeting rule grants them read.
occ groupfolders:permissions <folder_id> --enable
#    Default-deny ("Deny by default"): set it in the Advanced permissions UI, or
#    the version-specific occ flag (e.g. --acl-no-default-permission).

# 4. Delegate the owner as the folder's ACL manager, so the operator can set
#    per-meeting rules over WebDAV without admin impersonation.
occ groupfolders:permissions <folder_id> --manage-add --user admin

# 5. The owner must ALSO be a member of the viewer group, so the folder mounts
#    in its own tree for the operator's PROPPATCH/PUT path.
occ group:adduser recording-viewers admin
```

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
      grant recording-viewers read on the "Cassini" and "Cassini/Recordings"
      container folders (NOT the .opus files) in Advanced permissions, keeping
      default-deny on the leaves, and re-check. Adjust until the list matches
      exactly what each caller is granted.
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
| A granted user sees an empty list | Per-caller scan can't traverse the container folders | Grant the viewer group read on `Cassini` + `Cassini/Recordings` (containers only), keep default-deny on leaves |
| A meeting is visible to no one | Non-Talk job, or all participants were guests/federated | Share the `.opus` (+ sidecar) manually |
| Viewer errors instead of empty list | Nextcloud Files unreachable (502) | Check the ExApp → Nextcloud WebDAV connectivity and the owner account |
| Publish succeeds but no ACL applied | Best-effort ACL step failed (logged, non-fatal) | Check operator logs for `nc files access …`; re-publish to retry |

## Related

- `docs/proposals/nextcloud-files-access-and-index.md` — the design and the
  spike this implements.
- **D-532** — dedicated `cassini` service account (replaces the `admin` interim).
- **D-529** — the underlying Nextcloud Files delivery this builds on.
