package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// setBundleManifestTitle stamps a title into a fixture bundle's cassini.json,
// mirroring what the operator does after promotion (SetMeetingBundleTitle).
func setBundleManifestTitle(t *testing.T, bundleDir, title string) {
	t.Helper()
	setBundleManifestFields(t, bundleDir, map[string]any{"title": title})
}

// setBundleManifestFields merges fields into a fixture bundle's cassini.json.
func setBundleManifestFields(t *testing.T, bundleDir string, fields map[string]any) {
	t.Helper()
	manifestPath := filepath.Join(bundleDir, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse fixture manifest: %v", err)
	}
	for key, value := range fields {
		manifest[key] = value
	}
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode fixture manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, updated, 0o644); err != nil {
		t.Fatalf("write fixture manifest: %v", err)
	}
}

// readOpusTitleTag returns the TITLE tag of a packed .opus file. Ogg tags can
// surface on the format or the stream depending on the muxer, so both are
// queried.
func readOpusTitleTag(t *testing.T, path string) string {
	t.Helper()
	output, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format_tags=title:stream_tags=title",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe title tag: %v: %s", err, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func TestPackPrefersManifestTitleOverFileName(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	setBundleManifestTitle(t, bundleDir, "Daily Meeting")

	outPath := filepath.Join(tmp, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got := readOpusTitleTag(t, outPath); got != "Daily Meeting" {
		t.Errorf("packed TITLE = %q, want manifest title %q", got, "Daily Meeting")
	}
	manifest := decodePortableManifestFromOpus(t, outPath)
	if manifest.Version != portable.WireVersion || manifest.Integrity.MatchPolicy != portable.AudioMatchPolicy {
		t.Fatalf("packed integrity contract = v%d/%q, want v%d/%q", manifest.Version, manifest.Integrity.MatchPolicy, portable.WireVersion, portable.AudioMatchPolicy)
	}
	if len(manifest.Integrity.OpusSHA256) != 64 {
		t.Fatalf("packed integrity = %+v, want compressed Opus digest", manifest.Integrity)
	}
	if got := readOpusTag(t, outPath, "CASSINI_FORMAT"); got != portable.Format {
		t.Errorf("CASSINI_FORMAT = %q, want %q", got, portable.Format)
	}
	if got := readOpusTag(t, outPath, "CASSINI_AUDIO_OPUS_SHA256"); got != manifest.Integrity.OpusSHA256 {
		t.Errorf("CASSINI_AUDIO_OPUS_SHA256 = %q, manifest says %q", got, manifest.Integrity.OpusSHA256)
	}
}

// TestPackEmitsThePublishedWireVersionAndSchema pins what a packed file tells
// the outside world it is: format tag org.cassini.portable-meeting/1, manifest
// version 1, and the schema URL that resolves.
func TestPackEmitsThePublishedWireVersionAndSchema(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	outPath := filepath.Join(tmp, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got, want := readOpusTag(t, outPath, "CASSINI_FORMAT"), "org.cassini.portable-meeting/1"; got != want {
		t.Errorf("CASSINI_FORMAT = %q, want %q", got, want)
	}
	wantSchema := "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json"
	if got := readOpusTag(t, outPath, "CASSINI_PAYLOAD_SCHEMA"); got != wantSchema {
		t.Errorf("CASSINI_PAYLOAD_SCHEMA = %q, want %q", got, wantSchema)
	}
	if got := readOpusTag(t, outPath, "CASSINI_PAYLOAD_SCHEMA"); strings.Contains(got, "cassini.local") {
		t.Errorf("CASSINI_PAYLOAD_SCHEMA = %q, still points at the placeholder host", got)
	}

	manifest := decodePortableManifestFromOpus(t, outPath)
	if manifest.Version != 1 {
		t.Errorf("manifest version = %d, want 1", manifest.Version)
	}
	if len(manifest.Transcripts) == 0 {
		t.Errorf("packed manifest carries no transcripts index: %+v", manifest)
	}
	if got, want := manifest.Integrity.MatchPolicy, portable.AudioMatchPolicy; got != want {
		t.Errorf("integrity.matchPolicy = %q, want %q", got, want)
	}
}

func TestPackStabilizesMixedWebMOpusIdentity(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "mixed.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Mirror MixDownToWebM's multi-track path. FFmpeg 9.0.1 writes a final
	// granule 24 samples lower on the first Ogg -> Ogg metadata remux for this
	// exact one-second shape. Compressed packets remain identical, but the
	// integrity contract also binds playable end trim, so packing must rebuild its
	// manifest rather than publishing the first, now-stale digest.
	output, err := exec.Command(
		"ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=410:sample_rate=48000:duration=1.00",
		"-f", "lavfi", "-i", "sine=frequency=520:sample_rate=48000:duration=1.00",
		"-filter_complex", "[0:a][1:a]amix=inputs=2:duration=longest:normalize=0,alimiter=limit=0.95[out]",
		"-map", "[out]",
		"-ac", "1", "-ar", "48000",
		"-c:a", "libopus", "-b:a", "64k", "-vbr", "on",
		"-compression_level", "10", "-application", "voip",
		filepath.Join(bundleDir, "meeting.webm"),
	).CombinedOutput()
	if err != nil {
		t.Fatalf("write mixed WebM fixture: %v: %s", err, strings.TrimSpace(string(output)))
	}

	outPath := filepath.Join(tmp, "mixed.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack mixed WebM failed code=%d stderr=%q", code, stderr.String())
	}

	manifest := decodePortableManifestFromOpus(t, outPath)
	actual, err := computePortableAudioIntegrity(outPath)
	if err != nil {
		t.Fatalf("hash packed mixed WebM: %v", err)
	}
	if actual.OpusSHA256 != manifest.Integrity.OpusSHA256 ||
		actual.SampleCount != manifest.Integrity.SampleCount ||
		actual.DurationMS != manifest.Integrity.DurationMS {
		t.Fatalf("packed integrity = %+v, manifest integrity = %+v", actual, manifest.Integrity)
	}
	if got := readOpusTag(t, outPath, "CASSINI_AUDIO_OPUS_SHA256"); got != actual.OpusSHA256 {
		t.Fatalf("CASSINI_AUDIO_OPUS_SHA256 = %q, actual %q", got, actual.OpusSHA256)
	}
}

func TestPackTitleFlagOverridesManifestTitle(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-a.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	setBundleManifestTitle(t, bundleDir, "Manifest Title")

	outPath := filepath.Join(tmp, "meeting-a.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath, "--title", "Flag Title"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got := readOpusTitleTag(t, outPath); got != "Flag Title" {
		t.Errorf("packed TITLE = %q, want flag title %q", got, "Flag Title")
	}
}

// readOpusTag returns one named tag of a packed .opus, or "" when the file
// does not carry it. Unlike readOpusTitleTag this must distinguish absent from
// empty, so it asks for the key as well as the value.
func readOpusTag(t *testing.T, path, tag string) string {
	t.Helper()
	output, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format_tags:stream_tags",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		t.Fatalf("ffprobe tags: %v", err)
	}
	var probed struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &probed); err != nil {
		t.Fatalf("parse ffprobe tags: %v", err)
	}
	// Ogg puts comments on the stream; other muxers on the format. Look in both
	// rather than assuming which one ffmpeg picked for this build.
	maps := []map[string]string{probed.Format.Tags}
	for _, stream := range probed.Streams {
		maps = append(maps, stream.Tags)
	}
	for _, tags := range maps {
		for key, value := range tags {
			if strings.EqualFold(key, tag) {
				return value
			}
		}
	}
	return ""
}

