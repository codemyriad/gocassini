package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ann "cassini-annotations"
	"cassini-operator/internal/operator/appapi"
)

// topicLibraryService is the annotation routes with topic suggestions on,
// over the fake Nextcloud of the tag tests: MEETING1 (hiring, budget) is the
// caller's, SECRET (hiring, layoffs) is not.
func topicLibraryService(t *testing.T, visible ...string) (*annotationService, http.Handler, *topicsService) {
	t.Helper()
	nc := newAnnotationsNextcloud(t, visible...)
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResult(t, testTag{"tag_hiring", "hiring"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = true
	s.rt.setSettings(settings)
	topics := newTopicsService(s.rt, s.exapp, log.New(io.Discard, "", 0))
	if topics == nil {
		t.Fatal("no topics service")
	}
	t.Cleanup(func() { topics.vectors.Close() })
	s.rt.topics = topics
	unlockTopicsPreview(t, topics)
	return s, h, topics
}

func topicCall(h http.Handler, method, path, caller, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/annotations/topics"+path, reader)
	req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeTopic(t *testing.T, rec *httptest.ResponseRecorder) topicView {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Topic topicView `json:"topic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Topic
}

func createTopicBody(id, label, definition string) string {
	return fmt.Sprintf(`{"requestId":%q,"label":%q,"definition":%q}`, id, label, definition)
}

func TestTopicCreateAdoptsOnlyVisibleTagsAndReplays(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	hiring := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "Hiring", "Recruiting new people.")))
	if hiring.TagID != "tag_hiring" || hiring.Revision != 1 || !hiring.Preparing {
		t.Fatalf("a visible tag's label adopts its id: %+v", hiring)
	}
	// layoffs exists only on a meeting alice cannot read: a new id.
	layoffs := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r2", "layoffs", "Letting people go.")))
	if layoffs.TagID == "tag_layoffs" || !strings.HasPrefix(layoffs.TagID, "tag_") {
		t.Fatalf("an id only hidden meetings carry is never adopted: %+v", layoffs)
	}
	again := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "Hiring", "Recruiting new people.")))
	if again.TagID != hiring.TagID {
		t.Fatal("a retried create replays its first answer")
	}
	if rec := topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "Other", "Something else.")); rec.Code != http.StatusConflict {
		t.Fatalf("a reused requestId with another body: %d", rec.Code)
	}
	if rec := topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r3", "hiring ", "Again.")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "label-taken") {
		t.Fatalf("labels are unique among active topics: %d %s", rec.Code, rec.Body)
	}
	if ids, _ := topics.store.rescoreQueue(context.Background()); len(ids) != 2 {
		t.Fatalf("each create queues a rescore: %v", ids)
	}
}

func TestTopicRetiredLabelOffersRestoreOrASeparateTopic(t *testing.T) {
	_, h, _ := topicLibraryService(t, "MEETING1.opus")
	hiring := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "hiring", "Recruiting.")))
	retired := decodeTopic(t, topicCall(h, http.MethodPost, "/library/"+hiring.TagID, "alice", `{"requestId":"r2","expectRevision":1,"status":"retired"}`))
	if retired.Status != topicStatusRetired || retired.Revision != 2 {
		t.Fatalf("retire: %+v", retired)
	}
	rec := topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r3", "hiring", "Recruiting."))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"topic-retired"`) || !strings.Contains(rec.Body.String(), hiring.TagID) {
		t.Fatalf("a retired topic's label: %d %s", rec.Code, rec.Body)
	}
	separate := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", `{"requestId":"r4","label":"hiring","definition":"Recruiting.","separate":true}`))
	if separate.TagID == hiring.TagID {
		t.Fatal("a separate topic mints a new id")
	}
	// Restoring the old one now collides with the new active label.
	if rec := topicCall(h, http.MethodPost, "/library/"+hiring.TagID, "alice", `{"requestId":"r5","expectRevision":2,"status":"active"}`); rec.Code != http.StatusConflict {
		t.Fatalf("restore over a taken label: %d %s", rec.Code, rec.Body)
	}
}

func TestTopicEditsNeedTheRevisionAndAreLimited(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	topic := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "budget", "Money.")))
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r2","expectRevision":7,"definition":"More money."}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "revision-conflict") {
		t.Fatalf("a stale revision: %d %s", rec.Code, rec.Body)
	}
	edited := decodeTopic(t, topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r3","expectRevision":1,"definition":"Budgets and spending."}`))
	if edited.Revision != 2 || edited.Definition != "Budgets and spending." {
		t.Fatalf("edit: %+v", edited)
	}
	// Twenty-eight more creates or definition edits fill alice's hour.
	for i := range 28 {
		if _, err := topics.store.db.Exec(`INSERT INTO topic_library_event(tag_id,revision,actor,at,change_json) VALUES('x',?,'alice',?,'{"kind":"definition"}')`, i, topics.store.unix()); err != nil {
			t.Fatal(err)
		}
	}
	rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r4","expectRevision":2,"definition":"Thirty-first."}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the 31st definition edit in an hour: %d %s", rec.Code, rec.Body)
	}
	// A colour change is not a definition edit.
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r5","expectRevision":2,"color":"teal"}`); rec.Code != http.StatusOK {
		t.Fatalf("a restyle: %d %s", rec.Code, rec.Body)
	}
	for i := range maxActiveTopics {
		topics.store.db.ExecContext(ctx, `INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,status,origin,revision,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,'d','s','active','manual',1,'bob',1,'bob',1)`,
			fmt.Sprintf("tag_cap%d", i), fmt.Sprintf("cap %d", i), fmt.Sprintf("cap %d", i))
	}
	if rec := topicCall(h, http.MethodPost, "/library", "bob", createTopicBody("r6", "one too many", "No room.")); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "library-full") {
		t.Fatalf("the 201st active topic: %d %s", rec.Code, rec.Body)
	}
}

