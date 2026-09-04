package transcribe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The published audio, end to end.
//
// Everything else in this package tests a piece of the splice. This tests the
// decision the owner actually made: that a word in the transcript can be heard
// in the meeting people play back. The recorded track and the upload are
// different tones, so "the upload is in the published mix" is a question about
// spectrum with an unambiguous answer, and the same question can be asked of
// the transcription input to show the two are one render.

// mkvStreamSpec is one participant track in a synthetic recording.
type mkvStreamSpec struct {
	participantID string
	frequency     int
	offsetSeconds float64
	seconds       float64
	// The wall-clock anchor the remux would have written. Zero leaves the tags
	// off entirely, which is what a recording made before the remux emitted
	// them looks like.
	firstPacketWallMS int64
	firstTimelineNS   int64
}

// buildTaggedMeeting writes an MKV shaped like a real recording: one Opus track
// per participant, each carrying the participant id and the wall-clock time base
// the splice places uploads against.
func buildTaggedMeeting(t *testing.T, dir string, specs []mkvStreamSpec) string {
	t.Helper()
	outPath := filepath.Join(dir, "recording.mkv")
	args := []string{"-y", "-v", "error"}
	for _, spec := range specs {
		if spec.offsetSeconds > 0 {
			args = append(args, "-itsoffset", strconv.FormatFloat(spec.offsetSeconds, 'f', 3, 64))
		}
		args = append(args, "-f", "lavfi", "-i",
			fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=%g", spec.frequency, spec.seconds))
	}
	for i := range specs {
		args = append(args, "-map", fmt.Sprintf("%d:a:0", i))
	}
	for i, spec := range specs {
		args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), "participant_id="+spec.participantID)
		args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), "participant_name="+spec.participantID)
		if spec.firstPacketWallMS > 0 {
			args = append(args,
				fmt.Sprintf("-metadata:s:a:%d", i), fmt.Sprintf("first_packet_wall_ms=%d", spec.firstPacketWallMS),
				fmt.Sprintf("-metadata:s:a:%d", i), fmt.Sprintf("first_timeline_ns=%d", spec.firstTimelineNS),
				fmt.Sprintf("-metadata:s:a:%d", i), "clock_rate=48000")
		}
	}
	args = append(args, "-c:a", "libopus", outPath)
	if err := runMediaCommand("ffmpeg", args...); err != nil {
		t.Fatalf("build tagged meeting: %v", err)
	}
	return outPath
}

// writeCaptureAt puts one participant's upload where DiscoverSourceCaptures
// looks for it: <root>/<room>/<owner>/<call-start-ms>/.
func writeCaptureAt(t *testing.T, root, room, owner string, segment SourceSegment, seconds float64, frequency int) {
	t.Helper()
	dir := filepath.Join(root, room, owner, strconv.FormatInt(segment.StartWallMS, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir capture: %v", err)
	}
	if err := runMediaCommand("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=%d:duration=%g:sample_rate=48000", frequency, seconds),
		"-ac", "1", "-ar", "48000", "-f", "wav", filepath.Join(dir, segment.AudioName)); err != nil {
		t.Fatalf("synthesise segment: %v", err)
	}
	writeSidecar(t, dir, SourceSidecar{
		Format: SourceCaptureFormat, RoomToken: room, OwnerUserID: owner,
		CallStartWallMS: segment.StartWallMS, CallEndWallMS: segment.StopWallMS,
		Segments: []SourceSegment{segment},
	})
}

// decodePCM decodes any audio file to mono float samples at the given rate.
func decodePCM(t *testing.T, path string, sampleRate int) []float32 {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-vn", "-sn", "-dn", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "s16le", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("decode %s: %v: %s", path, err, stderr.String())
	}
	out := make([]float32, len(raw)/2)
	for i := range out {
		out[i] = float32(int16(uint16(raw[i*2])|uint16(raw[i*2+1])<<8)) / 32768
	}
	return out
}

