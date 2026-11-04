package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// sharingFake is a Nextcloud just complete enough for the caller's view of
// recording shares: the pending list, the caller's groups and the accept
// endpoint. It records who each call ran as.
type sharingFake struct {
	mu       sync.Mutex
	pending  []map[string]any
	groups   []string
	refuse   map[string]int // path suffix -> HTTP status
	delay    time.Duration
	accepted []string
	actors   map[string]string // method+path -> actor
	// acceptFail names share ids whose accept Nextcloud refuses, with its
	// own message.
	acceptFail map[string]string
}

func pendingShare(id int, shareType int, path string) map[string]any {
	return map[string]any{"id": fmt.Sprint(id), "share_type": shareType, "share_with": "alice", "permissions": 0,
		"uid_owner": ncRecordingsOwner, "uid_file_owner": ncRecordingsOwner, "item_type": "file", "path": path,
		"note": "Recording of Weekly sync. Open it in Cassini."}
}

func (f *sharingFake) serve(w http.ResponseWriter, r *http.Request) {
	const shares = "/ocs/v2.php/apps/files_sharing/api/v1/shares"
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actors == nil {
		f.actors = map[string]string{}
	}
	auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
	actor, _, _ := strings.Cut(string(auth), ":")
	f.actors[r.Method+" "+r.URL.Path] = actor
	if f.delay > 0 {
		f.mu.Unlock()
		time.Sleep(f.delay)
		f.mu.Lock()
	}
	for suffix, code := range f.refuse {
		if strings.HasSuffix(r.URL.Path, suffix) {
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, `{"ocs":{"meta":{"status":"failure","statuscode":%d,"message":"refused"},"data":[]}}`, code)
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == shares+"/pending":
		raw, _ := json.Marshal(f.pending)
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":`+string(raw)+`}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/ocs/v2.php/cloud/user":
		raw, _ := json.Marshal(f.groups)
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"id":"`+actor+`","groups":`+string(raw)+`}}}`)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, shares+"/pending/"):
		id := strings.TrimPrefix(r.URL.Path, shares+"/pending/")
		if message, refused := f.acceptFail[id]; refused {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":404,"message":"`+message+`"},"data":[]}}`)
			return
		}
		kept := f.pending[:0:0]
		for _, share := range f.pending {
			if share["id"] != id {
				kept = append(kept, share)
			}
		}
		f.pending = kept
		f.accepted = append(f.accepted, id)
		// Nextcloud answers an accept with an empty data element.
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":[]}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *sharingFake) actor(method, path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actors[method+" "+path]
}