func TestTopicLibraryNeedsAVisibleMeetingAndServesNoUserIDs(t *testing.T) {
	_, empty, _ := topicLibraryService(t)
	if rec := topicCall(empty, http.MethodGet, "", "carol", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a caller who can read no meeting: %d", rec.Code)
	}
	s, h, _ := topicLibraryService(t, "MEETING1.opus")
	decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "budget", "Money.")))
	rec := topicCall(h, http.MethodGet, "", "alice", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("library: %d %s", rec.Code, rec.Body)
	}
	var library struct {
		Enabled bool        `json:"enabled"`
		Library []topicView `json:"library"`
	}
	json.Unmarshal(rec.Body.Bytes(), &library)
	if !library.Enabled || len(library.Library) != 1 || library.Library[0].Usage.Meetings != 1 {
		t.Fatalf("budget is on alice's one meeting: %+v", library)
	}
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = false
	s.rt.setSettings(settings)
	if rec := topicCall(h, http.MethodGet, "", "alice", ""); rec.Code != http.StatusOK || rec.Body.String() != "{\"enabled\":false}\n" {
		t.Fatalf("off: %s", rec.Body)
	}
	if rec := topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r2", "x", "y")); rec.Code != http.StatusConflict {
		t.Fatalf("a write while off: %d", rec.Code)
	}
}

