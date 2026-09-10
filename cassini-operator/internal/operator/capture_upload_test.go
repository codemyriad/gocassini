package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

func validSidecar() captureSidecar {
	return captureSidecar{
		Format:          captureSourceFormat,
		RoomToken:       "abc123",
		ParticipantID:   "alice",
		CallStartWallMS: 1_700_000_000_000,
		CallEndWallMS:   1_700_000_060_000,
		Segments: []captureSegment{{
			Index:       0,
			AudioName:   "segment-0.webm",
			MimeType:    "audio/webm;codecs=opus",
			StartWallMS: 1_700_000_000_000,
			StopWallMS:  1_700_000_060_000,
			Anchors: []captureAnchor{
				{FrameIndex: 0, RTPTimestamp: 4_000_000, SSRC: 42, WallMS: 1_700_000_000_100},
			},
		}},
	}
}

func TestValidateSidecar(t *testing.T) {
	t.Run("accepts a well-formed sidecar", func(t *testing.T) {
		sidecar := validSidecar()
		if err := validateSidecar(&sidecar); err != nil {
			t.Fatalf("validateSidecar: %v", err)
		}
	})

	cases := []struct {
		name   string
		mutate func(*captureSidecar)
		want   string
	}{
		{"rejects an unknown format", func(s *captureSidecar) { s.Format = "org.cassini.source-capture/9" }, "unsupported capture format"},
		{"rejects an empty room token", func(s *captureSidecar) { s.RoomToken = "" }, "invalid room token"},
		{"rejects a traversing room token", func(s *captureSidecar) { s.RoomToken = "../../etc" }, "invalid room token"},
		{"rejects no segments", func(s *captureSidecar) { s.Segments = nil }, "no segments"},
		{"rejects a traversing segment name", func(s *captureSidecar) { s.Segments[0].AudioName = "../escape.webm" }, "invalid segment name"},
		{"rejects an absolute segment name", func(s *captureSidecar) { s.Segments[0].AudioName = "/etc/passwd" }, "invalid segment name"},
		{"rejects a backwards call window", func(s *captureSidecar) { s.CallEndWallMS = s.CallStartWallMS - 1 }, "invalid call window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := validSidecar()
			tc.mutate(&sidecar)
			err := validateSidecar(&sidecar)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateSidecar error = %v, want containing %q", err, tc.want)
			}
		})
	}

	t.Run("rejects duplicate segment names", func(t *testing.T) {
		sidecar := validSidecar()
		sidecar.Segments = append(sidecar.Segments, sidecar.Segments[0])
		if err := validateSidecar(&sidecar); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("validateSidecar error = %v, want duplicate", err)
		}
	})

	t.Run("rejects an unreasonable segment count", func(t *testing.T) {
		sidecar := validSidecar()
		for i := 1; i <= captureMaxSegments; i++ {
			sidecar.Segments = append(sidecar.Segments, captureSegment{
				Index: i, AudioName: fmt.Sprintf("segment-%d.webm", i),
			})
		}
		if err := validateSidecar(&sidecar); err == nil || !strings.Contains(err.Error(), "too many") {
			t.Fatalf("validateSidecar error = %v, want too many", err)
		}
	})
}

func TestCaptureUploadDir(t *testing.T) {
	got := captureUploadDir("/data/capture", "abc123", "alice", 1700)
	if want := filepath.Join("/data/capture", "abc123", "alice", "1700"); got != want {
		t.Fatalf("captureUploadDir = %q, want %q", got, want)
	}
}

