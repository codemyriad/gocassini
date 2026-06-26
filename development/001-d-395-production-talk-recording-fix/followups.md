# D-395 — Follow-ups

- **Production upgrade mechanics:** Write an ops-specific runbook once the actual production AppAPI install path is known (App Store UI vs local info.xml vs release package). D-395 documents the invariant but does not automate production data-preserving reinstall.
- **CI coverage:** Consider promoting `harness/bin/validate-installed-exapp-private-talk.sh` into a scheduled or opt-in CI job. It is heavier than the current direct-container Talk roundtrip.
- **Storage/product model:** Keep Cassini AppAPI persistent storage authoritative for now. Nextcloud-Files-native rich artifacts, per-owner archive ACLs, and viewer sharing belong in D-416/D-265-class work.
- **Harness cleanup ergonomics:** `manual-test-setup.sh` intentionally resets the local AppAPI harness. If repeated no-reset local redeploys become important, add an explicit data-preserving mode and test it separately.
- **Portable transcript assertion:** The validator treats positive portable `segmentCount` as transcript evidence. If/when all published entries expose directory artifacts with `transcript.display.v1.json`, tighten the helper to fetch and inspect the display transcript for every job.
