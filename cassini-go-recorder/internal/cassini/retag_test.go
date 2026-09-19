package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// packFixtureOpus produces a real portable meeting file for the retag tests to
// edit. Retag's whole job is preserving a file it did not write field by field,
// so every test below starts from an actual packed artifact rather than a
// hand-built tag map.
func packFixtureOpus(t *testing.T, dir, name string, packArgs ...string) string {
	t.Helper()
	bundleDir := filepath.Join(dir, name+".meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	outPath := filepath.Join(dir, name+".opus")
	args := append([]string{"pack", bundleDir, "--out", outPath}, packArgs...)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("pack fixture failed code=%d stderr=%q", code, stderr.String())
	}
	return outPath
}

func TestRetagSetsTheRoomInBothTheManifestAndTheTags(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	outPath := filepath.Join(tmp, "after.opus")

	const roomID = "rm_9f2a1c3d4e5b6a70"
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--room-id", roomID,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	// Both, together. A file whose plain tag says one thing and whose payload
	// says another is the exact failure this command exists to avoid: the
	// exporter reads the payload, so a tags-only edit republishes as roomless
	// while looking correct to ffprobe.
	if got := readOpusTag(t, outPath, "CASSINI_ROOM_ID"); got != roomID {
		t.Errorf("CASSINI_ROOM_ID = %q, want %q", got, roomID)
	}
	if got := decodePortableManifestFromOpus(t, outPath).Meeting.RoomID; got != roomID {
		t.Errorf("manifest meeting.roomId = %q, want %q", got, roomID)
	}
}

func TestRetagPreservesEverythingItWasNotAskedToChange(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before",
		"--title", "Weekly Sync", "--room-token", "a7bc3k9x",
		"--job-id", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "--attempt-number", "2")
	outPath := filepath.Join(tmp, "after.opus")

	before := decodePortableManifestFromOpus(t, inPath)
	beforeTags, err := portableMeetingTags(inPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--room-id", "rm_1111111111111111",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	after := decodePortableManifestFromOpus(t, outPath)
	if after.Meeting.RoomID != "rm_1111111111111111" {
		t.Fatalf("meeting.roomId = %q, want the new id", after.Meeting.RoomID)
	}
	// Everything else in the meeting, byte for byte in meaning.
	if after.Meeting.ID != before.Meeting.ID {
		t.Errorf("meeting.id changed: %q -> %q", before.Meeting.ID, after.Meeting.ID)
	}
	if after.Meeting.Title != before.Meeting.Title {
		t.Errorf("meeting.title changed: %q -> %q", before.Meeting.Title, after.Meeting.Title)
	}
	if after.Meeting.JobID != before.Meeting.JobID || after.Meeting.AttemptNumber != before.Meeting.AttemptNumber {
		t.Errorf("provenance changed: %q/%d -> %q/%d",
			before.Meeting.JobID, before.Meeting.AttemptNumber, after.Meeting.JobID, after.Meeting.AttemptNumber)
	}
	// The integrity block is the part a UseNumber-less decode would quietly
	// perturb, and it is the part the whole format is checkable by.
	if after.Integrity != before.Integrity {
		t.Errorf("integrity block changed:\n before %+v\n after  %+v", before.Integrity, after.Integrity)
	}
	if after.Audio != before.Audio {
		t.Errorf("audio block changed:\n before %+v\n after  %+v", before.Audio, after.Audio)
	}

	// And every non-payload tag, including the per-transcript chunk sets.
	// ffmpeg runs with
	// -map_metadata -1, so a tag not carried forward is deleted.
	afterTags, err := portableMeetingTags(outPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	for key, want := range beforeTags {
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "CASSINI_PAYLOAD_") || upper == "CASSINI_ROOM_ID" {
			continue
		}
		if got := portableTagValue(afterTags, key); got != strings.TrimSpace(want) {
			t.Errorf("tag %s = %q after retag, want the original %q", key, got, want)
		}
	}
	// Specifically: the transcript payloads must still be there.
	transcriptChunks := 0
	for key := range afterTags {
		if strings.HasPrefix(strings.ToUpper(key), "CASSINI_TX_") {
			transcriptChunks++
		}
	}
	if transcriptChunks == 0 {
		t.Error("no CASSINI_TX_* tags survived the retag; the file lost its transcript bodies")
	}
}