// uploadRequest builds a multipart source-capture upload.
func uploadRequest(t *testing.T, sidecar captureSidecar, segments map[string][]byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("sidecar", captureSidecarName)
	if err != nil {
		t.Fatalf("create sidecar part: %v", err)
	}
	if err := json.NewEncoder(part).Encode(sidecar); err != nil {
		t.Fatalf("encode sidecar: %v", err)
	}
	i := 0
	for name, content := range segments {
		// Distinct field names, as the payload sends them. See
		// TestCaptureUploadIdentifiesSegmentsByFileName for why.
		filePart, err := writer.CreateFormFile(fmt.Sprintf("segment_%d", i), name)
		i++
		if err != nil {
			t.Fatalf("create segment part: %v", err)
		}
		if _, err := filePart.Write(content); err != nil {
			t.Fatalf("write segment: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/capture/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func captureTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	// Pinned on rather than left to the default. Source capture is on by
	// default on this branch, but a test of the intake path must not be
	// silently re-aimed by a change to that default: it says what it needs.
	t.Setenv(envSourceCaptureEnabled, "1")
	// The free-space floor is checked against the real volume the temp
	// directory sits on. A CI host whose /tmp is smaller than the floor would
	// fail every one of these with a 507 that has nothing to do with what they
	// assert, so the volume is made roomy here; the tests that mean to exercise
	// the floor stub it again with the figure they want.
	stubCaptureFreeBytes(t, 64<<30)
	return &Runtime{cfg: Config{CaptureRoot: filepath.Join(t.TempDir(), "capture")}}
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestCaptureUploadHandlerRejectsUnauthenticated(t *testing.T) {
	rt := captureTestRuntime(t)
	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCaptureUploadHandlerRejectsNonMethod(t *testing.T) {
	rt := captureTestRuntime(t)
	req := httptest.NewRequest(http.MethodGet, "/capture/upload", nil)
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestCaptureUploadHandlerStoresUnderAuthenticatedOwner(t *testing.T) {
	rt := captureTestRuntime(t)
	sidecar := validSidecar()
	req := uploadRequest(t, sidecar, map[string][]byte{"segment-0.webm": []byte("audio-bytes")})
	// The client claims to be "alice"; the authenticated caller is "bob". The
	// authenticated identity is what the recording must be filed under —
	// PARTICIPANT_ID in the MKV comes from the same Talk user id, so believing
	// the body would let anyone attach audio to somebody else's track.
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s, want 202", rec.Code, rec.Body.String())
	}
	dir := captureUploadDir(rt.cfg.CaptureRoot, "abc123", "bob", sidecar.CallStartWallMS)
	audio, err := os.ReadFile(filepath.Join(dir, "segment-0.webm"))
	if err != nil {
		t.Fatalf("read stored segment: %v", err)
	}
	if string(audio) != "audio-bytes" {
		t.Fatalf("stored segment = %q", audio)
	}
	raw, err := os.ReadFile(filepath.Join(dir, captureSidecarName))
	if err != nil {
		t.Fatalf("read stored sidecar: %v", err)
	}
	var stored captureSidecar
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("parse stored sidecar: %v", err)
	}
	if stored.OwnerUserID != "bob" {
		t.Fatalf("stored owner = %q, want bob (the authenticated caller, not the claimed participantId)", stored.OwnerUserID)
	}
	if stored.ReceivedAt == "" {
		t.Fatal("stored sidecar has no ReceivedAt")
	}
	if len(stored.Segments[0].Anchors) != 1 || stored.Segments[0].Anchors[0].RTPTimestamp != 4_000_000 {
		t.Fatalf("RTP anchors did not survive intake: %+v", stored.Segments[0].Anchors)
	}
}

func TestCaptureUploadHandlerRejectsNonParticipant(t *testing.T) {
	rt := captureTestRuntime(t)
	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "mallory"))
	rec := httptest.NewRecorder()

	notAMember := func(context.Context, string, string) (bool, error) { return false, nil }
	rt.captureUploadHandler(notAMember, quietLogger())(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	entries, err := os.ReadDir(rt.cfg.CaptureRoot)
	if err != nil {
		t.Fatalf("read capture root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected upload left %d entries behind", len(entries))
	}
}

func TestCaptureUploadHandlerRejectsMissingSegment(t *testing.T) {
	rt := captureTestRuntime(t)
	// Sidecar promises a segment the body never sends.
	req := uploadRequest(t, validSidecar(), map[string][]byte{})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCaptureUploadHandlerReplacesEarlierUploadOfSameCall(t *testing.T) {
	rt := captureTestRuntime(t)
	sidecar := validSidecar()
	logger := quietLogger()

	for _, content := range []string{"first", "second"} {
		req := uploadRequest(t, sidecar, map[string][]byte{"segment-0.webm": []byte(content)})
		req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
		rec := httptest.NewRecorder()
		rt.captureUploadHandler(nil, logger)(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
	}

	dir := captureUploadDir(rt.cfg.CaptureRoot, "abc123", "bob", sidecar.CallStartWallMS)
	audio, err := os.ReadFile(filepath.Join(dir, "segment-0.webm"))
	if err != nil {
		t.Fatalf("read stored segment: %v", err)
	}
	if string(audio) != "second" {
		t.Fatalf("retry did not replace the earlier upload: %q", audio)
	}
}

func TestValidateSidecarReservesTheSidecarName(t *testing.T) {
	// A segment claiming the sidecar's own name would be written and then
	// overwritten by the manifest, losing that audio with no trace.
	sidecar := validSidecar()
	sidecar.Segments[0].AudioName = captureSidecarName
	if err := validateSidecar(&sidecar); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("validateSidecar error = %v, want reserved", err)
	}
}

func TestCaptureUploadHandlerRejectsAReservedSegmentPart(t *testing.T) {
	rt := captureTestRuntime(t)
	// The sidecar is well-formed; the multipart body sneaks the reserved name.
	req := uploadRequest(t, validSidecar(), map[string][]byte{captureSidecarName: []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCaptureUploadHandlerReportsOversizeAsTooLarge(t *testing.T) {
	rt := captureTestRuntime(t)
	// Lower the ceiling rather than move half a gigabyte through the handler.
	original := captureMaxUploadBytes
	// Large enough for the sidecar, far too small for the segment: the limit
	// should be reported by the part that actually exceeds it.
	captureMaxUploadBytes = 2048
	t.Cleanup(func() { captureMaxUploadBytes = original })

	req := uploadRequest(t, validSidecar(), map[string][]byte{
		"segment-0.webm": bytes.Repeat([]byte("x"), 16384),
	})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (an oversize upload is not a malformed one)", rec.Code)
	}
	// And nothing of an over-cap upload is kept.
	staged, _ := filepath.Glob(filepath.Join(rt.cfg.CaptureRoot, "upload-*"))
	if len(staged) != 0 {
		t.Fatalf("an oversize upload left staging behind: %v", staged)
	}
}

func TestPromoteCaptureKeepsThePreviousUploadWhenTheSwapFails(t *testing.T) {
	rt := captureTestRuntime(t)
	final := filepath.Join(rt.cfg.CaptureRoot, "room", "bob", "1700")
	if err := os.MkdirAll(final, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(final, "segment-0.webm"), []byte("the good one"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Staging does not exist, so the promotion rename fails. The participant's
	// previous audio must survive that — they may well have deleted their only
	// other copy.
	_, err := rt.promoteCapture(&captureSidecar{}, filepath.Join(rt.cfg.CaptureRoot, "no-such-staging"), final)
	if err == nil {
		t.Fatal("expected the promotion to fail")
	}
	kept, readErr := os.ReadFile(filepath.Join(final, "segment-0.webm"))
	if readErr != nil {
		t.Fatalf("the previous upload was destroyed by a failed promotion: %v", readErr)
	}
	if string(kept) != "the good one" {
		t.Fatalf("previous upload = %q", kept)
	}
	if entries, _ := filepath.Glob(final + ".superseded"); len(entries) != 0 {
		t.Fatal("a superseded directory was left behind")
	}
}

// A browser that reloads mid-recording resumes its buffer, so its one upload
// for that call describes MORE of it than any earlier copy of the prefix. If a
// stale copy of that prefix reaches the endpoint afterwards — a second tab, a
// request the network reordered — last-writer-wins would replace the whole
// recording with its own first half, and the sweep would then delete the rest.
func TestPromoteCaptureKeepsTheLongerStoredCapture(t *testing.T) {
	rt := captureTestRuntime(t)
	final := filepath.Join(rt.cfg.CaptureRoot, "room", "bob", "1700")
	staging := filepath.Join(rt.cfg.CaptureRoot, "upload-stale")
	for _, dir := range []string{final, staging} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	stored := captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 9000,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
			{Index: 1, AudioName: "segment-1.webm", StartWallMS: 4000, StopWallMS: 6000},
			{Index: 2, AudioName: "segment-2.webm", StartWallMS: 6000, StopWallMS: 9000},
		},
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(final, captureSidecarName), raw, 0o640); err != nil {
		t.Fatalf("write stored sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(final, "segment-2.webm"), []byte("after the reload"), 0o640); err != nil {
		t.Fatalf("write stored segment: %v", err)
	}

	// The prefix: the same call, fewer segments, ending earlier.
	prefix := &captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 4000,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
		},
	}
	outcome, err := rt.promoteCapture(prefix, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome != captureAlreadyStored {
		t.Fatalf("a stale prefix got outcome %d; it is a subset of what is stored", outcome)
	}
	if _, err := os.Stat(filepath.Join(final, "segment-2.webm")); err != nil {
		t.Fatalf("the post-reload audio was destroyed by a stale prefix upload: %v", err)
	}

	// Segment COUNT is not a measure of audio. A stale snapshot of a live
	// one-segment capture has the same count as the finished one and a
	// fraction of its seconds, so the window is what decides.
	sameCount := &captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 7000,
		Segments: []captureSegment{
			// The sealed segments are identical — a snapshot cuts at the same
			// boundaries — and only the live one is short.
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
			{Index: 1, AudioName: "segment-1.webm", StartWallMS: 4000, StopWallMS: 6000},
			{Index: 2, AudioName: "segment-2.webm", StartWallMS: 6000, StopWallMS: 7000},
		},
	}
	outcome, err = rt.promoteCapture(sameCount, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome != captureAlreadyStored {
		t.Fatalf("a stale snapshot with the same segment count got outcome %d", outcome)
	}

	// A capture that reaches at least as far but is MISSING one of the stored
	// segments would be dropping that audio, whatever its call window says.
	// Two pages that both resumed one prefix can diverge like this.
	divergent := &captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 9500,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
			{Index: 1, AudioName: "segment-1.webm", StartWallMS: 4000, StopWallMS: 6000},
		},
	}
	outcome, err = rt.promoteCapture(divergent, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome != captureAlreadyStored {
		t.Fatalf("an upload missing a stored segment got outcome %d; it holds nothing new", outcome)
	}

	// Two racing pages that each hold a segment the other does not. Neither may
	// replace the other, and accepting either is what makes a browser delete
	// the only copy of the audio only it has — so this one is refused
	// retryably rather than answered "accepted".
	diverged := &captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 9500,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
			{Index: 3, AudioName: "segment-3.webm", StartWallMS: 9000, StopWallMS: 9500},
		},
	}
	outcome, err = rt.promoteCapture(diverged, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome != captureDiverged {
		t.Fatalf("a capture holding audio the server does not got outcome %d; accepting it destroys that audio", outcome)
	}

	// The same names and a later call end, but one segment covering LESS of the
	// call than the stored one does. Names and the call window each say this is
	// a superset; the segment's own span says it is not, and the audio between
	// the two spans exists only in what is stored.
	narrower := &captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 9500,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 3000},
			{Index: 1, AudioName: "segment-1.webm", StartWallMS: 3000, StopWallMS: 6000},
			{Index: 2, AudioName: "segment-2.webm", StartWallMS: 6000, StopWallMS: 9500},
		},
	}
	outcome, err = rt.promoteCapture(narrower, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome == capturePromoted {
		t.Fatal("an upload whose segment covers less of the call than the stored one replaced it")
	}

	// The manifests agree on everything and the FILES do not. Recovery sidecars
	// are checkpointed, so two uploads for one call can carry the same account
	// of a segment that was still growing; the staler snapshot must not replace
	// the fuller one on the strength of metadata that compares equal.
	for name, size := range map[string]int{
		"segment-0.webm": 4000,
		"segment-1.webm": 4000,
		"segment-2.webm": 4000,
	} {
		if err := os.WriteFile(filepath.Join(final, name), make([]byte, size), 0o640); err != nil {
			t.Fatalf("write stored %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(staging, name), make([]byte, size/2), 0o640); err != nil {
			t.Fatalf("write staged %s: %v", name, err)
		}
	}
	staleSnapshot := stored
	outcome, err = rt.promoteCapture(&staleSnapshot, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome == capturePromoted {
		t.Fatal("a staler snapshot of the same segments replaced the fuller stored files")
	}
	for name := range map[string]int{"segment-0.webm": 0, "segment-1.webm": 0, "segment-2.webm": 0} {
		if err := os.Remove(filepath.Join(staging, name)); err != nil {
			t.Fatalf("clean staged %s: %v", name, err)
		}
	}

	// An ordinary re-upload — the same segments, no shorter — still replaces.
	again := stored
	again.CallEndWallMS = 9500
	outcome, err = rt.promoteCapture(&again, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome != capturePromoted {
		t.Fatalf("an ordinary re-upload of the same call got outcome %d", outcome)
	}
}

// promoteCapture moves the previous capture aside before it swaps, so a crash
// between those two renames leaves the whole recording under `.superseded` and
// nothing at the live path. A stale prefix arriving afterwards would find no
// stored capture to compare against, promote itself, and let the sweep delete
// the longer copy the set-aside exists to protect.
func TestPromoteCaptureConsultsTheSetAsideCopy(t *testing.T) {
	rt := captureTestRuntime(t)
	final := filepath.Join(rt.cfg.CaptureRoot, "room", "bob", "1700")
	setAside := final + captureSupersededSuffix
	staging := filepath.Join(rt.cfg.CaptureRoot, "upload-stale")
	for _, dir := range []string{setAside, staging} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	whole := captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 9000,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
		},
	}
	raw, err := json.Marshal(whole)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(setAside, captureSidecarName), raw, 0o640); err != nil {
		t.Fatalf("write set-aside sidecar: %v", err)
	}

	prefix := &captureSidecar{
		Format: captureSourceFormat, RoomToken: "room", CallStartWallMS: 1700, CallEndWallMS: 4000,
		Segments: []captureSegment{
			{Index: 0, AudioName: "segment-0.webm", StartWallMS: 1700, StopWallMS: 4000},
		},
	}
	outcome, err := rt.promoteCapture(prefix, staging, final)
	if err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}
	if outcome == capturePromoted {
		t.Fatal("a stale prefix promoted itself over a set-aside capture holding more of the call")
	}
	// And the interrupted promotion is finished, so the capture that was kept
	// is one a build can actually find: discovery ignores the `.superseded`
	// name by design, and a capture left there is retained and unreachable.
	if _, err := os.Stat(filepath.Join(final, captureSidecarName)); err != nil {
		t.Fatalf("the set-aside capture was kept where no build can discover it: %v", err)
	}
	if _, err := os.Stat(setAside); !os.IsNotExist(err) {
		t.Fatalf("the set-aside directory survived its own restoration (err=%v)", err)
	}
}

// The name promoteCapture gives a set-aside upload is a cross-module contract.
// The build's capture discovery lives in a different Go module
// (cassini-go-recorder, internal/transcribe/sourceaudio.go) and cannot import a
// constant from here, so it filters on the literal ".superseded". A directory
// at that depth which discovery does not recognise is found as a second
// capture for the same speaker and summed onto the timeline: the same speech
// twice, at double amplitude, with the recorded track already suppressed.
//
// This pins the name by observing it. promoteCapture clears any stale
// set-aside directory before it renames, so a leftover under the expected name
// must be gone afterwards. Change the suffix and this fails.
func TestSupersededSuffixIsTheNameDiscoveryFiltersOn(t *testing.T) {
	// The literal cassini-go-recorder greps for. Keep the two in step.
	const discoveryFiltersOn = ".superseded"

	root := t.TempDir()
	final := filepath.Join(root, "1700")
	staging := filepath.Join(root, "upload-xyz")
	stale := final + discoveryFiltersOn
	for _, dir := range []string{final, staging, stale} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// A marker so a surviving directory is unmistakably the stale one.
	if err := os.WriteFile(filepath.Join(stale, "marker"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	rt := &Runtime{}
	if _, err := rt.promoteCapture(&captureSidecar{}, staging, final); err != nil {
		t.Fatalf("promoteCapture: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a directory named %q survived promotion (err=%v). Either promoteCapture no longer uses that suffix, or it stopped clearing a stale one; in both cases cassini-go-recorder's discovery filter in internal/transcribe/sourceaudio.go must be updated in the same change", filepath.Base(stale), err)
	}
}

func TestCaptureUploadWritesTheSidecarBeforePromoting(t *testing.T) {
	// A promoted directory must never exist without its manifest:
	// DiscoverSourceCaptures reads the sidecar to decide a capture exists at
	// all, so a directory without one is indistinguishable from a truncated
	// upload.
	rt := captureTestRuntime(t)
	sidecar := validSidecar()
	req := uploadRequest(t, sidecar, map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d", rec.Code)
	}

	dir := captureUploadDir(rt.cfg.CaptureRoot, "abc123", "bob", sidecar.CallStartWallMS)
	if _, err := os.Stat(filepath.Join(dir, captureSidecarName)); err != nil {
		t.Fatalf("promoted directory has no sidecar: %v", err)
	}
	// And nothing is left staged.
	staged, _ := filepath.Glob(filepath.Join(rt.cfg.CaptureRoot, "upload-*"))
	if len(staged) != 0 {
		t.Fatalf("staging directories left behind: %v", staged)
	}
}

// The sidecar is the client's account of its own recording. It is not trusted
// for identity, but it IS the input to placement, so a self-contradicting one
// must be refused rather than produce a confident wrong answer about where
// somebody's words belong.
func TestValidateSidecarRejectsSelfContradiction(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*captureSidecar)
		want   string
	}{
		{
			"duplicate segment indices",
			func(s *captureSidecar) {
				second := s.Segments[0]
				second.AudioName = "segment-1.webm"
				s.Segments = append(s.Segments, second)
			},
			"duplicate segment index",
		},
		{
			"a negative segment index",
			func(s *captureSidecar) { s.Segments[0].Index = -1 },
			"negative",
		},
		{
			"a backwards segment window",
			func(s *captureSidecar) { s.Segments[0].StopWallMS = s.Segments[0].StartWallMS - 1 },
			"invalid window",
		},
		{
			"a segment recorded outside its own call",
			func(s *captureSidecar) {
				s.Segments[0].StartWallMS = s.CallStartWallMS - 60_000
				s.Segments[0].StopWallMS = s.CallStartWallMS - 30_000
			},
			"outside the call window",
		},
		{
			"anchors out of order",
			func(s *captureSidecar) {
				s.Segments[0].Anchors = []captureAnchor{
					{FrameIndex: 50, RTPTimestamp: 5000, WallMS: s.Segments[0].StartWallMS + 100},
					{FrameIndex: 10, RTPTimestamp: 6000, WallMS: s.Segments[0].StartWallMS + 200},
				}
			},
			"out-of-order anchors",
		},
		{
			"anchors going back in time",
			func(s *captureSidecar) {
				s.Segments[0].Anchors = []captureAnchor{
					{FrameIndex: 10, RTPTimestamp: 5000, WallMS: s.Segments[0].StartWallMS + 900},
					{FrameIndex: 20, RTPTimestamp: 6000, WallMS: s.Segments[0].StartWallMS + 100},
				}
			},
			"back in time",
		},
		{
			"an anchor outside its segment",
			func(s *captureSidecar) {
				s.Segments[0].Anchors[0].WallMS = s.Segments[0].StopWallMS + 60_000
			},
			"outside its own window",
		},
		{
			"an out-of-range RTP timestamp",
			func(s *captureSidecar) { s.Segments[0].Anchors[0].RTPTimestamp = 1 << 33 },
			"out-of-range RTP",
		},
		{
			"a backwards mute interval",
			func(s *captureSidecar) { s.Segments[0].MuteIntervals = [][2]int64{{500, 100}} },
			"backwards mute",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := validSidecar()
			tc.mutate(&sidecar)
			err := validateSidecar(&sidecar)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateSidecar error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateSidecarAcceptsARealisticMultiSegmentCapture(t *testing.T) {
	// What the client actually produces after a device change mid-call: two
	// segments, a continuing frame counter, mute intervals, sampled anchors.
	sidecar := validSidecar()
	start := sidecar.CallStartWallMS
	sidecar.Segments = []captureSegment{
		{
			Index: 0, AudioName: "segment-0.webm", MimeType: "audio/webm;codecs=opus",
			StartWallMS: start, StopWallMS: start + 20_000,
			Anchors: []captureAnchor{
				{FrameIndex: 0, RTPTimestamp: 1_000_000, SSRC: 7, WallMS: start + 20},
				{FrameIndex: 50, RTPTimestamp: 1_048_000, SSRC: 7, WallMS: start + 1020},
			},
			MuteIntervals: [][2]int64{{start + 5_000, start + 8_000}},
		},
		{
			Index: 1, AudioName: "segment-1.webm", MimeType: "audio/webm;codecs=opus",
			StartWallMS: start + 20_000, StopWallMS: start + 40_000,
			Anchors: []captureAnchor{
				// A new SSRC after the renegotiation, and the frame counter
				// continues across the seam because the transform outlives the
				// track.
				{FrameIndex: 1000, RTPTimestamp: 90_000, SSRC: 9, WallMS: start + 20_100},
			},
		},
	}
	sidecar.CallEndWallMS = start + 40_000
	if err := validateSidecar(&sidecar); err != nil {
		t.Fatalf("a realistic two-segment capture was rejected: %v", err)
	}
}

func TestCaptureUploadIsRefusedWhenAnAdministratorOptsOut(t *testing.T) {
	rt := captureTestRuntime(t)
	// A stale client from before the feature was turned off must still be
	// unable to store anything.
	t.Setenv(envSourceCaptureEnabled, "0")

	req := uploadRequest(t, validSidecar(), map[string][]byte{"segment-0.webm": []byte("audio")})
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))
	rec := httptest.NewRecorder()

	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if _, err := os.Stat(rt.cfg.CaptureRoot); err == nil {
		t.Fatal("a refused upload created the capture root")
	}
}

// A segment is identified by its FILE name, never by its form field name.
//
// The AppAPI proxy does not stream the body through. It rebuilds it from PHP's
// $_POST/$_FILES, and PHP keeps only the LAST file for a repeated field name,
// and rewrites characters it dislikes in the names it does keep. A handler that
// switched on the field name therefore lost every segment but one on a real
// install and refused the upload as incomplete — for the ordinary case of a
// participant changing microphone mid-call, which is exactly when a second
// segment is cut. Reproduced against real AppAPI as
// `400: missing segment "segment-0.webm"`.
//
// All three shapes must store both segments: what the client sends now, what an
// older client still open in a browser sends, and what the proxy may hand over
// after rewriting the names.
func TestCaptureUploadIdentifiesSegmentsByFileName(t *testing.T) {
	shapes := []struct {
		name       string
		fieldNames []string
	}{
		{"distinct names, as the client sends them", []string{"segment_0", "segment_1"}},
		{"one repeated name, as an older client sends", []string{"segments", "segments"}},
		{"names rewritten in transit", []string{"segment_0_webm", "file2"}},
	}

	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			sidecar := validSidecar()
			second := sidecar.Segments[0]
			second.Index = 1
			second.AudioName = "segment-1.webm"
			sidecar.Segments = append(sidecar.Segments, second)

			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("sidecar", captureSidecarName)
			if err != nil {
				t.Fatalf("create sidecar part: %v", err)
			}
			if err := json.NewEncoder(part).Encode(sidecar); err != nil {
				t.Fatalf("encode sidecar: %v", err)
			}
			for i, field := range shape.fieldNames {
				filePart, err := writer.CreateFormFile(field, sidecar.Segments[i].AudioName)
				if err != nil {
					t.Fatalf("create segment part: %v", err)
				}
				if _, err := fmt.Fprintf(filePart, "audio-%d", i); err != nil {
					t.Fatalf("write segment: %v", err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("close writer: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/capture/upload", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))

			rt := captureTestRuntime(t)
			rec := httptest.NewRecorder()
			rt.captureUploadHandler(nil, quietLogger())(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			dir := captureUploadDir(rt.cfg.CaptureRoot, sidecar.RoomToken, "bob", sidecar.CallStartWallMS)
			for i, segment := range sidecar.Segments {
				got, err := os.ReadFile(filepath.Join(dir, segment.AudioName))
				if err != nil {
					t.Fatalf("%s was not stored: %v", segment.AudioName, err)
				}
				if want := fmt.Sprintf("audio-%d", i); string(got) != want {
					t.Fatalf("%s holds %q, want %q", segment.AudioName, got, want)
				}
			}
		})
	}
}

// A part carrying no file name, under a field the server does not know, stays
// ignored — so a newer client can add fields without breaking an older server.
func TestCaptureUploadStillIgnoresUnknownNonFileFields(t *testing.T) {
	sidecar := validSidecar()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("sidecar", captureSidecarName)
	if err != nil {
		t.Fatalf("create sidecar part: %v", err)
	}
	if err := json.NewEncoder(part).Encode(sidecar); err != nil {
		t.Fatalf("encode sidecar: %v", err)
	}
	if err := writer.WriteField("clientVersion", "9.9.9"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	filePart, err := writer.CreateFormFile("segment_0", "segment-0.webm")
	if err != nil {
		t.Fatalf("create segment part: %v", err)
	}
	if _, err := filePart.Write([]byte("audio")); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/capture/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))

	rt := captureTestRuntime(t)
	rec := httptest.NewRecorder()
	rt.captureUploadHandler(nil, quietLogger())(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	dir := captureUploadDir(rt.cfg.CaptureRoot, sidecar.RoomToken, "bob", sidecar.CallStartWallMS)
	if _, err := os.Stat(filepath.Join(dir, "clientVersion")); !os.IsNotExist(err) {
		t.Fatalf("a non-file field was stored as a segment (err=%v)", err)
	}
}

// A part carrying a file name the sidecar never declared must be refused, not
// stored. Parts are classified as segments by carrying a file name at all, so
// without this an undeclared file would be written into the capture directory
// and promoted alongside the real audio.
func TestCaptureUploadRejectsAnUndeclaredSegment(t *testing.T) {
	sidecar := validSidecar()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("sidecar", captureSidecarName)
	if err != nil {
		t.Fatalf("create sidecar part: %v", err)
	}
	if err := json.NewEncoder(part).Encode(sidecar); err != nil {
		t.Fatalf("encode sidecar: %v", err)
	}
	for _, name := range []string{"segment-0.webm", "stowaway.webm"} {
		filePart, err := writer.CreateFormFile("segment_"+name, name)
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := filePart.Write([]byte("audio")); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/capture/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))

	rt := captureTestRuntime(t)
	rec := httptest.NewRecorder()
	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stowaway.webm") {
		t.Fatalf("refusal should name the undeclared file: %s", rec.Body.String())
	}
	dir := captureUploadDir(rt.cfg.CaptureRoot, sidecar.RoomToken, "bob", sidecar.CallStartWallMS)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused upload must not be promoted (err=%v)", err)
	}
}

// A body truncated in transit is a property of the delivery, not of the
// capture, so it must not answer with a status the client treats as terminal.
// The client deletes its only copy on 400; a proxy cutting a request short
// would then destroy an intact recording.
func TestCaptureUploadReportsTruncationAsRetryable(t *testing.T) {
	sidecar := validSidecar()
	full := uploadRequest(t, sidecar, map[string][]byte{"segment-0.webm": []byte("audio-bytes")})
	raw, err := io.ReadAll(full.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/capture/upload", bytes.NewReader(raw[:len(raw)/2]))
	req.Header.Set("Content-Type", full.Header.Get("Content-Type"))
	req = req.WithContext(appapi.WithUserID(context.Background(), "bob"))

	rt := captureTestRuntime(t)
	rec := httptest.NewRecorder()
	rt.captureUploadHandler(nil, quietLogger())(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 so the client keeps its buffer; body=%s", rec.Code, rec.Body.String())
	}
}