// sharingRuntime is a runtime publishing to that Nextcloud.
func sharingRuntime(t *testing.T, fake *sharingFake) *Runtime {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	rt, cleanup := newTestRuntime(t)
	t.Cleanup(cleanup)
	cfg := ExAppConfig{NextcloudURL: srv.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", PublishSink: publishSinkNextcloudFiles}
	rt.publishSink = &directSharesPublishSink{&nextcloudFilesPublishSink{cfg: cfg, client: srv.Client(), logger: rt.logger, rt: rt}}
	return rt
}

func indexMeeting(t *testing.T, rt *Runtime, name, title string, durationMS int64) {
	t.Helper()
	entry := fmt.Sprintf(`{"id":%q,"title":%q,"dateLabel":"2026-10-30 10:00","meetingPath":"./meetings/%s","digestDurationMs":%d}`, meetingStem(name), title, name, durationMS)
	if err := rt.meetingMetadata.Put(context.Background(), testMeetingFileID(name), name, json.RawMessage(entry)); err != nil {
		t.Fatal(err)
	}
}

func setJobTimes(t *testing.T, rt *Runtime, id, started, finished string) {
	t.Helper()
	if _, err := rt.store.db.Exec(`UPDATE jobs SET record_started_at=?, record_finished_at=? WHERE id=?`, started, finished, id); err != nil {
		t.Fatal(err)
	}
}

func retireMeeting(t *testing.T, rt *Runtime, name string) {
	t.Helper()
	if _, err := rt.store.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason)
VALUES(?,?,?,'retired','2026-01-01T00:00:00Z','test','','',?)`, name, testMeetingFileID(name), ncRecordingsRoot+"/meetings/"+name, meetingRetireReasonExpired); err != nil {
		t.Fatal(err)
	}
}

func getSetup(t *testing.T, rt *Runtime, caller string) setupResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	rt.setupHandler(rec, callerReq(http.MethodGet, "/setup", caller))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup = %d %s", rec.Code, rec.Body.String())
	}
	var resp setupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode setup response: %v", err)
	}
	return resp
}

// The caller's own view, end to end: the recordings waiting for them, one
// entry per meeting with what the operator knows about it, newest first; and
// how many meetings could not be shared with them, by name or through a
// group, or were not published because they were refused as the starter.
func TestSetupTellsTheCallerWhatIsWaitingAndWhatWasRefused(t *testing.T) {
	fake := &sharingFake{
		groups: []string{"staff"},
		pending: []map[string]any{
			// A user share and a group share of the same recording.
			pendingShare(34, ncShareTypeUser, "/CassiniRecordings/meetings/J1.opus"),
			pendingShare(35, ncShareTypeGroup, "/CassiniRecordings/meetings/J1.opus"),
			// A transcript-only meeting, newer, that the operator never indexed.
			pendingShare(40, ncShareTypeUser, "/CassiniRecordings/meetings/J2.json"),
			// Not a recording: a colleague's document.
			{"id": "50", "share_type": 0, "permissions": 0, "uid_owner": "bob", "uid_file_owner": "bob", "item_type": "file", "path": "/Documents/plan.pdf"},
			// A recording file that is not Cassini's.
			{"id": "51", "share_type": 0, "permissions": 0, "uid_owner": "bob", "uid_file_owner": "bob", "item_type": "file", "path": "/J9.opus"},
			// An expired meeting: nothing left to accept.
			pendingShare(60, ncShareTypeUser, "/CassiniRecordings/meetings/J3.opus"),
		},
	}
	rt := sharingRuntime(t, fake)
	ctx := context.Background()
	for _, id := range []string{"J1", "J2", "J3", "J4", "J5", "J6", "J7", "J8", "J9"} {
		seedJobRow(t, rt.store.db, seededJobRow{ID: id, Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	}
	indexMeeting(t, rt, "J1.opus", "Weekly sync", 2520000)
	setJobTimes(t, rt, "J1", "2026-10-30T10:00:00.000000000Z", "2026-10-30T10:45:00.000000000Z")
	setJobTimes(t, rt, "J2", "2026-10-31T09:00:00.000000000Z", "2026-10-31T09:10:00.000000000Z")
	retireMeeting(t, rt, "J3.opus")
	// Refusals: by name, through a group, somebody else's, as the starter.
	record := func(id, status string, skipped ...skippedShare) {
		t.Helper()
		if err := rt.store.RecordJobShareOutcome(ctx, id, status, skipped); err != nil {
			t.Fatal(err)
		}
	}
	record("J4", shareOutcomePartial, skippedShare{Type: "user", ID: "alice", Reason: "Sharing is only allowed with group members"})
	// J5 reached alice through staff alone: bob started it, carol was in the
	// room herself. J10's room had alice in it, so her own share reached her
	// and the group's refusal costs her nothing. J11 captured no audience.
	captureAudience := func(id, starter string, roster ...sharePrincipal) {
		t.Helper()
		if err := rt.store.SetJobTalkBinding(ctx, id, `{"backend_url":"https://talk.example","room_token":"room1","owner":"`+starter+`","room_public":false}`); err != nil {
			t.Fatal(err)
		}
		if err := rt.store.MergeJobRoomAudience(ctx, id, roster, nowUTCString()); err != nil {
			t.Fatal(err)
		}
	}
	record("J5", shareOutcomePartial, skippedShare{Type: "group", ID: "staff"})
	captureAudience("J5", "bob", sharePrincipal{Type: "user", ID: "carol"}, sharePrincipal{Type: "group", ID: "staff"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J10", Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	record("J10", shareOutcomePartial, skippedShare{Type: "group", ID: "staff"})
	captureAudience("J10", "bob", sharePrincipal{Type: "user", ID: "alice"}, sharePrincipal{Type: "group", ID: "staff"})
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J11", Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	record("J11", shareOutcomePartial, skippedShare{Type: "group", ID: "staff"})
	record("J6", shareOutcomePartial, skippedShare{Type: "user", ID: "bob"}, skippedShare{Type: "circle", ID: "team-alice-is-in"})
	record("J7", shareOutcomeStarterRefused, skippedShare{Type: "user", ID: "alice"})
	if _, err := rt.store.db.Exec(`UPDATE jobs SET state='failed', error=? WHERE id='J7'`, "the recording "+starterRefusedMarker+", alice: refused"); err != nil {
		t.Fatal(err)
	}
	// A starter refusal a later rerun replaced: no longer why it failed.
	record("J8", shareOutcomeStarterRefused, skippedShare{Type: "user", ID: "alice"})
	// A refusal on an expired meeting.
	record("J9", shareOutcomePartial, skippedShare{Type: "user", ID: "alice"})
	retireMeeting(t, rt, "J9.opus")

	resp := getSetup(t, rt, "alice")
	sharing := resp.Sharing
	if !sharing.Checked {
		t.Fatalf("sharing not checked: %+v", sharing)
	}
	want := []pendingRecordingShare{
		{ShareIDs: []string{"40"}, JobID: "J2", Title: "", StartedAt: "2026-10-31T09:00:00Z", DurationSeconds: 600},
		{ShareIDs: []string{"34", "35"}, JobID: "J1", Title: "Weekly sync", StartedAt: "2026-10-30T10:00:00Z", DurationSeconds: 2520},
	}
	if !reflect.DeepEqual(sharing.Pending, want) {
		t.Fatalf("pending = %+v\nwant      %+v", sharing.Pending, want)
	}
	// J4 by name, J5 through staff alone, J11 through staff with nothing
	// known about the room; not J10, whose room alice was in.
	if sharing.Refused != 3 || sharing.RefusedStarted != 1 {
		t.Fatalf("refused = %d, refused_started = %d, want 3 and 1", sharing.Refused, sharing.RefusedStarted)
	}
	for _, call := range []string{"GET /ocs/v2.php/apps/files_sharing/api/v1/shares/pending", "GET /ocs/v2.php/cloud/user"} {
		method, path, _ := strings.Cut(call, " ")
		if actor := fake.actor(method, path); actor != "alice" {
			t.Errorf("%s ran as %q, want the caller", call, actor)
		}
	}
	// Somebody else sees their own view, and nothing of alice's. carol is in
	// staff too (the fake answers the same groups): J10 and J11 count for
	// her, J5 does not, because she was in that room and has her own share;
	// J4 and J7 are alice's alone.
	if other := getSetup(t, rt, "carol").Sharing; !other.Checked || len(other.Pending) != 2 || other.Refused != 2 || other.RefusedStarted != 0 {
		t.Fatalf("carol's view = %+v", other)
	}
}

// A read that fails or runs out of time establishes nothing, and says so:
// never an empty list presented as the truth.
func TestSetupSharingIsNotGuessedWhenNextcloudDoesNotAnswer(t *testing.T) {
	for name, fake := range map[string]*sharingFake{
		"pending refused": {refuse: map[string]int{"/shares/pending": http.StatusForbidden}, groups: []string{"staff"}},
		"groups refused":  {refuse: map[string]int{"/cloud/user": http.StatusNotFound}, pending: []map[string]any{pendingShare(34, 0, "/CassiniRecordings/meetings/J1.opus")}},
		"too slow":        {delay: 300 * time.Millisecond, pending: []map[string]any{pendingShare(34, 0, "/CassiniRecordings/meetings/J1.opus")}},
	} {
		t.Run(name, func(t *testing.T) {
			if fake.delay > 0 {
				previous := sharingReadTimeout
				sharingReadTimeout = 50 * time.Millisecond
				t.Cleanup(func() { sharingReadTimeout = previous })
			}
			rt := sharingRuntime(t, fake)
			if err := rt.store.RecordJobShareOutcome(context.Background(), "J4", shareOutcomePartial, []skippedShare{{Type: "user", ID: "alice"}}); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			rt.setupHandler(rec, callerReq(http.MethodGet, "/setup", "alice"))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pending":[]`) {
				t.Fatalf("GET /setup = %d %s", rec.Code, rec.Body.String())
			}
			var resp setupResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Sharing.Checked || len(resp.Sharing.Pending) != 0 || resp.Sharing.Refused != 0 || resp.Sharing.RefusedStarted != 0 {
				t.Fatalf("sharing = %+v, want unchecked and empty", resp.Sharing)
			}
		})
	}
}

