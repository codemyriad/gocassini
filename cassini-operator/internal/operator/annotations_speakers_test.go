package operator

import (
	"context"
	"encoding/json"
	"errors"
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

// startSpeakerAttempt claims a queued attempt the way the build worker does:
// running, with build_started_at at.
func startSpeakerAttempt(t *testing.T, f *speakersFixture, attempt int, at time.Time) {
	t.Helper()
	if _, err := f.rt.store.db.Exec(`UPDATE job_attempts SET stage = 'build', state = 'running', build_started_at = ? WHERE job_id = 'MEETING1' AND attempt_number = ?`, formatUTCString(at), attempt); err != nil {
		t.Fatal(err)
	}
}

// deferSpeakerAttempt sends a running attempt back to the queue the way a
// resource deferral does: build_queued_at kept, build_started_at cleared.
func deferSpeakerAttempt(t *testing.T, f *speakersFixture, attempt int) {
	t.Helper()
	if _, err := f.rt.store.db.Exec(`UPDATE job_attempts SET stage = 'build', state = 'queued', build_started_at = NULL WHERE job_id = 'MEETING1' AND attempt_number = ?`, attempt); err != nil {
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
// meeting's length plus the fixed part, and does not move while it runs —
// not even when the attempt stores a turn set of its own. Once the attempt
// starts, elapsed counts from its start: the 3 s it waited are not work.
func TestSpeakersProgressFollowsASplitThroughItsPhases(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	clock := f.useClock(speakerProgressT0)

	const estimate = 3_840_000*16/1000 + 3_000 + 3_840_000*3/1000 // 61 440 diarizing + 14 520 fixed
	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 0, EstimatedMs: estimate})

	clock.advance(3 * time.Second)
	wantSpeakerProgress(t, "still queued", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 3000, EstimatedMs: estimate})

	startSpeakerAttempt(t, f, 2, clock.now)
	clock.advance(40 * time.Second)
	wantSpeakerProgress(t, "separating", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseSeparating, ElapsedMs: 40_000, EstimatedMs: estimate})

	// The diarizer finishes, at a pace that would change a learned rate.
	clock.advance(20 * time.Second)
	putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 55_903, 3_850_492, clock.now)
	wantSpeakerProgress(t, "applying", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 60_000, EstimatedMs: estimate})

	// Seal and publish keep the build's start.
	setSpeakerAttemptStage(t, f, 2, "publish", "queued")
	clock.advance(10 * time.Second)
	wantSpeakerProgress(t, "publishing", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 70_000, EstimatedMs: estimate})

	// Running past the estimate is reported as it is.
	clock.advance(time.Minute)
	wantSpeakerProgress(t, "late", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 130_000, EstimatedMs: estimate})

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
	const estimate = 3_840_000*23/1000 + 14_520 // 88 320 diarizing + the fixed part
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
// diarizer: the estimate is the fixed part alone, which grows with the
// meeting — 14.5 s for 64 minutes, 3.5 s for 3 — and the attempt never
// separates.
func TestSpeakersProgressOfAnEditThatNeedsNoDiarizer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		audioMs  int64
		estimate int64
	}{
		{"64 minutes", 3_840_000, 14_520},
		{"3 minutes", 180_000, 3_540},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpeakersFixture(t)
			f.installModel(t)
			seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
			setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", tc.audioMs)
			putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 55_903, 3_850_492, speakerProgressT0.Add(-time.Hour))
			clock := f.useClock(speakerProgressT0)

			resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~1","label":"Ann"}]}}`)
			wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, EstimatedMs: tc.estimate})
			startSpeakerAttempt(t, f, 2, clock.now)
			clock.advance(400 * time.Millisecond)
			wantSpeakerProgress(t, "running", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 400, EstimatedMs: tc.estimate})
		})
	}
}

