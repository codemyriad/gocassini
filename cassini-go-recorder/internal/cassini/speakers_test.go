package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gocassini/internal/portable"
	"gocassini/internal/transcribe"
)

// The bundles here are written by the transcribe package's own writers, so
// apply is tested against the bytes a build produces. Turns are fixed: no
// model runs.

const (
	speakersRoomID = "spk_meeting_room_0123456789abcdef01234567"
	speakersBenID  = "spk_ben_0123456789abcdef01234567"
	speakersCleoID = "spk_cleo_0123456789abcdef01234567"
)

type speakersBundleOptions struct {
	// realAudio writes a decodable meeting.webm (ffmpeg); otherwise the audio
	// is opaque bytes, which is all apply ever looks at.
	realAudio bool
	// additional lists a second model's transcript in files.transcripts.
	additional bool
	// silent adds a third participant who said nothing: a stream the build
	// counts in speakerCount but that has no words, so no roster entry.
	silent bool
	// untranscribed writes the bundle a build writes when transcription is
	// skipped: the participants listed, no words, no captions.
	untranscribed bool
}

// speakersFixtureWords: three words in the room's first voice, three in its
// second, two by Ben on his own device.
func speakersFixtureSegments() []transcribe.Segment {
	room := []transcribe.Word{
		{Text: "Good", StartMS: 0, EndMS: 400},
		{Text: "morning", StartMS: 500, EndMS: 900},
		{Text: "everyone.", StartMS: 1000, EndMS: 1400},
		{Text: "Thanks,", StartMS: 3200, EndMS: 3600},
		{Text: "happy", StartMS: 3700, EndMS: 4100},
		{Text: "here.", StartMS: 4200, EndMS: 4600},
	}
	ben := []transcribe.Word{
		{Text: "Hello", StartMS: 6500, EndMS: 7000},
		{Text: "both.", StartMS: 7100, EndMS: 7500},
	}
	return transcribe.MergeAndSortSegments([][]transcribe.Segment{
		transcribe.AssembleSegments(speakersRoomID, room, 0, 0),
		transcribe.AssembleSegments(speakersBenID, ben, 0, 0),
	})
}

