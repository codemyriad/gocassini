# Recording access with Nextcloud shares

Cassini writes each new `.opus` recording into the `cassini` account's private
`CassiniRecordings/meetings` folder. It creates Nextcloud Files shares for the
local accounts, groups and Teams captured from the Talk room, including people
invited who did not join the call. Guests without a Nextcloud account receive no
share. A public Talk room adds reshare permission to recipients, including
groups and Teams, when this Nextcloud allows it. Recipients can then create
public links if the instance permits them; Cassini does not make one itself.

Cassini lists meetings from the caller's **current Nextcloud shares**. Each audio
or annotation read goes through Nextcloud as that caller, so removing a share
removes access. Nextcloud's own downstream reshare behavior applies. Cassini
keeps meeting card metadata in a local SQLite index; that index never grants
access. The installed app writes no `catalog.json` to Nextcloud Files. The
standalone static exporter still makes one for static hosting.

## Existing recordings

This release does not migrate archives from older permission modes. Bring old
recordings in through meeting import (D-828) rather than by hand:

- In the viewer, Browse → **Import**: drop the `.opus` (or transcription-only
  `.json`) files, choose who each meeting is shared with, and import.
- On the server, `cassini-operator import-meeting --as <user>
  [--share-with user:a,group:b] <file>...` does the same from local paths —
  the recovery path for files restored from a backup.

Either way each file is validated by the cassini CLI, delivered byte for byte
into `CassiniRecordings/meetings` under a fresh local id, filed under the one
**Imported meetings** room (never the room it was recorded in — that room
belongs to the source installation), indexed for listing, search and tags,
adopted into retention with the meeting's own dates as its age, and shared — the importer first, then the selection, with reshare
permission where the instance allows it. The staged upload in the importer's
own Files is removed once the import lands.

A recording placed in the folder by hand still works for recipients of its
shares, but it has no import record: retention will not manage it, and it
lists with a filename-based title until `import-meeting-metadata` restores its
description. Prefer the import paths above. Search and annotation backfills
discover recordings from the private Nextcloud directory either way, so loss
of the local metadata index does not hide the archive from those rebuilds.
