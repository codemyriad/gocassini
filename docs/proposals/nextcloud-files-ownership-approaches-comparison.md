# Cassini recording ownership approaches: synthesis and comparison

Date: 2026-07-29

Inputs:

- [`nextcloud-files-access-and-index.md`](./nextcloud-files-access-and-index.md) — original proposal, unchanged;
- [`nextcloud-files-access-and-index-adversarial-review.md`](./nextcloud-files-access-and-index-adversarial-review.md);
- [`nextcloud-files-per-user-ownership.md`](./nextcloud-files-per-user-ownership.md);
- [`nextcloud-talk-recording-storage.md`](./nextcloud-talk-recording-storage.md).

## Executive conclusion

There is no privacy benefit inherent in “one canonical path, no owner.” It is an institutional storage topology, not a security boundary.

The strongest general conclusions are:

1. keep **one physical artifact** per meeting;
2. do not require one global path unless institutional retention/discovery needs it;
3. normal Nextcloud files have one owner plus shared references, not multiple co-owners;
4. use the Talk-provided recording initiator as owner when the recording is user-custodied;
5. choose frozen user shares or a dynamic Talk room share based on access semantics;
6. use a Team folder only when recordings are institution-custodied and the universal-manager role is accepted;
7. none of these models protects plaintext from a malicious Nextcloud/Cassini infrastructure administrator.

For the requirement as previously stated—**all selected participants at publication, frozen and individually revocable**—the closest fit is:

> recording-initiator-owned file + explicit read-only user shares to the captured local-user set.

For the different product promise—**the recording belongs to the Talk conversation and follows its members**—the closest fit is:

> recording-initiator-owned file + one Talk `TYPE_ROOM` share.

For **institutional records with centralized retention**, the closest fit is:

> ownerless Team folder + advanced ACL, using the live-validated child-deny recipe and ACL-before-data publication.

## The approaches

### A — fixed admin/service-account owner

```text
service account owns every recording
        ├─ shares outward to Alice/Bob
        └─ central fixed tree and retention
```

This is operationally simple and preserves a stable archive, but one account credential exposes every recording. Using the actual `admin` account is strictly worse than a dedicated service account.

### B — recording initiator owns; private until manual share

```text
Alice clicks Record ──► Alice owns file ──► no default shares
```

This is native Talk recording behavior and the strongest application-layer privacy default. It does not satisfy automatic participant access.

### C — recording initiator owns; frozen user shares

```text
Alice owns one file
   ├─ read share → Bob
   └─ read share → Carol

share set is a publish-time snapshot
```

This gives automatic access and precise later revocation while retaining one source artifact. It needs O(N) shares and a well-defined participant snapshot.

### D — recording initiator owns; Talk room share

```text
Alice owns one file ── TYPE_ROOM ──► current local room members
```

This is the most Talk-native collaborative model. One share grants a dynamic set; room removal revokes and a later join grants. It is not a historical snapshot.

### E — ownerless Team folder with advanced ACL

```text
institutional Team folder
   ├─ Meeting A ACL: Alice + cassini manager
   └─ Meeting B ACL: Bob   + cassini manager
```

This gives one global tree, organization-controlled retention and filtered per-user scans. It adds Team-folder provisioning, nuanced inherited ACLs and a central manager that necessarily retains read access.

### F — copy into every participant's home

```text
meeting bytes ─┬─ copy owned by Alice
               ├─ copy owned by Bob
               └─ copy owned by Carol
```

This is true multi-owner-by-copy. It consumes N-times storage, diverges on updates and makes publisher revocation/deletion impossible. It should be rejected unless independent irrevocable copies are explicitly required.

## Comparison

Legend: **strong**, acceptable, weak, **no**.