// Without a caller there is nobody to read as, and without Nextcloud there
// are no shares: unchecked, not empty-and-true.
func TestSetupSharingIsUncheckedWithoutACallerOrNextcloud(t *testing.T) {
	fake := &sharingFake{pending: []map[string]any{pendingShare(34, 0, "/CassiniRecordings/meetings/J1.opus")}}
	rt := sharingRuntime(t, fake)
	if sharing := getSetup(t, rt, "").Sharing; sharing.Checked || len(sharing.Pending) != 0 {
		t.Fatalf("sharing without a caller = %+v", sharing)
	}
	if actor, ok := fake.actors["GET /ocs/v2.php/apps/files_sharing/api/v1/shares/pending"]; ok {
		t.Fatalf("read the pending list without a caller, as %q", actor)
	}
	rt.publishSink = &localPublishSink{siteRoot: t.TempDir(), logger: rt.logger}
	if sharing := getSetup(t, rt, "alice").Sharing; sharing.Checked || len(sharing.Pending) != 0 {
		t.Fatalf("sharing on the local sink = %+v", sharing)
	}
}

func TestPendingRecordingSharesAreCassinisMeetingFilesOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		share ncShare
		job   string
		ok    bool
	}{
		"a recording":             {ncShare{UIDFileOwner: "cassini", UIDOwner: "cassini", Path: "/CassiniRecordings/meetings/01ABC.opus", ItemType: "file"}, "01ABC", true},
		"a transcript-only one":   {ncShare{UIDFileOwner: "cassini", Path: "/CassiniRecordings/meetings/01ABC.json"}, "01ABC", true},
		"reshared by a recipient": {ncShare{UIDFileOwner: "cassini", UIDOwner: "bob", Path: "/Shares/01ABC.opus"}, "01ABC", true},
		"somebody else's file":    {ncShare{UIDFileOwner: "bob", UIDOwner: "bob", Path: "/01ABC.opus"}, "", false},
		"not a meeting file":      {ncShare{UIDFileOwner: "cassini", Path: "/CassiniRecordings/notes.txt"}, "", false},
		"a folder":                {ncShare{UIDFileOwner: "cassini", Path: "/CassiniRecordings/meetings", ItemType: "folder"}, "", false},
		"no path at all":          {ncShare{UIDFileOwner: "cassini"}, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			job, ok := pendingRecordingJobID(tc.share)
			if ok != tc.ok || job != tc.job {
				t.Fatalf("pendingRecordingJobID = %q, %v; want %q, %v", job, ok, tc.job, tc.ok)
			}
		})
	}
}

