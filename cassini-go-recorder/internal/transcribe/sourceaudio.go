package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Ingestion of participant-captured source audio.
//
// The recorder subscribes through the SFU, so a track in the MKV is whatever
// survived that participant's uplink. Source capture records the same signal in
// the participant's browser before Opus encoding and before the network
// (cassini-app/src/capture/, docs/source-audio-capture.md) and uploads it after
// the call. This file turns one of those uploads into PCM on the meeting
// timeline, which is the only shape the rest of the pipeline knows.
//
// # Two clocks, and which one does what
//
// A first design tried to map the client's RTP timestamps directly onto the
// recorder's, on the theory that both are the sender's 48 kHz audio clock. They
// are not. Janus rewrites the timestamps it relays to each subscriber —
// janus_rtp_header_update computes last_ts = (timestamp - base_ts) +
// base_ts_prev, and re-anchors on every SSRC change or pause — so what the
// recorder logs sits in a per-subscriber space whose offset from the sender is
// unknown and moves at those seams. Anchoring on it would place a
// participant's words at a confidently wrong time.
//
// So the two axes come from different places:
//
//	RATE, from the client's own anchors. Each anchor pairs a wall-clock
//	instant with an RTP timestamp on the participant's audio sample clock. The
//	ratio between them is that machine's sound-card drift, which is the
//	dominant drift in this system — tens to hundreds of milliseconds over a
//	long meeting — and the fit below solves it rather than estimating it.
//	This part is immune to loss: the anchors describe frames the client
//	ENCODED, so whether each one reached the server is irrelevant. A test
//	asserts an 86% anchor loss rate moves nothing.
//
//	OFFSET, from wall clock. The recorder records the wall-clock instant of
//	each stream's first packet (remux.StreamPlan.FirstPacketWallMS, emitted as
//	first_packet_wall_ms) against its position on the meeting timeline. Both
//	derive from the same monotonic clock, so that mapping is exact on the
//	recorder's side. The segment's own start instant — when MediaRecorder
//	began, which is sample zero of the uploaded file — is what gets mapped
//	through it. NOT the first anchor: anchors are sampled one per fifty frames
//	and the first arrives after the encoder spins up, so anchoring on it placed
//	every speaker late by up to a second.
//
// # What that costs, stated plainly
//
// The offset is only as good as the agreement between the participant's clock
// and the recorder's, plus the encoder's roughly-constant latency. With both
// machines NTP-disciplined that is tens of milliseconds, comfortably inside a
// word. With a badly-synchronised client it is seconds, and the transcript
// would carry that speaker's words at the wrong time.
//
// PlausibleOffset below is a guard, not a fix: it rejects placements that fall
// outside the recording, which catches a clock that is wrong by hours but not
// one that is wrong by seconds. The real fix is a single cross-correlation
// against any stretch where the recorded track has intact audio — one constant,
// needing a few good seconds anywhere in the call rather than a good reference
// throughout. That is not implemented yet, and until it is, ingestion should be
// treated as trustworthy only where clients are known to be time-synchronised.

// SourceCaptureFormat is the sidecar schema this ingester accepts. It must
// match SOURCE_CAPTURE_FORMAT in cassini-app/src/capture/protocol.ts and
// captureSourceFormat in the operator's capture_upload.go.
const SourceCaptureFormat = "org.cassini.source-capture/1"

const (
	// minPlacementAnchors is the fewest anchors a segment may be placed from.
	// Two define a line; this leaves enough redundancy for the outlier pass
	// below to mean something. A segment with fewer is not placed at all — the
	// recorded track is used instead, which is the safe direction.
	minPlacementAnchors = 8

	// maxPlacementResidualMS rejects a fit whose anchors do not agree. Encoder
	// latency jitter is a few ms; a wall-clock step, a spliced file, or anchors
	// from a different call show up far above that.
	maxPlacementResidualMS = 120.0

	// maxPlacementRateDeviation bounds the fitted rate. Consumer sample clocks
	// drift by tens to a few hundred ppm; anything past 0.5% is not drift, it
	// is a broken fit.
	maxPlacementRateDeviation = 0.005

	// rtpWrap is the RTP timestamp modulus. At 48 kHz it takes about 24 hours
	// to wrap, so it matters only when a meeting happens to straddle one.
	rtpWrap = int64(1) << 32
)

// SourceAnchor ties one outgoing encoded frame to wall-clock time, as reported
// by the participant's browser.
type SourceAnchor struct {
	FrameIndex   int64 `json:"frameIndex"`
	RTPTimestamp int64 `json:"rtpTimestamp"`
	SSRC         int64 `json:"ssrc"`
	WallMS       int64 `json:"wallMs"`
}

// SourceSegment is one continuous local recording of one sender track.
type SourceSegment struct {
	Index         int            `json:"index"`
	AudioName     string         `json:"audioName"`
	MimeType      string         `json:"mimeType"`
	StartWallMS   int64          `json:"startWallMs"`
	StopWallMS    int64          `json:"stopWallMs"`
	Anchors       []SourceAnchor `json:"anchors"`
	MuteIntervals [][2]int64     `json:"muteIntervals"`
}

// SourceSidecar is the manifest uploaded alongside the audio.
type SourceSidecar struct {
	Format          string          `json:"format"`
	RoomToken       string          `json:"roomToken"`
	CallStartWallMS int64           `json:"callStartWallMs"`
	CallEndWallMS   int64           `json:"callEndWallMs"`
	Segments        []SourceSegment `json:"segments"`
	// OwnerUserID is stamped by the operator from the authenticated uploader,
	// never taken from the browser. It is the join key against the MKV's
	// PARTICIPANT_ID tag, since both come from Talk's user id.
	OwnerUserID string `json:"ownerUserId"`
}

