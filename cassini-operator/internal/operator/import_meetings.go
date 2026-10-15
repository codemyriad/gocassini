package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Meeting import (D-828): ingest a portable meeting file into the archive as if
// it had been published here.
//
// The engine mirrors the direct-shares publish delivery step for step — upload
// as the recordings owner, read back the file ID, index the metadata, share,
// ingest marks and search — but it is fed by an uploaded file instead of a
// sealed attempt, so three things differ by design:
//
//   - The delivered stem is a FRESH local ULID (the import job's id), never the
//     file's original stem. Reusing the source stem would collide with the
//     original job row when a meeting is imported back into its source instance
//     (the backup-recovery case), and retention, speaker edits and publish
//     would all mistake the import for that job's artifact.
//   - The audience comes from the importer's selection, not a Talk binding: the
//     importer is always the share starter, and resharing is granted by default
//     (the same permission a public room's recording carries).
//   - Retention is adopted eagerly, with the meeting's own timestamps as the
//     age anchor — the standard policy. A file without a usable createdAtUtc is
//     invalid for import, because retention could never establish its age.
//
// The file bytes are stored untouched: the manifest (source job and room ids,
// speakers, provenance, annotations) stays the authoritative travel record, and
// the unchanged audio digest keeps its marks resolved. What is local-only — who
// imported it, when — is stamped on the catalog entry and kept on the import
// job row.

// meetingImportRequest is one file the engine ingests. LocalPath is a staged
// copy on the operator's own disk; fetching it (from the caller's Files, or a
// path on the CLI) is the caller's business.
type meetingImportRequest struct {
	ImportID   string
	CreatedBy  string
	LocalPath  string
	Audience   []sharePrincipal
}

// meetingImportResult is what the ingest settled.
type meetingImportResult struct {
	DeliveredName   string `json:"deliveredName"`
	FileID          int64  `json:"fileId"`
	Format          string `json:"format"`
	ContentID       string `json:"contentId,omitempty"`
	SourceRoomID    string `json:"sourceRoomId,omitempty"`
	CreatedAtUTC    string `json:"createdAtUtc"`
	RecordedAtLocal string `json:"recordedAtLocal,omitempty"`
	ImportedAtUTC   string `json:"importedAtUtc"`
	SharedWith      int    `json:"sharedWith"`
	SearchIndexed   bool   `json:"searchIndexed"`
}

// importPhase lets the long-running engine report coarse progress; the HTTP
// worker persists it, the CLI ignores it.
type importPhaseFunc func(state, phase string)

// meetingImporter wraps the Nextcloud delivery sink: everything that writes to
// the archive goes through the same code the publish path uses.
type meetingImporter struct {
	sink *nextcloudFilesPublishSink
}

func newMeetingImporter(cfg Config, exapp ExAppConfig, rt *Runtime, logger *log.Logger) *meetingImporter {
	return &meetingImporter{sink: &nextcloudFilesPublishSink{
		cfg: exapp, logger: logger, client: &http.Client{Timeout: ncFilesUploadTimeout},
		cassiniBin: cfg.CassiniBin, rt: rt,
	}}
}

// importCLITimeout bounds each cassini subprocess the engine runs. Validation
// re-reads the whole file, so long meetings take real time, but never hours.
const importCLITimeout = 10 * time.Minute

