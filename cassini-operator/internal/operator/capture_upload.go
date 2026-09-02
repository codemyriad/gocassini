package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"cassini-operator/internal/operator/appapi"
)

// Source-capture upload intake.
//
// A participant's browser records its own microphone before Opus encoding and
// before the network (cassini-app/src/capture/), and posts the result here once
// the call has ended. This endpoint only receives and stores; consuming the
// audio — placing it on the meeting timeline through the RTP anchors in the
// sidecar and rebuilding the transcript from it — is a separate stage.
//
// Trust model. The uploaded audio is user-supplied content that is intended to
// end up as speaker-attributed text everybody in the room can read, so the two
// facts that matter are decided here and never taken from the request body:
//
//   - WHO. The authenticated AppAPI caller is stamped as the owner. The client
//     also states a participantId; it is recorded only so a mismatch can be
//     logged. That authenticated id is the same value the recorder writes into
//     each MKV audio track as PARTICIPANT_ID (it comes from Talk's `userid`),
//     which is what lets a later stage join an upload to a track exactly rather
//     than by matching names.
//   - WHETHER. The caller must be a participant of the room they are uploading
//     for, checked against Talk rather than believed.
//
// What is deliberately NOT decided here is whether the audio is genuine. That
// needs the recorder's own copy of the same speaker to compare against, and it
// belongs with the stage that does the placement.

const (
	// captureMaxSegments bounds segment count. Segments are cut on track
	// replacement (device switches), so a healthy call has one or two and a
	// pathological one has a handful.
	captureMaxSegments = 64

	captureSidecarName = "capture.json"

	// captureSidecarField is the one form field name that carries meaning. It
	// is a single part, so the proxy cannot collapse it, and it is plain ASCII
	// with no character PHP rewrites.
	captureSidecarField = "sidecar"
	// captureSegmentPart is an internal label for "this part is a segment",
	// decided by the part carrying a file name rather than by its field name.
	// See the classification comment in the read loop.
	captureSegmentPart = "\x00segment"

	// captureSourceFormat must match SOURCE_CAPTURE_FORMAT in
	// cassini-app/src/capture/protocol.ts. Rejecting an unknown value is how a
	// breaking client change is fenced off from an older server.
	captureSourceFormat = "org.cassini.source-capture/1"

	captureMembershipTimeout = 15 * time.Second
)

// captureMaxUploadBytes bounds one call's upload. An hour of 128 kbit/s mono
// Opus is ~57 MB; the ceiling leaves room for a long meeting split across
// several segments without letting a single request fill the volume. A var
// rather than a const so a test can exercise the limit without moving half a
// gigabyte through the handler.
var captureMaxUploadBytes int64 = 512 << 20

// captureSafeName rejects anything that is not a plain file name. Segment names
// arrive from the browser and are used to build paths.
var captureSafeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// captureAnchor ties one outgoing encoded frame to wall-clock time.
// RTPTimestamp is the participant's own 48 kHz sample clock, and is NOT the
// value the recorder logs for the same audio — Janus rewrites the timestamps it
// relays to each subscriber. The pair's use is the RATE (this machine's
// sound-card drift against its wall clock), which is immune to loss because
// these describe frames the client encoded. This endpoint only stores them; the
// build decides what they mean.
type captureAnchor struct {
	FrameIndex   int64 `json:"frameIndex"`
	RTPTimestamp int64 `json:"rtpTimestamp"`
	SSRC         int64 `json:"ssrc"`
	WallMS       int64 `json:"wallMs"`
}

type captureSegment struct {
	Index         int             `json:"index"`
	AudioName     string          `json:"audioName"`
	MimeType      string          `json:"mimeType"`
	StartWallMS   int64           `json:"startWallMs"`
	StopWallMS    int64           `json:"stopWallMs"`
	SampleRate    *int            `json:"sampleRate"`
	ChannelCount  *int            `json:"channelCount"`
	Anchors       []captureAnchor `json:"anchors"`
	MuteIntervals [][2]int64      `json:"muteIntervals"`
}