// SourceTimeBase now lives in timebase.go, landed on main with the remux
// anchor it reads. The mapping onto the meeting timeline stays here, with the
// placement arithmetic that uses it.

// timelineMS converts a wall-clock instant to a position on the meeting
// timeline. Exact on the recorder's side; the caller's error is whatever
// separates its clock from the recorder's.
func (b SourceTimeBase) timelineMS(wallMS int64) float64 {
	return float64(b.FirstTimelineNS)/1e6 + float64(wallMS-b.FirstPacketWallMS)
}

// mediaMS reads an anchor's position inside the local recording from the
// participant's own audio sample clock, relative to the segment's first anchor.
// Unwrapped against that base rather than the previous value: anchors are
// sampled, so consecutive ones can legitimately be far apart.
func mediaMS(rtp, baseRTP int64, clockRate uint32) float64 {
	delta := rtp - baseRTP
	for delta < -rtpWrap/2 {
		delta += rtpWrap
	}
	for delta > rtpWrap/2 {
		delta -= rtpWrap
	}
	return float64(delta) * 1000 / float64(clockRate)
}

// Placement maps a segment's local recording time onto the meeting timeline:
//
//	meetingMS = OffsetMS + Rate*localMS
type Placement struct {
	OffsetMS float64
	Rate     float64
	// Anchors is how many anchors survived outlier rejection, and ResidualMS
	// the RMS disagreement among them. Both travel into the build manifest:
	// a placement is a claim about where somebody's words belong, and it should
	// be auditable after the fact.
	Anchors    int
	ResidualMS float64
}

// RatePPMDeviation expresses the fitted rate as parts per million away from
// 1.0 — the units clock drift is normally quoted in, and the scale that makes
// a plausible fit (tens to low hundreds) obviously different from a broken one.
func (p Placement) RatePPMDeviation() float64 {
	return (p.Rate - 1) * 1e6
}

// FitPlacement solves the segment's placement from its anchors.
//
// One robustness pass, not a full RANSAC: fit, drop anchors more than three
// sigma out, refit. The outliers being removed are wall-clock steps and
// scheduling stalls in the browser, which are rare and large — not a
// contaminated majority that would need something stronger.
func FitPlacement(segment SourceSegment, base SourceTimeBase) (Placement, error) {
	if !base.Known || base.ClockRate == 0 {
		return Placement{}, fmt.Errorf("recording carries no wall-clock time base for this speaker")
	}
	if len(segment.Anchors) < minPlacementAnchors {
		return Placement{}, fmt.Errorf("only %d anchors, need %d", len(segment.Anchors), minPlacementAnchors)
	}
	if segment.StartWallMS <= 0 {
		return Placement{}, fmt.Errorf("segment has no start time")
	}

	// Sample zero of the decoded file is the instant MediaRecorder started,
	// which is segment.StartWallMS — NOT the first anchor.
	//
	// This distinction was got wrong once and is worth spelling out. Anchors
	// are sampled one per fifty encoded frames and the first one arrives after
	// the encoder has spun up (on the initial connection, after negotiation),
	// so treating the first anchor as local time zero placed every speaker's
	// audio late by up to a second. The anchors' job is the RATE; the file's
	// start is what fixes the OFFSET.
	//
	// So: fit the participant's audio sample clock against their wall clock
	// over the anchors, and take the offset from the segment's start instant
	// mapped through the recorder's own wall anchor.
	type point struct{ wall, audio float64 }
	baseRTP := segment.Anchors[0].RTPTimestamp
	points := make([]point, 0, len(segment.Anchors))
	for _, anchor := range segment.Anchors {
		points = append(points, point{
			wall:  float64(anchor.WallMS - segment.StartWallMS),
			audio: mediaMS(anchor.RTPTimestamp, baseRTP, base.ClockRate),
		})
	}

	fit := func(pts []point) (slope, intercept float64, ok bool) {
		n := float64(len(pts))
		if n < 2 {
			return 0, 0, false
		}
		var sx, sy, sxx, sxy float64
		for _, p := range pts {
			sx += p.wall
			sy += p.audio
			sxx += p.wall * p.wall
			sxy += p.wall * p.audio
		}
		denom := n*sxx - sx*sx
		if denom == 0 {
			// Every anchor at the same instant: no slope is determined.
			return 0, 0, false
		}
		slope = (n*sxy - sx*sy) / denom
		intercept = (sy - slope*sx) / n
		return slope, intercept, true
	}

	slope, intercept, ok := fit(points)
	if !ok {
		return Placement{}, fmt.Errorf("anchors are degenerate")
	}
	residual := func(pts []point, slope, intercept float64) float64 {
		var sum float64
		for _, p := range pts {
			d := p.audio - (intercept + slope*p.wall)
			sum += d * d
		}
		return math.Sqrt(sum / float64(len(pts)))
	}
	rms := residual(points, slope, intercept)
	if rms > 0 {
		// One robustness pass, not a full RANSAC: the outliers being removed
		// are wall-clock steps and browser scheduling stalls, which are rare and
		// large, not a contaminated majority.
		kept := points[:0:0]
		for _, p := range points {
			if math.Abs(p.audio-(intercept+slope*p.wall)) <= 3*rms {
				kept = append(kept, p)
			}
		}
		if len(kept) >= minPlacementAnchors {
			if s, i, ok := fit(kept); ok {
				slope, intercept = s, i
				points = kept
				rms = residual(points, slope, intercept)
			}
		}
	}

	// slope is audio-ms per wall-ms. A millisecond of recorded audio therefore
	// covers 1/slope milliseconds of the meeting.
	if slope <= 0 {
		return Placement{}, fmt.Errorf("fitted audio clock does not advance")
	}
	placement := Placement{
		OffsetMS:   base.timelineMS(segment.StartWallMS),
		Rate:       1 / slope,
		Anchors:    len(points),
		ResidualMS: rms,
	}
	if math.Abs(placement.Rate-1) > maxPlacementRateDeviation {
		return placement, fmt.Errorf("fitted rate %.6f is not plausible clock drift", placement.Rate)
	}
	if rms > maxPlacementResidualMS {
		return placement, fmt.Errorf("anchors disagree by %.1f ms RMS", rms)
	}
	return placement, nil
}