// Each split that has no turns yet is diarized over its own full-length
// track, one after another: two new splits take twice the diarizing; a split
// whose turns are already stored adds nothing.
func TestSpeakersProgressCountsEverySplitToDiarize(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	clock := f.useClock(speakerProgressT0)

	const estimate = 2*3_840_000*16/1000 + 14_520 // 2 × 61 440 diarizing + the fixed part
	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"},{"speakerId":"spk_remote"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, EstimatedMs: estimate})

	// The first split's turns land: the attempt still separates the second,
	// and the estimate it was queued with stands.
	startSpeakerAttempt(t, f, 2, clock.now)
	clock.advance(70 * time.Second)
	putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 55_903, 3_850_492, clock.now)
	wantSpeakerProgress(t, "second split", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseSeparating, ElapsedMs: 70_000, EstimatedMs: estimate})
}

// One split already has turns and one has none: only the second counts.
func TestSpeakersProgressSkipsSplitsWithStoredTurns(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	// Its pace, ×1.15 for decoding, is the default 0.016 per audio ms.
	putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 16_000, 1_150_000, speakerProgressT0.Add(-time.Hour))
	f.useClock(speakerProgressT0)

	const estimate = 3_840_000*16/1000 + 14_520 // only spk_remote is diarized
	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"},{"speakerId":"spk_remote"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, EstimatedMs: estimate})
}

// A long wait in the queue does not eat the countdown: while queued, elapsed
// is the wait so far; once the attempt starts it counts from the start, and
// a resource deferral that sends it back to the queue reports the wait from
// the original queue time again until it restarts.
func TestSpeakersProgressCountsDownFromTheStartNotTheQueue(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	setSpeakerMeetingAudioMs(t, f.rt.cfg.WorkRoot, "MEETING1", 3_840_000)
	putSpeakerTurnSet(t, f.rt.store, "MEETING1", speakerTestRoom, 55_903, 3_850_492, speakerProgressT0.Add(-time.Hour))
	clock := f.useClock(speakerProgressT0)
	const estimate = 14_520

	resp := f.post(t, `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~1","label":"Ann"}]}}`)
	wantSpeakerProgress(t, "queued", resp, speakerEditsProgress{Phase: speakerPhaseQueued, EstimatedMs: estimate})

	// Five minutes behind another build: longer than the whole estimate.
	clock.advance(5 * time.Minute)
	wantSpeakerProgress(t, "waiting", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 300_000, EstimatedMs: estimate})

	startSpeakerAttempt(t, f, 2, clock.now)
	clock.advance(2 * time.Second)
	wantSpeakerProgress(t, "started", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 2_000, EstimatedMs: estimate})

	deferSpeakerAttempt(t, f, 2)
	clock.advance(time.Second)
	wantSpeakerProgress(t, "deferred", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseQueued, ElapsedMs: 303_000, EstimatedMs: estimate})

	startSpeakerAttempt(t, f, 2, clock.now)
	clock.advance(500 * time.Millisecond)
	wantSpeakerProgress(t, "restarted", f.get(t, "MEETING1"), speakerEditsProgress{Phase: speakerPhaseUpdating, ElapsedMs: 500, EstimatedMs: estimate})
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

func TestSpeakerRefineEstimateScalesWithTheMeeting(t *testing.T) {
	cases := []struct {
		name     string
		audioMs  int64
		diarizes int
		wantMs   int64
	}{
		// Unknown length: only the base is known, and it is the floor.
		{"unknown length", 0, 0, speakerRefineBaseMs},
		{"unknown length, splits to diarize", 0, 2, speakerRefineBaseMs},
		// Calibrated against measured runs: 3–4 s for 3 minutes, 14 s for 64.
		{"3 minutes", 180_000, 0, 3_540},
		{"64 minutes", 3_840_000, 0, 14_520},
		{"3 minutes, one split", 180_000, 1, 3_540 + 2_880},
		{"64 minutes, three splits", 3_840_000, 3, 14_520 + 3*61_440},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := speakerRefineEstimateMs(tc.audioMs, 0.016, tc.diarizes); got != tc.wantMs {
				t.Fatalf("estimate = %d, want %d", got, tc.wantMs)
			}
		})
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

