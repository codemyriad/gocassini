# Nextcloud retention storage contract

Remote retention requires a conditional overwrite of the existing file, followed
by a same-directory MOVE with `Overwrite: F`. DELETE must name the exact current
leaf and carry its current strong ETag. A failed precondition is a conflict, not
permission to retry unconditionally or recreate a file and its shares.

```
original leaf -> conditional PUT -> verify ID and bytes -> conditional MOVE
                                                         |
                                 verify ID and bytes <---+
```

Transport success alone does not prove preservation. The operation journal must
record intent before mutation and settle lost responses by independently reading
the old/new paths, file identity and bytes. Only caller-authorized Nextcloud reads
may serve content. Remote hrefs are validated against the exact requested leaf;
mutation targets are constructed locally. Credential-bearing requests do not
follow redirects.

Run the synthetic installed probe against the development harness:

```sh
./bin/cassini dev stack up --resume --services appapi \
  --cassini installed-exapp --recording-backend none --build
harness/bin/validate-retention-dav.py
```

The probe creates disposable users, group and files, uses the installed ExApp's
act-as-user credentials, checks stale mutations, collisions, file identity,
direct/group/Team/downstream shares, public-link attributes, recipient rename and
revocation, then deletes its own users and group. It never uses seed recordings.
`RETENTION_PROBE_URL` and `CASSINI_EXAPP_CONTAINER` select another disposable
installed harness. Credentials are read in memory and are not printed.

The isolated matrix in `harness/bin/validate-retention-matrix.py` repeats these
checks against the pinned Nextcloud 33.0.9, 34.0.0 and 35.0.0 baselines. It also
checks public-password access, unchanged download-restriction behavior, a version
restore, AppAPI route upgrade, concurrent annotation recovery, the actual sweep
and the installed headless viewer. `validate-retention-lifecycle.sh` uses the
production Go lifecycle and freshly built CLI against the installed AppAPI DAV.

File-ID equality alone does not establish effective permission equivalence for a
rule depending on MIME type or filename. The runtime capability check rejects
unverified storage/access-control configurations; see
[Nextcloud retention](nextcloud-retention.md#supported-storage-and-access).

Cassini does not purge Nextcloud history/trash or change instance retention
configuration. Active-file removal is not a measurement of physical reclaimed
storage.