// PlausibleOffset rejects a placement that falls outside the recording.
//
// This is a guard against a client whose clock is wrong by hours or is not set
// at all — the case where believing it would scatter one participant's words
// across a timeline they never occupied. It cannot catch a clock that is wrong
// by seconds; nothing here can, and a cross-correlation refinement is what
// eventually should. Deliberately generous: half a meeting of slack, because
// the cost of wrongly rejecting is only that the recorded track is used, while
// the cost of wrongly accepting is a transcript nobody can trust.
func PlausibleOffset(placement Placement, timelineMS int64) bool {
	slack := float64(timelineMS) / 2
	if slack < 30_000 {
		slack = 30_000
	}
	return placement.OffsetMS > -slack && placement.OffsetMS < float64(timelineMS)+slack
}

// overlayOntoTimeline writes one segment's resampled PCM over `dst`, in place,
// and reports the half-open output window [start, stop) it wrote.
//
// Overlay, not sum. `dst` starts as the participant's RECORDED track, so adding
// would play both copies of the same words at once; writing over it means the
// upload replaces the recorded audio exactly where it has audio, and nowhere
// else. That is the whole safety property of the splice: outside the window the
// recorded track is left byte-identical, so the result can never be worse than
// not ingesting at all.
//
// The two boundaries are decided here and nowhere else. The window opens at the
// first output sample whose position inside the segment is non-negative, and
// closes at the first one that would need a sample past the end of the segment.
// So each output sample is written by at most one segment, no instant is
// represented twice, and none is skipped between the recorded track and the
// upload. What a seam can still carry is a discontinuity — a click where the
// two sources disagree about the participant's clock by a few milliseconds —
// which is the placement error the anchors and the wall-clock offset already
// determine, not something the overlay adds.
//
// Pure arithmetic, deliberately not delegated to ffmpeg: the rate correction is
// a handful of parts per million, fiddly to express as a filter graph and
// trivial to state directly. Linear interpolation is more than enough at that
// ratio — the correction moves a sample by a small fraction of a sample.
func overlayOntoTimeline(dst, src []float32, sampleRate int, placement Placement) (int, int) {
	if len(dst) == 0 || len(src) < 2 || sampleRate <= 0 || placement.Rate <= 0 {
		return 0, 0
	}
	msPerSample := 1000.0 / float64(sampleRate)
	// The first output sample at or after the segment's own start. Ceil rather
	// than round: rounding down would ask for a source position just before
	// sample zero, which is audio the segment does not have.
	start := int(math.Ceil(placement.OffsetMS / msPerSample))
	if start < 0 {
		start = 0
	}
	if start >= len(dst) {
		return 0, 0
	}
	stop := start
	for j := start; j < len(dst); j++ {
		localMS := (float64(j)*msPerSample - placement.OffsetMS) / placement.Rate
		pos := localMS / msPerSample
		if pos < 0 {
			// Only reachable when the segment starts before the timeline does;
			// those samples belong to audio outside the recording.
			continue
		}
		if pos >= float64(len(src)) {
			break
		}
		i := int(pos)
		if i >= len(src)-1 {
			// The last sample has no partner to interpolate towards. Taking it
			// as it is rather than stopping one short: the window is supposed
			// to hold every sample the segment decoded, and leaving the final
			// one to the recorded track underneath is both a dropped sample and
			// a discontinuity at the seam.
			dst[j] = src[len(src)-1]
		} else {
			frac := float32(pos - float64(i))
			dst[j] = src[i]*(1-frac) + src[i+1]*frac
		}
		stop = j + 1
	}
	if stop <= start {
		return 0, 0
	}
	return start, stop
}

// RenderOntoTimeline places one segment's PCM on an otherwise silent meeting
// timeline. It is overlayOntoTimeline against silence, kept because "where does
// this segment land on its own" is the question the placement tests ask.
func RenderOntoTimeline(src []float32, sampleRate int, placement Placement, outSamples int) []float32 {
	out := make([]float32, outSamples)
	overlayOntoTimeline(out, src, sampleRate, placement)
	return out
}

// LoadSourceSidecar reads and validates one upload directory's manifest.
func LoadSourceSidecar(dir string) (SourceSidecar, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "capture.json"))
	if err != nil {
		return SourceSidecar{}, fmt.Errorf("read capture sidecar: %w", err)
	}
	var sidecar SourceSidecar
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		return SourceSidecar{}, fmt.Errorf("parse capture sidecar: %w", err)
	}
	if sidecar.Format != SourceCaptureFormat {
		return SourceSidecar{}, fmt.Errorf("unsupported capture format %q", sidecar.Format)
	}
	if strings.TrimSpace(sidecar.OwnerUserID) == "" {
		return SourceSidecar{}, fmt.Errorf("capture sidecar has no owner")
	}
	return sidecar, nil
}