func TestTopicHistoryAndRedaction(t *testing.T) {
	s, h, _ := topicLibraryService(t, "MEETING1.opus")
	topic := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "budget", "Words pasted from a meeting.")))
	decodeTopic(t, topicCall(h, http.MethodPost, "/library/"+topic.TagID, "bob", `{"requestId":"r2","expectRevision":1,"definition":"Budgets and spending."}`))
	history := func(caller string) []topicRevisionView {
		rec := topicCall(h, http.MethodGet, "/library/"+topic.TagID+"/history", caller, "")
		var out struct {
			Revisions []topicRevisionView `json:"revisions"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("history: %d %s", rec.Code, rec.Body)
		}
		return out.Revisions
	}
	s.isAdminFn = func(context.Context, string) bool { return false }
	if h := history("carol"); len(h) != 2 || h[0].CanRedact || h[1].CanRedact {
		t.Fatalf("carol wrote neither: %+v", h)
	}
	if h := history("alice"); !h[0].CanRedact || h[1].CanRedact {
		t.Fatalf("alice may redact her old revision, never the current one: %+v", h)
	}
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID+"/history/1", "carol", `{"requestId":"x1","action":"redact"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("carol redacting: %d", rec.Code)
	}
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID+"/history/2", "bob", `{"requestId":"x2","action":"redact"}`); rec.Code != http.StatusConflict {
		t.Fatalf("the current revision: %d", rec.Code)
	}
	s.isAdminFn = func(_ context.Context, caller string) bool { return caller == "carol" }
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID+"/history/1", "carol", `{"requestId":"x3","action":"redact"}`); rec.Code != http.StatusOK {
		t.Fatalf("an admin redacting: %d %s", rec.Code, rec.Body)
	}
	got := history("alice")
	body, _ := json.Marshal(got)
	if !got[0].Redacted || got[0].Label != nil || got[0].Definition != nil || strings.Contains(string(body), "pasted") {
		t.Fatalf("a redacted revision serves no text: %s", body)
	}
}

func TestTypedLibraryLabelKeepsTheLibraryID(t *testing.T) {
	s, h, _ := topicLibraryService(t, "MEETING1.opus")
	topic := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "Quarterly review", "Quarter results.")))
	doc, _, err := s.resolveVocabulary(context.Background(), []json.RawMessage{json.RawMessage(`{"op":"mark","tag":{"label":"quarterly review"},"target":{"kind":"meeting"}}`)}, []string{"MEETING1.opus"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), topic.TagID) {
		t.Fatalf("a typed library label must use the library id: %s", doc)
	}
	// An explicit library id is honoured though no visible file carries it.
	doc, _, err = s.resolveVocabulary(context.Background(), []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"op":"mark","tag":{"id":%q,"label":"Quarterly review"},"target":{"kind":"meeting"}}`, topic.TagID))}, []string{"MEETING1.opus"})
	if err != nil || !strings.Contains(string(doc), topic.TagID) {
		t.Fatalf("explicit id: %s %v", doc, err)
	}
}

func TestTagManagerChangesFollowTheLibrary(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	hiring := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "hiring", "Recruiting.")))
	rec := tagCall(h, http.MethodPost, "/tag_hiring", "alice", `{"label":"Recruiting"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	waitTagJob(t, s, "alice")
	row, _, _ := topicByID(ctx, topics.store.db, hiring.TagID)
	if row.Label != "Recruiting" || row.Revision != 2 {
		t.Fatalf("the library follows a rename: %+v", row)
	}
	// Merging budget (a topic) into hiring.
	budget := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r2", "budget", "Money.")))
	topics.store.db.Exec(`INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at) VALUES('MEETING1.opus',?,'rejected','review','alice',1)`, budget.TagID)
	topics.store.db.Exec(`INSERT INTO topic_suggestion(opus_name,tag_id,state,rule,support_words,definition_sha,scorer_key,created_at,updated_at) VALUES('OTHER.opus',?,'pending','short-strong',40,'d','s',1,1)`, budget.TagID)
	if err := topics.libraryFollowMerge(ctx, budget.TagID, hiring.TagID, "Recruiting", "alice"); err != nil {
		t.Fatal(err)
	}
	from, _, _ := topicByID(ctx, topics.store.db, budget.TagID)
	if from.Status != topicStatusRetired || from.MergedInto == nil || *from.MergedInto != hiring.TagID {
		t.Fatalf("merged topic: %+v", from)
	}
	var state, verdict, source string
	topics.store.db.QueryRow(`SELECT state FROM topic_suggestion WHERE tag_id=?`, budget.TagID).Scan(&state)
	topics.store.db.QueryRow(`SELECT verdict,source FROM topic_verdict WHERE tag_id=? AND opus_name='MEETING1.opus'`, hiring.TagID).Scan(&verdict, &source)
	if state != "withdrawn" || verdict != "rejected" || source != "merge" {
		t.Fatalf("pending withdrawn (%s), rejection copied (%s) and marked as copied (%s)", state, verdict, source)
	}
	// Merging into an undescribed tag gives it the definition.
	other := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r3", "travel", "Trips.")))
	if err := topics.libraryFollowMerge(ctx, other.TagID, "tag_plain", "plain", "alice"); err != nil {
		t.Fatal(err)
	}
	plain, exists, _ := topicByID(ctx, topics.store.db, "tag_plain")
	if !exists || plain.Definition != "Trips." || plain.Status != topicStatusActive {
		t.Fatalf("into takes the definition: %+v %v", plain, exists)
	}
}

