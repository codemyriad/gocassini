# Shaping: Storage & access control that least-surprises a Nextcloud admin

Status: shaping (not a committed plan). **Supersedes decision D10** in
[`installable-nextcloud-app.md`](installable-nextcloud-app.md) (org-wide archive):
the org-wide archive is dropped in favour of Nextcloud-native, scoped delivery
(see §4). Builds on the "Future: filter `/published/catalog.json` by attendance,
integrate with NC sharing" note at `installable-nextcloud-app.md:313`.

This document records the current behaviour (code-verified), the gap against
what a Nextcloud admin expects from a meeting recorder, and a target design with
a phased path. Two product decisions are already made (see *Appetite*).

---

## 1. Why now

An admin installs Cassini expecting it to behave like a Talk meeting recorder.
Talk's *native* recording lands in the **moderator's Files**, is **quota-counted
and backed up** with everything else, and is **visible only to the owner plus
people they share it with**. Cassini today diverges from all three. The
divergence is intentional for v1 (D10) but it is the single biggest source of
admin surprise, and it touches privacy/compliance, so it needs a deliberate
target — not an accreted one.

## 2. Current state (code-verified)

### 2a. Storage — a recording ends up in *two* places

1. **The ExApp's own private volume** — the durable home for everything.
   AppAPI creates one named volume `nc_app_gocassini_data` (mounted at
   `/nc_app_gocassini_data`, exposed as `APP_PERSISTENT_STORAGE`). The operator
   redirects all data roots under it (`exapp.go:238-246`, wired `run.go:273-285`):
   - `…/operator/jobs.sqlite3` — job metadata only (**paths, not bytes**;
     schema: `migrations/0001_initial_jobs.up.sql`)
   - `…/operator/jobs/` — raw `recording.mkv` + meeting bundles
     (`meeting.webm` + `transcript.words.v1.json` + `manifest.json` always;
     `transcript.readable.v1.json` + `captions.vtt` + `summary.md` only when an
     LLM is configured — those three are co-dependent)
   - `…/site/published/` — the browsable viewer site (`catalog.json` + assets)
   - `…/operator/app-state.json` — AppAPI lifecycle state

2. **Nextcloud's own storage, via Talk.** After capture the operator uploads the
   **raw multitrack `recording.mkv`** as a multipart POST to spreed's
   `…/ocs/v2.php/apps/spreed/api/v1/recording/{token}/store` with the `owner`
   field set (`talk_backend.go:578-604`). Spreed files it into the **room
   owner's `Talk/Recordings` folder** — WebDAV-visible, quota-counted, in normal
   Files backups. Best-effort: never fails the record stage, retried with
   bounded backoff, re-attempted on rerun while `talk_delivered_at` is unset.

**Consequences:**
- The two copies **diverge in content**: only the raw `.mkv` reaches Files. The
  transcript, summary, captions and the browsable site live **only** in the
  ExApp volume — which is **not** in normal Files backups and **not**
  quota-counted.
- The Files copy is the **raw multitrack `.mkv`**, not the clean
  `meeting.webm` / portable `.opus`. The "nice" artifact never reaches Files.
- Storage is **per-job** and grows **unbounded**: no TTL, no pruning, no
  age/size cap anywhere. Removal is either manual-per-file (the Files `.mkv`) or
  `occ app_api:app:unregister --rm-data` (all-or-nothing wipe of the whole
  volume).
- Atomic promotion is real, but the published-site swap does two sequential
  renames, so `/published` is briefly **absent** (transient 404) during each
  promotion — not a partial read, a momentary outage.

> Note: PHASE1-REVIEW-REPORT issue #7 ("data on overlayfs, destroyed on update")
> describes the state **before** the `APP_PERSISTENT_STORAGE` redirect landed.
> The redirect is in the tree and documented; treat #7 as resolved (verify the
> running image carries the build).

### 2b. Access control — gated by identity, open by entitlement

Two separate auth systems:
- **Browser traffic** is proxied through Nextcloud; AppAPI authenticates each
  request (`AUTHORIZATION-APP-API` = base64(userId:appSecret), verified in
  `appapi/middleware.go:75-148`) and enforces the manifest's per-route level
  *before* it reaches the operator: `ADMIN` (`/control-panel/*`,
  `/operator/*`), `USER` (`/viewer/*`, `/published/*`), `PUBLIC`
  (`/api/v1/*` Talk backend).
- **The Talk backend** is machine-to-machine, authed by Talk's own HMAC-SHA256
  over `random+body` (`talk_backend.go`), independent of any NC session.

The load-bearing fact: once past the proxy, **any logged-in Nextcloud user can
browse and play every published meeting.** `publishedHandler` is a bare
`http.FileServer` with zero per-request authorization (`exapp.go:645-653`); the
catalog and viewer carry no `owner`/`room`/`acl` fields; `ListJobs()` returns
all jobs to any admin. No room-membership or attendance check exists. This is
**intentional v1 design** (D10), documented honestly at
`docs/exapp-install.md:350-352` — but it is the opposite of what a Talk admin
assumes (owner + explicit shares only).