// run ingests one staged file. It is idempotent: a re-run after a crash puts
// the same bytes under the same name, re-indexes the same rows and adds only
// missing shares, so re-queueing interrupted jobs is the whole recovery story.
func (imp *meetingImporter) run(ctx context.Context, req meetingImportRequest, report importPhaseFunc) (meetingImportResult, error) {
	var result meetingImportResult
	if report == nil {
		report = func(string, string) {}
	}
	s := imp.sink
	if s.rt == nil || s.rt.store == nil {
		return result, fmt.Errorf("meeting import has no job store")
	}
	if req.ImportID == "" || req.CreatedBy == "" || req.LocalPath == "" {
		return result, fmt.Errorf("invalid import request")
	}

	// --- validate ---------------------------------------------------------
	report(importStateValidating, "validating")
	ext, err := sniffPortableMeetingExtension(req.LocalPath)
	if err != nil {
		return result, err
	}
	result.Format = strings.TrimPrefix(ext, ".")
	staging, err := os.MkdirTemp(filepath.Dir(req.LocalPath), ".import-"+req.ImportID+"-")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	// The exporter names the delivered asset after the input file's stem, so the
	// minted identity is applied by staging the file under it before validation.
	staged := filepath.Join(staging, req.ImportID+ext)
	if err := os.Link(req.LocalPath, staged); err != nil {
		if err := copyFileContents(req.LocalPath, staged); err != nil {
			return result, err
		}
	}
	siteDir := filepath.Join(staging, "site")
	// `cassini publish` re-checks the embedded integrity fail-closed (format
	// tag, manifest digests, audio identity) and emits the one-entry catalog.
	if err := imp.runCassini(ctx, []string{"publish", staged, "--out", siteDir}); err != nil {
		return result, fmt.Errorf("not a valid portable meeting: %w", err)
	}
	catalog, ok, err := loadSiteCatalog(siteDir)
	if err != nil {
		return result, err
	}
	if !ok || len(catalog.Meetings) != 1 {
		return result, fmt.Errorf("validation did not describe exactly one meeting")
	}
	entry := catalog.Meetings[0]
	assets, err := catalogEntryAssets(entry)
	if err != nil || len(assets) != 1 || assets[0] != filepath.Join("meetings", req.ImportID+ext) {
		return result, fmt.Errorf("validation did not describe the staged recording")
	}

	// Retention anchors on the meeting's own dates (the standard policy), so a
	// file that cannot state them cannot be managed and is refused up front.
	times, err := imp.inspectMeetingTimes(ctx, staged)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(times.CreatedAtUTC) == "" {
		return result, fmt.Errorf("the meeting carries no createdAtUtc timestamp, so retention could never establish its age; it cannot be imported")
	}
	result.CreatedAtUTC, result.RecordedAtLocal = times.CreatedAtUTC, times.RecordedAtLocal
	if shown, err := runAnnotateShow(ctx, s.cassiniBin, staged); err == nil {
		if digest := strings.TrimSpace(shown.AudioOpusSHA256); digest != "" {
			result.ContentID = "mtg_" + digest
		}
	}
	result.SourceRoomID = catalogStringFieldRaw(entry, "roomId")

	// --- deliver ----------------------------------------------------------
	report(importStateDelivering, "delivering")
	if !ncAccessSubstrate.usable() {
		s.cfg.preflightDirectShares(ctx, s.logger)
	}
	if !ncAccessSubstrate.usable() {
		snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
		return result, fmt.Errorf("Nextcloud recording shares are not ready (%s: %s)", snap.Step, snap.Detail)
	}
	name := req.ImportID + ext
	remote := ncRecordingsRoot + "/meetings/" + name
	for _, dir := range recordingsTreeDirs(ncRecordingsRoot) {
		if err := s.cfg.davMkcol(ctx, s.client, ncRecordingsOwner, dir); err != nil {
			return result, fmt.Errorf("create private archive directory %s: %w", dir, err)
		}
	}
	release, err := annotationWriteLocks.acquire(ctx, name)
	if err != nil {
		return result, err
	}
	defer release()
	if err := s.cfg.meetingNotRetired(ctx, name); err != nil {
		return result, err
	}
	info, err := os.Stat(staged)
	if err != nil || info.Size() <= 0 {
		return result, fmt.Errorf("staged recording is missing or empty: %v", err)
	}
	localDigest, err := fileSHA256(staged)
	if err != nil {
		return result, err
	}
	before, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, remote)
	if err != nil {
		return result, fmt.Errorf("inspect delivered recording: %w", err)
	}
	item := upload{local: staged, remote: remote, size: info.Size()}
	// A pre-existing leaf under a minted stem can only be this import's own
	// interrupted attempt, so equal bytes are already delivered and different
	// bytes are a truncated upload to replace. Marks cannot have accrued on it
	// before the import finished sharing it, so no carry is attempted.
	alreadyDelivered := before.Exists && strings.EqualFold(strings.TrimSpace(before.Checksum), localDigest)
	if !alreadyDelivered {
		if err := s.putAssetBytes(ctx, item, ""); err != nil {
			return result, err
		}
	}
	after, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, remote)
	if err != nil || !after.Exists || after.FileID <= 0 {
		return result, fmt.Errorf("Nextcloud did not report a file ID for %s after upload: %v", remote, err)
	}
	if before.Exists && before.FileID > 0 && before.FileID != after.FileID {
		return result, fmt.Errorf("Nextcloud changed %s from file ID %d to %d; re-run the import", remote, before.FileID, after.FileID)
	}
	result.DeliveredName, result.FileID = name, after.FileID
	result.ImportedAtUTC = nowUTCString()

	if s.rt.meetingMetadata != nil {
		stamped, err := stampImportedEntry(entry, req.CreatedBy, result.ImportedAtUTC)
		if err != nil {
			return result, err
		}
		if err := s.rt.meetingMetadata.Put(ctx, after.FileID, name, stamped); err != nil {
			return result, fmt.Errorf("index recording metadata: %w", err)
		}
	}

	// Adopt the retention lifecycle eagerly: the provenance that the inventory
	// pass would otherwise have to re-establish is in hand right now.
	lifecycle := meetingLifecycle{
		CreatedAtUTC:    times.CreatedAtUTC,
		RecordedAtLocal: times.RecordedAtLocal,
		Name:            name,
		FileID:          after.FileID,
		Path:            remote,
		State:           "active",
	}
	if err := setMeetingRetentionAge(&lifecycle); err != nil {
		return result, fmt.Errorf("establish meeting age: %w", err)
	}
	if err := s.rt.store.adoptMeetingLifecycle(ctx, lifecycle); err != nil {
		return result, fmt.Errorf("adopt retention lifecycle: %w", err)
	}

	// --- share ------------------------------------------------------------
	report(importStateSharing, "sharing")
	audience := append([]sharePrincipal{{Type: "user", ID: req.CreatedBy}}, req.Audience...)
	// public=true grants read+reshare, the same bits a public Talk room's
	// recording gets: recipients may pass the meeting along (D-828 decision D1),
	// and reconcileRecordingShares already falls back to read-only where
	// instance policy refuses the reshare bit.
	if err := s.cfg.reconcileRecordingShares(ctx, s.client, remote, audience, true); err != nil {
		return result, err
	}
	result.SharedWith = len(audience)

	// --- index ------------------------------------------------------------
	report(importStateIndexing, "indexing")
	s.indexDeliveredMarks(ctx, []upload{item}, nil)
	result.SearchIndexed = imp.ingestSearchRows(ctx, name, staged, localDigest)
	return result, nil
}

