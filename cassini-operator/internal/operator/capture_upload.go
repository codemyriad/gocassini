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

// captureWindow is one segment's declared span.
type captureWindow struct{ start, stop int64 }

// covers reports whether this window holds everything `other` does.
func (w captureWindow) covers(other captureWindow) bool {
	return w.start <= other.start && w.stop >= other.stop
}

// storedCapture is what the server already holds for one call: the span of each
// segment it names, how many bytes of it are on disk, and how far into the call
// it reaches.
type storedCapture struct {
	windows map[string]captureWindow
	bytes   map[string]int64
	endsAt  int64
}

// readStoredCapture reads that. A zero value means there is genuinely nothing
// there; an error means the question could not be answered, which is NOT the
// same thing and must not be treated as it.
func readStoredCapture(dir string, roomToken string, callStartWallMS int64) (storedCapture, error) {
	raw, err := os.ReadFile(filepath.Join(dir, captureSidecarName))
	if os.IsNotExist(err) {
		return storedCapture{}, nil
	}
	if err != nil {
		// A transient read failure here used to read as "no capture stored",
		// which promotes the incoming upload and deletes the copy that could not
		// be read. The guard has to survive the storage faults it exists for.
		return storedCapture{}, fmt.Errorf("read stored capture sidecar: %w", err)
	}
	var stored captureSidecar
	if err := json.Unmarshal(raw, &stored); err != nil {
		// A manifest that does not parse describes nothing this can be compared
		// against, and a directory with one is already unusable to the build.
		return storedCapture{}, nil
	}
	if stored.CallStartWallMS != callStartWallMS || stored.RoomToken != roomToken {
		// A different call. Nothing to compare, and nothing to protect.
		return storedCapture{}, nil
	}
	windows := make(map[string]captureWindow, len(stored.Segments))
	bytes := make(map[string]int64, len(stored.Segments))
	for _, segment := range stored.Segments {
		windows[segment.AudioName] = captureWindow{segment.StartWallMS, segment.StopWallMS}
		// The file, not just the manifest. Recovery sidecars are checkpointed,
		// so two uploads for one call can carry the SAME manifest while their
		// snapshots of a still-growing segment hold different amounts of it —
		// and metadata that compares equal would let the staler one replace the
		// fuller. Bytes are the only thing that separates them here.
		if info, err := os.Stat(filepath.Join(dir, segment.AudioName)); err == nil {
			bytes[segment.AudioName] = info.Size()
		}
	}
	return storedCapture{windows: windows, bytes: bytes, endsAt: stored.CallEndWallMS}, nil
}

// captureWouldLoseStoredAudio reports whether promoting `incoming` would drop
// audio the server already holds for this call.
//
// A re-upload for one call replaces what is stored, and that is right for the
// case it was built for: a client offering the same capture again. It is wrong
// for the case a reload introduces. A browser that reloads mid-recording
// resumes its buffer, so a later upload for that call describes MORE of it —
// and if a stale copy of the earlier prefix reaches this endpoint afterwards
// (a second tab, a request the network reordered), last-writer-wins would
// replace the whole recording with its own first half and the sweep would
// delete the rest.
//
// The test is containment, asked three ways, because each of the narrower ones
// let through exactly the upload this is meant to refuse.
//
//   - Segment COUNT is not a measure of audio: a snapshot of a live one-segment
//     capture has the same count as the finished one and a fraction of its
//     seconds. That was the first thing tried.
//   - Segment NAMES alone are not either: the same two names can describe
//     twenty seconds in one capture and two minutes in another.
//   - The call's END alone is not: two pages that both resumed one prefix can
//     diverge, so the one that happens to end later need not hold everything
//     the other did.
//   - And no amount of METADATA is: a checkpointed manifest describes a segment
//     that was still growing, so two uploads can agree on every declared field
//     and disagree about how much of that segment they actually carry.
//
// So an upload may replace what is stored only if it names every segment the
// stored capture names, each over a window that covers the stored one's and
// with at least as many bytes, and reaches at least as far into the call. The
// byte comparison is against the staged file, which is the only description of
// this upload that is not the client's own account of it.
//
// The set-aside copy is consulted too. promoteCapture moves the previous
// capture to `.superseded` before it swaps, so a crash between those two
// renames leaves the whole recording THERE and nothing at the live path — and
// a stale prefix arriving afterwards would find no stored capture to compare
// against, promote itself, and let the sweep delete the longer copy.
// capturePromotion is what promoteCapture decided.
type capturePromotion int

