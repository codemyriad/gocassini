package operator

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// recordedAtFixture is a runtime with a jobs DB, a meeting index and a work
// root holding run bundles; the MKV probe is stubbed and counted.
type recordedAtFixture struct {
	rt     *Runtime
	probes atomic.Int32
}

func newRecordedAtFixture(t *testing.T) *recordedAtFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	metadata, err := openMeetingMetadataStore(meetingMetadataPath(filepath.Join(dir, "jobs.sqlite3")), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &recordedAtFixture{}
	f.rt = &Runtime{ctx: ctx, store: store, meetingMetadata: metadata, logger: log.New(io.Discard, "", 0)}
	f.rt.cfg.WorkRoot = filepath.Join(dir, "jobs")
	f.rt.cfg.DBPath = filepath.Join(dir, "jobs.sqlite3")
	f.rt.recordingAnchorProbe = func(_ context.Context, mkv string) ([]recordingAnchor, error) {
		f.probes.Add(1)
		if filepath.Base(filepath.Dir(mkv)) == testJobULID+".run" {
			return testAnchors, nil
		}
		return nil, nil
	}
	return f
}

// job inserts a recording's jobs row; recordStarted "" leaves it NULL.
func (f *recordedAtFixture) job(t *testing.T, id, recordStarted string) {
	t.Helper()
	ctx := context.Background()
	now := nowUTCString()
	if err := f.rt.store.InsertQueuedJob(ctx, Job{ID: id, Provider: "nextcloud-talk", RequestJSON: "{}", Stage: "record", State: "queued", CurrentAttemptNumber: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if recordStarted != "" {
		if err := f.rt.store.MarkRecordRunning(ctx, id, recordStarted); err != nil {
			t.Fatal(err)
		}
	}
}

// bundle writes a run bundle with an MKV and/or a session.json.
func (f *recordedAtFixture) bundle(t *testing.T, id string, mkv bool, sessionStarted string) {
	t.Helper()
	run := canonicalRunPath(f.rt.cfg.WorkRoot, id)
	if err := os.MkdirAll(filepath.Join(run, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if mkv {
		if err := os.WriteFile(filepath.Join(run, "recording.mkv"), []byte("mkv"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sessionStarted != "" {
		body, _ := json.Marshal(map[string]any{"started_wall_utc": sessionStarted, "recorded_at_local": "ignored"})
		if err := os.WriteFile(filepath.Join(run, "session", "session.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *recordedAtFixture) put(t *testing.T, fileID int64, name, entry string) {
	t.Helper()
	if err := f.rt.meetingMetadata.Put(context.Background(), fileID, name, json.RawMessage(entry)); err != nil {
		t.Fatal(err)
	}
}

func (f *recordedAtFixture) entry(t *testing.T, fileID int64) (string, string) {
	t.Helper()
	var entry, column string
	if err := f.rt.meetingMetadata.db.QueryRow(`SELECT entry_json, recorded_at_utc FROM meeting_metadata WHERE file_id=?`, fileID).Scan(&entry, &column); err != nil {
		t.Fatal(err)
	}
	return entry, column
}

func (f *recordedAtFixture) queued(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.rt.meetingMetadata.db.Query(`SELECT opus_name, recorded_at_utc FROM recorded_at_file_backfill`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		_ = rows.Scan(&name, &value)
		out[name] = value
	}
	return out
}

func entryField(t *testing.T, raw, field string) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	value, _ := fields[field].(string)
	return value
}

const (
	sessionJob = "01KV5DHN3FS2ZMY0AB6BASHSVD"
	startedJob = "01KXQT6N0000000000000000AA"
	bareJob    = "01KXQT6N0000000000000000BB"
	importID   = "01M7ZZZZ00000000000000IMPT"
	newJob     = "01M8000000000000000000NEWJ"
)

func TestRecordedAtIndexBackfillStampsByPrecedenceAndIsIdempotent(t *testing.T) {
	f := newRecordedAtFixture(t)
	ctx := context.Background()
	// The MKV wins over session.json and the job start.
	f.job(t, testJobULID, testRecordStarted)
	f.bundle(t, testJobULID, true, testSessionStarted)
	// No MKV: session.json.
	f.job(t, sessionJob, "2026-06-15T10:34:01Z")
	f.bundle(t, sessionJob, false, "2026-06-15T10:34:02.250000000Z")
	// No bundle at all: the job's record start.
	f.job(t, startedJob, "2026-07-17T10:33:05.123456789Z")
	// Failed before recording: the ULID.
	f.job(t, bareJob, "")

	f.put(t, 1, testJobULID+".opus", `{"id":"`+testJobULID+`","title":"T","dateLabel":"2026-11-09 11:31","audioPath":"./meetings/`+testJobULID+`.opus","roomId":"rm_a","extension":{"keep":true}}`)
	f.put(t, 2, sessionJob+".opus", `{"id":"`+sessionJob+`","dateLabel":"2026-06-15 10:34","audioPath":"./meetings/`+sessionJob+`.opus"}`)
	f.put(t, 3, startedJob+".opus", `{"id":"`+startedJob+`","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/`+startedJob+`.opus"}`)
	f.put(t, 4, bareJob+".opus", `{"id":"`+bareJob+`","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/`+bareJob+`.opus"}`)
	// An import and a seeded legacy meeting have no jobs row: untouched.
	importEntry := `{"id":"` + importID + `","dateLabel":"2026-11-21 21:25","audioPath":"./meetings/` + importID + `.opus","imported":true}`
	legacyEntry := `{"id":"daily-meeting-2026-06-10","dateLabel":"2026-06-10","audioPath":"./meetings/daily-meeting-2026-06-10.opus"}`
	f.put(t, 5, importID+".opus", importEntry)
	f.put(t, 6, "daily-meeting-2026-06-10.opus", legacyEntry)
	// A new publish whose file states its instant: kept as stated, no file work.
	f.job(t, newJob, "2026-11-10T09:00:00Z")
	f.put(t, 7, newJob+".opus", `{"id":"`+newJob+`","dateLabel":"2026-11-10 09:00","recordedAtUtc":"2026-11-10T09:00:01.5Z","audioPath":"./meetings/`+newJob+`.opus"}`)

	report, err := f.rt.backfillRecordedAtIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stamped[recordedAtFromRecording] != 1 || report.Stamped[recordedAtFromSession] != 1 || report.Stamped[recordedAtFromJob] != 1 || report.Stamped[recordedAtFromJobID] != 1 || report.NoJob != 2 || report.Present != 1 {
		t.Fatalf("report = %s", report)
	}
	want := map[int64]string{
		1: "2026-11-09T11:32:27.181Z",
		2: "2026-06-15T10:34:02.250Z",
		3: "2026-07-17T10:33:05.123Z",
	}
	for id, value := range want {
		entry, column := f.entry(t, id)
		if got := entryField(t, entry, "recordedAtUtc"); got != value || column != value {
			t.Errorf("file %d: entry %q column %q, want %q", id, got, column, value)
		}
	}
	entry, _ := f.entry(t, 1)
	if entryField(t, entry, "dateLabel") != "2026-11-09 11:31" || entryField(t, entry, "roomId") != "rm_a" || !json.Valid([]byte(entry)) {
		t.Fatalf("other members must be kept: %s", entry)
	}
	var keep struct {
		Extension struct{ Keep bool } `json:"extension"`
	}
	if json.Unmarshal([]byte(entry), &keep); !keep.Extension.Keep {
		t.Fatalf("extension lost: %s", entry)
	}
	if entry, column := f.entry(t, 5); entry != importEntry || column != "" {
		t.Fatalf("import touched: %s %q", entry, column)
	}
	if entry, column := f.entry(t, 6); entry != legacyEntry || column != "" {
		t.Fatalf("legacy touched: %s %q", entry, column)
	}
	if _, column := f.entry(t, 7); column != "2026-11-10T09:00:01.500Z" {
		t.Fatalf("stated instant column = %q", column)
	}
	queued := f.queued(t)
	if len(queued) != 4 || queued[testJobULID+".opus"] != want[1] || queued[newJob+".opus"] != "" || queued[importID+".opus"] != "" {
		t.Fatalf("file queue = %v", queued)
	}

	// Second pass: nothing to do, nothing probed, nothing rewritten.
	before := map[int64]string{}
	for id := int64(1); id <= 7; id++ {
		before[id], _ = f.entry(t, id)
	}
	probes := f.probes.Load()
	again, err := f.rt.backfillRecordedAtIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Present != 5 || again.NoJob != 2 || len(again.Stamped) != 0 || f.probes.Load() != probes {
		t.Fatalf("second pass = %s (probes %d -> %d)", again, probes, f.probes.Load())
	}
	for id := int64(1); id <= 7; id++ {
		if after, _ := f.entry(t, id); after != before[id] {
			t.Fatalf("file %d rewritten on the second pass", id)
		}
	}
}

// A publish that replaces the entry while the pass derived its instant wins;
// the pass does not overwrite it.
func TestRecordedAtIndexBackfillYieldsToConcurrentPublish(t *testing.T) {
	f := newRecordedAtFixture(t)
	f.job(t, startedJob, "2026-07-17T10:33:05Z")
	f.put(t, 3, startedJob+".opus", `{"id":"`+startedJob+`","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/`+startedJob+`.opus"}`)
	var row recordedAtIndexRow
	if err := f.rt.meetingMetadata.db.QueryRow(`SELECT file_id, opus_name, entry_json, recorded_at_utc FROM meeting_metadata`).Scan(&row.fileID, &row.opusName, &row.entry, &row.column); err != nil {
		t.Fatal(err)
	}
	replacement := `{"id":"` + startedJob + `","title":"Rerun","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/` + startedJob + `.opus"}`
	f.put(t, 3, startedJob+".opus", replacement)
	outcome, _, err := f.rt.stampRecordedAtRow(context.Background(), row)
	if err != nil || outcome != recordedAtRaced {
		t.Fatalf("outcome = %v err=%v", outcome, err)
	}
	if entry, _ := f.entry(t, 3); entry != replacement {
		t.Fatalf("replacement overwritten: %s", entry)
	}
	if len(f.queued(t)) != 0 {
		t.Fatal("raced row must not queue file work")
	}
}

// A republish delivers a new file: an entry without an instant is stamped and
// its file requeued even if the old file was already done.
func TestStampPublishedRecordedAtRequeuesReplacedFile(t *testing.T) {
	f := newRecordedAtFixture(t)
	ctx := context.Background()
	f.job(t, startedJob, "2026-07-17T10:33:05Z")
	f.put(t, 3, startedJob+".opus", `{"id":"`+startedJob+`","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/`+startedJob+`.opus"}`)
	if _, err := f.rt.backfillRecordedAtIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.meetingMetadata.db.Exec(`UPDATE recorded_at_file_backfill SET state='done'`); err != nil {
		t.Fatal(err)
	}
	f.put(t, 3, startedJob+".opus", `{"id":"`+startedJob+`","title":"Rerun","dateLabel":"2026-07-17 10:33","audioPath":"./meetings/`+startedJob+`.opus"}`)
	if err := f.rt.stampPublishedRecordedAt(ctx, 3, startedJob+".opus"); err != nil {
		t.Fatal(err)
	}
	entry, column := f.entry(t, 3)
	if entryField(t, entry, "recordedAtUtc") != "2026-07-17T10:33:05.000Z" || column != "2026-07-17T10:33:05.000Z" || entryField(t, entry, "title") != "Rerun" {
		t.Fatalf("entry = %s column %q", entry, column)
	}
	var state string
	if err := f.rt.meetingMetadata.db.QueryRow(`SELECT state FROM recorded_at_file_backfill WHERE opus_name=?`, startedJob+".opus").Scan(&state); err != nil || state != "pending" {
		t.Fatalf("state = %q %v", state, err)
	}

	// A file that states its own instant needs no work.
	f.put(t, 3, startedJob+".opus", `{"id":"`+startedJob+`","dateLabel":"2026-07-17 10:33","recordedAtUtc":"2026-07-17T10:33:06Z","audioPath":"./meetings/`+startedJob+`.opus"}`)
	if err := f.rt.stampPublishedRecordedAt(ctx, 3, startedJob+".opus"); err != nil {
		t.Fatal(err)
	}
	if q := f.queued(t); len(q) != 0 {
		t.Fatalf("queue = %v", q)
	}
	if _, column := f.entry(t, 3); column != "2026-07-17T10:33:06.000Z" {
		t.Fatalf("column = %q", column)
	}
}

// Room defaults are chosen once; re-ordering the index by instant must not move
// one, across the backfill and a restart.
func TestRecordedAtBackfillKeepsRoomDefaults(t *testing.T) {
	f := newRecordedAtFixture(t)
	ctx := context.Background()
	f.job(t, startedJob, "2026-07-17T10:33:05Z")
	f.job(t, bareJob, "2026-07-17T12:00:00Z")
	// Label order says startedJob is newer; the instants say bareJob is.
	f.put(t, 1, startedJob+".opus", `{"id":"`+startedJob+`","dateLabel":"2026-07-17 23:00","audioPath":"./meetings/`+startedJob+`.opus","roomId":"rm_room","roomName":"Newest by label"}`)
	f.put(t, 2, bareJob+".opus", `{"id":"`+bareJob+`","dateLabel":"2026-07-17 08:00","audioPath":"./meetings/`+bareJob+`.opus","roomId":"rm_room","roomName":"Newest by instant"}`)
	names := func() string {
		got, err := f.rt.meetingMetadata.roomNames(ctx, []string{"rm_room"})
		if err != nil {
			t.Fatal(err)
		}
		return got["rm_room"].DisplayName
	}
	original := names()
	if _, err := f.rt.backfillRecordedAtIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.meetingMetadata.registerMetadataRooms(ctx); err != nil {
		t.Fatal(err)
	}
	if got := names(); got != original {
		t.Fatalf("room default moved from %q to %q", original, got)
	}
	// The index itself now puts the later instant first.
	var first string
	if err := f.rt.meetingMetadata.db.QueryRow(`SELECT opus_name FROM meeting_metadata ORDER BY ` + meetingMetadataSortKeySQL + ` DESC LIMIT 1`).Scan(&first); err != nil || first != bareJob+".opus" {
		t.Fatalf("newest = %q %v", first, err)
	}
}

// A version 2 index migrates in place: rows survive and gain the column, and
// user_version stays 2 so a released operator (which refuses anything newer)
// can still open the file after a rollback.
func TestMeetingMetadataV2MigratesRecordedAtColumn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "meetings.sqlite3")
	old, err := openSidecarDB(file, "fixture", meetingMetadataSchema+roomNamesSchema, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"A","dateLabel":"2026-10-21 10:00","audioPath":"./meetings/A.opus"}`
	if _, err := old.db.Exec(`INSERT INTO meeting_metadata VALUES(1,'A.opus','2026-10-21 10:00',?,'now')`, raw); err != nil {
		t.Fatal(err)
	}
	old.Close()
	s, err := openMeetingMetadataStore(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	var column, entry string
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version = %d %v", version, err)
	}
	if err := s.db.QueryRow(`SELECT recorded_at_utc, entry_json FROM meeting_metadata WHERE file_id=1`).Scan(&column, &entry); err != nil || column != "" || entry != raw {
		t.Fatalf("row = %q %q %v", column, entry, err)
	}
	if counts, err := s.recordedAtFileCounts(context.Background()); err != nil || len(counts) != 0 {
		t.Fatalf("counts = %v %v", counts, err)
	}
	// A released operator's write (it names its columns and knows nothing of
	// recorded_at_utc) still lands, and the next open of this build is a no-op.
	if _, err := s.db.Exec(`INSERT INTO meeting_metadata (file_id, opus_name, date_label, entry_json, indexed_at) VALUES (2, 'B.opus', '', '{}', 'now')`); err != nil {
		t.Fatalf("released-operator insert: %v", err)
	}
	s.Close()
	again, err := openMeetingMetadataStore(file, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if err := again.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version after reopen = %d %v", version, err)
	}
}

// The share note's start is the instant the entry states, when it has one.
func TestMeetingFactsPreferRecordedAt(t *testing.T) {
	f := newRecordedAtFixture(t)
	f.job(t, startedJob, "2026-07-17T10:33:05Z")
	f.put(t, 3, startedJob+".opus", `{"id":"`+startedJob+`","title":"T","dateLabel":"2026-07-17 10:33","recordedAtUtc":"2026-07-17T10:34:00Z","audioPath":"./meetings/`+startedJob+`.opus"}`)
	facts := f.rt.meetingFactsFor(context.Background(), startedJob)
	if got := facts.StartedAt.Format("15:04:05"); got != "10:34:00" {
		t.Fatalf("started = %s", got)
	}
}
