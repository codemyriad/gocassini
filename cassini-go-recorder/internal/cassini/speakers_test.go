package cassini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		Summary:      "none",
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
		record["sourceSeparation"] != false || record["assignment"] != transcribe.DiarizationAssignment ||
		record["minDurationOn"] != 0.3 || record["minDurationOff"] != 0.5 {
		t.Errorf("x-speakerDiarization = %v", record)
	}
	editsRecord, ok := sepProv["x-speakerEdits"].(map[string]any)
	if !ok || editsRecord["editsRevision"] != float64(1) || editsRecord["editsSha256"] != sha256Hex([]byte(editsBody)) {
		t.Errorf("separated-voices x-speakerEdits = %v, want revision 1 and the edits' SHA-256", sepProv["x-speakerEdits"])
	}
	if _, ok := raw["provenance"].(map[string]any)["x-speakerEdits"]; ok {
		t.Error("the original transcript says it reflects the edits")
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
	if !ok || !reflect.DeepEqual(top["splits"], wantSplits) || top["base"] == nil {
		t.Errorf("manifest x-speakerDiarization = %v, want the splits and the build's members", manifest["x-speakerDiarization"])
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

// rewriteSpeakersTurns changes the room's stored turn set in place.
func rewriteSpeakersTurns(t *testing.T, turnsDir string, change func(*transcribe.SpeakerTurnSet)) {
	t.Helper()
	path := filepath.Join(turnsDir, speakersRoomID+".json")
	set, err := transcribe.ReadSpeakerTurnSet(path)
	if err != nil {
		t.Fatal(err)
	}
	change(&set)
	if err := transcribe.WriteSpeakerTurnSet(path, set); err != nil {
		t.Fatal(err)
	}
}

// Turns are times in one recording. Given the recording, apply refuses turns
// measured on any other: they would credit words to voices that never said
// them, and they are stored write-once, so nothing would ever correct it.
func TestSpeakersApplyRefusesTurnsOfAnotherRecording(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	edits := writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels))
	recording := filepath.Join(tmp, "capture.mkv")
	if err := os.WriteFile(recording, []byte("the capture"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, bundle)

	code, _, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", turnsDir, "--recording", recording)

	if code != speakersExitRuntime || !strings.HasPrefix(stderr, "turns-source-mismatch: "+speakersRoomID) {
		t.Fatalf("exit %d stderr %q, want a runtime failure naming the mismatch", code, stderr)
	}
	if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Fatalf("a refused apply changed the bundle: %v", diffs)
	}

	sum := sha256.Sum256([]byte("the capture"))
	rewriteSpeakersTurns(t, turnsDir, func(set *transcribe.SpeakerTurnSet) { set.Source.SHA256 = hex.EncodeToString(sum[:]) })
	if code, _, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", turnsDir, "--recording", recording); code != 0 {
		t.Fatalf("turns of this recording: exit %d stderr %q", code, stderr)
	}
}

func TestSpeakersApplyRefusesTurnsThatCannotBeApplied(t *testing.T) {
	cases := map[string]func(*transcribe.SpeakerTurnSet){
		"no recording":   func(s *transcribe.SpeakerTurnSet) { s.Source.SHA256 = "" },
		"no model":       func(s *transcribe.SpeakerTurnSet) { s.Model.SHA256 = "" },
		"backwards turn": func(s *transcribe.SpeakerTurnSet) { s.Turns[1].EndMS = s.Turns[1].StartMS },
		"negative start": func(s *transcribe.SpeakerTurnSet) { s.Turns[0].StartMS = -5 },
		"negative voice": func(s *transcribe.SpeakerTurnSet) { s.Turns[0].Speaker = -1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
			turnsDir := writeSpeakersTurns(t, tmp, false)
			rewriteSpeakersTurns(t, turnsDir, change)
			edits := writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels))
			before := snapshotDir(t, bundle)

			code, _, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", turnsDir)

			if code != speakersExitRuntime || !strings.HasPrefix(stderr, "read turns: ") {
				t.Errorf("exit %d stderr %q, want the turns refused", code, stderr)
			}
			if diffs := diffSnapshots(before, snapshotDir(t, bundle)); len(diffs) != 0 {
				t.Errorf("a refused apply changed the bundle: %v", diffs)
			}
		})
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

