# Adversarial review: Nextcloud Files access topology and meeting index

Date: 2026-07-29

Review target: [`nextcloud-files-access-and-index.md`](./nextcloud-files-access-and-index.md)

Scope: challenge the proposal's premises, especially “one canonical path, no single owner,” rather than only checking its internal consistency.

The reviewed proposal is intentionally left unchanged.

## Verdict

The proposal's indexing conclusions remain useful, but its access recommendation is not ready to be treated as the default architecture.

The central problem is that three independent properties were bundled into one requirement:

1. one canonical **artifact**;
2. one canonical **path**;
3. no user **owner**.

Only the first is clearly necessary. Nextcloud can keep one physical file while exposing shared mounts to many users. A stable global path is convenient for the producer, but it is not required for access control or viewer discovery if files are identified by `fileid` and discovered through Nextcloud. Eliminating a user owner also does not eliminate a privileged universal reader.

The adversarial conclusion is:

> Do not accept “canonical shared tree with no owner” as an invariant. Treat user-owned files plus native Nextcloud shares as a first-class candidate, and choose between user custody and institutional custody based on product policy—not filesystem neatness.

## Findings

### F1 — Critical: the stated privacy concern uses the wrong threat boundary

Changing the file owner protects against some actors, but not against a maintainer who controls the Nextcloud or Cassini infrastructure.

```text
ordinary NC user / leaked service-account password
                    │
                    │ Nextcloud Files permissions are meaningful here
                    ▼
       Nextcloud application authorization boundary
                    │
       ┌────────────┴────────────────┐
       │ Cassini APP_SECRET          │ host/container/root access
       │ can select any NC user ID   │ can read storage, keys and Cassini work files
       └────────────┬────────────────┘
                    ▼
       outside the protection offered by file ownership
```

Three independent facts matter:

- In AppAPI 34, an enabled ExApp request supplies `base64("<userId>:<app-secret>")`. AppAPI validates the app secret, looks up the supplied user ID, and installs that user in the session. The DAV plugin accepts the same identity. Therefore a leaked Cassini `APP_SECRET` is not equivalent to a low-privilege service-account password; it is an arbitrary-user impersonation credential.
- Without Nextcloud server-side encryption, user files are ordinary bytes in the data directory. In the live check described below, the raw file under `data/alice/files/...` had the same SHA-256 as the uploaded Ogg Opus file.
- Cassini itself retains plaintext capture and derived artifacts under its work and published roots. Per-user ownership of the final Nextcloud file does not hide `current/<job>.run`, retained attempts, the portable `.opus`, or backups from a Cassini host administrator.

Nextcloud's own encryption documentation is explicit: server-side encryption (SSE) does not protect against a compromised Nextcloud server or malicious administrator; E2EE is the mechanism for that threat. User-key SSE offers limited additional resistance, but it does not turn a live server controlled by an adversary into a trusted execution environment.

**Consequence:** per-user ownership can reduce accidental exposure and credential blast radius, but neither per-user ownership nor a Team folder provides confidentiality from an infrastructure administrator. If that is a product requirement, this proposal family is unfit without a separate client-side/E2EE architecture and deletion of Cassini's retained plaintext.

### F2 — High: “ownerless” Team-folder storage still has universal readers

The recommended Team-folder shape removes a user from the `uid_owner` field, but it does not remove centralized privilege:

- Team-folder administrators can manage the folder.
- A delegated ACL manager receives at least read access in a default-deny Team folder.
- The ACL DAV plugin refuses a rule update that would remove the acting ACL manager's own read permission.
- Cassini's AppAPI secret can separately impersonate arbitrary users.
- The underlying files remain in `data/__groupfolders/<id>/files/` by default.

The live ACL check confirmed that the delegated `cassini` account could read **both** test meetings while Alice and Bob each saw only their granted meeting.

```text
Team folder: no user uid_owner
             │
             ├── Alice: Meeting A only
             ├── Bob:   Meeting B only
             └── cassini ACL manager: Meeting A + Meeting B
                                      ^
                                      universal reader still exists
```

This may be acceptable for institutional custody, but it does not answer the privacy objection to a service account that can see every recording. It changes the account's role from “owner” to “manager” while preserving the sensitive capability.

### F3 — High: canonical object, canonical path and custody were conflated

These are separate design choices:

| Property | Benefit | Is it required? |
|---|---|---|
| One physical artifact | No divergent copies; one deletion/update target | **Yes** |
| One globally fixed path | Easy producer code, retention jobs and shallow scans | Convenient, not inherently required |
| One institutional custodian | Stable retention, quota and offboarding | Product-policy choice |
| No user owner | Avoids tying lifecycle to one user | Product-policy choice; not a privacy guarantee |
| Per-user shared mounts | Native user visibility and sharing UI | Compatible with one physical artifact |

