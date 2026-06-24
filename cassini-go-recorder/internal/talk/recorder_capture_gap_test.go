package talk

// Guards for D-386: the recorder must reconcile the participants it stood a
// subscriber up for (inCallEver) against the audio it actually captured, and
// surface any in-call participant it recorded nothing — or almost nothing —
// for. Before this, a participant abandoned after requestoffer exhaustion, or
// one whose offer arrived but whose media never flowed, opened no stream and
// raised no capture error, so the loss was completely invisible.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gocassini/internal/config"
)

func gapBySID(gaps []captureGap, sid string) (captureGap, bool) {
	for _, g := range gaps {
		if g.RemoteSessionID == sid {
			return g, true
		}
	}
	return captureGap{}, false
}

func TestDetectCaptureGapsFlagsInCallZeroAudio(t *testing.T) {
	r := &Recorder{
		inCallEver:       map[string]struct{}{"alice-session": {}},
		identityByRemote: map[string]participantIdentity{},
	}
	sessions := []sessionCapture{
		{RemoteSessionID: "alice-session", ParticipantName: "Alice", AudioPackets: 0, VideoPackets: 0},
	}

	gaps := r.detectCaptureGaps(sessions)

	if len(gaps) != 1 {
		t.Fatalf("expected exactly one gap, got %d: %+v", len(gaps), gaps)
	}
	g := gaps[0]
	if g.RemoteSessionID != "alice-session" || g.Reason != "no-media" || !g.CapturedSession {
		t.Fatalf("unexpected gap: %+v", g)
	}
}

func TestDetectCaptureGapsZeroAudioButHasVideoIsNotMissing(t *testing.T) {
	// A muted / camera-only participant is legitimately in the recording.
	// Flagging them would cry wolf, so audio==0 with video>0 must NOT be a gap.
	r := &Recorder{
		inCallEver:       map[string]struct{}{"cam-session": {}},
		identityByRemote: map[string]participantIdentity{},
	}
	sessions := []sessionCapture{
		{RemoteSessionID: "cam-session", ParticipantName: "Cam", AudioPackets: 0, VideoPackets: 500},
	}

	if gaps := r.detectCaptureGaps(sessions); len(gaps) != 0 {
		t.Fatalf("video-only participant must not be flagged, got %+v", gaps)
	}
}

func TestDetectCaptureGapsNeverCaptured(t *testing.T) {
	// In-call but no sessionCapture row at all: the severe case (the track
	// never fired). The name must fall back to the remembered identity.
	r := &Recorder{
		inCallEver: map[string]struct{}{"bob-session": {}},
		identityByRemote: map[string]participantIdentity{
			"bob-session": {DisplayName: "Bob"},
		},
	}

	gaps := r.detectCaptureGaps(nil)

	g, ok := gapBySID(gaps, "bob-session")
	if !ok {
		t.Fatalf("expected a gap for the never-captured in-call session, got %+v", gaps)
	}
	if g.Reason != "no-media" || g.CapturedSession {
		t.Fatalf("expected no-media/captured=false, got %+v", g)
	}
	if g.ParticipantName != "Bob" {
		t.Fatalf("expected name from identityByRemote, got %q", g.ParticipantName)
	}
}

func TestDetectCaptureGapsHealthyAllAudioNoWarning(t *testing.T) {
	r := &Recorder{
		inCallEver:       map[string]struct{}{"a": {}, "b": {}},
		identityByRemote: map[string]participantIdentity{},
	}
	sessions := []sessionCapture{
		{RemoteSessionID: "a", ParticipantName: "A", AudioPackets: 5000},
		{RemoteSessionID: "b", ParticipantName: "B", AudioPackets: 200},
	}

	if gaps := r.detectCaptureGaps(sessions); len(gaps) != 0 {
		t.Fatalf("healthy participants must produce no gaps, got %+v", gaps)
	}
}

func TestDetectCaptureGapsIgnoresNeverInCall(t *testing.T) {
	// A zero-audio session that was never recorded as in-call (e.g. a stray
	// track) is not a candidate: only sessions in inCallEver are reconciled.
	r := &Recorder{
		inCallEver:       map[string]struct{}{"healthy-session": {}},
		identityByRemote: map[string]participantIdentity{},
	}
	sessions := []sessionCapture{
		{RemoteSessionID: "healthy-session", ParticipantName: "H", AudioPackets: 1000},
		{RemoteSessionID: "stray-session", ParticipantName: "Stray", AudioPackets: 0, VideoPackets: 0},
	}

	if gaps := r.detectCaptureGaps(sessions); len(gaps) != 0 {
		t.Fatalf("a session not in inCallEver must not be flagged, got %+v", gaps)
	}
}

