# How Nextcloud Talk stores and shares recording files

Date: 2026-07-29

Target verified: Nextcloud 34.0.0, Talk 24.0.3 (`stable34`)

## Short answer

Talk uses a **single user owner plus optional shares** for recordings.

- The owner is the authenticated moderator who clicked **Start recording**.
- The recording backend receives that user ID in `start.owner`.
- `/recording/{token}/store` writes the file into that user's Files, by default under `/Talk/Recording/<room-token>/`.
- The stored recording is **private to the owner by default**.
- Talk notifies the owner and offers a manual **Share to chat** action.
- Sharing to chat creates one read-only `TYPE_ROOM` share. Local users who are current conversation members receive a shared mount; it is not a second copy and they do not become owners.

```text
moderator Alice clicks Record
           │
           ▼
Talk ── start.owner=alice ──► recording backend
                                   │
                                   │ POST /recording/<token>/store
                                   │ owner=alice + recording file
                                   ▼
                    Alice /Talk/Recording/<token>/<file>
                                   │
                       owner notification only
                                   │
                         [Share to chat]
                                   ▼
                    read-only TYPE_ROOM share
                         │ current membership
                 ┌───────┴────────┐
                 ▼                ▼
               Bob mount       Carol mount
                 (same source file; Alice remains owner)
```

## 1. Who becomes the recording owner?

Talk's start endpoint is available only to a logged-in moderator/owner participant. The controller passes the current authenticated user ID into the recording service:

```php
$this->recordingService->start($this->room, $status, $this->userId, $this->participant);
```

The backend notification then includes:

```json
{
  "type": "start",
  "start": {
    "owner": "alice",
    "actor": { "type": "users", "id": "alice" }
  }
}
```

For the usual user flow, `owner` and actor ID are the same person: the person who clicked Record. The protocol keeps both fields because the actor structure and storage owner have different roles.

The owner is **not selected by the HPB**. HPB/signaling supplies media; Talk's application API supplies the storage owner as part of the recording-backend contract.

Cassini already handles this correctly for Talk's raw recording:

- `talkStartData.Owner` receives the field;
- `talkRoomState.Owner` and `talk_binding.owner` persist it;
- `streamTalkUpload()` sends it back as multipart field `owner` to Talk `/store`.

## 2. Where does Talk store the file?

`RecordingService::store()`:

1. verifies that the supplied owner is a participant of the room;
2. validates file content MIME and filename extension;
3. resolves the owner's user folder;
4. resolves the owner's recording root;
5. creates a room-token subfolder;
6. creates the file there.

The default path is:

```text
/<owner Files>/Talk/Recording/<room-token>/<uploaded-name>
```

The attachment root is user-configurable. The default is `/Talk`; the recording root defaults to `<attachment root>/Recording`.

This means two users who initiate recordings in the same Talk room can own files in two different home storages. Talk does not construct one organization-wide recording tree.

### Ownership validation caveat

At store time the owner must still be a valid room participant. If the initiating user is deleted or removed before a delayed portable upload, Talk returns `owner_participant`/`owner_invalid`. The user's quota and write permissions also apply.

## 3. What does `/store` make accessible?

Only the owner receives access initially. `finalizeRecording()` creates a notification for that owner and may schedule Talk's own transcription/summary tasks. It does not automatically share the file.

The notification includes two actions:

- **Share to chat**;
- **Dismiss notification**.

This is an important product behavior: native Talk is owner-private with opt-in publication, not “all participants by default.”

## 4. What does “Share to chat” do?

`RecordingService::shareToChat()` resolves the file through the acting participant's user folder, creates a share with:

```text
share type:  IShare::TYPE_ROOM
shared with: Talk room token
permissions: READ
```

It then posts a `file_shared` system message into the chat.

The endpoint is a **user/moderator OCS endpoint**:

```text
POST /ocs/v2.php/apps/spreed/api/v1/recording/<token>/share-chat
body: fileId=<id>&timestamp=<notification timestamp>
```