| Dimension | A fixed service owner | B user owner, private | C user owner + frozen shares | D user owner + Talk room | E Team folder + ACL | F copies |
|---|---|---|---|---|---|---|
| One physical canonical artifact | **Strong** | **Strong** | **Strong** | **Strong** | **Strong** | **No** |
| One fixed global path | **Strong** | No | No | No | **Strong** | No |
| No single account with all-recording read | No | **Strong** at Files-account layer | **Strong** at Files-account layer | **Strong** at Files-account layer | No: ACL manager reads all | Superficially yes |
| Protects from leaked service-account password | No | **Strong** | **Strong** | **Strong** | Weak if service is manager | Strong |
| Protects from leaked Cassini `APP_SECRET` | **No** | **No** | **No** | **No** | **No** | **No** |
| Protects from infrastructure admin | **No** | **No** | **No** | **No** | **No** | **No** |
| Private by default | Policy-dependent | **Strong** | Brief grant window only if sharing succeeds atomically | Brief grant window only if sharing succeeds | Depends on ACL ordering | No after copy |
| Automatic participant access | Via custom shares | No | **Strong** | **Strong** | **Strong** | Strong |
| Grant semantics | Chosen by Cassini | Owner choice | Frozen snapshot | Current room membership | Frozen ACL | Permanent independent copy |
| Revoke one user while keeping room membership | **Strong** | **Strong** | **Strong** | Weak | **Strong** | **No** |
| Later room joiner automatically gains access | Optional | No | No | **Strong** | No | No |
| Later room leaver automatically loses access | Optional | Owner decides | No | **Strong** | No | No |
| User quota/offboarding independence | **Strong** | Weak | Weak | Weak | **Strong** | Weak across N users |
| User agency over source | Weak | **Strong** | **Strong** | **Strong** | Weak | **Strong** per copy |
| Institutional retention/control | **Strong** | Weak | Weak | Weak | **Strong** | Very weak |
| Extra app dependency | No | No | No | Talk already required | Team folders required | No |
| Custom ACL/share automation | Medium | None | Medium/O(N) | Low/one share, but needs OCS act-as-owner | **High** | High |
| Viewer discovery from one known root | **Strong** | Weak | Weak | Medium (`/Talk`, but movable) | **Strong** | Weak |
| Native Talk alignment | Weak | **Exact default** | Medium | **Strong** | Weak | Weak |
| Guest/federated completeness | No | No | No | Partial public-room chat links; no local scan | No | Could copy only to local users |

## Pros, cons and fitness

### A — fixed service owner

**Pros**

- simplest fixed path and retention jobs;
- quota and offboarding are independent of users;
- one source file and easy global cleanup;
- no Team-folder app required.

**Cons**

- service credential is an all-recordings key;
- poor user-custody semantics;
- central account may become a quota and availability bottleneck;
- an `admin` owner unnecessarily combines archive and super-admin risk.

**Fitness:** acceptable for a transitional implementation or explicit institutional archive. Prefer a dedicated service account, never `admin`. Not the best privacy-first model.

### B — user owner, private/manual

**Pros**

- native Talk behavior;
- least surprise for sensitive recordings;
- no automatic disclosure from a participant-list mistake;
- minimal custom code.

**Cons**

- no invited-to-access guarantee;
- collaboration depends on a user action;
- user quota, deletion and offboarding apply.

**Fitness:** best for privacy-first personal custody and meetings where release must be deliberate.

### C — user owner, frozen user shares

**Pros**

- all intended local users can receive access by default;
- one physical artifact;
- individual, auditable shares and precise revocation;
- later room churn does not silently rewrite historical access;
- no archive-wide service-owner credential.

**Cons**

- O(N) share creation/revocation;
- must define and capture the correct set—room members versus actual attendees;
- owner lifecycle and quota remain load-bearing;
- user/recipient path movement complicates discovery;
- guests and federation need another mechanism.

**Fitness:** best match for “publish-time participants, frozen and individually revocable.”

### D — user owner, Talk room share

**Pros**

- one native share operation;
- current room members receive access automatically;
- room removal/leave is automatic revocation;
- Talk UI/chat and Files mounts share the same mechanism;
- source remains under the initiating user.

**Cons**

- native Talk is manual; automatic invocation is a Cassini product divergence;
- later members see historical recordings and leavers lose them;
- individual exclusion while retaining room membership is awkward;
- `share-chat` requires acting as the owner/moderator and a file-ID lookup;
- federated conversations are not handled by the local provider.

**Fitness:** best for “recording belongs to this conversation,” not for frozen attendance grants.

### E — Team folder with ACL

**Pros**

- institution-owned lifecycle, quota and retention;
- one predictable tree and efficient shallow discovery;
- ACL-filtered DAV listings are achievable;
- no user deletion can orphan the archive.

**Cons**

