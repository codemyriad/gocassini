package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// Changing a tag (D-746), against the fake Nextcloud of the meetings tests:
// MEETING1 is alice's to read, SECRET is someone else's. Both carry hiring;
// MEETING1 also budget, SECRET also layoffs.

func seedTagChanges(t *testing.T) *annotationStore {
	t.Helper()
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "MEETING1.opus", annotatedFile(t, "c1", testTagNamespaceA,
		[]testTag{{"tag_hiring", "hiring"}, {"tag_budget", "budget"}},
		meetingMark("m1", "tag_hiring"), rangeMark("m2", "tag_budget", 0, 1000)))
	recordMarks(t, store, "SECRET.opus", annotatedFile(t, "c2", testTagNamespaceA,
		[]testTag{{"tag_hiring", "hiring"}, {"tag_layoffs", "layoffs"}},
		meetingMark("m1", "tag_hiring"), meetingMark("m2", "tag_layoffs")))
	return store
}

// tagChangeService mounts the annotation routes; store may be nil for "no index".
func tagChangeService(t *testing.T, ncURL, bin string, store *annotationStore) (*annotationService, http.Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rt := &Runtime{ctx: ctx}
	rt.cfg.CassiniBin = bin
	rt.cfg.DBPath = filepath.Join(t.TempDir(), "jobs.sqlite3")
	if store != nil {
		rt.annotations = store
	}
	cfg := testExAppConfig(ncURL)
	cfg.PublishSink = publishSinkNextcloudFiles
	s := newAnnotationService(rt, cfg, log.New(io.Discard, "", 0))
	mux := http.NewServeMux()
	s.start.Do(func() {}) // These tests drive DB changes; worker recovery has separate tests.
	s.register(mux)
	return s, mux
}

