# Direct shares cutover (one-user installation)

The installed Cassini app now uses one Nextcloud Files sharing model. New `.opus` recordings are written to the private `cassini/CassiniNoACL/Recordings/meetings` directory and shared with the local people, groups and Teams captured from the Talk room. Cassini does not write a remote `catalog.json`. The separate static-site exporter still writes one for static hosting.

## Existing recordings

Older files have no dependable saved recipient list. For this installation, use the one account that should read the archive as their explicit recipient. Do not infer past audiences from today's Talk roster.

1. Back up the `cassini` account's Files and the Cassini operator volume. Keep the `cassini` account; deleting it deletes the recording archive.
2. If the account cannot be opened in Files, set a temporary password with `occ user:resetpassword cassini` and sign in as `cassini`.
3. In `CassiniNoACL/Recordings/meetings`, share each existing `.opus` with the chosen user using Nextcloud Files. These files need no copy. Leave the old `catalog.json` alone until the cutover is checked; the new app does not read it.
4. If recordings are still in the former `Cassini/Recordings/meetings` Team folder, copy each `.opus` into `CassiniNoACL/Recordings/meetings` as `cassini`. Verify the copy's bytes and open it as the chosen user after adding its direct share. Do not overwrite an existing name without comparing the files.
5. Check that the chosen user sees and plays the recordings in Cassini. A file whose local metadata row was lost appears with a filename-based title until republished; the original filename is recovered from the private archive even if the recipient renamed their share mount.
6. Remove the old Team folder's broad access only after the new copies and caller reads are verified. Handle its trashbin under the instance's retention policy. The new app's Settings page reports how many recordings remain in that former folder.

Nextcloud's current shares decide each caller's meeting list. A Cassini metadata row never grants access, and every audio read uses the caller's Nextcloud identity. A recipient can remove a share in Files. Republishing a recording after a successful publish preserves later share edits.

On Nextcloud 34, removing an original direct share did not remove a downstream reshare that recipient had already made. The downstream recipient still had a current Nextcloud share and DAV access. Remove that downstream share too when access must end for everyone in that branch; Cassini follows Nextcloud's live share state.

## Scope and scale

This cutover is deliberately manual for the current one-user installation. It does not try to migrate arbitrary historical ACLs. The active app has one access model and no Team Folders or Everyone Group app prerequisite.

The local `meetings.sqlite3` sidecar stores derived list metadata by Nextcloud file ID. It uses the SQLite dependency already bundled by the operator. A normal list reads one fresh `shared_with_me` result and joins its IDs locally; it does not fetch each `.opus`. If the sidecar is missing, the owner archive is enumerated once to recover original names and minimal cards. Detailed titles and content badges for those old files require republishing. Media range requests use a short path cache, while Nextcloud checks each GET as the caller. The viewer refreshes less often and renders meeting rows in batches of 100.

The `shared_with_me` response still grows with all Nextcloud shares of the caller. Cassini pagination cannot remove that cost. Benchmark 100, 1,000 and 3,000 meetings on a real instance before promising a latency target at those sizes.