// DiscoverSourceCaptures finds the uploads belonging to ONE recording, keyed by
// the owner's Nextcloud user id — the same value the MKV carries as
// PARTICIPANT_ID.
//
// Layout is <root>/<room>/<owner>/<call-start-ms>/, written by the operator's
// capture_upload.go.
//
// Selection is by room AND overlapping call window, not by participant alone.
// Matching on participant alone was wrong in two ways that both end with one
// meeting's speech in another's transcript: a later unrelated capture hid the
// correct older one, and two calls close together in time could each satisfy
// the (deliberately generous) placement check. A capture has to be from this
// room and from a call overlapping this recording before it is a candidate.
//
// roomToken may be empty when the caller does not know it — building a bare MKV
// outside the operator, say. The window check still applies; the room check is
// skipped, and the caller is trusting the window alone.
// supersededSuffix is the name the upload handler gives a capture it has set
// aside while promoting a newer one for the same call. It must never be
// discovered as a capture in its own right; see the loop below.
const supersededSuffix = ".superseded"

func DiscoverSourceCaptures(root, roomToken string, windowStartMS, windowEndMS int64) (map[string][]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	roomGlob := "*"
	if token := strings.TrimSpace(roomToken); token != "" {
		roomGlob = token
	}
	matches, err := filepath.Glob(filepath.Join(root, roomGlob, "*", "*", "capture.json"))
	if err != nil {
		return nil, fmt.Errorf("scan capture root: %w", err)
	}
	sort.Strings(matches)
	found := map[string][]string{}
	for _, match := range matches {
		dir := filepath.Dir(match)
		// The upload handler promotes a re-upload by renaming the previous one
		// aside before moving the new one into place. That name sits at the
		// same depth as a real capture, so it matches the glob above, and a
		// build that ran inside the rename window — or after a crash left one
		// behind — would find two directories for one owner and sum both onto
		// the timeline: the same speech twice, at double amplitude, with the
		// recorded track already suppressed.
		if strings.HasSuffix(filepath.Base(dir), supersededSuffix) {
			continue
		}
		sidecar, err := LoadSourceSidecar(dir)
		if err != nil {
			// A malformed upload must not fail the build: the recorded track is
			// still there and the meeting still publishes.
			continue
		}
		if roomToken != "" && sidecar.RoomToken != roomToken {
			continue
		}
		if !windowsOverlap(sidecar.CallStartWallMS, sidecar.CallEndWallMS, windowStartMS, windowEndMS) {
			continue
		}
		// EVERY matching capture, not just the newest. A participant who left
		// and rejoined uploads one per session, and both belong to this
		// recording; keeping only the later one while suppressing their
		// recorded streams silently dropped the first half of what they said.
		found[sidecar.OwnerUserID] = append(found[sidecar.OwnerUserID], dir)
	}
	return found, nil
}

// windowsOverlap reports whether two wall-clock spans intersect, with a minute
// of slack on each side. The slack covers a client that started recording
// slightly before the recorder attached or stopped slightly after it detached;
// it is far tighter than the placement guard, which is what makes it useful for
// telling two nearby meetings apart.
func windowsOverlap(aStart, aEnd, bStart, bEnd int64) bool {
	const slackMS = 60_000
	if aStart <= 0 || bStart <= 0 {
		return false
	}
	return aStart-slackMS <= bEnd && bStart-slackMS <= aEnd
}

// SourceRenderReport records what ingestion did for one speaker, for the build
// manifest. A transcript built partly from audio the server never received
// should say so, and — since ingestion is now a splice over the recorded track
// rather than a replacement of it — should say how much of it.
type SourceRenderReport struct {
	SpeakerID  string  `json:"speaker_id"`
	Owner      string  `json:"owner"`
	Segments   int     `json:"segments"`
	Placed     int     `json:"placed"`
	Anchors    int     `json:"anchors"`
	ResidualMS float64 `json:"residual_ms"`
	RatePPM    float64 `json:"rate_ppm"`
	CoverageMS int64   `json:"coverage_ms"`
	// DeclaredMS is how much audio the capture's own sidecar says it holds:
	// the sum of its segment windows. CoverageMS is how much actually decoded.
	// The two are reported side by side because their ratio is the only signal
	// that a capture is internally incomplete, and a pilot needs the numbers to
	// choose a threshold rather than inherit a guessed one.
	DeclaredMS int64 `json:"declared_ms"`
	CallMS     int64 `json:"call_ms"`
	// SplicedMS is how much of the meeting timeline the upload actually
	// replaced. The rest of this speaker's transcription input is their
	// recorded track, unchanged, so this is the one number that says how much
	// of the transcript the capture is answerable for.
	SplicedMS int64 `json:"spliced_ms"`
	// Skipped counts segments left out of the splice entirely. Each one costs
	// nothing but the recorded audio staying where it was.
	Skipped int `json:"skipped"`
	// Rejections says what the splice declined to do and why. Every skipped
	// segment has an entry; so does a segment that was used only across the
	// window it declared, which is not a skip but is still the splice refusing
	// part of what arrived.
	Rejections []string `json:"rejections,omitempty"`
}

