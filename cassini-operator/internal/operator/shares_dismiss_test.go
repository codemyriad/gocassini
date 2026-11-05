package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func postDismiss(t *testing.T, rt *Runtime, caller, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/shares/dismiss", strings.NewReader(body))
	req = req.WithContext(callerReq(http.MethodPost, "/shares/dismiss", caller).Context())
	rec := httptest.NewRecorder()
	rt.sharesDismissHandler(rec, req)
	return rec
}

// seedStarterRefused leaves a job failed because Nextcloud refused its
// starter, recorded in the named room at the given time.
func seedStarterRefused(t *testing.T, rt *Runtime, id, starter, room, started string) {
	t.Helper()
	seedJobRow(t, rt.store.db, seededJobRow{ID: id, Stage: "publish", State: "failed", CreatedAt: started})
	ctx := context.Background()
	if err := rt.store.SetJobTalkBinding(ctx, id, `{"backend_url":"https://talk.example","room_token":"tok-`+id+`","owner":"`+starter+`","room_name":"`+room+`","room_type":2}`); err != nil {
		t.Fatal(err)
	}
	if err := rt.store.RecordJobShareOutcome(ctx, id, shareOutcomeStarterRefused, []skippedShare{{Type: "user", ID: starter}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE jobs SET error=?, record_started_at=? WHERE id=?`, "the recording "+starterRefusedMarker+", "+starter+": refused", started, id); err != nil {
		t.Fatal(err)
	}
}

// Each recording the caller started that could not be published is named on
// its own, with its room and when it started, newest first; a dismissed one
// is gone for that caller for good, and for nobody else.
func TestSetupNamesEachUnpublishedRecordingAndForgetsADismissedOne(t *testing.T) {
	rt := sharingRuntime(t, &sharingFake{groups: []string{"staff"}, pending: []map[string]any{}})
	seedStarterRefused(t, rt, "J20", "alice", "Weekly sync", "2026-10-30T10:00:00.000000000Z")
	seedStarterRefused(t, rt, "J21", "alice", "Design review", "2026-10-31T15:00:00.000000000Z")
	seedStarterRefused(t, rt, "J22", "bob", "Bob's room", "2026-10-31T16:00:00.000000000Z")

	sharing := getSetup(t, rt, "alice").Sharing
	want := []recordingNotice{
		{JobID: "J21", RoomName: "Design review", StartedAt: "2026-10-31T15:00:00Z"},
		{JobID: "J20", RoomName: "Weekly sync", StartedAt: "2026-10-30T10:00:00Z"},
	}
	if sharing.RefusedStarted != 2 || !reflect.DeepEqual(sharing.RefusedStartedMeetings, want) {
		t.Fatalf("refused_started = %d %+v\nwant 2 %+v", sharing.RefusedStarted, sharing.RefusedStartedMeetings, want)
	}

	// Somebody else dismissing alice's notice changes nothing for her.
	if rec := postDismiss(t, rt, "bob", `{"job_ids":["J21"]}`); rec.Code != http.StatusOK {
		t.Fatalf("bob's dismiss = %d %s", rec.Code, rec.Body.String())
	}
	if got := getSetup(t, rt, "alice").Sharing.RefusedStarted; got != 2 {
		t.Fatalf("after bob's dismiss alice has %d, want 2", got)
	}

	rec := postDismiss(t, rt, "alice", `{"job_ids":["J21"," J21 ","NOPE"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /shares/dismiss = %d %s", rec.Code, rec.Body.String())
	}
	var answer sharesDismissResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || !reflect.DeepEqual(answer.Dismissed, []string{"J21", "NOPE"}) {
		t.Fatalf("answer = %s (%v)", rec.Body.String(), err)
	}
	sharing = getSetup(t, rt, "alice").Sharing
	if sharing.RefusedStarted != 1 || len(sharing.RefusedStartedMeetings) != 1 || sharing.RefusedStartedMeetings[0].JobID != "J20" {
		t.Fatalf("after dismissing J21: %+v", sharing)
	}
	// Dismissed for good: a rerun that is refused again does not bring it back.
	if err := rt.store.RecordJobShareOutcome(context.Background(), "J21", shareOutcomeStarterRefused, []skippedShare{{Type: "user", ID: "alice"}}); err != nil {
		t.Fatal(err)
	}
	if got := getSetup(t, rt, "alice").Sharing.RefusedStarted; got != 1 {
		t.Fatalf("a re-refused, dismissed notice came back: %d", got)
	}
	// An unknown id stores nothing.
	var count int
	if err := rt.store.db.QueryRow(`SELECT COUNT(*) FROM share_notice_dismissals WHERE job_id='NOPE'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dismissal rows for an unknown job = %d, %v", count, err)
	}
	// Bob's own notice is untouched by any of it.
	if got := getSetup(t, rt, "bob").Sharing.RefusedStartedMeetings; len(got) != 1 || got[0].JobID != "J22" {
		t.Fatalf("bob's notices = %+v", got)
	}
}

// A published meeting Nextcloud would not share with the caller — the
// customer's case: cassini only in "admin" — is named the same way, title,
// room and time, newest first, and dismissed the same way.
func TestSetupNamesEachRefusedMeetingAndForgetsADismissedOne(t *testing.T) {
	rt := sharingRuntime(t, &sharingFake{groups: []string{"staff"}, pending: []map[string]any{}})
	ctx := context.Background()
	for _, row := range []struct{ id, room, started string }{
		{"J30", "Weekly sync", "2026-10-30T10:00:00.000000000Z"},
		{"J31", "All hands", "2026-10-31T15:00:00.000000000Z"},
	} {
		seedJobRow(t, rt.store.db, seededJobRow{ID: row.id, Stage: "done", State: "succeeded", CreatedAt: row.started})
		if err := rt.store.SetJobTalkBinding(ctx, row.id, `{"backend_url":"https://talk.example","room_token":"tok-`+row.id+`","owner":"bob","room_name":"`+row.room+`","room_type":2}`); err != nil {
			t.Fatal(err)
		}
		setJobTimes(t, rt, row.id, row.started, "")
		if err := rt.store.RecordJobShareOutcome(ctx, row.id, shareOutcomePartial, []skippedShare{{Type: "user", ID: "alice", Reason: "Sharing is only allowed with group members"}}); err != nil {
			t.Fatal(err)
		}
	}
	indexMeeting(t, rt, "J30.opus", "Weekly sync, 30 Oct", 0)

	sharing := getSetup(t, rt, "alice").Sharing
	want := []recordingNotice{
		{JobID: "J31", RoomName: "All hands", StartedAt: "2026-10-31T15:00:00Z"},
		{JobID: "J30", Title: "Weekly sync, 30 Oct", RoomName: "Weekly sync", StartedAt: "2026-10-30T10:00:00Z"},
	}
	if sharing.Refused != 2 || !reflect.DeepEqual(sharing.RefusedMeetings, want) {
		t.Fatalf("refused = %d %+v\nwant 2 %+v", sharing.Refused, sharing.RefusedMeetings, want)
	}
	if rec := postDismiss(t, rt, "alice", `{"job_ids":["J31"]}`); rec.Code != http.StatusOK {
		t.Fatalf("POST /shares/dismiss = %d %s", rec.Code, rec.Body.String())
	}
	sharing = getSetup(t, rt, "alice").Sharing
	if sharing.Refused != 1 || len(sharing.RefusedMeetings) != 1 || sharing.RefusedMeetings[0].JobID != "J30" {
		t.Fatalf("after dismissing J31: %+v", sharing)
	}
}

func TestDismissRefusesWhatItCannotRecord(t *testing.T) {
	rt := sharingRuntime(t, &sharingFake{})
	for name, tc := range map[string]struct {
		caller, body string
		code         int
	}{
		"no caller":   {"", `{"job_ids":["J1"]}`, http.StatusBadGateway},
		"not JSON":    {"alice", `nope`, http.StatusBadRequest},
		"nothing":     {"alice", `{"job_ids":[" "]}`, http.StatusBadRequest},
		"too many":    {"alice", `{"job_ids":[` + manyIDs(maxShareDismissIDs+1) + `]}`, http.StatusBadRequest},
		"wrong route": {"alice", `{"job_ids":["J1"]}`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "wrong route" {
				rec := httptest.NewRecorder()
				rt.sharesDismissHandler(rec, callerReq(http.MethodGet, "/shares/dismiss", "alice"))
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("GET = %d", rec.Code)
				}
				return
			}
			if rec := postDismiss(t, rt, tc.caller, tc.body); rec.Code != tc.code {
				t.Fatalf("= %d %s, want %d", rec.Code, rec.Body.String(), tc.code)
			}
		})
	}
}

func manyIDs(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = `"id-` + strconv.Itoa(i) + `"`
	}
	return strings.Join(ids, ",")
}

// Through Nextcloud the operator is reached only on routes info.xml declares.
// This one is USER: every signed-in person dismisses their own notices.
func TestDismissSharesRouteIsDeclaredForUsers(t *testing.T) {
	raw, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`(?s)<route>\s*<url>([^<]+)</url>\s*<verb>([^<]+)</verb>\s*<access_level>([^<]+)</access_level>`).FindAllStringSubmatch(string(raw), -1)
	for _, route := range routes {
		pattern, err := regexp.Compile(route[1])
		if err != nil {
			continue
		}
		if pattern.MatchString("operator/shares/dismiss") && strings.Contains(route[2], "POST") {
			if route[3] != "USER" {
				t.Fatalf("shares/dismiss is declared with access %q, want USER", route[3])
			}
			return
		}
	}
	t.Fatal("no info.xml route lets POST operator/shares/dismiss through AppAPI")
}
