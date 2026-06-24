# D-425 Format-Consolidation — Retirement Inventory

This is the checklist the final removal PR(s) will execute. One canonical
user-facing format survives: the portable `.opus` file
(`org.cassini.portable-meeting/2`). The two bundle manifest schemas and the
`.meeting`-directory-as-contract should stop being durable/published.

This PR (D-431) makes the **safe, non-breaking** subset of changes (docs +
this inventory) and verifies the recorder already emits v2 only. It removes
nothing. Each reference below is tagged with WHEN it becomes removable.

## Tag legend

The three predecessor tickets each retire one axis. Mapping (per epic):

- **A = D-428** — retire `cassini.meeting.v1` (the `cassini.json` bundle
  envelope) as a durable contract.
- **B = D-429** — retire `cassini.meeting-artifact.v1` (the bundle's
  `manifest.json` artifact manifest) as a durable contract.
- **C = D-430** — retire the `.meeting` directory / `artifactPath` /
  `loadArtifactFromDirectory` "unpacked bundle directory is a published thing"
  contract, leaving `.opus` as the only viewer/publish input.

Each bullet is tagged:

- `removable-after: A` | `B` | `C` — delete/refactor it once that ticket lands.
- `keep: transient-scratch-only` — the `.meeting` bundle stays as an internal
  build intermediate even after this epic; this reference is legitimate
  build/staging plumbing and is only deleted if/when the `.meeting`
  intermediate itself is removed (out of scope for A/B/C — these are the
  "scratch is fine" cases the removal PR should NOT touch).

> Note on `removable-after: C`: many `.meeting` path/loader references are the
> publish/viewer **input contract** (a directory you can hand to publish or the
> viewer). Those go with C. The `.meeting` paths that are purely internal
> build-staging plumbing are `keep: transient-scratch-only`.

---

## Recorder — `cassini-go-recorder`

### A — `cassini.meeting.v1` (cassini.json bundle envelope)

- `cassini-go-recorder/internal/cassini/meeting_bundle.go:12` — `const meetingManifestVersion = "cassini.meeting.v1"` (schema-version constant) — `removable-after: A`
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:236` — `meta.Version = meetingManifestVersion` written into `cassini.json` — `removable-after: A`
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:19` — `type MeetingBundleManifest struct` (the `cassini.json` schema) — `removable-after: A`
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:28` — `ArtifactManifest string json:"artifact_manifest"` field (pointer from envelope to artifact `manifest.json`) — `removable-after: A`
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:69-72` — `FinalizeMeetingBundle` sets `meta.ArtifactManifest = "manifest.json"` (couples envelope A to artifact B) — `removable-after: A`
- The bundle **directory** lifecycle that writes/reads `cassini.json` (`PrepareMeetingBundle:38`, `FinalizeMeetingBundle:59`, `LoadMeetingBundle:77`, `UpdateMeetingBundleStatus:198`, `UpdateMeetingBundleSource:209`, `readMeetingManifest:222`, `writeMeetingManifest:234`, plus `MeetingBundle:14`/`LoadedMeetingBundle:32` types) — these implement the `cassini.json` envelope contract; once `.meeting` is no longer a publish/viewer input (C) and the envelope is no longer durable (A) they go. The directory itself remains transient scratch unless explicitly removed: `keep: transient-scratch-only` for the staging lifecycle, `removable-after: A` for the *manifest schema/version* it serializes.
- Callers staging the bundle (build/resume): `internal/cassini/build.go:83,90,92,100,266,273,277,281`; `internal/cassini/portable_resume.go:97,100,110,120,182,191,195,204,208,214` — these drive the internal build intermediate — `keep: transient-scratch-only`

### B — `cassini.meeting-artifact.v1` (bundle manifest.json)

