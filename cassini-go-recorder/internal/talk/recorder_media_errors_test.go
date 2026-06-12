package talk

// Regression guards for D-357: media-path errors used to be swallowed —
// per-packet rtplog write failures were log-and-continue, per-stream close
// errors were discarded by sessionCaptureArtifact.close(), and the fatal
// run-loop path threw away the cleanup outcome — so a truncated recording
// could exit 0 and finalize as ready. These tests pin the new behavior:
// media loss is sticky and fails the artifact close (and thus the run),
// benign shutdown races stay benign, and a fatal loop error whose cleanup
// fully succeeded is tagged ErrAbnormalStop so callers can salvage the
// composed recording.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func newMediaErrorTestArtifact(t *testing.T) *sessionCaptureArtifact {
	t.Helper()
	tmp := t.TempDir()
	artifact, err := newSessionCaptureArtifact(
		filepath.Join(tmp, "recording.mkv"),
		"https://example.test/call/room",
		"room-token",
		"recorder",
	)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	return artifact
}

func mediaErrorTestDescriptor() trackDescriptor {
	return trackDescriptor{
		kind:      "audio",
		codec:     "audio/opus",
		mid:       "0",
		clockRate: 48000,
	}
}

func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection does not work as root")
	}
}

func TestSessionArtifactClosePropagatesStreamCloseFailure(t *testing.T) {
	requireNonRoot(t)
	artifact := newMediaErrorTestArtifact(t)

	streamID, err := artifact.openStream("remote-1", "user-1", "Alice", mediaErrorTestDescriptor(), 1234, 111, time.Now())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SSRC: 1234, PayloadType: 111},
		Payload: []byte{1, 2, 3},
	}
	if err := artifact.writeRTP(streamID, pkt, time.Now()); err != nil {
		t.Fatalf("write rtp: %v", err)
	}

	// Make the streams dir read-only so closeStream's BuildIndex cannot
	// create the .idx file: a real shutdown-time stream-finalize failure.
	if err := os.Chmod(artifact.streamsDir, 0o555); err != nil {
		t.Fatalf("chmod streams dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(artifact.streamsDir, 0o755) })

	if err := artifact.close(); err == nil {
		t.Fatalf("expected close() to surface the per-stream close failure; pre-D-357 it was discarded and the run finalized as ready")
	}
}

func TestSessionArtifactCloseSurfacesEarlierOpenFailure(t *testing.T) {
	requireNonRoot(t)
	artifact := newMediaErrorTestArtifact(t)

	if err := os.Chmod(artifact.streamsDir, 0o555); err != nil {
		t.Fatalf("chmod streams dir: %v", err)
	}
	if _, err := artifact.openStream("remote-1", "user-1", "Alice", mediaErrorTestDescriptor(), 1234, 111, time.Now()); err == nil {
		t.Fatalf("expected open stream to fail with read-only streams dir")
	}
	if err := os.Chmod(artifact.streamsDir, 0o755); err != nil {
		t.Fatalf("restore streams dir permissions: %v", err)
	}

	summary := artifact.summary()
	if summary.CaptureErrorCount == 0 {
		t.Fatalf("expected the failed stream open to be recorded as a capture error")
	}
	if !strings.Contains(summary.FirstCaptureError, "open stream") {
		t.Fatalf("unexpected first capture error: %q", summary.FirstCaptureError)
	}

	// The participant's media was lost from the failed open onward, so a
	// smooth shutdown must still fail the artifact close (and the run).
	if err := artifact.close(); err == nil {
		t.Fatalf("expected close() to report media loss after a failed stream open")
	}
}

func TestSessionArtifactWriteAfterStreamCloseIsNotMediaLoss(t *testing.T) {
	artifact := newMediaErrorTestArtifact(t)

	streamID, err := artifact.openStream("remote-1", "user-1", "Alice", mediaErrorTestDescriptor(), 1234, 111, time.Now())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := artifact.closeStream(streamID, "ended", time.Now()); err != nil {
		t.Fatalf("close stream: %v", err)
	}

	// Track readers racing shutdown can write to a just-closed stream;
	// that is a benign ordering race, not media loss.
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SSRC: 1234, PayloadType: 111},
		Payload: []byte{1, 2, 3},
	}
	if err := artifact.writeRTP(streamID, pkt, time.Now()); err == nil {
		t.Fatalf("expected write to a closed stream to error")
	}
	if got := artifact.summary().CaptureErrorCount; got != 0 {
		t.Fatalf("write to closed stream was recorded as media loss: capture_error_count=%d", got)
	}

	if err := artifact.close(); err != nil {
		t.Fatalf("close artifact: %v", err)
	}

	// Stream opens after close (shutdown stragglers) report the dedicated
	// sentinel so callers neither log noise nor keep retrying.
	if _, err := artifact.openStream("remote-1", "user-1", "Alice", mediaErrorTestDescriptor(), 1234, 111, time.Now()); !errors.Is(err, errSessionArtifactClosed) {
		t.Fatalf("expected errSessionArtifactClosed, got %v", err)
	}
	if got := artifact.summary().CaptureErrorCount; got != 0 {
		t.Fatalf("post-close open attempt was recorded as media loss: capture_error_count=%d", got)
	}
}

func TestSessionArtifactWriteRacingStreamCloseIsNotMediaLoss(t *testing.T) {
	artifact := newMediaErrorTestArtifact(t)

	streamID, err := artifact.openStream("remote-1", "user-1", "Alice", mediaErrorTestDescriptor(), 1234, 111, time.Now())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Simulate the closeStream race: writeRTP fetches the writer under
	// a.mu, then a concurrent closeStream closes that writer before the
	// write lands. The store writer rejects the record with
	// ErrWriterClosed without touching the file — a correctly-dropped
	// packet, not media loss.
	artifact.mu.Lock()
	writer := artifact.streams[streamID].writer
	artifact.mu.Unlock()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SSRC: 1234, PayloadType: 111},
		Payload: []byte{1, 2, 3},
	}
	if err := artifact.writeRTP(streamID, pkt, time.Now()); err == nil {
		t.Fatalf("expected write on a closed writer to error")
	}
	if got := artifact.summary().CaptureErrorCount; got != 0 {
		t.Fatalf("write racing stream close was recorded as media loss: capture_error_count=%d", got)
	}
}

func TestFatalRunErrorTagsSalvageableRuns(t *testing.T) {
	loopErr := errors.New("signaling connection error: ws closed")

	salvageable := fatalRunError(loopErr, nil)
	if !errors.Is(salvageable, ErrAbnormalStop) {
		t.Fatalf("expected ErrAbnormalStop tag when cleanup succeeded, got %v", salvageable)
	}
	if !strings.Contains(salvageable.Error(), loopErr.Error()) {
		t.Fatalf("expected loop error detail in %q", salvageable)
	}

	cleanupErr := errors.New("compose final output failed: no remuxable streams")
	failed := fatalRunError(loopErr, cleanupErr)
	if errors.Is(failed, ErrAbnormalStop) {
		t.Fatalf("a failed cleanup must not be salvageable, got %v", failed)
	}
	if !strings.Contains(failed.Error(), loopErr.Error()) || !strings.Contains(failed.Error(), cleanupErr.Error()) {
		t.Fatalf("expected both loop and cleanup errors to surface, got %q", failed)
	}
}