type captureSidecar struct {
	Format          string           `json:"format"`
	RoomToken       string           `json:"roomToken"`
	ParticipantID   string           `json:"participantId"`
	CallStartWallMS int64            `json:"callStartWallMs"`
	CallEndWallMS   int64            `json:"callEndWallMs"`
	UserAgent       string           `json:"userAgent"`
	Segments        []captureSegment `json:"segments"`
	// OwnerUserID is stamped by the server from the authenticated caller. It is
	// never read from the client's payload.
	OwnerUserID string `json:"ownerUserId"`
	// ReceivedAt records intake time, so a retention sweep can act on uploads
	// whose meeting never materialised.
	ReceivedAt string `json:"receivedAt"`
}

// validateSidecar checks everything decidable without touching the filesystem.
// Split out so the rules are unit-testable without a multipart request.
func validateSidecar(sidecar *captureSidecar) error {
	if sidecar.Format != captureSourceFormat {
		return fmt.Errorf("unsupported capture format %q", sidecar.Format)
	}
	if !captureSafeName.MatchString(sidecar.RoomToken) {
		return fmt.Errorf("invalid room token")
	}
	if len(sidecar.Segments) == 0 {
		return fmt.Errorf("no segments")
	}
	if len(sidecar.Segments) > captureMaxSegments {
		return fmt.Errorf("too many segments (%d)", len(sidecar.Segments))
	}
	if sidecar.CallStartWallMS <= 0 || sidecar.CallEndWallMS < sidecar.CallStartWallMS {
		return fmt.Errorf("invalid call window")
	}
	// Everything below is the client's account of its own recording. None of it
	// is trusted for identity or authorisation — those are decided from the
	// authenticated caller — but it IS the input to placement, and a sidecar
	// that contradicts itself would produce a confident, wrong answer about
	// where somebody's words belong. Contradictions are cheaper to refuse here
	// than to reason about in the build.
	seen := make(map[string]struct{}, len(sidecar.Segments))
	seenIndex := make(map[int]struct{}, len(sidecar.Segments))
	for _, segment := range sidecar.Segments {
		if segment.Index < 0 {
			return fmt.Errorf("segment index %d is negative", segment.Index)
		}
		if _, dup := seenIndex[segment.Index]; dup {
			return fmt.Errorf("duplicate segment index %d", segment.Index)
		}
		seenIndex[segment.Index] = struct{}{}
		if segment.StartWallMS <= 0 || segment.StopWallMS < segment.StartWallMS {
			return fmt.Errorf("segment %d has an invalid window", segment.Index)
		}
		// A segment cannot have been recorded outside the call it belongs to.
		// One second of slack on each side: the client stamps these from its
		// own clock at slightly different moments in the teardown.
		const segmentWindowSlackMS = 1000
		if segment.StartWallMS < sidecar.CallStartWallMS-segmentWindowSlackMS ||
			segment.StopWallMS > sidecar.CallEndWallMS+segmentWindowSlackMS {
			return fmt.Errorf("segment %d falls outside the call window", segment.Index)
		}
		var lastFrame int64 = -1
		var lastWall int64 = -1
		for _, anchor := range segment.Anchors {
			// Anchors are sampled from a monotonic frame counter, so they must
			// arrive in order. Out-of-order anchors mean a rebuilt or spliced
			// sidecar, not a lossy network.
			if anchor.FrameIndex <= lastFrame {
				return fmt.Errorf("segment %d has out-of-order anchors", segment.Index)
			}
			lastFrame = anchor.FrameIndex
			if anchor.WallMS < lastWall {
				return fmt.Errorf("segment %d has anchors going back in time", segment.Index)
			}
			lastWall = anchor.WallMS
			if anchor.WallMS < segment.StartWallMS-segmentWindowSlackMS ||
				anchor.WallMS > segment.StopWallMS+segmentWindowSlackMS {
				return fmt.Errorf("segment %d has an anchor outside its own window", segment.Index)
			}
			// RTP timestamps are 32-bit unsigned on the wire.
			if anchor.RTPTimestamp < 0 || anchor.RTPTimestamp >= 1<<32 {
				return fmt.Errorf("segment %d has an out-of-range RTP timestamp", segment.Index)
			}
		}
		for _, interval := range segment.MuteIntervals {
			if interval[1] < interval[0] {
				return fmt.Errorf("segment %d has a backwards mute interval", segment.Index)
			}
		}
		if !captureSafeName.MatchString(segment.AudioName) {
			return fmt.Errorf("invalid segment name %q", segment.AudioName)
		}
		// The sidecar's own name is reserved: a segment claiming it would be
		// written and then overwritten by the manifest, losing that audio
		// silently and leaving the sidecar describing a file that is no longer
		// what it says.
		if segment.AudioName == captureSidecarName {
			return fmt.Errorf("segment name %q is reserved", segment.AudioName)
		}
		if _, dup := seen[segment.AudioName]; dup {
			return fmt.Errorf("duplicate segment name %q", segment.AudioName)
		}
		seen[segment.AudioName] = struct{}{}
	}
	return nil
}