// tonePower is a Goertzel filter: how much of one frequency a stretch of audio
// holds, normalised so stretches of different lengths compare.
func tonePower(samples []float32, sampleRate int, frequency float64) float64 {
	n := len(samples)
	if n < 2 {
		return 0
	}
	coeff := 2 * math.Cos(2*math.Pi*frequency/float64(sampleRate))
	var s1, s2 float64
	for _, x := range samples {
		s0 := float64(x) + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	power := s1*s1 + s2*s2 - coeff*s1*s2
	if power < 0 {
		power = 0
	}
	return power / (float64(n) * float64(n))
}

func sliceSeconds(samples []float32, sampleRate int, fromSec, toSec float64) []float32 {
	from := int(fromSec * float64(sampleRate))
	to := int(toSec * float64(sampleRate))
	if from < 0 {
		from = 0
	}
	if to > len(samples) {
		to = len(samples)
	}
	if to <= from {
		return nil
	}
	return samples[from:to]
}

func dbRatio(a, b float64) float64 {
	if b <= 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(a/b)
}

// aliceBase is the time base the synthetic segments in this file are built
// against: syntheticSegmentDelayed places a segment by mapping its start
// instant through testBase(), so the fixture's own tags have to be the same
// ones or the upload lands somewhere else entirely.
const aliceFirstTimelineNS = 2_000_000_000

func spliceFixture(t *testing.T, specs []mkvStreamSpec) (string, []AudioStream) {
	t.Helper()
	dir := t.TempDir()
	mkv := buildTaggedMeeting(t, dir, specs)
	streams, _, err := ProbeMKV(mkv)
	if err != nil {
		t.Fatalf("ProbeMKV: %v", err)
	}
	if len(streams) != len(specs) {
		t.Fatalf("probed %d streams, want %d", len(streams), len(specs))
	}
	for i := range streams {
		if specs[i].firstPacketWallMS > 0 && !streams[i].TimeBase.Known {
			t.Fatalf("stream %d lost its wall-clock tags on the way through ffmpeg", i)
		}
	}
	return mkv, streams
}

// twoSpeakerFixture: alice on 300 Hz from 2 s, bob on 900 Hz from the start.
func twoSpeakerSpecs() []mkvStreamSpec {
	return []mkvStreamSpec{
		{participantID: "alice", frequency: 300, offsetSeconds: 2, seconds: 28,
			firstPacketWallMS: testFirstPacketWallMS, firstTimelineNS: aliceFirstTimelineNS},
		{participantID: "bob", frequency: 900, seconds: 30,
			firstPacketWallMS: testFirstPacketWallMS - 2000, firstTimelineNS: 0},
	}
}

// runSplicedBuild does what BuildMeetingArtifact's mix phase does, without the
// model download and the recogniser behind it.
func runSplicedBuild(t *testing.T, mkv string, streams []AudioStream, captureRoot, room, bundleDir string) (string, []SourceRenderReport, string) {
	t.Helper()
	mix, err := PrepareMix(mkv, streams)
	if err != nil {
		t.Fatalf("PrepareMix: %v", err)
	}
	defer mix.Close()
	var log bytes.Buffer
	var reports []SourceRenderReport
	if captureRoot != "" {
		reports = ApplySourceAudio(context.Background(), mix, streams, captureRoot, room, bundleDir, &log)
	}
	webm := filepath.Join(bundleDir, "meeting.webm")
	if err := mix.Encode(webm); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return webm, reports, log.String()
}

// The decision, checked where it lands: a participant's upload is audible in
// the published mix over exactly the window it covers, the recorded track is
// audible everywhere else, and the transcription input carries the same splice
// because it is a resample of the same render.
func TestTheSplicedUploadIsAudibleInThePublishedMix(t *testing.T) {
	requireFFMediaTools(t)
	const sampleRate = 16000

	specs := twoSpeakerSpecs()
	mkv, streams := spliceFixture(t, specs)

	// Ten seconds of a third tone, uploaded, landing at 10 s on the timeline.
	root := t.TempDir()
	segment := syntheticSegmentDelayed(10_000, 10, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	writeCaptureAt(t, root, "room1", "alice", segment, 10, 1100)

	bundle := t.TempDir()
	webm, reports, log := runSplicedBuild(t, mkv, streams, root, "room1", bundle)

	if len(reports) != 1 {
		t.Fatalf("got %d reports, want one for alice: %v", len(reports), reports)
	}
	report := reports[0]
	if !report.MixSpliced {
		t.Fatalf("the published mix was not spliced: %+v", report)
	}
	if report.Placed != 1 || report.Skipped != 0 {
		t.Fatalf("placed %d and skipped %d, want 1 and 0: %v", report.Placed, report.Skipped, report.Rejections)
	}
	if report.RenderHz != 48000 {
		t.Fatalf("the render is %d Hz, want the mix's 48000", report.RenderHz)
	}
	if report.CrossfadeMS != mixSpliceCrossfadeMS {
		t.Fatalf("the crossfade is %d ms, want %d", report.CrossfadeMS, mixSpliceCrossfadeMS)
	}
	if len(report.Windows) != 1 {
		t.Fatalf("got %d windows, want 1: %+v", len(report.Windows), report.Windows)
	}
	window := report.Windows[0]
	if window.FromMS < 9900 || window.FromMS > 10100 {
		t.Fatalf("the window opens at %d ms, want about 10000", window.FromMS)
	}
	if window.ToMS < 19900 || window.ToMS > 20100 {
		t.Fatalf("the window closes at %d ms, want about 20000", window.ToMS)
	}
	for _, want := range []string{
		"transcribing from participant capture spliced over",
		"the published mix carries the same splice",
	} {
		if !bytes.Contains([]byte(log), []byte(want)) {
			t.Fatalf("the build log does not say %q:\n%s", want, log)
		}
	}

	published := decodePCM(t, webm, sampleRate)
	// Inside the window, the upload dominates alice's recorded tone, and bob is
	// still there — the splice replaces one participant, not the mix.
	inside := sliceSeconds(published, sampleRate, 10.2, 19.8)
	uploaded := tonePower(inside, sampleRate, 1100)
	recorded := tonePower(inside, sampleRate, 300)
	if got := dbRatio(uploaded, recorded); got < 20 {
		t.Fatalf("inside the window the upload is only %.1f dB above alice's recorded tone; it was not spliced into the mix", got)
	}
	if bob := tonePower(inside, sampleRate, 900); dbRatio(bob, uploaded) < -20 {
		t.Fatalf("bob is %.1f dB below the upload inside the window; the splice took the whole mix, not one participant",
			dbRatio(bob, uploaded))
	}
	// Outside it, alice's recorded tone stands and the upload is nowhere.
	for _, span := range [][2]float64{{3, 9.5}, {20.5, 27}} {
		outside := sliceSeconds(published, sampleRate, span[0], span[1])
		if got := dbRatio(tonePower(outside, sampleRate, 300), tonePower(outside, sampleRate, 1100)); got < 20 {
			t.Fatalf("between %gs and %gs the recorded tone is only %.1f dB above the upload; the splice ran past its window",
				span[0], span[1], got)
		}
	}

	// One render, two consumers: the transcription input is a resample of the
	// very samples the mix was encoded from, so it carries the same tone over
	// the same window.
	transcript := filepath.Join(bundle, "_work", "sourceaudio", "source-"+streams[0].SpeakerID+".wav")
	if streams[0].SourceAudioPath != transcript {
		t.Fatalf("the stream points at %q, want %q", streams[0].SourceAudioPath, transcript)
	}
	track := decodePCM(t, transcript, sampleRate)
	insideTrack := sliceSeconds(track, sampleRate, 10.2, 19.8)
	if got := dbRatio(tonePower(insideTrack, sampleRate, 1100), tonePower(insideTrack, sampleRate, 300)); got < 20 {
		t.Fatalf("the transcription input holds the upload only %.1f dB above the recorded tone inside the window", got)
	}
	outsideTrack := sliceSeconds(track, sampleRate, 3, 9.5)
	if got := dbRatio(tonePower(outsideTrack, sampleRate, 300), tonePower(outsideTrack, sampleRate, 1100)); got < 20 {
		t.Fatalf("the transcription input holds the upload %.1f dB outside the window", got)
	}
	// It must not carry bob: this is alice's track, spliced.
	if got := dbRatio(tonePower(outsideTrack, sampleRate, 300), tonePower(outsideTrack, sampleRate, 900)); got < 20 {
		t.Fatalf("alice's transcription input carries bob at %.1f dB below her own tone", got)
	}
}

// A build with no upload for this room must publish the audio it would have
// published before, and leave the bundle as untouched as a build with ingestion
// switched off.
func TestABuildWithNoUploadsPublishesTheRecordedMixUnchanged(t *testing.T) {
	requireFFMediaTools(t)
	specs := twoSpeakerSpecs()
	mkv, streams := spliceFixture(t, specs)

	plainBundle := t.TempDir()
	plain, reports, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), "", "", plainBundle)
	if reports != nil {
		t.Fatalf("a build with ingestion off reported %v", reports)
	}

	// An empty capture root is the ordinary case: ingestion is on, nobody
	// uploaded.
	emptyBundle := t.TempDir()
	ingested, reports, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), t.TempDir(), "room1", emptyBundle)
	if reports != nil {
		t.Fatalf("a build with no captures reported %v", reports)
	}
	if mixAudioSHA256(t, plain) != mixAudioSHA256(t, ingested) {
		t.Fatal("a build with an empty capture root published different audio from one with ingestion off")
	}
	if _, err := os.Stat(filepath.Join(emptyBundle, "_work")); !os.IsNotExist(err) {
		t.Fatal("a build with no captures left a work directory in the bundle")
	}
}