A normal Nextcloud share is a reference to one source node. Recipients do not become co-owners and no byte copy is required. Therefore rejecting user ownership because it allegedly destroys the “single canonical artifact” is a false dichotomy.

A path is also weaker than a node identity in Nextcloud: owners can move files and recipients can move their own share mounts. The more durable identity is the file/node ID plus Cassini's embedded meeting ID.

### F4 — High: the proposed Team-folder recipe is incomplete and unsafe as written

A live Nextcloud 34.0.0 / Team folders 22.0.5 check resolved the proposal's open S1 and found material differences from the documented recipe.

#### The exact leaf-only recipe fails

With a default-deny Team folder and only `+read` for Alice on a nested meeting:

| Operation as Alice | Result |
|---|---:|
| `PROPFIND` Team-folder root | `403` |
| direct GET of the known nested `.opus` | `404` |

Read on the leaf does not make its ancestors traversable. The proposal's suggestion that a known leaf URL would “likely” work was not borne out.

#### A working recipe needs an explicit deny and an always-reading manager

This recipe did produce correctly filtered DAV listings:

```text
root:
  recording-viewers group  +read
  cassini user              +read +write +create +delete +share

Meeting A directory:
  recording-viewers group  -read -write -create -delete -share
  alice user                +read
  cassini user              +read +write +create +delete +share

Meeting B directory:
  recording-viewers group  -read -write -create -delete -share
  bob user                  +read
  cassini user              +read +write +create +delete +share
```

Result:

| Caller | Root listing | Direct reads |
|---|---|---|
| Alice | Meeting A | A=`200`, B=`404` |
| Bob | Meeting B | A=`404`, B=`200` |
| Eve | empty root | A=`404`, B=`404` |
| `cassini` manager | A and B | A=`200`, B=`200` |

This works because root read makes the collection traversable, the per-meeting group denial hides each child by default, and a same-path user allow overrides that denial for an authorized user.

#### The setup commands in the proposal are also wrong/incomplete

- `--acl-no-default-permission` is an option of `groupfolders:create`, not `groupfolders:permissions`.
- `groupfolders:group <id> recording-viewers` defaults to read-only. If `cassini` is only a member of that group, the mount-level permission ceiling prevents it from creating files. A writer mapping with `read write delete share` or a separate writer group is required.
- A delegated manager starts with read, not create/update. Cassini needs an explicit writer rule at the root before `MKCOL`/`PUT`.

#### Publish ordering is fail-open in the documented flow

The proposal orders operations as `MKCOL → PUT opus → PUT sidecar → PROPPATCH ACL`. With root read granted for traversal, every broad-group member can read a new child until its per-meeting denial is installed. The live check showed all Alice, Bob and Eve listing both new meetings before the child ACLs were applied.

A safe Team-folder publisher must instead create an empty random directory, install the deny-by-default child ACL, verify it as an unauthorized caller, and only then upload sensitive bytes—or publish from a hidden staging area with an atomic move. The proposal's ordering exposes the recording during a race window.

### F5 — High: the proposal discards an owner already supplied by Talk

Talk does not leave recording ownership ambiguous. Its recording controller passes the authenticated user who clicked **Start recording** to the backend as `start.owner`. Cassini already persists that value in `talk_binding` and sends it back to Talk's `/store` endpoint.

The user must be a logged-in moderator/owner to start recording, but the **file owner is the initiating user**, not necessarily the conversation creator and not the moderator who later stops recording.

Inventing a central service owner therefore overrides a native, auditable product decision already present in the protocol. That can still be the right institutional policy, but it needs a positive justification such as retention, legal custody, quota isolation or offboarding—not the assertion that Talk provides no owner.

### F6 — Medium: “frozen at publish” was treated as a requirement without comparing native Talk semantics

Talk's `TYPE_ROOM` share deliberately follows **current conversation membership**:

- a current local-user participant gets the mount;
- removing Bob from the room removed the recording from Bob's `/Talk` listing in the live check;
- re-adding Bob restored the same mount;
- a recipient can unshare the mount from themselves without deleting the source.

A frozen set of per-user shares has different semantics:

```text
frozen snapshot                     Talk TYPE_ROOM
---------------                     --------------
publish-time member gets access     current member gets access
later joiner gets nothing            later joiner gains access
later leaver keeps access             later leaver loses access
individual owner revocation easy     individual exclusion while still in room is awkward
```

Neither is universally better. Frozen access fits historical attendance and legal grants. Dynamic room access fits the mental model “files shared to this conversation.” The proposal selected frozen access ex ante and then optimized around it.

### F7 — Medium: “participant” is underspecified

Talk has at least three relevant sets:

1. conversation members/invitees;
2. users currently in the call;
3. users who attended at any point during the recorded interval.