// markSpeakerRebuildUnpublished makes the job's current meeting the bundle of
// a rerun whose publish failed: current/ follows the last attempt that built.
func markSpeakerRebuildUnpublished(t *testing.T, store *Store, workRoot, jobID string) int {
	t.Helper()
	job, err := store.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	rerun, err := store.QueueRerunAttempt(context.Background(), job, nowUTCString())
	if err != nil {
		t.Fatal(err)
	}
	attempt := rerun.CurrentAttemptNumber
	if _, err := store.db.Exec(`UPDATE job_attempts SET stage = 'done', state = 'failed', error = 'publish failed' WHERE job_id = ? AND attempt_number = ?`, jobID, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'failed' WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	if err := SetMeetingBundleRoom(canonicalMeetingPath(workRoot, jobID), "", "", "", jobID, attempt); err != nil {
		t.Fatal(err)
	}
	return attempt
}

// A rerun whose seal or publish failed left its rebuild — new transcript,
// re-encoded audio — in current/, while readers still have the recording
// before it. A speaker edit must not publish that rebuild in its name: the
// meeting is reported unavailable until a rerun publishes.
func TestSpeakersRefuseARebuildThatWasNeverPublished(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	if resp := f.get(t, "MEETING1"); !resp.Available {
		t.Fatalf("before the rerun: %+v", resp)
	}
	markSpeakerRebuildUnpublished(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	if resp := f.get(t, "MEETING1"); resp.Available || resp.Reason != speakerReasonUnpublishedRebuild {
		t.Fatalf("after an unpublished rerun: %+v", resp)
	}
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"labels":[{"speakerId":"spk_room","label":"Room"}]}}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), speakerReasonUnpublishedRebuild) {
		t.Fatalf("POST after an unpublished rerun = %d %s", rec.Code, rec.Body.String())
	}
}