// An upload that cannot be placed costs the upload and nothing else: the
// speaker keeps their recorded audio, in the mix as much as in the transcript,
// and the published audio is what it would have been.
func TestARefusedUploadLeavesThePublishedMixAlone(t *testing.T) {
	requireFFMediaTools(t)
	specs := twoSpeakerSpecs()
	mkv, streams := spliceFixture(t, specs)

	control := t.TempDir()
	reference, _, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), "", "", control)

	// Three anchors: fewer than a placement needs.
	root := t.TempDir()
	segment := syntheticSegmentDelayed(10_000, 3, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	writeCaptureAt(t, root, "room1", "alice", segment, 3, 1100)

	bundle := t.TempDir()
	spliced := append([]AudioStream(nil), streams...)
	webm, reports, log := runSplicedBuild(t, mkv, spliced, root, "room1", bundle)

	if len(reports) != 1 {
		t.Fatalf("got %d reports, want one saying why alice's upload was refused", len(reports))
	}
	if reports[0].MixSpliced {
		t.Fatal("a refused upload claims the mix was spliced")
	}
	if len(reports[0].Rejections) == 0 {
		t.Fatal("a refused upload gave no reason")
	}
	if !bytes.Contains([]byte(log), []byte("keeping the recorded audio")) {
		t.Fatalf("the build log does not say the recorded audio was kept:\n%s", log)
	}
	for i := range spliced {
		if spliced[i].SourceAudioPath != "" || spliced[i].SuppressTranscription {
			t.Fatalf("stream %d was rerouted although the upload was refused", i)
		}
	}
	if mixAudioSHA256(t, reference) != mixAudioSHA256(t, webm) {
		t.Fatal("a refused upload changed the published audio")
	}
}

