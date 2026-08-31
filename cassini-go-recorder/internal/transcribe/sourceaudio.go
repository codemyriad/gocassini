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
// # Why this does not correlate audio
//
// The obvious way to align an upload is to cross-correlate it against the
// recorder's copy of the same speaker. That fails exactly when the feature
// matters: if the uplink was bad, the reference is full of holes.
//
// Instead the client reports the RTP timestamps of the frames it encoded, and
// the recorder now records the RTP timestamp its first packet of each stream
// carried (remux.StreamPlan.FirstRTPTimestamp, emitted into the MKV as
// first_rtp_timestamp). Those are the same clock — the sender's own 48 kHz
// audio sample clock — so a timestamp converts to a meeting-timeline position
// by arithmetic:
//
//	timelineMS(rtp) = first_timeline_ns/1e6 + (rtp - first_rtp_timestamp)*1000/clock_rate
//
// Loss does not weaken this. The base comes from a packet that arrived, and
// every anchor the client reports is on the sender's clock whether or not that
// particular packet made it. Ten percent loss and ninety percent loss produce
// the same mapping.
//
// # What is exact and what is not
//
// The RATE is exact: both sides of the fit below are ultimately the sender's
// sample clock, so drift between the participant's sound card and the
// recorder's host — tens to hundreds of milliseconds over a long meeting — is
// solved rather than estimated.
//
// The OFFSET is not exact. Associating an anchor with a position inside the
// local recording goes through wall-clock time, which carries the encoder's
// latency: roughly constant, tens of milliseconds. So placement lands within a
// few tens of ms rather than at sample accuracy. That is comfortably inside a
// word, which is what the transcript needs. Removing the residual bias would
// need correlation against an intact stretch of the recorded track, and is
// deliberately not done here — it would reintroduce the dependency this design
// exists to avoid, for an error smaller than a syllable.

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

// RTPTimeBase is the recorder's side of the mapping, read from the MKV stream
// tags the remux writes.
type RTPTimeBase struct {
	FirstRTPTimestamp int64
	FirstTimelineNS   int64
	ClockRate         uint32
	Known             bool
}