// A save queues an attempt, so it is admitted the way a rerun is: under the
// job's artifact lock, never while an archive or Nextcloud retention operation
// is pending, and never once retention removed the capture or the recording's
// media is set to be deleted after processing. The lock is waited for only
// briefly, so a worker holding it for a whole build answers busy instead of
// holding the request, and one that lets go in time does not.
func TestSpeakersPostIsAdmittedLikeARerun(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	const body = `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`
	queuedNothing := func(step string) {
		t.Helper()
		rec, err := f.rt.store.GetSpeakerEdits(context.Background(), "MEETING1")
		job, jobErr := f.rt.store.GetJob(context.Background(), "MEETING1")
		if err != nil || jobErr != nil || rec.Revision != 0 || job.CurrentAttemptNumber != 1 {
			t.Fatalf("%s: revision %d attempt %d (%v %v), want nothing stored or queued", step, rec.Revision, job.CurrentAttemptNumber, err, jobErr)
		}
	}

	// A build, seal, publish or expiry of the job holds its lock.
	unlock := f.rt.store.lockArtifacts("MEETING1")
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body) }()
	select {
	case rec := <-answered:
		if rec.Code != http.StatusConflict || annTestError(t, rec) != "busy" {
			t.Fatalf("POST while the artifacts are locked = %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(testWaitTimeout):
		unlock()
		t.Fatal("POST waited on the job's artifact lock")
	}
	unlock()
	queuedNothing("locked")

	// A lock released while the save waits for it: let go only once the
	// save found it taken.
	waiting := make(chan struct{}, 1)
	f.rt.store.lockWaitBlocked = func(string) {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	unlock = f.rt.store.lockArtifacts("MEETING1")
	go func() { answered <- annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body) }()
	select {
	case <-waiting:
	case <-time.After(testWaitTimeout):
		unlock()
		t.Fatal("POST never waited for the job's artifact lock")
	}
	unlock()
	if rec := <-answered; rec.Code != http.StatusOK {
		t.Fatalf("POST once the lock came free = %d %s", rec.Code, rec.Body.String())
	}
	// Back to idle at revision 0 for the checks below.
	if _, err := f.rt.store.db.Exec(`DELETE FROM speaker_edits; DELETE FROM job_attempts WHERE attempt_number > 1; UPDATE jobs SET stage = 'done', state = 'succeeded', current_attempt_number = 1`); err != nil {
		t.Fatal(err)
	}

	// A promotion or expiry journalled and not finished.
	if _, err := f.rt.store.db.Exec(`INSERT INTO artifact_operations (job_id, operation) VALUES ('MEETING1', '{}')`); err != nil {
		t.Fatal(err)
	}
	if rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body); rec.Code != http.StatusConflict || annTestError(t, rec) != "busy" {
		t.Fatalf("POST with an archive operation pending = %d %s", rec.Code, rec.Body.String())
	}
	queuedNothing("pending operation")
	if _, err := f.rt.store.db.Exec(`DELETE FROM artifact_operations`); err != nil {
		t.Fatal(err)
	}

	// Retention removed the capture.
	if _, err := f.rt.store.db.Exec(`INSERT INTO artifact_availability (job_id, source) VALUES ('MEETING1', 'expired')`); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, "MEETING1"); resp.Available || resp.Reason != speakerReasonNoSourceAudio {
		t.Fatalf("GET with the capture expired: %+v", resp)
	}
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body)
	if rec.Code != http.StatusConflict || annTestError(t, rec) != "unavailable" || !strings.Contains(rec.Body.String(), speakerReasonNoSourceAudio) {
		t.Fatalf("POST with the capture expired = %d %s", rec.Code, rec.Body.String())
	}
	queuedNothing("source expired")
	// The store refuses it too, whatever answered the page before.
	if _, err := f.rt.store.QueueSpeakerEdits(context.Background(), "MEETING1", 0, splitDoc(speakerTestRoom), "alice", nowUTCString()); !errors.Is(err, errSpeakerEditsSourceExpired) {
		t.Fatalf("QueueSpeakerEdits with the capture expired = %v", err)
	}

	if _, err := f.rt.store.db.Exec(`DELETE FROM artifact_availability`); err != nil {
		t.Fatal(err)
	}

	// A Nextcloud retention operation on the published meeting, not completed.
	if _, err := f.rt.store.db.Exec(`INSERT INTO remote_retention_operation (name, operation_json, status, updated_at) VALUES ('MEETING1.opus', '{}', 'prepared', ?)`, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	if rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body); rec.Code != http.StatusConflict || annTestError(t, rec) != "busy" {
		t.Fatalf("POST with a Nextcloud retention operation pending = %d %s", rec.Code, rec.Body.String())
	}
	queuedNothing("pending remote retention")
	if _, err := f.rt.store.db.Exec(`UPDATE remote_retention_operation SET status = 'completed'`); err != nil {
		t.Fatal(err)
	}

	// The recording's media is set to be deleted after processing: gone, or
	// about to be, for a refine as for a rerun.
	var request string
	if err := f.rt.store.db.QueryRow(`SELECT request_json FROM jobs WHERE id = 'MEETING1'`).Scan(&request); err != nil {
		t.Fatal(err)
	}
	settings := STTSettings{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, TranscriptionEnabled: true}
	disposing, _ := json.Marshal(TriggerRequest{ProcessingPolicy: &recordingProcessingPolicy{MeetingFormat: "json", SourceRetention: sourceDeleteAfterProcessing, Transcription: &settings}})
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET request_json = ? WHERE id = 'MEETING1'`, string(disposing)); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, "MEETING1"); resp.Available || resp.Reason != speakerReasonNoSourceAudio {
		t.Fatalf("GET with the media set to be deleted: %+v", resp)
	}
	if rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), speakerReasonNoSourceAudio) {
		t.Fatalf("POST with the media set to be deleted = %d %s", rec.Code, rec.Body.String())
	}
	queuedNothing("media set to be deleted")
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET request_json = ? WHERE id = 'MEETING1'`, request); err != nil {
		t.Fatal(err)
	}

	if resp := f.post(t, body); resp.State != speakerStateApplying || resp.Revision != 1 {
		t.Fatalf("POST once nothing stands in the way: %+v", resp)
	}
}