func TestRetagClearsFields(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before",
		"--room-token", "a7bc3k9x", "--job-id", "01ABCDEFGHJKMNPQRSTVWXYZ01", "--attempt-number", "4")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath,
		"--clear-room-id", "--clear-job-id", "--clear-attempt-number",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	after := decodePortableManifestFromOpus(t, outPath)
	if after.Meeting.RoomID != "" || after.Meeting.JobID != "" || after.Meeting.AttemptNumber != 0 {
		t.Errorf("cleared fields survived: room=%q job=%q attempt=%d",
			after.Meeting.RoomID, after.Meeting.JobID, after.Meeting.AttemptNumber)
	}
	// The mirrors go too. A stale CASSINI_ROOM_ID is worse than no tag: the
	// shell readers these exist for would believe it over the payload.
	for _, tag := range []string{"CASSINI_ROOM_ID", "CASSINI_JOB_ID", "CASSINI_ATTEMPT_NUMBER"} {
		if got := readOpusTag(t, outPath, tag); got != "" {
			t.Errorf("%s = %q after clearing, want absent", tag, got)
		}
	}
}

func TestRetagJSONSummaryReportsOnlyRealChanges(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before", "--room-token", "a7bc3k9x")
	existing := decodePortableManifestFromOpus(t, inPath).Meeting.RoomID
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--json",
		// The room id it already has, plus a job it does not.
		"--room-id", existing, "--job-id", "01ABCDEFGHJKMNPQRSTVWXYZ01",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	var summary RetagSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("parse --json summary %q: %v", stdout.String(), err)
	}
	// Setting a field to the value it already holds is not a change. A caller
	// re-running a backfill needs "nothing moved" to be visible, not buried in
	// a list of no-ops.
	if len(summary.Changes) != 1 {
		t.Fatalf("changes = %+v, want exactly the jobId change", summary.Changes)
	}
	if summary.Changes[0].Field != "jobId" {
		t.Errorf("changed field = %q, want %q", summary.Changes[0].Field, "jobId")
	}
	if summary.Changes[0].From != nil {
		t.Errorf("jobId from = %v, want null for a field that was absent", summary.Changes[0].From)
	}
	if summary.MeetingID == "" {
		t.Error("summary has no meetingId")
	}
}

// The payload DESCRIPTOR tags share the CASSINI_PAYLOAD_ prefix with the chunk
// set and have nothing to do with it. A blanket prefix drop deleted all three
// from every re-tagged file — invisibly, because nothing in this repo needs
// them to decode a payload, and the scripts upload the result over a recording
// that under D-612 cannot be deleted.
func TestRetagKeepsThePayloadDescriptorTags(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--room-id", "rm_9f2a1c3d4e5b6a70",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	// Required by docs/portable-meeting-format.md, and asserted against the
	// input rather than a literal so this keeps working if the values change.
	beforeTags, err := portableMeetingTags(inPath)
	if err != nil {
		t.Fatalf("read input tags: %v", err)
	}
	afterTags, err := portableMeetingTags(outPath)
	if err != nil {
		t.Fatalf("read output tags: %v", err)
	}
	for _, tag := range []string{"CASSINI_PAYLOAD_MIME", "CASSINI_PAYLOAD_ENCODING", "CASSINI_PAYLOAD_SCHEMA"} {
		want := portableTagValue(beforeTags, tag)
		if want == "" {
			t.Fatalf("the fixture does not carry %s, so this test proves nothing", tag)
		}
		if got := portableTagValue(afterTags, tag); got != want {
			t.Errorf("%s = %q after retag, want the original %q", tag, got, want)
		}
	}
	// And the counters ARE rewritten, since the payload changed.
	if portableTagValue(afterTags, "CASSINI_PAYLOAD_SHA256") == portableTagValue(beforeTags, "CASSINI_PAYLOAD_SHA256") {
		t.Error("CASSINI_PAYLOAD_SHA256 did not change, so the payload was not rewritten")
	}
}

