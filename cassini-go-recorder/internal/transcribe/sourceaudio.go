package transcribe

import (
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

// SourceTimeBase is the recorder's side of the mapping, read from the MKV
// stream tags the remux writes.
type SourceTimeBase struct {
	// FirstPacketWallMS is when this track's first packet arrived, in Unix
	// milliseconds, and FirstTimelineNS is where that instant sits on the
	// meeting timeline. Together they map recorder wall time to meeting time.
	FirstPacketWallMS int64
	FirstTimelineNS   int64
	// ClockRate is the track's RTP clock rate, used to read the client's
	// anchors as a media-time axis. Janus does not change the rate, only the
	// offset, so this stays correct for the sender's clock.
	ClockRate uint32
	Known     bool
}

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

// RenderOntoTimeline resamples one segment's PCM onto the meeting timeline.
//
// Pure, and deliberately not delegated to ffmpeg: the rate correction here is a
// handful of parts per million, which is fiddly to express as a filter graph
// and trivial to state directly. Linear interpolation is more than enough at
// that ratio — the correction moves a sample by a small fraction of a sample.
//
// Samples outside the segment stay zero. The pipeline already treats a
// participant's absent spans as digital silence on the shared timeline (see
// decodeTrackWithSparseGaps), so this matches what the rest of the code expects
// and keeps the attribution envelope's Present logic honest.
func RenderOntoTimeline(src []float32, sampleRate int, placement Placement, outSamples int) []float32 {
	out := make([]float32, outSamples)
	if len(src) == 0 || sampleRate <= 0 || placement.Rate <= 0 {
		return out
	}
	msPerSample := 1000.0 / float64(sampleRate)
	for j := range out {
		meetingMS := float64(j) * msPerSample
		localMS := (meetingMS - placement.OffsetMS) / placement.Rate
		pos := localMS / msPerSample
		if pos < 0 || pos >= float64(len(src)-1) {
			continue
		}
		i := int(pos)
		frac := float32(pos - float64(i))
		out[j] = src[i]*(1-frac) + src[i+1]*frac
	}
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
// should say so.
type SourceRenderReport struct {
	SpeakerID  string   `json:"speaker_id"`
	Owner      string   `json:"owner"`
	Segments   int      `json:"segments"`
	Placed     int      `json:"placed"`
	Anchors    int      `json:"anchors"`
	ResidualMS float64  `json:"residual_ms"`
	RatePPM    float64  `json:"rate_ppm"`
	CoverageMS int64    `json:"coverage_ms"`
	Rejections []string `json:"rejections,omitempty"`
}

// RenderSourceTrack builds one speaker's timeline-aligned PCM from every
// capture they uploaded for this recording, or reports why it could not.
//
// All or nothing, deliberately. Skipping an unplaceable segment and keeping the
// rest looked conservative and was the opposite: the caller substitutes this
// render for the speaker AND drops their recorded streams, so a skipped segment
// became silence where words had been. Losing speech is the one outcome worse
// than not using the capture at all, so any segment that cannot be placed
// fails the whole speaker back to the recorded track.
//
// Several directories because one participant can legitimately have more than
// one capture for a single recording — they left and rejoined. Rendering only
// the newest while suppressing their recorded streams lost the earlier
// session the same way.
func RenderSourceTrack(dirs []string, base SourceTimeBase, sampleRate int, outSamples int) ([]float32, SourceRenderReport, error) {
	report := SourceRenderReport{}
	out := make([]float32, outSamples)
	timelineMS := int64(outSamples) * 1000 / int64(sampleRate)
	var totalAnchors int
	var worstResidual float64
	var rateSum float64

	for _, dir := range dirs {
		sidecar, err := LoadSourceSidecar(dir)
		if err != nil {
			return nil, report, fmt.Errorf("%s: %w", filepath.Base(dir), err)
		}
		report.Owner = sidecar.OwnerUserID
		report.Segments += len(sidecar.Segments)
		for _, segment := range sidecar.Segments {
			placement, err := FitPlacement(segment, base)
			if err != nil {
				return nil, report, fmt.Errorf("segment %d: %w", segment.Index, err)
			}
			if !PlausibleOffset(placement, timelineMS) {
				return nil, report, fmt.Errorf(
					"segment %d places at %.0f ms, outside a %d ms recording — the uploader's clock is not usable",
					segment.Index, placement.OffsetMS, timelineMS)
			}
			samples, err := decodeSourceSegment(filepath.Join(dir, segment.AudioName), sampleRate)
			if err != nil {
				return nil, report, fmt.Errorf("segment %d: decode: %w", segment.Index, err)
			}
			placed := RenderOntoTimeline(samples, sampleRate, placement, outSamples)
			for i := range out {
				// Segments never overlap in time (each starts when the previous
				// stopped), so summing is a placement, not a mix.
				out[i] += placed[i]
			}
			report.Placed++
			totalAnchors += placement.Anchors
			rateSum += placement.Rate
			if placement.ResidualMS > worstResidual {
				worstResidual = placement.ResidualMS
			}
			report.CoverageMS += int64(float64(len(samples)) * 1000 / float64(sampleRate))
		}
	}

	if report.Placed == 0 {
		return nil, report, fmt.Errorf("no segment to place")
	}
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
func decodeSourceSegment(path string, sampleRate int) ([]float32, error) {
	cmd := exec.Command("ffmpeg",
		"-v", "error",
		"-i", path,
		"-vn", "-sn", "-dn",
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-f", "s16le",
		"-",
	)
	return runPCM16LECommand(cmd, 0)
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
		v := sample
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		s := int16(v * 32767)
		body[i*2] = byte(uint16(s))
		body[i*2+1] = byte(uint16(s) >> 8)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create wav: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(header); err != nil {
		return fmt.Errorf("write wav header: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("write wav body: %w", err)
	}
	return nil
}

// ApplySourceAudio replaces each speaker's transcription input with their own
// browser-captured audio where one was uploaded and can be placed.
//
// It mutates streams in place and returns what it did, for the manifest. Every
// failure is a skip, never an error: the recorded track is always still there,
// and a meeting must publish whether or not anybody's upload arrived, decoded
// or fitted.
//
// A participant with no track in this recording is ignored even if they
// uploaded. Being a member of the room is what the upload endpoint can check;
// having actually been in THIS call is what a matching track proves, and that
// is the check that belongs here.
func ApplySourceAudio(streams []AudioStream, captureRoot, roomToken, workDir string, sampleRate int, timelineMS int64, stdout io.Writer) []SourceRenderReport {
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
	// where the RTP identity changed mid-call. The rendered source track spans
	// the WHOLE meeting timeline, so handing the same file to each of those
	// streams would transcribe that participant once per stream and put every
	// word in the transcript two or three times. It goes to exactly one stream,
	// and that participant's other streams are dropped from transcription —
	// their audio is already inside the source render.
	used := map[string]bool{}
	// A participant whose render already failed is not retried per stream: the
	// failure is a property of their capture, not of the stream we happened to
	// try it on, and re-decoding a meeting's audio to fail identically is pure
	// cost.
	failed := map[string]bool{}
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
			fmt.Fprintf(stdout, "  source audio: %s has another stream already covered by their capture; not transcribing it twice\n", stream.SpeakerLabel)
			continue
		}
		if !stream.TimeBase.Known {
			fmt.Fprintf(stdout, "  source audio: %s uploaded, but this recording carries no wall-clock base for their track; keeping the recorded audio\n", stream.SpeakerLabel)
			continue
		}
		samples, report, err := RenderSourceTrack(dirs, stream.TimeBase, sampleRate, outSamples)
		report.SpeakerID = stream.SpeakerID
		if err != nil {
			// The whole speaker stays on the recorded track, and the reason
			// travels into the manifest: a transcript that quietly declined to
			// use somebody's upload should say why.
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
		fmt.Fprintf(stdout, "  source audio: %s transcribing from participant capture (%d/%d segments, %d anchors, %.1f ms residual, %.0f ppm drift)\n",
			stream.SpeakerLabel, report.Placed, report.Segments, report.Anchors, report.ResidualMS, report.RatePPM)
		reports = append(reports, report)
	}
	return reports
}
