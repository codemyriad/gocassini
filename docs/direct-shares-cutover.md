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

This release does not migrate archives from older permission modes. For an
existing one-user installation:

1. Keep the `cassini` account, and back up its Files and the operator volume.
2. Sign in as `cassini` (use `occ user:resetpassword cassini` if needed).
3. Move the old `.opus` files into `CassiniRecordings/meetings` in that account's
   Files. Check for duplicate names before moving. Files in a former Team folder
   must first be copied into this private folder.
4. Share each imported file with its intended Nextcloud users, then check its
   listing and playback as one recipient and one outsider. Existing file shares
   may survive a move within the same account; check them rather than assuming.
5. Retire old broad access only after the new copies and caller reads are
   verified. Apply the instance's trash retention policy to old copies.

A recording without a local metadata row appears with a filename based title
until it is republished. Search and annotation backfills discover recordings
from the private Nextcloud directory, so loss of the local metadata index does
not hide the archive from those rebuilds.