func fakeSpeakersDiarization(t *testing.T, model func(string) (transcribe.DiarizationModel, error), diarize func(context.Context, string, string, transcribe.DiarizationModel, int) (transcribe.SpeakerTurnSet, error)) {
	t.Helper()
	// The inference lock lives in the model store; keep it out of $HOME.
	t.Setenv("CASSINI_CACHE_ROOT", t.TempDir())
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
		func(_ context.Context, path, speaker string, m transcribe.DiarizationModel, _ int) (transcribe.SpeakerTurnSet, error) {
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

// The operator decides how many CPU threads a diarization may use and says so
// in $CASSINI_DIARIZATION_THREADS: an integer, 2 when unset (as on Android),
// kept between 1 and 16. Anything else runs with 2 and says why on stderr.
func TestSpeakersDiarizeTakesItsThreadsFromTheEnvironment(t *testing.T) {
	model := transcribe.DiarizationModel{Path: "/models/m.onnx", Name: "m", SHA256: strings.Repeat("d", 64)}
	var gotThreads int
	fakeSpeakersDiarization(t,
		func(string) (transcribe.DiarizationModel, error) { return model, nil },
		func(_ context.Context, _, speaker string, _ transcribe.DiarizationModel, threads int) (transcribe.SpeakerTurnSet, error) {
			gotThreads = threads
			return transcribe.SpeakerTurnSet{Format: transcribe.SpeakerTurnsFormat, SpeakerID: speaker}, nil
		})
	for _, tc := range []struct {
		env     string
		want    int
		warning bool
	}{
		{env: "", want: 2},
		{env: "6", want: 6},
		{env: " 3 ", want: 3},
		{env: "0", want: 1},
		{env: "-4", want: 1},
		{env: "64", want: 16},
		{env: "many", want: 2, warning: true},
	} {
		t.Setenv(transcribe.DiarizationThreadsEnv, tc.env)
		gotThreads = 0

		code, _, stderr := runSpeakersForTest("diarize", speakersDummyMKV(t), "--speaker", speakersRoomID, "--out", filepath.Join(t.TempDir(), "t.json"))

		if code != 0 || gotThreads != tc.want {
			t.Errorf("%s=%q: exit %d, %d threads, want %d (stderr %q)", transcribe.DiarizationThreadsEnv, tc.env, code, gotThreads, tc.want, stderr)
		}
		if warned := strings.Contains(stderr, transcribe.DiarizationThreadsEnv); warned != tc.warning {
			t.Errorf("%s=%q: stderr %q, want a warning: %v", transcribe.DiarizationThreadsEnv, tc.env, stderr, tc.warning)
		}
	}
}

// Diarizing is inference, so it takes the model store's inference lock, the
// one builds and model checks take: a model check started from a terminal
// must not run beside it. Held elsewhere, diarize waits; it never starts.
func TestSpeakersDiarizeWaitsForTheModelRuntimeLock(t *testing.T) {
	model := transcribe.DiarizationModel{Path: "/models/m.onnx", Name: "m", SHA256: strings.Repeat("d", 64)}
	fakeSpeakersDiarization(t,
		func(string) (transcribe.DiarizationModel, error) { return model, nil },
		func(context.Context, string, string, transcribe.DiarizationModel, int) (transcribe.SpeakerTurnSet, error) {
			t.Error("diarized while another inference held the model runtime lock")
			return transcribe.SpeakerTurnSet{}, nil
		})
	unlock, err := transcribe.LockModelRuntime(context.Background(), os.Getenv("CASSINI_CACHE_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Given up while it waits: what an operator restart does to a waiting refine.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	out := filepath.Join(t.TempDir(), "turns.json")

	code := Run(ctx, []string{"speakers", "diarize", speakersDummyMKV(t), "--speaker", speakersRoomID, "--out", out}, &stdout, &stderr)

	if code != speakersExitRuntime || !strings.Contains(stderr.String(), "model runtime lock") {
		t.Errorf("exit %d stderr %q, want a runtime failure waiting for the lock", code, stderr.String())
	}
	assertNotExist(t, out)
}

func TestSpeakersDiarizeWithoutAModelIsUnavailable(t *testing.T) {
	fakeSpeakersDiarization(t,
		func(string) (transcribe.DiarizationModel, error) {
			return transcribe.DiarizationModel{}, fmt.Errorf("%w: set %s", transcribe.ErrDiarizationUnavailable, transcribe.DiarizationModelEnv)
		},
		func(context.Context, string, string, transcribe.DiarizationModel, int) (transcribe.SpeakerTurnSet, error) {
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
		func(_ context.Context, _, speaker string, _ transcribe.DiarizationModel, _ int) (transcribe.SpeakerTurnSet, error) {
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
		func(context.Context, string, string, transcribe.DiarizationModel, int) (transcribe.SpeakerTurnSet, error) {
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
	// The device stays in the list, before its voices and marked as
	// separated into them: the original transcript still credits it, and a
	// reader that does not know the mark shows one name more rather than
	// refusing the original transcript.
	wantSpeakers := []portable.Speaker{
		{ID: speakersRoomID, Label: "Meeting room", SeparatedInto: []string{voice1, voice2}},
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
	if len(record.Splits) != 1 || !reflect.DeepEqual(record.Splits[0].Voices, []string{voice1, voice2}) || record.Base != nil {
		t.Errorf("packed record = %+v", record)
	}
	var edits speakerEditsRecord
	if err := json.Unmarshal(manifest.Provenance.SpeechToText.SpeakerEdits, &edits); err != nil || edits.EditsRevision != 4 {
		t.Errorf("packed x-speakerEdits = %s (%v), want revision 4", manifest.Provenance.SpeechToText.SpeakerEdits, err)
	}
	if got := portable.People(manifest.Speakers); len(got) != 3 {
		t.Errorf("people = %+v, want the two voices and Ben", got)
	}
	var inspected, inspectErr bytes.Buffer
	if code := Run(context.Background(), []string{"inspect", sealed}, &inspected, &inspectErr); code != 0 || !strings.Contains(inspected.String(), " speakers=3 ") {
		t.Errorf("inspect: exit %d, %q (stderr %q), want speakers=3: the voices, not their device as well", code, inspected.String(), inspectErr.String())
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
		len(shown.Speakers) != 3 || shown.Speakers[0].ID != voice1 || shown.Speakers[0].Device != "Meeting room" {
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

// The originals a speaker edit keeps are the only copies of the build's
// transcript and summary once the edited ones replace them. Each is flushed,
// with its directory entry, while the file it preserves is still the build's:
// after a power loss the edited file is never on disk without its original.
func TestSpeakersApplyMakesTheOriginalsDurableBeforeReplacingThem(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	writeSpeakersSummary(t, bundle, []byte("# The build's summary\n"))
	stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) { return "# Rewritten\n", "m", nil })
	primary := filepath.Join(bundle, speakersDefaultTranscript)
	summary := filepath.Join(bundle, speakersSummaryFile)
	originalTranscript, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	type flush struct {
		name                          string
		transcriptBuilt, summaryBuilt bool
	}
	var flushes []flush
	prev := durableSync
	t.Cleanup(func() { durableSync = prev })
	durableSync = func(f *os.File) error {
		transcript, _ := os.ReadFile(primary)
		summaryNow, _ := os.ReadFile(summary)
		flushes = append(flushes, flush{f.Name(), bytes.Equal(transcript, originalTranscript), string(summaryNow) == "# The build's summary\n"})
		return prev(f)
	}

	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1, speakersNoMergesNoLabels)), writeSpeakersTurns(t, tmp, false))

	rawASR := filepath.Join(bundle, speakersRawASRTranscript)
	baseSummary := filepath.Join(bundle, speakersBaseSummary)
	want := []flush{
		{rawASR + ".tmp", true, true}, {bundle, true, true},
		{baseSummary + ".tmp", false, true}, {bundle, false, true},
	}
	if !reflect.DeepEqual(flushes, want) {
		t.Fatalf("flushes = %+v\nwant      %+v", flushes, want)
	}
}

// stubSummarize replaces the summary model for one test and counts its calls.
func stubSummarize(t *testing.T, fn func(transcribe.TranscriptSpeakers) (string, string, error)) *int {
	t.Helper()
	calls := 0
	prev := summarizeSpeakersFn
	summarizeSpeakersFn = func(tr transcribe.TranscriptSpeakers) (string, string, error) {
		calls++
		return fn(tr)
	}
	t.Cleanup(func() { summarizeSpeakersFn = prev })
	return &calls
}

func writeSpeakersSummary(t *testing.T, bundle string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bundle, speakersSummaryFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// speakersRecord reads x-speakerEdits on the bundle's default transcript;
// ok is false when it has none.
func speakersRecord(t *testing.T, bundle string) (speakerEditsRecord, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseOrderedJSONObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	files, err := manifest.object("files")
	if err != nil {
		t.Fatal(err)
	}
	record, err := readBundleSpeakerEdits(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	return record, record.EditsRevision > 0 || record.EditsSHA256 != ""
}

const speakersNamedMira = `"merges":[],"labels":[{"speakerId":"` + speakersRoomID + `~1","label":"Mira"}]`

// A summary credits whoever the transcript credited when it was written, so a
// split or a name rewrites it; the manifest says it was rewritten, by which
// model and for which edits; undoing every edit brings the build's back and
// drops that record with the rest.
func TestSpeakersApplyRewritesTheSummaryAndUndoRestoresIt(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	original := []byte("## Action items\n- Meeting room: book the hall\n")
	writeSpeakersSummary(t, bundle, original)
	rewritten := "## Action items\n- Mira: book the hall\n"
	var asked []string
	stubSummarize(t, func(tr transcribe.TranscriptSpeakers) (string, string, error) {
		for _, r := range tr.Roster() {
			asked = append(asked, r.Label)
		}
		return rewritten, "test-model", nil
	})

	turnsDir := writeSpeakersTurns(t, tmp, false)
	namedDoc := speakersSplitDoc(1, speakersNamedMira)
	named := writeSpeakersEdits(t, tmp, "named.json", namedDoc)
	report := speakersApplyOK(t, bundle, named, turnsDir)
	if report.Summary != "regenerated" {
		t.Fatalf("summary = %q, want regenerated", report.Summary)
	}
	if want := []string{"Mira", "Meeting room · Speaker 2", "Ben"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("summary written for %v, want the edited speakers %v", asked, want)
	}
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersSummaryFile)); string(got) != rewritten {
		t.Errorf("summary.md = %q, want the rewritten summary", got)
	}
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersBaseSummary)); !bytes.Equal(got, original) {
		t.Errorf("the build's summary was not kept byte for byte: %q", got)
	}
	record, _ := speakersRecord(t, bundle)
	written, err := os.ReadFile(filepath.Join(bundle, speakersDefaultTranscript))
	if err != nil {
		t.Fatal(err)
	}
	want := &speakerSummaryRecord{Rewritten: true, Model: "test-model", SHA256: sha256Hex([]byte(rewritten)), EditsSHA256: sha256Hex([]byte(namedDoc)), TranscriptSHA256: sha256Hex(written)}
	if !reflect.DeepEqual(record.Summary, want) {
		t.Errorf("x-speakerEdits.summary = %+v, want %+v", record.Summary, want)
	}
	if report.SummarySHA256 != want.SHA256 {
		t.Errorf("report summarySha256 = %q, want the rewritten summary's %q", report.SummarySHA256, want.SHA256)
	}

	empty := writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`)
	report = speakersApplyOK(t, bundle, empty, turnsDir)
	if report.Summary != "restored" || report.SummarySHA256 != "" {
		t.Fatalf("summary after undo = %q (sha256 %q), want restored, the build's own", report.Summary, report.SummarySHA256)
	}
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersSummaryFile)); !bytes.Equal(got, original) {
		t.Errorf("summary.md after undo = %q, want the build's", got)
	}
	if fileExists(filepath.Join(bundle, speakersBaseSummary)) {
		t.Error("the kept copy of the build's summary survived the undo")
	}
	if record, ok := speakersRecord(t, bundle); ok {
		t.Errorf("undo left x-speakerEdits (summary %+v)", record.Summary)
	}
}

// The summary model is slow and can fail; it runs before apply writes
// anything, so the bundle is as built for as long as it runs.
func TestSpeakersApplyAsksForTheSummaryBeforeTouchingTheBundle(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	writeSpeakersSummary(t, bundle, []byte("## Summary\nMeeting room spoke.\n"))
	built := snapshotDir(t, bundle)
	var during []string
	stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) {
		during = diffSnapshots(built, snapshotDir(t, bundle))
		return "## Summary\nMira spoke.\n", "test-model", nil
	})
	report := speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "named.json", speakersSplitDoc(1, speakersNamedMira)), writeSpeakersTurns(t, tmp, false))
	if report.Summary != "regenerated" {
		t.Fatalf("summary = %q, want regenerated", report.Summary)
	}
	if len(during) != 0 {
		t.Errorf("the bundle had already changed while the summary model ran: %v", during)
	}
}

// Without a summary model, or when it fails, the transcript is still
// separated; the summary is left as it was, the report says it is stale and
// stderr says why. Applying the same edits again tries again.
func TestSpeakersApplyKeepsTheSummaryWhenItCannotBeRewrittenAndRetries(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	original := []byte("## Summary\nMeeting room spoke.\n")
	writeSpeakersSummary(t, bundle, original)
	failure := errors.New("summary endpoint said 502")
	stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) { return "", "", failure })

	turnsDir := writeSpeakersTurns(t, tmp, false)
	edits := writeSpeakersEdits(t, tmp, "split.json", speakersSplitDoc(1, speakersNoMergesNoLabels))
	code, stdout, stderr := runSpeakersForTest("apply", bundle, "--edits", edits, "--turns-dir", turnsDir, "--json")
	if code != 0 {
		t.Fatalf("apply: exit %d stderr=%q", code, stderr)
	}
	var report speakersApplyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary != "stale" || len(report.Splits) != 1 {
		t.Fatalf("report = %+v, want the split applied and the summary stale", report)
	}
	if !strings.Contains(stderr, "summary not rewritten") || !strings.Contains(stderr, failure.Error()) {
		t.Errorf("stderr = %q, want the reason the summary was not rewritten", stderr)
	}
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersSummaryFile)); !bytes.Equal(got, original) {
		t.Errorf("summary.md = %q, want it left as it was", got)
	}
	if record, _ := speakersRecord(t, bundle); record.Summary != nil {
		t.Errorf("a summary that was not rewritten is recorded as rewritten: %+v", record.Summary)
	}

	stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) {
		return "## Summary\nSpeaker 1 spoke.\n", "test-model", nil
	})
	if report := speakersApplyOK(t, bundle, edits, turnsDir); report.Summary != "regenerated" {
		t.Errorf("retry: summary = %q, want regenerated", report.Summary)
	}
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersBaseSummary)); !bytes.Equal(got, original) {
		t.Errorf("the build's summary was not kept on the retry: %q", got)
	}
}

// Every call to the summary model is billed and its text differs each time.
// A retried apply, or a replay onto the bundle this apply already wrote,
// leaves the summary as it is; other edits, a summary.md that is no longer
// the one apply wrote, and a rebuilt bundle (its fresh summary has no
// record) are rewritten.
func TestSpeakersApplyCallsTheSummaryModelOncePerEdits(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	original := []byte("## Summary\nMeeting room spoke.\n")
	writeSpeakersSummary(t, bundle, original)
	n := 0
	calls := stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) {
		n++
		return fmt.Sprintf("## Summary\nversion %d\n", n), "test-model", nil
	})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	named := writeSpeakersEdits(t, tmp, "named.json", speakersSplitDoc(1, speakersNamedMira))
	expect := func(step string, edits string, status string, wantCalls int) {
		t.Helper()
		if report := speakersApplyOK(t, bundle, edits, turnsDir); report.Summary != status || *calls != wantCalls {
			t.Errorf("%s: summary %q after %d model calls, want %q after %d", step, report.Summary, *calls, status, wantCalls)
		}
	}

	expect("first apply", named, "regenerated", 1)
	written, _ := os.ReadFile(filepath.Join(bundle, speakersSummaryFile))
	expect("same edits again", named, "unchanged", 1)
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersSummaryFile)); !bytes.Equal(got, written) {
		t.Errorf("same edits rewrote summary.md: %q, want %q", got, written)
	}
	if record, _ := speakersRecord(t, bundle); record.Summary == nil || record.Summary.SHA256 != sha256Hex(written) {
		t.Errorf("an unchanged apply lost the summary record: %+v", record.Summary)
	}
	// An apply that leaves the summary still says which one the meeting has:
	// a reader may not have the earlier apply's yet.
	if report := speakersApplyOK(t, bundle, named, turnsDir); report.SummarySHA256 != sha256Hex(written) {
		t.Errorf("an unchanged apply reports summarySha256 %q, want the kept summary's %q", report.SummarySHA256, sha256Hex(written))
	}

	expect("other edits", writeSpeakersEdits(t, tmp, "renamed.json", speakersSplitDoc(2,
		`"merges":[],"labels":[{"speakerId":"`+speakersRoomID+`~1","label":"Mina"}]`)), "regenerated", 2)
	// The same edits saved again, at a new revision, and a name for a voice
	// that has no words: the transcript the model would read is the same.
	expect("same edits, next revision", writeSpeakersEdits(t, tmp, "renamed-again.json", speakersSplitDoc(3,
		`"merges":[],"labels":[{"speakerId":"`+speakersRoomID+`~1","label":"Mina"},{"speakerId":"`+speakersRoomID+`~9","label":"Nobody"}]`)), "unchanged", 2)
	if record, _ := speakersRecord(t, bundle); record.Summary == nil || record.Summary.EditsSHA256 != record.EditsSHA256 {
		t.Errorf("an unchanged summary does not name the edits it now stands for: %+v (edits %s)", record.Summary, record.EditsSHA256)
	}

	writeSpeakersSummary(t, bundle, []byte("## Summary\nedited by hand\n"))
	renamed := filepath.Join(tmp, "renamed.json")
	expect("summary.md replaced since", renamed, "regenerated", 3)
	if got, _ := os.ReadFile(filepath.Join(bundle, speakersBaseSummary)); !bytes.Equal(got, original) {
		t.Errorf("the build's summary copy changed: %q", got)
	}

	// A rerun rebuilds the bundle with its own fresh summary and replays the
	// last edits: that summary credits the device, so it is rewritten.
	if err := os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}
	bundle = writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	writeSpeakersSummary(t, bundle, original)
	expect("replay after a rebuild", renamed, "regenerated", 4)
}

// An undo puts the build's summary back and removes its kept copy last, after
// the raw-asr copy. If it stops in between, the next undo still finishes the
// job and says the summary was restored.
func TestSpeakersApplyFinishesAnUndoThatStoppedBeforeDroppingTheSummaryCopy(t *testing.T) {
	tmp := t.TempDir()
	bundle := writeSpeakersBundle(t, tmp, speakersBundleOptions{})
	original := []byte("## Summary\nMeeting room spoke.\n")
	writeSpeakersSummary(t, bundle, original)
	built := snapshotDir(t, bundle)
	stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) {
		return "## Summary\nMira spoke.\n", "test-model", nil
	})
	turnsDir := writeSpeakersTurns(t, tmp, false)
	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "named.json", speakersSplitDoc(1, speakersNamedMira)), turnsDir)

	// The state an undo leaves when it stops after removing the raw-asr copy:
	// everything is as built except the kept copy of the summary.
	for name, body := range built {
		if err := os.WriteFile(filepath.Join(bundle, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{speakersRawASRTranscript, speakersEditsFile} {
		if err := os.Remove(filepath.Join(bundle, name)); err != nil {
			t.Fatal(err)
		}
	}
	if !fileExists(filepath.Join(bundle, speakersBaseSummary)) {
		t.Fatal("setup: the summary copy should still be there")
	}

	empty := writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`)
	if report := speakersApplyOK(t, bundle, empty, turnsDir); report.Summary != "restored" {
		t.Errorf("summary = %q, want restored", report.Summary)
	}
	if diffs := diffSnapshots(built, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("the retried undo did not restore the build: %v", diffs)
	}
}

// ---------------------------------------------------------------- rename

const speakersRenameBen = `{"format":"cassini.speaker-edits.v1","revision":1,"splits":[],"merges":[],"labels":[{"speakerId":"` + speakersBenID + `","label":"Benedict"}]}`

// speakersWithoutDisplay is a bundle as the current build writes it: no
// display or readable transcript, so an undo can restore it byte for byte.
func speakersWithoutDisplay(t *testing.T, tmp string, opts speakersBundleOptions) string {
	t.Helper()
	bundle := writeSpeakersBundle(t, tmp, opts)
	for _, name := range []string{speakersDisplayTranscript, speakersReadableTranscript} {
		if err := os.Remove(filepath.Join(bundle, name)); err != nil {
			t.Fatal(err)
		}
	}
	return bundle
}

// transcriptWithoutLabels is a transcript file with every speaker label
// blanked, to compare what a rename must leave alone.
func transcriptWithoutLabels(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, speaker := range doc["speakers"].([]any) {
		speaker.(map[string]any)["label"] = ""
	}
	return doc
}

// Naming a speaker separates nobody's voice. The meeting keeps its one
// transcript, with only that speaker's label changed; the original is kept
// beside it for an exact undo but is not listed, so it is never packed; and
// nothing says voices were separated. The edits are recorded on the
// transcript's own speech-to-text step, with the summary they rewrote.
func TestSpeakersApplyARenameOnlyRelabelsTheOneTranscript(t *testing.T) {
	tmp := t.TempDir()
	bundle := speakersWithoutDisplay(t, tmp, speakersBundleOptions{additional: true})
	writeSpeakersSummary(t, bundle, []byte("## Summary\nBen spoke.\n"))
	rewritten := "## Summary\nBenedict spoke.\n"
	stubSummarize(t, func(transcribe.TranscriptSpeakers) (string, string, error) { return rewritten, "test-model", nil })
	built := snapshotDir(t, bundle)
	builtManifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))
	edits := writeSpeakersEdits(t, tmp, "rename.json", speakersRenameBen)

	report := speakersApplyOK(t, bundle, edits, t.TempDir())

	want := speakersApplyReport{
		Revision: 1, Splits: []speakersSplitReport{}, Missing: []string{}, Inconclusive: []string{},
		SpeakerCount: 2, Summary: "regenerated", SummarySHA256: sha256Hex([]byte(rewritten)),
	}
	if !reflect.DeepEqual(report, want) {
		t.Errorf("report = %+v, want %+v", report, want)
	}
	after := snapshotDir(t, bundle)
	if got := rosterOf(t, filepath.Join(bundle, speakersDefaultTranscript)); !reflect.DeepEqual(got, []transcribe.RosterEntry{
		{ID: speakersRoomID, Label: "Meeting room"}, {ID: speakersBenID, Label: "Benedict"},
	}) {
		t.Errorf("roster = %+v, want Ben renamed and nothing else", got)
	}
	if got, want := transcriptWithoutLabels(t, after[speakersDefaultTranscript]), transcriptWithoutLabels(t, built[speakersDefaultTranscript]); !reflect.DeepEqual(got, want) {
		t.Error("a rename changed the transcript beyond its speaker labels")
	}
	if !bytes.Equal(after[speakersRawASRTranscript], built[speakersDefaultTranscript]) {
		t.Error("the original transcript is not kept byte for byte for the undo")
	}
	if !strings.Contains(string(after["captions.vtt"]), "<Benedict> Hello both.") {
		t.Errorf("captions do not use the new name:\n%s", after["captions.vtt"])
	}

	manifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))
	files := manifest["files"].(map[string]any)
	entries := files["transcripts"].([]any)
	delete(entries[0].(map[string]any)["provenance"].(map[string]any), speakersEditsKey)
	if !reflect.DeepEqual(files, builtManifest["files"]) {
		t.Errorf("files = %v, want the build's %v less the edits record (no transcript added)", files, builtManifest["files"])
	}
	for _, key := range []string{"speakerCount", "segmentCount", "wordCount"} {
		if manifest[key] != builtManifest[key] {
			t.Errorf("%s = %v, want the build's %v", key, manifest[key], builtManifest[key])
		}
	}
	if _, ok := manifest[speakersDiarizationKey]; ok {
		t.Errorf("a rename recorded a diarization: %v", manifest[speakersDiarizationKey])
	}
	stt := manifest["provenance"].(map[string]any)["speechToText"].(map[string]any)
	if _, ok := stt[speakersDiarizationKey]; ok {
		t.Error("a rename put x-speakerDiarization on the speech-to-text step")
	}
	record, ok := speakersRecord(t, bundle)
	wantRecord := speakerEditsRecord{
		EditsRevision: 1, EditsSHA256: sha256Hex([]byte(speakersRenameBen)),
		Summary: &speakerSummaryRecord{Rewritten: true, Model: "test-model", SHA256: sha256Hex([]byte(rewritten)), EditsSHA256: sha256Hex([]byte(speakersRenameBen)), TranscriptSHA256: sha256Hex(after[speakersDefaultTranscript])},
	}
	if !ok || !reflect.DeepEqual(record, wantRecord) {
		t.Errorf("x-speakerEdits = %+v, want %+v", record, wantRecord)
	}

	code, stdout, _ := runSpeakersForTest("show", bundle, "--json")
	var shown speakersShowResult
	if code != 0 || json.Unmarshal([]byte(stdout), &shown) != nil {
		t.Fatalf("show: exit %d %s", code, stdout)
	}
	if shown.DefaultTranscript != "parakeet-tdt-0-6b-v3-int8" || shown.EditsRevision == nil || *shown.EditsRevision != 1 || !reflect.DeepEqual(shown.Transcripts, []string{"parakeet-tdt-0-6b-v3-int8", "other"}) {
		t.Errorf("show after a rename = %+v", shown)
	}

	// Applying it again changes nothing and asks no model.
	speakersApplyOK(t, bundle, edits, t.TempDir())
	if diffs := diffSnapshots(after, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("a second apply of the rename changed the bundle: %v", diffs)
	}

	// Undoing it restores the build exactly.
	empty := writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`)
	if report := speakersApplyOK(t, bundle, empty, t.TempDir()); report.Summary != "restored" {
		t.Errorf("undo: summary %q, want restored", report.Summary)
	}
	if diffs := diffSnapshots(built, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("undoing a rename left a trace: %v", diffs)
	}
}

// A packed renamed meeting has one transcript, the build's raw-asr, whose
// speakers carry the new name; the kept original is not in the file, and the
// edits revision is on that transcript's step for readers to compare.
func TestSpeakersApplyARenameOnlyPacksOneTranscript(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundle := speakersWithoutDisplay(t, tmp, speakersBundleOptions{realAudio: true})
	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "rename.json", speakersRenameBen), t.TempDir())

	manifest := decodePortableManifestFromOpus(t, packAnnotateBundle(t, bundle, filepath.Join(tmp, "renamed.opus")))

	var ids []string
	for _, entry := range manifest.Transcripts {
		if entry.Role == "" {
			ids = append(ids, entry.ID)
		}
	}
	if !reflect.DeepEqual(ids, []string{"raw-asr"}) {
		t.Errorf("transcripts = %v, want the one raw-asr transcript", ids)
	}
	if want := []portable.Speaker{{ID: speakersRoomID, Label: "Meeting room"}, {ID: speakersBenID, Label: "Benedict"}}; !reflect.DeepEqual(manifest.Speakers, want) {
		t.Errorf("speakers = %+v, want %+v", manifest.Speakers, want)
	}
	stt := manifest.Provenance.SpeechToText
	if len(stt.SpeakerDiarization) != 0 {
		t.Errorf("a rename packed a diarization record: %s", stt.SpeakerDiarization)
	}
	var edits speakerEditsRecord
	if err := json.Unmarshal(stt.SpeakerEdits, &edits); err != nil || edits.EditsRevision != 1 {
		t.Errorf("x-speakerEdits = %s (%v), want revision 1", stt.SpeakerEdits, err)
	}
}

// A meeting moves between a rename and a split in either direction, and each
// state is the one a fresh apply of its edits gives; undoing everything at the
// end restores the build.
func TestSpeakersApplyMovesBetweenARenameAndASplit(t *testing.T) {
	tmp := t.TempDir()
	bundle := speakersWithoutDisplay(t, tmp, speakersBundleOptions{additional: true})
	built := snapshotDir(t, bundle)
	turnsDir := writeSpeakersTurns(t, tmp, false)
	renamed := `"merges":[],"labels":[{"speakerId":"` + speakersBenID + `","label":"Benedict"}]`
	rename := writeSpeakersEdits(t, tmp, "rename.json", speakersRenameBen)
	split := writeSpeakersEdits(t, tmp, "split.json", speakersSplitDoc(1, renamed))

	// What each document gives on a fresh bundle.
	fresh := func(edits string) map[string][]byte {
		t.Helper()
		dir := t.TempDir()
		other := speakersWithoutDisplay(t, dir, speakersBundleOptions{additional: true})
		speakersApplyOK(t, other, edits, turnsDir)
		snapshot := snapshotDir(t, other)
		delete(snapshot, "cassini.json") // names the bundle's own path
		return snapshot
	}
	wantRenamed, wantSplit := fresh(rename), fresh(split)

	for i, step := range []struct {
		edits string
		want  map[string][]byte
	}{{rename, wantRenamed}, {split, wantSplit}, {rename, wantRenamed}} {
		speakersApplyOK(t, bundle, step.edits, turnsDir)
		got := snapshotDir(t, bundle)
		delete(got, "cassini.json")
		if diffs := diffSnapshots(step.want, got); len(diffs) != 0 {
			t.Errorf("step %d (%s): differs from a fresh apply: %v", i+1, filepath.Base(step.edits), diffs)
		}
	}

	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":4,"splits":[],"merges":[],"labels":[]}`), turnsDir)
	if diffs := diffSnapshots(built, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("undo after a rename and a split left a trace: %v", diffs)
	}
}

