package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewQueueGroupsTheCallersPendingSuggestionsWithAQuote(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	search, err := openSearchStore(filepath.Join(t.TempDir(), "search.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { search.Close() })
	s.rt.searchStore = search
	if err := search.ReplaceMeeting(context.Background(), "MEETING1.opus", "d", searchRowSourceSegments, []searchRow{
		{SegmentID: "s1", StartMS: 0, EndMS: 30000, Text: "We cut the release on Thursday."},
		{SegmentID: "s2", StartMS: 90000, EndMS: 99000, Text: "Unrelated."},
	}); err != nil {
		t.Fatal(err)
	}
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	seedSuggestion(t, topics, "tag_audio", "Audio bugs", "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "SECRET.opus")
	seedSuggestion(t, topics, "tag_layoffs", "Layoffs", "SECRET.opus")
	rec := reviewCall(h, http.MethodGet, "review", "alice", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "SECRET") || strings.Contains(body, "Layoffs") {
		t.Fatalf("queue leaks a hidden meeting: %d %s", rec.Code, body)
	}
	var q topicQueue
	json.Unmarshal(rec.Body.Bytes(), &q)
	if q.Total != 2 || q.ByMeeting["MEETING1"] != 2 || len(q.Groups) != 2 || q.Coverage.Visible != 1 {
		t.Fatalf("queue %+v", q)
	}
	release := q.Groups[1]
	if release.TagID != "tag_release" || release.Total != 1 || release.Items[0].MeetingID != "MEETING1" || release.Items[0].Quote != "We cut the release on Thursday." {
		t.Fatalf("release group %+v", release)
	}
	// Paging.
	var page topicQueue
	json.Unmarshal(reviewCall(h, http.MethodGet, "review?limit=1", "alice", "").Body.Bytes(), &page)
	if len(page.Groups) != 1 || page.Groups[0].TagID != "tag_audio" || page.Next == "" {
		t.Fatalf("page %+v", page)
	}
	// Someone decides the first item meanwhile: the next page still starts
	// right after what was served, skipping nothing.
	topics.store.db.Exec(`UPDATE topic_suggestion SET state='decided' WHERE tag_id='tag_audio'`)
	cursor := page.Next
	page = topicQueue{}
	json.Unmarshal(reviewCall(h, http.MethodGet, "review?limit=1&cursor="+cursor, "alice", "").Body.Bytes(), &page)
	if len(page.Groups) != 1 || page.Groups[0].TagID != "tag_release" || page.Next != "" {
		t.Fatalf("second page %+v", page)
	}
	if rec := reviewCall(h, http.MethodGet, "review?cursor=1", "alice", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("a foreign cursor: %d", rec.Code)
	}
}

func TestBatchConfirmDecidesEachVisibleMeetingAndReplays(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "SECRET.opus")
	body := `{"requestId":"b1","tagId":"tag_release","meetingIds":["MEETING1","SECRET"],"decision":"confirm"}`
	rec := reviewCall(h, http.MethodPost, "review/batch", "alice", body)
	var out struct {
		Results []topicBatchResult `json:"results"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	if len(out.Results) != 2 || out.Results[0].Status != "confirmed" || out.Results[1].Status != "not-found" {
		t.Fatalf("results %+v", out.Results)
	}
	doc, _ := s.rt.annotationReads().document(context.Background(), "MEETING1.opus")
	if !strings.Contains(string(doc.Annotations), `"tag_release"`) {
		t.Fatalf("the library id is on the file: %s", doc.Annotations)
	}
	var n int
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_verdict WHERE opus_name='SECRET.opus'`).Scan(&n)
	if n != 0 {
		t.Fatal("a hidden meeting was decided")
	}
	again := reviewCall(h, http.MethodPost, "review/batch", "alice", body)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"replayed":true`) {
		t.Fatalf("replay: %d %s", again.Code, again.Body)
	}
	var targets int
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_receipt_target WHERE request_id='b1'`).Scan(&targets)
	if targets != 1 {
		t.Fatalf("receipt targets %d", targets)
	}
	// Retiring the meeting scrubs its entry; a replay says why it is gone.
	ctx := context.Background()
	if err := topics.store.inTx(ctx, func(tx *sql.Tx) error {
		return forgetTopicMeeting(ctx, tx, "MEETING1.opus", meetingRetireReasonExpired)
	}); err != nil {
		t.Fatal(err)
	}
	again = reviewCall(h, http.MethodPost, "review/batch", "alice", body)
	if again.Code != http.StatusOK || json.Unmarshal(again.Body.Bytes(), &out) != nil || len(out.Results) != 2 || out.Results[0].Status != "expired" || strings.Contains(again.Body.String(), "Release") {
		t.Fatalf("replay after retirement: %d %s", again.Code, again.Body)
	}
	ids := make([]string, topicBatchMax+1)
	for i := range ids {
		ids[i] = "MEETING1"
	}
	raw, _ := json.Marshal(map[string]any{"requestId": "b2", "tagId": "tag_release", "meetingIds": ids, "decision": "reject"})
	if rec := reviewCall(h, http.MethodPost, "review/batch", "alice", string(raw)); rec.Code != http.StatusBadRequest {
		t.Fatalf("101 meetings: %d", rec.Code)
	}
}

func TestBatchIsClaimedOnceValidatedAndReplaysOnlyWhatIsVisible(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	if rec := reviewCall(h, http.MethodPost, "review/batch", "alice", `{"requestId":"b0","tagId":"tag_release","meetingIds":["MEETING1","MEETING1"],"decision":"reject"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate ids: %d", rec.Code)
	}
	// Another request holds the id: refused, not run twice.
	topics.store.db.Exec(`INSERT INTO topic_receipt(caller,request_id,request_hash,response,created_at) VALUES('alice','b1',?,'{"pending":true}',?)`,
		topicRequestHash("batch", "tag_release", "reject", []string{"MEETING1"}), topics.store.unix())
	if rec := reviewCall(h, http.MethodPost, "review/batch", "alice", `{"requestId":"b1","tagId":"tag_release","meetingIds":["MEETING1"],"decision":"reject"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "in-progress") {
		t.Fatalf("in progress: %d %s", rec.Code, rec.Body)
	}
	if rec := reviewCall(h, http.MethodPost, "review/batch", "alice", `{"requestId":"b1","tagId":"tag_other","meetingIds":["MEETING1"],"decision":"reject"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "request-collision") {
		t.Fatalf("collision: %d %s", rec.Code, rec.Body)
	}
	var n int
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_verdict`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused batch decided something")
	}
	// A stored answer naming a meeting the caller can no longer read.
	topics.store.db.Exec(`INSERT INTO topic_receipt(caller,request_id,request_hash,response,created_at) VALUES('alice','b2',?,'{"results":[{"meetingId":"SECRET","status":"confirmed"}]}',?)`,
		topicRequestHash("batch", "tag_release", "confirm", []string{"SECRET"}), topics.store.unix())
	rec := reviewCall(h, http.MethodPost, "review/batch", "alice", `{"requestId":"b2","tagId":"tag_release","meetingIds":["SECRET"],"decision":"confirm"}`)
	if !strings.Contains(rec.Body.String(), `"status":"not-found"`) || strings.Contains(rec.Body.String(), `"confirmed"`) {
		t.Fatalf("replay of a hidden meeting: %s", rec.Body)
	}
}

func TestQueueRoomKeysMatchTheViewers(t *testing.T) {
	entries := []catalogHydration{{id: "a", opusName: "a.opus", roomID: "r1"}, {id: "b", opusName: "b.opus", roomName: "Board"}, {id: "c", opusName: "c.opus"}}
	for room, want := range map[string]string{"id:r1": "a", "r1": "a", "name:Board": "b", "no-room": "c"} {
		var got []string
		for _, e := range entries {
			if topicRoomMatches(e, room) {
				got = append(got, e.id)
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("room %q: %v", room, got)
		}
	}
}
