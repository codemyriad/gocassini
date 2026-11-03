package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

// seedSuggestion makes a topic and a pending suggestion of it on a meeting.
func seedSuggestion(t *testing.T, topics *topicsService, tagID, label, opusName string) {
	t.Helper()
	db := topics.store.db
	if _, err := db.Exec(`INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,color,status,origin,revision,created_by,created_at,updated_by,updated_at)
 VALUES(?,?,?,'About '||?,'sha','teal','active','manual',1,'bob',1,'bob',1) ON CONFLICT(tag_id) DO NOTHING`, tagID, label, foldTagLabel(label), label); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO topic_suggestion(opus_name,tag_id,state,rule,support_words,peak,min_margin,definition_sha,scorer_key,created_at,updated_at)
 VALUES(?,?,'pending','supporting-words',120,0.91,0.12,'sha','sk',1,1)`, opusName, tagID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO topic_evidence(opus_name,tag_id,ordinal,start_ms,end_ms,word_count,counted) VALUES(?,?,0,1000,40000,120,1)`, opusName, tagID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO topic_meeting(opus_name,job_id,state,priority,language,detector_version,payload_sha256,queued_at) VALUES(?,?,'done',0,'en',?,'payload',1) ON CONFLICT(opus_name) DO NOTHING`, opusName, meetingStem(opusName), topicsExpectedDetectorVersion); err != nil {
		t.Fatal(err)
	}
}

func reviewCall(h http.Handler, method, path, caller, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/annotations/topics/"+path, reader)
	req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func meetingSuggestions(t *testing.T, h http.Handler, caller string) topicMeetingView {
	t.Helper()
	rec := reviewCall(h, http.MethodGet, "meetings/MEETING1", caller, "")
	var view topicMeetingView
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil {
		t.Fatalf("suggestions: %d %s", rec.Code, rec.Body)
	}
	return view
}

func TestASuggestionIsServedWithEvidenceAndNoScores(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	seedSuggestion(t, topics, "tag_secret", "Layoffs", "SECRET.opus")
	rec := reviewCall(h, http.MethodGet, "meetings/MEETING1", "alice", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "0.91") || strings.Contains(body, "0.12") || strings.Contains(body, "peak") {
		t.Fatalf("scores leak: %d %s", rec.Code, body)
	}
	view := meetingSuggestions(t, h, "alice")
	if len(view.Suggestions) != 1 || view.Suggestions[0].TagID != "tag_release" || len(view.Suggestions[0].Evidence) != 1 || view.Suggestions[0].SupportWords != 120 {
		t.Fatalf("view %+v", view)
	}
	if view.State != topicStateDone || view.Language != "en" || !view.ValidatedLanguage || !view.Confirmable {
		t.Fatalf("meeting state %+v", view)
	}
	if rec := reviewCall(h, http.MethodGet, "meetings/SECRET", "alice", ""); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Layoffs") {
		t.Fatalf("a hidden meeting's suggestions: %d %s", rec.Code, rec.Body)
	}
}

func TestConfirmWritesAPersonsMarkAndTheVerdictTogether(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	doc, err := s.rt.annotationReads().document(ctx, "MEETING1.opus")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(doc.Annotations)
	if !strings.Contains(raw, `"tag_release"`) || !strings.Contains(raw, `"kind":"person","id":"alice"`) || !strings.Contains(raw, testTagNamespaceA) {
		t.Fatalf("the mark is the person's, in the deployment namespace: %s", raw)
	}
	var verdict, source, actor string
	topics.store.db.QueryRow(`SELECT verdict,source,actor_id FROM topic_verdict WHERE opus_name='MEETING1.opus' AND tag_id='tag_release'`).Scan(&verdict, &source, &actor)
	var state string
	topics.store.db.QueryRow(`SELECT state FROM topic_suggestion WHERE opus_name='MEETING1.opus' AND tag_id='tag_release'`).Scan(&state)
	if verdict != "confirmed" || source != "review" || actor != "alice" || state != "decided" {
		t.Fatalf("verdict %s %s %s, suggestion %s", verdict, source, actor, state)
	}
	// A retry returns the first answer, even after the library colour moved.
	topics.store.db.Exec(`UPDATE topic_library SET color='red' WHERE tag_id='tag_release'`)
	again := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"replayed":true`) {
		t.Fatalf("retried confirm: %d %s", again.Code, again.Body)
	}
	if view := meetingSuggestions(t, h, "alice"); len(view.Suggestions) != 0 || len(view.Decided) != 1 {
		t.Fatalf("after confirm %+v", view)
	}

	// Undo takes the mark back and the suggestion is pending again at once.
	undo := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"u1","decision":"undo"}`)
	if undo.Code != http.StatusOK {
		t.Fatalf("undo: %d %s", undo.Code, undo.Body)
	}
	doc, _ = s.rt.annotationReads().document(ctx, "MEETING1.opus")
	if strings.Contains(string(doc.Annotations), `"tag_release"`) {
		t.Fatalf("undo leaves the mark: %s", doc.Annotations)
	}
	if view := meetingSuggestions(t, h, "alice"); len(view.Suggestions) != 1 || len(view.Decided) != 0 {
		t.Fatalf("after undo %+v", view)
	}
}

func TestConfirmMarksTheFilesOwnTagOfTheSameName(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	// MEETING1 carries "budget" as tag_budget; the library's budget has its own id.
	seedSuggestion(t, topics, "tag_lib_budget", "Budget", "MEETING1.opus")
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_lib_budget", "alice", `{"requestId":"c1","decision":"confirm"}`); rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	doc, _ := s.rt.annotationReads().document(context.Background(), "MEETING1.opus")
	if strings.Contains(string(doc.Annotations), "tag_lib_budget") || strings.Count(strings.ToLower(string(doc.Annotations)), `"label":"budget"`) != 1 {
		t.Fatalf("one chip per name: %s", doc.Annotations)
	}
	var alias, local string
	topics.store.db.QueryRow(`SELECT tag_id FROM topic_tag_alias WHERE alias_tag_id='tag_budget'`).Scan(&alias)
	topics.store.db.QueryRow(`SELECT local_tag_id FROM topic_verdict WHERE tag_id='tag_lib_budget'`).Scan(&local)
	if alias != "tag_lib_budget" || local != "tag_budget" {
		t.Fatalf("alias %q local %q", alias, local)
	}
}

func TestRejectionIsDurableAndReopenBringsItBack(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"r1","decision":"reject"}`); rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "bob", `{"requestId":"c1","decision":"confirm"}`); rec.Code != http.StatusConflict {
		t.Fatalf("a confirm after a reject: %d %s", rec.Code, rec.Body)
	}
	// The scorer never undoes it: a rescore that qualifies leaves it decided.
	topic := topicForScoring{TagID: "tag_release", LabelFolded: "release planning", DefinitionSHA: "sha", Vector: []float32{1, 0}}
	scores := scoreMeetingTopics([]topicPassage{{WordCount: 120, Vector: []float32{1, 0}}}, topicScoringInputs{Controls: [][]float32{{0, 1}}, Topics: []topicForScoring{topic}})
	if err := topics.store.inTx(ctx, func(tx *sql.Tx) error { return topics.store.applyScores(ctx, tx, "MEETING1.opus", "p", scores) }); err != nil {
		t.Fatal(err)
	}
	if view := meetingSuggestions(t, h, "alice"); len(view.Suggestions) != 0 || view.Decided[0].Verdict != "rejected" {
		t.Fatalf("a rejection survives a rescore: %+v", view)
	}
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"o1","decision":"reopen"}`); rec.Code != http.StatusOK {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body)
	}
	if view := meetingSuggestions(t, h, "alice"); len(view.Suggestions) != 1 || len(view.Decided) != 0 {
		t.Fatalf("reopened: %+v", view)
	}
}

func TestConfirmIsRefusedOnUnresolvedMarksButRejectWorks(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	seedSuggestion(t, topics, "tag_other", "Other", "MEETING1.opus")
	// The audio changed under the existing marks.
	store := s.rt.annotationReads()
	doc, _ := store.document(context.Background(), "MEETING1.opus")
	resolved := false
	doc.Resolved = &resolved
	doc.AudioOpusSHA256 = strings.Repeat("cd", 32)
	data, _ := json.Marshal(doc)
	if _, err := store.db.Exec(`UPDATE annotation_snapshot SET result_json=? WHERE id=(SELECT desired FROM annotation_head WHERE opus_name='MEETING1.opus')`, data); err != nil {
		t.Fatal(err)
	}
	if view := meetingSuggestions(t, h, "alice"); view.Confirmable || view.BlockedReason != "marks-unresolved" {
		t.Fatalf("view %+v", view)
	}
	rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "marks-unresolved") {
		t.Fatalf("confirm on unresolved marks: %d %s", rec.Code, rec.Body)
	}
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_other", "alice", `{"requestId":"r1","decision":"reject"}`); rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
}

func TestTopicCoverageCountsOnlyVisibleMeetings(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	topics.store.db.Exec(`INSERT INTO topic_meeting(opus_name,job_id,state,priority,reason,queued_at) VALUES('SECRET.opus','SECRET','unavailable',0,'no-transcript',1)`)
	rec := reviewCall(h, http.MethodGet, "coverage", "alice", "")
	var c topicCoverage
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("coverage: %d %s", rec.Code, rec.Body)
	}
	if c.Visible != 1 || c.Checked != 1 || len(c.Unavailable) != 0 || c.UnvalidatedLanguage != 0 {
		t.Fatalf("coverage %+v", c)
	}
}

func TestConcurrentConfirmAndRejectLeaveOneVerdictThatMatchesTheFile(t *testing.T) {
	for i := range 5 {
		s, h, topics := topicLibraryService(t, "MEETING1.opus")
		seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
		done := make(chan int, 2)
		go func() {
			done <- reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`).Code
		}()
		go func() {
			done <- reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "bob", `{"requestId":"r1","decision":"reject"}`).Code
		}()
		codes := []int{<-done, <-done}
		ok := 0
		for _, code := range codes {
			if code == http.StatusOK {
				ok++
			}
		}
		var verdicts int
		var verdict string
		topics.store.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(verdict),'') FROM topic_verdict WHERE tag_id='tag_release'`).Scan(&verdicts, &verdict)
		doc, _ := s.rt.annotationReads().document(context.Background(), "MEETING1.opus")
		marked := strings.Contains(string(doc.Annotations), `"tag_release"`)
		if ok != 1 || verdicts != 1 || marked != (verdict == "confirmed") {
			t.Fatalf("run %d: codes %v, %d verdicts (%s), marked %v", i, codes, verdicts, verdict, marked)
		}
	}
}

func TestRetriedUndoReplaysAndKeepsTheEvidence(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`)
	for i := range 2 {
		if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"u1","decision":"undo"}`); rec.Code != http.StatusOK {
			t.Fatalf("undo try %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	view := meetingSuggestions(t, h, "alice")
	if len(view.Suggestions) != 1 || len(view.Suggestions[0].Evidence) != 1 {
		t.Fatalf("the suggestion comes back with its evidence: %+v", view)
	}
	reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"r1","decision":"reject"}`)
	for i := range 2 {
		if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"u2","decision":"undo"}`); rec.Code != http.StatusOK {
			t.Fatalf("undo of a rejection, try %d: %d %s", i, rec.Code, rec.Body)
		}
	}
}

func TestAConfirmWhoseTagWasRemovedCanBeReopened(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`)
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"o1","decision":"reopen"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"confirmed"`) {
		t.Fatalf("reopen while the tag is on the file: %d %s", rec.Code, rec.Body)
	}
	// Someone removes the tag by hand.
	untag, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "unmark-tag", "tagId": "tag_release"}}})
	if _, err := s.commitDocument(ctx, "MEETING1", "MEETING1.opus", annTestRecording, []string{"MEETING1.opus"}, "bob", mustWriteRequest(t, untag)); err != nil {
		t.Fatal(err)
	}
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"o2","decision":"reopen"}`); rec.Code != http.StatusOK {
		t.Fatalf("reopen after the tag went: %d %s", rec.Code, rec.Body)
	}
	if view := meetingSuggestions(t, h, "alice"); len(view.Suggestions) != 1 {
		t.Fatalf("reopened: %+v", view)
	}
}

