# Per-meeting ownership (D-547 Option B): 1-1 custody, custodial roles, and owner succession

Date: 2026-07-30

Status: follow-up investigation to the ownership comparison; live-verified on the local NC 34.0.0 harness

Inputs:

- [`nextcloud-files-ownership-approaches-comparison.md`](./nextcloud-files-ownership-approaches-comparison.md) — the comparison this follows up on;
- [`nextcloud-files-per-user-ownership.md`](./nextcloud-files-per-user-ownership.md) — the per-user-ownership exploration (policies U1/U2/U3);
- D-547 — the formalised A-vs-B ownership decision; this doc examines **Option B** (per-meeting recorder-owner) edge cases.

Terminology bridge: D-547's "Option B" is the user-custody family of the comparison doc — one recorder-owned artifact plus a grant policy chosen from **U1** (owner-private), **U2** (frozen user shares) or **U3** (Talk `TYPE_ROOM` share). The reservations below are examined against that family, not against a single fixed policy.

## The three reservations

1. **1-1 superior–subordinate:** the superior records for their own purposes and does not share. Is the model coherent?
2. **1-1 colleague–colleague:** equals, both want durable custody of the recording. Who owns it, and can the non-owner survive the owner deleting it?
3. **Custodial roles (team lead, regional manager, manager's assistant):** the custodian owns the archive for their tenure, shares to relevant parties, feeds transcriptions into a downstream business-information system. When they leave the position or the company, can an admin reassign ownership and permissions in NC Files?

All three were checked live on the harness Nextcloud 34.0.0 (2026-07-30); the verification log is at the end.

## 1 — 1-1 superior–subordinate: clear under U1

This is the trivially supported case and needs no machinery beyond the model's default. Owner = the user who clicked record (Talk supplies `start.owner`; in a 1-1 conversation both members are moderators, so either may record). With policy U1 the file lands owner-private; nothing is granted unless the owner deliberately shares.

```text
superior clicks Record ──► superior owns file ──► no shares
                                      │
                                      └─ (optional, deliberate) share to subordinate later
```

One product note: if Cassini's Option B automation defaults to auto-`share-chat` (U3), this case needs an opt-out — "record privately" must remain expressible, either as a per-room policy or by the owner revoking the auto-created room share afterwards. Native Talk's own default (U1, notify-then-share) already matches this case exactly.

## 2 — 1-1 colleague–colleague: ownership is asymmetric, custody need not be

### Who owns it

Nextcloud cannot make them co-owners — a normal file has exactly one owner plus shared references (see the per-user-ownership doc). Talk also already answers the question mechanically: **the owner is whoever clicked Start recording**, and only one recording runs per call. Trying to negotiate a "fairer" owner at publish time invents a policy Nextcloud can't represent.

The productive reframing: the *owner* is asymmetric, but *durable custody* can be symmetric, because the non-owner can take a *point-in-time copy* of anything shared with them. That converts "who owns it?" from a blocking design question into a self-service action.

### Live-verified: the non-owner's point-in-time copy

Both grant mechanisms were exercised: a plain read-only user share (U2) and a Talk room share into a real 1-1 conversation (U3, the D-547 Option B mechanism).

```text
alice records ──► alice owns meeting.opus
                     │ share (U2 user share, read-only)  or  (U3 TYPE_ROOM into the 1-1 room)
                     ▼
   bob sees a received mount:  /meeting.opus   or   /Talk/meeting.opus   (owner-id: alice)
                     │
                     │  WebDAV COPY  →  /MyRecordings/meeting.opus
                     ▼
   bob's copy: owner-id bob, new fileid, full permissions, bytes identical (checksum match)
                     │
   alice deletes her source ──► bob's received mount: 404
                                bob's copy:           200  ← custody survives
```

Findings, each confirmed live:

- A **read-only** share (`permissions=1`) is sufficient: the recipient's `COPY` from the received mount into their own tree returned `201`. The copy is owned by the recipient, gets a new `fileid`, full permissions, and byte-identical content.
- The same `COPY` works from the `/Talk` mount produced by a room share in a 1-1 conversation — so the copy right holds under Option B's native `share-chat` flow, not just under explicit user shares.
- After the owner deletes the source, the recipient's received mount 404s but the copy is untouched. Owner deletion is *revocation of the reference*, never *reach into the recipient's home*.
- The copy is server-side (storage-to-storage): no download/re-upload round trip, but it does consume the recipient's quota.

### Caveats that shape the product promise

- **The `download=false` share attribute defeats the copy.** A share created with `attributes=[{"scope":"permissions","key":"download","value":false}]` made the recipient's `COPY` fail with `403` (notably, a raw DAV `GET` still returned the bytes on NC 34 — the attribute is not a watertight exfiltration control, it just breaks the polite path). Consequence: if "the non-owner can keep a copy" is a product promise, Cassini's share automation must never set `download=false`; conversely it cannot be promised for recordings a cautious owner shared with download hidden.
- **Default room-share permissions are too broad.** The `shareType=10` share created without an explicit `permissions` parameter came back with `permissions=19` and the recipient's mount showed a writable permission string (`SRGDNVW`) — the 1-1 counterpart could modify the owner's recording. Option B's automation should pin recording shares to read-only explicitly (verify live when implementing; only the too-broad default was exercised here).
- **Copy only what you point at.** `COPY` of the `.opus` does not bring a manifest/transcript sidecar along. If meetings publish as a per-meeting folder, "keep a copy" should copy the folder; if flat, it must copy each artifact.
- **Viewer identity:** the copy is a second physical artifact with the same embedded meeting ID and a different `fileid`. The per-user scan will find both for a user who copied. The viewer needs a stated dedupe policy — recommend keying the list on embedded meeting ID and treating (shared instance, own copy) as one meeting with two custody paths, preferring the share while it exists.

### Recommendation for the colleague case

Keep owner = recording initiator (Talk's answer). Give the peer durable custody as an explicit action, not automatic replication:

- a **"Keep a copy"** viewer affordance — a server-side `COPY` performed as the authenticated caller from their received mount into their own recordings folder (one WebDAV call; Cassini already acts as the caller for scans);
- this is precisely the comparison doc's carve-out for approach F: per-user copies are acceptable *as an explicit user export*, unacceptable as the default sharing model.

This answers the reservation directly: yes, user B can easily make a point-in-time copy after the recording is shared, with read-only grants, under both U2 and U3 — and it survives user A's later deletion.

## 3 — Custodial roles and succession

### The role during tenure

The team-lead case is Option B with a standing custodian instead of an ad-hoc recorder: the custodian's account accumulates the recordings for their org unit, they share outward, and downstream systems consume via ordinary shares.

```text
                 team lead (custodian) owns  /Recordings/…
                        │
        ├─ U2 user shares ─────► relevant parties (frozen, individually revocable)
        ├─ U3 room shares ─────► the team conversation(s)
        └─ user share ─────────► BIS service account  (reads transcripts over WebDAV
                                                        for day-to-day operations)
```

For the custodial promise, **U2 (frozen user shares) fits better than U3**: grants to "relevant parties" are deliberate and auditable, and — decisive for succession — outgoing shares survive an ownership transfer (verified below). The downstream feed is just one more outgoing share, to a BIS service account, so it inherits the same survival property.

### Succession: two levers, both real

**Self-service (position handover, person still present):** Personal settings → Sharing → "Transfer file ownership" lets the outgoing custodian pick a folder and a recipient; the recipient accepts via notification. Right tool for a planned handover; not live-tested here (documented NC feature; the admin path below exercises the same underlying transfer service).

**Admin (offboarding, person gone):** `occ files:transfer-ownership [--path=<folder>] <source> <target>`. This is the answer to "can admin reassign ownership and permissions": yes, and the live check on NC 34 confirmed the properties that matter, including the case where the source account is already disabled:

```text
occ user:disable alice                       (offboarding first step)
occ files:transfer-ownership --path=Recordings alice carol
        │
        ▼
carol:/Transferred from Alice on 2026-07-30…/Recordings/
        ├─ meeting-nodl.opus   fileid 356 (unchanged), owner-id carol
        └─ meeting-1on1.opus   fileid 357 (unchanged), owner-id carol
        │
        ├─ user share to bob:        same share id, uid_owner alice → carol, bob's mount uninterrupted
        └─ TYPE_ROOM share (1-1):    same share id, uid_owner alice → carol, bob's /Talk mount uninterrupted
```

Verified properties:

| Property | Live result |
|---|---|
| Transfer from a **disabled** account | Works (`user:disable` then transfer succeeded) |
| File identity | `fileid`s preserved across the transfer (and across a later `MOVE`) — a `fileid`-keyed index survives; any path-keyed assumption breaks |
| Outgoing **user shares** | Re-owned in place (same share id, `uid_owner` → successor); recipient access uninterrupted |
| Outgoing **Talk room shares** | Also re-owned and functional — even into a 1-1 conversation the successor is not a member of |
| Successor can **revoke** | Deleted both the user share and the room share; recipients' mounts 404'd immediately — including the room share for a room the successor cannot see |
| Successor can **reorganise** | `MOVE` of the transferred folder to a clean path broke nothing for recipients (shares follow the node, not the path) |
| **Incoming** shares | Do **not** transfer: a file shared *to* the outgoing custodian did not reappear for the successor |
| Deletion without transfer | Destructive: deleting an owner account removed the file and every recipient's mount (verified with a throwaway user) |

### Consequences for the runbook and for Cassini

- **Offboarding order is load-bearing: disable → transfer → verify → delete.** Never delete the account first — there is no share-preserving recovery after `user:delete`.
- **Transfer fixes the past, not the future.** New recordings still target whoever Talk names as `start.owner`. The custodial role change must also be applied where the custodian is configured (Talk room moderators, any Cassini standing-recording policy / `talk_binding`-derived owner) or the archive forks between predecessor and successor.
- **Incoming shares need a sweep.** Anything colleagues had shared *to* the outgoing custodian (e.g. their own copies, escalated recordings) must be re-shared to the successor by the original owners; the transfer will not do it. Cassini can enumerate the outgoing custodian's received shares before deletion to produce the re-share worklist.
- **Room-share audit is awkward for the successor.** They own shares whose target renders only as `private_conversation_<hash>`; revocation works, but attribution ("which meeting's conversation is this?") needs the recording's embedded meeting ID / sidecar, not the share listing.
- **Path churn is expected.** The transfer lands under `Transferred from <name> on <date>/…` and the successor will reorganise. The viewer must already tolerate moved sources under Option B (owners can move files anytime); succession just makes it certain. Discovery must remain scan/share-enumeration based with `fileid` + embedded meeting ID as identity — never a remembered path.
- **BIS continuity is free if the feed is an outgoing share.** The share to the BIS service account transfers with ownership like any other outgoing share, so the downstream pipeline keeps reading across a succession with no re-grant.

### The honest boundary

Succession-by-transfer makes Option B *survivable* for custodial roles, but a role whose archive is *designed* to outlive any person is drifting toward institution custody — the comparison doc's approach E (Team folder + ACL), where succession is a group-membership edit instead of a data migration. The decision guide's custody question stands: pick B + the succession runbook when recordings are personally custodied and succession is exceptional; if succession is routine and the archive is an org record, that is the E signal, not a B edge case.

## Summary against the reservations

| Reservation | Verdict under Option B |
|---|---|
| 1-1 superior–subordinate, private recording | Native fit (U1); ensure auto-share has an opt-out |
| 1-1 colleagues: who owns? | Whoever clicked record (Talk decides); don't fight the single-owner model |
| 1-1 colleagues: can B keep a copy if A might delete? | **Yes, verified** — read-only share suffices for a server-side `COPY`; copy survives A's deletion; blocked only if the share sets `download=false` |
| Custodian shares + downstream BIS feed | Ordinary outgoing shares (U2 + service-account share); both survive succession |
| Custodian leaves: admin reassigns ownership & permissions? | **Yes, verified** — `occ files:transfer-ownership`, works on disabled accounts, re-owns outgoing user *and* room shares in place, successor gains full manage/revoke; incoming shares and future-recording policy need explicit handling |

## Verification log (NC 34.0.0 harness, 2026-07-30)

Users: `alice` (owner/outgoing custodian), `bob` (peer/recipient), `carol` (successor), `dave` (throwaway). All state created for the check and cleaned up afterwards.

1. Read-only user share `alice→bob` (`permissions=1`); bob `COPY /files/bob/meeting-x.opus → /files/bob/MyRecordings/` → `201`; copy props `owner-id=bob`, new fileid, `RGDNVW`; MD5 of copy == source. Alice `DELETE` source → bob's mount `404`, bob's copy `200`.
2. Share with `attributes=[{scope:permissions,key:download,value:false}]`: bob `GET` → `200`, bob `COPY` → `403`.
3. 1-1 Talk room `alice↔bob` (`roomType=1`); `shareType=10` share of `meeting-1on1.opus` (first attempt raced room creation and 404'd; retry `200`, `permissions=19`); bob's `/Talk/meeting-1on1.opus` mount `SRGDNVW`; bob `COPY` from the mount → `201`, copy `owner-id=bob`.
4. `occ user:disable alice`; `occ files:transfer-ownership --path=Recordings alice carol` → files under `Transferred from Alice on 2026-07-30 10-03-06/Recordings/`, fileids 278/356/357 preserved, `owner-id=carol`; shares 2 (user) and 3 (room) kept their ids with `uid_owner=carol`; bob's two mounts stayed `200`.
5. Carol `MOVE` the transferred `Recordings` to `/Recordings` → `201`; bob's mounts still `200`. Carol `DELETE` share 2 → bob's user-share mount `404`; carol `DELETE` share 3 (room she is not in) → bob's `/Talk` mount `404`.
6. Incoming share `bob→alice` created before the transfer did not appear in carol's received shares after it.
7. `dave` uploads + shares to carol (`200` mount), `occ user:delete dave` → carol's mount `404`.
