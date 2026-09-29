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
direct/group/downstream shares, public-link attributes, recipient rename and
revocation, then deletes its own users and group. It never uses seed recordings.
`RETENTION_PROBE_URL` and `CASSINI_EXAPP_CONTAINER` select another disposable
installed harness. Credentials are read in memory and are not printed.

This is a mechanics test, not full feature certification. Teams, download
restrictions, public-link downloads, file-access-control plugins, alternative
storage backends, recovery history, fault injection and the supported-version
matrix require additional acceptance coverage before destructive retention can
be enabled. File-ID equality does not establish effective permission equivalence
for a rule depending on MIME type or filename.

Cassini does not purge Nextcloud history/trash or change instance retention
configuration. Active-file removal is not a measurement of physical reclaimed
storage.