func TestTopicRescoreWritesOneTopicAndDropsStaleVectors(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	// A topic whose definition points the same way as every passage.
	if _, err := s.store.db.Exec(`INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,status,origin,revision,created_by,created_at,updated_by,updated_at) VALUES('tag_x','X','x','Exes.',?,'active','manual',1,'a',1,'a',1)`, topicDefinitionSHA("Exes.")); err != nil {
		t.Fatal(err)
	}
	versions := topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	if err := s.store.putTextVector(ctx, topicTextDefinition, topicDefinitionSHA("Exes."), versions, []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	s.queueRescore(ctx, "tag_x")
	// First with a stale key: the worker holds newer passages, so nothing.
	s.store.db.Exec(`UPDATE topic_meeting SET embed_key='newer' WHERE opus_name=?`, name)
	if err := s.rescorePending(ctx, versions); err != nil {
		t.Fatal(err)
	}
	var n int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_suggestion`).Scan(&n)
	if n != 0 {
		t.Fatal("a rescore over stale vectors must not write")
	}
	set, _, _ := s.vectors.meeting(ctx, name)
	s.store.db.Exec(`UPDATE topic_meeting SET embed_key=? WHERE opus_name=?`, set.EmbedKey, name)
	s.queueRescore(ctx, "tag_x")
	if err := s.rescorePending(ctx, versions); err != nil {
		t.Fatal(err)
	}
	var state string
	var words int
	if err := s.store.db.QueryRow(`SELECT state,support_words FROM topic_suggestion WHERE opus_name=? AND tag_id='tag_x'`, name).Scan(&state, &words); err != nil || state != "pending" || words != 120 {
		t.Fatalf("rescored: %s %d %v", state, words, err)
	}
	if ids, _ := s.store.rescoreQueue(ctx); len(ids) != 0 {
		t.Fatalf("the queue empties: %v", ids)
	}
	var _ sql.NullString
}

func TestTopicRestoreCountsTowardTheHourlyLimit(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	topic := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "budget", "Money.")))
	for i := range maxTopicEditsPerHour - 1 {
		topics.store.db.Exec(`INSERT INTO topic_library_event(tag_id,revision,actor,at,change_json) VALUES('x',?,'alice',?,'{"kind":"restore"}')`, i, topics.store.unix())
	}
	decodeTopic(t, topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r2","expectRevision":1,"status":"retired"}`))
	rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r3","expectRevision":2,"status":"active"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a restore rescores the archive and is limited: %d %s", rec.Code, rec.Body)
	}
	// Redacting old events does not give the hour back.
	topics.store.db.Exec(`UPDATE topic_library_event SET change_json='{"kind":"redacted"}',redacted_at=1 WHERE actor='alice' AND tag_id='x'`)
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r4","expectRevision":2,"status":"active"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after redaction: %d", rec.Code)
	}
}

