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

	// And every non-payload tag, including the v2 per-transcript chunk sets
	// that nothing in this command understands. ffmpeg runs with
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
	// Specifically: a v2 file's transcript payloads must still be there.
	transcriptChunks := 0
	for key := range afterTags {
		if strings.HasPrefix(strings.ToUpper(key), "CASSINI_TX_") {
			transcriptChunks++
		}
	}
	if transcriptChunks == 0 {
		t.Error("no CASSINI_TX_* tags survived the retag; a v2 file lost its transcript bodies")
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

// TestRetagStripsALegacyRoomName covers the field D-640 stopped writing: an
// already-published file still carries a frozen room name, and the tool that
// edits published files has to be able to remove one.
func TestRetagStripsALegacyRoomName(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")

	// Put a legacy name on it the way a pre-D-640 producer would have.
	legacyPath := filepath.Join(tmp, "legacy.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"retag", inPath, "--out", legacyPath, "--room-name", "Old Standup",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed legacy name failed code=%d stderr=%q", code, stderr.String())
	}
	if got := decodePortableManifestFromOpus(t, legacyPath).Meeting.RoomName; got != "Old Standup" {
		t.Fatalf("seeded meeting.roomName = %q, want %q", got, "Old Standup")
	}

	strippedPath := filepath.Join(tmp, "stripped.opus")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{
		"retag", legacyPath, "--out", strippedPath, "--clear-room-name",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("clear failed code=%d stderr=%q", code, stderr.String())
	}
	if got := decodePortableManifestFromOpus(t, strippedPath).Meeting.RoomName; got != "" {
		t.Errorf("meeting.roomName = %q after --clear-room-name, want empty", got)
	}
	if got := readOpusTag(t, strippedPath, "CASSINI_ROOM_NAME"); got != "" {
		t.Errorf("CASSINI_ROOM_NAME = %q after --clear-room-name, want absent", got)
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