// ingestSearchRows makes the imported meeting searchable from its own words —
// the archive-backfill granularity, since an import has no producer bundle.
// Failure is non-fatal and recorded, the same contract publish-time ingest has.
func (imp *meetingImporter) ingestSearchRows(ctx context.Context, name, local, containerDigest string) bool {
	rt := imp.sink.rt
	if rt == nil || rt.searchStore == nil {
		return false
	}
	words, err := transcriptWordsFromOpus(ctx, imp.sink.cassiniBin, local)
	if err != nil {
		imp.logf("import search ingest: %s: %v", name, err)
		if err := rt.searchStore.MarkUnavailable(ctx, name, searchIngestReasonUnreadable); err != nil {
			imp.logf("import search ingest: %s: recording the failure failed too: %v", name, err)
		}
		return false
	}
	rows := deriveSearchRowsFromWords(words)
	if len(rows) == 0 {
		// Empty words alone cannot distinguish silence from disabled or failed
		// transcription, and an import has no bundle manifest to consult.
		if err := rt.searchStore.MarkUnavailable(ctx, name, searchIngestReasonNoSegments); err != nil {
			imp.logf("import search ingest: %s: %v", name, err)
		}
		return false
	}
	if err := rt.searchStore.ReplaceMeeting(ctx, name, containerDigest, searchRowSourceWords, rows); err != nil {
		imp.logf("import search ingest: %s: %v", name, err)
		return false
	}
	return true
}