func writeSpeakersBundle(t *testing.T, dir string, opts speakersBundleOptions) string {
	t.Helper()
	bundle := filepath.Join(dir, "meeting.meeting")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(bundle, "meeting.webm")
	if opts.realAudio {
		if out, err := exec.Command("ffmpeg", "-y", "-v", "error",
			"-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000:duration=8",
			"-c:a", "libopus", "-application", "voip", audio,
		).CombinedOutput(); err != nil {
			t.Fatalf("write audio: %v: %s", err, out)
		}
	} else if err := os.WriteFile(audio, []byte("opaque audio bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	streams := []transcribe.AudioStream{
		{Index: 0, SpeakerID: speakersRoomID, SpeakerLabel: "Meeting room"},
		{Index: 1, SpeakerID: speakersBenID, SpeakerLabel: "Ben"},
	}
	if opts.silent {
		streams = append(streams, transcribe.AudioStream{Index: 2, SpeakerID: speakersCleoID, SpeakerLabel: "Cleo"})
	}
	transcriptPath := filepath.Join(bundle, "transcript.words.v1.json")
	input := transcribe.ManifestInput{
		SrcBasename: "2026-10-08 10-00-00.mkv", SrcDurationMS: 8000, DigestDurationMS: 8000,
		Streams: streams,
	}
	if opts.untranscribed {
		// As writeUntranscribed does: every participant listed, no segments.
		if err := transcribe.WriteTranscriptJSON(transcriptPath, streams, nil, 8000); err != nil {
			t.Fatal(err)
		}
		empty, err := transcribe.ReadTranscriptSpeakers(transcriptPath)
		if err != nil {
			t.Fatal(err)
		}
		roster := []transcribe.RosterEntry{}
		for _, s := range streams {
			roster = append(roster, transcribe.RosterEntry{ID: s.SpeakerID, Label: s.SpeakerLabel})
		}
		listed, err := empty.WithSpeakers(nil, roster)
		if err != nil {
			t.Fatal(err)
		}
		if err := listed.Write(transcriptPath); err != nil {
			t.Fatal(err)
		}
		input.Processing = &portable.Processing{Transcription: portable.TranscriptionStatus{Status: "skipped", Reason: "model_unavailable"}}
	} else {
		segments := speakersFixtureSegments()
		if err := transcribe.WriteTranscriptJSON(transcriptPath, streams, segments, 8000); err != nil {
			t.Fatal(err)
		}
		if err := transcribe.WriteCaptionsVTT(filepath.Join(bundle, "captions.vtt"), streams, segments); err != nil {
			t.Fatal(err)
		}
		input.Segments = segments
		input.STTBackend, input.STTModelID, input.STTDevice = "sherpa-onnx", "parakeet-tdt-0.6b-v3-int8", "cpu"
	}
	if opts.additional {
		extra := "transcript.other.words.v1.json"
		if err := transcribe.WriteTranscriptJSON(filepath.Join(bundle, extra), streams, speakersFixtureSegments(), 8000); err != nil {
			t.Fatal(err)
		}
		input.Additional = []transcribe.AdditionalTranscript{{ID: "other", Path: extra, ModelID: "other-model"}}
	}
	if err := transcribe.WriteManifest(filepath.Join(bundle, "manifest.json"), input); err != nil {
		t.Fatal(err)
	}
	if !opts.untranscribed {
		for _, name := range []string{speakersDisplayTranscript, speakersReadableTranscript} {
			if err := os.WriteFile(filepath.Join(bundle, name), []byte(`{"version":"stale"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := FinalizeMeetingBundle(MeetingBundle{RootDir: bundle, ManifestPath: filepath.Join(bundle, "cassini.json")},
		MeetingBundleManifest{SourceKind: "mkv", SourcePath: "/tmp/source.mkv"}); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// writeSpeakersTurns stores a turn set for the room: voice 0 until 3 s, then
// voice 1 (or only voice 0 when oneVoice).
func writeSpeakersTurns(t *testing.T, dir string, oneVoice bool) string {
	t.Helper()
	turnsDir := filepath.Join(dir, "turns")
	if err := os.MkdirAll(turnsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	second := 1
	if oneVoice {
		second = 0
	}
	set := transcribe.SpeakerTurnSet{
		Format: transcribe.SpeakerTurnsFormat, SpeakerID: speakersRoomID, Streams: []int{0},
		Source:     transcribe.TurnSetSource{File: "meeting.mkv", SHA256: strings.Repeat("c", 64)},
		Assignment: transcribe.DiarizationAssignment, DurationMS: 8000,
		Model:  transcribe.TurnSetModel{Name: "nvidia/Nemotron-3-Diarization INT8", SHA256: strings.Repeat("d", 64), Runtime: "1.13.7-cassini.6"},
		Params: transcribe.TurnSetParams{MinDurationOn: 0.3, MinDurationOff: 0.5, Threads: 2, Provider: "cpu"},
		Turns: []transcribe.SpeakerTurnEntry{
			{StartMS: 0, EndMS: 3000, Speaker: 0},
			{StartMS: 3000, EndMS: 6000, Speaker: second},
		},
	}
	if err := transcribe.WriteSpeakerTurnSet(filepath.Join(turnsDir, speakersRoomID+".json"), set); err != nil {
		t.Fatal(err)
	}
	return turnsDir
}

func writeSpeakersEdits(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func speakersSplitDoc(revision int, extra string) string {
	return fmt.Sprintf(`{"format":"cassini.speaker-edits.v1","revision":%d,"splits":[{"speakerId":%q}],%s}`,
		revision, speakersRoomID, extra)
}

const speakersNoMergesNoLabels = `"merges":[],"labels":[]`

func runSpeakersForTest(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), append([]string{"speakers"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func speakersApplyOK(t *testing.T, bundle, edits, turnsDir string) speakersApplyReport {
	t.Helper()
	code, stdout, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", turnsDir, "--json")
	if code != 0 {
		t.Fatalf("apply: exit %d stderr=%q", code, stderr)
	}
	var report speakersApplyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	return report
}

// snapshotDir maps every file name in dir to its bytes.
func snapshotDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	return out
}

func diffSnapshots(before, after map[string][]byte) []string {
	var diffs []string
	for name, b := range before {
		a, ok := after[name]
		switch {
		case !ok:
			diffs = append(diffs, "removed "+name)
		case !bytes.Equal(a, b):
			diffs = append(diffs, "changed "+name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			diffs = append(diffs, "added "+name)
		}
	}
	sort.Strings(diffs)
	return diffs
}

func readJSONMap(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func rosterOf(t *testing.T, path string) []transcribe.RosterEntry {
	t.Helper()
	tr, err := transcribe.ReadTranscriptSpeakers(path)
	if err != nil {
		t.Fatal(err)
	}
	return tr.Roster()
}

func wordsBySpeaker(t *testing.T, path string) map[string][]string {
	t.Helper()
	tr, err := transcribe.ReadTranscriptSpeakers(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, seg := range tr.Segments() {
		for _, w := range seg.Words {
			out[seg.SpeakerID] = append(out[seg.SpeakerID], w.Text)
		}
	}
	return out
}

func TestSpeakersApplySplitsASharedDevice(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	editsBody := speakersSplitDoc(1, speakersNoMergesNoLabels)
	edits := writeSpeakersEdits(t, tmp, "edits.json", editsBody)
	before := snapshotDir(t, bundle)
	beforeManifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))

	report := speakersApplyOK(t, bundle, edits, turnsDir)

	voice1, voice2 := speakersRoomID+"~1", speakersRoomID+"~2"
	wantReport := speakersApplyReport{
		Revision:     1,
		Splits:       []speakersSplitReport{{SpeakerID: speakersRoomID, Voices: []string{voice1, voice2}}},
		Missing:      []string{},
		Inconclusive: []string{},
		SpeakerCount: 3,
	}
	if !reflect.DeepEqual(report, wantReport) {
		t.Errorf("report = %+v, want %+v", report, wantReport)
	}

	// The original transcript is kept byte for byte.
	after := snapshotDir(t, bundle)
	if !bytes.Equal(after[speakersRawASRTranscript], before["transcript.words.v1.json"]) {
		t.Error("raw-asr transcript is not the original byte for byte")
	}
	if !bytes.Equal(after["meeting.webm"], before["meeting.webm"]) {
		t.Error("audio changed")
	}
	if string(after[speakersEditsFile]) != editsBody {
		t.Errorf("speaker-edits.json = %q, want the applied document verbatim", after[speakersEditsFile])
	}
	for _, name := range []string{speakersDisplayTranscript, speakersReadableTranscript} {
		if _, ok := after[name]; ok {
			t.Errorf("%s survived; its speaker labels are stale", name)
		}
	}

	// The primary transcript has the voices.
	wantRoster := []transcribe.RosterEntry{
		{ID: voice1, Label: "Meeting room · Speaker 1"},
		{ID: voice2, Label: "Meeting room · Speaker 2"},
		{ID: speakersBenID, Label: "Ben"},
	}
	if got := rosterOf(t, filepath.Join(bundle, "transcript.words.v1.json")); !reflect.DeepEqual(got, wantRoster) {
		t.Errorf("roster = %+v, want %+v", got, wantRoster)
	}
	wantWords := map[string][]string{
		voice1:        {"Good", "morning", "everyone."},
		voice2:        {"Thanks,", "happy", "here."},
		speakersBenID: {"Hello", "both."},
	}
	if got := wordsBySpeaker(t, filepath.Join(bundle, "transcript.words.v1.json")); !reflect.DeepEqual(got, wantWords) {
		t.Errorf("words = %v, want %v", got, wantWords)
	}

	captions := string(after["captions.vtt"])
	if !strings.Contains(captions, "<Meeting room · Speaker 2> Thanks, happy here.") || !strings.Contains(captions, "<Ben> Hello both.") {
		t.Errorf("captions were not regenerated from the voices:\n%s", captions)
	}

	// The manifest points at both transcripts and records the split.
	manifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))
	files := manifest["files"].(map[string]any)
	transcripts := files["transcripts"].([]any)
	if len(transcripts) != 2 {
		t.Fatalf("files.transcripts = %v, want separated-voices and raw-asr", transcripts)
	}
	separated, raw := transcripts[0].(map[string]any), transcripts[1].(map[string]any)
	if separated["id"] != "separated-voices" || separated["path"] != "transcript.words.v1.json" || separated["default"] != true {
		t.Errorf("first transcript = %v", separated)
	}
	if raw["id"] != "raw-asr" || raw["path"] != speakersRawASRTranscript || raw["default"] != nil {
		t.Errorf("second transcript = %v", raw)
	}
	stt := beforeManifest["provenance"].(map[string]any)["speechToText"].(map[string]any)
	if !reflect.DeepEqual(raw["provenance"], stt) {
		t.Errorf("raw-asr provenance = %v, want a copy of provenance.speechToText %v", raw["provenance"], stt)
	}
	sepProv := separated["provenance"].(map[string]any)
	for key, value := range stt {
		if !reflect.DeepEqual(sepProv[key], value) {
			t.Errorf("separated-voices provenance %s = %v, want %v", key, sepProv[key], value)
		}
	}
	record, ok := sepProv["x-speakerDiarization"].(map[string]any)
	if !ok {
		t.Fatalf("separated-voices provenance has no x-speakerDiarization: %v", sepProv)
	}
	if record["model"] != "nvidia/Nemotron-3-Diarization INT8" || record["modelSha256"] != strings.Repeat("d", 64) ||
		record["backend"] != "sherpa-onnx 1.13.7-cassini.6 Nemotron diarization, CPU, 2 threads" ||
		record["sourceSeparation"] != false || record["editsRevision"] != float64(1) ||
		record["editsSha256"] != sha256Hex([]byte(editsBody)) || record["assignment"] != transcribe.DiarizationAssignment ||
		record["minDurationOn"] != 0.3 || record["minDurationOff"] != 0.5 {
		t.Errorf("x-speakerDiarization = %v", record)
	}
	wantSplits := []any{map[string]any{
		"speakerId": speakersRoomID, "voices": []any{voice1, voice2},
		"turnCount": float64(2), "speakerCount": float64(2), "inconclusive": false,
	}}
	if !reflect.DeepEqual(record["splits"], wantSplits) {
		t.Errorf("splits = %v, want %v", record["splits"], wantSplits)
	}
	if _, ok := record["base"]; ok {
		t.Error("the provenance copy carries the base stash")
	}
	top, ok := manifest["x-speakerDiarization"].(map[string]any)
	if !ok || top["editsRevision"] != float64(1) {
		t.Errorf("manifest x-speakerDiarization = %v", manifest["x-speakerDiarization"])
	}
	if manifest["speakerCount"] != float64(3) || manifest["wordCount"] != beforeManifest["wordCount"] {
		t.Errorf("speakerCount=%v wordCount=%v, want 3 and unchanged %v", manifest["speakerCount"], manifest["wordCount"], beforeManifest["wordCount"])
	}
	if manifest["segmentCount"] != float64(3) {
		t.Errorf("segmentCount = %v, want 3", manifest["segmentCount"])
	}

	// The bundle is still a ready meeting bundle (every declared file exists).
	if err := validateReadyMeetingBundleContents(bundle); err != nil {
		t.Errorf("bundle no longer valid: %v", err)
	}
}

func TestSpeakersApplyTwiceChangesNothing(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	edits := writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels))
	first := speakersApplyOK(t, bundle, edits, turnsDir)
	once := snapshotDir(t, bundle)

	second := speakersApplyOK(t, bundle, edits, turnsDir)

	if diffs := diffSnapshots(once, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("a second apply of the same document changed the bundle: %v", diffs)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("reports differ: %+v then %+v", first, second)
	}
}

// Removing every edit restores the bundle as the build wrote it, including a
// second model's transcript entry, and leaves no file behind.
func TestSpeakersApplyEmptyDocumentRestoresTheBuild(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{additional: true})
	for _, name := range []string{speakersDisplayTranscript, speakersReadableTranscript} {
		_ = os.Remove(filepath.Join(bundle, name))
	}
	built := snapshotDir(t, bundle)
	turnsDir := writeSpeakersTurns(t, tmp, false)
	split := writeSpeakersEdits(t, tmp, "split.json", speakersSplitDoc(1, `"merges":[],"labels":[{"speakerId":"`+speakersBenID+`","label":"Benedict"}]`))

	speakersApplyOK(t, bundle, split, turnsDir)
	manifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))
	var ids []string
	for _, entry := range manifest["files"].(map[string]any)["transcripts"].([]any) {
		ids = append(ids, entry.(map[string]any)["id"].(string))
	}
	if want := []string{"separated-voices", "raw-asr", "other"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("transcripts = %v, want %v (the second model's entry kept after the two)", ids, want)
	}

	empty := writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`)
	report := speakersApplyOK(t, bundle, empty, turnsDir)

	if diffs := diffSnapshots(built, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("undoing every edit left a trace: %v", diffs)
	}
	if report.Revision != 2 || len(report.Splits) != 0 || report.SpeakerCount != 2 {
		t.Errorf("report = %+v", report)
	}
}

// An edits document that never changed anything writes nothing at all.
func TestSpeakersApplyNoOpOnAFreshBundleWritesNothing(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	before := snapshotDir(t, bundle)
	// A label for a voice that does not exist is kept but has no effect.
	edits := writeSpeakersEdits(t, tmp, "edits.json", `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[],"merges":[],"labels":[{"speakerId":"`+speakersRoomID+`~3","label":"Ann"}]}`)

	speakersApplyOK(t, bundle, edits, t.TempDir())

	if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("a no-op apply changed the bundle: %v", diffs)
	}
}

// A rerun whose transcription was skipped builds a bundle that lists the
// participants but has no words. Replaying the stored edits on it must leave it
// as built: participants, speakerCount and the untranscribed marker included.
func TestSpeakersApplyLeavesAnUntranscribedBundleAsBuilt(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{untranscribed: true})
	if got := readJSONMap(t, filepath.Join(bundle, "manifest.json"))["files"].(map[string]any)["transcripts"]; !reflect.DeepEqual(got,
		[]any{map[string]any{"id": "untranscribed", "path": "transcript.words.v1.json", "default": true}}) {
		t.Fatalf("fixture is not an untranscribed build: files.transcripts = %v", got)
	}
	before := snapshotDir(t, bundle)
	turnsDir := writeSpeakersTurns(t, tmp, false)

	for _, body := range []string{
		speakersSplitDoc(1, `"merges":[],"labels":[{"speakerId":"`+speakersBenID+`","label":"Benedict"}]`),
		`{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`,
	} {
		report := speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "edits.json", body), turnsDir)
		if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
			t.Errorf("apply changed an untranscribed bundle: %v", diffs)
		}
		if report.SpeakerCount != 2 {
			t.Errorf("report speakerCount = %d, want the build's 2", report.SpeakerCount)
		}
	}
}