func TestTopicRenameRefusesAVisibleTagsLabelAndABusyCaller(t *testing.T) {
	s, h, _ := topicLibraryService(t, "MEETING1.opus")
	hiring := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "hiring", "Recruiting.")))
	rec := topicCall(h, http.MethodPost, "/library/"+hiring.TagID, "alice", `{"requestId":"r2","expectRevision":1,"label":"Budget"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"label-exists"`) || !strings.Contains(rec.Body.String(), "tag_budget") {
		t.Fatalf("budget is another tag on alice's meeting: %d %s", rec.Code, rec.Body)
	}
	s.jobs.start(&tagJob{ID: "j0", Actor: "alice", State: tagJobRunning})
	rec = topicCall(h, http.MethodPost, "/library/"+hiring.TagID, "alice", `{"requestId":"r3","expectRevision":1,"label":"Recruiting"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "busy") {
		t.Fatalf("a rename while alice's job runs: %d %s", rec.Code, rec.Body)
	}
	s.jobs.finish(s.jobs.last["alice"], tagJobFinished)
	rec = topicCall(h, http.MethodPost, "/library/"+hiring.TagID, "alice", `{"requestId":"r4","expectRevision":1,"label":"Recruiting"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "alice") || !strings.Contains(rec.Body.String(), `"job":{`) {
		t.Fatalf("the rename starts a job that names no user: %d %s", rec.Code, rec.Body)
	}
	waitTagJob(t, s, "alice")
}