### 2c. The real blocker: the data model has no notion of entitlement

The `jobs` table stores **no owner, no room token, no participant list** — only
`request_json`, stage/state, timestamps, and artifact paths (migrations
0001–0004). `RoomToken`/`Owner` exist only **transiently in memory** during a
recording (`talk_backend.go:138-139, 248-249`) and inside the `request_json`
blob. So even a minimal "show me only my meetings" filter has nothing to filter
*on* today. The auth layer is *ready* — `appapi.UserID(ctx)` already exposes the
caller's NC id — but there is no per-recording "who may see this" to compare it
against. **Scoping is a data-model problem before it is an access problem.**

Worse for participant-scoping specifically: Cassini joins as an anonymous guest,
so it never receives the room's participant list. The only identity it reliably
has is the `owner` Talk hands it at record start.

## 3. Is it documented?

Storage *location* — yes and accurate (`docs/exapp-install.md:307-332`). Org-wide
access — yes and honest (`:350-352`). **Everything an admin must operate around —
no:**
- the **second copy in Files** is never mentioned in admin docs (only in the
  internal review report);
- **no retention/lifecycle** story (answer is "forever");
- **no backup/DR** guidance for the ExApp volume — the *only* home for
  transcripts/summaries/site;
- **no GDPR/erasure path, no audit logging, no encryption-at-rest** note;
- **no "where do I find a recording"** UX explanation (Files vs viewer vs both);
- **no secret-rotation** guidance; `--rm-data` implication stated but not its
  default.

## 4. Appetite & decisions

**Direction (set earlier):**
- **Storage → Nextcloud's own storage.** Recordings *and* the rich artifacts
  should live in Nextcloud Files (a Recordings folder), so they are
  Files-visible, quota-counted, backed up, and natively shareable.
- **Access → owner/participant scoping.** A recording should be visible to the
  call owner and the people they share to (like native Talk recording), not the
  whole org.

**Resolved this round (the §9 questions):**
- **Delivery = mirror native Talk (option C).** Land the rich bundle in the
  **owner's `Talk/Recordings`** and post a notification *offering* to share it
  into the conversation — exactly what native Talk recording does. Owner-gated by
  default; one click distributes to the conversation. **Auto-share into the
  conversation is an opt-in** (per-room or admin policy), not the default.
- **Org-wide archive = dropped.** No operator-served, browse-every-meeting
  surface. There is **one** access model: Nextcloud-native, scoped through Files
  + Talk sharing. This **reverses D10** and means the `info.xml` pitch
  ("any logged-in user can browse") must change.

## 5. Target design

**One access model: Nextcloud is the source of truth.** Cassini already pushes
the raw `.mkv` through Talk's blessed store endpoint into the owner's Files. The
target extends that path to the *rich* artifacts and **retires the operator's
org-wide serving surface entirely**:

1. **Deliver the rich bundle to the owner's Files (option C).** After build,
   write the clean meeting bundle — playable audio + transcript + summary (+ the
   portable `.opus` and/or a self-contained viewer) — into the owner's
   `Talk/Recordings`, and post a Talk notification offering to share it into the
   conversation. Access is then plain Nextcloud file/share ACL: owner + whoever
   they share to (or all conversation members, if auto-share is enabled),
   owner-deletable via Files, quota-counted, backed up, shareable with
   links/expiry/passwords, all under the admin's existing sharing/audit tooling.

2. **Retire the org-wide archive.** Remove the operator as a content-serving
   origin: drop the `published`-as-org-archive behaviour and the global catalog,
   and the USER `/published/*` + viewer-as-archive routes from the manifest. The
   operator's volume keeps only *working* artifacts (in-flight jobs, the source
   of the delivered bundle), not a durable public library.

3. **Viewer becomes per-meeting, entitlement-resolved.** The polished
   playback/transcript/search/summary SPA is worth keeping, but it renders a
   **single meeting the caller already has access to**, not a global catalog.
   Two ways to deliver it (open sub-question, §9):
   - a **self-contained viewer artifact** delivered into Files (open it like any
     file; zero operator serving), or
   - an **app viewer route** that takes a file/share reference and loads the
     meeting from the *caller's own* Files via WebDAV (keeps one shared viewer
     build; needs per-user file resolution).

This collapses storage, ACL, deletion, and backup onto Nextcloud's own
mechanisms — the simplest possible model, and the least surprising.

## 6. Hard parts / rabbit holes

- **Capturing the delivery target at record time.** Persist `owner` +
  `room_token` on the job (small migration; both are in hand transiently). These
  drive where the bundle is delivered and the deletion mapping. Note: a true
  *attendee* list is unavailable to a guest recorder — which is fine, because
  access now rides on Nextcloud sharing (owner + their share / conversation
  members), not an operator-computed attendee ACL.