It is guarded by `RequireModeratorParticipant`; it is not authenticated with the recording-backend HMAC used by `/store`. A Cassini automation would need to act as the owner/moderator through AppAPI (or add a new Talk backend capability).

The `/store` response does not return the new file ID. Cassini can resolve `oc:fileid` by `PROPFIND` on the known owner path before invoking `share-chat`.

## 5. Who can access a Talk room share?

For logged-in local users, `RoomShareProvider::getSharedWith()` first obtains the caller's current rooms with attachments, then returns `TYPE_ROOM` shares whose room token is in that set.

Therefore access follows **current room membership**, not the membership at recording time.

```text
TYPE_ROOM share S points to file F and room R

is Bob a current local-user member of R?
       │
       ├─ yes ─► mount F in Bob's Files
       └─ no  ─► no mount, no Files access through S
```

### Live result

On NC34/Talk24:

| Event | Bob's Files result |
|---|---|
| Alice shares recording to a room containing Bob | `/Talk/cassini-portable.ogg` appears |
| Eve is not a room member | Eve has no mount |
| Alice removes Bob from room | recording disappears from Bob's `/Talk` listing |
| Alice re-adds Bob | same recording mount returns |

A recipient may also unshare the room mount from themselves. Talk stores a per-user `TYPE_USERROOM` child record with zero permissions so the original room share remains for everyone else.

### Guests and federation

- A guest has no local Files home, so no normal WebDAV mount can appear for a Cassini Files scan.
- For public rooms, Talk can render attachment links through the room share's public token so a guest can open a chat attachment. That is a Talk chat-link path, not a Files-home discovery path.
- The local room-share provider rejects creating a room share for a federated conversation. Private federated delivery requires a separate federation mechanism.

## 6. How is access revoked?

| Desired action | Native mechanism | Effect |
|---|---|---|
| Revoke for everyone | Delete the `TYPE_ROOM` share or source file | All received mounts disappear |
| Revoke because user leaves room | Remove/leave room | Dynamic room mount disappears |
| Recipient no longer wants it | Recipient unshares from self | Only that recipient's mount disappears |
| Revoke one user who remains a room member | No natural per-user owner control on one `TYPE_ROOM` share | Use an explicit user-share model instead, or remove them from room |
| Prevent future room members seeing historical recording | Do not use `TYPE_ROOM`; create frozen user shares | Later joiners receive nothing |

Deleting the corresponding file-share chat message can remove the room share when the actor is an authorized moderator or the share owner.

## 7. Does Talk support multiple owners?

Not for one recording file. Talk's share row retains one original `uid_owner`. Room members receive references to that source.

Talk does, however, use a useful **multi-owner collection** pattern for ordinary conversation attachments in NC34:

```text
Alice's home                           Bob's home
/Talk/<room>/Alice-alice/              /Talk/<room>/Bob-bob/
  alice-photo.jpg                        bob-notes.pdf
        │ TYPE_ROOM share                      │ TYPE_ROOM share
        └──────────── room members ────────────┘

one owner per uploader subtree; many owners across the conversation
```

`ConversationFolderService` creates a per-conversation, per-uploader subfolder in the uploader's own home and shares that subfolder to the room. This is direct evidence that Talk does **not** require one canonical physical tree to present a coherent conversation-level file experience.

Recordings use an older/specialized path—owner's `/Talk/Recording/<token>/` plus optional per-file room share—but the same ownership principle holds.

## 8. What formats does Talk accept?

Talk 24.0.3's allow-list is:

| Detected MIME | Allowed extension |
|---|---|
| `audio/ogg` | `.ogg` |
| `video/ogg` | `.ogv` |
| `video/mp4` | `.mp4` |
| `video/webm` | `.webm` |
| `video/x-matroska` | `.mkv` |

The check validates **both** detected content and extension.

### `.opus` versus `.ogg`: live result

An Opus-in-Ogg file generated by ffmpeg was detected as `audio/ogg`:

| Same bytes uploaded as | Result |
|---|---:|
| `cassini-portable.opus` | `400`, `file_extension` |
| `cassini-portable.ogg` | `200`, stored and playable as `audio/ogg` |

Talk is not rejecting the Opus codec or Ogg payload. It is rejecting `.opus` because that extension is absent from the `audio/ogg` extension list.

This gives Cassini three choices:

1. upload its portable artifact under `.ogg`;
2. continue direct Files/WebDAV upload under `.opus`;
3. propose/ship a Talk change allowing `opus` alongside `ogg` for `audio/ogg`.

The third is the cleanest native integration if Nextcloud's MIME detector is consistently `audio/ogg` on supported versions.

## 9. What Cassini currently does

There are two separate delivery paths on this branch lineage:

```text
raw capture recording.mkv
    └─ Talk /store, owner = start.owner
       └─ user's /Talk/Recording/<token>/recording-....mkv

portable meeting <job-id>.opus
    └─ direct WebDAV, owner = hard-coded admin
       └─ admin/Cassini/Recordings/meetings/<job-id>.opus
```

The first path follows native Talk ownership. The second overrides it in order to create a central Cassini archive and preserve the `.opus` filename.

That means Cassini already has the data needed for a user-owned portable model; the unresolved choice is policy and delivery endpoint, not owner discovery.

## 10. Implications for Cassini

### If Cassini wants native Talk behavior

- Store the portable Ogg Opus under the recording initiator.
- Keep it private by default.
- Let the owner manually use Talk's Share to chat action.
- Prefer Talk's upload endpoint or temporary chunked upload share.

### If Cassini wants all room members by default

- Store under the initiator.
- Resolve the stored file ID.
- Act as that moderator and call `share-chat` once.
- Accept that access follows current room membership.

### If Cassini wants frozen publish-time access

- Store under the initiator.
- Enumerate the intended local-user set.
- Create ordinary user shares rather than a `TYPE_ROOM` share.
- Persist share IDs or reliably re-query them for idempotency and revocation.

### If Cassini wants institutional custody

Talk's recording behavior is no longer the direct model. Use a service-owned archive or ownerless Team folder, and describe it as an intentional institutional override with retention benefits and centralized-reader risk.

## Live-validation record

The 2026-07-29 lab used real Nextcloud/Talk APIs:

- Nextcloud `34.0.0.12` / version string `34.0.0`;
- Talk `24.0.3`;
- a room owned by Alice with Bob invited and Eve uninvolved;
- direct HMAC-authenticated `/store` uploads;
- WebDAV `PROPFIND`/GET for Alice, Bob and Eve;
- real `share-chat`, participant removal and re-addition.

The test stack was disposable and contained synthetic one-second audio only.

## Source references

- Owner is the recording initiator: [`RecordingController::start()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Controller/RecordingController.php#L356-L368), [`BackendNotifier::start()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Recording/BackendNotifier.php#L119-L133), and [recording API docs](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/docs/recording.md#L69-L84).
- Store validation and placement: [`RecordingService::store()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L140-L164), [`getRecordingFolder()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L622-L645), and [`Config::getRecordingFolder()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Config.php#L238-L244).
- Private notification plus manual sharing action: [`Notifier.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Notification/Notifier.php#L315-L360).
- Room share creation: [`RecordingService::shareToChat()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L708-L751); OCS guard: [`RecordingController.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Controller/RecordingController.php#L551-L580).
- Dynamic received mounts and self-unshare: [`RoomShareProvider.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Share/RoomShareProvider.php#L354-L408) and [`getSharedWith()`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Share/RoomShareProvider.php#L795-L865).
- Per-uploader conversation attachment folders: [`ConversationFolderService.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/ConversationFolderService.php#L25-L118) and its [`TYPE_ROOM` share](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/ConversationFolderService.php#L282-L314).
- Format allow-list: [`RecordingService.php`](https://github.com/nextcloud/spreed/blob/3944afae9559c9d7ce4c42db50f13bf9dd6bf1bc/lib/Service/RecordingService.php#L59-L65).