// minSegmentDecodedFraction is how much of the audio a segment DECLARES it must
// actually decode to before its audio is used.
//
// Intake validates that a declared file arrived, never that it holds what the
// sidecar claims. A segment can therefore declare ten minutes and hold one, and
// the difference would be nine minutes placed from a file that has nothing to
// put there. The splice bounds the damage — an overlaid window only ever covers
// the audio that decoded — but a file that disagrees with its own manifest by
// this much is not a file whose timing claims are worth believing either, so it
// is left out and the recorded track stands for that stretch.
const minSegmentDecodedFraction = 0.9

// segmentOverrunSlackMS is how far past its declared window a segment's audio
// may reach and still be laid over the recorded track.
//
// Some overrun is honest. A page that died mid-recording leaves a manifest
// written at the last checkpoint, so the file on disk holds everything up to
// the last chunk after it — bounded by the checkpoint interval plus one chunk.
// Beyond that the file is not describing itself any more, and the difference is
// recorded audio the splice would replace with something the sidecar never
// claimed was there. Only the declared window plus this slack is overlaid; the
// rest of the file is ignored rather than the whole segment being dropped,
// because the audio inside the window is still the audio the manifest promised.
const segmentOverrunSlackMS = 10_000

// SpliceSourceTrack builds one speaker's transcription input by laying every
// placeable segment they uploaded over their own RECORDED track.
//
// The recorded track is the floor, and that is the whole design. Substitution
// used to be whole-track — the render replaced the participant for the entire
// meeting — so every span the upload did not cover became digital silence while
// the recorded audio was suppressed, and words the recorder had heard perfectly
// well disappeared. Guarding that with a coverage threshold only decided WHICH
// participants lost speech; it could not stop the loss, and it refused exactly
// the people the feature exists for, whose page reloaded on a bad connection
// and whose capture therefore has a hole in it where they were not in the call
// at all.
//
// Splicing removes the question. Each segment replaces the recorded audio over
// the window it actually holds audio for, and nowhere else. A reload gap, a
// late start, a worker that died mid-call, a segment that cannot be placed:
// all of them simply leave the recorded track standing there, which is exactly
// what would have been transcribed without any upload. The result can never be
// worse than not ingesting, so there is nothing left to refuse.
//
// A segment that cannot be placed, cannot be decoded, or holds far less audio
// than it declares is skipped with its reason recorded, not fatal. Only a
// capture that contributes nothing at all is an error, so the caller can keep
// the recorded track untouched rather than write a WAV identical to it.
//
// Several directories because one participant can legitimately have more than
// one capture for a single recording — they left and rejoined, or a reload was
// not adopted into the capture that followed it.
func SpliceSourceTrack(ctx context.Context, recorded []float32, dirs []string, base SourceTimeBase, sampleRate int, outSamples int) ([]float32, SourceRenderReport, error) {
	report := SourceRenderReport{}
	if sampleRate <= 0 || outSamples <= 0 {
		// An error rather than a division by zero. Production passes 16 kHz and
		// a measured timeline, but a caller that gets this wrong should be told
		// so rather than take the build down with a panic.
		return nil, report, fmt.Errorf("splice needs a positive sample rate and timeline, got %d Hz and %d samples", sampleRate, outSamples)
	}
	// A copy, always. The caller's recorded PCM is the fallback for this
	// speaker and for the assertion that a splice changed only what it claims
	// to have changed; mutating it in place would destroy both.
	out := make([]float32, outSamples)
	copy(out, recorded)
	timelineMS := int64(outSamples) * 1000 / int64(sampleRate)
	var totalAnchors int
	var worstResidual float64
	var rateSum float64
	// The output windows each overlaid segment wrote, for the report. Collected
	// rather than summed: two captures for one recording can overlap (a rejoin
	// whose windows meet, a page that filed its reload separately), and a later
	// overlay merely overwrites an earlier one — so adding the lengths would
	// claim more of the timeline was spliced than exists.
	type window struct{ from, to int }
	var windows []window

	// Load every sidecar first, so a capture that cannot even be read costs no
	// ffmpeg. This IS fatal: an unreadable manifest says nothing about which
	// windows the rest of the capture may claim.
	loaded := make([]SourceSidecar, 0, len(dirs))
	for _, dir := range dirs {
		sidecar, err := LoadSourceSidecar(dir)
		if err != nil {
			return nil, report, fmt.Errorf("%s: %w", filepath.Base(dir), err)
		}
		report.Owner = sidecar.OwnerUserID
		report.Segments += len(sidecar.Segments)
		report.CallMS += sidecar.CallEndWallMS - sidecar.CallStartWallMS
		for _, segment := range sidecar.Segments {
			if declaredMS := segment.StopWallMS - segment.StartWallMS; declaredMS > 0 {
				report.DeclaredMS += declaredMS
			}
		}
		loaded = append(loaded, sidecar)
	}

	skip := func(segment SourceSegment, format string, args ...any) {
		report.Skipped++
		report.Rejections = append(report.Rejections,
			fmt.Sprintf("segment %d: %s; keeping the recorded audio there", segment.Index,
				fmt.Sprintf(format, args...)))
	}

	for i, dir := range dirs {
		sidecar := loaded[i]
		for _, segment := range sidecar.Segments {
			placement, err := FitPlacement(segment, base)
			if err != nil {
				skip(segment, "%v", err)
				continue
			}
			if !PlausibleOffset(placement, timelineMS) {
				skip(segment, "places at %.0f ms, outside a %d ms recording — the uploader's clock is not usable",
					placement.OffsetMS, timelineMS)
				continue
			}
			segmentMS := segment.StopWallMS - segment.StartWallMS
			if segmentMS <= 0 {
				// A segment declaring no window claims no audio, and both bounds
				// below are expressed in terms of that window: the too-short
				// check and the overrun clamp are skipped for it, so a file
				// holding a minute under a zero-length window would replace a
				// minute of recorded audio while promising none. Intake accepts
				// stop == start; the splice will not act on it.
				skip(segment, "declares no window, so there is nothing its audio can be said to cover")
				continue
			}
			samples, err := decodeSourceSegment(ctx, filepath.Join(dir, segment.AudioName), sampleRate,
				sourceDecodeTimeout(segmentMS), maxSourceSegmentSamples(segmentMS, sampleRate, outSamples))
			if err != nil {
				skip(segment, "decode: %v", err)
				continue
			}
			decodedMS := int64(float64(len(samples)) * 1000 / float64(sampleRate))
			// The audio against the sidecar, kept from the whole-track design.
			// Intake validates that a declared file ARRIVED, never that it holds
			// what was claimed, and a file this far short of its own manifest is
			// not one whose timing claims are worth believing.
			if float64(decodedMS) < float64(segmentMS)*minSegmentDecodedFraction {
				skip(segment, "holds %d ms of the %d ms it declares (%.0f%%); the audio does not match the sidecar",
					decodedMS, segmentMS, float64(decodedMS)*100/float64(segmentMS))
				continue
			}
			// Only what the segment declared, plus the overrun a checkpointed
			// manifest can honestly lag by. A file holding a minute of audio
			// under a one-second window would otherwise replace a minute of
			// recorded audio, which is exactly the "never worse than the
			// recorded track" property the splice exists for.
			if limit := expectedPCMSamples(segmentMS+segmentOverrunSlackMS, sampleRate); limit > 0 && len(samples) > limit {
				report.Rejections = append(report.Rejections, fmt.Sprintf(
					"segment %d holds %d ms under a %d ms window; only the first %d ms of it was used",
					segment.Index, decodedMS, segmentMS, segmentMS+segmentOverrunSlackMS))
				samples = samples[:limit]
			}
			from, to := overlayOntoTimeline(out, samples, sampleRate, placement)
			if to <= from {
				skip(segment, "places entirely outside the %d ms recording", timelineMS)
				continue
			}
			report.Placed++
			windows = append(windows, window{from, to})
			report.CoverageMS += decodedMS
			totalAnchors += placement.Anchors
			rateSum += placement.Rate
			if placement.ResidualMS > worstResidual {
				worstResidual = placement.ResidualMS
			}
		}
	}

	if report.Placed == 0 {
		return nil, report, fmt.Errorf("no segment could be placed")
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].from < windows[j].from })
	var splicedSamples, cursor int
	for _, w := range windows {
		if w.to <= cursor {
			continue
		}
		if w.from > cursor {
			cursor = w.from
		}
		splicedSamples += w.to - cursor
		cursor = w.to
	}
	report.SplicedMS = int64(splicedSamples) * 1000 / int64(sampleRate)
	report.Anchors = totalAnchors
	report.ResidualMS = worstResidual
	report.RatePPM = Placement{Rate: rateSum / float64(report.Placed)}.RatePPMDeviation()
	return out, report, nil
}