- **The viewer through Files** (the §9 sub-question): self-contained per-meeting
  artifact (simplest, but duplicates viewer assets per recording) vs. an app
  viewer route that resolves the caller's file via WebDAV (one build, per-user
  resolution). Retiring the global `/published` catalog removes the org-wide
  surface either way.
- **Deletion / erasure.** Owner-initiated delete must remove the delivered Files
  bundle (+ unwind any share) *and* the operator's working copy + job record, or
  deletion is a lie. Core GDPR primitive; needs a real delete endpoint.
- **Raw `.mkv` vs clean bundle in Files.** Today the raw multitrack `.mkv`
  already lands in `Talk/Recordings`. Once the clean bundle is delivered, decide
  whether the raw `.mkv` stays (two files, confusing) or is superseded/omitted.

## 7. Phased path

**Uncontroversial now (no further product decision needed):**
1. **Docs honesty pass** — document the two-copy reality (raw `.mkv` in
   `Talk/Recordings`), state retention = unbounded, add ExApp-volume backup/DR
   guidance, and announce that the org-wide archive is being removed (so no admin
   builds on it). *(Lowest risk; can land immediately.)*
2. **Persist `owner` + `room_token` per job** — already satisfied: both live in
   the `talk_binding` column (migration `0004`), read via `Store.JobOwner`. No
   new migration needed.

**The agreed design (§4/§5), in dependency order:**
3. **Native delivery of the rich bundle** to the owner's `Talk/Recordings` +
   Talk "offer to share" notification (auto-share behind an opt-in).
4. **Retire the org-wide surface** — drop the global catalog / `published`-as-
   archive behaviour and the USER `/published/*` + viewer-archive routes from the
   manifest; update the `info.xml` description (no more "any logged-in user can
   browse"); revisit the "Cassini" navigation entry. *(This is the exposure fix —
   it replaces the earlier "owner-only filter" interim, which is moot once the
   surface is gone.)*
5. **Per-meeting viewer consumption** — self-contained artifact or app viewer
   route (§9 sub-question).
6. **Delete endpoint** (erasure) — removes the Files bundle + share + working
   copy + job record.
7. **Retention controls** — admin-configurable max-age/quota with pruning.

(The earlier "fix the `/published` 404 window" item is dropped — retiring the
published surface removes the promotion-swap concern.)

## 8. Out of scope / no-gos

- Re-implementing Nextcloud's *full* sharing/permission model inside the operator
  (a single owner-equality check for the interim scoping is fine; group/share
  graphs are not).
- An org-wide "browse every meeting" archive — **decided against** (dropped for
  simplicity; not even as a Group Folder for now).
- Server-side encryption-at-rest (defer; note it as a gap).
- Non-public room recording (separate tracked limitation).

## 9. Open questions (remaining)

The two big ones are resolved (§4): delivery = mirror native Talk (C); org-wide =
dropped. Remaining, narrower:

- **Viewer delivery mechanism** — self-contained per-meeting viewer artifact in
  Files, or an app viewer route that loads the caller's file via WebDAV? (The
  main remaining design fork.)
- **Bundle location** — `Talk/Recordings` (alongside the `.mkv`, least-surprise)
  or a dedicated `Cassini/` folder (tidier)?
- **Raw `.mkv`** — keep it in Files alongside the clean bundle, or supersede/omit
  it to avoid two files per meeting?
- **Auto-share opt-in granularity** — per-room toggle, admin-global setting, or
  per-recording prompt?
- **Retention default** once pruning exists — off / N days / size cap?

---

## 10. Build log (what shipped vs. shaped)

This round delivers the **immediate, fully-testable exposure fix** and leaves the
larger Talk-integration work shaped:

- **Shipped:** **owner-scoped** the USER-facing published archive + viewer
  catalog so a user sees only the recordings they own (not the whole org).
  - No migration was needed: the owner is already persisted per job in the
    `talk_binding` column (migration `0004`). `Store.JobOwner` derives it.
  - `publishedHandler` (`cassini-operator/internal/operator/exapp.go`) now
    filters `catalog.json` to the caller's meetings and 404s another owner's
    per-meeting assets; site-shell paths and the standalone/dev mode (no AppAPI
    identity) are served unscoped. Caller identity comes from the existing
    `appapi.UserID(ctx)`.
  - Tests: `exapp_published_scope_test.go` (catalog filtering, asset gating,
    site-shell passthrough, unscoped-without-identity, `Store.JobOwner`). Runs
    in CI under `go test -race`.
  - Docs/manifest updated to drop the org-wide claim (`appinfo/info.xml`,
    `docs/exapp-install.md`). This realizes the "owner-gated by default"
    decision within today's architecture and removes the #1 risk (org-wide
    exposure) — no live Talk server required.
- **Shaped, not built (needs a live Talk/Nextcloud to build+verify):** native
  delivery of the rich bundle into the owner's Files via Talk + offer-to-share,
  auto-share opt-in, the delete/erasure endpoint, and retention controls
  (phases 3 and 5–7 above).
