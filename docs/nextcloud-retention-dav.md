# Whole-meeting eviction validation

Run operator tests with `cd cassini-operator && go test ./...`, retention UI tests
with `npm test --workspace=cassini-app -- src/RetentionPanel.browser.test.ts
src/operator/retention.test.ts`, and both app builds with
`npm run build:all --workspace=cassini-app`.

The deletion tests cover original job/attempt dates, immutable ages, conditional
DELETE, identity/path/ETag conflicts, lost responses, missing files before intent,
restored identities, busy jobs, rerun admission, independent conflict recovery,
unreadable journals and blocked projection rebuilds.

For installed acceptance, use a fresh disposable local-home NC 33.0.9, 34 and 35
matrix with AppAPI and serverinfo. Publish a synthetic meeting through the
operator, retain its structured successful job history and use an old recording
completion date. Do not use an existing user's seed corpus. For each installation:

1. Confirm participant playback, transcript and notes before expiry.
2. Read the deployed ADMIN settings route; keep all container policies forever.
3. Set only `nextcloud.meetings` to one day using the returned revision/ETag.
4. Preview through the deployed route. Verify the original-date deadline,
   retire action, file count/bytes and unchanged active Files content.
5. Run the deployed sweep route and inspect its operation status. Confirm owner
   and recipient Files no longer expose the meeting, Cassini reads fail, and
   lists/search/annotations no longer return its content.
6. Restart the operator and verify the tombstone remains. Restore the original
   managed file identity in the disposable installation, sweep again and verify
   it is removed. Replacements with a different identity must remain untouched.

A harness that instantiates its own runtime does not prove these deployed routes.
An old audio-only/preconverted fixture is not a deletion-only baseline. Record
versions, build commit, settings, preview, sweep, access checks and restart
results without credentials. No installed matrix was available in the split
execution environment; that validation remains outstanding.
