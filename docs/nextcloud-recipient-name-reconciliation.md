# Experimental audio-only retention: remaining recipient-name work

This downstream branch preserves audio conversion but is not ready for PR 2.
Do not open PR 2 until recipient-name reconciliation is implemented and validated.
The owner file becomes `.cassini.transcription.json`; received share mount names
can remain `.opus` while serving JSON. Renaming is not implemented in this branch.

Preserve the file, shares and ACLs. Unsharing/recreating loses onward sharing,
custom recipient paths and recipient public links. Public-room publication asks
for permissions 17 (read + reshare), falling back to 1; private-room publication
asks for 1. The viewer accepts current OCS received shares for owner `cassini`,
matching file ID and read permission, including onward shares.

AppAPI can act as each local user using the existing app credential. A recipient
DAV MOVE changes that user's incoming mount name; direct share IDs and permissions
remain, and group shares may use per-user naming overrides. Public links have no
local mount. Federated remote users cannot be renamed with a local act-as request.

```text
owner conversion completes -> durable pending name reconciliation
    -> group affected files by local recipient
    -> fetch current received shares once per recipient
    -> MOVE each stale matching mount, without overwrite
    -> verify identity and record completion or retry conflict
```

Owner-side OCS `GET .../shares?path=...&reshares=true` discovers downstream grants
without walking Alice -> Bob -> Carol. The current `ownerSharesForPath` helper
does not request `reshares=true`. Group/Team grants also need member discovery;
received-shares responses supply personalized current paths. Deduplicate users
with overlapping direct/group/Team access.

OCS has no bulk rename in the inspected paths. Batching reduces repeated discovery
and bounds load; it still requires one DAV MOVE per affected file mount, followed
by verification. Keep this off the eviction critical path, rate-limit discovery
and MOVEs, bound concurrency, preserve custom folders/basenames, refuse collisions
and persist retry state. Include previously converted documents and newly arriving
group members. Repair on Cassini access can supplement background work but misses
Files-only users.

Before PR 2, validate direct, group, Team and downstream grants; personalized
paths; overlapping access; new members; revoked access; collisions; restarts;
public links; and ACL equivalence across NC 33-35. Also validate actual installed
settings save, preview, sweep and Files/Cassini access with fresh synthetic
fixtures. A harness-created runtime or preconverted browser fixture alone does
not cover that deployed flow. Keep the user's seed corpus untouched.
