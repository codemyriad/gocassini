# Managing recording permissions in Nextcloud Files

How **per-participant access control** for Cassini recordings works and how to
operate it. With it enabled, a recording is visible in the Cassini viewer **only
to the people who were in the Talk room when it was published** — access is
governed entirely by Nextcloud's own file permissions, and admins change it with
Nextcloud's normal sharing/ACL tools. With it **off** (the production default),
recordings behave as before: any authenticated user can browse and play every
meeting.

> **Setup is automatic.** Enabling the ExApp with access control on makes the
> operator create the canonical recordings directory and all of its
> permissions itself, over Nextcloud's HTTP APIs — there is **no `occ`
> group-folder setup to run by hand** any more (that was the pre-automation
> procedure). The one environmental prerequisite is the **Group folders / Team
> folders app**, which the operator cannot install for itself. See
> [Prerequisites](#prerequisites) and [Automatic setup](#automatic-setup).

## How it works

```text
  PUBLISH (write)                                    VIEW (read)
  ───────────────                                    ───────────
  operator delivers <id>.opus to NC Files (D-529)    browser opens the Cassini viewer
        │                                                  │  GET /published/catalog.json
        │  enumerate Talk participants (as owner)          │  GET /published/meetings/<id>.opus
        ▼                                                  ▼
  PROPPATCH nc:acl-list on <id>.opus:                operator proxy acts AS THE CALLER:
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
another user's home, so a plain per-user home cannot support this.

### The permission model

```text
  who can see the DIRECTORY?          who can see meeting X?
  ──────────────────────────          ──────────────────────
  every logged-in account       AND   granted +read on X.opus at publish
        │                                     │
        │ (recording-viewers group,           └── advanced ACL rule (frozen at publish);
        │  kept == all users by the               each leaf denies the broad viewer group
        │  operator — see below)                   and allows only the participants
        │
        └── group folder mount + a root ACL granting the viewer group read,
            so anyone can browse/traverse the recordings tree …
                … but each recording file overrides that with a deny + per-participant
                  allow, so a non-participant sees the directory but not the file
                  (a direct fetch 404s; existence never leaks).
```

- **Everyone with a logged-in account can read the directory.** The operator
  keeps a `recording-viewers` group equal to *all users* and grants that group
  read on the recordings root, so any account can open and traverse the
  archive.
- **Each recording file is private to its participants.** At publish the
  operator writes a per-file advanced-ACL rule that denies the broad viewer
  group and allows only the meeting's Talk participants.

## Prerequisites

- Nextcloud **32+**.
- The **Group folders** ("Team folders") app enabled. This is the single
  one-click prerequisite an admin installs (Apps → search "Team folders" →
  Enable); the ExApp reaches Nextcloud only over HTTP and cannot install a PHP
  app for itself. The local harness installs and enables it automatically in
  `harness/bin/bootstrap.sh`.
- The Cassini ExApp deployed and enabled (AppAPI), with access control turned
  on (see [Turning it on](#turning-it-on)).

## Automatic setup

On the AppAPI **enabled** edge (install, re-enable, or restart) — the first
moment its act-as-user calls back into Nextcloud are accepted — the operator
provisions everything idempotently (`cassini-operator/internal/operator/nc_provision.go`):

```text
  ├── ensure the recording-viewers group exists          (OCS provisioning API)
  ├── ensure the "Cassini" group folder, default-deny     (Group Folders API, addFolder
  │     acl_default_no_permission=1 — only settable at creation)
  ├── assign recording-viewers READ + the owner group ALL (Group Folders API)
  ├── enable advanced ACL + delegate the owner as manager (Group Folders API)
  ├── root container ACL: owner ALL + viewer-group READ   (WebDAV PROPPATCH)
  │     → the owner can write under default-deny; everyone can traverse
  ├── MKCOL Cassini/Recordings/meetings                   (WebDAV) so the dir exists on install
  └── reconcile every user into recording-viewers         (OCS provisioning API)
        + a periodic reconcile (every 15 min) so accounts created later converge
```

Every step is idempotent and best-effort: a failure is logged (`nc provision: …`
in the operator log) and never blocks startup or delivery, and each step is safe
to re-run on the next enable. The owner / ACL-manager is the built-in `admin`
account for now; a dedicated `cassini` service account is tracked in **D-532**
and is a drop-in replacement (change `ncRecordingsOwner`).

Verify the result (optional) with `occ`:

```bash
occ groupfolders:list --output=json_pretty   # acl: true, acl_default_no_permission: true,
                                              # groups {admin:31, recording-viewers:1}, manage [admin]
occ group:list | grep -A99 recording-viewers # == all users
```

### New accounts

Nextcloud has no built-in "all users" group and an ExApp cannot hook user
creation, so the operator keeps `recording-viewers == all users` by reconciling:
once on every enabled edge (covers everyone present at install) and then on a
15-minute timer. A brand-new account therefore gains read/traversal of the
recordings directory within one interval; the per-file participant ACLs are
unaffected either way.

## Turning it on

Set the ExApp environment variable:

```bash
CASSINI_NC_ACCESS_CONTROL=true
```

Set it in the ExApp's environment — the deploy/compose env or the AppAPI deploy
config — and restart the app so the enabled edge re-provisions. Any value Go's
`strconv.ParseBool` accepts works (`true`, `1`). The local installed-ExApp
harness declares and passes this value automatically, defaulting it to `true`:

```bash
./bin/cassini dev stack up \
  --cassini installed-exapp \
  --nc-access-control=true \
  ...
```

Production remains **off** when the deploy value is not supplied.

## Day-to-day: editing a meeting's permissions

Access is **frozen at publish** to the room's participants, then fully editable:

- **In the Files UI:** open the group folder → the meeting's `.opus` → the
  sharing panel's **Advanced permissions** tab → add or remove users/groups and
  toggle their read access.
- **Revoke / grant to more people:** add or remove read rules on `<id>.opus`.
  Removing all rules makes the meeting visible only to the ACL manager.
- **Guests / email / federated participants** are skipped automatically at
  publish (they have no local account to grant). To share with them, use a
  normal Nextcloud share/public link on the `.opus` (out of scope for the
  managed model).
- **Non-Talk / dev recordings** have no participant list, so publish grants them
  to no one — they stay manager-only until you share them by hand.

## Validate on your instance

Because Group folders ACL inheritance is version-sensitive, you can confirm the
recipe on your instance:

```text
  [ ] Record + publish a meeting with two Talk participants (alice, bob) and
      have a non-participant (carol) logged in.
  [ ] As alice: the Cassini viewer lists the meeting and plays it.
  [ ] As carol: the Cassini viewer does NOT list it, and a direct
      /published/meetings/<id>.opus returns 404.
  [ ] If alice sees an EMPTY list, the per-caller scan can't traverse the folder:
      confirm (occ groupfolders:list) that the folder has advanced ACL + the
      recording-viewers read mount, and that carol is in recording-viewers.
```

The playback check is the security floor and must always hold. The list check is
the visibility tuning; the operator **fails closed** (shows an empty list) rather
than ever leaking, so a mis-tuned recipe degrades to "no meetings", never to
"everyone's meetings".

## Migrating from the public archive

If a deployment already ran with access control **off** (the D-529 public
archive), recordings live in the owner's own `Cassini/Recordings` **home**
folder. Turning access control on creates a *group folder* mounted at `Cassini`,
and Nextcloud will not mount a group folder over an existing same-named home
folder — it remaps the mount (e.g. `Cassini (2)`), so the operator would write to
a different path and the previously delivered recordings would be left behind in
the old home folder. Before enabling access control on such an instance, move or
remove the owner's existing `Cassini` home folder (back it up first) so the group
folder can mount at the canonical `Cassini` path, then re-enable; the startup
sync re-delivers the archive into the group folder.

## Turning it off

Unset `CASSINI_NC_ACCESS_CONTROL` (or set it to `false`) and restart. In the
local harness, whose default is `true`, explicitly pass
`--nc-access-control=false`. The operator reverts to the public behavior
immediately (it serves as the owner again) and stops provisioning/reconciling.
Existing ACLs on the files remain but are not consulted by the public read path.
The group folder and its contents are unaffected.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Everyone still sees every meeting | Flag not actually set, or operator not restarted | Confirm `CASSINI_NC_ACCESS_CONTROL=true` in the running container's env |
| `nc provision: ensure group folder … failed` in the log | Group folders app not installed/enabled | Enable the Team folders app, then re-enable Cassini |
| A granted user sees an empty list | Per-caller scan can't traverse the container folders, or the leaf ACL was not applied | Confirm (occ) the folder's advanced ACL + recording-viewers read mount and that the user is in recording-viewers; re-publish to retry |
| A meeting is visible to no one | Non-Talk job, or all participants were guests/federated | Share the `.opus` manually |
| Viewer errors instead of empty list | Nextcloud Files unreachable (502) | Check the ExApp → Nextcloud WebDAV connectivity and the owner account |
| Files delivery reports `MKCOL Cassini/Recordings -> 403` | Root container ACL (owner grant) not applied — provisioning was interrupted | Re-enable Cassini so the enabled edge re-provisions the root ACL, then re-publish |
| Publish succeeds but no ACL applied | Best-effort ACL step failed (logged, non-fatal) | Check operator logs for `nc files access …`; re-publish to retry |

## Related

- `docs/proposals/nextcloud-files-access-and-index.md` — the design and the
  spike this implements.
- **D-532** — dedicated `cassini` service account (replaces the `admin` interim).
- **D-529** — the underlying Nextcloud Files delivery this builds on.
