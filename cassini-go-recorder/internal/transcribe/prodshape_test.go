package transcribe

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Production-shaped fixtures.
//
// The attribution stage shipped with a bug that would have fired on two thirds
// of production streams, and none of my fixtures could have caught it, because
// every fixture track started at t=0, was fully dense, and had a unique speaker
// id. A survey of the 44 multitrack meetings in the archive (199 audio streams)
// says production looks nothing like that:
//
//	streams starting >= 1 s late     67.8%   median 36.8 s, p90 737 s, max 4308 s
//	duplicate speaker ids            17/44 meetings, 46 extra streams
//	packet density (DTX sparsity)    p10 5.4%, p25 22%, p50 71%
//	streams under 50% dense          37.7%
//
// A participant joins after the call starts, mutes, drops and rejoins under a
// second stream carrying the same participant id, and speaks for 40 seconds of
// a 50-minute meeting. Every one of those is normal, and each one broke an
// assumption baked into the fixtures.
//
// These constants are the shape contract. Tests below build meetings against
// them so a future change has to survive production's shape, not a tidy
// idealisation of it.
const (
	// prodMedianJoinOffsetSeconds is the median first-packet offset. Half of all
	// production streams start later than this.
	prodMedianJoinOffsetSeconds = 36.8
	// prodLateJoinP90Seconds is the 90th percentile: a track can be absent for
	// twelve minutes and still be ordinary.
	prodLateJoinP90Seconds = 737.0
	// prodSparseDensityP10 is the 10th-percentile packet density. A participant
	// who barely speaks contributes almost no audio to their own track.
	prodSparseDensityP10 = 0.054
)

// buildProdShapedMeeting writes a multitrack MKV with production's shape: a
// punctual dense speaker, a late joiner, a very sparse near-silent participant,
// and a rejoined second stream that shares the first speaker's participant id.
//
// Levels are deliberately different per track so a bug that ignores each
// track's own calibration shows up as a wrong winner rather than as noise.
func buildProdShapedMeeting(t *testing.T, dir string) string {
	t.Helper()
	outPath := filepath.Join(dir, "prod-shaped.mkv")

	const total = 6.0
	// A hot mic, present from the start, talking in the middle of the meeting.
	punctual := fmt.Sprintf(
		"sine=frequency=300:sample_rate=48000:duration=%.1f,volume=0.9,"+
			"afade=t=in:st=2:d=0.05,afade=t=out:st=4:d=0.05", total)
	// A quiet mic that joins late: silence until the join, then real audio.
	late := fmt.Sprintf(
		"sine=frequency=700:sample_rate=48000:duration=%.1f,volume=0.05", total-2.0)
	// A participant who is barely present at all (p10 density).
	sparse := fmt.Sprintf(
		"sine=frequency=1100:sample_rate=48000:duration=%.1f,volume=0.4", 0.3)
	// The rejoin: a second stream for the punctual speaker, same participant id.
	rejoin := fmt.Sprintf(
		"sine=frequency=300:sample_rate=48000:duration=%.1f,volume=0.9", 1.0)

	args := []string{
		"-y", "-v", "error",
		"-f", "lavfi", "-i", punctual,
		"-itsoffset", "2.0", "-f", "lavfi", "-i", late,
		"-itsoffset", "4.5", "-f", "lavfi", "-i", sparse,
		"-itsoffset", "5.0", "-f", "lavfi", "-i", rejoin,
		"-map", "0:a:0", "-map", "1:a:0", "-map", "2:a:0", "-map", "3:a:0",
		"-metadata:s:a:0", "title=Punctual",
		"-metadata:s:a:0", "participant_id=user-punctual",
		"-metadata:s:a:1", "title=LateJoiner",
		"-metadata:s:a:1", "participant_id=user-late",
		"-metadata:s:a:2", "title=BarelySpoke",
		"-metadata:s:a:2", "participant_id=user-sparse",
		// Same participant id as stream 0: this is the rotated/rejoined stream.
		"-metadata:s:a:3", "title=Punctual",
		"-metadata:s:a:3", "participant_id=user-punctual",
		"-c:a", "libopus",
		outPath,
	}
	if err := runMediaCommand("ffmpeg", args...); err != nil {
		t.Fatalf("create prod-shaped meeting: %v", err)
	}
	return outPath
}

