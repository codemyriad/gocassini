# Alternative perspective: per-user ownership of Cassini recordings

Date: 2026-07-29

Status: exploration, not an implementation proposal

## Summary

Per-user ownership is a viable and more Nextcloud-native model than the original proposal gives it credit for.

The model is not “multiple owners of one file.” Normal Nextcloud Files has one original owner for a node and zero or more shares. Instead, it is:

- **one owner per recording**;
- **many owners across the recording corpus**;
- **one physical artifact per recording**;
- recipients receive shared references/mounts, not copies.

For Talk-triggered recordings, the most defensible default owner is the authenticated moderator who clicked **Start recording**, because Talk already sends that user as `start.owner` and Cassini already persists it.

```text
Talk user Alice clicks Start recording
                 │
                 ▼
Talk sends start.owner = alice ───────► Cassini records/processes
                 │                              │
                 │                              ▼
                 └────────────────────── one portable Ogg Opus node
                                                │ owned by Alice
                         ┌──────────────────────┼─────────────────────┐
                         ▼                      ▼                     ▼
                    Bob share              Carol share          Alice's Files
                    (reference)            (reference)           (source node)

                 one source object, one owner, multiple access paths
```

## What “ownership” means in Nextcloud

Nextcloud's share interface distinguishes:

- `sharedBy`: the actor who created a share;
- `shareOwner`: the original owner “who owns the path that is shared”; and
- `sharedWith`: the recipient user, group, Team or Talk room.

There is one original owner field. A recipient can receive read, update, create, delete or re-share permissions as applicable, but that does not make them a second file owner.

### Can there be multiple owners?

| Interpretation | Supported? | Consequence |
|---|---:|---|
| Multiple original owners of one normal file | No | Nextcloud models one source owner plus shares |
| Multiple users with strong edit/share permissions | Yes | Co-management, but still one original owner |
| Different users own different recordings | Yes | The corpus is multi-owner |
| Copy the file into every participant's home | Yes, as copies | N independent objects; no common revocation or canonical deletion |
| Ownerless Team folder with several managers | Yes | Institutional shared storage, not multi-user ownership |

True per-participant copies are a different product promise: every recipient receives an independent artifact that the original publisher can no longer revoke. That is usually a poor fit for meeting recordings.

## Selecting the owner

### Talk-triggered recording

Talk already decides this:

1. only a logged-in moderator/owner can call the recording-start endpoint;
2. `RecordingController::start()` passes the authenticated user ID to `RecordingService::start()`;
3. Talk sends that ID to the recording backend as `start.owner`;
4. Cassini stores it in `talk_binding` and sends it back in `/store`'s multipart `owner` field.

Therefore:

> For a Talk-triggered recording, owner = the user who clicked Start recording.

This is not necessarily the conversation creator, the only room owner, or the person who stops the recording.

### Cassini UI starts a Talk recording

If a user presses Record in a Cassini surface, use the authenticated AppAPI user who initiated the action, after verifying they have Talk moderator rights. This preserves the same semantics as Talk's own button.

### Scheduled or autonomous recording

There is no initiating user. Do not silently fall back to `admin` or the service account. Require an explicit policy:

| Policy | When it fits | Risk |
|---|---|---|
| Designated room custodian | Recurring team meetings | Needs durable room-to-custodian configuration |
| Conversation owner | Rooms with one stable owner | Owner may be absent, changed or not unique enough for policy |
| Institutional Team folder | Compliance/retention recording | Not user-owned; use the institutional model honestly |
| Service account | Operational fallback only | Central universal reader and quota/custody concentration |

## Default sharing choices

User ownership does not decide who else gets access. Three policies are available.

### Policy U1 — owner-private, manual sharing

This is native Talk's recording default. The file lands in the initiator's Files and Talk sends that user a “Call recording now available” notification with a **Share to chat** action.