// decodeSourceSegment decodes one uploaded segment to mono float PCM.
//
// The input is a browser-produced WebM/Opus file — untrusted content, handed
// straight to ffmpeg. That is the same exposure the recorder already has to its
// own captured media, but the provenance is different and worth naming: this
// arrived over HTTP from a participant's machine.
// sourceDecodeTimeout bounds the decode of one participant-supplied segment.
//
// Every other input this pipeline decodes was produced by the recorder. This
// one arrived over HTTP from a participant's machine, so it is the first media
// an outsider chooses, and an unbounded ffmpeg on it would hold the single
// build worker for as long as the attacker liked while every queued meeting
// waits behind it.
//
// Decoding runs far faster than real time, so a generous multiple of the
// segment's own declared length is still a tight bound, and the floor keeps a
// short segment workable on a loaded host. The ceiling is what actually
// contains a file whose declared length is a lie.
func sourceDecodeTimeout(segmentMS int64) time.Duration {
	const (
		floor   = 60 * time.Second
		ceiling = 10 * time.Minute
	)
	budget := time.Duration(segmentMS/10) * time.Millisecond
	if budget < floor {
		budget = floor
	}
	if budget > ceiling {
		budget = ceiling
	}
	return budget
}

// maxSourceSegmentSamples is how much decoded audio one participant-supplied
// segment may produce, from its own declared length.
//
// The deadline alone does not bound this. ffmpeg decodes far faster than real
// time, so a small compressed file that expands to hours of audio — or loops —
// emits gigabytes of PCM well inside any wall-clock budget, and the build dies
// on memory rather than on the timeout. The declared window is the only size
// the client committed to; anything past it plus generous slack is the file
// contradicting its own sidecar.
func maxSourceSegmentSamples(segmentMS int64, sampleRate int, timelineSamples int) int {
	const slackMS = 60_000
	if segmentMS < 0 {
		segmentMS = 0
	}
	limit := expectedPCMSamples(segmentMS+slackMS, sampleRate)
	if limit <= 0 {
		// A segment that declares no length still gets a floor rather than a
		// blank cheque.
		limit = expectedPCMSamples(slackMS, sampleRate)
	}
	// The recording is the real bound, and the only one not chosen by the
	// participant. A segment cannot legitimately contain more audio than the
	// meeting it came from, however long that meeting was, and a declaration
	// that under-reports is still allowed everything the timeline can hold. A
	// fixed constant could only be wrong in one direction or the other.
	if ceiling := timelineSamples + expectedPCMSamples(slackMS, sampleRate); timelineSamples > 0 && limit > ceiling {
		limit = ceiling
	}
	return limit
}