`GET room/{token}/participants` is primarily a conversation-membership list; an `inCall` field is only a current-state signal. Querying it at publish time does not reconstruct historical attendance. `TYPE_ROOM` also follows conversation membership, not recorded-call attendance.

If the product promise is “everyone who attended the recorded meeting,” Cassini must capture an attendance snapshot during recording. That snapshot can be used only to create native Nextcloud shares; it need not become a second authorization authority.

### F8 — Medium: user-owned and centrally owned models optimize different risks

The proposal scores a central Team folder highly on topology while underweighting custody policy.

User ownership adds real costs:

- the recording consumes the initiator's quota;
- the user can move or delete it;
- account deletion/offboarding needs ownership transfer or retention policy;
- discovery can no longer assume one shared root.

Central institutional storage adds different costs:

- one service/manager principal can read everything;
- a credential or policy mistake has archive-wide blast radius;
- Team folders are an extra app and operational dependency;
- per-meeting ACL correctness and fail-closed publishing are custom Cassini responsibilities.

These are product trade-offs. A topology table should not turn them into “excellent” versus “contradicts the model” before the custody model itself is chosen.

## What survives the review

Several findings in the original proposal remain strong:

- Nextcloud Files/shares should remain the authorization source of truth; Cassini should not invent a parallel ACL.
- A caller-scoped listing is safer than downloading a global catalog and filtering it in Cassini.
- Co-located sidecar metadata is a useful non-authoritative accelerator, with fallback to the `.opus` header.
- Paths are mutable; file IDs and embedded meeting IDs are better identities.
- A Nextcloud-DB/RLS design is not available to an ExApp and should remain rejected.
- Guests, email invitees and federated users need a separate access path; a local Files mount alone cannot cover them.

## Revised decision questions

Before selecting a topology, answer these in order:

1. **Threat model:** protect against other users, a leaked service-account password, a leaked Cassini AppAPI secret, or a malicious infrastructure operator?
2. **Custody:** is a recording a user's file or an institutional record?
3. **Default grant semantics:** owner-private, frozen attendee snapshot, or current Talk-room membership?
4. **Lifecycle:** who pays quota, may delete, handles offboarding, and enforces retention?
5. **Discovery:** fixed Team-folder scan, owner roots plus received-share enumeration, or a broader tagged-file search?
6. **External participants:** are guest/federated delivery and revocation required now?

Only after those answers should “single path” be accepted or rejected.

## Recommendation from the adversarial review

Carry two candidates into the decision rather than one recommended direction:

- **User custody:** the Talk-provided recording initiator owns one file; grant access through either native `TYPE_ROOM` sharing (dynamic) or explicit user shares (frozen).
- **Institutional custody:** an ownerless Team folder with advanced ACL, using the live-validated root-read/child-deny recipe and ACL-before-data publishing.

Reject true per-participant file copies unless independent, irrevocable copies are explicitly desired. They destroy canonical deletion, consume N-times storage and make revocation impossible.

## Evidence and references

### Live validation

Run on 2026-07-29 with Nextcloud 34.0.0, Talk 24.0.3 and Team folders 22.0.5. The checks used real OCS, WebDAV and `occ` calls. No production data was involved.

### Upstream source

- AppAPI arbitrary-user session selection: [`AppAPIService.php`](https://github.com/nextcloud/app_api/blob/12d104cccf9d65c42b277b4f4f17714ff6799abe/lib/Service/AppAPIService.php#L280-L359) and DAV integration in [`DavPlugin.php`](https://github.com/nextcloud/app_api/blob/12d104cccf9d65c42b277b4f4f17714ff6799abe/lib/DavPlugin.php#L39-L43).
- Team-folder manager read floor: [`ACLManager.php`](https://github.com/nextcloud/groupfolders/blob/31efa539af741d80c9fc300b65e6b266311cbfa2/lib/ACL/ACLManager.php#L295-L310); own-read guard: [`ACLPlugin.php`](https://github.com/nextcloud/groupfolders/blob/31efa539af741d80c9fc300b65e6b266311cbfa2/lib/DAV/ACLPlugin.php#L287-L291); filtering: [`ACLStorageWrapper.php`](https://github.com/nextcloud/groupfolders/blob/31efa539af741d80c9fc300b65e6b266311cbfa2/lib/ACL/ACLStorageWrapper.php#L112-L131).
- Team-folder raw storage layout: [`groupfolders` README](https://github.com/nextcloud/groupfolders/blob/31efa539af741d80c9fc300b65e6b266311cbfa2/README.md#L41-L76).
- Nextcloud encryption threat model: [Nextcloud 34 server-side encryption](https://docs.nextcloud.com/server/34/admin_manual/configuration_files/encryption_configuration.html).
- Cassini retained artifact layout: [`docs/reference/artifacts-and-filesystem.md`](../reference/artifacts-and-filesystem.md#operator-runtime-layout).
