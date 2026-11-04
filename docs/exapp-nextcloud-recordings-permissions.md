# Recording permissions in Nextcloud

Cassini uses one permission model for installed ExApps. Every recording belongs
to the dedicated `cassini` account in its private `CassiniRecordings/meetings`
folder. Cassini gives the Talk room's Nextcloud users, groups, and Teams direct
Files shares on the recording. A public Talk room grants reshare permission to
those recipients, including members reached through a group or Team share,
when the instance permits it. A recipient may then create a public link if
Nextcloud permits link sharing. Guests without an account receive no Files
share. Cassini itself does not create public links.

## Access checks

The meeting list starts from the caller's current Nextcloud shares. Cassini's
local SQLite metadata index supplies titles and dates, but it cannot make a
recording visible. On audio, transcript, annotation, search, and context reads,
Cassini resolves the current share and asks Nextcloud to read the file as the
caller. Removing access in Nextcloud removes it in Cassini. Nextcloud's handling
of downstream reshares and instance-wide reshare policy applies.

Anyone who can read a recording can add or remove its shared Cassini marks and
tags. Each annotation request checks the caller's current share before it
accepts a change.

The `cassini` account can read all recordings because it owns them. Cassini
requires its Files root to be a private directory before publishing. If a share
cannot be made for the meeting starter, publication fails. Any other recipient
Nextcloud refuses — an individual, a group or a Team, typically because of an
instance sharing policy — is skipped (D-885): the meeting is published for
everyone else, the run says "Published, but not shared with everyone" with the
list of who was refused and why, and the run list flags it. An administrator
can re-share once the policy is fixed. Outages and malformed share responses
still stop publication.

A share that was made may still be waiting for the person. Nextcloud lets an
account accept shared files by hand (Personal settings › Sharing), and an
administrator can make that the rule for everyone. Until the person accepts,
the recording is in none of their lists, and Nextcloud's notification shows
the share with Accept and Decline. Cassini tells them too: `GET
operator/setup` (USER) carries the caller's waiting recording shares and
their refusals as `sharing`, and Browse lists the waiting ones with **Accept**
for one and **Accept all** for the lot. Accepting goes through `POST
operator/shares/accept` (USER): the operator accepts as the person, through
Nextcloud's own pending-share endpoint, and only shares that are in that
person's own pending list and are Cassini's recordings. Nothing is accepted
unasked, and never for anyone else. Every recording share carries a note
naming the meeting, its date and its length ("Recording of Weekly sync,
Friday 30 October 2026, 10:00 (42 min). Open it in Cassini."), so the file is
recognisable in Files; the notification itself does not show the note.

Nextcloud's sharing settings apply to the `cassini` account like to anyone:
"Allow apps to use the Share API", "Exclude some groups from sharing" /
"Limit sharing to some groups", "Restrict users to only share with users in
their groups" (with its ignored groups) and "Allow sharing with groups". The
**Recording sharing** check in Doctor reads them, and `cassini`'s groups, and
names the setting and the remedy when they stop Cassini sharing recordings.

The installed app creates no remote `catalog.json` and needs neither Team
folders nor the Everyone Group app. The local static exporter still uses
`catalog.json` to build a portable static site.

## Imported meetings

A portable meeting file brought in through the viewer's Import tab or with
`cassini-operator import-meeting` (D-828) follows the same model. The importer
chooses the audience at import time: nobody (the importer alone), or selected
users, groups, and Teams — the importer always receives a share, as the
recording starter does. A refused importer, group or Team fails the import; a
refused individual is skipped. Created shares carry reshare permission where the
instance allows it, the same permission a public room's recording grants, so
recipients can pass the meeting along. There is no "everyone" option, and no
public link is ever created. Afterwards the shares are ordinary Nextcloud file
shares, changed in Files like any other.

Every imported meeting is listed under one **Imported meetings** room,
regardless of who imported it: the ones you brought in and the ones shared
with you sit together there. The room the meeting was recorded in on its
source installation is not shown; it is kept on the import record only.

## Storage and scale

The metadata index supports list filters and paging, but Nextcloud's shares are
always the access source. Listings currently collect one Nextcloud share
snapshot for the caller; this is suitable for dozens to hundreds of meetings.
Thousands may require paging or a share cache in a later release. A cache must
be checked against current Nextcloud access before it affects the returned list.

## Older installations

This branch removes the previous mode choice and automatic migration. See
[cutover instructions](./direct-shares-cutover.md) for moving an older archive
into the private root and assigning shares.
