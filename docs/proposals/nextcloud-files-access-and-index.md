# Proposal: Nextcloud-Files access-control topology + meeting index model

Date: 2026-07-27
Status: Draft for discussion (output of spike [D-530](https://linear.app/code-myriad/issue/D-530))

## TL;DR

[D-529](https://linear.app/code-myriad/issue/D-529) (PR #150) moved the meeting archive into
Nextcloud Files: the operator delivers each meeting's portable `.opus` over WebDAV into a
canonical `Cassini/Recordings/` tree and serves the viewer from there — **public**, owned by
the NC **admin**, indexed by a single root `catalog.json`. This proposal is the follow-up
spike's recommendation for the two things that first pass deliberately left open: **who may
read a recording**, and **how the viewer builds the meeting list cheaply and access-filtered**.

Two independent questions that compose:

- **Access** — express per-user / per-group read **as Nextcloud file permissions** (no
  Cassini-side ACL, per [D-416](https://linear.app/code-myriad/issue/D-416)/[D-521](https://linear.app/code-myriad/issue/D-521)), on a **canonical shared tree with no single
  owner**, default-granted to the Talk room's participants **at publish time** and **frozen**
  there (editable later). **Recommended:** a system-owned *Recordings* **group/Team folder +
  advanced ACL**, ACL written per-meeting by a delegated `cassini` service account over WebDAV.
- **Index** — build the list without pulling each `.opus`, and access-filtered.
  **Recommended:** the **per-user WebDAV scan is the access authority**; a tiny co-located
  `<meeting>.manifest.json` **sidecar** is a non-authoritative metadata accelerator. Reject a
  Nextcloud-DB / RLS index (infeasible for an ExApp). Keep `catalog.json` for the
  standalone/no-NC export only.

Together: **one per-user PROPFIND of `Recordings/` yields an access-filtered, cheap meeting
list — NC Files as the single source of truth for access *and* display.**

```text
                     the two D-530 questions, and how they compose
   ┌──────────────────────────────┐        ┌──────────────────────────────┐
   │  ACCESS MODEL                 │        │  INDEX MODEL                  │
   │  who may READ meeting X?      │        │  how to LIST meetings cheaply?│
   │  → group-folder + advanced ACL│        │  → per-user PROPFIND scan     │
   │    (deny-read HIDES the path) │        │    + tiny sidecar accelerator │
   └──────────────┬───────────────┘        └───────────────┬──────────────┘
                  │  ACL makes the scan return ONLY readable │
                  │  meetings ─────────────────────────────► │
                  │                                          │ sidecar makes each
                  │  ◄──────────────────────────────────────┤ listed entry a few
                  │     the .opus is the shared unit the     │ hundred bytes, not
                  │     scan authorises against              │ a ~1 MiB header read
                  ▼                                          ▼
                 "NC Files = single source of truth for access + display"
```

**Status honesty (from source verification, below).** The *index* recommendation is settled
by source-reading. The *access* recommendation is the right **direction** — group-folder
advanced ACL is exactly the "different members see different subfolders of one shared folder"
feature — but the precise ACL rule recipe that makes a per-user **scan** return only the
caller's meetings (given groupfolders' inheritance semantics) is **not** settled by reading
code and is the one load-bearing **live-instance spike (S1)** gating implementation. A proven
per-user **OCS-share** path is retained as the hedge.

## What this builds on (verified against PR #150)

`cassini-operator/internal/operator/webdav_upload.go` already implements the act-as-user
WebDAV seam this proposal extends: `ncFilesUploader` (MKCOL + PUT), `ncFilesProxy` (a
Range-forwarding GET proxy that relays `200/206` + `Content-Range`/`-Length`/`-Type`),
`setAppAPIDAVHeadersForUser` (`AUTHORIZATION-APP-API: base64("<user>:<secret>")` +
`EX-APP-ID`/`EX-APP-VERSION`/`AA-VERSION`), and the constants `ncRecordingsOwner = "admin"`,
`ncRecordingsRoot = "Cassini/Recordings"`. It is wired into the publish worker
(`run.go` → `publish_runtime.go`) and behind the existing `viewerHandler`/`publishedHandler`
routes (`exapp.go`). **Today it performs only MKCOL/PUT/GET as the single owner `admin`** — the
per-meeting `PROPPATCH`, OCS shares, and sidecar PUT this proposal adds are net-new, but they
layer onto the same act-as-user primitives (`davFileURL(user, …)`, `setAppAPIDAVHeadersForUser(req, user)`),
which are already user-parameterised. This proposal is therefore additive to #150, not a rewrite.

The viewer keeps its `DataProvider` seam — `StaticCatalogProvider` over `catalog.json`
(`cassini-viewer/src/viewer/dataProvider.ts`, `catalog.ts`) — and #150 left the **CSP, the
route set, and that seam** unchanged (it did touch the viewer client for a catalog
refresh-on-focus, unrelated to this proposal).

## Constraints to preserve

From [D-416](https://linear.app/code-myriad/issue/D-416)/[D-521](https://linear.app/code-myriad/issue/D-521):

1. **Nextcloud is the source of truth for access.** If you can see the file in NC you can view
   it in Cassini; if you can't, you never learn it exists. **No Cassini-side ACL.**
2. **No server-side meeting model / Postgres** unless clearly justified. The operator is an
   AppAPI ExApp with only a private local sqlite store.
3. **Canonical shared tree, no single owner** — not "one owner sharing outward."
4. **Frozen at publish** — the default grant is the room's participants at publish time; it
   must not track live membership at retrieval, but stays manually editable.

---

## Part A — Access model

### A.1 The default the model has to realise

The recording `.opus` is readable, by default, by **whoever was in the Talk room at the moment
it was published**; that grant is **frozen at publish** and **manually editable later**. The
ExApp reaches every NC mechanism below by acting as a user via the AppAPI header — NC decodes
`base64("<userId>:<secret>")`, validates the secret against the ExApp's stored secret, and runs
the request as that user, fully headless (source-verified: `app_api`
`AppAPIService::finalizeRequestToNC` → `userSession->setUser`).

### A.2 The three candidates

```text
(1) GROUP / TEAM FOLDER + ADVANCED ACL          (2) PER-FILE USER SHARES        (3) GROUP SHARES
    system-owned mount (NO owner)                   service-acct owns file          service-acct owns file
    /Recordings ── system mount ──┐             svc-home/…/mtg.opus             svc-home/…/mtg.opus
      <mtg>/mtg.opus              │               │ TYPE_USER share (N of)         │ TYPE_GROUP share
      + ACL rule per user/group   │               ├─► alice home (SharedMount)     └─► every member of grp G
    mounts into EVERY assigned-   │               ├─► bob   home (SharedMount)          (all-or-nothing)
    group member's home; ACL      │               └─► …
    deny-read HIDES paths a user  │             true per-user, but one owner       one owner, one coarse
    lacks READ on ────────────────┘             + scattered mounts, no tree        grant, no per-file
```

- **(1) Group/Team folder + advanced ACL.** A `groupfolders` folder (NC 32 "Team folders") is
  owned by **no user**; it is a system mount assigned to groups/teams that appears in each
  assigned member's Files home. Its *advanced ACL* **hides** any path a user lacks `READ` on:
  `ACLStorageWrapper` returns `false` from `isReadable`/`is_dir`/`is_file`/`stat`, and
  `opendir()` rebuilds a directory listing to include **only** children the user can read
  (source-verified against `nextcloud/groupfolders` master). Permissions are a bitmask
  (`READ=1, UPDATE=2, CREATE=4, DELETE=8, SHARE=16`).
- **(2) Per-file / per-dir USER shares.** The ExApp, acting as a service account that **owns**
  the file, issues one OCS share per recipient (`POST …/apps/files_sharing/api/v1/shares`,
  `shareType=0`); revoke via `DELETE …/shares/{id}`. True per-user granularity; each recipient
  gets a `SharedMount` at their **home root** — trivially scan-discoverable, but the account is
  the sole owner and there is no canonical tree.
- **(3) GROUP shares.** Same call, `shareType=1`; one grant to a whole group. All-or-nothing,
  no per-file granularity.

### A.3 Comparison

E = excellent / native fit · ~ = partial · ✗ = contradicts the model.

| Dimension | (1) Group folder + ACL | (2) Per-file user shares | (3) Group shares |
|---|:---:|:---:|:---:|
| Canonical single shared tree | **E** one system-owned tree | ✗ file in svc home; scattered mounts | ✗ same |
| No single owner | **E** owned by system | ✗ svc account owns every file | ✗ same |
| Per-user granularity | **E** ACL user-mapping per path | **E** one share per user | ✗ group-only |
| Per-group / per-team granularity | **E** ACL group/circle mapping | ~ separate group share | E (whole group) |
| Programmatic from the ExApp | folder = `occ` **once**; rules = DAV `PROPPATCH` | OCS `POST`/`DELETE` per share | OCS `POST` |
| **Scan discoverability** | ~ deny-read hides non-granted paths, **but per-user *reachability* is the open spike (A.5)** | **E** `SharedMount` at home root, unambiguous | E mounts for every group member |
| Freeze-at-publish fit | **E** ACL is static; membership churn does not change it | **E** N shares at publish | ~ coarse |
| Key caveat | ACL not in OCS (PROPPATCH only); folder setup `occ`-only; mount gated by **group** membership; **inheritance recipe unsettled** | single owner; scattered, unstable paths | everyone-or-no-one |

Only **(1)** delivers all three structural requirements at once — canonical tree, no single
owner, fine-grained per-user/per-group. (2) and (3) keep a single owning account and scatter
mount-by-reference copies. But (1) buys those structural wins at the cost of the one mechanic
that is *not* settled by reading source — per-user **reachability under a scan** (A.5) — where
(2)'s `SharedMount` is unambiguous. Hence: (1) is the recommended **direction**, (2) is the
**hedge**, decided after the S1 live spike.

### A.4 Recommended — a system-owned "Recordings" group/Team folder + advanced ACL

The load-bearing nuance that shapes the whole design: **groupfolders ACL rules are not in the
OCS API** ([nextcloud/groupfolders#1256](https://github.com/nextcloud/groupfolders/issues/1256),
open since 2021). The app's own web UI sets ACL by issuing a **WebDAV `PROPPATCH` of a custom
property `{http://nextcloud.org/ns}acl-list`** on the file's normal DAV path (`src/services/acl.ts`,
`lib/DAV/ACLPlugin.php`). An ExApp acting-as-user can issue that exact PROPPATCH over DAV, so the
per-meeting grant is programmatically reachable — the Go operator emits the same XML `acl.ts` does.

Authorisation for that PROPPATCH is `FolderManager::canManageACL(folderId, user)`, true for a NC
admin **or** a user delegated as ACL-manager for that folder (source-verified:
`computeCanManageACL` returns true for a non-admin delegated user/group/circle manager). So the
ExApp never impersonates an admin per meeting; it acts as a `cassini` service account
([D-532](https://linear.app/code-myriad/issue/D-532)) delegated once as ACL-manager. One guard:
an ACL manager cannot remove their **own** read permission.

This splits cleanly into **one-time admin setup** (out-of-band, `occ`/UI — the folder-lifecycle
routes are `#[FrontpageRoute]`s not callable via AppAPI, spike S2, resolved) and a **repeatable
per-meeting ExApp path** (all WebDAV, no OCS/CSRF):

```text
  ONE-TIME SETUP (admin — occ or the groupfolders UI, NOT the ExApp; spike S2 → occ-only)
  ─────────────────────────────────────────────────────────────────────────
  occ groupfolders:create Recordings                          → folder_id
  occ groupfolders:group  <id> recording-viewers             # BROAD membership GATE (see A.5)
  occ groupfolders:permissions <id> --enable                 # turn advanced ACL on
  occ groupfolders:permissions <id> --acl-no-default-permission   # default-deny floor (base 0)
  occ groupfolders:permissions <id> --manage-add --user cassini / # delegate ACL management
  occ group:adduser recording-viewers cassini                # cassini must be a MEMBER too (A.5)

  PER MEETING (ExApp acting as `cassini`, entirely WebDAV — authenticates cleanly)
  ─────────────────────────────────────────────────────────────────────────
  MKCOL     /remote.php/dav/files/cassini/Recordings/<meeting-id>/
  PUT       <meeting-id>.opus            (DAV ignores the extension → audio/ogg)
  PUT       <meeting-id>.manifest.json   (the index sidecar — Part B)
  PROPPATCH nc:acl-list on <meeting-id>/ : one +read rule per publish-time participant
            (mapping-type=user|group|circle, mask=READ(1), perms=READ(1))
            — folder-level, so .opus AND sidecar inherit the grant together
```

The mount is reachable on two WebDAV surfaces — the normal Files home (what `acl.ts` targets)
and `/remote.php/dav/groupfolders/<uid>`. Deny-read-hides makes a per-user scan of the normal
path access-filtered **with zero Cassini-side filtering** — *subject to A.5*.

### A.5 The load-bearing open question — per-user reachability under a scan (spike S1)

Source-reading **confirms** three points and **leaves one unsettled**:

```text
  CONFIRMED by source                                UNSETTLED (needs a live instance — S1)
  ──────────────────                                ──────────────────────────────────────
  • deny-read HIDES a path (opendir filters          • groupfolders has NO upward read
    children by their own READ bit)                    propagation, and the mount root resolves
  • the mount is gated by GROUP/CIRCLE                 to base 0 under acl_default_no_permission,
    membership, evaluated BEFORE ACL; a +read          so opendir(root) FAILS for a plain member.
    rule for a user in NO assigned group is          • ⇒ a +read on a NESTED <meeting>/ alone is
    INERT (→ broad viewer group + default-deny;         NOT reachable by a SCAN without read on
    cassini must be a member so the folder             the root + every ancestor.
    mounts in its tree for the PROPPATCH)            • whether a root/intermediate grant then
  • a delegated non-admin `cassini` may PROPPATCH      LEAKS siblings via DOWNWARD inheritance
    nc:acl-list; own-read guard exists                 (vs. being per-path) — the two review
                                                        agents disagreed. THIS is the recipe to pin.
```

groupfolders' **headline feature** is precisely "different members see different subfolders of
one shared folder," so the *capability* exists; what is unsettled is the **exact rule recipe**
for our layout and how a per-user PROPFIND scan traverses it. The scan needs read on the
container it lists, and read may (or may not) inherit down to that container's other children —
which decides between "one root grant + per-meeting grants, no leak" and "per-ancestor grants
per user." There is no separate traverse/execute bit (READ governs both listing and reading),
which is what makes this non-trivial.

> **S1 — the go/no-go spike.** On a live NC 32+: create a `Recordings` group folder with
> `acl_default_no_permission`, place a recording at `Recordings/<id>/<id>.opus`, grant **only**
> a per-user `+read` on `Recordings/<id>/`, then — acting as that member — PROPFIND
> `Recordings/` and confirm the scan surfaces exactly that meeting and nothing else. Iterate the
> rule set (root grant? per-ancestor? group rule on root?) until a per-user scan returns only
> the caller's meetings **without** leaking siblings. If no clean recipe exists, fall back to
> A.7 (per-user OCS shares). A direct GET of a known leaf URL likely works with leaf-read alone
> (`isReadable(leaf)` passes); **discovery via scan** is the part S1 must settle.

### A.6 The default policy — enumerate at publish, freeze the ACL

```text
  publish time (ExApp acts as room owner/moderator — holds `owner` in talk_binding)
     │
     ▼
  GET /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants
     │   actorType == users    → map actorId → NC userId          (grantable, +read per user)
     │   actorType == groups   → group rule (or expand)           (grantable — see below)
     │   actorType == circles  → circle/team rule (or expand)     (grantable — see below)
     │   actorType == guests/emails/federated_users → WARN + SKIP  (no local mount)
     ▼
  PROPPATCH nc:acl-list on <meeting-id>/  → one +read rule per grantable participant
     │
     ▼
  ACL is STATIC — later room churn does NOT re-evaluate it → access FROZEN at publish,
  admin-editable later via PROPPATCH / the groupfolders UI
```

`getParticipants` is callable by any **non-guest** participant and always by a moderator; it is
subject to a lobby gate, so Cassini must enumerate **as the room owner/moderator** (which it is)
— then the lobby caveat does not bite. Verified nuances to design for:

- **Groups and circles (Teams) are also grantable**, not only `actorType==users`. Talk usually
  expands a group added to a room into per-user attendees (so per-user rules suffice), but an
  **unexpanded circle/team would be silently dropped** — enumerate and grant them explicitly, or
  assert they were expanded.
- **Guests / email / federated participants have no local mount.** Skip them with a warning.
  `getParticipants` is `#[FederationSupported]`, so **federated attendees are a first-class,
  expected case** — "skip federated" is a genuine **product gap** (remote meeting attendees get
  no recording access via the local mechanism), not a benign edge. Surface it.

Writing the rule-set **once** freezes access: a group-folder ACL is static and — unlike a Talk
`TYPE_ROOM` share, which tracks *current* membership and revokes on leave — does not re-evaluate
on churn. That static behaviour is exactly "frozen at publish, editable later."

### A.7 Fallback / hedge — per-user OCS shares from the `cassini` account

If `groupfolders` is absent, **or** if S1 shows no clean per-user-scan recipe, fall back to
per-user `files_sharing` shares (`shareType=0`) from the `cassini` service account. An OCS
user-share surfaces as a `SharedMount` at the recipient's **home root** — **unambiguously
scan-discoverable, with none of the groupfolder inheritance complexity** — at the cost of a
single owning account and scattered mounts (no canonical tree). This is also the mechanism the
existing #150 seam can reach today by issuing OCS shares as it already acts-as-user. Net: the
group folder is the better *topology*; OCS shares are the better *proven discoverability* — S1
decides which becomes the primary.

---

## Part B — Index model

### B.1 What a list entry needs, and the cost problem (verified)

A viewer list **entry** needs `{id, title, dateLabel}` plus **either** `artifactPath` **or**
`audioPath` (`catalog.ts` `MeetingCatalogEntry` + `validateMeetingCatalogEntry`);
`speakerCount`/`segmentCount`/`digestDurationMs` are optional and hydrate lazily
(`dataProvider.ts` `loadMeetingSummary`). The question: produce those entries for N meetings
*without* pulling each `.opus`, and access-filtered. Two verified facts make the answer
non-obvious:

**A filename-only scan is insufficient.** Talk recordings are named by the operator's ULID job
id, so `describeMeeting` returns `title = "Untitled meeting"` (date only) for a plausible-ULID
name (`portable.ts`); the real title — the Talk room name — lives **only** in the OpusTags
`TITLE`, stamped into the bundle at promotion and propagated into the `.opus` at pack/publish
(`publish.go`, `pack.go`). (A non-ULID id renders as its raw id, but Talk recordings are
ULID-named.)

**Header-reading the `.opus` is expensive — up to ~1 MiB per meeting.** Every list field but
`segmentCount` exists as a plain OpusTag (`TITLE`, `DATE`, `CASSINI_MEETING_ID`,
`CASSINI_CREATED_AT`, `CASSINI_SPEAKER_COUNT`, `CASSINI_AUDIO_DURATION_MS`;
`manifest_v2.go`), **but** the gzipped manifest *and every transcript body* live in the **same**
Ogg comment-header packet, ffmpeg emits tags **alphabetically** so the small `TITLE`/counts land
**after** the bulky `CASSINI_*_PAYLOAD` chunks (`portable_meeting.go` `sort.Strings`), and the
viewer's parser needs the whole comment header intact or throws "truncated". So the loader
Range-reads `bytes=0-1048575` (1 MiB) and falls back to a full-file GET (`loadArtifact.ts`).
Worse, today's viewer derives all three counts from the **decompressed** manifest
(`loadArtifact.ts:218-222`), so it already pays that header read + gzip even for speaker count.

```text
  Ogg comment header packet (alphabetical tag order) — why a list read costs ~1 MiB
  ┌──────────────────────────────────────────────────────────────────────────────┐
  │ CASSINI_AUDIO_DURATION_MS · CASSINI_CREATED_AT · CASSINI_MEETING_ID            │  ← a few fields early
  │ CASSINI_PAYLOAD_000…NNN   (gzipped manifest)         ~KBs                       │
  │ CASSINI_TX_readable_PAYLOAD_000…NNN                  ~220 KB / transcript       │  ← the bulk; must be read
  │ CASSINI_TX_words_PAYLOAD_000…NNN                     ~220 KB / transcript       │    intact to parse header
  │ CASSINI_SPEAKER_COUNT · DATE · TITLE                 ← buried at the TAIL       │  ← the fields the LIST wants
  └──────────────────────────────────────────────────────────────────────────────┘
     ⇒ an N-meeting list built from headers ≈ N ranged GETs ≈ N MiB worst case.
```

### B.2 The three candidates

```text
(1) ROOT catalog.json, server-filtered     (2) PER-.opus SIDECAR             (3) NC-DB with RLS
    one cheap GET builds the list               <mtg>.opus                        oc_* tables +
    ┌───────────┐                               <mtg>.manifest.json  (~200 B)     row-level policy
    │catalog.json│ lists ALL                         │ tiny read per entry
    └─────┬─────┘ meetings                            │ NATURALLY access-filtered  ✗ INFEASIBLE:
          │ but to filter by access:                  │ by the per-user scan (the   NC exposes no RLS;
          │ N per-user probes (the O(N)               │ sidecar sits beside the     the ExApp has NO
          │ it was meant to save) + a                 │ .opus it accelerates)       NC-DB access at all
          │ fileid the catalog lacks
```

- **(1) Root `catalog.json`, server-filtered.** Cheapest *read* (one GET), but to access-filter,
  the operator must PROBE each entry's `.opus` as the caller — reintroducing the N per-user
  probes the index was meant to save, keyed on a fileid the catalog doesn't carry, and its answer
  is a publish snapshot that drifts from live sharing.
- **(2) Per-`.opus` `manifest.json` sidecar.** Co-upload a tiny `<meeting>.manifest.json`
  (`id, title, dateLabel, audioPath, speakerCount, durationMs`) beside each `.opus`. The list is
  a per-user PROPFIND to enumerate + a tiny read per entry (~hundreds of bytes, no gzip/Ogg
  parse), **naturally access-filtered** because the per-user tree *is* the access-scoped view. It
  must co-write/co-delete with the `.opus` and be delivered via **WebDAV PUT** (Talk `/store`
  rejects `application/json` — and rejects `.opus`; source-verified).
- **(3) NC-DB index with RLS — INFEASIBLE, on two independent grounds (both verified).** NC's
  data layer is Doctrine/DBAL and exposes **no row-level security** to apps; access is enforced in
  PHP joining `oc_share`/`oc_filecache`/`oc_mounts`. And the Cassini operator is an AppAPI ExApp:
  a separate Go process reaching NC only over HTTP, with **no NC-DB credentials**, shipping only
  `modernc.org/sqlite` against its **own** store whose `jobs` table has no owner/room/participant
  columns (owner+token live only in an opaque `talk_binding` JSON blob, Talk jobs only). A
  local-sqlite hint cache is buildable but non-authoritative and stale — never the access
  authority.

| Dimension | (1) catalog.json server-filtered | (2) per-.opus sidecar | (3) NC-DB RLS |
|---|:---:|:---:|:---:|
| List-build cost (N meetings) | E one GET… | E one PROPFIND + N×~200 B | — |
| …but true access-filter cost | ✗ N per-user probes | **E** the scan itself IS the filter | — |
| Access-filtered correctly | ~ snapshot; drifts | **E** per-user tree = access view | ✗ no RLS to derive from |
| Needs a fileid the source lacks | ✗ catalog has none | **E** sibling of the .opus | — |
| Concurrency on write | ✗ one mutable root, races | E independent per-meeting files | — |
| Feasible for an ExApp at all | E (exists today) | E (WebDAV PUT) | ✗ no NC-DB access; no RLS |
| Fails safe if metadata missing | ✗ absent = gone | **E** falls back to header read | — |

### B.3 Recommended — per-user scan is the authority, sidecar is the accelerator

Not any candidate in pure form; a hybrid framed as an accelerator over the per-user scan:

```text
  ACCESS AUTHORITY                          METADATA ACCELERATOR (non-authoritative)
  ────────────────                          ─────────────────────────────────────────
  per-user PROPFIND of the caller's NC       for each .opus the scan returns:
  Files for .opus meeting files                 sidecar present? ─yes─► read <mtg>.manifest.json
     │  the .opus is the UNIT NC shares/ACLs                       │       (~200 B, no gzip parse)
     │  authorise → the scan is the SOLE                           └─no──► header-read the .opus
     │  authority; needs NO Cassini ACL;                                   (the existing ~1 MiB Range —
     ▼  key on fileid, not path                                            slower but always correct)
  returns EXACTLY the caller's meetings      ⇒ list cost: N×~200 B (sidecars) not N×~1 MiB (headers)
```

1. **Access filter = the per-user scan for `.opus` files.** The `.opus` is the unit the ACL /
   share authorises, so the scan is the authority and needs no Cassini ACL; key on **fileid**
   (paths are user-movable).
2. **List metadata = the co-located sidecar when present, else a header-read.** The sidecar is a
   cache of the `.opus`'s own leading tags — never the access authority. A missing/under-shared
   sidecar degrades to the header read (the caller can read the `.opus` — it is the shared file),
   so a meeting **never silently disappears**. This makes sidecar share-parity a *performance*
   concern, not a correctness one — safe to adopt incrementally.
3. **Deliver the sidecar via WebDAV PUT**, and in the group-folder model grant it parity for free
   (folder-level ACL on `<meeting>/` covers `.opus` + sidecar; groupfolders share-to-chat, by
   contrast, shares the **file** only — verified — so the sidecar would *not* inherit a file
   share on the OCS-share path and needs its own share there).
4. **Keep `catalog.json` for the standalone / no-NC export only.** The `DataProvider` seam keeps
   `StaticCatalogProvider`/`catalog.json` for standalone + portable-share builds; retire it as the
   *in-NC org archive*. Do **not** server-side-filter `catalog.json` (it costs the same N per-user
   probes minus the sidecar's free parity, plus a fileid it lacks).
5. **Reject the NC-DB RLS index.** If volume ([D-498](https://linear.app/code-myriad/issue/D-498))
   ever demands a fast index, the ExApp's own sqlite may cache list rows as a **non-authoritative**
   hint, re-validated against NC Files per request — never the access authority.

### B.4 How the read side realises the per-user scan (a fork worth naming)

#150's read path is a **server-side proxy** (`ncFilesProxy`) that the operator runs as a fixed
`admin`. The minimal per-user step reuses that exact seam and swaps the impersonated user
**`admin → appapi.UserID(ctx)` (the caller)** — the operator PROPFINDs/GETs as the one caller at
read time, and deny-read-hides makes the result access-filtered. This keeps the client, CSP, and
routes unchanged (no browser `/remote.php/dav` call), which is why #150 chose the proxy shape.

```text
  browser (unchanged) ──GET /viewer|/published/{catalog.json,meetings/<id>.opus}──►
     operator viewerHandler/publishedHandler
        [#150]  ncFilesProxy AS admin  →  everyone sees everything
        [D-530] ncFilesProxy AS appapi.UserID(ctx) (the caller)  →  per-user, access-filtered
                + list from a per-user PROPFIND scan (+ sidecars) instead of the fixed catalog
```

A verified constraint shapes this: `dav RootCollection` enforces a **principal match** (the
authenticated UID must equal the target files UID), so the operator **cannot list all users from
one privileged session** — per-user read is inherently **per-caller act-as** (one re-auth + one
tree walk *per viewer*, at read time). That is fine for the viewer (one caller at a time) and is
another reason the list is built at read time per caller, not precomputed org-wide.

The alternative — a client-side `WebDavCatalogProvider` doing a browser PROPFIND with the user's
NC session — remains the eventual shape but is gated on the still-open embedded-CSP-vs-`/remote.php/dav`
spike; the server-side-proxy-as-caller path sidesteps it. Recommend the **server-side proxy-as-caller**
for the first per-user pass.

### B.5 Optional producer tweak — reorder OpusTags so list metadata leads

Today's alphabetical order buries `TITLE`/counts behind the payload chunks. If the producer
emitted a small **leading** metadata block (`TITLE`, `DATE`, `CASSINI_MEETING_ID`,
`CASSINI_SPEAKER_COUNT`, `CASSINI_AUDIO_DURATION_MS`) *ahead* of the `CASSINI_*_PAYLOAD` chunks, a
filename + few-KB read would list a meeting without a sidecar — shrinking the sidecar's role. It
is a ~10-line, backward-compatible producer change, **but** exploiting it needs a *new
short-header reader* in the viewer (today's parser needs the whole comment header). Land it as a
future-proofing follow-up; keep the sidecar as the near-term accelerator. Ranking:
**reorder > sidecar > custom WebDAV dead-props** (the last rejected: NC dead props are strictly
per-user, so "system sets once, everyone reads" is impossible; the `nc:acl-list` precedent is an
in-process Sabre plugin an external ExApp can't register).

---

## Part C — How the two models compose

```text
   group-folder advanced ACL                       per-meeting sidecar
   (deny-read HIDES paths)                          (~200 B beside each .opus)
            │  makes the per-user scan return ONLY   │  and makes each of those
            │  the meetings the caller is entitled   │  entries a tiny read instead
            │  to  (ACCESS solved by the scan) ─────►│  of a ~1 MiB header read
            │◄───────────────────────────────────── │  (DISPLAY solved cheaply)
            │  the .opus the scan authorises is the  │
            ▼  same file the sidecar accelerates     ▼
        ONE per-user PROPFIND of Recordings/  →  access-filtered, cheap meeting list
                    =  "NC Files as the single source of truth for access + display"
```

The ACL makes the scan **access-correct** with zero Cassini-side filtering; the sidecar makes it
**cheap**. Neither adds a second authorisation surface — the `.opus` in NC Files stays the sole
source of truth, exactly what D-416 asks for.

---

## Part D — Recommended path forward & handoff

- **Access:** system-owned *Recordings* group/Team folder + advanced ACL (direction).
  One-time `occ` setup; per meeting the ExApp acts as `cassini`, MKCOLs `<meeting-id>/`, PUTs
  `.opus` + sidecar, PROPPATCHes `nc:acl-list` granting `+read` to each publish-time participant
  (users + groups/circles; skip+warn guests/email/federated). ACL is static → **frozen at
  publish**, editable later. **Gate on spike S1** (per-user scan reachability recipe); fall back
  to per-user OCS shares from `cassini` if S1 disappoints or `groupfolders` is absent.
- **Index:** per-user PROPFIND `.opus` scan is the access authority; a co-located
  `<meeting>.manifest.json` sidecar is a non-authoritative accelerator (missing → header-read,
  never a disappearance). Read side: reuse #150's proxy, impersonating the caller. Keep
  `catalog.json` for the standalone/no-NC export. Reject NC-DB RLS. Optionally reorder OpusTags
  later.

**Downstream tickets this hands to:**

| Ticket | Picks up |
|---|---|
| [D-532](https://linear.app/code-myriad/issue/D-532) | Provision the dedicated `cassini` service account (recordings owner + ACL-manager identity this model needs) |
| [D-533](https://linear.app/code-myriad/issue/D-533) | Modular publish-sink adapters — the NC Files adapter is where the MKCOL/PUT/PROPPATCH/sidecar of this model land, without re-touching `runPublishJob` |
| (new access task) | Implement the group-folder ACL write path + per-caller read, after S1 |
| [D-482-4](https://linear.app/code-myriad/issue/D-482) | Viewer per-user scan provider (client-side `WebDavCatalogProvider`) once the CSP spike is resolved |

## Spikes to run before implementation

- **S1 — per-user scan reachability recipe (primary go/no-go, live).** The rule set (root /
  ancestor / `<meeting>/` grants) that makes a per-user PROPFIND scan return only the caller's
  meetings without sibling leakage, given groupfolders' inheritance. Decides group-folder ACL vs
  the OCS-share hedge. *Needs a live NC 32+.*
- **S2 — folder-lifecycle routes (resolved: `occ`-only).** `#[FrontpageRoute]`s not AppAPI-callable
  (`#[PasswordConfirmationRequired]` unsatisfiable headlessly) → one-time `occ`/admin provisioning;
  the per-meeting path is pure WebDAV. Ship the setup as an `occ` script.
- **S3 — live per-user scan visibility (C5b).** Confirm a per-user PROPFIND returns exactly the
  caller's entitled mounts and nothing else, across share / groupfolder / (accepted) federated
  types; confirm a Talk `TYPE_ROOM`-shared file surfaces for a participant. *Live.*
- **S4 — OpusTag reorder (optional, later).** Backward-compatible producer change; needs a new
  short-header viewer reader before it pays off.

## Verification note (what is settled vs live)

This proposal graduated from spike `_ivans-notes/development/530-.../exploration.md` after an
adversarial re-verification (7 clusters, verify+audit). **Settled by source-reading:** the whole
index cost model and RLS-infeasibility (this repo); deny-read-hides, membership-gated mount,
delegated-manager PROPPATCH, `acl_default_no_permission` base-0, AppAPI act-as-user,
principal-match on `dav RootCollection`, `/store` format rejection, share-chat-is-file-scoped,
and the #150 seam (upstream source + this repo). **Requires a live instance:** the exact per-user
scan reachability recipe (S1 — the one thing that gates the access model) and the end-to-end
per-user scan visibility (S3).

## References

**Cassini repo (code-verified).** `cassini-viewer/src/viewer/`: `catalog.ts`
(`MeetingCatalogEntry`, `validateMeetingCatalogEntry`), `dataProvider.ts` (`DataProvider`,
`StaticCatalogProvider`, lazy summary), `loadArtifact.ts` (`bytes=0-1048575`, full-GET fallback,
counts from decompressed manifest), `portable.ts` (`describeMeeting` "Untitled meeting", truncated
guards). `cassini-go-recorder/internal/portable/`: `manifest_v2.go` (plain list tags + payload
chunks in one packet), `manifest.go` (no room/token field). `cassini-go-recorder/internal/cassini/`:
`portable_meeting.go` (id = PCM-hash; alphabetical tag emit), `publish.go`/`pack.go` (TITLE
propagated from bundle). `cassini-operator/internal/operator/`: `webdav_upload.go` (the act-as-user
seam), `run.go`/`publish_runtime.go`/`exapp.go` (wiring), migrations (`jobs` has no
owner/room/participant; `talk_binding` blob), `go.mod` (`modernc.org/sqlite` only).

**Upstream (WebFetch-verified).** `nextcloud/groupfolders`: `lib/ACL/ACLStorageWrapper.php`
(deny-read hides), `lib/Mount/MountProvider.php` (`&= aclRootPermissions`, AND-only),
`lib/Folder/FolderManager.php` (`getFoldersForUser` groups+circles; `computeCanManageACL`),
`lib/ACL/ACLManager.php` (`getBasePermission`, no upward propagation), `lib/DAV/ACLPlugin.php`
(`nc:acl-list` PROPPATCH, own-read guard), `src/services/acl.ts` (PROPPATCH XML);
[no OCS ACL API #1256](https://github.com/nextcloud/groupfolders/issues/1256). `nextcloud/spreed`:
`lib/Service/RecordingService.php` (`DEFAULT_ALLOWED_RECORDING_FORMATS`, `shareToChat(fileId)`),
`lib/Controller/RoomController.php` (`getParticipants`, actor types, lobby/guest gates).
`nextcloud/app_api`: `lib/Service/AppAPIService.php` (`finalizeRequestToNC` → `setUser`).
`nextcloud/server`: `apps/dav/lib/Files/RootCollection.php` (principal match),
`lib/public/IDBConnection.php` (Doctrine/DBAL, no RLS).

**Linear.** [D-521](https://linear.app/code-myriad/issue/D-521) (goal),
[D-529](https://linear.app/code-myriad/issue/D-529) (first pass / #150),
[D-530](https://linear.app/code-myriad/issue/D-530) (this spike),
[D-532](https://linear.app/code-myriad/issue/D-532) (service account),
[D-533](https://linear.app/code-myriad/issue/D-533) (publish sink adapters),
[D-416](https://linear.app/code-myriad/issue/D-416) (epic constraints).