// decodePortableManifestFromOpus reads the gzipped payload rather than the
// plain tags. The tags are a convenience mirror; the payload is the record, and
// it is what the exporter reads to build a catalog entry — so a field that is
// only in the tags is a field that does not reach the catalog.
func decodePortableManifestFromOpus(t *testing.T, path string) portable.Manifest {
	t.Helper()
	manifest, _, err := readPortableMeetingManifest(path)
	if err != nil {
		t.Fatalf("read portable manifest from %s: %v", path, err)
	}
	return manifest
}

func TestPackRoomFlagsReachOpusTags(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-room.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	outPath := filepath.Join(tmp, "meeting-room.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", outPath,
		"--room-token", "a7bc3k9x", "--room-name", "Weekly Sync",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	// The published id is a DERIVATION of the token, never the token: for a
	// public conversation the token is also the link that joins it.
	want := portable.RoomIDFromToken("", "a7bc3k9x")
	got := readOpusTag(t, outPath, "CASSINI_ROOM_ID")
	if got != want {
		t.Errorf("CASSINI_ROOM_ID = %q, want the derived %q", got, want)
	}
	if strings.Contains(got, "a7bc3k9x") {
		t.Errorf("CASSINI_ROOM_ID = %q leaks the room token", got)
	}
	// The room NAME is an input, not a stored field (D-640). It still becomes
	// the title — the record-time label a player shows — but the room's current
	// name belongs in the catalog, where a rename does not mean rewriting a
	// sealed file.
	if got := readOpusTag(t, outPath, "CASSINI_ROOM_NAME"); got != "" {
		t.Errorf("CASSINI_ROOM_NAME = %q, want absent: the room name is no longer stored in the artifact", got)
	}
	if got := readOpusTag(t, outPath, "TITLE"); got != "Weekly Sync" {
		t.Errorf("TITLE = %q, want the room name %q", got, "Weekly Sync")
	}
}

// TestPackNeverWritesTheRoomTokenAnywhere is the invariant the whole room-id
// design rests on, asserted rather than assumed: a recording may be shared with
// someone who must not be able to join the conversation it came from, and for a
// public conversation the token IS the join link.
//
// It reads every tag rather than the ones we expect to be interesting, because
// the failure this guards against is a token appearing in a tag nobody thought
// about — including the gzipped payload, which is checked by decoding it.
func TestPackNeverWritesTheRoomTokenAnywhere(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-secret.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Distinctive enough that a substring match cannot be a coincidence, and
	// shaped like a real spreed token.
	const token = "zzq4tokenzz"
	outPath := filepath.Join(tmp, "meeting-secret.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", outPath,
		"--room-token", token, "--room-name", "Secret Room",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	// The whole container, not just the tags: if the token reaches the file by
	// any route at all — a tag, the payload, a stray comment — it is in these
	// bytes.
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read packed file: %v", err)
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Fatalf("the packed .opus contains the raw room token %q", token)
	}
	// And decoded, because the payload is gzipped: a token inside it would not
	// show up in the raw scan above.
	manifest := decodePortableManifestFromOpus(t, outPath)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("re-encode manifest: %v", err)
	}
	if bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("the decoded manifest contains the raw room token %q: %s", token, encoded)
	}
	if manifest.Meeting.RoomID != portable.RoomIDFromToken("", token) {
		t.Errorf("meeting.roomId = %q, want the derivation of the token", manifest.Meeting.RoomID)
	}
}