func decodeSourceSegment(ctx context.Context, path string, sampleRate int, timeout time.Duration, maxSamples int) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error",
		"-i", path,
		"-vn", "-sn", "-dn",
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-f", "s16le",
		"-",
	)
	samples, err := runPCM16LECommandBounded(cmd, 0, maxSamples)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("decode exceeded its %s budget; treating the upload as unusable", timeout)
	}
	return samples, err
}

// recordingWallWindow is the wall-clock span this recording covers.
//
// Timeline zero is derived, not observed: a track whose first packet arrives
// thirty seconds in sits at FirstTimelineNS=30s, so its wall instant is thirty
// seconds AFTER the recording began. Taking the earliest first-packet time as
// zero shifted the whole window later in a recording where nobody spoke
// immediately, which could then select a later call in the same room.
func recordingWallWindow(streams []AudioStream, timelineMS int64) (int64, int64) {
	var zero int64
	for _, stream := range streams {
		if !stream.TimeBase.Known {
			continue
		}
		candidate := stream.TimeBase.FirstPacketWallMS - stream.TimeBase.FirstTimelineNS/1e6
		if zero == 0 || candidate < zero {
			zero = candidate
		}
	}
	if zero <= 0 {
		return 0, 0
	}
	return zero, zero + timelineMS
}

// writeWAV16 writes mono 16-bit PCM. Hand-rolled rather than piped through
// ffmpeg because the render is already in memory as float32 and a subprocess
// buys nothing: the header is 44 fixed bytes and the conversion is a clamp.
func writeWAV16(path string, samples []float32, sampleRate int) error {
	dataBytes := len(samples) * 2
	header := make([]byte, 0, 44)
	le32 := func(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }
	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }
	header = append(header, "RIFF"...)
	header = append(header, le32(uint32(36+dataBytes))...)
	header = append(header, "WAVEfmt "...)
	header = append(header, le32(16)...)
	header = append(header, le16(1)...) // PCM
	header = append(header, le16(1)...) // mono
	header = append(header, le32(uint32(sampleRate))...)
	header = append(header, le32(uint32(sampleRate*2))...) // byte rate
	header = append(header, le16(2)...)                    // block align
	header = append(header, le16(16)...)                   // bits per sample
	header = append(header, "data"...)
	header = append(header, le32(uint32(dataBytes))...)

	body := make([]byte, dataBytes)
	for i, sample := range samples {
		// 32768, matching the decoder, and rounded rather than truncated.
		//
		// This file is now a SPLICE: most of it is the participant's recorded
		// track, decoded by readPCM16LEFloatsBounded as s16/32768 and expected
		// to survive untouched wherever no upload was laid over it. Scaling
		// back by 32767 and truncating turned every one of those samples into a
		// slightly different one — 16384 came back as 16383, and ±1 came back
		// as zero — so "the recorded audio is unchanged outside the overlaid
		// windows" was true in memory and false in the file the transcription
		// pass actually reads. The pair is exact in both directions now, and
		// only the single value +1.0 has to be clamped because int16 has no
		// positive counterpart to -32768.
		v := float64(sample)
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		scaled := math.Round(v * 32768)
		if scaled > 32767 {
			scaled = 32767
		}
		s := int16(scaled)
		body[i*2] = byte(uint16(s))
		body[i*2+1] = byte(uint16(s) >> 8)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create wav: %w", err)
	}
	// Sync and check the close. Everything else in this file treats a failure
	// as a skip that leaves the recorded track in place, but a WAV that was
	// truncated by a full disk and never reported would be handed to the
	// transcription pass as if it were whole, and that failure is fatal to the
	// build. A meeting must not be lost to a feature that is only ever meant
	// to improve one.
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	if _, err := f.Write(header); err != nil {
		return fmt.Errorf("write wav header: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write wav body: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync wav: %w", err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("close wav: %w", err)
	}
	return nil
}

// recordedParticipantFloats decodes everything this participant contributed to
// the recording, onto the meeting timeline, as the floor the splice lays audio
// over.
//
// EVERY one of their streams, summed, because the splice goes to exactly one of
// them and the rest are dropped from transcription. Taking only the chosen
// stream would make a rejoin's second track vanish from the transcript in every
// window the upload does not cover — which is precisely the loss the splice
// exists to stop. Their streams are disjoint in time (a rejoin is a new RTP
// identity for a span the previous one had ended), so summing places rather
// than mixes.
func recordedParticipantFloats(mkvPath string, streams []AudioStream, participantID string, sampleRate, outSamples int) ([]float32, error) {
	out := make([]float32, outSamples)
	var decoded bool
	for i := range streams {
		if streams[i].ParticipantID != participantID {
			continue
		}
		samples, err := ExtractStreamFloatsAt(mkvPath, streams[i], sampleRate)
		if err != nil {
			return nil, fmt.Errorf("decode recorded track for %s: %w", streams[i].SpeakerLabel, err)
		}
		decoded = true
		for j := 0; j < len(samples) && j < outSamples; j++ {
			out[j] += samples[j]
		}
	}
	if !decoded {
		return nil, fmt.Errorf("no recorded track for %s", participantID)
	}
	return out, nil
}

