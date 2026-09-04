package transcribe

import (
	"bufio"
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

// overlayWindow is where a segment's audio lands on the output timeline: the
// half-open window [start, stop) of output samples the placement covers.
//
// The two boundaries are decided here and nowhere else, for every renderer.
// The window opens at the first output sample whose position inside the segment
// is non-negative, and closes at the first one that would need a sample past
// the end of the segment. So each output sample is written by at most one
// segment, no instant is represented twice, and none is skipped between the
// recorded track and the upload.
//
// The closed form is inverted algebra, but the boundary is then walked against
// the very expression the renderer evaluates per sample. Trusting the ceil
// alone would let a rounding difference between the two put one sample inside
// the window for the mix and outside it for the transcript, which is exactly
// the disagreement this function exists to prevent.
func overlayWindow(placement Placement, sampleRate, srcSamples, dstSamples int) (int, int) {
	if dstSamples <= 0 || srcSamples < 2 || sampleRate <= 0 || placement.Rate <= 0 {
		return 0, 0
	}
	msPerSample := 1000.0 / float64(sampleRate)
	// The source position the renderer will ask for at output sample j.
	pos := func(j int) float64 {
		return ((float64(j)*msPerSample - placement.OffsetMS) / placement.Rate) / msPerSample
	}
	// The first output sample at or after the segment's own start. Ceil rather
	// than round: rounding down would ask for a source position just before
	// sample zero, which is audio the segment does not have.
	start := int(math.Ceil(placement.OffsetMS / msPerSample))
	if start < 0 {
		start = 0
	}
	if start >= dstSamples {
		return 0, 0
	}
	stop := int(math.Ceil((placement.OffsetMS + placement.Rate*float64(srcSamples)*msPerSample) / msPerSample))
	if stop > dstSamples {
		stop = dstSamples
	}
	if stop < start {
		stop = start
	}
	for stop > start && pos(stop-1) >= float64(srcSamples) {
		stop--
	}
	for stop < dstSamples && pos(stop) < float64(srcSamples) {
		stop++
	}
	if stop <= start {
		return 0, 0
	}
	return start, stop
}

// minFadeSamples is the shortest ramp that is a fade rather than a step.
//
// The head weight is (head+1)/fadeSamples, so a one-sample fade puts its very
// first sample at full weight: the "crossfade" is then a full-amplitude jump
// from the recorded track to the upload, which is precisely the click the fade
// exists to remove. Two samples is the shortest ramp that starts below full
// weight, and with the fade at both ends that makes four output samples the
// shortest window a crossfade can be applied to at all.
const minFadeSamples = 2

// effectiveFade is how long a fade can be inside a window of `length` output
// samples: the requested length, or half the window when the window is too
// short to carry two of them.
//
// It returns less than minFadeSamples for a window that cannot be faded, and
// the callers then leave that window alone. Skipping rather than scaling
// further down, because there is nothing left to scale to: at three output
// samples or fewer, every weight the formula can produce is 1, so "fading" is
// a full-amplitude step and the honest choice between a step and no splice is
// no splice. What is given up is at most three samples — 62 microseconds at
// 48 kHz — of a participant's upload, and what stands there instead is their
// recorded audio, which is where every other refusal in this file lands.
func effectiveFade(fadeSamples, length int) int {
	if fadeSamples <= 0 {
		return 0
	}
	if fadeSamples*2 > length {
		// Too short to fade both ends at full length. Half the window each way
		// still removes the discontinuity, which is the point.
		fadeSamples = length / 2
	}
	return fadeSamples
}

// overlayInto renders the part of [start, stop) that falls inside one chunk of
// the output timeline.
//
// Overlay, not sum. The destination is the participant's RECORDED track, so
// adding would play both copies of the same words at once; writing over it
// means the upload replaces the recorded audio exactly where it has audio, and
// nowhere else. That is the whole safety property of the splice: outside the
// window the recorded track is left byte-identical, so the result can never be
// worse than not ingesting at all.
//
// Pure arithmetic, deliberately not delegated to ffmpeg: the rate correction is
// a handful of parts per million, fiddly to express as a filter graph and
// trivial to state directly. Linear interpolation is more than enough at that
// ratio — the correction moves a sample by a small fraction of a sample.
//
// dst holds output samples [dstBase, dstBase+len(dst)); src holds source
// samples [srcBase, srcBase+len(src)) of a segment that is srcSamples long in
// total. Callers that hold the whole timeline pass zero for both bases.
//
// fadeSamples, when non-zero, ramps the upload in over the first fadeSamples of
// the window and back out over the last. What a seam otherwise carries is a
// discontinuity — a click where the two sources disagree about the
// participant's clock by a few milliseconds — which is the placement error the
// anchors and the wall-clock offset already determine, not something the
// overlay adds. The transcript never minded; a listener would.
func overlayInto(dst []float32, dstBase int, src []float32, srcBase int, start, stop int, placement Placement, sampleRate, srcSamples, fadeSamples int) {
	if len(dst) == 0 || len(src) == 0 || stop <= start || sampleRate <= 0 || placement.Rate <= 0 {
		return
	}
	if fadeSamples > 0 {
		fadeSamples = effectiveFade(fadeSamples, stop-start)
		if fadeSamples < minFadeSamples {
			// Nothing to overlay here: see effectiveFade. The caller asked for a
			// crossfade and this window cannot carry one, so the recorded floor
			// stands rather than being stepped over.
			return
		}
	}
	msPerSample := 1000.0 / float64(sampleRate)
	from := start
	if from < dstBase {
		from = dstBase
	}
	to := stop
	if to > dstBase+len(dst) {
		to = dstBase + len(dst)
	}
	for j := from; j < to; j++ {
		localMS := (float64(j)*msPerSample - placement.OffsetMS) / placement.Rate
		pos := localMS / msPerSample
		if pos < 0 {
			// Only reachable when the segment starts before the timeline does;
			// those samples belong to audio outside the recording.
			continue
		}
		if pos >= float64(srcSamples) {
			break
		}
		i := int(pos)
		var value float32
		if i >= srcSamples-1 {
			// The last sample has no partner to interpolate towards. Taking it
			// as it is rather than stopping one short: the window is supposed
			// to hold every sample the segment decoded, and leaving the final
			// one to the recorded track underneath is both a dropped sample and
			// a discontinuity at the seam.
			idx := srcSamples - 1 - srcBase
			if idx < 0 || idx >= len(src) {
				continue
			}
			value = src[idx]
		} else {
			lo := i - srcBase
			if lo < 0 || lo+1 >= len(src) {
				continue
			}
			frac := float32(pos - float64(i))
			value = src[lo]*(1-frac) + src[lo+1]*frac
		}
		if fadeSamples > 0 {
			weight := float32(1)
			if head := j - start; head < fadeSamples {
				weight = float32(head+1) / float32(fadeSamples)
			}
			if tail := stop - j; tail <= fadeSamples {
				if w := float32(tail) / float32(fadeSamples); w < weight {
					weight = w
				}
			}
			value = dst[j-dstBase]*(1-weight) + value*weight
		}
		dst[j-dstBase] = value
	}
}

// overlayOntoTimeline writes one segment's resampled PCM over `dst`, in place,
// and reports the half-open output window [start, stop) it wrote. The
// whole-timeline form of overlayInto, with hard edges: it is what the placement
// tests and RenderOntoTimeline ask about, and the oracle the chunked file
// renderer is checked against.
func overlayOntoTimeline(dst, src []float32, sampleRate int, placement Placement) (int, int) {
	start, stop := overlayWindow(placement, sampleRate, len(src), len(dst))
	if stop <= start {
		return 0, 0
	}
	overlayInto(dst, 0, src, 0, start, stop, placement, sampleRate, len(src), 0)
	return start, stop
}

// overlayChunkSamples is how much of either timeline the file renderer holds at
// once. Four thousand samples is 85 ms at 48 kHz: small enough that the whole
// render is bounded by a few tens of kilobytes however long the meeting is,
// large enough that the per-chunk read and write costs disappear.
const overlayChunkSamples = 4096

// overlayFileWindow is overlayInto against a meeting-length WAV on disk,
// applied a chunk at a time.
//
// Same arithmetic, same boundaries, no timeline in memory. It reads and writes
// only inside [start, stop), so "the recorded floor is byte-identical outside
// the window" holds by construction rather than by argument: the bytes were
// never opened.
func overlayFileWindow(dst *wavFile, src *wavFile, srcSamples int, placement Placement, sampleRate, fadeSamples int) (int, int, error) {
	start, stop := overlayWindow(placement, sampleRate, srcSamples, dst.samples)
	if stop <= start {
		return 0, 0, nil
	}
	if fadeSamples > 0 && effectiveFade(fadeSamples, stop-start) < minFadeSamples {
		// The same refusal overlayInto makes, made here as well so the window
		// this reports is the window it wrote. A caller that was told [start,
		// stop) while the file was left untouched would put a splice in the
		// manifest, and in the published windows, that nobody can hear.
		return 0, 0, nil
	}
	msPerSample := 1000.0 / float64(sampleRate)
	pos := func(j int) float64 {
		return ((float64(j)*msPerSample - placement.OffsetMS) / placement.Rate) / msPerSample
	}
	dstBuf := make([]float32, overlayChunkSamples)
	srcBuf := make([]float32, 0, overlayChunkSamples*2)
	for j0 := start; j0 < stop; j0 += overlayChunkSamples {
		j1 := j0 + overlayChunkSamples
		if j1 > stop {
			j1 = stop
		}
		lo := int(math.Floor(pos(j0)))
		if lo < 0 {
			lo = 0
		}
		// One past the last source sample the interpolation can reach in this
		// chunk, plus its partner.
		hi := int(pos(j1-1)) + 2
		if hi > srcSamples {
			hi = srcSamples
		}
		if hi <= lo {
			hi = lo + 1
			if hi > srcSamples {
				break
			}
		}
		if cap(srcBuf) < hi-lo {
			srcBuf = make([]float32, hi-lo)
		}
		srcBuf = srcBuf[:hi-lo]
		if err := src.readSamples(lo, srcBuf); err != nil {
			return 0, 0, err
		}
		chunk := dstBuf[:j1-j0]
		if err := dst.readSamples(j0, chunk); err != nil {
			return 0, 0, err
		}
		overlayInto(chunk, j0, srcBuf, lo, start, stop, placement, sampleRate, srcSamples, fadeSamples)
		if err := dst.writeSamples(j0, chunk); err != nil {
			return 0, 0, err
		}
	}
	return start, stop, nil
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
	// MixSpliced says whether the PUBLISHED audio carries this splice too, so
	// that every word in the transcript can be heard in playback. False means
	// the mix kept the recorded track, and MixSkipReason says why.
	MixSpliced bool `json:"mix_spliced"`
	// MixSkipReason is set only when the transcript was spliced and the mix was
	// not — today, only the CASSINI_SOURCE_AUDIO_MIX=0 off switch. A speaker
	// whose upload was refused outright has neither, and their Rejections say
	// what happened.
	MixSkipReason string `json:"mix_skip_reason,omitempty"`
	// Windows is where on the meeting timeline each placed segment landed. One
	// entry per placed segment, not the merged union, because the question a
	// reader has is which part of what they are hearing came from an upload.
	Windows []SpliceWindow `json:"windows,omitempty"`
	// CrossfadeMS is how long the render takes to hand over at each window
	// edge, and RenderHz the rate it was made at. Both are here so that "the
	// published audio and the transcript are the same samples" is a claim the
	// manifest states rather than one a reader has to take on trust.
	CrossfadeMS int `json:"crossfade_ms"`
	RenderHz    int `json:"render_hz"`
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

// mixRenderHz is the rate the splice renders at, and it is not a free choice:
// it is the rate the published mix is encoded from, so that the transcript's
// audio and the published audio can be the same samples rather than two
// renders that agree only approximately. The transcription input is a 16 kHz
// resample of this file, made after the mix is encoded.
const mixRenderHz = 48000

// mixSpliceCrossfadeMS is how long the upload takes to replace, and give back,
// the recorded track at each end of a window.
//
// Fifteen milliseconds. The transcript never cared about a hard edge — a
// decoder does not click — but a listener does, and the two sources differ at
// the seam by whatever the placement got wrong (about a millisecond and a half
// of anchor residual, plus however far the participant's clock sits from the
// recorder's). Linear rather than equal-power: the two sides are the same voice
// a few milliseconds apart, so they are correlated, and an equal-power curve
// would bump the level in the middle of the fade instead of holding it.
const mixSpliceCrossfadeMS = 15

// SpliceWindow is one stretch of the meeting timeline a segment replaced, in
// milliseconds. Reported per placed segment rather than merged, because the
// question a reader has is "which part of what I am hearing came from whose
// upload"; SplicedMS is the union of these, which is the different question of
// how much of the timeline the capture is answerable for.
type SpliceWindow struct {
	FromMS int64 `json:"from_ms"`
	ToMS   int64 `json:"to_ms"`
	// Capture and Segment name which upload this came from. Both, because
	// segment indexes restart at zero in every capture: a participant who
	// rejoined uploads one capture per session, and "segment 0" alone would
	// name two different stretches of audio.
	Capture int `json:"capture"`
	Segment int `json:"segment"`
}

// markUnused says the splice was not used after all: neither the published mix
// nor the transcript carries it, and the recorded track stands.
//
// The counters go with it. A report that still claimed placed segments and
// spliced milliseconds would tell a reader that some of what they are hearing
// came from an upload, when the render it came from was deleted and nothing
// downstream ever saw it. What survives is the diagnosis — how many segments
// arrived, what the capture declared, and why it went unused.
func (r *SourceRenderReport) markUnused(reason string) {
	r.MixSpliced = false
	r.MixSkipReason = ""
	// Every segment that arrived went unused, however far the render got before
	// it was thrown away. Leaving Skipped where it was would leave the placed
	// ones unaccounted for: two segments, none placed, none skipped.
	r.Skipped = r.Segments
	r.Placed = 0
	r.SplicedMS = 0
	r.CoverageMS = 0
	r.Anchors = 0
	r.ResidualMS = 0
	r.RatePPM = 0
	r.CrossfadeMS = 0
	r.Windows = nil
	if reason != "" {
		r.Rejections = append(r.Rejections, reason)
	}
}

// renderSourceTrack lays every placeable segment a speaker uploaded over their
// own RECORDED track, in place, in the file the mix will encode from.
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
// what would have been transcribed — and played — without any upload. The
// result can never be worse than not ingesting, so there is nothing left to
// refuse.
//
// A segment that cannot be placed, cannot be decoded, or holds far less audio
// than it declares is skipped with its reason recorded, not fatal. Only a
// capture that contributes nothing at all is an error, so the caller can throw
// the render away and leave the recorded track exactly where it was.
//
// Several directories because one participant can legitimately have more than
// one capture for a single recording — they left and rejoined, or a reload was
// not adopted into the capture that followed it.
//
// This is the ONE placement decision in the pipeline. The published mix and the
// transcription input are both this file: the mix encodes it, and the 16 kHz
// track handed to the recogniser is a resample of it. There is no second
// placement to disagree with the first.
func renderSourceTrack(ctx context.Context, floor *wavFile, dirs []string, base SourceTimeBase, owner, scratchDir string, sampleRate, timelineSamples, fadeSamples int) (SourceRenderReport, error) {
	report := SourceRenderReport{Owner: owner, RenderHz: sampleRate}
	if sampleRate <= 0 || timelineSamples <= 0 {
		// An error rather than a division by zero. Production passes 48 kHz and
		// a measured timeline, but a caller that gets this wrong should be told
		// so rather than take the build down with a panic.
		return report, fmt.Errorf("splice needs a positive sample rate and timeline, got %d Hz and %d samples", sampleRate, timelineSamples)
	}
	if fadeSamples > 0 {
		report.CrossfadeMS = fadeSamples * 1000 / sampleRate
	}
	timelineMS := int64(timelineSamples) * 1000 / int64(sampleRate)
	var totalAnchors int
	var worstResidual float64
	var rateSum float64

	// Load every sidecar first, so a capture that cannot even be read costs no
	// ffmpeg. This IS fatal: an unreadable manifest says nothing about which
	// windows the rest of the capture may claim.
	loaded := make([]SourceSidecar, 0, len(dirs))
	for _, dir := range dirs {
		sidecar, err := LoadSourceSidecar(dir)
		if err != nil {
			return report, fmt.Errorf("%s: %w", filepath.Base(dir), err)
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

	segmentPath := filepath.Join(scratchDir, "segment.wav")
	defer os.Remove(segmentPath)

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
			decoded, err := decodeSourceSegmentToFile(ctx, filepath.Join(dir, segment.AudioName), segmentPath, sampleRate,
				sourceDecodeTimeout(segmentMS), maxSourceSegmentSamples(segmentMS, sampleRate, timelineSamples))
			if err != nil {
				skip(segment, "decode: %v", err)
				continue
			}
			decodedMS := int64(float64(decoded) * 1000 / float64(sampleRate))
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
			// The bound exactly, not expectedPCMSamples: that helper adds a
			// second of codec-tail allowance to a CAPACITY hint, which is right
			// for an allocation and wrong for a clamp that says how much
			// recorded audio may be replaced.
			if limit := int((segmentMS + segmentOverrunSlackMS) * int64(sampleRate) / 1000); limit > 0 && decoded > limit {
				report.Rejections = append(report.Rejections, fmt.Sprintf(
					"segment %d holds %d ms under a %d ms window; only the first %d ms of it was used",
					segment.Index, decodedMS, segmentMS, segmentMS+segmentOverrunSlackMS))
				decoded = limit
			}
			// Recomputed after any clamp above, so the report counts the audio
			// that was actually laid over the recorded track rather than
			// everything the file decoded to.
			decodedMS = int64(float64(decoded) * 1000 / float64(sampleRate))
			source, err := openWAVForRead(segmentPath)
			if err != nil {
				skip(segment, "decoded audio: %v", err)
				continue
			}
			from, to, err := overlayFileWindow(floor, source, decoded, placement, sampleRate, fadeSamples)
			closeErr := source.Close()
			if err != nil {
				// A read or write failure against the render is not a property
				// of this segment, and continuing would leave the file half
				// spliced with nobody the wiser. Give up on the speaker.
				return report, fmt.Errorf("segment %d: %w", segment.Index, err)
			}
			if closeErr != nil {
				return report, fmt.Errorf("segment %d: close decoded audio: %w", segment.Index, closeErr)
			}
			if to <= from {
				// Two different refusals reach here, and a reader of the
				// manifest needs them apart: a segment that lands nowhere on
				// this timeline, and one that lands on so few samples that the
				// crossfade would be a step. Asking overlayWindow again rather
				// than guessing from the geometry, so the two answers can never
				// drift from each other.
				if geomFrom, geomTo := overlayWindow(placement, sampleRate, decoded, floor.samples); geomTo > geomFrom {
					skip(segment, "lands on %d output sample(s), too few to hand over without a click",
						geomTo-geomFrom)
				} else {
					skip(segment, "places entirely outside the %d ms recording", timelineMS)
				}
				continue
			}
			report.Placed++
			report.Windows = append(report.Windows, SpliceWindow{
				FromMS:  int64(from) * 1000 / int64(sampleRate),
				ToMS:    int64(to) * 1000 / int64(sampleRate),
				Capture: i,
				Segment: segment.Index,
			})
			report.CoverageMS += decodedMS
			totalAnchors += placement.Anchors
			rateSum += placement.Rate
			if placement.ResidualMS > worstResidual {
				worstResidual = placement.ResidualMS
			}
		}
	}

	if report.Placed == 0 {
		return report, fmt.Errorf("no segment could be placed")
	}
	report.SplicedMS = splicedUnionMS(report.Windows)
	report.Anchors = totalAnchors
	report.ResidualMS = worstResidual
	report.RatePPM = Placement{Rate: rateSum / float64(report.Placed)}.RatePPMDeviation()
	return report, nil
}

// splicedUnionMS is how much of the timeline the upload actually replaced.
//
// A union rather than a sum: two captures for one recording can overlap (a
// rejoin whose windows meet, a page that filed its reload separately), and a
// later overlay merely overwrites an earlier one — so adding the lengths would
// claim more of the timeline was spliced than exists.
func splicedUnionMS(windows []SpliceWindow) int64 {
	if len(windows) == 0 {
		return 0
	}
	sorted := append([]SpliceWindow(nil), windows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FromMS < sorted[j].FromMS })
	var total, cursor int64
	for i, w := range sorted {
		if i > 0 && w.ToMS <= cursor {
			continue
		}
		from := w.FromMS
		if i > 0 && from < cursor {
			from = cursor
		}
		if w.ToMS <= from {
			continue
		}
		total += w.ToMS - from
		cursor = w.ToMS
	}
	return total
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
// on disk rather than on the timeout. The declared window is the only size
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

// decodeSegmentCopyChunkBytes is the staging buffer between ffmpeg's stdout and
// the decoded segment on disk. Nothing else in the render scales with the
// meeting, so this and the overlay chunks are the whole memory footprint.
const decodeSegmentCopyChunkBytes = 64 * 1024

// decodeSourceSegmentToFile decodes one uploaded segment to a mono WAV beside
// the render, and reports how many samples it holds.
//
// To a file rather than to memory, and that is the point. A segment can
// legitimately be as long as the meeting, and at the mix's rate a two-hour one
// is 1.4 GB of float32 — so the old in-memory decode, which was affordable at
// 16 kHz and merely wasteful, becomes the largest allocation in the build. The
// ceiling still applies, and still says the same thing when a file contradicts
// the window its own sidecar declared.
func decodeSourceSegmentToFile(ctx context.Context, path, outPath string, sampleRate int, timeout time.Duration, maxSamples int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"-v", "error",
		"-i", path,
		"-vn", "-sn", "-dn",
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
	}
	if maxSamples > 0 {
		// A second PAST the byte ceiling below, deliberately, so that the
		// ceiling is what a file exceeding its declared window meets: it is
		// refused with a reason rather than silently truncated to whatever
		// ffmpeg felt like emitting. This is the outer bound behind it — a
		// second line against an input an outsider chose, for the case where
		// the byte accounting is ever wrong.
		args = append(args, "-t", strconv.FormatFloat(float64(maxSamples)/float64(sampleRate)+1, 'f', 3, 64))
	}
	args = append(args, "-f", "s16le", "-")
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("open PCM pipe: %w", err)
	}
	var stderr boundedBuffer
	stderr.limit = 8192
	cmd.Stderr = &stderr

	out, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("create decoded segment: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()
	if err := writeWAVHeader(out, 0, sampleRate); err != nil {
		return 0, fmt.Errorf("write decoded segment header: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start ffmpeg: %w", err)
	}
	var written int64
	buf := make([]byte, decodeSegmentCopyChunkBytes)
	var copyErr error
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			if maxSamples > 0 && (written+int64(n))/2 > int64(maxSamples) {
				copyErr = fmt.Errorf(
					"decoded audio exceeds the %d samples this input declared; refusing to buffer more",
					maxSamples)
				break
			}
			if _, err := out.Write(buf[:n]); err != nil {
				copyErr = fmt.Errorf("write decoded segment: %w", err)
				break
			}
			written += int64(n)
		}
		if readErr != nil {
			if readErr != io.EOF {
				copyErr = fmt.Errorf("read decoded PCM: %w", readErr)
			}
			break
		}
	}
	if copyErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if copyErr != nil {
		return 0, copyErr
	}
	if waitErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, fmt.Errorf("decode exceeded its %s budget; treating the upload as unusable", timeout)
		}
		return 0, fmt.Errorf("ffmpeg: %w\n%s", waitErr, truncate(stderr.String(), 800))
	}
	if written%2 != 0 {
		return 0, fmt.Errorf("decoded PCM has odd byte count")
	}
	samples := int(written / 2)
	// The header was written before the length was known; correct it now so the
	// file is a WAV anything can open, this reader included.
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("rewind decoded segment: %w", err)
	}
	if err := writeWAVHeader(out, samples, sampleRate); err != nil {
		return 0, fmt.Errorf("finish decoded segment header: %w", err)
	}
	closed = true
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("close decoded segment: %w", err)
	}
	return samples, nil
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