func TestMeetingFactsComeFromTheOperatorsOwnRecords(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	// A recorded meeting: title and audio length from the index, start from
	// the job. The audio length wins over the recording's span.
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J1", Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	indexMeeting(t, rt, "J1.opus", "Weekly sync", 2520000)
	setJobTimes(t, rt, "J1", "2026-10-30T10:00:00.000000000Z", "2026-10-30T10:45:00.000000000Z")
	facts := rt.meetingFactsFor(ctx, "J1")
	if facts.Title != "Weekly sync" || !facts.StartedAt.Equal(time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)) || facts.Duration != 42*time.Minute {
		t.Fatalf("facts = %+v", facts)
	}
	// No audio length indexed: the recording's own span stands in.
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J2", Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	setJobTimes(t, rt, "J2", "2026-10-31T09:00:00.000000000Z", "2026-10-31T09:10:00.000000000Z")
	if facts := rt.meetingFactsFor(ctx, "J2"); facts.Title != "" || facts.Duration != 10*time.Minute || facts.StartedAt.IsZero() {
		t.Fatalf("facts without an index entry = %+v", facts)
	}
	// An import has no job: its start is the lifecycle's createdAtUtc.
	if _, err := rt.store.db.Exec(`INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason)
VALUES('IMP1.opus',43,'CassiniRecordings/meetings/IMP1.opus','active','2026-09-01T08:00:00Z','createdAtUtc','2026-09-01T08:00:00Z','','')`); err != nil {
		t.Fatal(err)
	}
	indexMeeting(t, rt, "IMP1.opus", "Brought in", 65*60*1000)
	if facts := rt.meetingFactsFor(ctx, "IMP1"); facts.Title != "Brought in" || !facts.StartedAt.Equal(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)) || facts.Duration != 65*time.Minute {
		t.Fatalf("imported facts = %+v", facts)
	}
	// Unknown: empty, never an error.
	if facts := rt.meetingFactsFor(ctx, "NOPE"); facts != (meetingFacts{}) {
		t.Fatalf("unknown meeting facts = %+v", facts)
	}
}