const (
	// capturePromoted: the incoming upload is now the stored capture.
	capturePromoted capturePromotion = iota
	// captureAlreadyStored: the incoming upload names a subset of what is
	// stored and reaches no further into the call, so its bytes are already
	// here. Accepted without replacing, and the client stops offering it.
	captureAlreadyStored
	// captureDiverged: the two disagree — each holds a segment the other does
	// not. Neither may replace the other and neither may be thrown away, so the
	// upload is refused RETRYABLY rather than accepted: accepting it is what
	// makes the browser delete the only copy of the audio only it has.
	captureDiverged
)

func captureWouldLoseStoredAudio(incoming *captureSidecar, final, staging string) (bool, error) {
	live, err := readStoredCapture(final, incoming.RoomToken, incoming.CallStartWallMS)
	if err != nil {
		return false, err
	}
	setAside, err := readStoredCapture(final+captureSupersededSuffix, incoming.RoomToken, incoming.CallStartWallMS)
	if err != nil {
		return false, err
	}
	offered := make(map[string]captureWindow, len(incoming.Segments))
	for _, segment := range incoming.Segments {
		offered[segment.AudioName] = captureWindow{segment.StartWallMS, segment.StopWallMS}
	}
	// Both copies are compared against, not the "bigger" of them: whichever one
	// the incoming upload would drop something from is a reason to keep what is
	// stored.
	for _, held := range []storedCapture{live, setAside} {
		if len(held.windows) == 0 {
			continue
		}
		if incoming.CallEndWallMS < held.endsAt {
			return true, nil
		}
		for name, stored := range held.windows {
			mine, ok := offered[name]
			if !ok || !mine.covers(stored) {
				return true, nil
			}
			if info, err := os.Stat(filepath.Join(staging, name)); err == nil {
				if storedBytes, known := held.bytes[name]; known && info.Size() < storedBytes {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// restoreInterruptedPromotion completes a promotion that stopped between its
// two renames.
//
// promoteCapture moves the previous capture aside and then renames staging into
// place. A crash, or a failed second rename whose recovery also failed, can
// leave the set-aside copy holding the ONLY copy of that participant's audio
// with nothing at the live path. Discovery ignores that name by design, so the
// capture is on disk and unreachable; moving it back is the whole of the fix,
// and doing it here means the next upload for that call is judged against it.
func restoreInterruptedPromotion(final string) error {
	setAside := final + captureSupersededSuffix
	if _, err := os.Stat(setAside); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// "Cannot tell" is not "not there". Carrying on would judge this upload
		// as if no set-aside copy existed.
		return fmt.Errorf("stat set-aside capture: %w", err)
	}
	if _, err := os.Stat(final); err == nil {
		// A live capture exists; the set-aside copy is the older one the sweep
		// removes. Nothing to restore.
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat capture: %w", err)
	}
	if err := os.Rename(setAside, final); err != nil {
		// Reported, not swallowed. Carrying on would judge this upload against a
		// capture that is still under a name discovery ignores, and could accept
		// a shorter one — telling the client to delete its buffer while the only
		// complete copy stays unreachable. A retryable failure keeps both.
		return fmt.Errorf("restore interrupted promotion: %w", err)
	}
	return nil
}

// promoteCapture swaps a completed staging directory into its final path
// without destroying a previous good upload until the new one is in place.
//
// The obvious "remove the old, rename the new" loses the previous capture if
// the rename then fails — the participant's audio is gone and they have already
// deleted their local copy. Moving the old aside first means the worst case is
// a leftover directory, not a lost recording.
//
// The return says which of the three outcomes above happened; only
// capturePromoted means the staging directory became the stored capture.
func (rt *Runtime) promoteCapture(sidecar *captureSidecar, staging, final string) (capturePromotion, error) {
	capturePromotionMu.Lock()
	defer capturePromotionMu.Unlock()

	// An interrupted promotion left the whole capture aside and nothing at the
	// live path. Finish it before deciding anything: a capture under
	// `.superseded` is one discovery deliberately ignores, so "kept" would
	// otherwise mean retained on disk and invisible to every build — the audio
	// preserved and never used, which is not what the set-aside is for.
	if err := restoreInterruptedPromotion(final); err != nil {
		return capturePromoted, err
	}

	lossy, err := captureWouldLoseStoredAudio(sidecar, final, staging)
	if err != nil {
		// Refused rather than guessed. Promoting on an unanswered question is
		// what destroys the longer copy.
		return capturePromoted, err
	}
	if lossy {
		unique, err := captureHasAudioNotStored(sidecar, final)
		if err != nil {
			return capturePromoted, err
		}
		if unique {
			// Each holds something the other does not. Answering "accepted"
			// here is what makes the browser delete the only copy of the
			// segments only it has, and promoting would delete the only copy
			// of the ones only the server has. Neither, then: the client keeps
			// its buffer and an operator gets a log line naming the collision.
			return captureDiverged, nil
		}
		return captureAlreadyStored, nil
	}
	superseded := ""
	if _, err := os.Stat(final); err == nil {
		superseded = final + captureSupersededSuffix
		_ = os.RemoveAll(superseded)
		if err := os.Rename(final, superseded); err != nil {
			return capturePromoted, fmt.Errorf("set aside previous capture: %w", err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		if superseded != "" {
			// Put the previous upload back rather than leaving nothing.
			_ = os.Rename(superseded, final)
		}
		return capturePromoted, fmt.Errorf("promote staging: %w", err)
	}
	if superseded != "" {
		_ = os.RemoveAll(superseded)
	}
	return capturePromoted, nil
}

// captureHasAudioNotStored reports whether the incoming upload names a segment
// neither stored copy has. Together with captureWouldLoseStoredAudio it tells
// "this is a subset of what is here" apart from "these two disagree".
func captureHasAudioNotStored(incoming *captureSidecar, final string) (bool, error) {
	live, err := readStoredCapture(final, incoming.RoomToken, incoming.CallStartWallMS)
	if err != nil {
		return false, err
	}
	setAside, err := readStoredCapture(final+captureSupersededSuffix, incoming.RoomToken, incoming.CallStartWallMS)
	if err != nil {
		return false, err
	}
	for _, segment := range incoming.Segments {
		mine := captureWindow{segment.StartWallMS, segment.StopWallMS}
		liveHas := false
		if stored, ok := live.windows[segment.AudioName]; ok && stored.covers(mine) {
			liveHas = true
		}
		setAsideHas := false
		if stored, ok := setAside.windows[segment.AudioName]; ok && stored.covers(mine) {
			setAsideHas = true
		}
		if !liveHas && !setAsideHas {
			return true, nil
		}
	}
	return false, nil
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

// refuseCaptureUpload answers one refusal and leaves a server-side trace of it.
//
// Every non-2xx exit from the upload handler goes through here. A refusal used
// to be invisible on the server: the client discarded the recording and said so
// in a browser console nobody reads, so a pilot could lose captures with
// nothing in the container log to explain it. The reason is a stable token
// rather than prose, so grepping for "capture upload refused" answers "why is
// nothing arriving" on its own.
func refuseCaptureUpload(w http.ResponseWriter, logger *log.Logger, owner string, status int, reason, message string) {
	if strings.TrimSpace(owner) == "" {
		owner = "-"
	}
	if logger != nil {
		logger.Printf("capture upload refused: owner=%s status=%d reason=%s: %s", owner, status, reason, message)
	}
	http.Error(w, message, status)
}

// captureUploadHandler receives one participant's post-call source audio.
func (rt *Runtime) captureUploadHandler(isMember roomMembershipChecker, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			refuseCaptureUpload(w, logger, "", http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if !sourceCaptureEnabled() {
			// The administrator gate, checked server-side on every upload. A
			// stale client from before the feature was turned off still cannot
			// store anything.
			//
			// It stays the FIRST thing this handler decides, ahead of the
			// free-space and quota checks below. The documented promise is that
			// with collection off this feature touches no storage at all, and a
			// statfs or a walk of the capture root is work done on behalf of a
			// feature that is supposed to be inert.
			refuseCaptureUpload(w, logger, "", http.StatusForbidden, "collection_disabled",
				"source capture is not enabled on this installation")
			return
		}
		owner := strings.TrimSpace(appapi.UserID(r.Context()))
		if owner == "" {
			// Without an authenticated caller there is no way to know whose
			// track this is, and an upload nobody owns can never be verified.
			refuseCaptureUpload(w, logger, "", http.StatusUnauthorized, "unauthenticated", "authentication required")
			return
		}
		// Storage admission, before a byte is written and before the body is
		// read. Uploads share a volume with the job database, the work root and
		// the published site, so one that would crowd them out has to be
		// refused rather than half-written and cleaned up afterwards.
		admission, refusal := admitCaptureUpload(rt.cfg.CaptureRoot, owner, r.ContentLength, captureLimitsFromEnv())
		if refusal != nil {
			refuseCaptureUpload(w, logger, owner, refusal.status, refusal.reason, refusal.message)
			return
		}
		// Released however this request ends. Until it does, the bytes it was
		// promised are held against every other upload, because they are not
		// yet on disk where a measurement could see them.
		defer releaseCaptureAdmission(admission.owner, admission.reserved)
		r.Body = http.MaxBytesReader(w, r.Body, captureMaxUploadBytes)
		reader, err := r.MultipartReader()
		if err != nil {
			refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "not_multipart", "expected multipart/form-data")
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

		// Created on first upload rather than at startup: a deployment that
		// never receives one never grows the directory, whatever the switch
		// says.
		if err := os.MkdirAll(rt.cfg.CaptureRoot, 0o750); err != nil {
			logger.Printf("capture upload: capture root %s: %v", rt.cfg.CaptureRoot, err)
			refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "storage_unavailable", "capture storage unavailable")
			return
		}
		staging, err := os.MkdirTemp(rt.cfg.CaptureRoot, captureStagingPrefix)
		if err != nil {
			logger.Printf("capture upload: staging dir: %v", err)
			refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "storage_unavailable", "capture storage unavailable")
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
					refuseCaptureUpload(w, logger, owner, http.StatusRequestEntityTooLarge, "too_large", "capture upload is too large")
					return
				}
				// Truncation is a property of THIS delivery, not of the
				// capture: a proxy or upstream cutting the body short must
				// not make the client throw away an intact recording. 503
				// is the status the client retries on.
				refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "truncated", "upload truncated in transit")
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
						refuseCaptureUpload(w, logger, owner, http.StatusRequestEntityTooLarge, "too_large", "capture upload is too large")
						return
					}
					// A sidecar that stops mid-JSON was cut in transit; a
					// sidecar that parses wrong is the client's bug. Only the
					// second is worth telling the client to give up over.
					if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
						refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "truncated", "upload truncated in transit")
						return
					}
					refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "malformed_sidecar", "malformed sidecar")
					return
				}
				if err := validateSidecar(&parsed); err != nil {
					refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "invalid_sidecar", err.Error())
					return
				}
				sidecar = &parsed
				// A re-upload replaces a capture that is still on disk, and
				// until this point the quota was charged as if both would
				// coexist. They never do: promotion sets the old one aside and
				// the sweep removes it. Charging it would let a participant's
				// own previous copy of this very call refuse the replacement,
				// and that refusal is terminal — the browser deletes its only
				// copy. The room and call are only knowable here, because the
				// sidecar is the first part on the wire.
				replaced, err := captureDirBytes(
					captureUploadDir(rt.cfg.CaptureRoot, parsed.RoomToken, owner, parsed.CallStartWallMS))
				if err == nil {
					admission = admission.credit(replaced)
				}
			case captureSegmentPart:
				name := filepath.Base(part.FileName())
				if !captureSafeName.MatchString(name) || name == captureSidecarName {
					refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "invalid_segment_name", "invalid segment name")
					return
				}
				dest, err := os.Create(filepath.Join(staging, name))
				if err != nil {
					logger.Printf("capture upload: create segment: %v", err)
					refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "storage_unavailable", "capture storage unavailable")
					return
				}
				// The quota again, against the bytes that actually arrive.
				// admitCaptureUpload could only charge what Content-Length
				// declared, and that number is absent on a chunked body and
				// wrong on a lying one. Reading one byte past the budget is how
				// the overrun is detected; it goes out with the staging
				// directory.
				budget := admission.remaining()
				n, copyErr := io.Copy(dest, io.LimitReader(part, budget+1))
				closeErr := dest.Close()
				if copyErr != nil || closeErr != nil {
					if captureBodyTooLarge(copyErr) {
						refuseCaptureUpload(w, logger, owner, http.StatusRequestEntityTooLarge, "too_large", "capture upload is too large")
						return
					}
					// Same reasoning as the reader error above: retryable.
					refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "truncated", "upload truncated in transit")
					return
				}
				if n > budget {
					refusal := admission.overrun()
					refuseCaptureUpload(w, logger, owner, refusal.status, refusal.reason, refusal.message)
					return
				}
				admission = admission.consume(n)
				written[name] = n
			default:
				// Ignore unknown parts rather than failing: a newer client may
				// send fields this server does not know about yet.
			}
			_ = part.Close()
		}

		if sidecar == nil {
			refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "missing_sidecar", "missing sidecar")
			return
		}
		declared := make(map[string]struct{}, len(sidecar.Segments))
		for _, segment := range sidecar.Segments {
			declared[segment.AudioName] = struct{}{}
			if _, ok := written[segment.AudioName]; !ok {
				refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "missing_segment",
					fmt.Sprintf("missing segment %q", segment.AudioName))
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
				refuseCaptureUpload(w, logger, owner, http.StatusBadRequest, "undeclared_segment",
					fmt.Sprintf("segment %q is not declared in the sidecar", name))
				return
			}
		}
		if isMember != nil {
			member, err := isMember(r.Context(), owner, sidecar.RoomToken)
			if err != nil {
				logger.Printf("capture upload: membership check for %s: %v", sidecar.RoomToken, err)
				refuseCaptureUpload(w, logger, owner, http.StatusBadGateway, "membership_unknown", "could not verify room membership")
				return
			}
			if !member {
				// Not an error the client can fix by retrying: they were not in
				// this call.
				refuseCaptureUpload(w, logger, owner, http.StatusForbidden, "not_a_participant", "not a participant of this room")
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
			refuseCaptureUpload(w, logger, owner, http.StatusInternalServerError, "sidecar_unencodable", "could not record sidecar")
			return
		}
		if err := writeFileSynced(filepath.Join(staging, captureSidecarName), body); err != nil {
			logger.Printf("capture upload: write sidecar: %v", err)
			refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "storage_unavailable", "capture storage unavailable")
			return
		}

		final := captureUploadDir(rt.cfg.CaptureRoot, sidecar.RoomToken, owner, sidecar.CallStartWallMS)
		if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
			logger.Printf("capture upload: mkdir %s: %v", final, err)
			refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "storage_unavailable", "capture storage unavailable")
			return
		}
		outcome, err := rt.promoteCapture(sidecar, staging, final)
		if err != nil {
			logger.Printf("capture upload: promote %s -> %s: %v", staging, final, err)
			refuseCaptureUpload(w, logger, owner, http.StatusServiceUnavailable, "storage_unavailable", "capture storage unavailable")
			return
		}
		if outcome == captureDiverged {
			// Retryable on purpose. The client keeps its buffer, because the
			// segments only it has are not on this server and accepting would
			// make it delete them.
			logger.Printf("capture upload: room=%s owner=%s diverges from the capture already stored for that call; both copies hold audio the other does not",
				sidecar.RoomToken, owner)
			// 409, not 503. The client keeps a buffer either way, but 503 is a
			// transient failure and counts towards its attempt cap — which
			// would delete, on the fifth page load, exactly the audio this
			// refusal exists to preserve. 409 says the disagreement is about
			// the content, and the client holds on to it without counting.
			refuseCaptureUpload(w, logger, owner, http.StatusConflict, "diverged_capture",
				"another capture is stored for this call and neither contains the other")
			return
		}
		promoted = outcome == capturePromoted

		var total int64
		for _, n := range written {
			total += n
		}
		if outcome == captureAlreadyStored {
			// Accepted, and deliberately not stored: what is already here holds
			// this call's audio and more of it. Saying so is worth a log line,
			// because "the upload arrived and the bytes on disk did not change"
			// is otherwise indistinguishable from a bug.
			logger.Printf("capture upload: room=%s owner=%s kept the stored capture; this upload's %d segments are already inside it",
				sidecar.RoomToken, owner, len(sidecar.Segments))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "accepted",
				"room":     sidecar.RoomToken,
				"segments": len(sidecar.Segments),
				"bytes":    total,
			})
			return
		}
		logger.Printf("capture upload: room=%s owner=%s segments=%d bytes=%d", sidecar.RoomToken, owner, len(sidecar.Segments), total)
		// Only the promoted branch, never captureAlreadyStored: an upload whose
		// bytes are already here adds nothing a rebuild could read, and
		// counting it would re-transcribe a meeting to produce the identical
		// transcript (D-698).
		rt.noteCaptureArrival(sidecar, owner, logger)
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
