# Bulk annotation updates and D-773

User request: make list selection tagging one database batch and one UI update, while the existing archive workers continue synchronizing individual recordings. Include Linear D-773 in a separate commit clearly naming the issue, then push the work.

Related issue: [D-773](https://linear.app/code-myriad/issue/D-773/a-meeting-only-ever-keeps-one-tag-adding-a-second-reports-success-but), “A meeting only ever keeps one tag: adding a second reports success but never lands.” The issue records `Test`/`test` with different IDs, a reused ID in multiple namespaces, and a viewer duplicate-key guard already implemented on another branch. Do not duplicate that guard.

The previously approved approach is one bulk HTTP request, bounded access checks, one SQLite transaction for all desired documents and their projections, a durable batch receipt, one UI state publication, and wake-ups for both archive workers. Failed validation/access/preparation leaves the batch uncommitted.