// A rename interrupted after it kept the original and relabelled the
// transcript, but before the manifest, is finished by the next apply, and its
// undo restores the build.
func TestSpeakersApplyRecoversFromAnInterruptedRename(t *testing.T) {
	tmp := t.TempDir()
	bundle := speakersWithoutDisplay(t, tmp, speakersBundleOptions{})
	built := snapshotDir(t, bundle)
	edits := writeSpeakersEdits(t, tmp, "rename.json", speakersRenameBen)
	speakersApplyOK(t, bundle, edits, t.TempDir())
	done := snapshotDir(t, bundle)
	// The crash: the manifest is still the build's.
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), built["manifest.json"], 0o644); err != nil {
		t.Fatal(err)
	}

	speakersApplyOK(t, bundle, edits, t.TempDir())
	if diffs := diffSnapshots(done, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("the retried rename differs from an uninterrupted one: %v", diffs)
	}
	speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "empty.json", `{"format":"cassini.speaker-edits.v1","revision":2,"splits":[],"merges":[],"labels":[]}`), t.TempDir())
	if diffs := diffSnapshots(built, snapshotDir(t, bundle)); len(diffs) != 0 {
		t.Errorf("undo after the interrupted rename left a trace: %v", diffs)
	}
}

// A split that hears one voice separates nothing, so a name saved with it is
// a rename: no separated transcript, no diarization record, and the report
// still says the split was inconclusive.
func TestSpeakersApplyAnInconclusiveSplitWithANameIsARename(t *testing.T) {
	tmp := t.TempDir()
	bundle := speakersWithoutDisplay(t, tmp, speakersBundleOptions{})
	turnsDir := writeSpeakersTurns(t, tmp, true)

	report := speakersApplyOK(t, bundle, writeSpeakersEdits(t, tmp, "edits.json", speakersSplitDoc(1,
		`"merges":[],"labels":[{"speakerId":"`+speakersBenID+`","label":"Benedict"}]`)), turnsDir)

	if !reflect.DeepEqual(report.Inconclusive, []string{speakersRoomID}) || report.SpeakerCount != 2 {
		t.Errorf("report = %+v, want the split inconclusive and 2 speakers", report)
	}
	manifest := readJSONMap(t, filepath.Join(bundle, "manifest.json"))
	if _, ok := manifest["files"].(map[string]any)["transcripts"]; ok {
		t.Errorf("files.transcripts = %v, want none added", manifest["files"].(map[string]any)["transcripts"])
	}
	if _, ok := manifest[speakersDiarizationKey]; ok {
		t.Error("a split that separated nobody recorded a diarization")
	}
	if got := rosterOf(t, filepath.Join(bundle, speakersDefaultTranscript)); got[1].Label != "Benedict" || got[0].ID != speakersRoomID {
		t.Errorf("roster = %+v", got)
	}
}