// speakerTestRerunTranscript is what a rerun that heard a third participant
// built: the two devices of speakerTestTranscript and a late joiner.
const speakerTestRerunTranscript = `{"version":"transcript.words.v1","speakers":[` +
	`{"id":"spk_room","label":"Meeting room laptop"},{"id":"spk_remote","label":"Remote"},{"id":"spk_late","label":"Late joiner"}],"segments":[]}`

// publishUnpromotedRerun leaves jobID the way a publish does between marking
// its attempt succeeded and promoting it: attempt 1 is what current/ holds,
// and attempt 2, a rerun with a third participant, is published from its own
// bundle and not copied into current/ yet.
func publishUnpromotedRerun(t *testing.T, store *Store, workRoot, jobID string) {
	t.Helper()
	at := nowUTCString()
	if _, err := store.db.Exec(`UPDATE job_attempts SET publish_finished_at = ? WHERE job_id = ? AND attempt_number = 1`, at, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_availability (job_id, published_attempt, output) VALUES (?, 1, 'present')`, jobID); err != nil {
		t.Fatal(err)
	}
	current := canonicalMeetingPath(workRoot, jobID)
	if err := SetMeetingBundleRoom(current, "", "", "", jobID, 1); err != nil {
		t.Fatal(err)
	}
	run := canonicalRunPath(workRoot, jobID)
	if _, err := store.db.Exec(`
INSERT INTO job_attempts (job_id, attempt_number, trigger_kind, request_json, stage, state, artifact_run_path, created_at, updated_at, publish_finished_at, completed_at)
VALUES (?, 2, 'rerun', '{}', 'done', 'succeeded', ?, ?, ?, ?, ?)`, jobID, run, at, at, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE jobs SET current_attempt_number = 2, stage = 'done', state = 'succeeded' WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	rerun := attemptMeetingPath(workRoot, jobID, 2)
	writeSpeakerMeetingFixture(t, rerun, run)
	if err := os.WriteFile(filepath.Join(rerun, speakerTranscriptPrimary), []byte(speakerTestRerunTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetMeetingBundleRoom(rerun, "", "", "", jobID, 2); err != nil {
		t.Fatal(err)
	}
}

// A publish marks its attempt succeeded, and the edits it carries applied,
// before it copies the attempt's bundle into current/. A GET in that window
// describes the meeting readers have, not the one current/ still holds.
func TestSpeakersGetReadsThePublishedMeetingBeforeItIsPromoted(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	publishUnpromotedRerun(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	resp := f.get(t, "MEETING1")
	if !resp.Available || len(resp.Participants) != 3 || resp.Participants[2].ID != "spk_late" {
		t.Fatalf("GET before the promotion: %+v, want the published rerun's three participants", resp)
	}

	// The promotion lands and prunes the attempt's bundle: the same meeting.
	current := canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1")
	rerun := attemptMeetingPath(f.rt.cfg.WorkRoot, "MEETING1", 2)
	if err := os.RemoveAll(current); err != nil {
		t.Fatal(err)
	}
	if err := copyDirectory(rerun, current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.store.db.Exec(`UPDATE artifact_availability SET published_attempt = 2 WHERE job_id = 'MEETING1'`); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(rerun); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, "MEETING1"); !resp.Available || len(resp.Participants) != 3 {
		t.Fatalf("GET after the promotion: %+v", resp)
	}
}

// A GET picks the published attempt's own bundle and then reads it. If that
// attempt's promotion, or a later attempt's, is recorded in between and the
// bundle is pruned, the GET reads current/ instead: it must not report the
// meeting as having no transcript.
func TestSpeakersGetRereadsCurrentWhenThePromotionPrunesUnderIt(t *testing.T) {
	const laterTranscript = `{"version":"transcript.words.v1","speakers":[` +
		`{"id":"spk_room","label":"Meeting room laptop"},{"id":"spk_remote","label":"Remote"},{"id":"spk_late","label":"Late joiner"},{"id":"spk_later","label":"Later still"}],"segments":[]}`
	for _, c := range []struct {
		name     string
		promoted int
		want     int
	}{
		{"its own promotion", 2, 3},
		{"a later attempt's promotion", 3, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSpeakersFixture(t)
			f.installModel(t)
			seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
			publishUnpromotedRerun(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
			current := canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1")
			rerun := attemptMeetingPath(f.rt.cfg.WorkRoot, "MEETING1", 2)
			pruned := false
			f.rt.speakerMeetingReading = func(path string) {
				if path != rerun || pruned {
					return
				}
				pruned = true
				if err := os.RemoveAll(current); err != nil {
					t.Fatal(err)
				}
				if err := copyDirectory(rerun, current); err != nil {
					t.Fatal(err)
				}
				if c.promoted == 3 {
					if err := os.WriteFile(filepath.Join(current, speakerTranscriptPrimary), []byte(laterTranscript), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.rt.store.db.Exec(`UPDATE artifact_availability SET published_attempt = ? WHERE job_id = 'MEETING1'`, c.promoted); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(rerun); err != nil {
					t.Fatal(err)
				}
			}
			resp := f.get(t, "MEETING1")
			if !pruned {
				t.Fatal("the GET never read the rerun's own bundle")
			}
			if !resp.Available || len(resp.Participants) != c.want {
				t.Fatalf("GET with the bundle pruned under it: %+v, want %d participants from current/", resp, c.want)
			}
		})
	}
}

// A build that kept only the audio (transcription skipped or failed) still
// lists every participant in its transcript, with no words. There is nothing
// to separate or name: splitting one diarized a whole track and then reported
// "only one voice found".
func TestSpeakersAudioOnlyMeetingHasNoTranscriptToSplit(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	manifest := `{"version":"cassini.meeting-artifact.v1","processing":{"transcription":{"status":"skipped","reason":"model_unavailable"}}}`
	if err := os.WriteFile(filepath.Join(canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1"), "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, "MEETING1"); resp.Available || resp.Reason != speakerReasonNoTranscript {
		t.Fatalf("audio-only meeting: %+v", resp)
	}
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"splits":[{"speakerId":"spk_room"}]}}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), speakerReasonNoTranscript) {
		t.Fatalf("POST on an audio-only meeting = %d %s", rec.Code, rec.Body.String())
	}
}

// "merged" is the mixed track a thin per-participant transcription falls
// back to. It is nobody's own audio, so it is not a device that can be split.
func TestSpeakersMixedTrackIsNotADevice(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	transcript := `{"version":"transcript.words.v1","speakers":[{"id":"spk_room","label":"Meeting room laptop"},{"id":"merged","label":"Everyone"}]}`
	if err := os.WriteFile(filepath.Join(canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1"), "transcript.words.v1.json"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, "MEETING1"); len(resp.Participants) != 1 || resp.Participants[0].ID != speakerTestRoom {
		t.Fatalf("participants = %+v, want the room only", resp.Participants)
	}
	rec := annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", `{"expectRevision":0,"doc":{"splits":[{"speakerId":"merged"}]}}`)
	if rec.Code != http.StatusBadRequest || annTestError(t, rec) != "invalid" {
		t.Fatalf("split of the mixed track = %d %s", rec.Code, rec.Body.String())
	}
}

// Saving the edits the recording already carries changes nothing, so it
// queues nothing: no republish and no summary model call each time someone
// presses Save again. A different document, or a revision that failed, is
// queued as before.
func TestSpeakersPostOfTheAppliedEditsQueuesNothing(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	applied := `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[{"speakerId":"spk_room"}],"merges":[],"labels":[{"speakerId":"spk_room~1","label":"Ann"},{"speakerId":"spk_room~2","label":"Bea"}]}`
	if _, err := f.rt.store.db.Exec(`INSERT INTO speaker_edits (job_id, revision, doc_json, applied_revision, applied_doc_json, updated_at) VALUES ('MEETING1', 1, ?, 1, ?, ?)`, applied, applied, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	attempts := func() int {
		t.Helper()
		var n int
		if err := f.rt.store.db.QueryRow(`SELECT COUNT(*) FROM job_attempts WHERE job_id = 'MEETING1'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := attempts()

	same := `{"expectRevision":1,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~2","label":"Bea "},{"speakerId":"spk_room~1","label":"Ann"}]}}`
	resp := f.post(t, same)
	if resp.Revision != 1 || resp.AppliedRevision != 1 || resp.State != speakerStateIdle || attempts() != before {
		t.Fatalf("the applied edits again: %+v, %d attempts (was %d); want nothing queued", resp, attempts(), before)
	}

	// Another name is an edit.
	resp = f.post(t, `{"expectRevision":1,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~1","label":"Ann"},{"speakerId":"spk_room~2","label":"Bo"}]}}`)
	if resp.Revision != 2 || resp.State != speakerStateApplying || attempts() != before+1 {
		t.Fatalf("a new name: %+v, %d attempts", resp, attempts())
	}
	// That revision fails: sending it again is a retry, and is queued.
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'failed' WHERE id = 'MEETING1'`); err != nil {
		t.Fatal(err)
	}
	setSpeakerAttemptStage(t, f, before+1, "done", "failed")
	resp = f.post(t, `{"expectRevision":2,"doc":{"splits":[{"speakerId":"spk_room"}],"labels":[{"speakerId":"spk_room~1","label":"Ann"},{"speakerId":"spk_room~2","label":"Bo"}]}}`)
	if resp.Revision != 3 || resp.State != speakerStateApplying || attempts() != before+2 {
		t.Fatalf("a retry of a failed revision: %+v, %d attempts", resp, attempts())
	}
}

// A refine that failed after its build — at seal or publish, where nothing in
// the speaker-edits path records an error — followed by any rerun (which
// replays the applied revision) must still be reported failed: the saved
// revision never reached the recording. And what the page is told is a
// sentence, not the publish sink's error with the archive's paths in it.
func TestSpeakersRefineThatFailedAfterItsBuildStaysFailedAcrossARerun(t *testing.T) {
	f := newSpeakersFixture(t)
	f.installModel(t)
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	applied := `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[],"merges":[],"labels":[{"speakerId":"spk_remote","label":"Remote Ann"}]}`
	if _, err := f.rt.store.db.Exec(`INSERT INTO speaker_edits (job_id, revision, doc_json, applied_revision, applied_doc_json, updated_at) VALUES ('MEETING1', 1, ?, 1, ?, ?)`, applied, applied, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	resp := f.post(t, `{"expectRevision":1,"doc":{"labels":[{"speakerId":"spk_remote","label":"Remote Bea"}]}}`)
	refine := 2
	if resp.Revision != 2 {
		t.Fatalf("POST = %+v", resp)
	}
	if _, err := f.rt.store.db.Exec(`UPDATE job_attempts SET stage = 'done', state = 'failed', error = 'nc files: cassini/CassiniRecordings/meetings/MEETING1.opus: ETag mismatch' WHERE job_id = 'MEETING1' AND attempt_number = ?`, refine); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'failed' WHERE id = 'MEETING1'`); err != nil {
		t.Fatal(err)
	}
	job, err := f.rt.store.GetJob(context.Background(), "MEETING1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.store.QueueRerunAttempt(context.Background(), job, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	setSpeakerAttemptStage(t, f, refine+1, "done", "succeeded")
	if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'succeeded' WHERE id = 'MEETING1'`); err != nil {
		t.Fatal(err)
	}

	resp = f.get(t, "MEETING1")
	if resp.State != speakerStateFailed || resp.Revision != 2 || resp.AppliedRevision != 1 || resp.LastError != "The recording could not be updated." {
		t.Fatalf("after the rerun: %+v; want revision 2 reported failed, in plain words", resp)
	}
}