// The off switch: the transcript is still built from the spliced render, and the
// published mix is exactly what it would have been. This is the rollback a
// deployment gets without giving up ingestion.
func TestTheMixSpliceCanBeTurnedOffOnItsOwn(t *testing.T) {
	requireFFMediaTools(t)
	specs := twoSpeakerSpecs()
	mkv, streams := spliceFixture(t, specs)

	control := t.TempDir()
	reference, _, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), "", "", control)

	root := t.TempDir()
	segment := syntheticSegmentDelayed(10_000, 10, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	writeCaptureAt(t, root, "room1", "alice", segment, 10, 1100)

	t.Setenv("CASSINI_SOURCE_AUDIO_MIX", "0")
	bundle := t.TempDir()
	spliced := append([]AudioStream(nil), streams...)
	webm, reports, log := runSplicedBuild(t, mkv, spliced, root, "room1", bundle)

	if len(reports) != 1 || reports[0].MixSpliced {
		t.Fatalf("the off switch did not stop the mix splice: %+v", reports)
	}
	if reports[0].MixSkipReason == "" {
		t.Fatal("the manifest does not say why the mix was left alone")
	}
	if reports[0].Placed != 1 {
		t.Fatalf("the transcript splice was placed %d times; the off switch is only about the mix", reports[0].Placed)
	}
	if spliced[0].SourceAudioPath == "" {
		t.Fatal("the transcription input was not rerouted; the off switch is only about the mix")
	}
	if !bytes.Contains([]byte(log), []byte("the published mix keeps the recorded track")) {
		t.Fatalf("the build log does not explain the untouched mix:\n%s", log)
	}
	if mixAudioSHA256(t, reference) != mixAudioSHA256(t, webm) {
		t.Fatal("the off switch still changed the published audio")
	}
}