// speakerCount counts people, not roster entries: a participant who said
// nothing has no roster entry but stays counted through a split and a merge.
func TestSpeakersApplyKeepsCountingASilentParticipant(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{silent: true})
	if got := readJSONMap(t, filepath.Join(bundle, "manifest.json"))["speakerCount"]; got != float64(3) {
		t.Fatalf("fixture speakerCount = %v, want 3 (room, Ben, silent Cleo)", got)
	}
	turnsDir := writeSpeakersTurns(t, tmp, false)
	voice1, voice2 := speakersRoomID+"~1", speakersRoomID+"~2"

	split := speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "split.json", speakersSplitDoc(1, speakersNoMergesNoLabels)), turnsDir)
	if got := readJSONMap(t, filepath.Join(bundle, "manifest.json"))["speakerCount"]; split.SpeakerCount != 4 || got != float64(4) {
		t.Errorf("after the split: report %d manifest %v, want 4 (two voices, Ben, Cleo)", split.SpeakerCount, got)
	}

	merged := speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "merged.json", speakersSplitDoc(2,
		`"merges":[{"from":"`+voice2+`","into":"`+voice1+`"}],"labels":[]`)), turnsDir)
	if got := readJSONMap(t, filepath.Join(bundle, "manifest.json"))["speakerCount"]; merged.SpeakerCount != 3 || got != float64(3) {
		t.Errorf("after the merge: report %d manifest %v, want 3 (one voice, Ben, Cleo)", merged.SpeakerCount, got)
	}
}