// writeParticipantFloor writes everything this participant contributed to the
// recording, onto the meeting timeline, as the floor the splice lays audio
// over.
//
// EVERY one of their tracks, summed, because the splice replaces all of them
// with one render. Taking only the chosen track would make a rejoin's second
// one vanish from both the transcript and the published mix in every window the
// upload does not cover — which is precisely the loss the splice exists to
// stop. Their tracks are disjoint in time (a rejoin is a new RTP identity for a
// span the previous one had ended), so summing places rather than mixes.
//
// Zero-padded to the full timeline. A participant who left before the meeting
// ended has a shorter track, and an upload covering the time after that still
// has somewhere to land.
//
// Streamed a chunk at a time: this is a full-length 48 kHz timeline, and the
// whole point of the file-based render is that no such thing is ever a Go
// slice.
func writeParticipantFloor(trackPaths []string, timelineSamples, sampleRate int, outPath string) error {
	if len(trackPaths) == 0 {
		return fmt.Errorf("no recorded track to build a floor from")
	}
	tracks := make([]*wavFile, 0, len(trackPaths))
	defer func() {
		for _, track := range tracks {
			_ = track.Close()
		}
	}()
	for _, path := range trackPaths {
		track, err := openWAVForRead(path)
		if err != nil {
			return err
		}
		tracks = append(tracks, track)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create render: %w", err)
	}
	// Sync and check the close. Everything else in this file treats a failure
	// as a skip that leaves the recorded track in place, but a render that was
	// truncated by a full disk and never reported would be handed to the mix
	// and to the transcription pass as if it were whole, and that failure is
	// fatal to the build. A meeting must not be lost to a feature that is only
	// ever meant to improve one.
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()
	writer := bufio.NewWriterSize(out, decodeSegmentCopyChunkBytes)
	if err := writeWAVHeader(writer, timelineSamples, sampleRate); err != nil {
		return fmt.Errorf("write render header: %w", err)
	}
	sum := make([]float32, overlayChunkSamples)
	one := make([]float32, overlayChunkSamples)
	raw := make([]byte, overlayChunkSamples*2)
	for at := 0; at < timelineSamples; at += overlayChunkSamples {
		n := overlayChunkSamples
		if at+n > timelineSamples {
			n = timelineSamples - at
		}
		for i := 0; i < n; i++ {
			sum[i] = 0
		}
		for _, track := range tracks {
			if err := track.readSamples(at, one[:n]); err != nil {
				return err
			}
			for i := 0; i < n; i++ {
				sum[i] += one[i]
			}
		}
		for i := 0; i < n; i++ {
			s := uint16(s16FromFloat32(sum[i]))
			raw[i*2] = byte(s)
			raw[i*2+1] = byte(s >> 8)
		}
		if _, err := writer.Write(raw[:n*2]); err != nil {
			return fmt.Errorf("write render: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush render: %w", err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync render: %w", err)
	}
	closed = true
	if err := out.Close(); err != nil {
		return fmt.Errorf("close render: %w", err)
	}
	return nil
}

// resampleForTranscription writes the 16 kHz mono track the recogniser reads,
// from the very file the mix encodes.
//
// A resample rather than a second render, and this is what makes the published
// audio and the transcript the same splice rather than two that agree
// approximately. There is one placement decision, one set of windows, one
// crossfade; the recogniser simply hears it at a lower rate.
//
// Sixteen kilohertz in the bundle rather than forty-eight because this copy
// lives in the meeting bundle, which is kept, while the 48 kHz render dies with
// the mix's temporary directory. Three times the bytes, forever, to hand the
// recogniser samples it immediately downsamples would be a poor trade.
func resampleForTranscription(renderPath, outPath string) error {
	return runFFmpegQuiet(
		"-y",
		"-v", "error",
		"-i", renderPath,
		"-vn", "-sn", "-dn",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		outPath,
	)
}

// revertSourceAudio undoes ingestion for the whole build: every stream goes
// back to its recorded track, and every report says the upload went unused.
//
// It exists for one case — the spliced mix failed to encode — and it has to be
// all or nothing. Reverting the published audio alone would leave the
// transcript built from uploads that are not in the file people play back,
// which is the exact defect this branch was written to remove; and it would
// leave a rejoined participant's sibling streams suppressed while their
// recorded audio is back in the mix, so words audible in the meeting would be
// missing from the transcript entirely.
func revertSourceAudio(streams []AudioStream, reports []SourceRenderReport, bundleDir, reason string) {
	for i := range streams {
		// The rendered transcription input goes with it. A bundle that used no
		// upload must hold no upload: leaving a couple of hundred megabytes of
		// audio nothing references is both a surprise to whoever finds it and a
		// copy of a participant's recording living past its purpose.
		if streams[i].SourceAudioPath != "" {
			_ = os.Remove(streams[i].SourceAudioPath)
		}
		streams[i].SourceAudioPath = ""
		streams[i].SuppressTranscription = false
	}
	for i := range reports {
		if reports[i].Placed == 0 {
			// Already refused, for its own reason, before the mix was even
			// encoded. Adding the encode failure to it would claim this upload
			// had been in the spliced mix when it never was.
			continue
		}
		reports[i].markUnused(reason)
	}
	pruneSourceAudioWorkDir(bundleDir)
}

// pruneSourceAudioWorkDir removes the bundle's ingestion work directory when
// nothing was left in it, so a build that used no upload leaves the bundle a
// build with ingestion switched off would leave. os.Remove refuses a directory
// that still holds something, which is exactly the test wanted here.
func pruneSourceAudioWorkDir(bundleDir string) {
	if strings.TrimSpace(bundleDir) == "" {
		return
	}
	work := filepath.Join(bundleDir, "_work")
	_ = os.Remove(filepath.Join(work, "sourceaudio"))
	_ = os.Remove(work)
}

// mixSpliceEnabled reports whether the published mix carries the splice.
//
// An off switch for the published audio alone: with CASSINI_SOURCE_AUDIO_MIX=0
// the transcript is still built from the spliced render, and the mix is exactly
// what it would have been without ingestion. It exists so that a deployment
// that dislikes what the published audio sounds like can go back without
// turning off ingestion, which is the larger and more useful half of the
// feature.
func mixSpliceEnabled() (bool, string) {
	raw := strings.TrimSpace(os.Getenv(envMixSpliceEnabled))
	if raw == "" {
		return true, ""
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		// The convention the other two ingestion switches already follow: a
		// value that is neither true nor false lands OFF and says so, rather
		// than being read as "not exactly 0, so on" and quietly doing the thing
		// the administrator was trying to stop.
		return false, fmt.Sprintf("%s=%q is not a boolean, so it is read as off", envMixSpliceEnabled, raw)
	}
	if enabled {
		return true, ""
	}
	return false, fmt.Sprintf("disabled by configuration (%s=%s)", envMixSpliceEnabled, raw)
}

const envMixSpliceEnabled = "CASSINI_SOURCE_AUDIO_MIX"

// ApplySourceAudio splices each speaker's own browser-captured audio over their
// recorded track, wherever a segment was uploaded and can be placed, and feeds
// the result to both the published mix and the transcription pass.
//
// It mutates streams and the mix in place and returns what it did, for the
// manifest. Every failure is a skip, never an error: the recorded track is
// always still there, and a meeting must publish whether or not anybody's
// upload arrived, decoded or fitted.
//
// A participant with no track in this recording is ignored even if they
// uploaded. Being a member of the room is what the upload endpoint can check;
// having actually been in THIS call is what a matching track proves, and that
// is the check that belongs here — and with a splice it is load-bearing twice
// over, because a participant with no recorded track has no floor to splice on.
func ApplySourceAudio(ctx context.Context, mix *meetingMix, streams []AudioStream, captureRoot, roomToken, bundleDir string, stdout io.Writer) []SourceRenderReport {
	timelineSamples := mix.TimelineSamples
	timelineMS := int64(timelineSamples) * 1000 / int64(mixRenderHz)
	windowStartMS, windowEndMS := recordingWallWindow(streams, timelineMS)
	captures, err := DiscoverSourceCaptures(captureRoot, roomToken, windowStartMS, windowEndMS)
	if err != nil {
		fmt.Fprintf(stdout, "  source audio: cannot scan %s: %v\n", captureRoot, err)
		return nil
	}
	if len(captures) == 0 {
		return nil
	}
	if timelineSamples <= 0 {
		return nil
	}
	// What discovery actually selected, before anything is done with it.
	//
	// Every later line is per speaker, so a participant whose upload was never
	// found at all produced no output whatsoever — and "no line for Alice" then
	// looked exactly like a splice that failed silently, when the upload had
	// simply not been selected for this recording. One line here separates the
	// two without having to reason backwards from a build log.
	owners := make([]string, 0, len(captures))
	for owner, dirs := range captures {
		owners = append(owners, fmt.Sprintf("%s(%d)", owner, len(dirs)))
	}
	sort.Strings(owners)
	fmt.Fprintf(stdout, "  source audio: %d capture(s) selected for this recording over %d ms: %s\n",
		len(captures), timelineMS, strings.Join(owners, " "))
	spliceMix, mixSkipReason := mixSpliceEnabled()

	// The bundle's work directory is created on first use, never before it. A
	// build that finds no upload it can use — because nobody who uploaded was
	// in this call, or because every capture was refused — must leave the
	// bundle exactly as a build with ingestion switched off would, and an empty
	// _work/sourceaudio to explain to whoever finds it is not that.
	workDir := ""
	ensureWorkDir := func() (string, error) {
		if workDir != "" {
			return workDir, nil
		}
		path, err := WorkPath(bundleDir, "sourceaudio")
		if err == nil {
			err = os.MkdirAll(path, 0o755)
		}
		if err != nil {
			return "", fmt.Errorf("no work directory: %w", err)
		}
		workDir = path
		return workDir, nil
	}

	// One participant can own several MKV streams: a rejoin, or a rotation
	// where the RTP identity changed mid-call. The render spans the WHOLE
	// meeting timeline and already contains all of their recorded audio, so it
	// replaces every one of those streams at once — in the mix, where feeding
	// the siblings to amix as well would play their words twice, and in the
	// transcription pass, where transcribing both would write every word twice.
	participantStreams := map[string][]int{}
	for i := range streams {
		if streams[i].ParticipantID == "" {
			continue
		}
		participantStreams[streams[i].ParticipantID] = append(participantStreams[streams[i].ParticipantID], i)
	}
	// A participant with ANY stream lacking a wall-clock base is refused up
	// front. Deciding inside the loop made the outcome depend on which of their
	// streams came first: an anchorless stream seen last was harmless, the same
	// stream seen first abandoned the splice.
	var reports []SourceRenderReport
	done := map[string]bool{}
	// Owners whose upload found a track in this recording. The rest uploaded
	// for a call they were not recorded in, which is a legitimate outcome and
	// silent today; saying so is what tells it apart from a splice that failed.
	matched := map[string]bool{}
	for i := range streams {
		stream := &streams[i]
		dirs, ok := captures[stream.ParticipantID]
		if !ok || stream.ParticipantID == "" || done[stream.ParticipantID] {
			continue
		}
		matched[stream.ParticipantID] = true
		done[stream.ParticipantID] = true
		idxs := participantStreams[stream.ParticipantID]
		anchorless := false
		for _, idx := range idxs {
			if !streams[idx].TimeBase.Known {
				anchorless = true
			}
		}
		if anchorless {
			// Without it the participant's OTHER stream could still splice —
			// the render spans the whole timeline, including this stream's span
			// — while this one keeps its recorded audio and is transcribed
			// alongside it, putting every word said here into the transcript
			// twice.
			fmt.Fprintf(stdout, "  source audio: %s uploaded, but this recording carries no wall-clock base for their track; keeping the recorded audio\n", stream.SpeakerLabel)
			continue
		}

		renderPath := mix.RenderPath(stream.SpeakerID)
		report := SourceRenderReport{
			SpeakerID: stream.SpeakerID,
			// The join key, so a failure before any sidecar was read still says
			// whose upload went unused.
			Owner:    stream.ParticipantID,
			RenderHz: mixRenderHz,
		}
		transcriptPath := ""
		// Every failure below ends here: the render and any half-written
		// transcription input are removed, the report is stripped of the
		// splice it did not deliver, and the speaker keeps their recorded audio
		// in the published mix and in the transcript alike. A report still
		// claiming placed segments would tell a reader that part of what they
		// are hearing came from an upload, when nothing downstream ever saw it.
		keepRecorded := func(err error) {
			report.markUnused(err.Error())
			_ = os.Remove(renderPath)
			if transcriptPath != "" {
				_ = os.Remove(transcriptPath)
			}
			fmt.Fprintf(stdout, "  source audio: %s: %v; keeping the recorded audio\n", stream.SpeakerLabel, err)
			reports = append(reports, report)
		}

		if err := writeParticipantFloor(mix.TrackPaths(idxs), timelineSamples, mixRenderHz, renderPath); err != nil {
			// No floor, no splice. The recorded track is what the mix and the
			// transcription pass would use anyway, so this costs the upload and
			// nothing else.
			keepRecorded(err)
			continue
		}
		floor, err := openWAV(renderPath)
		if err != nil {
			keepRecorded(err)
			continue
		}
		// The decoded segment is scratch that belongs with the render, not with
		// the bundle: putting it beside the tracks keeps the bundle's work
		// directory for the one file that has to survive the build.
		rendered, err := renderSourceTrack(ctx, floor, dirs, stream.TimeBase, stream.ParticipantID, mix.dir,
			mixRenderHz, timelineSamples, mixSpliceCrossfadeMS*mixRenderHz/1000)
		syncErr := floor.f.Sync()
		closeErr := floor.Close()
		rendered.SpeakerID = stream.SpeakerID
		report = rendered
		if err == nil && syncErr != nil {
			err = fmt.Errorf("sync render: %w", syncErr)
		}
		if err == nil && closeErr != nil {
			err = fmt.Errorf("close render: %w", closeErr)
		}
		if err != nil {
			// Nothing could be placed, or the render failed part way through.
			// Either way the recorded track is untouched, so throw the render
			// away and let the reason travel into the manifest: a transcript
			// that quietly declined to use somebody's upload should say why.
			keepRecorded(err)
			continue
		}

		dir, err := ensureWorkDir()
		if err != nil {
			keepRecorded(err)
			continue
		}
		transcriptPath = filepath.Join(dir, "source-"+stream.SpeakerID+".wav")
		if err := resampleForTranscription(renderPath, transcriptPath); err != nil {
			keepRecorded(err)
			continue
		}
		for _, idx := range idxs {
			streams[idx].SuppressTranscription = true
		}
		stream.SourceAudioPath = transcriptPath
		stream.SuppressTranscription = false
		if spliceMix {
			mix.Substitute(idxs, renderPath)
			report.MixSpliced = true
		} else {
			report.MixSkipReason = mixSkipReason
		}
		if len(idxs) > 1 {
			fmt.Fprintf(stdout, "  source audio: %s has %d streams in this recording; one render covers them all\n",
				stream.SpeakerLabel, len(idxs))
		}
		// This line's prefix is matched verbatim by the browser-capture CI leg.
		fmt.Fprintf(stdout, "  source audio: %s transcribing from participant capture spliced over %d ms of their recorded track (%d/%d segments, %d skipped, %d anchors, %.1f ms residual, %.0f ppm drift)\n",
			stream.SpeakerLabel, report.SplicedMS, report.Placed, report.Segments, report.Skipped, report.Anchors, report.ResidualMS, report.RatePPM)
		if report.MixSpliced {
			fmt.Fprintf(stdout, "  source audio: %s: the published mix carries the same splice (%d window(s), %d ms crossfade)\n",
				stream.SpeakerLabel, len(report.Windows), report.CrossfadeMS)
		} else {
			fmt.Fprintf(stdout, "  source audio: %s: the published mix keeps the recorded track (%s)\n",
				stream.SpeakerLabel, report.MixSkipReason)
		}
		reports = append(reports, report)
	}
	for owner := range captures {
		if !matched[owner] {
			fmt.Fprintf(stdout, "  source audio: %s uploaded for this call but has no track in the recording; nothing to splice onto\n", owner)
		}
	}
	// A run that created the work directory and then had every render fail must
	// still leave the bundle as a build with ingestion off would.
	pruneSourceAudioWorkDir(bundleDir)
	return reports
}