// The fixture must actually have the shape it claims, or the tests built on it
// prove nothing. This is the guard against the fixture quietly drifting back
// towards the tidy version.
func TestProdShapedFixtureHasProductionShape(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(streams) != 4 {
		t.Fatalf("expected 4 audio streams, got %d", len(streams))
	}

	byID := map[string]int{}
	for _, s := range streams {
		byID[s.SpeakerID]++
	}
	var shared int
	for _, n := range byID {
		if n > 1 {
			shared += n - 1
		}
	}
	if shared == 0 {
		t.Error("fixture must contain a rejoined stream sharing a participant id: " +
			"39% of production meetings do")
	}

	var late int
	for _, s := range streams {
		samples, err := ExtractSpeakerFloats(mkv, s)
		if err != nil {
			t.Fatalf("decode %s: %v", s.SpeakerLabel, err)
		}
		// A late joiner's decoded track begins with timeline padding.
		if len(samples) > 0 && samples[0] == 0 {
			var lead int
			for lead < len(samples) && samples[lead] == 0 {
				lead++
			}
			if float64(lead)/16000.0 > 0.5 {
				late++
			}
		}
	}
	if late == 0 {
		t.Error("fixture must contain a late-joining track: 67.8% of production streams are")
	}
}

// The regression the review caught, at the level it actually bites: a late
// joiner whose pre-join padding was measured as their noise floor scored
// hundreds of dB above it and would have won every contested word.
func TestAttributionOnProductionShapeDoesNotFavourLateJoiners(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	envelopes, err := BuildSpeakerEnvelopes(mkv, streams, 16000, nil)
	if err != nil {
		t.Fatalf("envelopes: %v", err)
	}

	// No track may report an implausible level above its own floor. Padding
	// measured as floor produced ~238 dB; real microphone dynamics are tens.
	for _, env := range envelopes {
		for i := range env.FrameDB {
			if len(env.Present) == len(env.FrameDB) && !env.Present[i] {
				continue
			}
			if got := env.FrameDB[i] - env.FloorDB; got > 120 {
				t.Fatalf("%s reports %.1f dB above its own floor: padding is being "+
					"measured as the noise floor", env.SpeakerID, got)
			}
		}
	}

	// During the punctual speaker's loud passage the late joiner has not even
	// arrived, so a word wrongly attributed to them must be contradicted, and
	// the punctual speaker's own word must not be.
	word := Word{Text: "test", StartMS: 2500, EndMS: 2700}
	if gap, ok := AttributionGapDB(word, "spk_punctual", envelopes); ok && gap > 6 {
		t.Errorf("the actual speaker was contradicted by %.1f dB", gap)
	}
}

// Attribution must survive a meeting whose tracks are late, sparse and
// duplicated, without failing the build or corrupting the transcript. This is
// the end-to-end shape guard.
func TestAttributionSurvivesProductionShapeEndToEnd(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	mkv := buildProdShapedMeeting(t, dir)

	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	// A transcript with words on every track, including the rejoined duplicate
	// and the barely-present participant.
	var segments []Segment
	for _, s := range streams {
		words := []Word{
			{Text: "one", StartMS: 2500, EndMS: 2600},
			{Text: "two", StartMS: 2600, EndMS: 2700},
		}
		segments = append(segments, Segment{
			SpeakerID: s.SpeakerID, StartMS: 2500, EndMS: 2700,
			Text: "one two", Words: words,
		})
	}
	before := CountWords(segments)

	out := applyAttribution(mkv, streams, segments, 16000, BuildConfig{}, os.Stdout)
	if CountWords(out) != before {
		t.Errorf("the default path must not delete words: %d became %d", before, CountWords(out))
	}
	if err := ValidateSegments(out); err != nil {
		t.Fatalf("attribution produced an invalid transcript on production shape: %v", err)
	}

	// Whatever the verdict, it must be reproducible: the same input twice gives
	// the same answer, which a stream-order-dependent bug would break.
	again := applyAttribution(mkv, streams, segments, 16000, BuildConfig{}, os.Stdout)
	if len(again) != len(out) {
		t.Fatalf("attribution is not deterministic: %d vs %d segments", len(again), len(out))
	}
	for i := range out {
		for j := range out[i].Words {
			if out[i].Words[j].AttributionGapDB != again[i].Words[j].AttributionGapDB {
				t.Errorf("segment %d word %d gap changed between runs: %.3f vs %.3f",
					i, j, out[i].Words[j].AttributionGapDB, again[i].Words[j].AttributionGapDB)
			}
		}
	}
}