// A crash part way through leaves the raw-asr copy next to a manifest the
// build wrote, with no x-speakerDiarization record. The next apply must take
// the build's values from that manifest, so the split is right and a later
// undo still restores the build exactly.
func TestSpeakersApplyRecoversFromAnInterruptedApply(t *testing.T) {
	cases := map[string]func(t *testing.T, bundle string, built map[string][]byte, tmp, turnsDir string){
		// The first apply copied the original and stopped before the manifest.
		"interrupted split": func(t *testing.T, bundle string, built map[string][]byte, _, _ string) {
			if err := os.WriteFile(filepath.Join(bundle, speakersRawASRTranscript), built["transcript.words.v1.json"], 0o644); err != nil {
				t.Fatal(err)
			}
		},
		// An undo restored the manifest and stopped before removing the copy.
		"interrupted undo": func(t *testing.T, bundle string, built map[string][]byte, tmp, turnsDir string) {
			speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "first.json", speakersSplitDoc(1, speakersNoMergesNoLabels)), turnsDir)
			for _, name := range []string{"manifest.json", "transcript.words.v1.json", "captions.vtt"} {
				if err := os.WriteFile(filepath.Join(bundle, name), built[name], 0o644); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, crash := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{additional: true})
			for _, name := range []string{speakersDisplayTranscript, speakersReadableTranscript} {
				_ = os.Remove(filepath.Join(bundle, name))
			}
			built := snapshotDir(t, bundle)
			turnsDir := writeSpeakersTurns(t, tmp, false)
			crash(t, bundle, built, tmp, turnsDir)

			speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "split.json", speakersSplitDoc(2, speakersNoMergesNoLabels)), turnsDir)
			manifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))
			var ids []string
			for _, entry := range manifest["files"].(map[string]any)["transcripts"].([]any) {
				ids = append(ids, entry.(map[string]any)["id"].(string))
			}
			if manifest["speakerCount"] != float64(3) || !reflect.DeepEqual(ids, []string{"separated-voices", "raw-asr", "other"}) {
				t.Errorf("after the crash a split wrote speakerCount %v transcripts %v, want 3 and separated-voices, raw-asr, other", manifest["speakerCount"], ids)
			}

			speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":3,"splits":[],"merges":[],"labels":[]}`), turnsDir)
			if diffs := diffSnapshots(built, snapshotDir(t, bundle)); len(diffs) != 0 {
				t.Errorf("undo after the crash did not restore the build: %v", diffs)
			}
		})
	}
}