// ApplySourceAudio splices each speaker's own browser-captured audio over their
// recorded track, wherever a segment was uploaded and can be placed.
//
// It mutates streams in place and returns what it did, for the manifest. Every
// failure is a skip, never an error: the recorded track is always still there,
// and a meeting must publish whether or not anybody's upload arrived, decoded
// or fitted.
//
// A participant with no track in this recording is ignored even if they
// uploaded. Being a member of the room is what the upload endpoint can check;
// having actually been in THIS call is what a matching track proves, and that
// is the check that belongs here — and with a splice it is load-bearing twice
// over, because a participant with no recorded track has no floor to splice on.
func ApplySourceAudio(ctx context.Context, mkvPath string, streams []AudioStream, captureRoot, roomToken, workDir string, sampleRate int, timelineMS int64, stdout io.Writer) []SourceRenderReport {
	windowStartMS, windowEndMS := recordingWallWindow(streams, timelineMS)
	captures, err := DiscoverSourceCaptures(captureRoot, roomToken, windowStartMS, windowEndMS)
	if err != nil {
		fmt.Fprintf(stdout, "  source audio: cannot scan %s: %v\n", captureRoot, err)
		return nil
	}
	if len(captures) == 0 {
		return nil
	}
	outSamples := int(timelineMS * int64(sampleRate) / 1000)
	if outSamples <= 0 {
		return nil
	}
	var reports []SourceRenderReport
	// One participant can own several MKV streams: a rejoin, or a rotation
	// where the RTP identity changed mid-call. The spliced track spans the
	// WHOLE meeting timeline and already contains all of their recorded audio,
	// so handing the same file to each of those streams would transcribe that
	// participant once per stream and put every word in the transcript two or
	// three times. It goes to exactly one stream, and that participant's other
	// streams are dropped from transcription.
	used := map[string]bool{}
	// A participant whose splice already failed is not retried per stream: the
	// failure is a property of their capture, not of the stream we happened to
	// try it on, and re-decoding a meeting's audio to fail identically is pure
	// cost.
	failed := map[string]bool{}
	// A participant with ANY stream lacking a wall-clock base is refused up
	// front. Deciding inside the loop made the outcome depend on which of their
	// streams came first: an anchorless stream seen last was harmless, the same
	// stream seen first abandoned the splice.
	for i := range streams {
		if streams[i].ParticipantID != "" && !streams[i].TimeBase.Known {
			failed[streams[i].ParticipantID] = true
		}
	}
	for i := range streams {
		stream := &streams[i]
		dirs, ok := captures[stream.ParticipantID]
		if !ok || stream.ParticipantID == "" {
			continue
		}
		if failed[stream.ParticipantID] {
			continue
		}
		if used[stream.ParticipantID] {
			stream.SuppressTranscription = true
			fmt.Fprintf(stdout, "  source audio: %s has another stream already covered by their spliced track; not transcribing it twice\n", stream.SpeakerLabel)
			continue
		}
		if !stream.TimeBase.Known {
			// Marked failed, not merely skipped, and decided the same way
			// whichever order the streams arrive in: the loop above computed it
			// over all of them before this one began.
			//
			// Without it the participant's OTHER stream could still splice —
			// the spliced track spans the whole timeline, including this
			// stream's span — while this one keeps its recorded audio and is
			// transcribed alongside it, putting every word said here into the
			// transcript twice.
			failed[stream.ParticipantID] = true
			fmt.Fprintf(stdout, "  source audio: %s uploaded, but this recording carries no wall-clock base for their track; keeping the recorded audio\n", stream.SpeakerLabel)
			continue
		}
		recorded, err := recordedParticipantFloats(mkvPath, streams, stream.ParticipantID, sampleRate, outSamples)
		if err != nil {
			// No floor, no splice. The recorded track is what the transcription
			// pass would decode for itself anyway, so this costs the upload and
			// nothing else.
			failed[stream.ParticipantID] = true
			fmt.Fprintf(stdout, "  source audio: %s: %v; keeping the recorded audio\n", stream.SpeakerLabel, err)
			continue
		}
		samples, report, err := SpliceSourceTrack(ctx, recorded, dirs, stream.TimeBase, sampleRate, outSamples)
		report.SpeakerID = stream.SpeakerID
		if err != nil {
			// Nothing could be placed, so the splice would be a byte-for-byte
			// copy of the recorded track. Leave the stream alone rather than
			// write one, and let the reason travel into the manifest: a
			// transcript that quietly declined to use somebody's upload should
			// say why.
			report.Rejections = append(report.Rejections, err.Error())
			failed[stream.ParticipantID] = true
			fmt.Fprintf(stdout, "  source audio: %s: %v; keeping the recorded audio\n", stream.SpeakerLabel, err)
			reports = append(reports, report)
			continue
		}
		path := filepath.Join(workDir, "source-"+stream.SpeakerID+".wav")
		if err := writeWAV16(path, samples, sampleRate); err != nil {
			report.Rejections = append(report.Rejections, err.Error())
			failed[stream.ParticipantID] = true
			fmt.Fprintf(stdout, "  source audio: %s: %v; keeping the recorded audio\n", stream.SpeakerLabel, err)
			reports = append(reports, report)
			continue
		}
		stream.SourceAudioPath = path
		used[stream.ParticipantID] = true
		fmt.Fprintf(stdout, "  source audio: %s transcribing from participant capture spliced over %d ms of their recorded track (%d/%d segments, %d skipped, %d anchors, %.1f ms residual, %.0f ppm drift)\n",
			stream.SpeakerLabel, report.SplicedMS, report.Placed, report.Segments, report.Skipped, report.Anchors, report.ResidualMS, report.RatePPM)
		reports = append(reports, report)
	}
	return reports
}