// timelineMS converts a sender-side RTP timestamp to a position on the meeting
// timeline, in milliseconds.
func (b RTPTimeBase) timelineMS(rtp int64) float64 {
	delta := rtp - b.FirstRTPTimestamp
	// Unwrap against the base rather than against the previous value: anchors
	// are sampled, so consecutive ones can legitimately be far apart, and only
	// the distance from the base matters here.
	for delta < -rtpWrap/2 {
		delta += rtpWrap
	}
	for delta > rtpWrap/2 {
		delta -= rtpWrap
	}
	return float64(b.FirstTimelineNS)/1e6 + float64(delta)*1000/float64(b.ClockRate)
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
func FitPlacement(segment SourceSegment, base RTPTimeBase) (Placement, error) {
	if !base.Known || base.ClockRate == 0 {
		return Placement{}, fmt.Errorf("recording carries no RTP time base for this speaker")
	}
	if len(segment.Anchors) < minPlacementAnchors {
		return Placement{}, fmt.Errorf("only %d anchors, need %d", len(segment.Anchors), minPlacementAnchors)
	}
	type point struct{ local, meeting float64 }
	points := make([]point, 0, len(segment.Anchors))
	for _, anchor := range segment.Anchors {
		points = append(points, point{
			local:   float64(anchor.WallMS - segment.StartWallMS),
			meeting: base.timelineMS(anchor.RTPTimestamp),
		})
	}

	fit := func(pts []point) (slope, intercept float64, ok bool) {
		n := float64(len(pts))
		if n < 2 {
			return 0, 0, false
		}
		var sx, sy, sxx, sxy float64
		for _, p := range pts {
			sx += p.local
			sy += p.meeting
			sxx += p.local * p.local
			sxy += p.local * p.meeting
		}
		denom := n*sxx - sx*sx
		if denom == 0 {
			// Every anchor at the same local time: no slope is determined.
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
			d := p.meeting - (intercept + slope*p.local)
			sum += d * d
		}
		return math.Sqrt(sum / float64(len(pts)))
	}
	rms := residual(points, slope, intercept)
	if rms > 0 {
		kept := points[:0:0]
		for _, p := range points {
			if math.Abs(p.meeting-(intercept+slope*p.local)) <= 3*rms {
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

	placement := Placement{OffsetMS: intercept, Rate: slope, Anchors: len(points), ResidualMS: rms}
	if math.Abs(slope-1) > maxPlacementRateDeviation {
		return placement, fmt.Errorf("fitted rate %.6f is not plausible clock drift", slope)
	}
	if rms > maxPlacementResidualMS {
		return placement, fmt.Errorf("anchors disagree by %.1f ms RMS", rms)
	}
	return placement, nil
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

// DiscoverSourceCaptures finds every upload under root, keyed by the owner's
// Nextcloud user id — the same value the MKV carries as PARTICIPANT_ID.
//
// Layout is <root>/<room>/<owner>/<call-start-ms>/, written by the operator's
// capture_upload.go. When a participant has several (a rejoin, or a retried
// upload of a different call), the one whose call window starts latest wins:
// re-uploads replace in place, so multiple directories mean genuinely different
// calls and the most recent is the one this recording is most likely to be.
func DiscoverSourceCaptures(root string) (map[string]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "capture.json"))
	if err != nil {
		return nil, fmt.Errorf("scan capture root: %w", err)
	}
	sort.Strings(matches)
	best := map[string]int64{}
	found := map[string]string{}
	for _, match := range matches {
		dir := filepath.Dir(match)
		sidecar, err := LoadSourceSidecar(dir)
		if err != nil {
			// A malformed upload must not fail the build: the recorded track is
			// still there and the meeting still publishes.
			continue
		}
		if prev, ok := best[sidecar.OwnerUserID]; ok && prev >= sidecar.CallStartWallMS {
			continue
		}
		best[sidecar.OwnerUserID] = sidecar.CallStartWallMS
		found[sidecar.OwnerUserID] = dir
	}
	return found, nil
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

// RenderSourceTrack builds one speaker's timeline-aligned PCM from their
// upload, or reports why it could not.
//
// A segment that cannot be placed is skipped rather than guessed at: the
// recorded track remains the fallback for the whole speaker, and a half-placed
// mixture would put words at times nobody can defend.
func RenderSourceTrack(dir string, sidecar SourceSidecar, base RTPTimeBase, sampleRate int, outSamples int) ([]float32, SourceRenderReport, error) {
	report := SourceRenderReport{Owner: sidecar.OwnerUserID, Segments: len(sidecar.Segments)}
	out := make([]float32, outSamples)
	var totalAnchors int
	var worstResidual float64
	var rateSum float64
	for _, segment := range sidecar.Segments {
		placement, err := FitPlacement(segment, base)
		if err != nil {
			report.Rejections = append(report.Rejections,
				fmt.Sprintf("segment %d: %v", segment.Index, err))
			continue
		}
		samples, err := decodeSourceSegment(filepath.Join(dir, segment.AudioName), sampleRate)
		if err != nil {
			report.Rejections = append(report.Rejections,
				fmt.Sprintf("segment %d: decode: %v", segment.Index, err))
			continue
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
	if report.Placed == 0 {
		return nil, report, fmt.Errorf("no segment could be placed")
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
func ApplySourceAudio(streams []AudioStream, captureRoot, workDir string, sampleRate int, timelineMS int64, stdout io.Writer) []SourceRenderReport {
	captures, err := DiscoverSourceCaptures(captureRoot)
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
	for i := range streams {
		stream := &streams[i]
		dir, ok := captures[stream.ParticipantID]
		if !ok || stream.ParticipantID == "" {
			continue
		}
		if !stream.RTPBase.Known {
			fmt.Fprintf(stdout, "  source audio: %s uploaded, but this recording carries no RTP base for their track; keeping the recorded audio\n", stream.SpeakerLabel)
			continue
		}
		sidecar, err := LoadSourceSidecar(dir)
		if err != nil {
			fmt.Fprintf(stdout, "  source audio: %s: %v\n", stream.SpeakerLabel, err)
			continue
		}
		samples, report, err := RenderSourceTrack(dir, sidecar, stream.RTPBase, sampleRate, outSamples)
		report.SpeakerID = stream.SpeakerID
		if err != nil {
			fmt.Fprintf(stdout, "  source audio: %s: %v; keeping the recorded audio\n", stream.SpeakerLabel, err)
			reports = append(reports, report)
			continue
		}
		path := filepath.Join(workDir, "source-"+stream.SpeakerID+".wav")
		if err := writeWAV16(path, samples, sampleRate); err != nil {
			fmt.Fprintf(stdout, "  source audio: %s: %v; keeping the recorded audio\n", stream.SpeakerLabel, err)
			reports = append(reports, report)
			continue
		}
		stream.SourceAudioPath = path
		fmt.Fprintf(stdout, "  source audio: %s transcribing from participant capture (%d/%d segments, %d anchors, %.1f ms residual, %.0f ppm drift)\n",
			stream.SpeakerLabel, report.Placed, report.Segments, report.Anchors, report.ResidualMS, report.RatePPM)
		reports = append(reports, report)
	}
	return reports
}