func TestPackProvenanceFlagsReachTheArtifact(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-prov.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	outPath := filepath.Join(tmp, "meeting-prov.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", outPath,
		"--job-id", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "--attempt-number", "3",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got := readOpusTag(t, outPath, "CASSINI_JOB_ID"); got != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" {
		t.Errorf("CASSINI_JOB_ID = %q, want the flag's value", got)
	}
	if got := readOpusTag(t, outPath, "CASSINI_ATTEMPT_NUMBER"); got != "3" {
		t.Errorf("CASSINI_ATTEMPT_NUMBER = %q, want %q", got, "3")
	}
	manifest := decodePortableManifestFromOpus(t, outPath)
	if manifest.Meeting.JobID != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" {
		t.Errorf("meeting.jobId = %q, want the flag's value", manifest.Meeting.JobID)
	}
	if manifest.Meeting.AttemptNumber != 3 {
		t.Errorf("meeting.attemptNumber = %d, want 3", manifest.Meeting.AttemptNumber)
	}
}

func TestPackReadsProvenanceFromBundleManifest(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-prov-stamped.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	setBundleManifestFields(t, bundleDir, map[string]any{
		"job_id":         "01STAMPEDJOBID0000000000AB",
		"attempt_number": 2,
	})

	outPath := filepath.Join(tmp, "meeting-prov-stamped.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got := readOpusTag(t, outPath, "CASSINI_JOB_ID"); got != "01STAMPEDJOBID0000000000AB" {
		t.Errorf("CASSINI_JOB_ID = %q, want the bundle manifest's value", got)
	}
	if got := readOpusTag(t, outPath, "CASSINI_ATTEMPT_NUMBER"); got != "2" {
		t.Errorf("CASSINI_ATTEMPT_NUMBER = %q, want %q", got, "2")
	}
}

func TestPackWithoutProvenanceEmitsNoProvenanceTags(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-noprov.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	outPath := filepath.Join(tmp, "meeting-noprov.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	for _, tag := range []string{"CASSINI_JOB_ID", "CASSINI_ATTEMPT_NUMBER"} {
		if got := readOpusTag(t, outPath, tag); got != "" {
			t.Errorf("%s = %q on a meeting packed outside the operator, want absent", tag, got)
		}
	}
}