func TestTopicRedactionScrubsCopiesAndReceiptsHoldNoText(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	s.isAdminFn = func(context.Context, string) bool { return false }
	topic := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "budget", "PASTED SECRET")))
	// bob recolours (revision 2 copies the definition), then alice fixes it.
	decodeTopic(t, topicCall(h, http.MethodPost, "/library/"+topic.TagID, "bob", `{"requestId":"b1","expectRevision":1,"color":"teal"}`))
	decodeTopic(t, topicCall(h, http.MethodPost, "/library/"+topic.TagID, "alice", `{"requestId":"r2","expectRevision":2,"definition":"Budgets."}`))
	if rec := topicCall(h, http.MethodPost, "/library/"+topic.TagID+"/history/1", "alice", `{"requestId":"x1","action":"redact"}`); rec.Code != http.StatusOK {
		t.Fatalf("redact: %d %s", rec.Code, rec.Body)
	}
	history := topicCall(h, http.MethodGet, "/library/"+topic.TagID+"/history", "carol", "")
	if strings.Contains(history.Body.String(), "PASTED SECRET") {
		t.Fatalf("a later revision still serves the redacted text: %s", history.Body)
	}
	replay := topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "budget", "PASTED SECRET"))
	if replay.Code != http.StatusOK || strings.Contains(replay.Body.String(), "PASTED SECRET") {
		t.Fatalf("a replayed create serves the redacted text: %d %s", replay.Code, replay.Body)
	}
	var n int
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_receipt WHERE response LIKE '%SECRET%'`).Scan(&n)
	if n != 0 {
		t.Fatal("receipts keep topic text")
	}
}

func TestTopicLibraryIsInertWhileOff(t *testing.T) {
	s, h, _ := topicLibraryService(t, "MEETING1.opus")
	topic := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "Quarterly review", "Quarter results.")))
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = false
	s.rt.setSettings(settings)
	doc, _, err := s.resolveVocabulary(context.Background(), []json.RawMessage{json.RawMessage(`{"op":"mark","tag":{"label":"quarterly review"},"target":{"kind":"meeting"}}`)}, []string{"MEETING1.opus"})
	if err != nil || strings.Contains(string(doc), topic.TagID) {
		t.Fatalf("while off, a typed label does not resolve to the library: %s %v", doc, err)
	}
	for _, path := range []string{"/library/" + topic.TagID + "/history", "/library/" + topic.TagID + "/rooms"} {
		if rec := topicCall(h, http.MethodGet, path, "alice", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
			t.Fatalf("GET %s while off: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestLibraryAppearanceKeepsATagsColourWhenTheTopicHasNone(t *testing.T) {
	s, h, _ := topicLibraryService(t, "MEETING1.opus")
	decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", `{"requestId":"r1","label":"budget","definition":"Money.","fromTagId":"tag_budget"}`))
	red := "red"
	batch := ann.Batch{InitialTags: map[string]ann.AnnotationTag{"tag_budget": {ID: "tag_budget", Label: "budget", Color: &red}}}
	if err := s.libraryTagAppearance(context.Background(), &batch); err != nil {
		t.Fatal(err)
	}
	if got := batch.InitialTags["tag_budget"]; got.Color == nil || *got.Color != "red" {
		t.Fatalf("appearance %+v", got)
	}
}

func TestTopicMergeRespectsTheActiveCap(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	ctx := context.Background()
	source := decodeTopic(t, topicCall(h, http.MethodPost, "/library", "alice", createTopicBody("r1", "travel", "Trips.")))
	for i := range maxActiveTopics {
		topics.store.db.ExecContext(ctx, `INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,status,origin,revision,created_by,created_at,updated_by,updated_at) VALUES(?,?,?,'d','s','active','manual',1,'bob',1,'bob',1)`,
			fmt.Sprintf("tag_cap%d", i), fmt.Sprintf("cap %d", i), fmt.Sprintf("cap %d", i))
	}
	if err := topics.libraryFollowMerge(ctx, source.TagID, "tag_plain", "plain", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := topicByID(ctx, topics.store.db, "tag_plain"); exists {
		t.Fatal("a merge must not describe a tag past the cap")
	}
}

func TestTopicRescoreStopsWhenItsTopicChangesAndKeepsANewRequest(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	s.store.db.Exec(`INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,status,origin,revision,created_by,created_at,updated_by,updated_at) VALUES('tag_x','X','x','Exes.',?,'active','manual',1,'a',1,'a',1)`, topicDefinitionSHA("Exes."))
	versions := topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	s.store.putTextVector(ctx, topicTextDefinition, topicDefinitionSHA("Exes."), versions, []float32{1, 0, 0, 0})
	inputs, _, _ := s.store.scoringInputs(ctx, versions)
	// Retired after the pass read its inputs: nothing is written.
	s.store.db.Exec(`UPDATE topic_library SET status='retired' WHERE tag_id='tag_x'`)
	if _, err := s.rescoreTopic(ctx, inputs.Topics[0], inputs); err != nil {
		t.Fatal(err)
	}
	var n int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_suggestion WHERE opus_name=?`, name).Scan(&n)
	if n != 0 {
		t.Fatal("a stale pass wrote suggestions for a retired topic")
	}
	// A request during a pass survives the pass's completion.
	s.queueRescore(ctx, "tag_x")
	queue, _ := s.store.rescoreQueue(ctx)
	seq := queue["tag_x"]
	s.queueRescore(ctx, "tag_x")
	if err := s.store.doneRescore(ctx, "tag_x", seq); err != nil {
		t.Fatal(err)
	}
	if queue, _ := s.store.rescoreQueue(ctx); queue["tag_x"] == 0 {
		t.Fatal("the newer request was dropped")
	}
}

func TestApprovingAProposalAdoptsAVisibleTagOfThatName(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	db := topics.store.db
	db.Exec(`INSERT INTO topic_proposal(id,room_id,state,centroid,model_revision,recipe_version,chunker_version,draft_name,hints_json,flags_json,meeting_count,day_count,passage_count,created_at,updated_at)
 VALUES('tp1','r1','open',x'00','m','r1','natural-1','spend / quarter','["spend"]','{}',3,3,3,1,1)`)
	db.Exec(`INSERT INTO topic_proposal_member VALUES('tp1','MEETING1.opus',0,500,1)`)
	rec := reviewCall(h, http.MethodPost, "proposals/tp1", "alice", `{"requestId":"a1","action":"approve","label":"Budget","definition":"Money and how it is planned."}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tag_budget"`) {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
}