// Go's flag package accepts `--clear-room-id=false` for a bool. Deciding "was
// this asked for" from fs.Visit alone made the explicit "do NOT clear" spelling
// clear the field — the exact opposite of what it says.
func TestRetagHonoursAnExplicitlyFalseClearFlag(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before", "--room-token", "a7bc3k9x",
		"--job-id", "01ABCDEFGHJKMNPQRSTVWXYZ01", "--attempt-number", "2")
	before := decodePortableManifestFromOpus(t, inPath)
	if before.Meeting.RoomID == "" || before.Meeting.JobID == "" {
		t.Fatal("the fixture must carry a room and a job for this to prove anything")
	}
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath,
		"--clear-room-id=false", "--clear-job-id=false", "--clear-attempt-number=false",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	after := decodePortableManifestFromOpus(t, outPath)
	if after.Meeting.RoomID != before.Meeting.RoomID {
		t.Errorf("--clear-room-id=false cleared the room: %q -> %q", before.Meeting.RoomID, after.Meeting.RoomID)
	}
	if after.Meeting.JobID != before.Meeting.JobID {
		t.Errorf("--clear-job-id=false cleared the job: %q -> %q", before.Meeting.JobID, after.Meeting.JobID)
	}
	if after.Meeting.AttemptNumber != before.Meeting.AttemptNumber {
		t.Errorf("--clear-attempt-number=false cleared the attempt: %d -> %d", before.Meeting.AttemptNumber, after.Meeting.AttemptNumber)
	}
	// ...and =true still clears, so the fix did not simply disable the flags.
	clearedPath := filepath.Join(tmp, "cleared.opus")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{
		"retag", inPath, "--out", clearedPath, "--clear-room-id=true",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag --clear-room-id=true failed code=%d stderr=%q", code, stderr.String())
	}
	if got := decodePortableManifestFromOpus(t, clearedPath).Meeting.RoomID; got != "" {
		t.Errorf("--clear-room-id=true left roomId = %q", got)
	}
}

// ffprobe reports Vorbis comment keys in whatever case the muxer wrote them,
// and builds disagree. An exact-case delete on a file tagged `cassini_room_id`
// would leave the old value in place AND add the new one under a second key, so
// the file would name two different rooms.
func TestRetagMirrorTagsAreCaseInsensitive(t *testing.T) {
	tags := map[string]string{
		"cassini_room_id":             "rm_1111111111111111",
		"Cassini_Payload_Chunk_Count": "1",
		"CASSINI_PAYLOAD_MIME":        "application/vnd.cassini.portable-meeting+json",
		"cassini_payload_000":         "stale-chunk",
	}
	manifest := portable.Manifest{Meeting: portable.Meeting{RoomID: "rm_2222222222222222"}}

	updated, err := retagOpusTags(tags, []byte(`{"meeting":{"id":"m"}}`), manifest)
	if err != nil {
		t.Fatalf("retagOpusTags: %v", err)
	}

	var roomKeys []string
	for key := range updated {
		if strings.EqualFold(key, "CASSINI_ROOM_ID") {
			roomKeys = append(roomKeys, key)
		}
	}
	if len(roomKeys) != 1 {
		t.Fatalf("got %d room-id keys %v, want exactly one", len(roomKeys), roomKeys)
	}
	if updated[roomKeys[0]] != "rm_2222222222222222" {
		t.Errorf("room id = %q, want the new value", updated[roomKeys[0]])
	}
	// The lower-cased stale chunk must be gone, and the descriptor kept.
	if _, ok := updated["cassini_payload_000"]; ok && updated["cassini_payload_000"] == "stale-chunk" {
		t.Error("a lower-cased stale payload chunk survived")
	}
	if updated["CASSINI_PAYLOAD_MIME"] == "" {
		t.Error("CASSINI_PAYLOAD_MIME was dropped")
	}
	// One chunk-count key, whatever its spelling.
	var countKeys []string
	for key := range updated {
		if strings.EqualFold(key, "CASSINI_PAYLOAD_CHUNK_COUNT") {
			countKeys = append(countKeys, key)
		}
	}
	if len(countKeys) != 1 {
		t.Errorf("got %d chunk-count keys %v, want exactly one", len(countKeys), countKeys)
	}
}

func TestRetagRefusesWritingOverItsInput(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")

	for _, out := range []string{inPath, filepath.Join(tmp, ".", "before.opus")} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{
			"retag", inPath, "--out", out, "--room-id", "rm_1111111111111111",
		}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("retag --out %q exit = %d, want 2", out, code)
		}
		if !strings.Contains(stderr.String(), "differ from the input") {
			t.Errorf("stderr = %q, want it to explain that retag never writes in place", stderr.String())
		}
	}
}