```text
publish ──► Alice owns file ──► private
                                  │
                                  └─ Alice chooses Share to chat / Files sharing
```

**Best for:** sensitive meetings and least-surprise privacy.

**Cost:** invited participants do not receive access automatically.

### Policy U2 — frozen per-user shares

At publish, enumerate the chosen grant set and create ordinary user shares from the owner to each local user. Later room membership changes do not alter the shares.

```text
participant snapshot at publish: Alice, Bob, Carol
                          │
                          ├─ source owner: Alice
                          ├─ user share → Bob
                          └─ user share → Carol

Dave joins later: no access       Bob leaves later: share remains
```

**Best for:** historical attendance or explicit grants.

**Revocation:** delete Bob's individual share without affecting the room or Carol.

**Cost:** O(N) shares, participant snapshot logic, and no automatic local mount for guests/federated users.

### Policy U3 — Talk `TYPE_ROOM` share

Create one read-only Talk-room share. Talk resolves that share to the room's **current local-user members**.

```text
Alice-owned recording ── TYPE_ROOM(room-token) ──► current room membership
                                                ├─ Alice
                                                ├─ Bob
                                                └─ Carol

remove Bob from room ──► Bob's mount disappears
add Dave to room ──────► Dave's mount appears
```

**Best for:** the promise “this recording belongs to the conversation.”

**Revocation:** delete the room share for everyone, remove a user from the conversation, or let a recipient unshare it from themselves. Excluding one user while they remain a room member is not a natural `TYPE_ROOM` operation.

**Cost:** access is dynamic, not frozen; federated conversations are rejected by the local room-share provider; non-user participants do not receive a normal Files-home mount.

### Policy comparison

| Property | U1 owner-private | U2 frozen user shares | U3 Talk room share |
|---|---:|---:|---:|
| Automatic participant access | No | Yes | Yes |
| Grant set | Owner only | Publish-time snapshot | Current room membership |
| Later joiner | No access | No access | Gains access |
| Later leaver | Owner decides | Keeps access | Loses access |
| Revoke one current member | Easy | Easy | Awkward without room removal |
| Share operations per meeting | 0 | O(N) | 1 |
| Native Talk UX | **Exact default** | Files-native, custom automation | Native share type, custom auto-trigger |
| Guest/federated Files mount | No | No | No local mount; public-room chat links are a separate mechanism |

## “All participants” needs a precise definition

A room membership query does not necessarily mean meeting attendance.

```text
conversation members ───────────────┐
users currently in the live call ───┼─ different sets
users present at any recorded time ─┘
```

- U3 grants current conversation members.
- U2 can snapshot conversation members at publish.
- If the requirement is actual recorded-call attendees, Cassini must capture attendance during the recording interval. A post-publish room query cannot reconstruct it reliably.

The attendance record can remain grant-input metadata; Nextcloud shares still remain the sole access authority.

## Revocation and deletion

### Ordinary user shares

The owner or share creator can delete a specific share. The recipient's mount disappears, but the source file remains. A recipient can remove a received mount from themselves without deleting the owner's source.

### Talk room share

Talk's room-share provider computes received mounts from the caller's current rooms. The 2026-07-29 live check showed:

1. Alice shared an Alice-owned recording to a room containing Bob;
2. Bob received `/Talk/cassini-portable.ogg` while Eve did not;
3. removing Bob from the room removed the recording from Bob's `/Talk` listing;
4. re-adding Bob restored it.

Deleting the source file or the room share revokes it for everyone. Deleting the associated chat message can also remove the share when performed by an authorized moderator/share owner.

### Copies

Once a byte copy is placed in another user's home, revocation is not possible through the original share. This is why copies should not be described as “sharing.”

## Privacy analysis

Per-user ownership gives meaningful **application-layer** privacy, but not infrastructure-level secrecy.

### Actor matrix

