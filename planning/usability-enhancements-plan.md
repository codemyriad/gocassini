# Cassini Usability Enhancements Plan

Date: 2026-03-11

## The User Model Cassini Should Optimize For

Cassini should feel like a product for two simple jobs:

1. record a meeting into one portable file
2. keep a folder of those files and browse/share them easily

Everything else should be implementation detail.

That means the user-facing product should stop teaching:

- `.run`
- `.meeting` directories
- `.site`
- "build" vs "publish" as separate concepts

Those may still exist internally, but they are not the right mental model for normal users.

## The Ideal Shape

Cassini should revolve around two user-facing objects:

### 1. Meeting File

One file per meeting. Portable. Copyable. Shareable.

Example:

```text
2026-03-11 Weekly Sync.opus
```

This is the thing a user can:

- keep in Dropbox, iCloud, Syncthing, or a normal folder
- email or upload
- drag into an archive
- open in Cassini
- share as a single meeting

### 2. Archive Folder

A normal directory full of meeting files.

Example:

```text
My Meetings/
  2026-03-11 Weekly Sync.opus
  2026-03-12 Design Review.opus
  2026-03-15 Customer Demo.opus
```

This is the thing a user can:

- browse locally in the browser
- sync to another machine
- publish as a shareable site
- back up as ordinary files

## Recommendation: Use One Portable Meeting File

### Decision

Cassini should adopt one canonical user-facing file format:

- `*.opus` with embedded Cassini meeting metadata

That file should be a self-contained meeting package. It is the **only** durable,
published Cassini contract; everything else (`.run`, `.meeting`, their
`cassini.json`/`manifest.json` bundle manifests) is transient internal plumbing,
not a deliverable.

### Recommended technical shape

The canonical meeting file should be a normal `.opus` audio file with embedded
Cassini metadata.

The normative format is defined in:

- [Portable meeting format](../docs/portable-meeting-format.md)
- [cassini-portable-meeting-manifest-v2.schema.json](../spec/cassini-portable-meeting-manifest-v2.schema.json) (the format the producer now emits)
- [cassini-portable-meeting-manifest-v1.schema.json](../spec/cassini-portable-meeting-manifest-v1.schema.json) (read-only; older files)

## Product Surface Users Should See

Cassini should present these primary commands:

```bash
cassini record
cassini browse
cassini add
cassini share
cassini inspect
cassini doctor
```

## Current Implemented Slice

The first user-centered slice now exists in the CLI:

```bash
cassini record --call "$CALL_URL" --out "./My Meetings/Weekly Sync.opus"
cassini build /path/to/meeting.mkv --out "./My Meetings/Imported Meeting.opus"
cassini inspect "./My Meetings/Weekly Sync.opus"
```

What is still pending from the ideal shape:

- `cassini record --into "./My Meetings"`
- `cassini browse`
- `cassini add`
- `cassini share`

### Record

Record a new meeting directly into a meeting file.

Examples:

```bash
cassini record --call "$CALL_URL" --out "./2026-03-11 Weekly Sync.opus"

cassini record --call "$CALL_URL" --into "./My Meetings"
```

`--into` should auto-name the file from date + title.

The default user path should be:

1. capture
2. transcribe
3. package
4. write one `.opus` file

No visible `.run` directory unless the user asks for debug retention.

### Browse

Browse an archive folder locally in the browser.

Example:

```bash
cassini browse "./My Meetings"
```

What it should do:

- scan `.opus` files
- build or refresh a lightweight local index
- start a local browser UI
- let the user search, filter, and play meetings

Important:

- browsing should work directly from the archive folder
- users should not need a separate "publish" step just to look at their own archive

### Add

Add an existing meeting file to an archive.

Example:

```bash
cassini add "./Downloads/Customer Demo.opus" "./My Meetings"
```

What it should do:

- validate the file
- copy or move it into the archive
- refresh archive index metadata

### Share

Share either one meeting or a whole archive.

Examples:

```bash
cassini share "./My Meetings/2026-03-11 Weekly Sync.opus" --out ./shared/weekly-sync

cassini share "./My Meetings" --out ./shared/archive
```

What it should emit:

- a static HTML package
- browser-ready assets
- no special runtime needed

This is where the current `publish` concept belongs, but "share" is the better user verb.

## How The Internals Should Map

The current pipeline can remain, but it should become hidden plumbing:

- `.run` becomes a temporary working bundle
- `.meeting` directory becomes transient build scratch — an intermediate the build stage packs into one `*.opus` file, not a deliverable; its bundle manifests are not a published contract
- `.site` remains a generated export target for sharing/browsing

In other words:

```text
user-facing:
  .opus meeting file -> archive -> browser/share

internal:
  run dir -> meeting dir -> site dir
```

That is the right separation.

## Recommended Default Behaviors

### 1. Record directly into an archive

This should be the easiest happy path:

```bash
cassini record --call "$CALL_URL" --into "./My Meetings"
```

Result:

- creates one validated `.opus` file
- adds it to the archive
- optionally refreshes archive index

### 2. Browse without publishing

This should also be first-class:

```bash
cassini browse "./My Meetings"
```

The tool can generate cache/index files behind the scenes, but the user should
not be asked to reason about them.

### 3. Share by exporting static HTML

When a user wants to send something to someone else:

```bash
cassini share "./My Meetings/Design Review.opus" --out ./public/design-review
```

Or:

```bash
cassini share "./My Meetings" --out ./public/meetings
```

## What The Browser Experience Should Feel Like

An archive UI should feel like a small meeting library:

- list of meetings
- title, date, duration
- transcript search
- speaker list
- one-click play
- one-click download original `.opus` file
- one-click export/share

Single meeting page:

- audio player
- transcript panel
- speaker navigation
- captions
- share/download actions

## Naming And Concept Cleanup

Recommended user-facing language:

- "meeting file"
- "archive"
- "browse"
- "share"

Recommended internal-only language:

- run bundle
- meeting bundle directory
- site bundle

Specific recommendation:

- stop teaching `build` and `publish` as the main user verbs
- keep them only as expert / implementation commands until replaced

## Phased Product Plan

### Phase 1: Introduce the real user objects

- define the portable `.opus` file format
- make current `.meeting` directory pack into that file
- add `cassini inspect file.opus`

### Phase 2: Make recording land in the real object

- add `cassini record --out file.opus`
- add `cassini record --into archive-dir`
- keep `.run` only as hidden temporary work unless `--keep-run`

### Phase 3: Make archives first-class

- add `cassini browse archive-dir`
- add `cassini add file.opus archive-dir`
- maintain archive index automatically

### Phase 4: Rename sharing UX

- add `cassini share`
- demote `publish` to compatibility / advanced use

### Phase 5: Remove pipeline-shaped onboarding

- root docs teach only:
  - record
  - browse
  - share
- deeper docs keep internal pipeline language for developers only

## Bottom Line

The ideal Cassini shape for users is:

- one portable `.opus` file per meeting
- one ordinary folder as an archive
- one command to record into that archive
- one command to browse it
- one command to share one meeting or the whole archive

That is much closer to what users expect from files they own, move, back up,
and pass around than the current visible `run -> meeting -> site` pipeline.
