# Operator migrations

Each migration is a pair `NNNN_name.up.sql` / `NNNN_name.down.sql`, numbered
contiguously. `schema_migrations` records every applied migration by number
and name, and the operator refuses to start when a number was applied under a
different name.

To add a migration:

1. Take the number after the newest file here.
2. Write the up and down SQL, and a down-and-up test in `migrations_test.go`
   (listed in `TestNewestMigrationHasARollbackTest`).
3. Write its full name (e.g. `0025_attempt_logs`) to `LATEST`.

Two branches that add a migration both edit `LATEST`, so git reports the clash.
When that happens on a rebase or merge, renumber your migrations after the ones
from `main`, and write the newest one to `LATEST`. Any database that ran your
migrations under the old numbers must be recreated: the operator will refuse to
start on it.
