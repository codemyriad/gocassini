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
cannot be made for the meeting starter, publication fails. An individual
recipient share refusal is reported as a warning and other recipients remain
accessible. Outages and malformed share responses stop publication.

The installed app creates no remote `catalog.json` and needs neither Team
folders nor the Everyone Group app. The local static exporter still uses
`catalog.json` to build a portable static site.

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