// captureUploadDir is where one participant's capture for one call is stored.
// Keyed by room, then owner, then call start, so a re-upload of the same call
// replaces rather than accumulates, and two participants never collide.
func captureUploadDir(root, roomToken, owner string, callStartWallMS int64) string {
	return filepath.Join(root, roomToken, owner, fmt.Sprintf("%d", callStartWallMS))
}

// roomMembershipChecker reports whether a user may upload for a room. Nil when
// the operator has no AppAPI credentials (standalone dev), which is the same
// escape hatch the publish-time access control uses.
type roomMembershipChecker func(ctx context.Context, userID, roomToken string) (bool, error)

// talkRoomMembershipChecker asks Talk, acting AS the caller. A user who is not
// a participant cannot read the room's participant list, so a non-2xx is the
// answer rather than an error to work around.
func (c ExAppConfig) talkRoomMembershipChecker() roomMembershipChecker {
	if !c.appAPIActive() {
		return nil
	}
	base := strings.TrimRight(c.NextcloudURL, "/")
	client := &http.Client{Timeout: captureMembershipTimeout}
	return func(ctx context.Context, userID, roomToken string) (bool, error) {
		endpoint := base + "/ocs/v2.php/apps/spreed/api/v4/room/" + url.PathEscape(roomToken) + "/participants"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return false, fmt.Errorf("build participants request: %w", err)
		}
		c.setAppAPIOCSHeadersForUser(req, userID)
		resp, err := client.Do(req)
		if err != nil {
			return false, fmt.Errorf("talk participants: %w", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode >= 200 && resp.StatusCode < 300, nil
	}
}

// captureBodyTooLarge reports whether an error is MaxBytesReader refusing to
// read further. The typed error is the reliable signal, but multipart's reader
// does not always preserve the chain across its own wrapping, so the message
// Go's own handler emits is checked too — the alternative is reporting an
// oversize upload as a malformed one, which sends the client off debugging
// their encoder.
func captureBodyTooLarge(err error) bool {
	if err == nil {
		return false
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

// capturePromotionMu serializes the swap below. Two uploads for the same call
// are rare — a user retrying, or two tabs — but they target the same directory,
// and interleaving their renames would leave a mixture of both.
var capturePromotionMu sync.Mutex

// writeFileSynced writes a file and fsyncs it, so a crash cannot leave a
// zero-length sidecar that looks like a valid empty manifest.
func writeFileSynced(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// promoteCapture swaps a completed staging directory into its final path
// without destroying a previous good upload until the new one is in place.
//
// The obvious "remove the old, rename the new" loses the previous capture if
// the rename then fails — the participant's audio is gone and they have already
// deleted their local copy. Moving the old aside first means the worst case is
// a leftover directory, not a lost recording.
func (rt *Runtime) promoteCapture(staging, final string) error {
	capturePromotionMu.Lock()
	defer capturePromotionMu.Unlock()

	superseded := ""
	if _, err := os.Stat(final); err == nil {
		superseded = final + ".superseded"
		_ = os.RemoveAll(superseded)
		if err := os.Rename(final, superseded); err != nil {
			return fmt.Errorf("set aside previous capture: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if superseded != "" {
			// Put the previous upload back rather than leaving nothing.
			_ = os.Rename(superseded, final)
		}
		return fmt.Errorf("promote staging: %w", err)
	}
	if superseded != "" {
		_ = os.RemoveAll(superseded)
	}
	return nil
}

// captureEnabledHandler tells a running client whether collection is still
// permitted.
//
// It exists because turning the administrator gate off cannot, by itself, stop
// clients that are already running: the companion app cannot retract a script
// from a call already in progress. The payload polls this, so withdrawing
// permission reaches a live call rather than only the next one.
//
// Deliberately no-store: a cached "yes" is exactly the answer that would
// outlive the switch being turned off.
func (rt *Runtime) captureEnabledHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": sourceCaptureEnabled()})
}

// captureUploadHandler receives one participant's post-call source audio.
func (rt *Runtime) captureUploadHandler(isMember roomMembershipChecker, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !sourceCaptureEnabled() {
			// The administrator gate, checked server-side on every upload. A
			// stale client from before the feature was turned off still cannot
			// store anything.
			http.Error(w, "source capture is not enabled on this installation", http.StatusForbidden)
			return
		}
		owner := strings.TrimSpace(appapi.UserID(r.Context()))
		if owner == "" {
			// Without an authenticated caller there is no way to know whose
			// track this is, and an upload nobody owns can never be verified.
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, captureMaxUploadBytes)
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart/form-data", http.StatusBadRequest)
			return
		}

		var (
			sidecar   *captureSidecar
			stagedDir string
			promoted  bool
			written   = map[string]int64{}
		)
		// Stream parts straight to disk: buffering a meeting's audio in memory
		// to validate it first would make the size cap meaningless.
		//
		// Everything that lands in staging is removed unless it was promoted to
		// its final path. Keying this on "did we finish" rather than on any
		// individual failure is deliberate: a rejected upload — wrong room, no
		// membership, truncated body — must not leave a meeting's audio on the
		// volume, and there are too many exits to clean up one at a time.
		defer func() {
			if stagedDir != "" && !promoted {
				_ = os.RemoveAll(stagedDir)
			}
		}()

		// Created on first upload rather than at startup: a deployment where
		// nobody has opted in never grows the directory.
		if err := os.MkdirAll(rt.cfg.CaptureRoot, 0o750); err != nil {
			logger.Printf("capture upload: capture root %s: %v", rt.cfg.CaptureRoot, err)
			http.Error(w, "capture storage unavailable", http.StatusServiceUnavailable)
			return
		}
		staging, err := os.MkdirTemp(rt.cfg.CaptureRoot, "upload-")
		if err != nil {
			logger.Printf("capture upload: staging dir: %v", err)
			http.Error(w, "capture storage unavailable", http.StatusServiceUnavailable)
			return
		}
		stagedDir = staging

		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				if captureBodyTooLarge(err) {
					http.Error(w, "capture upload is too large", http.StatusRequestEntityTooLarge)
					return
				}
				// Truncation is a property of THIS delivery, not of the
				// capture: a proxy or upstream cutting the body short must
				// not make the client throw away an intact recording. 503
				// is the status the client retries on.
				http.Error(w, "upload truncated in transit", http.StatusServiceUnavailable)
				return
			}
			// Parts are classified by whether they carry a FILE NAME, not by
			// their form field name.
			//
			// A browser can send every segment under one repeated field name,
			// and Go's multipart reader hands them all back. Nextcloud's AppAPI
			// proxy does not stream the body through: it rebuilds it from PHP's
			// $_POST/$_FILES, and PHP keeps only the LAST file for a repeated
			// field name. Depending on the field name therefore lost every
			// segment but one, and the upload was refused as incomplete — for
			// the ordinary case of a participant switching microphone mid-call.
			// PHP also rewrites characters it dislikes in field names, so no
			// naming scheme is safe to rely on across that hop.
			//
			// The file name is what the sidecar already refers to and what the
			// staging directory is keyed by, so it is the only identifier that
			// has to survive. The client sends distinct field names purely so
			// the proxy keeps every part.
			formName := part.FormName()
			if formName != captureSidecarField && part.FileName() != "" {
				formName = captureSegmentPart
			}
			switch formName {
			case captureSidecarField:
				var parsed captureSidecar
				if err := json.NewDecoder(io.LimitReader(part, 32<<20)).Decode(&parsed); err != nil {
					if captureBodyTooLarge(err) {
						http.Error(w, "capture upload is too large", http.StatusRequestEntityTooLarge)
						return
					}
					// A sidecar that stops mid-JSON was cut in transit; a
					// sidecar that parses wrong is the client's bug. Only the
					// second is worth telling the client to give up over.
					if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
						http.Error(w, "upload truncated in transit", http.StatusServiceUnavailable)
						return
					}
					http.Error(w, "malformed sidecar", http.StatusBadRequest)
					return
				}
				if err := validateSidecar(&parsed); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				sidecar = &parsed
			case captureSegmentPart:
				name := filepath.Base(part.FileName())
				if !captureSafeName.MatchString(name) || name == captureSidecarName {
					http.Error(w, "invalid segment name", http.StatusBadRequest)
					return
				}
				dest, err := os.Create(filepath.Join(staging, name))
				if err != nil {
					logger.Printf("capture upload: create segment: %v", err)
					http.Error(w, "capture storage unavailable", http.StatusServiceUnavailable)
					return
				}
				n, copyErr := io.Copy(dest, part)
				closeErr := dest.Close()
				if copyErr != nil || closeErr != nil {
					if captureBodyTooLarge(copyErr) {
						http.Error(w, "capture upload is too large", http.StatusRequestEntityTooLarge)
						return
					}
					// Same reasoning as the reader error above: retryable.
					http.Error(w, "upload truncated in transit", http.StatusServiceUnavailable)
					return
				}
				written[name] = n
			default:
				// Ignore unknown parts rather than failing: a newer client may
				// send fields this server does not know about yet.
			}
			_ = part.Close()
		}

		if sidecar == nil {
			http.Error(w, "missing sidecar", http.StatusBadRequest)
			return
		}
		declared := make(map[string]struct{}, len(sidecar.Segments))
		for _, segment := range sidecar.Segments {
			declared[segment.AudioName] = struct{}{}
			if _, ok := written[segment.AudioName]; !ok {
				http.Error(w, fmt.Sprintf("missing segment %q", segment.AudioName), http.StatusBadRequest)
				return
			}
		}
		// The sidecar is the manifest, so the stored set must equal it. Parts
		// are classified as segments by carrying a file name, which means an
		// undeclared one would otherwise be written into the capture directory
		// and promoted alongside the real audio, where nothing downstream
		// expects it.
		for name := range written {
			if _, ok := declared[name]; !ok {
				http.Error(w, fmt.Sprintf("segment %q is not declared in the sidecar", name), http.StatusBadRequest)
				return
			}
		}
		if isMember != nil {
			member, err := isMember(r.Context(), owner, sidecar.RoomToken)
			if err != nil {
				logger.Printf("capture upload: membership check for %s: %v", sidecar.RoomToken, err)
				http.Error(w, "could not verify room membership", http.StatusBadGateway)
				return
			}
			if !member {
				// Not an error the client can fix by retrying: they were not in
				// this call.
				http.Error(w, "not a participant of this room", http.StatusForbidden)
				return
			}
		}
		if claimed := strings.TrimSpace(sidecar.ParticipantID); claimed != "" && claimed != owner {
			// Not fatal — a display-name-ish value from an odd client is
			// plausible — but it is the shape a spoofing attempt would take.
			logger.Printf("capture upload: participant id %q does not match authenticated %q", claimed, owner)
		}
		sidecar.OwnerUserID = owner
		sidecar.ReceivedAt = time.Now().UTC().Format(time.RFC3339)

		// Complete the directory in staging, THEN swap it in. Writing the
		// sidecar after promotion left a window where a crash or a disk error
		// published a capture directory with no manifest — a state nothing
		// downstream can tell from a truncated upload, since the sidecar is
		// what DiscoverSourceCaptures reads.
		body, err := json.Marshal(sidecar)
		if err != nil {
			http.Error(w, "could not record sidecar", http.StatusInternalServerError)
			return
		}
		if err := writeFileSynced(filepath.Join(staging, captureSidecarName), body); err != nil {
			logger.Printf("capture upload: write sidecar: %v", err)
			http.Error(w, "capture storage unavailable", http.StatusServiceUnavailable)
			return
		}

		final := captureUploadDir(rt.cfg.CaptureRoot, sidecar.RoomToken, owner, sidecar.CallStartWallMS)
		if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
			logger.Printf("capture upload: mkdir %s: %v", final, err)
			http.Error(w, "capture storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := rt.promoteCapture(staging, final); err != nil {
			logger.Printf("capture upload: promote %s -> %s: %v", staging, final, err)
			http.Error(w, "capture storage unavailable", http.StatusServiceUnavailable)
			return
		}
		promoted = true

		var total int64
		for _, n := range written {
			total += n
		}
		logger.Printf("capture upload: room=%s owner=%s segments=%d bytes=%d", sidecar.RoomToken, owner, len(sidecar.Segments), total)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "accepted",
			"room":     sidecar.RoomToken,
			"segments": len(sidecar.Segments),
			"bytes":    total,
		})
	}
}