func TestDetectCaptureGapsLowAudioAdvisory(t *testing.T) {
	r := &Recorder{
		inCallEver:       map[string]struct{}{"sliver-session": {}, "sliver-with-video": {}},
		identityByRemote: map[string]participantIdentity{},
	}
	sessions := []sessionCapture{
		{RemoteSessionID: "sliver-session", ParticipantName: "Sliver", AudioPackets: 10, VideoPackets: 0},
		// Same sliver of audio but with video: legitimately recorded, no gap.
		{RemoteSessionID: "sliver-with-video", ParticipantName: "SliverVid", AudioPackets: 10, VideoPackets: 300},
	}

	gaps := r.detectCaptureGaps(sessions)

	if g, ok := gapBySID(gaps, "sliver-session"); !ok || g.Reason != "low-audio" {
		t.Fatalf("expected a low-audio gap for the audio-only sliver, got %+v", gaps)
	}
	if _, ok := gapBySID(gaps, "sliver-with-video"); ok {
		t.Fatalf("a low-audio sliver with video must not be flagged, got %+v", gaps)
	}
	if len(gaps) != 1 {
		t.Fatalf("expected exactly one gap, got %d: %+v", len(gaps), gaps)
	}
}

func TestEnsureSubscriberRecordsInCallEverMonotonically(t *testing.T) {
	r := newInCallSubscribeTestRecorder()

	if err := r.handleParticipantsEvent(map[string]any{
		"changed": []any{
			map[string]any{
				"sessionId":   "alice-session",
				"displayName": "Alice",
				"userId":      "alice",
				"inCall":      float64(3), // in call + publishing audio
			},
		},
	}); err != nil {
		t.Fatalf("handleParticipantsEvent() error = %v", err)
	}
	if _, ok := r.inCallEver["alice-session"]; !ok {
		t.Fatalf("expected the inCall transition to record alice-session in inCallEver")
	}

	// The participant leaving must NOT forget that they were ever in-call:
	// the reconciliation at cleanup needs to flag them if we captured nothing.
	r.removeParticipantSessions([]any{"alice-session"})
	if got := r.subscriberCount(); got != 0 {
		t.Fatalf("expected the leave to remove the subscriber, got %d", got)
	}
	if _, ok := r.inCallEver["alice-session"]; !ok {
		t.Fatalf("inCallEver must be monotonic; alice-session was pruned on leave")
	}
}

func TestWriteReportEmitsCaptureGaps(t *testing.T) {
	tmp := t.TempDir()
	rec := &Recorder{
		cfg: config.Config{
			OutputPath: filepath.Join(tmp, "archive.csr"),
			CallURL:    "https://example.test/call/room",
			GuestName:  "recorder-bot",
		},
		baseURL:         "https://example.test",
		roomToken:       "room",
		finalOutputPath: filepath.Join(tmp, "final.mkv"),
		segmentsDir:     filepath.Join(tmp, "segments"),
		startedAt:       time.Unix(1_700_000_000, 0).UTC(),
	}

	gaps := []captureGap{
		{RemoteSessionID: "ivan-session", ParticipantName: "Ivan", Reason: "no-media", CapturedSession: false},
	}

	reportPath := filepath.Join(tmp, "report.json")
	if err := rec.writeReport(reportPath, nil, true, "", false, nil, gaps); err != nil {
		t.Fatalf("write report: %v", err)
	}

	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parse report json: %v", err)
	}

	if count, ok := report["capture_gap_count"].(float64); !ok || int(count) != 1 {
		t.Fatalf("expected capture_gap_count=1, got %v", report["capture_gap_count"])
	}
	gapList, ok := report["capture_gaps"].([]any)
	if !ok || len(gapList) != 1 {
		t.Fatalf("expected one capture_gaps entry, got %v", report["capture_gaps"])
	}
	first := asMap(gapList[0])
	if first["remote_session_id"] != "ivan-session" || first["reason"] != "no-media" {
		t.Fatalf("unexpected capture_gaps entry: %+v", first)
	}
	warnings, ok := report["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("expected one warning, got %v", report["warnings"])
	}
	if w := asString(warnings[0]); !strings.Contains(w, "in-call participant captured no media at all") || !strings.Contains(w, "Ivan") {
		t.Fatalf("warning missing the capture-gap message: %q", w)
	}
}

func TestWriteReportCaptureGapsEmptyIsNonNullArray(t *testing.T) {
	tmp := t.TempDir()
	rec := &Recorder{
		cfg:             config.Config{OutputPath: filepath.Join(tmp, "a.csr")},
		finalOutputPath: filepath.Join(tmp, "final.mkv"),
		segmentsDir:     filepath.Join(tmp, "segments"),
		startedAt:       time.Unix(1_700_000_000, 0).UTC(),
	}

	reportPath := filepath.Join(tmp, "report.json")
	if err := rec.writeReport(reportPath, nil, true, "", false, nil, nil); err != nil {
		t.Fatalf("write report: %v", err)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	// The key must be an empty array, not null/absent, so consumers can rely on it.
	if !strings.Contains(string(raw), "\"capture_gaps\": []") {
		t.Fatalf("expected capture_gaps to render as an empty array, got:\n%s", raw)
	}
}
