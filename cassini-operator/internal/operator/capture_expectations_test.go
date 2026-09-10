package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cassini-operator/internal/operator/appapi"
)

func TestCaptureRegistrationAndBoundedWait(t *testing.T) {
	rt := &Runtime{store: newRebuildTestStore(t), ctx: context.Background()}
	rt.cfg.CaptureRoot = t.TempDir()
	rt.buildQueue = make(chan buildTask, 10)
	rt.logger = quietLogger()
	now := time.Now().UTC()
	start := now.Add(-time.Minute)
	seedRecording(t, rt.store, "call", "room", formatUTCString(start), formatUTCString(now))
	setJobState(t, rt.store, "call", "build", "queued")
	member := func(ctx context.Context, owner, room string) (bool, error) {
		return owner == "alice" && room == "room", nil
	}
	announce := func(owner, status string, startMS, endMS int64) int {
		raw, _ := json.Marshal(map[string]any{"roomToken": "room", "callStartWallMs": startMS, "callEndWallMs": endMS, "status": status, "owner": "spoofed"})
		req := httptest.NewRequest(http.MethodPost, "/capture/register", strings.NewReader(string(raw)))
		req = req.WithContext(appapi.WithUserID(context.Background(), owner))
		rec := httptest.NewRecorder()
		rt.captureRegisterHandler(member)(rec, req)
		return rec.Code
	}
	if got := announce("", "recording", start.UnixMilli(), now.UnixMilli()); got != 401 {
		t.Fatalf("unauthenticated: %d", got)
	}
	if got := announce("bob", "recording", start.UnixMilli(), now.UnixMilli()); got != 403 {
		t.Fatalf("membership: %d", got)
	}
	if got := announce("alice", "uploading", start.UnixMilli(), now.UnixMilli()); got != 200 {
		t.Fatalf("register: %d", got)
	}
	if got := announce("alice", "recording", start.UnixMilli(), now.Add(-time.Second).UnixMilli()); got != 200 {
		t.Fatalf("retry: %d", got)
	}
	expected, deadline, err := rt.captureExpectationsForJob(rt.ctx, "call", nil)
	if err != nil || len(expected) != 1 || expected[0].Owner != "alice" || expected[0].CallEndMS != now.UnixMilli() || deadline == nil || !deadline.Equal(now.Add(captureArrivalGrace)) {
		t.Fatalf("expectations: %+v %v %v", expected, deadline, err)
	}
	if !rt.waitingForCaptureUploads("call") {
		t.Fatal("build did not wait")
	}
	// Waiting jobs stay durable without filling the worker queue.
	if _, err := rt.store.db.Exec(`UPDATE jobs SET artifact_run_path='/run' WHERE id='call'`); err != nil {
		t.Fatal(err)
	}
	dispatched := map[string]struct{}{}
	rt.dispatchQueuedBuildTasks(dispatched)
	if len(rt.buildQueue) != 0 {
		t.Fatal("waiting job occupied build worker")
	}
	// Reconstruct runtime to prove registration isn't process-local.
	restarted := &Runtime{store: rt.store, cfg: rt.cfg, ctx: rt.ctx}
	if !restarted.waitingForCaptureUploads("call") {
		t.Fatal("restart lost expectation")
	}
	// A partial recovery upload must not satisfy a longer announced capture.
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room", "alice", start.UnixMilli(), now.Add(-time.Second).UnixMilli(), 123)
	if !rt.waitingForCaptureUploads("call") {
		t.Fatal("partial upload prematurely satisfied registration")
	}
	seedRebuildCapture(t, rt.cfg.CaptureRoot, "room", "alice", start.UnixMilli(), now.UnixMilli(), 123)
	if rt.waitingForCaptureUploads("call") {
		t.Fatal("complete upload did not release build")
	}
	rt.dispatchQueuedBuildTasks(dispatched)
	if len(rt.buildQueue) != 1 {
		t.Fatal("arrival did not release queued build")
	}
	rt.dispatchQueuedBuildTasks(dispatched)
	if len(rt.buildQueue) != 1 {
		t.Fatal("released build was queued twice")
	}
	// A second session is independent even for the same authenticated participant.
	if got := announce("alice", "recording", start.Add(time.Millisecond).UnixMilli(), now.UnixMilli()); got != 200 {
		t.Fatalf("second session: %d", got)
	}
	if !rt.waitingForCaptureUploads("call") {
		t.Fatal("second session was conflated with first")
	}
	_, err = rt.store.db.Exec(`UPDATE jobs SET record_finished_at=? WHERE id='call'`, formatUTCString(now.Add(-3*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if rt.waitingForCaptureUploads("call") {
		t.Fatal("missing client blocked past deadline")
	}
	expected, deadline, err = rt.captureExpectationsForJob(rt.ctx, "call", nil)
	if err != nil || deadline != nil || expected[1].Status != "timed_out" {
		t.Fatalf("timeout invisible: %+v %v %v", expected, deadline, err)
	}
}

func TestSourceAudioObservationKeepsAmbiguousUploadsVisible(t *testing.T) {
	root := t.TempDir()
	start := time.Now().Add(-time.Minute).UnixMilli()
	end := start + 60000
	seedRebuildCapture(t, root, "room", "admin", start, end, 123)
	seedRebuildCapture(t, root, "room", "admin", start+1, end, 456)
	set, err := scanSourceCapturesForRecording(root, "room", captureRecordingWindow{StartMS: start, EndMS: end})
	if err != nil || set.Count != 0 || len(set.Owners) != 0 || len(set.Uploads) != 2 {
		t.Fatalf("scan: %+v %v", set, err)
	}
	for _, upload := range set.Uploads {
		if !upload.Complete || !strings.Contains(upload.ExclusionReason, "Overlapping") {
			t.Fatalf("rejection hidden: %+v", upload)
		}
	}
}