- `cassini-go-recorder/internal/transcribe/format.go:304` — `Kind: "cassini.meeting-artifact.v1"` written into the bundle `manifest.json` (the sole producer of this schema string) — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:52` — `type portableMeetingArtifact struct` (decodes the bundle `manifest.json`) — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:258` — `artifactPath := filepath.Join(rootDir, "manifest.json")` (reads the artifact manifest in `loadPortableMeetingSource`) — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:260` — `os.ReadFile(artifactPath)` of the bundle `manifest.json` — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:268,277,292,296,300,305` — consume `artifact.Files.{Audio,Transcript,ReadableTranscript,DisplayTranscript,Summary,Transcripts}` from the artifact manifest — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:317` — assigns `Artifact: artifact` into `portableMeetingSource` — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:352,370,395` — `artifactPath string` params of `loadPortableSummaryMarkdown` / `loadPortableReadableTranscript` / `loadPortableDisplayTranscript` (named after the artifact-manifest file entries; param name is incidental but the call chain is fed by the artifact manifest) — `removable-after: B`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:465,473,483,487` — read `source.Artifact.{Source.Basename,GeneratedAt,Source.RecordedAtLocal}` while building the `.opus` manifest — `removable-after: B` (these values must be sourced from somewhere else, e.g. the `.run` or probe, once `manifest.json` is gone)
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:125` — `maybeAddMeetingFile(files, rootDir, "artifact_manifest", "manifest.json")` (envelope lists the artifact manifest among files) — `removable-after: B`
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:136,154-179` — `validateReadyMeetingBundleContents` requires/validates `manifest.json` and its `files`/`files.transcripts` — `removable-after: B`

### C — `.meeting` directory / publish+inspect input contract

- `cassini-go-recorder/internal/cassini/publish.go:163` — `strings.TrimSuffix(name, ".meeting")` (derive meeting id from bundle dir name) — `removable-after: C`
- `cassini-go-recorder/internal/cassini/publish.go:202` — `filepath.Ext(root) == ".meeting"` (treat a `.meeting` dir as a publish input) — `removable-after: C`
- `cassini-go-recorder/internal/cassini/publish.go:232` — `filepath.Ext(candidate) == ".meeting"` (scan a directory of `.meeting` bundles) — `removable-after: C`
- `cassini-go-recorder/internal/cassini/publish.go:146,154,178,219,249` — publish consumes `LoadedMeetingBundle` / `validateReadyMeetingBundleContents` / `LoadMeetingBundle` / `meetingBundleStatusReason` as its input contract — `removable-after: C`
- `cassini-go-recorder/internal/cassini/cli.go:584` — `filepath.Ext(root) == ".meeting"` in `inspect` (treats a `.meeting` dir as inspectable) — `removable-after: C`
- `cassini-go-recorder/internal/cassini/cli.go:470,474,514` — `LoadMeetingBundle` + `inspectMeetingBundle` (inspect a `.meeting` bundle) — `removable-after: C`
- `cassini-go-recorder/internal/cassini/cli.go:408` — help text: ".run and .meeting outputs remain available for debugging" — `removable-after: C` (doc string)
- `cassini-go-recorder/internal/cassini/cli.go:419` — help example `cassini inspect ./meetings/meeting.meeting` — `removable-after: C` (doc string)
- `cassini-go-recorder/internal/cassini/build.go:41` — flag help: `"output .meeting bundle directory or portable .opus file"` — `removable-after: C` (doc string)
- `cassini-go-recorder/internal/cassini/build.go:50,51` — help examples writing `--out ./meetings/meeting.meeting` — `removable-after: C` (doc string)

### keep — transient build-scratch `.meeting` plumbing (do NOT remove for A/B/C)

- `cassini-go-recorder/internal/cassini/portable_resume.go:32` — `MeetingDir: filepath.Join(rootDir, "meeting.meeting")` (internal portable-work staging dir; lives under `.cassini-work`, never a deliverable) — `keep: transient-scratch-only`
- `cassini-go-recorder/internal/cassini/build.go:169` — `packMeetingBundle(ctx, bundle.RootDir, outPath, ...)` (consumes the scratch bundle to produce the `.opus`; stays until the `.meeting` intermediate is replaced) — `keep: transient-scratch-only`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:195` — `func packMeetingBundle(...)` definition (the pack-into-`.opus` step) — `keep: transient-scratch-only`
- `cassini-go-recorder/internal/cassini/portable_meeting.go:201,252` — `loadPortableMeetingSource` call + def (reads the scratch bundle to feed the packer) — `keep: transient-scratch-only` (the bundle-reading lives, but the `manifest.json` decode inside it is `removable-after: B` above)
- `cassini-go-recorder/internal/cassini/meeting_bundle.go:118-133` — `meetingBundleFiles` / `maybeAddMeetingFile` (enumerate scratch bundle contents; the `artifact_manifest` entry at :125 is B above) — `keep: transient-scratch-only`

### Not part of the contract (false positives — do not touch)

- `cassini-go-recorder/internal/inspect/inspect.go:63-64` — `artifactPath, ok := detectSessionArtifactPath(path)` — this is a **session.json** path, NOT the bundle artifact manifest. Unrelated identifier collision. `keep: not-in-scope`

---

## Operator — `cassini-operator`

### A — `cassini.meeting.v1` (cassini.json envelope reader)