func mustWriteRequest(t *testing.T, raw []byte) annotateWriteRequest {
	t.Helper()
	var request annotateWriteRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestAShareRemovedMeanwhileServesAndWritesNothing(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResult(t, testTag{"tag_hiring", "hiring"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = true
	s.rt.setSettings(settings)
	topics := newTopicsService(s.rt, s.exapp, nil)
	t.Cleanup(func() { topics.vectors.Close() })
	s.rt.topics = topics
	unlockTopicsPreview(t, topics)
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	nc.frontMu.Lock()
	delete(nc.visible, "MEETING1.opus")
	nc.frontMu.Unlock()
	if rec := reviewCall(h, http.MethodGet, "meetings/MEETING1", "alice", ""); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Release") {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"r1","decision":"reject"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	var n int
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_verdict`).Scan(&n)
	if n != 0 {
		t.Fatal("a verdict was written")
	}
}

// A topic needs no colour: one made without (or approved from a proposal,
// which takes none) is confirmed like any other and makes a plain tag.
func TestConfirmingATopicWithoutAColourMakesAPlainTag(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	topics.store.db.Exec(`UPDATE topic_library SET color=NULL WHERE tag_id='tag_release'`)
	rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"c1","decision":"confirm"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	doc, err := s.rt.annotationReads().document(context.Background(), "MEETING1.opus")
	if err != nil || !strings.Contains(string(doc.Annotations), `"tag_release"`) {
		t.Fatalf("document %s %v", doc.Annotations, err)
	}
}

// Someone tags the meeting by hand with a topic it was suggested: the
// suggestion turns into a direct confirmation, as at scoring, and leaves the
// review queue.
func TestTaggingByHandSettlesThePendingSuggestion(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	tag, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "mark", "tag": map[string]any{"label": "release planning"}, "target": map[string]any{"kind": "meeting"}}}})
	if _, err := s.commitDocument(ctx, "MEETING1", "MEETING1.opus", annTestRecording, []string{"MEETING1.opus"}, "bob", mustWriteRequest(t, tag)); err != nil {
		t.Fatal(err)
	}
	view := meetingSuggestions(t, h, "alice")
	if len(view.Suggestions) != 0 {
		t.Fatalf("still pending after tagging by hand: %+v", view.Suggestions)
	}
	var verdict, source string
	topics.store.db.QueryRow(`SELECT verdict,source FROM topic_verdict WHERE opus_name='MEETING1.opus' AND tag_id='tag_release'`).Scan(&verdict, &source)
	if verdict != "confirmed" || source != "direct" {
		t.Fatalf("verdict %q source %q", verdict, source)
	}
}

// Rejecting changes no file, so it does not wait for the meeting's
// annotations to be imported (the review queue lists meetings nobody opened).
func TestRejectingNeedsNoImportedAnnotations(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	db := s.rt.annotationReads().db
	for _, table := range []string{"annotation_head", "meeting_annotations"} {
		if _, err := db.Exec(`DELETE FROM ` + table + ` WHERE opus_name='MEETING1.opus'`); err != nil {
			t.Fatal(err)
		}
	}
	if rec := reviewCall(h, http.MethodPost, "meetings/MEETING1/tag_release", "alice", `{"requestId":"r1","decision":"reject"}`); rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
}
