package operator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func openImportTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestImportJobLifecycle(t *testing.T) {
	s := openImportTestStore(t)
	ctx := context.Background()
	job := ImportJob{
		ID: "01TESTIMPORT0000000000000A", BatchID: "01TESTBATCH00000000000000A",
		CreatedBy: "alice", SourceName: "retro.opus", StagedPath: ".CassiniImports/abc-retro.opus",
		Sharing: importAudience{Share: true, Audience: []importPrincipal{{Type: "user", ID: "mina"}}},
	}
	if err := s.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
		t.Fatal(err)
	}

	queued, ok, err := s.nextQueuedImport(ctx)
	if err != nil || !ok || queued.ID != job.ID || queued.State != importStateQueued {
		t.Fatalf("queued claim: %+v ok=%v err=%v", queued, ok, err)
	}
	if queued.Sharing.Audience[0].ID != "mina" || !queued.Sharing.Share {
		t.Fatalf("sharing round-trip: %+v", queued.Sharing)
	}

	// Only the creator's listing carries the job.
	if jobs, err := s.ImportJobsFor(ctx, "mallory"); err != nil || len(jobs) != 0 {
		t.Fatalf("stranger listing: %v %v", jobs, err)
	}
	if jobs, err := s.ImportJobsFor(ctx, "alice"); err != nil || len(jobs) != 1 {
		t.Fatalf("creator listing: %v %v", jobs, err)
	}

	if err := s.SetImportJobState(ctx, job.ID, importStateDelivering, "delivering", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImportJobProvenance(ctx, job.ID, "opus", "mtg_abc", "rm_def", "2026-08-12T10:00:00Z", "2026-08-12T12:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImportJobOutcome(ctx, job.ID, job.ID+".opus", 42, "2026-10-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ImportJob(ctx, job.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.State != importStateDone || got.DeliveredName != job.ID+".opus" || got.FileID != 42 || got.ContentID != "mtg_abc" {
		t.Fatalf("outcome: %+v", got)
	}

	// Done rows feed the retention provenance gate.
	names, err := s.doneImportDeliveries(ctx)
	if err != nil || len(names) != 1 || names[0] != job.ID+".opus" {
		t.Fatalf("done deliveries: %v %v", names, err)
	}

	// Terminal rows are not retried by requeue, and a done row refuses cancel.
	if n, err := s.RequeueUnfinishedImports(ctx); err != nil || n != 0 {
		t.Fatalf("requeue touched terminal rows: %d %v", n, err)
	}
	if err := s.CancelImportJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.ImportJob(ctx, job.ID); got.State != importStateDone {
		t.Fatalf("cancel moved a done job: %s", got.State)
	}
}

func TestImportJobCancelBeatsWorkerUpdates(t *testing.T) {
	s := openImportTestStore(t)
	ctx := context.Background()
	job := ImportJob{ID: "01TESTIMPORT0000000000000B", BatchID: "b", CreatedBy: "alice", SourceName: "a.opus", StagedPath: ".CassiniImports/a.opus"}
	if err := s.CreateImportJobs(ctx, []ImportJob{job}); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelImportJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	// A slower worker's state update must not resurrect a canceled job.
	if err := s.SetImportJobState(ctx, job.ID, importStateDelivering, "delivering", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImportJobOutcome(ctx, job.ID, job.ID+".opus", 7, "2026-10-15T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.ImportJob(ctx, job.ID)
	if got.State != importStateCanceled {
		t.Fatalf("cancel lost to worker update: %s", got.State)
	}
	// Retry re-queues it.
	if err := s.RetryImportJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.ImportJob(ctx, job.ID); got.State != importStateQueued {
		t.Fatalf("retry: %s", got.State)
	}
}

func TestRequeueUnfinishedImports(t *testing.T) {
	s := openImportTestStore(t)
	ctx := context.Background()
	jobs := []ImportJob{
		{ID: "01TESTIMPORT0000000000000C", BatchID: "b", CreatedBy: "alice", SourceName: "a.opus", StagedPath: "p1"},
		{ID: "01TESTIMPORT0000000000000D", BatchID: "b", CreatedBy: "alice", SourceName: "b.opus", StagedPath: "p2"},
	}
	if err := s.CreateImportJobs(ctx, jobs); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImportJobState(ctx, jobs[0].ID, importStateDelivering, "delivering", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetImportJobState(ctx, jobs[1].ID, importStateFailed, "", "boom"); err != nil {
		t.Fatal(err)
	}
	n, err := s.RequeueUnfinishedImports(ctx)
	if err != nil || n != 1 {
		t.Fatalf("requeue: %d %v", n, err)
	}
	if got, _, _ := s.ImportJob(ctx, jobs[0].ID); got.State != importStateQueued {
		t.Fatalf("in-flight row not requeued: %s", got.State)
	}
	if got, _, _ := s.ImportJob(ctx, jobs[1].ID); got.State != importStateFailed {
		t.Fatalf("failed row resurrected: %s", got.State)
	}
}

func TestSniffPortableMeetingExtension(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		body []byte
		want string
	}{
		{[]byte("OggS\x00\x02rest-of-ogg-page"), ".opus"},
		{[]byte(`{"kind":"cassini-transcription","version":1}`), ".json"},
		{[]byte("\xEF\xBB\xBF  {\"kind\":\"cassini-transcription\"}"), ".json"},
		{[]byte("RIFF....WAVE"), ""},
		{nil, ""},
	}
	for i, tc := range cases {
		got, err := sniffPortableMeetingExtension(write(filepath.Base(t.Name())+string(rune('a'+i)), tc.body))
		if tc.want == "" {
			if err == nil {
				t.Fatalf("case %d: accepted %q", i, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("case %d: got %q err=%v", i, got, err)
		}
	}
}

func TestStampImportedEntry(t *testing.T) {
	entry := json.RawMessage(`{"id":"01X","title":"Retro","roomId":"rm_abc"}`)
	stamped, err := stampImportedEntry(entry, "alice", "2026-10-15T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		ID       string `json:"id"`
		RoomID   string `json:"roomId"`
		Imported struct {
			By    string `json:"by"`
			AtUTC string `json:"atUtc"`
		} `json:"imported"`
	}
	if err := json.Unmarshal(stamped, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.ID != "01X" || fields.RoomID != "rm_abc" || fields.Imported.By != "alice" || fields.Imported.AtUTC != "2026-10-15T10:00:00Z" {
		t.Fatalf("stamp: %+v", fields)
	}
}

func TestParseSharePrincipals(t *testing.T) {
	audience, err := parseSharePrincipals(" user:mina , group:growth-team, circle:leads ")
	if err != nil || len(audience) != 3 {
		t.Fatalf("%v %v", audience, err)
	}
	if audience[0] != (sharePrincipal{Type: "user", ID: "mina"}) || audience[1].Type != "group" || audience[2].Type != "circle" {
		t.Fatalf("%+v", audience)
	}
	if _, err := parseSharePrincipals("team:x"); err == nil {
		t.Fatal("unknown principal type accepted")
	}
	if _, err := parseSharePrincipals("user:"); err == nil {
		t.Fatal("empty id accepted")
	}
	if audience, err := parseSharePrincipals(""); err != nil || len(audience) != 0 {
		t.Fatalf("empty list: %v %v", audience, err)
	}
}

func TestInterruptedCLIImportsAreNotRequeuedAsWebUploads(t *testing.T) {
	s := openImportTestStore(t)
	ctx := context.Background()
	states := []string{importStateQueued, importStateFetching, importStateValidating, importStateDelivering, importStateSharing, importStateIndexing, importStateFailed, importStateCanceled, importStateDone}
	for _, state := range states {
		j := ImportJob{ID: "cli-" + state, BatchID: "b", CreatedBy: "alice", SourceName: "source.opus", StagedPath: "local:/tmp/source.opus"}
		if err := s.CreateImportJobs(ctx, []ImportJob{j}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetImportJobState(ctx, j.ID, state, "", "original error"); err != nil {
			t.Fatal(err)
		}
	}
	upload := ImportJob{ID: "upload", BatchID: "b", CreatedBy: "alice", SourceName: "upload.opus", StagedPath: ".CassiniImports/upload.opus"}
	if err := s.CreateImportJobs(ctx, []ImportJob{upload}); err != nil {
		t.Fatal(err)
	}
	// Queue selection itself excludes CLI jobs, even before startup recovery.
	if next, ok, err := s.nextQueuedImport(ctx); err != nil || !ok || next.ID != upload.ID {
		t.Fatal(next, ok, err)
	}
	if n, err := s.RequeueUnfinishedImports(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, state := range states {
		j, ok, err := s.ImportJob(ctx, "cli-"+state)
		if err != nil || !ok || j.Origin != "cli" {
			t.Fatal(j, ok, err)
		}
		if importStateTerminal(state) {
			if j.State != state || j.Error != "original error" {
				t.Fatal(j)
			}
		} else if j.State != importStateFailed || j.Error != importCLIRecovery {
			t.Fatal(j)
		}
		if err := s.RetryImportJob(ctx, j.ID); err == nil {
			t.Fatalf("CLI retry accepted: %s", j.ID)
		}
	}
	jobs, err := s.ImportJobsFor(ctx, "alice")
	if err != nil || len(jobs) != len(states)+1 {
		t.Fatal(jobs, err)
	}
}