// A participant who rejoined has two tracks in the recording. The render sums
// them, so the mix must take the render in place of BOTH — counting a sibling
// again would play that participant twice, at double amplitude.
func TestARejoinedParticipantIsCountedOnceInTheMix(t *testing.T) {
	requireFFMediaTools(t)
	const sampleRate = 16000
	specs := []mkvStreamSpec{
		{participantID: "alice", frequency: 300, offsetSeconds: 2, seconds: 10,
			firstPacketWallMS: testFirstPacketWallMS, firstTimelineNS: aliceFirstTimelineNS},
		{participantID: "bob", frequency: 900, seconds: 30},
		{participantID: "alice", frequency: 300, offsetSeconds: 20, seconds: 10,
			firstPacketWallMS: testFirstPacketWallMS + 18_000, firstTimelineNS: 20_000_000_000},
	}
	mkv, streams := spliceFixture(t, specs)

	control := t.TempDir()
	reference, _, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), "", "", control)
	referencePCM := decodePCM(t, reference, sampleRate)
	// Alice's second stint, where the upload never reaches.
	referenceTone := tonePower(sliceSeconds(referencePCM, sampleRate, 22, 28), sampleRate, 300)

	root := t.TempDir()
	// The upload covers only the first stint.
	segment := syntheticSegmentDelayed(3_000, 8, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	writeCaptureAt(t, root, "room1", "alice", segment, 8, 1100)

	bundle := t.TempDir()
	spliced := append([]AudioStream(nil), streams...)
	webm, reports, _ := runSplicedBuild(t, mkv, spliced, root, "room1", bundle)
	if len(reports) != 1 || !reports[0].MixSpliced {
		t.Fatalf("the rejoined participant was not spliced once: %+v", reports)
	}
	// Exactly one stream carries the render; the sibling is dropped from
	// transcription so its words are not written twice.
	var rerouted, suppressed int
	for i := range spliced {
		if spliced[i].SourceAudioPath != "" {
			rerouted++
		}
		if spliced[i].SuppressTranscription {
			suppressed++
		}
	}
	if rerouted != 1 || suppressed != 1 {
		t.Fatalf("%d streams were rerouted and %d suppressed, want 1 and 1", rerouted, suppressed)
	}

	got := decodePCM(t, webm, sampleRate)
	// Alice's second stint is neither dropped nor doubled: the render carries
	// it, and the mix counts it once.
	tone := tonePower(sliceSeconds(got, sampleRate, 22, 28), sampleRate, 300)
	if ratio := dbRatio(tone, referenceTone); ratio < -1 || ratio > 1 {
		t.Fatalf("alice's second stint is %.1f dB from where it was without the upload; the render's siblings were mishandled", ratio)
	}
	// And the upload landed over the first stint.
	inside := sliceSeconds(got, sampleRate, 3.5, 10.5)
	if ratio := dbRatio(tonePower(inside, sampleRate, 1100), tonePower(inside, sampleRate, 300)); ratio < 20 {
		t.Fatalf("the upload is %.1f dB over alice's recorded tone in her first stint", ratio)
	}
}

// The splice renders onto a copy, never onto the decoded tracks. A speaker who
// was not spliced must find their track exactly as it was decoded, and a
// spliced speaker's track is removed rather than rewritten — the render holds
// every sample of it, and keeping both would double the temporary disk a long
// meeting needs.
func TestTheSpliceNeverWritesIntoTheMixsRecordedTracks(t *testing.T) {
	requireFFMediaTools(t)
	specs := twoSpeakerSpecs()
	mkv, streams := spliceFixture(t, specs)

	mix, err := PrepareMix(mkv, streams)
	if err != nil {
		t.Fatalf("PrepareMix: %v", err)
	}
	defer mix.Close()
	before := make([]string, len(mix.tracks))
	for i, path := range mix.tracks {
		before[i] = fileDigest(t, path)
	}

	root := t.TempDir()
	segment := syntheticSegmentDelayed(10_000, 10, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	writeCaptureAt(t, root, "room1", "alice", segment, 10, 1100)

	var log bytes.Buffer
	reports := ApplySourceAudio(context.Background(), mix, streams, root, "room1", t.TempDir(), &log)
	if len(reports) != 1 || !reports[0].MixSpliced {
		t.Fatalf("nothing was spliced: %+v", reports)
	}
	// Bob did not upload, so his decoded track is untouched.
	if fileDigest(t, mix.tracks[1]) != before[1] {
		t.Fatal("the splice wrote into a track belonging to a speaker who did not upload")
	}
	if _, err := os.Stat(mix.tracks[0]); !os.IsNotExist(err) {
		t.Fatal("alice's decoded track survived her substitution; the temporary disk doubles for every spliced speaker")
	}
	if mix.Inputs()[0] != mix.RenderPath(streams[0].SpeakerID) {
		t.Fatalf("the mix takes %q rather than alice's render", mix.Inputs()[0])
	}

	// And reverting brings the recorded mix back, byte-identically decoded, so
	// a failed spliced encode does not cost the meeting.
	if err := mix.RevertSubstitutions(); err != nil {
		t.Fatalf("RevertSubstitutions: %v", err)
	}
	if fileDigest(t, mix.tracks[0]) != before[0] {
		t.Fatal("the re-decoded track does not match the one the splice replaced")
	}
	if got, want := mix.Inputs(), mix.tracks; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("after reverting the mix takes %q, want the recorded tracks %q", got, want)
	}
}