func tagCall(h http.Handler, method, path, caller, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/annotations/tags"+path, reader)
	if caller != "" {
		req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// tagChangeResult is the stand-in CLI's report: the file now carries tags, each
// on the whole meeting.
func tagChangeResult(t *testing.T, tags ...testTag) string {
	t.Helper()
	marks := make([]testMark, 0, len(tags))
	for i, tag := range tags {
		marks = append(marks, meetingMark(fmt.Sprintf("mk_%d", i), tag.id))
	}
	raw, err := json.Marshal(annotatedFile(t, "c-after", testTagNamespaceA, tags, marks...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func tagChangeResultWithStyle(t *testing.T, color, icon string, tags ...testTag) string {
	t.Helper()
	result := annotatedFile(t, "c-after", testTagNamespaceA, tags, meetingMark("mk_0", tags[0].id))
	var document map[string]any
	if err := json.Unmarshal(result.Annotations, &document); err != nil {
		t.Fatal(err)
	}
	entries := document["tags"].([]any)
	entries[0].(map[string]any)["color"] = color
	entries[0].(map[string]any)["icon"] = icon
	raw, err := json.Marshal(map[string]any{
		"format": result.Format, "annotations": document, "revision": result.Revision,
		"audioOpusSha256": result.AudioOpusSHA256, "containerSha256": result.ContainerSHA256,
		"durationMs": result.DurationMS, "resolved": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func waitTagJob(t *testing.T, s *annotationService, caller string) *tagJob {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		job := s.jobs.snapshot(caller)
		switch {
		case job == nil:
			t.Fatalf("%s has no job", caller)
		case job.State != tagJobRunning:
			return job
		case time.Now().After(deadline):
			t.Fatalf("%s's job did not finish", caller)
		}
	}
}

func decodeTagEdit(t *testing.T, rec *httptest.ResponseRecorder) (tagVocabularyEntry, *tagJob) {
	t.Helper()
	var body struct {
		Tag tagVocabularyEntry `json:"tag"`
		Job *tagJob            `json:"job"`
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return body.Tag, body.Job
}

func decodeStartedJob(t *testing.T, rec *httptest.ResponseRecorder) *tagJob {
	t.Helper()
	var body struct {
		Job *tagJob `json:"job"`
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Job == nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return body.Job
}

func TestTagRenameRewritesOnlyTheCallersRecordings(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResult(t, testTag{"tag_hiring", "Recruiting"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))

	rec := tagCall(h, http.MethodPost, "/tag_hiring", "alice", `{"label":" Recruiting ","color":"teal"}`)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a tag change must be uncacheable")
	}
	tag, started := decodeTagEdit(t, rec)
	if tag.Label != "Recruiting" || tag.Color != "teal" {
		t.Errorf("tag = %+v, want the new label and colour", tag)
	}
	if started == nil || started.Kind != tagJobRename || started.Actor != "alice" || started.Total != 1 {
		t.Fatalf("job = %+v, want a rename of alice's one recording", started)
	}
	job := waitTagJob(t, s, "alice")
	if job.State != tagJobFinished || job.Done != 1 || len(job.Failed) != 0 || job.FinishedAtUTC == "" {
		t.Fatalf("job = %+v, want finished with nothing failed", job)
	}
	if got := nc.recording(annTestSecret); got != "OPUS-secret" {
		t.Errorf("a recording alice cannot read was rewritten: %q", got)
	}
	doc, err := s.rt.annotationReads().document(context.Background(), "MEETING1.opus")
	if err != nil || !strings.Contains(string(doc.Annotations), "Recruiting") || doc.Sync.State != "pending" {
		t.Fatalf("rename: %+v %v", doc, err)
	}
	if nc.recording(annTestRecording) != "OPUS-original" {
		t.Fatal("API rewrote media")
	}
	if poll := tagCall(h, http.MethodGet, "/job", "alice", ""); !strings.Contains(poll.Body.String(), `"state":"finished"`) {
		t.Errorf("GET job = %s, want alice's finished job", poll.Body.String())
	}
}

func TestTagRestyleRewritesOnlyTheCallersRecordings(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResultWithStyle(t, "red", "flag", testTag{"tag_hiring", "hiring"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))

	tag, started := decodeTagEdit(t, tagCall(h, http.MethodPost, "/tag_hiring", "alice", `{"color":"red","icon":"flag"}`))
	if tag.Color != "red" || tag.Icon != "flag" {
		t.Fatalf("tag = %+v, want the requested archive appearance", tag)
	}
	if started == nil || started.Kind != tagJobRestyle || started.Total != 1 {
		t.Fatalf("job = %+v, want one scoped restyle job", started)
	}
	if job := waitTagJob(t, s, "alice"); job.State != tagJobFinished || len(job.Failed) != 0 {
		t.Fatalf("job = %+v", job)
	}
	if nc.recording(annTestSecret) != "OPUS-secret" {
		t.Error("a recording alice cannot read was rewritten")
	}
	vocabulary, err := s.rt.annotationReads().Vocabulary(context.Background(), []string{"MEETING1.opus"})
	updated, found := findTag(vocabulary, "tag_hiring")
	if err != nil || !found || updated.Color != "red" || updated.Icon != "flag" {
		t.Fatalf("vocabulary = %+v, %v; want persisted red/flag appearance", vocabulary, err)
	}
}

// A rename that failed on one recording leaves the vocabulary showing the new
// label, since most recordings carry it; running it again must still find the
// one it missed.
func TestTagRenameAgainFindsTheRecordingItMissed(t *testing.T) {
	store := newTestAnnotationStore(t)
	for i, label := range []string{"Recruiting", "Recruiting", "hiring"} {
		recordMarks(t, store, fmt.Sprintf("M%d.opus", i), annotatedFile(t, fmt.Sprintf("c%d", i), testTagNamespaceA,
			[]testTag{{"tag_hiring", label}}, meetingMark("m1", "tag_hiring")))
	}
	ctx := context.Background()
	all := []string{"M0.opus", "M1.opus", "M2.opus"}
	tags, err := store.Vocabulary(ctx, all)
	if err != nil || len(tags) != 1 || tags[0].Label != "Recruiting" {
		t.Fatalf("vocabulary = %+v, %v; want the majority label Recruiting", tags, err)
	}
	for _, tc := range []struct {
		visible []string
		want    bool
	}{
		{all, true},
		{all[:2], false},
	} {
		got, err := store.tagLabelledOtherwise(ctx, "tag_hiring", "Recruiting", tc.visible)
		if err != nil || got != tc.want {
			t.Errorf("tagLabelledOtherwise(%v) = %v, %v; want %v", tc.visible, got, err, tc.want)
		}
	}
}

func TestTagMergeRewritesOnlyTheCallersRecordings(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResult(t, testTag{"tag_hiring", "hiring"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))
	started := decodeStartedJob(t, tagCall(h, http.MethodPost, "/tag_budget/merge", "alice", `{"into":"tag_hiring"}`))
	if started.Kind != tagJobMerge || started.Into != "tag_hiring" || started.Total != 1 {
		t.Fatalf("job = %+v, want a merge into hiring over one recording", started)
	}
	if job := waitTagJob(t, s, "alice"); job.State != tagJobFinished || len(job.Failed) != 0 {
		t.Fatalf("job = %+v", job)
	}

	if nc.recording(annTestSecret) != "OPUS-secret" {
		t.Error("a recording alice cannot read was rewritten")
	}
}

func TestTagDeleteDoesNotRewriteAnotherCallersRecording(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResult(t, testTag{"tag_budget", "budget"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))
	// Delete takes no fields, so no body at all is fine.
	if started := decodeStartedJob(t, tagCall(h, http.MethodPost, "/tag_hiring/delete", "alice", "")); started.Total != 1 {
		t.Fatalf("job = %+v, want one recording", started)
	}
	if job := waitTagJob(t, s, "alice"); job.State != tagJobFinished || len(job.Failed) != 0 {
		t.Fatalf("job = %+v", job)
	}

	if nc.recording(annTestSecret) != "OPUS-secret" {
		t.Error("a recording alice cannot read was rewritten")
	}
	if body := tagCall(h, http.MethodGet, "", "alice", "").Body.String(); strings.Contains(body, "tag_hiring") {
		t.Errorf("hiring is still in alice's vocabulary: %s", body)
	}
}

func TestTagChangesAnswerAHiddenTagAsUnknown(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(tagChangeResult(t, testTag{"tag_budget", "Layoffs"})))
	s, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))

	unknown := tagCall(h, http.MethodPost, "/tag_nosuch", "alice", `{"color":"red"}`)
	for _, probe := range []struct{ path, body string }{
		{"/tag_layoffs", `{"color":"red"}`},
		{"/tag_layoffs/delete", `{}`},
		{"/tag_budget/merge", `{"into":"tag_layoffs"}`},
		{"/tag_budget/merge", `{"into":"tag_nosuch"}`},
	} {
		rec := tagCall(h, http.MethodPost, probe.path, "alice", probe.body)
		if rec.Code != http.StatusNotFound || rec.Body.String() != unknown.Body.String() {
			t.Errorf("%s %s: %d %q, want what an unknown tag gets (%d %q)", probe.path, probe.body, rec.Code, rec.Body.String(), unknown.Code, unknown.Body.String())
		}
	}
	if runs := annTestRuns(t, bin); runs != 0 {
		t.Fatalf("the CLI ran %d times for tags alice cannot see", runs)
	}

	// label-exists looks among alice's meetings only.
	rec := tagCall(h, http.MethodPost, "/tag_budget", "alice", `{"label":"HIRING"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"error":"label-exists"`) || !strings.Contains(rec.Body.String(), `"tagId":"tag_hiring"`) {
		t.Fatalf("code = %d body = %s, want 409 label-exists naming hiring", rec.Code, rec.Body.String())
	}
	if _, job := decodeTagEdit(t, tagCall(h, http.MethodPost, "/tag_budget", "alice", `{"label":"Layoffs"}`)); job == nil {
		t.Fatal("a label used only where alice cannot read must not block her rename")
	}
	waitTagJob(t, s, "alice")
}

func TestTagJobsAreOnePerCaller(t *testing.T) {
	var jobs tagJobs
	if !jobs.start(&tagJob{Actor: "alice", State: tagJobRunning}) {
		t.Fatal("first rejected")
	}
	if jobs.start(&tagJob{Actor: "alice", State: tagJobRunning}) {
		t.Fatal("overlap accepted")
	}
	if !jobs.start(&tagJob{Actor: "bob", State: tagJobRunning}) {
		t.Fatal("other caller rejected")
	}
}

func TestTagChangeFailuresAreNamedAndTheJobGoesOn(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus", "SECRET.opus")
	store := seedTagChanges(t)
	if err := store.MarkUnavailable(context.Background(), "MEETING1.opus", "broken"); err != nil {
		t.Fatal(err)
	}
	s, _ := tagChangeService(t, nc.url, "must-not-run", store)
	job := &tagJob{ID: "job_test", Actor: "alice", State: tagJobRunning, Kind: tagJobRename, TagID: "tag_hiring", Total: 2}
	s.jobs.start(job)
	s.runTagJob(job, []string{"MEETING1.opus", "SECRET.opus"}, json.RawMessage(`{"op":"relabel","tagId":"tag_hiring","label":"Recruiting"}`), map[string]string{"MEETING1.opus": "Daily Standup"})
	if job.Done != 2 || len(job.Failed) != 1 || job.Failed[0].Meeting != "Daily Standup" {
		t.Fatalf("job: %+v", job)
	}
}

func TestTagChangeRefusals(t *testing.T) {
	_, h := tagChangeService(t, untouchedUpstream(t).URL, fakeCassini(t, "exit 9"), newTestAnnotationStore(t))
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"an unknown member", http.MethodPost, "/tag_x", `{"colour":"red"}`, http.StatusBadRequest},
		{"a colour off the palette", http.MethodPost, "/tag_x", `{"color":"mauve"}`, http.StatusBadRequest},
		{"no colour at all", http.MethodPost, "/tag_x", `{"color":""}`, http.StatusBadRequest},
		{"an unknown icon", http.MethodPost, "/tag_x", `{"icon":"rocket"}`, http.StatusBadRequest},
		{"a blank label", http.MethodPost, "/tag_x", `{"label":"  "}`, http.StatusBadRequest},
		{"a long label", http.MethodPost, "/tag_x", `{"label":"` + strings.Repeat("x", 65) + `"}`, http.StatusBadRequest},
		{"delete takes nothing", http.MethodPost, "/tag_x/delete", `{"label":"x"}`, http.StatusBadRequest},
		{"merge takes only into", http.MethodPost, "/tag_x/merge", `{"into":"tag_y","label":"x"}`, http.StatusBadRequest},
		{"merge without into", http.MethodPost, "/tag_x/merge", "", http.StatusBadRequest},
		{"anything after the object", http.MethodPost, "/tag_x", `{"color":"red"} {}`, http.StatusBadRequest},
		{"a body too large", http.MethodPost, "/tag_x", `{"label":"` + strings.Repeat("x", 5000) + `"}`, http.StatusRequestEntityTooLarge},
		{"an unknown action", http.MethodPost, "/tag_x/rename", `{}`, http.StatusNotFound},
		{"too deep", http.MethodPost, "/tag_x/merge/more", `{}`, http.StatusNotFound},
		{"not a tag id", http.MethodPost, "/-x", `{}`, http.StatusNotFound},
		{"a tag is changed by POST", http.MethodGet, "/tag_x", "", http.StatusMethodNotAllowed},
		{"the collection is read-only", http.MethodPost, "", `{}`, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tagCall(h, tc.method, tc.path, "alice", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if rec.Code != http.StatusNotFound && rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("a tag answer must be uncacheable")
			}
		})
	}
	if rec := tagCall(h, http.MethodGet, "/job", "alice", ""); rec.Code != http.StatusOK || rec.Body.String() != `{"job":null}`+"\n" {
		t.Errorf("no job yet: %d %q, want {\"job\":null}", rec.Code, rec.Body.String())
	}
	if rec := tagCall(h, http.MethodPost, "/tag_x", "", `{}`); rec.Code != http.StatusBadGateway {
		t.Errorf("no caller: code = %d, want 502", rec.Code)
	}
	_, noIndex := tagChangeService(t, untouchedUpstream(t).URL, fakeCassini(t, "exit 9"), nil)
	if rec := tagCall(noIndex, http.MethodPost, "/tag_x", "alice", `{}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no index: code = %d, want 503", rec.Code)
	}
}

func TestAnnotationsMeetingPOSTAcceptsStylesOnlyForTagsItCreates(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "MEETING1.opus", annotateResult{Format: annotateResultFormat, AudioOpusSHA256: testAudioDigest, DurationMS: 60000})
	_, h := tagChangeService(t, nc.url, bin, store)
	mark := func(styles string) *httptest.ResponseRecorder {
		return annTestCall(h, http.MethodPost, "MEETING1", "alice",
			`{"ops":[{"op":"mark","tag":{"label":"Hiring"},"target":{"kind":"meeting"}}],"tagStyles":`+styles+`}`)
	}

	if rec := mark(`[{"label":" HIRING ","color":"blue","icon":"star"},{"label":"unused","color":"red","icon":""}]`); rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body.String())
	}
	// The tag exists now, so a second batch's creation style is ignored.
	if rec := mark(`[{"label":"hiring","color":"red","icon":""}]`); rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body.String())
	}

	runs := 0
	for _, bad := range []string{
		`[{"label":"hiring","color":"mauve","icon":""}]`,
		`[{"label":"hiring","color":"blue","icon":"rocket"}]`,
		`[{"label":" ","color":"blue","icon":""}]`,
	} {
		if rec := mark(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", bad, rec.Code)
		}
	}
	if runs != 0 {
		t.Error("a refused style still ran the CLI")
	}
}

// A mark naming a tag id none of alice's meetings carries is resolved by its
// label, as if it named none. Honouring the id would bring a hidden tag into her
// vocabulary with its colour, and so say that meetings she cannot read carry it.
func TestAnnotationsMeetingPOSTResolvesAHiddenTagIDByLabel(t *testing.T) {
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	bin := fakeCassini(t, annTestCLIPrints(annTestApplied))
	_, h := tagChangeService(t, nc.url, bin, seedTagChanges(t))

	rec := annTestCall(h, http.MethodPost, "MEETING1", "alice", `{"ops":[
		{"op":"mark","tag":{"id":"tag_layoffs","label":"Layoffs"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"id":"tag_layoffs","label":"HIRING"},"target":{"kind":"meeting"}},
		{"op":"mark","tag":{"id":"tag_budget","label":"Money"},"target":{"kind":"meeting"}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tag_layoffs") {
		t.Fatalf("hidden ID leaked: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tag_budget") {
		t.Fatal("visible tag ID lost")
	}

}

// GET annotations/tags lists each visible meeting with a resolved mark, by
// catalog id. Appearance comes from each indexed archive document.
func TestTagVocabularyListsEachVisibleMeetingsTags(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "JOB1.opus", annotatedFile(t, "c1", testTagNamespaceA, []testTag{{"tag_h", "hiring"}, {"tag_b", "budget"}},
		meetingMark("m1", "tag_h"), rangeMark("m2", "tag_h", 0, 1000), rangeMark("m3", "tag_h", 2000, 3000), rangeMark("m4", "tag_b", 0, 500)))
	other := annotatedFile(t, "c2", testTagNamespaceA, []testTag{{"tag_s", "layoffs"}}, meetingMark("m1", "tag_s"))
	recordMarks(t, store, "JOB2.opus", other)
	const want = "[{MeetingID:MEET-1 Tags:[{TagID:tag_b Whole:false Stretches:1} {TagID:tag_h Whole:true Stretches:2}]}]"
	tags := func(visible ...string) tagVocabularyResponse {
		srv := searchUpstream{catalog: searchTestCatalog, visible: visible}.server(t)
		t.Cleanup(srv.Close)
		s := tagService(srv.URL, store)
		return decodeTags(t, getTags(t, s, "alice"))
	}

	got := tags("JOB1.opus")
	if fmt.Sprintf("%+v", got.Meetings) != want {
		t.Errorf("meetings = %+v, want %s (JOB2 is hidden)", got.Meetings, want)
	}

	// Marks made against other audio say nothing about this meeting's time.
	unresolved := false
	other.Resolved = &unresolved
	recordMarks(t, store, "JOB2.opus", other)
	if got := tags("JOB1.opus", "JOB2.opus"); fmt.Sprintf("%+v", got.Meetings) != want {
		t.Errorf("meetings = %+v, want %s (JOB2's marks are unresolved)", got.Meetings, want)
	}
	if got := tags(); got.Meetings == nil || len(got.Meetings) != 0 {
		t.Errorf("meetings = %#v, want an empty array", got.Meetings)
	}
}
