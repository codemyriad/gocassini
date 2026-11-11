# Recording permissions in Nextcloud

Cassini uses one permission model for installed ExApps. Every recording belongs
to the dedicated `cassini` account in its private `CassiniRecordings/meetings`
folder. Cassini gives the Talk room's Nextcloud users, groups, and Teams direct
Files shares on the recording. A public Talk room grants reshare permission to
those recipients, including members reached through a group or Team share,
when the instance permits it. A recipient may then create a public link if
Nextcloud permits link sharing. Guests without an account receive no Files
share. Cassini itself does not create public links.

## Who the recording is shared with

By default every recording is shared with everyone in the Talk room — a
standing channel's whole membership, whether they were in the call or not. An
administrator can change **Share recordings with** in Operator › Publish
pipeline to **Only people who were in the call**. In that mode the roster is
the local Nextcloud accounts Talk reports with its `inCall` flag when the
recording starts and stops, plus every local account the recorder itself saw in
the call, plus the person who started it. Guests, email invitees and federated
participants are skipped in either mode. The recorder's own list is monotonic,
so someone who joined after the start and left again before the stop is still
in it.

The chosen mode is saved on the job when recording starts: changing the setting
mid-recording does not alter that recording's captures, publication, re-share or
viewer label, and changing it later does not re-share recordings already
published. In attendee mode there is no publish-time Talk lookup. If no attendee
roster was captured at all, the recording is shared only with the person who
started it and the run carries a warning. Attendee mode also skips the Talk room
chat post, whose title and link the whole room would see.

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
unasked, and never for anyone else. A published recording Nextcloud refused
the person, and one they started that was not published because Nextcloud
refused them, are listed in `sharing` one meeting at a time
(`refused_meetings` and `refused_started_meetings`: job, title, room,
start); `POST operator/shares/dismiss` (USER) records that the person
dismissed one, by person and meeting, and hides it from nobody else. Every recording share carries a note
naming the meeting, its date and its length ("Cassini meeting recording —
Weekly sync, Friday 30 October 2026, 10:00 UTC (42 min). Open it in
Cassini."), so the file is recognisable in Files and the share email, whose
heading shows only the file's name, says what it is; the notification itself
does not show the note. The note is in English for every recipient.

Nextcloud's sharing settings apply to the `cassini` account like to anyone:
"Allow apps to use the Share API", "Exclude some groups from sharing" /
"Limit sharing to some groups", "Restrict users to only share with users in
their groups" (with its ignored groups) and "Allow sharing with groups". The
**Recording sharing** check in Doctor reads them, and `cassini`'s groups, and
names the setting and the remedy when they stop Cassini sharing recordings.
With the Share API off, the storage check itself stops at the share API, so
the **Recording storage** row names that setting instead.

Nextcloud limits how many shares one account may create: 20 in any 10
minutes, for every account, refused attempts included. Cassini creates every
recording share as `cassini`, so a meeting with more than 20 recipients, or
several meetings ending together, reach that limit. Cassini then publishes
anyway and records who the limit held back; the run says so and Doctor
counts those meetings. Nobody in that list is told they were refused,
because they were not. **Re-share with the room** on the run shares with them
once the window has passed. An administrator can raise the limit with Nextcloud's `ratelimit_overwrite`
system setting for `files_sharing.shareapi.createshare` (it applies to every
account's share creation):

```sh
occ config:system:set ratelimit_overwrite files_sharing.shareapi.createshare user limit --value=1000 --type=integer
occ config:system:set ratelimit_overwrite files_sharing.shareapi.createshare user period --value=600 --type=integer
```

Imports are not covered yet: an import that reaches the limit fails, with
the limit named in the error.

Under "Restrict users to only share with users in their groups", Nextcloud
also removes shares on its own. When an account leaves a group, Nextcloud
deletes every person-to-person share between that account and anyone it no
longer shares a group with, both directions, at once and without a
notification. If `cassini` leaves a group, every recording shared with
people reachable only through that group disappears from their lists; the
run still records the meeting as shared. **Re-share with the room** puts the
shares back where Nextcloud still allows them. Group shares themselves are
not removed. Doctor's advice to add `cassini` to a group says to keep it
there, because removing it later removes people's access this way.

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