// The failure this branch could most easily introduce: publishing the recorded
// mix while transcribing a spliced one, so the transcript quotes words that are
// not in the audio. The revert has to be all or nothing.
func TestRevertingIngestionPutsBothSidesBack(t *testing.T) {
	streams := []AudioStream{
		{ParticipantID: "alice", SpeakerID: "spk_alice", SourceAudioPath: "/work/source-alice.wav"},
		{ParticipantID: "alice", SpeakerID: "spk_alice_2", SuppressTranscription: true},
		{ParticipantID: "bob", SpeakerID: "spk_bob"},
	}
	reports := []SourceRenderReport{{
		SpeakerID: "spk_alice", Owner: "alice", Segments: 2, Placed: 2, SplicedMS: 24000,
		CoverageMS: 24000, Anchors: 16, MixSpliced: true, CrossfadeMS: 15, RenderHz: 48000,
		Windows: []SpliceWindow{{FromMS: 1000, ToMS: 13000}},
	}}
	revertSourceAudio(streams, reports, "the spliced mix would not encode: boom")

	for i := range streams {
		if streams[i].SourceAudioPath != "" {
			t.Fatalf("stream %d still transcribes from a render the mix no longer carries", i)
		}
		if streams[i].SuppressTranscription {
			t.Fatalf("stream %d is still dropped from transcription although its audio is back in the mix", i)
		}
	}
	got := reports[0]
	if got.MixSpliced || got.Placed != 0 || got.SplicedMS != 0 || len(got.Windows) != 0 {
		t.Fatalf("the report still claims a splice nothing used: %+v", got)
	}
	if got.Segments != 2 || got.Owner != "alice" {
		t.Fatalf("the report lost the diagnosis of what arrived: %+v", got)
	}
	if len(got.Rejections) != 1 || !strings.Contains(got.Rejections[0], "would not encode") {
		t.Fatalf("the report does not say why the upload went unused: %v", got.Rejections)
	}
}

// A capture in the root that belongs to nobody in this recording must leave the
// bundle exactly as a build with ingestion switched off would — no empty work
// directory to explain to whoever finds it.
func TestAnUnrelatedUploadLeavesTheBundleAlone(t *testing.T) {
	requireFFMediaTools(t)
	specs := twoSpeakerSpecs()
	mkv, streams := spliceFixture(t, specs)

	root := t.TempDir()
	segment := syntheticSegmentDelayed(10_000, 10, 1000, 0, 0)
	segment.AudioName = "segment-0.webm"
	// Charlie was in the room, but not in this call: no track, no floor.
	writeCaptureAt(t, root, "room1", "charlie", segment, 10, 1100)

	control := t.TempDir()
	reference, _, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), "", "", control)

	bundle := t.TempDir()
	webm, reports, _ := runSplicedBuild(t, mkv, append([]AudioStream(nil), streams...), root, "room1", bundle)
	if len(reports) != 0 {
		t.Fatalf("an upload from somebody not in the call produced %+v", reports)
	}
	if _, err := os.Stat(filepath.Join(bundle, "_work")); !os.IsNotExist(err) {
		t.Fatal("an unusable upload left a work directory in the bundle")
	}
	if mixAudioSHA256(t, reference) != mixAudioSHA256(t, webm) {
		t.Fatal("an unusable upload changed the published audio")
	}
}

// fileDigest is a content hash, for asserting a file was not touched.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