func (imp *meetingImporter) logf(format string, args ...any) {
	if imp.sink.logger != nil {
		imp.sink.logger.Printf(format, args...)
	}
}

// meetingTimesDocument is `cassini inspect --meeting-times`: the same reader
// retention uses to establish a delivered meeting's age.
type meetingTimesDocument struct {
	CreatedAtUTC    string `json:"createdAtUtc"`
	RecordedAtLocal string `json:"recordedAtLocal"`
}

func (imp *meetingImporter) inspectMeetingTimes(ctx context.Context, file string) (meetingTimesDocument, error) {
	ctx, cancel := context.WithTimeout(ctx, importCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, imp.sink.cassiniBin, "inspect", "--meeting-times", file)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return meetingTimesDocument{}, fmt.Errorf("read meeting times: %w: %s", err, tailOfOutput(stderr.String()))
	}
	var times meetingTimesDocument
	if err := json.Unmarshal(out, &times); err != nil {
		return meetingTimesDocument{}, fmt.Errorf("decode meeting times: %w", err)
	}
	return times, nil
}

func (imp *meetingImporter) runCassini(ctx context.Context, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, importCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, imp.sink.cassiniBin, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cassini %s: %w: %s", args[0], err, tailOfOutput(output.String()))
	}
	return nil
}

func tailOfOutput(out string) string {
	out = strings.TrimSpace(out)
	if len(out) > 1000 {
		out = out[len(out)-1000:]
	}
	return out
}

// sniffPortableMeetingExtension decides whether an uploaded file is an Ogg/Opus
// portable meeting or a transcription-only JSON document — by content, never by
// the name it arrived under, mirroring how the published routes tolerate a
// recipient's rename. The CLI still validates the contract; this only picks the
// extension the archive stores it under, which is what classifies its format.
func sniffPortableMeetingExtension(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	if bytes.HasPrefix(head, []byte("OggS")) {
		return ".opus", nil
	}
	if trimmed := bytes.TrimLeft(bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF}), " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '{' {
		return ".json", nil
	}
	return "", fmt.Errorf("the file is neither an Ogg/Opus recording nor a transcription document")
}

// stampImportedEntry records the local-only provenance on the catalog entry:
// who brought the meeting in and when. Everything else the entry carries came
// out of the file. Visible to everyone the meeting is shared with — it is
// attribution, the same way marks carry their authors.
func stampImportedEntry(entry json.RawMessage, importedBy, importedAtUTC string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil {
		return nil, err
	}
	stamp, err := json.Marshal(map[string]string{"by": importedBy, "atUtc": importedAtUTC})
	if err != nil {
		return nil, err
	}
	fields["imported"] = stamp
	return json.Marshal(fields)
}

// catalogStringFieldRaw reads one optional string field off a raw entry.
func catalogStringFieldRaw(entry json.RawMessage, field string) string {
	var fields map[string]any
	if err := json.Unmarshal(entry, &fields); err != nil {
		return ""
	}
	return catalogStringField(fields, field)
}

// copyFileContents is the fallback when the staged upload and the import
// scratch directory are on different filesystems and a hard link is refused.
func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