func TestRetagRejectsAnythingThatIsNotADerivedRoomID(t *testing.T) {
	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(inPath, []byte("not really an opus"), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// The one input that must never get through is a raw Talk token pasted
	// where an id belongs: it is a short alphanumeric string that looks
	// plausible, and writing one into a published recording would publish the
	// join link for a public conversation.
	for _, bad := range []string{"a7bc3k9x", "rm_notactuallyhex", "rm_9F2A1C3D4E5B6A70", "rm_9f2a", "weekly-sync"} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{
			"retag", inPath, "--out", filepath.Join(tmp, "out.opus"), "--room-id", bad,
		}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("retag --room-id %q exit = %d, want 2", bad, code)
		}
		if !strings.Contains(stderr.String(), "derived room id") {
			t.Errorf("--room-id %q: stderr = %q, want it to say what a room id looks like", bad, stderr.String())
		}
	}

	if !portable.IsRoomID("rm_9f2a1c3d4e5b6a70") {
		t.Error("IsRoomID rejected a well-formed id")
	}
}

func TestRetagRefusesContradictorySetAndClear(t *testing.T) {
	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(inPath, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", filepath.Join(tmp, "out.opus"),
		"--room-id", "rm_1111111111111111", "--clear-room-id",
	}, &stdout, &stderr)
	// An error, not a precedence rule: any precedence we picked would be a coin
	// flip the caller has to memorise, and a caller that emitted both has a bug
	// worth surfacing.
	if code != 2 {
		t.Fatalf("exit = %d, want 2 for contradictory flags", code)
	}
	if !strings.Contains(stderr.String(), "contradict") {
		t.Errorf("stderr = %q, want it to name the contradiction", stderr.String())
	}
}

func TestRetagRefusesAnEmptySetValue(t *testing.T) {
	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(inPath, []byte("stub"), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// An empty --room-id is indistinguishable from a shell variable that failed
	// to expand. In a tool that edits published recordings that ambiguity is
	// the dangerous kind, so the clear has to be said out loud.
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", filepath.Join(tmp, "out.opus"), "--room-id", "",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 for an empty --room-id", code)
	}
	if !strings.Contains(stderr.String(), "--clear-room-id") {
		t.Errorf("stderr = %q, want it to point at --clear-room-id", stderr.String())
	}
}

func TestRetagRefusesAFileThatIsNotAPortableMeeting(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "in.opus")
	if err := os.WriteFile(inPath, []byte("this is not an ogg container"), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	outPath := filepath.Join(tmp, "out.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--room-id", "rm_1111111111111111",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a non-portable input", code)
	}
	// And it must not have left a half-written file behind for a caller to
	// upload: under D-612 a published recording cannot be deleted.
	if _, err := os.Stat(outPath); err == nil {
		t.Error("a partial output was written for an input that could not be read")
	}
}

func TestRetagAppearsInRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("root usage exit %d", code)
	}
	if !strings.Contains(stdout.String(), "retag") {
		t.Errorf("root usage does not mention retag:\n%s", stdout.String())
	}
}

func TestRetagHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"retag", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini retag ") {
		t.Fatalf("expected retag usage in stderr, got %q", stderr.String())
	}
}

// A capture made through Talk carries the account that dialled in, not the
// person who spoke: a conference talk streamed into a room is labelled with the
// name of whoever held the laptop. Relabelling it is a metadata edit, not a
// reason to re-transcribe 30 minutes of audio.
func TestRetagRelabelsTheSpeakerAndTheTitle(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before", "--title", "From the conference")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath,
		"--title", "It Takes a Village",
		"--speaker", "Paula Grzegorzewska",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	after := decodePortableManifestFromOpus(t, outPath)
	if after.Meeting.Title != "It Takes a Village" {
		t.Errorf("meeting.title = %q, want the new title", after.Meeting.Title)
	}
	if len(after.Speakers) != 1 {
		t.Fatalf("speakers = %d, want 1", len(after.Speakers))
	}
	if after.Speakers[0].Label != "Paula Grzegorzewska" {
		t.Errorf("speaker label = %q, want the new label", after.Speakers[0].Label)
	}
	// The id is the join between the roster and every transcript word. A
	// relabelling that moved it would leave the words attributed to a speaker
	// the manifest no longer names, which renders as an empty transcript rather
	// than as an error.
	if after.Speakers[0].ID != "spk_host" {
		t.Errorf("speaker id = %q, want it untouched at spk_host", after.Speakers[0].ID)
	}
	// TITLE is what ordinary audio players show, and it is the mirror a
	// tags-only reader believes.
	if got := readOpusTag(t, outPath, "TITLE"); got != "It Takes a Village" {
		t.Errorf("TITLE tag = %q, want the new title", got)
	}
}