| Actor/compromise | Fixed service owner | User-owned file | Team folder + ACL |
|---|---|---|---|
| Unrelated NC user | Denied if shares/proxy are correct | Denied | Denied |
| Leaked service-account password | **All recordings exposed** | No special access unless shared | ACL manager may expose all |
| Leaked ordinary `admin` account password | All recordings if admin owns them | Standard DAV does not automatically mount user files | Depends on Team-folder admin/mapping |
| Leaked Cassini `APP_SECRET` | All users can be impersonated | All users can be impersonated | All users can be impersonated |
| Nextcloud host/container root | Raw files/keys available | Raw files/keys available | Raw `__groupfolders` files/keys available |
| Cassini host/container root | Retained capture and output available | Retained capture and output available | Retained capture and output available |

A live NC34 test confirmed a useful distinction:

- Basic DAV as `admin` targeting `/dav/files/alice/...` returned `404`; a normal admin Files credential is not automatically Alice's DAV principal.
- Shell access to the Nextcloud container read Alice's file directly from `data/alice/files/...`, byte-for-byte.

Thus user ownership improves resistance to accidental admin browsing and compromise of a central recording-account password. It does **not** protect from the infrastructure administrator assumed in the question.

## Are Nextcloud Files encrypted on disk?

Not by default. The default local-storage layout contains the file bytes under the Nextcloud data directory. The live uploaded Ogg began with the normal `OggS` header on disk and matched the source SHA-256.

Encryption modes change which threats are addressed:

| Mode | What a raw storage reader sees | Can a server/infra administrator obtain plaintext? | Cassini fit |
|---|---|---|---|
| No SSE (common default) | Plain file content, names and paths | Yes, directly | Current behavior |
| SSE, master-key mode | Encrypted content; names/structure remain visible | Yes; server controls the master key | Compatible, but not admin-private |
| SSE, user-key mode without recovery | Encrypted content; user password protects private key | Harder offline; Nextcloud documents limited protection, not safety from a compromised live server | Operational limitations; must be validated with AppAPI/Talk |
| E2EE folder | Ciphertext created by client; server lacks plaintext key | Designed to deny server administrators | Incompatible with current server-side recorder, transcriber and viewer flow without redesign |
| Disk/LUKS encryption | Protects powered-off/stolen media | Host root after unlock can read | Good baseline, not a malicious-host defense |

Nextcloud states that SSE is mainly for external-storage protection and does not protect against a compromised server or malicious administrator. E2EE is the relevant threat model, but Cassini currently must possess plaintext to record, transcribe, pack and serve the meeting. Even an E2EE final upload would not erase Cassini's plaintext working copies unless retention is redesigned.

## Storage and lifecycle consequences

### Benefits of user ownership

- No single Files account is the obvious “all meetings” target.
- Native owner sharing UI and audit concepts apply.
- The person who initiated recording controls its initial release.
- Talk already supplies and validates the owner.
- Different recordings naturally have different custodians.

### Costs and failure modes

- **Quota:** the recording consumes the initiating user's quota. Talk can return `owner_permission`/storage errors.
- **Offboarding:** deleting the owner can delete or orphan the source unless ownership is transferred first.
- **Retention:** a user can delete or move an organizational record unless policy restricts that behavior.
- **Availability:** Talk `/store` requires the supplied owner still to be a room participant and a valid user at upload time.
- **Discovery:** owner files and received mounts are distributed; a single fixed-root scan is no longer sufficient in every case.
- **Path movement:** owners can move source files and recipients can independently move room-share mounts. Indexing by file ID helps after discovery but does not by itself find a moved file.

These costs are decisive if recordings are institutional records. They are acceptable or desirable if recordings are personal files shared by their custodian.

## Viewer discovery under user ownership

The original Team-folder proposal gets cheap discovery from one known mount. User ownership needs a different discovery shape.

Options, from least to most custom:

1. scan the owner's configured recording root plus normal received-share roots such as `/Talk`;
2. enumerate received shares through Nextcloud's share APIs, then inspect candidate source nodes;
3. tag Cassini files and use a caller-scoped Nextcloud search mechanism;
4. recursively scan the whole caller home (correct but potentially expensive and over-broad).

The viewer should continue to treat a caller-scoped Nextcloud result as the authorization answer. It should use `fileid` and the embedded Cassini meeting ID for identity, and a sidecar/header only for display metadata.

A fixed `/Talk` scan alone is not complete because recipients can move their own share mounts.

## Delivery variants for Cassini

### V1 — use Talk `/store` with an `.ogg` filename

Talk 24.0.3 accepts MIME `audio/ogg` with extension `.ogg`, but not `.opus`. A live check used the **same Ogg Opus bytes**:

| Uploaded filename | Detected content | `/store` result |
|---|---|---:|
| `cassini-portable.opus` | `audio/ogg` | `400 file_extension` |
| `cassini-portable.ogg` | `audio/ogg` | `200`, stored in Alice's recording folder |

This means Talk rejects the extension, not the Ogg Opus payload. Using `.ogg` gains native owner placement and notification but weakens Cassini's canonical `.opus` naming contract.

### V2 — add `.opus` to Talk's allow-list

Talk's allow-list maps `audio/ogg` only to `ogg`. Adding `opus` is a small upstream-compatible change if Nextcloud's detector reports `audio/ogg`, as the live check did. This is the cleanest Talk-native long-term option, subject to upstream acceptance and version support.

### V3 — direct WebDAV as the Talk-provided owner

Cassini can keep the `.opus` extension and upload into the initiating user's Files using AppAPI act-as-user, then create Files or Talk shares. This avoids the Talk extension rule but uses the broad AppAPI credential and must choose/manage a path itself.

### V4 — Talk chunked upload token

For large files, Talk can issue a temporary password-protected, create-only upload share for the per-room recording folder. This is a strong least-privilege data path: the recorder can upload without receiving read access to existing recordings. Finalization still calls `/store`.

## Recommendation within this perspective

If Cassini chooses user custody:

1. use Talk's supplied `start.owner`; do not invent a service owner;
2. keep one physical artifact and share references, not copies;
3. choose U2 or U3 explicitly:
   - U2 when access must be frozen to a historical set;
   - U3 when access should follow the Talk conversation;
4. keep owner-private U1 as a room/admin policy for sensitive meetings;
5. spike `.opus` acceptance in Talk—prefer an upstream allow-list change, with `.ogg` as a technically proven compatibility path;
6. design offboarding, quota, retention and moved-file discovery before calling the model complete.

This is more coherent than forcing personal recordings into an ownerless global tree, but it is not automatically right for institutional records.

## References

- One original share owner: Nextcloud [`IShare.php`](https://github.com/nextcloud/server/blob/803e484e409d086c7b951a0f7f5d5be24262cbb9/lib/public/Share/IShare.php#L417-L449).
- Talk owner selection: [`RecordingController.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Controller/RecordingController.php#L356-L368) and recording contract: [`docs/recording.md`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/docs/recording.md#L69-L84).
- Talk storage and format checks: [`RecordingService.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L59-L65), [`store()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L140-L164), and [`getRecordingFolder()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L622-L645).
- Talk room sharing: [`RecordingService::shareToChat()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L708-L751) and [`RoomShareProvider.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Share/RoomShareProvider.php#L802-L864).
- AppAPI impersonation: [`AppAPIService.php`](https://github.com/nextcloud/app_api/blob/12d104cccf9d65c42b277b4f4f17714ff6799abe/lib/Service/AppAPIService.php#L280-L359).
- Encryption threat model: [Nextcloud 34 SSE documentation](https://docs.nextcloud.com/server/34/admin_manual/configuration_files/encryption_configuration.html) and [encryption details](https://docs.nextcloud.com/server/34/admin_manual/configuration_files/encryption_details.html).
