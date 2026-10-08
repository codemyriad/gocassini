package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	rt.cfg.CassiniBin = fakeModelsCLI(t)
	writeDiarizerInventory(t, rt.cfg.CassiniBin, false, false, true)
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

// installModel makes the model inventory list the diarizer ready, as after
// an installation from Settings. The inventory cache is dropped the way a
// finished model job drops it.
func (f *speakersFixture) installModel(t *testing.T) {
	t.Helper()
	writeDiarizerInventory(t, f.rt.cfg.CassiniBin, true, true, true)
	f.rt.invalidateModelInventory()
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
	// It tells an administrator how to install it.
	if !strings.Contains(resp.ReasonDetail, "Settings") || !strings.Contains(resp.ReasonDetail, "cassini models install "+defaultDiarizationModel) {
		t.Fatalf("no model: reasonDetail = %q", resp.ReasonDetail)
	}

	f.installModel(t)
	resp = f.get(t, "MEETING1")
	if !resp.Available || resp.Reason != "" || resp.ReasonDetail != "" || resp.State != speakerStateIdle || resp.Revision != 0 || resp.AppliedRevision != 0 {
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
	if rec.Code != http.StatusServiceUnavailable || annTestError(t, rec) != "diarization-unavailable" || !strings.Contains(rec.Body.String(), "cassini models install "+defaultDiarizationModel) {
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

// --- progress: where an applying edit is and how long it should take ---

var speakerProgressT0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// fakeSpeakerClock fixes the speakers surface's now; advance moves it.
type fakeSpeakerClock struct{ now time.Time }

func (c *fakeSpeakerClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func (f *speakersFixture) useClock(at time.Time) *fakeSpeakerClock {
	clock := &fakeSpeakerClock{now: at}
	f.rt.speakerClock = func() time.Time { return clock.now }
	return clock
}

// setSpeakerMeetingAudioMs gives the job's meeting bundle a manifest that
// says how long the meeting is, the way `cassini build` writes it.
func setSpeakerMeetingAudioMs(t *testing.T, workRoot, jobID string, ms int64) {
	t.Helper()
	manifest := fmt.Sprintf(`{"version":"cassini.meeting-artifact.v1","source":{"basename":"recording.mkv","durationMs":%d}}`, ms)
	if err := os.WriteFile(filepath.Join(canonicalMeetingPath(workRoot, jobID), "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setSpeakerAttemptStage(t *testing.T, f *speakersFixture, attempt int, stage, state string) {
	t.Helper()
	if _, err := f.rt.store.db.Exec(`UPDATE job_attempts SET stage = ?, state = ? WHERE job_id = 'MEETING1' AND attempt_number = ?`, stage, state, attempt); err != nil {
		t.Fatal(err)
	}
}

func putSpeakerTurnSet(t *testing.T, store *Store, jobID, speakerID string, elapsedMs, durationMs int64, storedAt time.Time) {
	t.Helper()
	turns := fmt.Sprintf(`{"format":"cassini.speaker-turns.v1","speakerId":%q,"durationMs":%d,"elapsedMs":%d,"turns":[]}`, speakerID, durationMs, elapsedMs)
	if _, err := store.PutSpeakerSplitTurns(context.Background(), jobID, speakerID, turns, "", "", formatUTCString(storedAt)); err != nil {
		t.Fatal(err)
	}
}

func (f *speakersFixture) post(t *testing.T, body string) speakerEditsResponse {
	t.Helper()
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	var resp speakerEditsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func wantSpeakerProgress(t *testing.T, step string, resp speakerEditsResponse, want speakerEditsProgress) {
	t.Helper()
	if resp.State != speakerStateApplying || resp.Progress == nil || *resp.Progress != want {
		t.Fatalf("%s: state %q progress %+v, want applying %+v", step, resp.State, resp.Progress, want)
	}
}

func TestSpeakersProgressIsNullUnlessApplying(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")

	rec := annTestCall(f.h, http.MethodGet, "MEETING1/speakers", "alice", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"progress":null`) {
		t.Fatalf("idle GET = %d %s, want progress null", rec.Code, rec.Body.String())
	}

	f.useClock(speakerProgressT0)
	if resp := f.post(t, `{"expectRevision":0,"doc":{"labels":[{"speakerId":"spk_room","label":"Room"}]}}`); resp.Progress == nil {
		t.Fatalf("POST = %+v, want progress", resp)
	}
	// It failed: nothing is applying any more.
	setSpeakerAttemptStage(t, f, 2, "build", "failed")
	if resp := f.get(t, "MEETING1"); resp.State != speakerStateFailed || resp.Progress != nil {
		t.Fatalf("failed: %+v", resp)
	}
	// A meeting nobody can split has no progress either.
	if resp, err := f.rt.speakerEditsState(context.Background(), "NOJOB"); err != nil || resp.State != speakerStateUnavailable || resp.Progress != nil {
		t.Fatalf("no job: %+v %v", resp, err)
	}
}

// One split, nothing learned yet: queued, then separating while the
// diarizer runs, then updating once the turns are stored, and gone once the
// recording carries the edits. The estimate is the default pace over the
// meeting's length plus the fixed tail, and does not move while it runs —
// not even when the attempt stores a turn set of its own.
func TestSpeakersProgressFollowsASplitThroughItsPhases(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	clock := f.useClock(speakerProgressT0)

	const estimate = 3_840_000*16/1000 + speakerRefineFixedMs // 61 440 + 15 000
	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 0, EstimatedMs: estimate})

	clock.advance(3 * time.Second)
	wantSpeakerProgress(t, "still queued", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 3000, EstimatedMs: estimate})

	setSpeakerAttemptStage(t, f, 2, "build", "running")
	clock.advance(40 * time.Second)
	wantSpeakerProgress(t, "separating", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseSeparating, ElapsedMs: 43_000, EstimatedMs: estimate})

	// The diarizer finishes, at a pace that would change a learned rate.
	clock.advance(20 * time.Second)
	putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 55_903, 3_850_492, clock.now)
	wantSpeakerProgress(t, "applying", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 63_000, EstimatedMs: estimate})

	setSpeakerAttemptStage(t, f, 2, "publish", "queued")
	clock.advance(10 * time.Second)
	wantSpeakerProgress(t, "publishing", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 73_000, EstimatedMs: estimate})

	// Running past the estimate is reported as it is.
	clock.advance(time.Minute)
	wantSpeakerProgress(t, "late", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 133_000, EstimatedMs: estimate})

	if _, err := f.rt.store.db.Exec(`UPDATE speaker_edits SET applied_revision = 1 WHERE job_id = 'MEETING1'`); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, "MEETING1"); resp.State != speakerStateIdle || resp.Progress != nil {
		t.Fatalf("published: %+v", resp)
	}
}

// The pace is the median of this operator's stored turn sets, of any meeting,
// plus decoding. Sets stored after the attempt was queued, and sets that say
// nothing about time, do not count.
func TestSpeakersProgressLearnsThePaceFromStoredTurnSets(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	before := speakerProgressT0.Add(-time.Hour)
	putSpeakerTurnSet(t, f.rt.store, "OTHER1", "spk_a", 1_000, 100_000, before)
	putSpeakerTurnSet(t, f.rt.store, "OTHER2", "spk_b", 2_000, 100_000, before)
	putSpeakerTurnSet(t, f.rt.store, "OTHER3", "spk_c", 4_000, 100_000, before)
	if _, err := f.rt.store.PutSpeakerSplitTurns(context.Background(), "OTHER4", "spk_d", `{}`, "", "", formatUTCString(before)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.store.PutSpeakerSplitTurns(context.Background(), "OTHER5", "spk_e", `not json`, "", "", formatUTCString(before)); err != nil {
		t.Fatal(err)
	}
	clock := f.useClock(speakerProgressT0)

	// median 0.02 × 1.15 = 0.023 per audio ms.
	const estimate = 3_840_000*23/1000 + speakerRefineFixedMs // 88 320 + 15 000
	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, EstimatedMs: estimate})

	// A very slow set stored while the attempt waits would move the median;
	// the attempt keeps the estimate it was queued with.
	clock.advance(time.Second)
	putSpeakerTurnSet(t, f.rt.store, "OTHER6", "spk_f", 9_000, 100_000, clock.now)
	putSpeakerTurnSet(t, f.rt.store, "OTHER7", "spk_g", 9_000, 100_000, clock.now)
	wantSpeakerProgress(t, "after other sets", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 1000, EstimatedMs: estimate})
}

// Naming, merging or undoing over turns that are already stored runs no
// diarizer: the estimate is the fixed tail and the attempt never separates.
func TestSpeakersProgressOfAnEditThatNeedsNoDiarizer(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 55_903, 3_850_492, speakerProgressT0.Add(-time.Hour))
	clock := f.useClock(speakerProgressT0)

	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~1","label":"Ann"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, EstimatedMs: speakerRefineFixedMs})
	setSpeakerAttemptStage(t, f, 2, "build", "running")
	clock.advance(400 * time.Millisecond)
	wantSpeakerProgress(t, "running", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 400, EstimatedMs: speakerRefineFixedMs})
}

func TestSpeakerDiarizeRateIsClampedAndDefaulted(t *testing.T) {
	at := speakerProgressT0.Add(-time.Hour)
	run := func(elapsed, duration int64) speakerDiarizationRun {
		return speakerDiarizationRun{ElapsedMs: elapsed, DurationMs: duration, StoredAt: formatUTCString(at)}
	}
	cases := []struct {
		name string
		runs []speakerDiarizationRun
		want float64
	}{
		{"none", nil, speakerDiarizeRateDefault},
		{"only after queue", []speakerDiarizationRun{{ElapsedMs: 1, DurationMs: 2, StoredAt: formatUTCString(speakerProgressT0)}}, speakerDiarizeRateDefault},
		{"too fast", []speakerDiarizationRun{run(1, 100_000)}, speakerDiarizeRateMin},
		{"too slow", []speakerDiarizationRun{run(100_000, 100_000)}, speakerDiarizeRateMax},
		{"even count", []speakerDiarizationRun{run(1_000, 100_000), run(4_000, 100_000), run(2_000, 100_000), run(9_000, 100_000)}, 0.03 * speakerDiarizeDecodeFactor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := learnedSpeakerDiarizeRate(tc.runs, speakerProgressT0, true); math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("rate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSpeakerRefineEstimateHasAFloor(t *testing.T) {
	if got := speakerRefineEstimateMs(0, 0, false, 0); got != speakerRefineMinMs {
		t.Fatalf("estimate with nothing to do = %d, want the %d ms floor", got, speakerRefineMinMs)
	}
	if got := speakerRefineEstimateMs(100_000, 0.016, true, 1_000); got != speakerRefineMinMs {
		t.Fatalf("short estimate = %d, want the floor", got)
	}
	if got := speakerRefineEstimateMs(1_000_000, 0.016, false, speakerRefineFixedMs); got != speakerRefineFixedMs {
		t.Fatalf("no diarization = %d, want the fixed tail", got)
	}
	// Unknown length: only the fixed tail is known.
	if got := speakerRefineEstimateMs(0, 0.016, true, speakerRefineFixedMs); got != speakerRefineFixedMs {
		t.Fatalf("unknown length = %d", got)
	}
}

// Without a manifest that says, the length comes from the transcript's media.
func TestSpeakerMeetingAudioMsFallsBackToTheTranscript(t *testing.T) {
	dir := t.TempDir()
	if got := readSpeakerMeetingAudioMs(dir); got != 0 {
		t.Fatalf("empty bundle = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"cassini.meeting-artifact.v1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, speakerTranscriptPrimary), []byte(`{"media":{"durationMs":60008}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSpeakerMeetingAudioMs(dir); got != 60008 {
		t.Fatalf("from transcript = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"source":{"durationMs":61000}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSpeakerMeetingAudioMs(dir); got != 61000 {
		t.Fatalf("from manifest = %d", got)
	}
}