// The words must still resolve against the roster after a relabelling: this is
// the failure that a manifest-only check cannot see, because a manifest with a
// renamed id validates perfectly on its own.
func TestRetagKeepsTranscriptWordsAttributedAfterRelabelling(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--speaker", "Frank Karlitschek",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"inspect", "--transcript", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("inspect --transcript exit=%d stderr=%q", code, stderr.String())
	}
	var transcript struct {
		Segments []struct {
			Speaker string `json:"speaker"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &transcript); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if len(transcript.Segments) == 0 {
		t.Fatal("the retagged file carries no transcript segments")
	}
	roster := map[string]bool{}
	for _, speaker := range decodePortableManifestFromOpus(t, outPath).Speakers {
		roster[speaker.ID] = true
	}
	for index, segment := range transcript.Segments {
		if !roster[segment.Speaker] {
			t.Errorf("segment %d is attributed to %q, which the roster no longer names", index, segment.Speaker)
		}
	}
}

func TestRetagRelabelsTheSpeakerNamedByID(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--speaker", "spk_host=Frank Karlitschek",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}
	if got := decodePortableManifestFromOpus(t, outPath).Speakers[0].Label; got != "Frank Karlitschek" {
		t.Errorf("speaker label = %q, want the new label", got)
	}
}

// Naming a speaker that is not in the file is a caller working from a stale
// roster. Relabelling the only speaker anyway — or writing the file unchanged
// and reporting success — would attribute a talk to the wrong person.
func TestRetagRefusesAnUnknownSpeakerID(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--speaker", "spk_nobody=Someone Else",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "spk_nobody") {
		t.Errorf("stderr does not name the missing speaker: %q", stderr.String())
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Error("a refused retag left an output file behind")
	}
}

func TestRetagRefusesAmbiguousSpeakerFlags(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")

	for name, args := range map[string][]string{
		// Two bare labels cannot both mean "the file's only speaker".
		"two bare labels":       {"--speaker", "Alice", "--speaker", "Bob"},
		"bare beside addressed": {"--speaker", "Alice", "--speaker", "spk_host=Bob"},
		"same id twice":         {"--speaker", "spk_host=Alice", "--speaker", "spk_host=Bob"},
		"empty label":           {"--speaker", "spk_host="},
		"empty id":              {"--speaker", "=Alice"},
	} {
		t.Run(name, func(t *testing.T) {
			outPath := filepath.Join(t.TempDir(), "after.opus")
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), append([]string{"retag", inPath, "--out", outPath}, args...), &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (configuration error); stderr=%q", code, stderr.String())
			}
			if _, err := os.Stat(outPath); !os.IsNotExist(err) {
				t.Error("a refused retag left an output file behind")
			}
		})
	}
}

// An empty --title is what an unexpanded shell variable looks like, and a
// portable meeting must carry a title, so there is no reading of it that is
// safe to guess at.
func TestRetagRefusesAnEmptyTitle(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	outPath := filepath.Join(tmp, "after.opus")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"retag", inPath, "--out", outPath, "--title", "  "}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr.String())
	}
}

// A retag that names neither must not disturb them: the title mirror was added
// for --title, and it runs on every rewrite.
func TestRetagLeavesTheTitleAndRosterAloneWhenNotAsked(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before", "--title", "Weekly Sync")
	outPath := filepath.Join(tmp, "after.opus")

	before := decodePortableManifestFromOpus(t, inPath)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"retag", inPath, "--out", outPath, "--room-id", "rm_1111111111111111",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}

	after := decodePortableManifestFromOpus(t, outPath)
	if after.Meeting.Title != before.Meeting.Title {
		t.Errorf("meeting.title changed: %q -> %q", before.Meeting.Title, after.Meeting.Title)
	}
	if got := readOpusTag(t, outPath, "TITLE"); got != before.Meeting.Title {
		t.Errorf("TITLE tag = %q, want the untouched %q", got, before.Meeting.Title)
	}
	if len(after.Speakers) != len(before.Speakers) {
		t.Fatalf("speakers = %d, want %d", len(after.Speakers), len(before.Speakers))
	}
	for index, speaker := range after.Speakers {
		if speaker != before.Speakers[index] {
			t.Errorf("speaker %d changed: %+v -> %+v", index, before.Speakers[index], speaker)
		}
	}
}