// The Go build writes no display or readable transcript; an older bundle may
// have them. A split removes them (their speaker labels go stale) and an undo
// does not bring them back: the viewer derives both from the words. The rest
// of the bundle is restored as built.
func TestSpeakersApplyUndoLeavesDisplayTranscriptsRemoved(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	manifestPath := filepath.Join(bundle, "manifest.json")
	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseOrderedJSONObject(manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	files, err := manifest.object("files")
	if err != nil {
		t.Fatal(err)
	}
	files.set("displayTranscript", mustJSON(speakersDisplayTranscript))
	files.set("readableTranscript", mustJSON(speakersReadableTranscript))
	if err := manifest.setObject("files", files); err != nil {
		t.Fatal(err)
	}
	if err := writeOrderedJSON(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	built := snapshotDir(t, bundle)
	turnsDir := writeSpeakersTurns(t, tmp, false)

	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "split.json", speakersSplitDoc(1, speakersNoMergesNoLabels)), turnsDir)
	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`), turnsDir)

	want := []string{"changed manifest.json", "removed " + speakersDisplayTranscript, "removed " + speakersReadableTranscript}
	if diffs := diffSnapshots(built, snapshotDir(t, bundle)); !reflect.DeepEqual(diffs, want) {
		t.Errorf("after undo: %v, want %v", diffs, want)
	}
	restored := readJSONMap(t, manifestPath)["files"].(map[string]any)
	if _, ok := restored["displayTranscript"]; ok {
		t.Error("files.displayTranscript names a removed file")
	}
	if _, ok := restored["readableTranscript"]; ok {
		t.Error("files.readableTranscript names a removed file")
	}
	if err := validateReadyMeetingBundleContents(bundle); err != nil {
		t.Errorf("bundle after undo is not valid: %v", err)
	}
}

func TestSpeakersApplyNamesAndMergesVoices(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	voice1, voice2 := speakersRoomID+"~1", speakersRoomID+"~2"

	named := writeSpeakersEdits(t, tmp, "named.json", speakersSplitDoc(2,
		`"merges":[],"labels":[{"speakerId":"`+voice1+`","label":"Mira"},{"speakerId":"`+voice2+`","label":"Leo"}]`))
	speakersApplyOK(t, bundle, named, turnsDir)
	want := []transcribe.RosterEntry{{ID: voice1, Label: "Mira"}, {ID: voice2, Label: "Leo"}, {ID: speakersBenID, Label: "Ben"}}
	if got := rosterOf(t, filepath.Join(bundle, "transcript.words.v1.json")); !reflect.DeepEqual(got, want) {
		t.Errorf("named roster = %+v, want %+v", got, want)
	}

	merged := writeSpeakersEdits(t, tmp, "merged.json", speakersSplitDoc(3,
		`"merges":[{"from":"`+voice2+`","into":"`+voice1+`"}],"labels":[{"speakerId":"`+voice1+`","label":"Mira"}]`))
	report := speakersApplyOK(t, bundle, merged, turnsDir)
	want = []transcribe.RosterEntry{{ID: voice1, Label: "Mira"}, {ID: speakersBenID, Label: "Ben"}}
	if got := rosterOf(t, filepath.Join(bundle, "transcript.words.v1.json")); !reflect.DeepEqual(got, want) {
		t.Errorf("merged roster = %+v, want %+v", got, want)
	}
	if report.Merged == 0 || report.SpeakerCount != 2 {
		t.Errorf("report = %+v, want a merge and 2 speakers", report)
	}
	if got := readJSONMap(t, filepath.Join(bundle, "manifest.json"))["speakerCount"]; got != float64(2) {
		t.Errorf("manifest speakerCount = %v, want 2", got)
	}
	// The base is still the build's transcript, not the previous result.
	if got := rosterOf(t, filepath.Join(bundle, speakersRawASRTranscript)); len(got) != 2 || got[0].ID != speakersRoomID {
		t.Errorf("raw-asr roster = %+v, want the original devices", got)
	}
}

// A split that finds one voice is not an error, but the device stays one
// speaker and nothing claims several people were there.
func TestSpeakersApplyInconclusiveSplitLeavesTheDevice(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	before := snapshotDir(t, bundle)
	turnsDir := writeSpeakersTurns(t, tmp, true)
	edits := writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels))

	report := speakersApplyOK(t, bundle, edits, turnsDir)

	if !reflect.DeepEqual(report.Inconclusive, []string{speakersRoomID}) || len(report.Splits) != 1 ||
		!report.Splits[0].Inconclusive || len(report.Splits[0].Voices) != 0 {
		t.Errorf("report = %+v, want one inconclusive split with no voices", report)
	}
	if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("an inconclusive split changed the bundle: %v", diffs)
	}
}

func TestSpeakersApplyRefusesASplitWithoutTurns(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	before := snapshotDir(t, bundle)
	edits := writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels))

	code, _, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", t.TempDir(), "--json")

	if code != speakersExitTurnsMissing || !strings.HasPrefix(stderr, "turns-missing: "+speakersRoomID) {
		t.Errorf("exit %d stderr %q, want %d turns-missing", code, stderr, speakersExitTurnsMissing)
	}
	if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("a refused apply changed the bundle: %v", diffs)
	}
}

func TestSpeakersApplyRefusesInvalidInput(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	before := snapshotDir(t, bundle)
	cases := map[string]string{
		"unknown member":   `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[],"merges":[],"labels":[],"extra":1}`,
		"split of a voice": `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[{"speakerId":"a~1"}],"merges":[],"labels":[]}`,
		"path in an id":    `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[{"speakerId":"../x"}],"merges":[],"labels":[]}`,
	}
	for name, body := range cases {
		edits := writeSpeakersEdits(t, tmp, "edits.json", body)
		if code, _, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", tmp); code != speakersExitRuntime {
			t.Errorf("%s: exit %d stderr %q, want %d", name, code, stderr, speakersExitRuntime)
		}
	}
	if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("a refused apply changed the bundle: %v", diffs)
	}
	for _, args := range [][]string{
		{"apply", bundle},
		{"apply", "--edits", "x.json", "--turns-dir", tmp},
		{"bogus"},
		{},
	} {
		if code, _, _ := runSpeakersForTest(args...); code != speakersExitUsage {
			t.Errorf("speakers %v: exit %d, want usage %d", args, code, speakersExitUsage)
		}
	}
}

// The guard against an accidental re-encode: if the audio changes while apply
// runs, apply fails rather than publishing transcripts against other audio.
func TestSpeakersApplyFailsIfTheAudioChanges(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	edits := writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels))
	previous := speakersBeforeAudioCheck
	t.Cleanup(func() { speakersBeforeAudioCheck = previous })
	speakersBeforeAudioCheck = func(root string) {
		_ = os.WriteFile(filepath.Join(root, "meeting.webm"), []byte("re-encoded"), 0o644)
	}

	code, _, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", turnsDir)

	if code != speakersExitRuntime || !strings.Contains(stderr, "meeting audio changed") {
		t.Errorf("exit %d stderr %q, want a runtime failure naming the audio", code, stderr)
	}
}

// ---------------------------------------------------------------- diarize

func fakeSpeakersDiarization(t *testing.T, model func(string) (transcribe.DiarizationModel, error), diarize func(context.Context, string, string, transcribe.DiarizationModel) (transcribe.SpeakerTurnSet, error)) {
	t.Helper()
	prevResolve, prevLoad, prevDiarize := resolveDiarizationModelFn, loadDiarizationModelFn, diarizeSpeakerFn
	t.Cleanup(func() {
		resolveDiarizationModelFn, loadDiarizationModelFn, diarizeSpeakerFn = prevResolve, prevLoad, prevDiarize
	})
	resolveDiarizationModelFn = model
	loadDiarizationModelFn = model
	diarizeSpeakerFn = diarize
}

func speakersDummyMKV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meeting.mkv")
	if err := os.WriteFile(path, []byte("not probed"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSpeakersDiarizeWritesTheTurns(t *testing.T) {
	mkv := speakersDummyMKV(t)
	model := transcribe.DiarizationModel{Path: "/models/m.onnx", Name: "nvidia/Nemotron-3-Diarization INT8", SHA256: strings.Repeat("d", 64)}
	var gotModel transcribe.DiarizationModel
	var gotModelArg, gotPath, gotSpeaker string
	fakeSpeakersDiarization(t,
		func(arg string) (transcribe.DiarizationModel, error) { gotModelArg = arg; return model, nil },
		func(_ context.Context, path, speaker string, m transcribe.DiarizationModel) (transcribe.SpeakerTurnSet, error) {
			gotPath, gotSpeaker, gotModel = path, speaker, m
			return transcribe.SpeakerTurnSet{
				Format: transcribe.SpeakerTurnsFormat, SpeakerID: speaker, Streams: []int{0}, ElapsedMS: 42,
				Turns: []transcribe.SpeakerTurnEntry{{StartMS: 0, EndMS: 1000, Speaker: 0}, {StartMS: 1000, EndMS: 2000, Speaker: 1}},
			}, nil
		})
	out := filepath.Join(t.TempDir(), speakersRoomID+".json")

	code, stdout, stderr := runSpeakersForTest("diarize", mkv, "--speaker", speakersRoomID, "--out", out, "--model", "/models/m.onnx")

	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if gotModelArg != "/models/m.onnx" || gotPath != mkv || gotSpeaker != speakersRoomID || gotModel != model {
		t.Errorf("diarized %q %q with %+v (model arg %q)", gotPath, gotSpeaker, gotModel, gotModelArg)
	}
	set, err := transcribe.ReadSpeakerTurnSet(out)
	if err != nil || set.SpeakerID != speakersRoomID || len(set.Turns) != 2 {
		t.Errorf("stored turns = %+v, %v", set, err)
	}
	if want := fmt.Sprintf("speaker-turns -> %s (2 turns, 2 voices, 42 ms)\n", out); stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestSpeakersDiarizeWithoutAModelIsUnavailable(t *testing.T) {
	fakeSpeakersDiarization(t,
		func(string) (transcribe.DiarizationModel, error) {
			return transcribe.DiarizationModel{}, fmt.Errorf("%w: set %s", transcribe.ErrDiarizationUnavailable, transcribe.DiarizationModelEnv)
		},
		func(context.Context, string, string, transcribe.DiarizationModel) (transcribe.SpeakerTurnSet, error) {
			t.Fatal("diarized without a model")
			return transcribe.SpeakerTurnSet{}, nil
		})
	out := filepath.Join(t.TempDir(), "turns.json")

	code, _, stderr := runSpeakersForTest("diarize", speakersDummyMKV(t), "--speaker", speakersRoomID, "--out", out)

	if code != speakersExitUnavailable || !strings.HasPrefix(stderr, "diarization-unavailable:") {
		t.Errorf("exit %d stderr %q, want %d diarization-unavailable", code, stderr, speakersExitUnavailable)
	}
	assertNotExist(t, out)
}

func TestSpeakersDiarizeUnknownSpeaker(t *testing.T) {
	fakeSpeakersDiarization(t,
		func(string) (transcribe.DiarizationModel, error) { return transcribe.DiarizationModel{Path: "m"}, nil },
		func(_ context.Context, _, speaker string, _ transcribe.DiarizationModel) (transcribe.SpeakerTurnSet, error) {
			return transcribe.SpeakerTurnSet{}, fmt.Errorf("%w: %s", transcribe.ErrSpeakerNotFound, speaker)
		})
	out := filepath.Join(t.TempDir(), "turns.json")

	code, _, stderr := runSpeakersForTest("diarize", speakersDummyMKV(t), "--speaker", "spk_nobody", "--out", out)

	if code != speakersExitNotFound || stderr != "speaker-not-found: spk_nobody\n" {
		t.Errorf("exit %d stderr %q, want %d speaker-not-found", code, stderr, speakersExitNotFound)
	}
	assertNotExist(t, out)
}

func TestSpeakersDiarizeOtherFailures(t *testing.T) {
	fakeSpeakersDiarization(t,
		func(string) (transcribe.DiarizationModel, error) { return transcribe.DiarizationModel{Path: "m"}, nil },
		func(context.Context, string, string, transcribe.DiarizationModel) (transcribe.SpeakerTurnSet, error) {
			return transcribe.SpeakerTurnSet{}, errors.New("ffmpeg exploded")
		})
	if code, _, stderr := runSpeakersForTest("diarize", speakersDummyMKV(t), "--speaker", speakersRoomID, "--out", filepath.Join(t.TempDir(), "t.json")); code != speakersExitRuntime {
		t.Errorf("exit %d stderr %q, want %d", code, stderr, speakersExitRuntime)
	}
	if code, _, _ := runSpeakersForTest("diarize", filepath.Join(t.TempDir(), "missing.mkv"), "--speaker", speakersRoomID, "--out", "t.json"); code != speakersExitRuntime {
		t.Errorf("missing input: exit %d, want %d", code, speakersExitRuntime)
	}
	if code, _, _ := runSpeakersForTest("diarize", speakersDummyMKV(t), "--out", "t.json"); code != speakersExitUsage {
		t.Errorf("no --speaker: exit %d, want %d", code, speakersExitUsage)
	}
}

// The real DiarizeSpeaker wiring: a recording without that participant is
// reported before any model runs.
func TestSpeakersDiarizeRealRecordingWithoutTheSpeaker(t *testing.T) {
	requireFFMediaTools(t)
	if !transcribe.HasDiarizationRuntime() {
		t.Skip("native runtime without Nemotron diarization")
	}
	dir := t.TempDir()
	mkv := filepath.Join(dir, "meeting.mkv")
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-c:a", "libopus", "-metadata:s:a:0", "participant_id=room", "-metadata:s:a:0", "participant_name=Room", mkv,
	).CombinedOutput(); err != nil {
		t.Fatalf("write mkv: %v: %s", err, out)
	}
	model := filepath.Join(dir, "model.int8.onnx")
	if err := os.WriteFile(model, []byte("never loaded"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runSpeakersForTest("diarize", mkv, "--speaker", "spk_nobody", "--out", filepath.Join(dir, "t.json"), "--model", model)

	if code != speakersExitNotFound || !strings.HasPrefix(stderr, "speaker-not-found:") {
		t.Errorf("exit %d stderr %q, want %d", code, stderr, speakersExitNotFound)
	}
}

// ---------------------------------------------------------------- packed

// A split meeting packs like any other: the separated voices are the default
// transcript, the original stays as raw-asr, the split's record survives, and
// the audio identity is the pre-split pack's, so marks carried across stay
// resolved.
func TestSpeakersApplyThenPackKeepsTheAudioAndTheMarks(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{realAudio: true})
	delivered := packAnnotateBundle(t, bundle, filepath.Join(tmp, "delivered.opus"))
	applyInPlace(t, delivered, annotateTwoMarks)

	turnsDir := writeSpeakersTurns(t, tmp, false)
	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(4, speakersNoMergesNoLabels)), turnsDir)
	sealed := packAnnotateBundle(t, bundle, filepath.Join(tmp, "sealed.opus"))

	manifest := decodePortableManifestFromOpus(t, sealed)
	if got, want := manifest.Integrity.OpusSHA256, decodePortableManifestFromOpus(t, delivered).Integrity.OpusSHA256; got != want {
		t.Fatalf("audio identity changed: %s, pre-split %s", got, want)
	}
	defaultID := ""
	var wordIDs []string
	for _, entry := range manifest.Transcripts {
		if entry.Role != "" {
			continue
		}
		wordIDs = append(wordIDs, entry.ID)
		if entry.Default {
			defaultID = entry.ID
		}
	}
	if defaultID != "separated-voices" || !reflect.DeepEqual(wordIDs, []string{"separated-voices", "raw-asr"}) {
		t.Errorf("transcripts %v default %q, want separated-voices (default) and raw-asr", wordIDs, defaultID)
	}
	voice1, voice2 := speakersRoomID+"~1", speakersRoomID+"~2"
	wantSpeakers := []portable.Speaker{
		{ID: voice1, Label: "Meeting room · Speaker 1"},
		{ID: voice2, Label: "Meeting room · Speaker 2"},
		{ID: speakersBenID, Label: "Ben"},
	}
	if !reflect.DeepEqual(manifest.Speakers, wantSpeakers) {
		t.Errorf("speakers = %+v, want %+v", manifest.Speakers, wantSpeakers)
	}
	if manifest.Provenance == nil || manifest.Provenance.SpeechToText == nil {
		t.Fatal("no speech-to-text provenance in the packed file")
	}
	var record speakerDiarizationRecord
	if err := json.Unmarshal(manifest.Provenance.SpeechToText.SpeakerDiarization, &record); err != nil {
		t.Fatalf("x-speakerDiarization did not survive pack: %v (%s)", err, manifest.Provenance.SpeechToText.SpeakerDiarization)
	}
	if record.EditsRevision != 4 || len(record.Splits) != 1 || !reflect.DeepEqual(record.Splits[0].Voices, []string{voice1, voice2}) || record.Base != nil {
		t.Errorf("packed record = %+v", record)
	}

	out := filepath.Join(tmp, "outgoing.opus")
	result := annotateOK(t, "", "carry", delivered, sealed, "--out", out, "--json")
	if result.Carried != 2 || result.Resolved == nil || !*result.Resolved {
		t.Errorf("carry: carried=%d resolved=%v, want 2 resolved marks", result.Carried, result.Resolved)
	}

	code, stdout, stderr := runSpeakersForTest("show", out, "--json")
	if code != 0 {
		t.Fatalf("show: exit %d stderr %q", code, stderr)
	}
	var shown speakersShowResult
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.DefaultTranscript != "separated-voices" || shown.EditsRevision == nil || *shown.EditsRevision != 4 ||
		len(shown.Speakers) != 3 || shown.Speakers[0].Device != speakersRoomID {
		t.Errorf("show = %+v", shown)
	}
}

func TestSpeakersShowOnABundle(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})

	code, stdout, _ := runSpeakersForTest("show", bundle, "--json")
	var plain speakersShowResult
	if code != 0 || json.Unmarshal([]byte(stdout), &plain) != nil {
		t.Fatalf("show: exit %d %s", code, stdout)
	}
	if plain.DefaultTranscript != "raw-asr" || plain.EditsRevision != nil || len(plain.Speakers) != 2 {
		t.Errorf("show before any edit = %+v", plain)
	}

	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels)), writeSpeakersTurns(t, tmp, false))
	code, stdout, _ = runSpeakersForTest("show", bundle)
	if code != 0 || !strings.Contains(stdout, "default transcript: separated-voices (of separated-voices, raw-asr)") ||
		!strings.Contains(stdout, "speaker edits revision: 1") || !strings.Contains(stdout, "(voice on Meeting room)") {
		t.Errorf("show after a split: exit %d\n%s", code, stdout)
	}
}
