package operator

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// annotations/meetings/<id>/speakers. MEETING1 is alice's to read and is the
// job MEETING1; SECRET exists and is someone else's.

type speakersFixture struct {
	rt *Runtime
	h  http.Handler
	nc *annotationsNextcloud
}

func newSpeakersFixture(t *testing.T) *speakersFixture {
	t.Helper()
	return newSpeakersFixtureBehind(t, nil)
}

// newSpeakersFixtureBehind puts front, when given, between the service and the
// fake Nextcloud: a way to make Nextcloud answer one request differently.
func newSpeakersFixtureBehind(t *testing.T, front func(next http.Handler) http.Handler) *speakersFixture {
	t.Helper()
	store, workRoot := openSpeakerTestStore(t)
	t.Setenv(envDiarizationModel, "")
	rt := &Runtime{store: store, logger: log.New(ioDiscard{}, "", 0)}
	rt.cfg.CassiniBin = "cassini"
	rt.cfg.WorkRoot = workRoot
	rt.cfg.ModelCacheRoot = filepath.Join(t.TempDir(), "cache")
	nc := newAnnotationsNextcloud(t, "MEETING1.opus")
	ncURL := nc.url
	if front != nil {
		backend, err := url.Parse(nc.url)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(front(httputil.NewSingleHostReverseProxy(backend)))
		t.Cleanup(server.Close)
		ncURL = server.URL
	}
	cfg := testExAppConfig(ncURL)
	s := newAnnotationService(rt, cfg, log.New(ioDiscard{}, "", 0))
	mux := http.NewServeMux()
	s.register(mux)
	return &speakersFixture{rt: rt, h: mux, nc: nc}
}

