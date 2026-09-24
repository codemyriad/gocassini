package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func seedAVCapture(t *testing.T, root string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg required")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required")
	}
	bundle, err := PrepareRunBundle(root, false)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=64x64:rate=10", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-metadata:s:a:0", "title=Alice", "-metadata:s:a:1", "title=Bob", "-c:v", "libvpx", "-c:a", "libopus", "-t", "0.5", bundle.RecordingPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	if err = FinalizeRunBundle(bundle, RunManifest{SourceMode: "talk"}); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "session")
	if err = os.MkdirAll(filepath.Join(session, "streams"), 0755); err != nil {
		t.Fatal(err)
	}
	meta := `{"version":1,"session_id":"fixture","logical_tracks":[{"ltid":"a","kind":"audio"},{"ltid":"v","kind":"video"}],"packet_streams":[{"stream_id":"audio","ltid":"a","codec":"audio/opus"},{"stream_id":"video","ltid":"v","codec":"video/VP8"}],"transceivers":[{"kind":"audio"},{"kind":"video"}]}`
	if err = os.WriteFile(filepath.Join(session, "session.json"), []byte(meta), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audio.rtplog", "audio.idx", "video.rtplog", "video.idx"} {
		if err = os.WriteFile(filepath.Join(session, "streams", name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle.RecordingPath
}
func TestRetentionVideoPreservesMultitrackAudio(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "video"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	run := canonicalRunPath(rt.cfg.WorkRoot, id)
	media := seedAVCapture(t, run)
	streams, err := probeCapture(context.Background(), media)
	if err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintCaptureAudio(context.Background(), media, streams)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatal("fixture must have two audio streams")
	}
	job := mustGetJob(t, rt.store, id)
	job.ArtifactRunPath = &run
	now := time.Now()
	err = rt.expireCaptureVideo(context.Background(), job, 1, retentionPolicy{Count: 1, Unit: "days"}, now.AddDate(0, 0, -2), now, 1)
	if err != nil {
		t.Fatal(err)
	}
	streams, err = probeCapture(context.Background(), media)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range streams {
		if s.Kind != "audio" {
			t.Fatal("video survived")
		}
	}
	after, err := fingerprintCaptureAudio(context.Background(), media, streams)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("audio changed")
		}
	}
	assertGone(t, filepath.Join(run, "session", "streams", "video.rtplog"), "video packets")
	assertGone(t, filepath.Join(run, "session", "streams", "video.idx"), "video index")
	for _, name := range []string{"audio.rtplog", "audio.idx"} {
		b, err := os.ReadFile(filepath.Join(run, "session", "streams", name))
		if err != nil || string(b) != name {
			t.Fatal("audio packets changed", err)
		}
	}
	b, err := os.ReadFile(filepath.Join(run, "session", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err = json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta["logical_tracks"].([]any)) != 1 || len(meta["packet_streams"].([]any)) != 1 {
		t.Fatal("stale video references")
	}
	if _, err = requireReadyRunBundle(run); err != nil {
		t.Fatal("not rerunnable", err)
	}
	// The second pass does not touch an already-transformed recording.
	digest, _ := fileSHA256(media)
	if err = rt.expireCaptureVideo(context.Background(), job, 1, retentionPolicy{Count: 1, Unit: "days"}, now.AddDate(0, 0, -2), now, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := fileSHA256(media)
	if digest != got {
		t.Fatal("not idempotent")
	}
}
func TestRetentionVideoFailureLeavesOriginal(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	id := "unknown"
	insertJob(t, rt.store.db, id, "2026-01-01T00:00:00Z")
	run := canonicalRunPath(rt.cfg.WorkRoot, id)
	media := seedAVCapture(t, run)
	before, _ := fileSHA256(media)
	if err := os.WriteFile(filepath.Join(run, "unknown.bin"), []byte("unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	job := mustGetJob(t, rt.store, id)
	now := time.Now()
	if err := rt.expireCaptureVideo(context.Background(), job, 1, retentionPolicy{Count: 1, Unit: "days"}, now.AddDate(0, 0, -2), now, 1); err == nil {
		t.Fatal("unknown payload accepted")
	}
	after, _ := fileSHA256(media)
	if before != after {
		t.Fatal("original mutated")
	}
	assertExists(t, filepath.Join(run, "session", "streams", "video.rtplog"), "failed staging retains video")
	if rt.pendingArtifactOperation(id) {
		t.Fatal("unvalidated copy journalled")
	}
}

func TestRetentionVideoCopyOutOfSpacePreservesOriginal(t *testing.T) {
	source := filepath.Join(t.TempDir(), "capture.run")
	media := seedAVCapture(t, source)
	before, _ := fileSHA256(media)
	staged := filepath.Join(t.TempDir(), "new-0")
	err := prepareAudioOnlyCapture(context.Background(), source, staged, func(_, dst string) error {
		if err := os.MkdirAll(dst, 0700); err != nil {
			return err
		}
		return syscall.ENOSPC
	})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	after, _ := fileSHA256(media)
	if before != after {
		t.Fatal("original changed on ENOSPC")
	}
	assertExists(t, filepath.Join(source, "session", "streams", "video.rtplog"), "no premature packet deletion")
}
