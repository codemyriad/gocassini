# Managing recording permissions in Nextcloud Files

How **per-participant access control** for Cassini recordings works and how to
operate it. A recording is visible in the Cassini viewer **only to the people
who had access to the Talk room when it was published** — that is everyone on
the room's attendee list, including users who were invited but never joined the
call, and not only those present during it — access is governed entirely by
Nextcloud's own file permissions, and admins change it with Nextcloud's normal
sharing/ACL tools.

This is the only mode. There is no setting that turns it off and no public
archive to fall back to: an installed Cassini ExApp serves each caller only the
recordings Nextcloud says they may read (D-554).

> **Setup is automatic.** Enabling the ExApp makes the
> operator create the canonical recordings directory and all of its
> permissions itself, over Nextcloud's HTTP APIs — there is **no `occ`
> group-folder setup to run by hand** any more (that was the pre-automation
> procedure). The one environmental prerequisite is the **Group folders / Team
> folders app**, which the operator cannot install for itself. See
> [Prerequisites](#prerequisites) and [Automatic setup](#automatic-setup).

> **Public conversations.** A conversation anyone with the link can join is not
> participant-private, so its recording is readable by **any account on this
> Nextcloud** rather than only its attendees. Publicness is read from Talk at
> record time and frozen with the recording, so flipping a conversation public
> afterwards does not widen a recording made while it was private, and the
> reverse does not narrow one people were already told they could see. If the
> room lookup fails the recording is treated as non-public — over-restriction is
> recoverable by a rerun, over-sharing is not. This does not create a public
> link: the viewer is still sign-in only.

> **Who owns the recordings.** The canonical tree is owned by a dedicated
> `cassini` service account, created automatically when the app is enabled. It
> is the only identity that writes recordings and manages their permissions.
> Instance setup — creating the group folder, the groups and that account —
> is done as an administrator instead, discovered automatically (override with
> `CASSINI_NC_ADMIN_USER` if detection is wrong). The two are deliberately
> separate: the account holding every recording should not also be able to
> reconfigure the instance.
>
> The recordings live in a group folder, which is shared storage rather than
> anyone's home directory, so this owner names the identity Cassini acts as —
> not where the files sit. Upgrading from an earlier version therefore needs no
> data migration; existing recordings stay exactly where they are.

> **How the audience is resolved.** The attendee list is read from Talk once, at
> publish time, and frozen onto the recording. Because that lookup now gates the
> publish, it retries three times as the recording's starter and then falls back
> to asking as the recordings owner — which is the only thing that helps when the
> starter has left the room, been removed or been disabled between recording and
> publishing. If every attempt fails the publish fails, with the reason on the
> job; a rerun re-attempts it. A room whose attendees are all guests, email or
> federated users has no local account to grant, so the recording stays readable
> by the owner alone — that is a successful publish, logged, not a failure.

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
- The Cassini ExApp deployed and enabled (AppAPI).

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

Every step is idempotent and safe to re-run on the next enable. A failure does
not stop the operator starting — an instance that cannot provision should still
come up so an admin can look at it — but it is **not** silent: the outcome is
recorded and reported by `GET /operator/status` under `recordings_access`, and
an ExApp whose substrate is missing answers **503** there rather than looking
healthy while showing nobody their recordings.

```json
"recordings_access": {
  "applicable": true,
  "publish_sink": "nextcloud-files",
  "ok": false,
  "detail": "group folder Cassini (is the Group folders app enabled?): …",
  "checked_at": "2026-08-03T21:14:02Z"
}
```

A publish is a different matter: writing a recording's participant ACL is part
of delivering it, so a failure there fails the publish rather than shipping a
recording nobody has been granted (D-549).

Verify the result (optional) with `occ`:

```bash
occ groupfolders:list --output=json_pretty   # acl: true, acl_default_no_permission: true,
                                              # groups {cassini:31, recording-viewers:1}, manage [cassini]
occ group:list | grep -A99 recording-viewers # == all users
```

### New accounts

Nextcloud has no built-in "all users" group and an ExApp cannot hook user
creation, so the operator keeps `recording-viewers == all users` by reconciling:
once on every enabled edge (covers everyone present at install) and then on a
15-minute timer. A brand-new account therefore gains read/traversal of the
recordings directory within one interval; the per-file participant ACLs are
unaffected either way.

## Is it on?

Nothing turns it on. Whenever the operator runs as an installed ExApp, its
recordings are access-controlled and the directory is provisioned on the enabled
edge; the only thing an admin does is enable the Group folders app (see
[Prerequisites](#prerequisites)).

To confirm an instance is actually set up, ask the operator:

```bash
curl -sS -u admin:<pass> \
  "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status" \
  | jq .recordings_access
```

`ok: true` means the group folder, its ACL floor, the viewer group and the
canonical collections are all in place.

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
      recording-viewers read mount, and that alice is in recording-viewers.
```

The playback check is the security floor and must always hold. The list check is
the visibility tuning; the operator **fails closed** (shows an empty list) rather
than ever leaking, so a mis-tuned recipe degrades to "no meetings", never to
"everyone's meetings".

## Upgrading from a pre-access-control install

If a deployment ran before access control was unconditional — when the operator
served the D-529 public archive — its recordings live in the owner's own
`Cassini/Recordings` **home** folder. Provisioning creates a *group folder*
mounted at `Cassini`, and Nextcloud will not mount a group folder over an
existing same-named home folder: it remaps the mount (e.g. `Cassini (2)`), so the
operator writes to a different path and the previously delivered recordings are
left behind in the old one.

Before upgrading such an instance, move or remove the owner's existing `Cassini`
home folder (back it up first) so the group folder can mount at the canonical
`Cassini` path, then enable Cassini; the startup sync re-delivers the archive
into the group folder. Which account holds that stale home folder depends on the
version you are coming from — `admin` before D-532, `cassini` after it.

There is no way back. `CASSINI_NC_ACCESS_CONTROL` was removed in D-554 and the
public read path with it, so an instance that upgrades is access-controlled.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Everyone still sees every meeting | Running a build from before D-554, when access control was an opt-in flag | Upgrade; there is no configuration that produces this any more |
| Nobody sees any meeting | Provisioning did not complete — most likely the group folder is missing | `GET /operator/status` → `recordings_access`; it names the step that failed. Enable the Team folders app, then re-enable Cassini |
| `nc provision: ensure group folder … failed` in the log | Group folders app not installed/enabled | Enable the Team folders app, then re-enable Cassini |
| A granted user sees an empty list | Per-caller scan can't traverse the container folders, or the leaf ACL was not applied | Confirm (occ) the folder's advanced ACL + recording-viewers read mount and that the user is in recording-viewers; re-publish to retry |
| A meeting is visible to no one | Non-Talk job, or all participants were guests/federated | Share the `.opus` manually |
| Viewer errors instead of empty list | Nextcloud Files unreachable (502) | Check the ExApp → Nextcloud WebDAV connectivity and the owner account |
| Files delivery reports `MKCOL Cassini/Recordings -> 403` | Root container ACL (owner grant) not applied — provisioning was interrupted | Re-enable Cassini so the enabled edge re-provisions the root ACL, then re-publish |
| Publish succeeds but no ACL applied | Best-effort ACL step failed (logged, non-fatal) | Check operator logs for `nc files access …`; re-publish to retry |

## Related

- **D-530** — the design spike this implements (the proposal is not tracked in
  this repo).
- **D-532** — dedicated `cassini` service account; landed, and replaced the
  `admin` interim.
- **D-554** — made this the only mode and added the `/status` substrate report.
- **D-529** — the underlying Nextcloud Files delivery this builds on.
