---
title: Who can see a recording
description: Each recording is shared with the people, groups and Teams in its Talk room, through ordinary Nextcloud file shares.
source: docs/exapp-nextcloud-recordings-permissions.md
copied: "2026-10-13"
---

Access follows the Talk room. Each recording is shared with the room's
Nextcloud people, groups and Teams, using ordinary Nextcloud file shares, and
Nextcloud Files decides who can read it. There is nothing to choose and no
extra Nextcloud app to install.

## Who gets a share

- **Everyone in the room**, captured when the recording starts and again when
  it stops. That includes people who were invited but never joined, not only
  those present on the call.
- **Groups and Teams are shared as themselves**, not expanded into a list of
  people, so changing a group or Team's membership changes who can open the
  recordings shared with it.
- **The person who started the recording** always gets a share, even if Talk's
  room list leaves them out.
- **Guests, email invitees and federated participants** get nothing. They have no
  local account to share with. Share the file from Files by hand if somebody
  needs it.

The room is captured while the meeting runs, so people who join or leave the
room later do not change who can open an earlier recording.

## Public rooms

A recording of a public Talk room is shared with the same people, with
permission to reshare where this Nextcloud allows it. Recipients can then pass
it on, or make a public link if the instance permits link sharing. Cassini never
creates a public link itself.

## Where recordings live

Every recording belongs to the `cassini` service account, in its private
`CassiniRecordings/meetings` folder, as one `.opus` file per meeting. Nothing
else is mounted there, and Cassini checks the folder is private before it
publishes. The `cassini` account can read every recording because it owns them;
no other account can, unless the recording is shared with it.

## Changing who can see a recording

The shares are ordinary Nextcloud file shares, so change them the usual way:
sign in as `cassini`, open the recording's `.opus` in Files, and use the sharing
panel. Removing a share removes access in Cassini straight away, because Cassini
checks the caller's current shares on every request.

## A recording you may not read looks like one that does not exist

This is deliberate, and it is the same everywhere:

- **In the app**, the meeting list is built from your own current Nextcloud
  shares. A recording you may not read is simply not in your list, and asking
  for it directly answers 404.
- **From the CLI**, `cassini meetings` answers *no recording you can read* rather
  than "forbidden" or "no such meeting". See
  [Agent access via the CLI](/docs/guides/agent-access).

Cassini keeps titles and dates in a local index for the meeting cards, but that
index never makes a recording visible. Every list, playback, transcript, search
and annotation goes through Nextcloud as the person asking.

## When a share cannot be made

- If Cassini cannot share a recording with the person who started it, the
  recording is not published.
- A failed group or Team share also stops publication, because it could leave
  many people out.
- One person's share being refused is a warning, and everybody else keeps
  access.

The warning and the state of the setup are under **Operator › Settings › Publish
pipeline › Who can see recordings**, and on `/operator/status` as `recordings_access`.

## Recordings from before direct shares

Older installs kept recordings in a Team folder or in `CassiniNoACL`. Cassini
does not move those automatically. The
[cutover steps](https://github.com/codemyriad/gocassini/blob/main/docs/direct-shares-cutover.md)
show how to move them into `CassiniRecordings/meetings` and share them.

## Related

- [Install on Nextcloud](/docs/getting-started/install): the recordings account
  and the sharing check.
- [Privacy and data processing](/docs/guides/privacy): what is stored, where,
  and what leaves your infrastructure.
- [Troubleshooting](/docs/guides/troubleshooting): the setup states and what
  clears them.
