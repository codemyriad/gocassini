package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// setTalkBindingRaw writes a binding blob without going through
// SetJobTalkBinding, so a test can stage the pre-0008 state: a row whose room
// exists only inside the JSON.
func setTalkBindingRaw(t *testing.T, db *sql.DB, id, bindingJSON string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE jobs SET talk_binding = ? WHERE id = ?`, bindingJSON, id); err != nil {
		t.Fatalf("seed talk_binding for %s: %v", id, err)
	}
}

func jobRoomColumns(t *testing.T, db *sql.DB, id string) (roomToken, roomName sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT room_token, room_name FROM jobs WHERE id = ?`, id).Scan(&roomToken, &roomName); err != nil {
		t.Fatalf("read room columns for %s: %v", id, err)
	}
	return roomToken, roomName
}

// TestJobRoomColumnsMigrationBackfillsFromTalkBinding runs migration 0008
// exactly as an installation upgrading from the last release would: the rooms
// of every job it has already recorded live only inside talk_binding, and the
// migration is the only thing that recovers them.
func TestJobRoomColumnsMigrationBackfillsFromTalkBinding(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))

	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cases := []struct {
		id        string
		binding   string
		wantToken string
		wantName  string
	}{
		{
			id:        "with-name",
			binding:   `{"backend_url":"https://nc.test","room_token":"tok-named","owner":"alice","room_name":"Weekly sync"}`,
			wantToken: "tok-named",
			wantName:  "Weekly sync",
		},
		{
			// room_name is omitempty, so a binding whose name lookup never
			// completed — and every binding written before D-622 — has no key
			// at all. It must land as NULL, not "".
			id:        "legacy-no-name",
			binding:   `{"backend_url":"https://nc.test","room_token":"tok-legacy","owner":"bob"}`,
			wantToken: "tok-legacy",
		},
		{
			// A non-Talk job never had a binding and must stay roomless.
			id:      "no-binding",
			binding: "",
		},
		{
			// json_valid guards the UPDATE: an unreadable blob costs its
			// columns, it does not fail the migration for every other row.
			id:      "unreadable",
			binding: "not json at all",
		},
	}

	for _, tc := range cases {
		seedJobRow(t, store.db, seededJobRow{ID: tc.id, Stage: "record", State: "queued", CreatedAt: "2026-08-28T10:00:00.000000000Z"})
		if tc.binding != "" {
			setTalkBindingRaw(t, store.db, tc.id, tc.binding)
		}
	}

	if err := store.migrateDownTo(7); err != nil {
		t.Fatalf("migrateDownTo(7): %v", err)
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema(): %v", err)
	}

	for _, tc := range cases {
		gotToken, gotName := jobRoomColumns(t, store.db, tc.id)
		if gotToken.String != tc.wantToken || gotToken.Valid != (tc.wantToken != "") {
			t.Errorf("%s: room_token = %#v, want %q", tc.id, gotToken, tc.wantToken)
		}
		if gotName.String != tc.wantName || gotName.Valid != (tc.wantName != "") {
			t.Errorf("%s: room_name = %#v, want %q", tc.id, gotName, tc.wantName)
		}
	}
}

// TestSetJobTalkBindingPromotesRoomColumnsWithoutBlanking covers the write
// path's half of the same guarantee. The name is resolved asynchronously and
// arrives on a second write, so the columns have to survive a binding that
// carries no name — otherwise every job whose name lookup was slow, or failed,
// would end up with the name blanked by its own re-persist.
func TestSetJobTalkBindingPromotesRoomColumnsWithoutBlanking(t *testing.T) {
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))

	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	seedJobRow(t, store.db, seededJobRow{ID: "job-1", Stage: "record", State: "queued", CreatedAt: "2026-08-28T10:00:00.000000000Z"})

	// First write, at Talk start: the name is not known yet.
	atStart := `{"backend_url":"https://nc.test","room_token":"tok-1","owner":"alice"}`
	if err := store.SetJobTalkBinding(ctx, "job-1", atStart); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	gotToken, gotName := jobRoomColumns(t, store.db, "job-1")
	if gotToken.String != "tok-1" {
		t.Fatalf("room_token after start = %#v, want tok-1", gotToken)
	}
	if gotName.Valid {
		t.Fatalf("room_name after start = %#v, want NULL", gotName)
	}

	// Second write, once resolveTalkRoomName answered.
	withName := `{"backend_url":"https://nc.test","room_token":"tok-1","owner":"alice","room_name":"Weekly sync"}`
	if err := store.SetJobTalkBinding(ctx, "job-1", withName); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	if _, gotName = jobRoomColumns(t, store.db, "job-1"); gotName.String != "Weekly sync" {
		t.Fatalf("room_name after resolve = %#v, want Weekly sync", gotName)
	}

	// A later re-persist that carries no name must not undo the one that
	// succeeded: blank means "nothing to say", not "set to empty".
	if err := store.SetJobTalkBinding(ctx, "job-1", atStart); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	if _, gotName = jobRoomColumns(t, store.db, "job-1"); gotName.String != "Weekly sync" {
		t.Fatalf("room_name after nameless re-persist = %#v, want Weekly sync", gotName)
	}

	// An unreadable blob still has to be stored — it is the crash-safe
	// delivery record — it just contributes no columns.
	if err := store.SetJobTalkBinding(ctx, "job-1", "not json at all"); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	job := mustGetJob(t, store, "job-1")
	if job.TalkBinding == nil || *job.TalkBinding != "not json at all" {
		t.Fatalf("talk_binding after unreadable write = %v, want it stored", job.TalkBinding)
	}
	if job.RoomToken == nil || *job.RoomToken != "tok-1" || job.RoomName == nil || *job.RoomName != "Weekly sync" {
		t.Fatalf("room columns after unreadable write = %v / %v, want them kept", job.RoomToken, job.RoomName)
	}
}

// TestJobsHandlerExposesRoomNameButNotTheToken pins the D-622 boundary. For a
// public conversation the Talk token is also the link that joins it, so what
// leaves the operator is the room's name and never its token — the same reason
// talk_binding has always been withheld.
func TestJobsHandlerExposesRoomNameButNotTheToken(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	seedJobRow(t, rt.store.db, seededJobRow{ID: "job-room", Stage: "publish", State: "succeeded", CreatedAt: "2026-08-28T10:00:00.000000000Z"})
	binding := `{"backend_url":"https://nc.test","room_token":"tok-secret","owner":"alice","room_name":"Weekly sync"}`
	if err := rt.store.SetJobTalkBinding(context.Background(), "job-room", binding); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}

	rec := httptest.NewRecorder()
	rt.jobsHandler(rec, httptest.NewRequest(http.MethodGet, "/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var jobs []Job
	if err := json.Unmarshal(rec.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %d", len(jobs))
	}
	if jobs[0].RoomName == nil || *jobs[0].RoomName != "Weekly sync" {
		t.Fatalf("room_name = %v, want Weekly sync", jobs[0].RoomName)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"room_name":"Weekly sync"`) {
		t.Fatalf("room_name missing from the response body: %s", body)
	}
	for _, withheld := range []string{"tok-secret", "room_token", "talk_binding", "backend_url"} {
		if strings.Contains(body, withheld) {
			t.Fatalf("response leaks %q: %s", withheld, body)
		}
	}
}
