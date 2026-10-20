package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTalkRoomNameFetcherKeepsRoomType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ocs":{"data":{"displayName":"Chris","type":1}}}`))
	}))
	defer server.Close()

	cfg := ExAppConfig{NextcloudURL: server.URL, AppSecret: "s3cret", AppID: "gocassini"}
	info, err := cfg.talkRoomNameFetcher()(context.Background(), "alice", "tok123")
	if err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if info.Type != talkRoomTypeOneToOne {
		t.Errorf("Type = %d, want %d", info.Type, talkRoomTypeOneToOne)
	}
	if info.Public {
		t.Error("Public = true, want false for a one-to-one room")
	}
}

func TestTalkBindingRoundTripsRoomType(t *testing.T) {
	encoded, err := encodeTalkBinding(&talkRoomState{
		BackendURL: "http://backend",
		RoomToken:  "tok123",
		RoomType:   talkRoomTypeOneToOne,
	})
	if err != nil {
		t.Fatalf("encodeTalkBinding() error = %v", err)
	}
	state, err := decodeTalkBinding(encoded)
	if err != nil {
		t.Fatalf("decodeTalkBinding() error = %v", err)
	}
	if state.RoomType != talkRoomTypeOneToOne {
		t.Errorf("RoomType = %d, want %d", state.RoomType, talkRoomTypeOneToOne)
	}
}

// writeSessionFixture places a session.json where the run bundle keeps it.
func writeSessionFixture(t *testing.T, runPath, body string) {
	t.Helper()
	dir := filepath.Join(runPath, "session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write session.json: %v", err)
	}
}

func TestOneToOneParticipantNames(t *testing.T) {
	cases := []struct {
		name    string
		session string
		want    []string
	}{
		{
			name: "two participants, sorted",
			session: `{"participants":[
				{"pid":"p2","display":"Chris"},
				{"pid":"p1","display":"Alex"}]}`,
			want: []string{"Alex", "Chris"},
		},
		{
			name: "placeholders and the recorder are dropped",
			session: `{"platform":{"recorder_identity":{"display":"CassiniRecorder"}},
				"participants":[
				{"pid":"p1","display":"Alex"},
				{"pid":"p2","display":"participant-abc123"},
				{"pid":"p3","display":"CassiniRecorder"},
				{"pid":"p4","display":""},
				{"pid":"p5","display":"Chris"}]}`,
			want: []string{"Alex", "Chris"},
		},
		{
			name: "a rejoining participant is one person",
			session: `{"participants":[
				{"pid":"p1","display":"Alex"},
				{"pid":"p2","display":"Alex"},
				{"pid":"p3","display":"Chris"}]}`,
			want: []string{"Alex", "Chris"},
		},
		{
			name: "a display equal to its pid is a placeholder",
			session: `{"participants":[
				{"pid":"deadbeef","display":"deadbeef"},
				{"pid":"p2","display":"Chris"}]}`,
			want: []string{"Chris"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runPath := t.TempDir()
			writeSessionFixture(t, runPath, tc.session)
			got := oneToOneParticipantNames(runPath)
			if len(got) != len(tc.want) {
				t.Fatalf("names = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("names = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestOneToOneParticipantNamesWithoutSession(t *testing.T) {
	if got := oneToOneParticipantNames(t.TempDir()); got != nil {
		t.Errorf("names = %v, want nil for a run bundle without session.json", got)
	}
	if got := oneToOneParticipantNames(""); got != nil {
		t.Errorf("names = %v, want nil for an empty run path", got)
	}
}

func TestDefaultMeetingTitleComposesPairForOneToOne(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-pair")
	rt.talkRoomNameRetryGap = time.Millisecond
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		return talkRoomInfo{Name: "Chris", Type: talkRoomTypeOneToOne}, nil
	}
	rt.resolveTalkRoomName("job-pair", "alice", "tok123")

	runPath := t.TempDir()
	writeSessionFixture(t, runPath, `{"participants":[
		{"pid":"p1","display":"Chris"},{"pid":"p2","display":"Alex"}]}`)

	if got := rt.defaultMeetingTitle("job-pair", runPath, "Chris"); got != "Alex & Chris" {
		t.Errorf("defaultMeetingTitle() = %q, want %q", got, "Alex & Chris")
	}

	// The composed title must also come from the persisted binding alone: a
	// restart between record and build drops the in-memory state (D-352).
	rt.recordMu.Lock()
	delete(rt.talkJobs, "job-pair")
	rt.recordMu.Unlock()
	if got := rt.defaultMeetingTitle("job-pair", runPath, "Chris"); got != "Alex & Chris" {
		t.Errorf("defaultMeetingTitle() after restart = %q, want %q", got, "Alex & Chris")
	}
}

func TestDefaultMeetingTitleFallsBackToRoomName(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-fallbacks")
	rt.talkRoomNameRetryGap = time.Millisecond

	// A group room keeps its own name even with a clean two-person roster.
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		return talkRoomInfo{Name: "Ops Standup", Type: 2}, nil
	}
	rt.resolveTalkRoomName("job-fallbacks", "alice", "tok123")
	runPath := t.TempDir()
	writeSessionFixture(t, runPath, `{"participants":[
		{"pid":"p1","display":"Chris"},{"pid":"p2","display":"Alex"}]}`)
	if got := rt.defaultMeetingTitle("job-fallbacks", runPath, "Ops Standup"); got != "Ops Standup" {
		t.Errorf("group room title = %q, want %q", got, "Ops Standup")
	}

	// A one-to-one whose roster is not exactly two trusted names keeps the
	// room name rather than guessing a pairing.
	rt.releaseTalkRoom("backend|tok123") // seedTalkJob reserves one fixed room key
	seedTalkJob(t, rt, "job-crowd")
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		return talkRoomInfo{Name: "Chris", Type: talkRoomTypeOneToOne}, nil
	}
	rt.resolveTalkRoomName("job-crowd", "alice", "tok123")
	crowded := t.TempDir()
	writeSessionFixture(t, crowded, `{"participants":[
		{"pid":"p1","display":"Chris"},{"pid":"p2","display":"Alex"},{"pid":"p3","display":"Silvio"}]}`)
	if got := rt.defaultMeetingTitle("job-crowd", crowded, "Chris"); got != "Chris" {
		t.Errorf("crowded one-to-one title = %q, want %q", got, "Chris")
	}
	// And so does one whose run bundle has no session.json at all.
	if got := rt.defaultMeetingTitle("job-crowd", t.TempDir(), "Chris"); got != "Chris" {
		t.Errorf("sessionless one-to-one title = %q, want %q", got, "Chris")
	}
}