- no privacy from the delegated ACL manager or infrastructure admin;
- Team folders are an extra deployment dependency;
- exact ACL inheritance is subtle;
- safe publishing must apply a child deny before uploading bytes;
- broad mount membership and manager/writer setup are operational requirements;
- the original documented setup recipe is incomplete.

**Fitness:** best for institutional records when centralized custody is a feature, not a privacy defect.

### F — per-user copies

**Pros**

- every recipient has independent custody;
- source-owner deletion cannot remove other copies;
- no later dependency on share availability.

**Cons**

- no canonical object;
- no effective revocation or global erasure;
- N-times storage and backup cost;
- metadata/transcript versions diverge;
- retention and legal deletion become distributed.

**Fitness:** reject for ordinary Cassini sharing. Consider only if independent export is an explicit user action.

## Privacy conclusions by threat

```text
What are we protecting against?

other NC users / accidental sharing
        └─ any correctly implemented model can protect

leaked central recording-account password
        └─ user ownership improves materially

leaked Cassini APP_SECRET
        └─ no model here protects; AppAPI can select another user

malicious NC/Cassini host administrator
        └─ no model here protects; plaintext exists during capture/processing
           and default storage is server-readable
```

A requirement that says “private even from the administrator who controls the infrastructure” changes the project entirely. It requires client-side/E2EE key ownership, privilege-separated processing, aggressive plaintext deletion and a decision about whether server-side transcription is possible at all.

## Index implications

The original sidecar conclusion remains good, but discovery depends on topology.

| Topology | Discovery authority | Main caveat |
|---|---|---|
| Fixed service root | caller-specific shares or a correctly authorized proxy | Do not proxy every read as the service owner |
| User owner + user shares | owner root + received-share enumeration/search | Paths and mounts can move |
| User owner + Talk room | received Talk mounts / share enumeration | `/Talk` mount can be moved by recipient |
| Team folder | caller-scoped `PROPFIND` of fixed mount | ACL traversal and fail-closed child rules |

In every case:

- the file visible to the caller is the authorization fact;
- use Nextcloud `fileid` plus embedded meeting ID for identity;
- use a sidecar or compact leading Opus tags only as metadata acceleration;
- never use a global catalog as an unverified entitlement list.

## Format and Talk integration conclusion

Talk's `/store` is closer to supporting Cassini's portable artifact than the initial decision suggests. On Talk 24.0.3, the same Opus-in-Ogg bytes were:

- rejected as `.opus` with `file_extension`;
- accepted as `.ogg` and stored under the initiating user.

The best long-term native path is to validate and upstream support for `.opus` as another `audio/ogg` extension. Direct WebDAV as the initiating user remains the compatibility option when the `.opus` filename is non-negotiable.

## Decision guide

```text
Must a malicious infrastructure operator be unable to read recordings?
  ├─ yes ─► none of A-F fits; redesign around E2EE/client-held keys
  └─ no
      │
      ├─ Is the recording an institutional record?
      │    ├─ yes ─► E: Team folder ACL
      │    │          (or A only if Team folders are unavailable)
      │    └─ no ─► user owns the source
      │
      └─ What should default access mean?
           ├─ deliberate release only ─► B: owner-private/manual
           ├─ fixed publish-time set ──► C: explicit user shares
           └─ current conversation ────► D: Talk TYPE_ROOM share
```

## Recommended next decision—not implementation

Replace “canonical single path, no single owner” with two explicit product decisions:

1. **Custody:** user-custodied or institution-custodied?
2. **Grant semantics:** private/manual, frozen snapshot, or dynamic room membership?

A sensible default for Cassini's current Talk-driven use case is to carry these two finalists:

- **C: initiator-owned + frozen user shares**, because it matches the previously stated frozen/individually revocable requirement;
- **D: initiator-owned + Talk room share**, because it is simpler and most closely matches Talk's collaboration model.

Keep **E: Team folder ACL** as the institutional-retention alternative, not as the presumed privacy winner.

Whichever option is selected, separately mitigate the AppAPI and host-level risks:

- isolate `APP_SECRET` from recorder workers where possible;
- never accept an arbitrary target user ID from an untrusted request;
- act only as the authenticated caller or the persisted Talk owner;
- audit AppAPI impersonation logs;
- define deletion/retention for Cassini's local `.run`, `.meeting`, `.opus` and attempt history;
- avoid using the real Nextcloud `admin` account as a file owner.