func (f *speakersFixture) installModel(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.rt.cfg.ModelCacheRoot, "models", "nemotron-3-diarization-int8", "model.int8.onnx")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("onnx"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *speakersFixture) get(t *testing.T, id string) speakerEditsResponse {
	t.Helper()
	rec := annTestCall(f.h, http.MethodGet, id+"/speakers", "alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/speakers = %d %s", id, rec.Code, rec.Body.String())
	}
	var resp speakerEditsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSpeakersGetReportsWhyAMeetingCannotBeSplit(t *testing.T) {
	f := newSpeakersFixture(t)

	// No job at all: a cron-era or imported recording.
	resp := f.get(t, "MEETING1")
	if resp.Available || resp.Reason != speakerReasonNoJob || resp.State != speakerStateUnavailable || len(resp.Participants) != 0 || resp.Doc.Format != speakerEditsFormat || string(resp.Report) != "null" {
		t.Fatalf("no job: %+v", resp)
	}

	// A job whose capture is gone.
	seedJobRow(t, f.rt.store.db, seededJobRow{ID: "MEETING1", Stage: "done", State: "succeeded", CreatedAt: "2026-10-01T10:00:00Z"})
	if resp = f.get(t, "MEETING1"); resp.Available || resp.Reason != speakerReasonNoSourceAudio {
		t.Fatalf("no capture: %+v", resp)
	}

	// The capture, but no transcript to split.
	bundle, err := PrepareRunBundle(canonicalRunPath(f.rt.cfg.WorkRoot, "MEETING1"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle.RecordingPath, []byte("mkv"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk"}); err != nil {
		t.Fatal(err)
	}
	setJobArtifactRunPath(t, f.rt.store.db, "MEETING1", bundle.RootDir)
	if resp = f.get(t, "MEETING1"); resp.Available || resp.Reason != speakerReasonNoTranscript {
		t.Fatalf("no transcript: %+v", resp)
	}

	// Everything but the model.
	writeSpeakerMeetingFixture(t, canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1"), bundle.RootDir)
	resp = f.get(t, "MEETING1")
	if resp.Available || resp.Reason != speakerReasonDiarizationUnav || len(resp.Participants) != 2 || resp.Participants[0].ID != speakerTestRoom || resp.Participants[0].Label != "Meeting room laptop" {
		t.Fatalf("no model: %+v", resp)
	}

	f.installModel(t)
	resp = f.get(t, "MEETING1")
	if !resp.Available || resp.Reason != "" || resp.State != speakerStateIdle || resp.Revision != 0 || resp.AppliedRevision != 0 {
		t.Fatalf("available: %+v", resp)
	}
}

// Once a split exists the participants are the ORIGINAL devices, read from the
// raw-ASR transcript the CLI keeps, never the voices in the primary one.
func TestSpeakersGetListsDevicesNotVoices(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	meeting := canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1")
	if err := os.Rename(filepath.Join(meeting, "transcript.words.v1.json"), filepath.Join(meeting, speakerTranscriptRawASR)); err != nil {
		t.Fatal(err)
	}
	split := `{"version":"transcript.words.v1","speakers":[{"id":"spk_room~1","label":"Ann"},{"id":"spk_room~2","label":"Bea"},{"id":"spk_remote","label":"Remote"}]}`
	if err := os.WriteFile(filepath.Join(meeting, "transcript.words.v1.json"), []byte(split), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := f.get(t, "MEETING1")
	if len(resp.Participants) != 2 || resp.Participants[0].ID != speakerTestRoom || resp.Participants[1].ID != speakerTestRemote {
		t.Fatalf("participants = %+v, want the two devices", resp.Participants)
	}
}

func TestSpeakersPostQueuesARefine(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")

	body := `{"expectRevision":0,"doc":{"format":"ignored","revision":99,"splits":[{"speakerId":"spk_room"}],"merges":[],"labels":[{"speakerId":"spk_room~1","label":" Ann "}]}}`
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	var resp speakerEditsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.State != speakerStateApplying || resp.Revision != 1 || resp.AppliedRevision != 0 || resp.Doc.Revision != 1 || resp.Doc.Format != speakerEditsFormat || resp.Doc.Labels[0].Label != "Ann" {
		t.Fatalf("POST answered %+v", resp)
	}
	kind, snapshot, err := f.rt.store.AttemptSpeakerEdits(context.Background(), "MEETING1", 2)
	if err != nil || kind != triggerKindRefine || snapshot == nil || snapshot.Revision != 1 {
		t.Fatalf("queued attempt = %q %+v %v", kind, snapshot, err)
	}
	var updatedBy string
	if err := f.rt.store.db.QueryRow(`SELECT updated_by FROM speaker_edits WHERE job_id = 'MEETING1'`).Scan(&updatedBy); err != nil || updatedBy != "alice" {
		t.Fatalf("updated_by = %q %v, want the caller", updatedBy, err)
	}
	if got := f.get(t, "MEETING1"); got.State != speakerStateApplying {
		t.Fatalf("GET while queued = %+v", got)
	}

	// The refine is queued, so the job is busy until it finishes.
	rec = annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":1,"doc":{"splits":[]}}`)
	if rec.Code != http.StatusConflict || annTestError(t, rec) != "busy" {
		t.Fatalf("POST while busy = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'succeeded' WHERE id = 'MEETING1'`); err != nil {
		t.Fatal(err)
	}
	rec = annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"splits":[]}}`)
	if rec.Code != http.StatusConflict || annTestError(t, rec) != "revision-conflict" || !strings.Contains(rec.Body.String(), `"revision":1`) {
		t.Fatalf("stale POST = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSpeakersPostRefusesWhatCannotBeApplied(t *testing.T) {
	f := newSpeakersFixture(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	cases := []struct{ name, body string }{
		{"not JSON", `nope`},
		{"no revision", `{"doc":{"splits":[]}}`},
		{"no doc", `{"expectRevision":0}`},
		{"unknown field", `{"expectRevision":0,"doc":{"splits":[]},"actor":"mallory"}`},
		{"split a voice", `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room~1"}]}}`},
		{"split a stranger", `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_nobody"}]}}`},
		{"merge across devices", `{"expectRevision":0,"doc":{"merges":[{"from":"spk_room~1","into":"spk_remote~1"}]}}`},
		{"long label", `{"expectRevision":0,"doc":{"labels":[{"speakerId":"spk_room~1","label":"` + strings.Repeat("x", 65) + `"}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", tc.body)
			if rec.Code != http.StatusBadRequest || annTestError(t, rec) != "invalid" {
				t.Fatalf("POST = %d %s, want 400 invalid", rec.Code, rec.Body.String())
			}
		})
	}
	if rec, _ := f.rt.store.GetSpeakerEdits(context.Background(), "MEETING1"); rec.Revision != 0 {
		t.Fatalf("a refused POST stored revision %d", rec.Revision)
	}

	// No model: a new split cannot run, but naming needs no diarizer.
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`)
	if rec.Code != http.StatusServiceUnavailable || annTestError(t, rec) != "diarization-unavailable" {
		t.Fatalf("split without a model = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.rt.store.PutSpeakerSplitTurns(context.Background(), "MEETING1", speakerTestRoom, `{}`, "", "", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rec = annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~1","label":"Ann"}]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("split with stored turns and no model = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSpeakersAreInvisibleOutsideTheCallersMeetings(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "SECRET")
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "NEVER")
	for _, id := range []string{"SECRET", "NEVER"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			body := ""
			if method == http.MethodPost {
				body = `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`
			}
			if rec := annTestCall(f.h, method, id+"/speakers", "alice", body); rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s/speakers = %d %s, want 404", method, id, rec.Code, rec.Body.String())
			}
		}
	}
	if rec, _ := f.rt.store.GetSpeakerEdits(context.Background(), "SECRET"); rec.Revision != 0 {
		t.Fatal("a caller who cannot read SECRET edited its speakers")
	}
	// A visible meeting with no job cannot be split either.
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"splits":[]}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST to a meeting with no job = %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// The share list can still name a share Nextcloud has stopped honouring. A
// mark write checks the leaf as the caller first; so must speakers, whose POST
// republishes the recording for every reader.
func TestSpeakersRefuseACallerNextcloudNoLongerLetsRead(t *testing.T) {
	f := newSpeakersFixtureBehind(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/remote.php/dav/files/alice/") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		body := ""
		if method == http.MethodPost {
			body = `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`
		}
		if rec := annTestCall(f.h, method, "MEETING1/speakers", "alice", body); rec.Code != http.StatusNotFound {
			t.Fatalf("%s with a refused share = %d %s, want 404", method, rec.Code, rec.Body.String())
		}
	}
	if rec, _ := f.rt.store.GetSpeakerEdits(context.Background(), "MEETING1"); rec.Revision != 0 {
		t.Fatalf("a caller Nextcloud refuses stored revision %d", rec.Revision)
	}
	if job := mustGetJob(t, f.rt.store, "MEETING1"); job.CurrentAttemptNumber != 1 {
		t.Fatalf("a caller Nextcloud refuses queued attempt %d", job.CurrentAttemptNumber)
	}
}

func TestSpeakersRoutes(t *testing.T) {
	f := newSpeakersFixture(t)
	if rec := annTestCall(f.h, http.MethodPut, "MEETING1/speakers", "alice", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d", rec.Code)
	}
	if rec := annTestCall(f.h, http.MethodGet, "MEETING1/speakers", "", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("no caller = %d, want the outage answer", rec.Code)
	}
	if rec := annTestCall(f.h, http.MethodGet, "MEETING1/speakers/", "alice", ""); rec.Code != http.StatusOK {
		t.Fatalf("trailing slash = %d", rec.Code)
	}
	if rec := annTestCall(f.h, http.MethodGet, "a/b/speakers", "alice", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("nested id = %d", rec.Code)
	}
	if got := annTestCall(f.h, http.MethodGet, "MEETING1/speakers", "alice", "").Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