func TestPackRejectsANegativeAttemptNumber(t *testing.T) {
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-badattempt.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", filepath.Join(tmp, "out.opus"), "--attempt-number", "-1",
	}, &stdout, &stderr)
	// Exit 2, not a silent clamp to "unknown": a negative attempt can only come
	// from a caller that computed it wrongly, and hiding that hides the bug.
	if code != 2 {
		t.Fatalf("pack exit = %d, want 2 for a negative --attempt-number (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "attempt-number") {
		t.Errorf("stderr = %q, want it to name the offending flag", stderr.String())
	}
}

func TestPackWithoutRoomEmitsNoRoomTags(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-noroom.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	outPath := filepath.Join(tmp, "meeting-noroom.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	// Absent, not empty. An empty CASSINI_ROOM_ID would read as "this meeting
	// has a room whose id is the empty string", and every consumer would then
	// have to check presence AND emptiness.
	for _, tag := range []string{"CASSINI_ROOM_ID", "CASSINI_ROOM_NAME"} {
		if got := readOpusTag(t, outPath, tag); got != "" {
			t.Errorf("%s = %q on a meeting with no room, want absent", tag, got)
		}
	}
}

func TestPackReadsRoomFromBundleManifest(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-stamped.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// What the operator stamps after a build (SetMeetingBundleRoom).
	setBundleManifestFields(t, bundleDir, map[string]any{
		"room_token": "stamped-token",
		"room_name":  "Stamped Room",
	})

	outPath := filepath.Join(tmp, "meeting-stamped.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got, want := readOpusTag(t, outPath, "CASSINI_ROOM_ID"), portable.RoomIDFromToken("", "stamped-token"); got != want {
		t.Errorf("CASSINI_ROOM_ID = %q, want the id derived from the bundle manifest's token, %q", got, want)
	}
	if got := readOpusTag(t, outPath, "TITLE"); got != "Stamped Room" {
		t.Errorf("TITLE = %q, want the bundle manifest's room name %q", got, "Stamped Room")
	}
}

func TestPackRoomFlagsOverrideBundleManifest(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "meeting-override.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	setBundleManifestFields(t, bundleDir, map[string]any{
		"room_token": "manifest-token",
		"room_name":  "Manifest Room",
	})

	outPath := filepath.Join(tmp, "meeting-override.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", outPath,
		"--room-token", "flag-token", "--room-name", "Flag Room",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	if got, want := readOpusTag(t, outPath, "CASSINI_ROOM_ID"), portable.RoomIDFromToken("", "flag-token"); got != want {
		t.Errorf("CASSINI_ROOM_ID = %q, want the id derived from the flag's token, %q", got, want)
	}
	if got := readOpusTag(t, outPath, "TITLE"); got != "Flag Room" {
		t.Errorf("TITLE = %q, want the flag's room name %q", got, "Flag Room")
	}
}

func TestPackHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cassini pack ") {
		t.Fatalf("expected pack usage in stderr, got %q", stderr.String())
	}
}

func TestPackListedInRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("root usage exit %d", code)
	}
	if !strings.Contains(stdout.String(), "pack") {
		t.Fatalf("expected pack listed in root usage, got %q", stdout.String())
	}
}

func TestPackRequiresOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", "./some.meeting"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--out is required") {
		t.Fatalf("expected --out required error, got %q", stderr.String())
	}
}

func TestPackRejectsNonOpusOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", "./some.meeting", "--out", "./out.meeting"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit code 2, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be a .opus file") {
		t.Fatalf("expected non-opus output error, got %q", stderr.String())
	}
}

func TestPackRejectsNonMeetingInput(t *testing.T) {
	tmp := t.TempDir()
	notMeeting := filepath.Join(tmp, "not-a-meeting")
	if err := os.MkdirAll(notMeeting, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", notMeeting, "--out", filepath.Join(tmp, "out.opus")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a meeting bundle") {
		t.Fatalf("expected non-meeting input error, got %q", stderr.String())
	}
}

func TestPackRejectsPartialMeetingBundle(t *testing.T) {
	tmp := t.TempDir()
	meeting := filepath.Join(tmp, "demo.meeting")
	if err := os.MkdirAll(meeting, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A meeting manifest that is present but not in the ready state must be
	// refused so we never pack a half-built bundle into a "durable" .opus.
	manifest := `{"kind":"meeting","version":"cassini.meeting.v1","state":"preparing","stage":"build"}`
	if err := os.WriteFile(filepath.Join(meeting, "cassini.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"pack", meeting, "--out", filepath.Join(tmp, "out.opus")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not ready") {
		t.Fatalf("expected not-ready error, got %q", stderr.String())
	}
}