- `cassini-operator/internal/operator/meetingbundle.go:11` — `type MeetingBundleManifest struct` (operator's copy of the `cassini.json` envelope schema) — `removable-after: A`
- `cassini-operator/internal/operator/meetingbundle.go:24` — `LoadMeetingBundleManifest` reads `<dir>/cassini.json` — `removable-after: A`
- `cassini-operator/internal/operator/build_runtime.go:134` — `LoadMeetingBundleManifest(meetingPath)` (reads envelope for build-failure detail/stage) — `removable-after: A`

### C — `.meeting` directory paths (operator artifact library contract)

- `cassini-operator/internal/operator/attempt_paths.go:26` — `canonicalMeetingPath` builds `current/<job>.meeting` — `removable-after: C`
- `cassini-operator/internal/operator/attempt_paths.go:38` — `attemptMeetingPath` builds `runs/<job>--attempt-NNN.meeting` — `removable-after: C`
- `cassini-operator/internal/operator/artifact_promotion.go:23` — `promoteMeetingBundle` promotes an attempt `.meeting` into `current/<job>.meeting` — `removable-after: C`
- `cassini-operator/internal/operator/build_runtime.go:65` — `promoteMeetingBundle(...)` call site — `removable-after: C`

> Operator note: the operator stores `.meeting` directories as canonical
> reusable build artifacts (`current/<job>.meeting`). If the operator keeps
> `.meeting` as its internal canonical build output (transient relative to the
> published `.opus`), retag these `keep: transient-scratch-only` during C
> planning. They are listed under C because today they form the publish input
> chain (`current/<job>.meeting` → publish).

> Docs note (D-431, not edited — pre-cleanup operator-library wording): several
> dev docs describe the operator's `current/` library with the phrase "canonical
> `current/<job>.meeting`" / "one canonical `.meeting` per job", parallel to the
> canonical `.run` pointer. This is the operator-`current/`-library sense (the
> single retained per-job copy / promotion pointer), **not** a claim that
> `.meeting` is the canonical user-facing format. D-431 left these as-is (the
> `.run` half stays canonical regardless); when C lands and the operator stops
> staging/publishing `.meeting`, update or drop the `.meeting` half here too:
> `dev-docs-wip/docs/reference/artifacts-and-filesystem.md:190,196`,
> `docs-wip/operator.md:90,369,529`,
> `docs-wip/system-architecture.md:181,222`,
> and the operator-stack/operator record-build-publish step lists
> (`dev-docs-wip/docs/operator-stack.md:131-132,143,198,213,220`,
> framed with a pre-cleanup note in D-431). — `removable-after: C` (doc strings)

---

## Viewer — `cassini-viewer`

### C — `artifactPath` (catalog field naming an unpacked bundle directory)

Runtime (`src/`):

- `cassini-viewer/src/viewer/catalog.ts:5` — `artifactPath?: string` field on `MeetingCatalogEntry` — `removable-after: C`
- `cassini-viewer/src/viewer/catalog.ts:31,32` — resolve `meeting.artifactPath` to a catalog asset URL — `removable-after: C`
- `cassini-viewer/src/viewer/catalog.ts:65,67-68,73` — validate/require `artifactPath` (or `audioPath`) and return it — `removable-after: C`
- `cassini-viewer/src/App.svelte:556` — `meeting.artifactPath ? ...` branch selecting directory-load path — `removable-after: C`
- `cassini-viewer/src/App.svelte:557` — `loadArtifactFromDirectory(meeting.artifactPath)` call — `removable-after: C`
- `cassini-viewer/src/App.svelte:561` — error "missing artifactPath and audioPath" — `removable-after: C`

Dev/build scripts (`scripts/`, lower stakes):

- `cassini-viewer/scripts/demo-data-pull.mjs:116,117,119,120,122,127,141` — require/normalize `artifactPath`, download `manifest.json` + files into `${artifactPath}/` — `removable-after: C`
- `cassini-viewer/scripts/export-static-meetings.mjs:172,190` — emit catalog entries with `artifactPath: ...` — `removable-after: C`
- `cassini-viewer/scripts/merge-published-sites.mjs:174,175,198` — require `artifactPath`/`audioPath`, copy assets for both — `removable-after: C`

### C — `loadArtifactFromDirectory` (load an unpacked bundle directory) + `.meeting` ext

Runtime (`src/`):

- `cassini-viewer/src/viewer/loadArtifact.ts:104` — `export async function loadArtifactFromDirectory(basePath)` definition (loads an unpacked `meetings/<id>/` directory: `manifest.json` + transcripts) — `removable-after: C`
- `cassini-viewer/src/viewer/loadArtifact.ts:110` — `${basePath}/manifest.json` URL built by the directory loader (reads the bundle artifact manifest = schema B over HTTP) — `removable-after: C` (and depends on B)
- `cassini-viewer/src/App.svelte:21` — `import { loadArtifactFromDirectory }` — `removable-after: C`

Dev/build scripts (`scripts/`):

- `cassini-viewer/scripts/pack-portable-artifacts.mjs:33` — `canonicalPortableMeetingName` strips `.meeting` extension — `removable-after: C`
- `cassini-viewer/scripts/pack-portable-artifacts.mjs:85,186,187` — `loadArtifactMeeting(artifactDir)` call/def + `join(rootDir, "manifest.json")` (reads unpacked bundle) — `removable-after: C` (and depends on B)
- `cassini-viewer/scripts/reprocess-portable-meetings.mjs:265,303,310` — `loadArtifactMeeting` call/def + `join(candidatePath, "manifest.json")` — `removable-after: C` (and depends on B)
- `cassini-viewer/scripts/export-static-meetings.mjs:1209,1222` — read/copy `manifest.json` from a source meeting dir — `removable-after: C` (and depends on B)

### A — `cassini.json` (site/bundle envelope) in dev scripts

- `cassini-viewer/scripts/merge-published-sites.mjs:241` — read site `cassini.json` (`join(siteDir, "cassini.json")`) — site envelope, related to A; verify against site-manifest scope during A — `removable-after: A`
- `cassini-viewer/scripts/merge-published-sites.mjs:268` — write merged site `cassini.json` — `removable-after: A`

> Note: these two are the **site** `cassini.json`, not the `.meeting` envelope.
> The site manifest may survive the bundle-manifest retirement; confirm scope
> when A is planned. Listed here so the removal PR consciously decides.

### Not in scope (false positives — JSON `meeting` field, not `.meeting` ext)

- `cassini-viewer/src/viewer/loadArtifact.ts:550,640,695,736`; `src/viewer/portable.ts:354`; `scripts/export-static-meetings.mjs:272`; `scripts/repack-portable-display-transcripts.mjs:196,197,217,218,223`; `scripts/reprocess-portable-meetings.mjs:260,277` — all access the `meeting` *metadata object* inside a portable manifest (`portable.meeting?.title`, etc.), NOT the `.meeting` bundle extension. `keep: not-in-scope`

---

## Publisher — `cassini-publisher`

The module is shell scripts plus a README (no TypeScript/Svelte/.mjs source).
The shell entry points delegate the actual export/merge to
`cassini-viewer/scripts/` (already inventoried under Viewer), but the publisher's
own README and script doc strings still document the `.meeting` bundle as a
build output / publish input, so they are part of the C cleanup.

### C — `.meeting` directory / publish input doc strings

- `cassini-publisher/README.md:32` — quick-start example `./bin/cassini build /path/to/meeting.mkv --out /tmp/meetings/weekly-sync.meeting` (documents building a `.meeting` bundle as the publishable output; should become a portable `.opus`) — `removable-after: C` (doc string)
- `cassini-publisher/bin/export-static-meetings.sh:9` — deprecation warning text `"... prefer ./bin/cassini publish ... for .meeting bundles"` (names the `.meeting` bundle as the publish input contract) — `removable-after: C` (doc string)

No `cassini.meeting.v1` / `cassini.meeting-artifact.v1` / `artifactPath` /
`loadArtifactFromDirectory` references exist in the publisher itself; those live
in the viewer scripts it shells out to (inventoried under Viewer).

---

## Summary of removable axes

- **A (D-428)** — `cassini.meeting.v1` envelope: recorder `meeting_bundle.go`
  (schema const + serialization), operator `meetingbundle.go`, viewer site
  `cassini.json` (scope TBD).
- **B (D-429)** — `cassini.meeting-artifact.v1` artifact manifest: recorder
  `transcribe/format.go` (writer) + `portable_meeting.go` (reader/decoder),
  `meeting_bundle.go` validation; viewer/script `manifest.json` directory reads
  depend on it.
- **C (D-430)** — `.meeting` directory / `artifactPath` /
  `loadArtifactFromDirectory` as a publish/viewer input contract: recorder
  publish+inspect+help, operator path/promotion, viewer `catalog.ts` /
  `App.svelte` / `loadArtifact.ts` and dev scripts, and the publisher's
  README/script doc strings that still name `.meeting` as the build output /
  publish input.

The `keep: transient-scratch-only` items (the internal `.cassini-work`
`meeting.meeting` staging and the pack-into-`.opus` step) are intentionally
**not** removed by A/B/C — the `.meeting` intermediate stays as build scratch.
