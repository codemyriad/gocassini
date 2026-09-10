package operator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D-737: an archive copy that has been marked since delivery differs from the
// local sealed file in its container and in nothing else. Backfill must tell
// that apart from the rerun that never published — by the audio digest.

// showAudioCLI answers `cassini annotate show` with the file's first word as its
// audio digest, so "<audio> +marks" is a marked copy of the same audio.
func showAudioCLI(t *testing.T) string {
	t.Helper()
	return fakeCassini(t, `read -r audio _ < "$3"
printf '{"format":"cassini.annotate.result.v1","annotations":null,"resolved":true,"audioOpusSha256":"%s","containerSha256":"x"}' "$audio"`)
}

// stubArchiveCopy reads back an archive copy holding content.
func stubArchiveCopy(t *testing.T, content string) (searchArchiveReader, *int) {
	t.Helper()
	copyPath := filepath.Join(t.TempDir(), "archive.opus")
	if err := os.WriteFile(copyPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	return func(context.Context, string) (searchArchiveCopy, func(), error) {
		calls++
		return searchArchiveCopy{Words: archiveWords, Digest: digestOf(content), Path: copyPath}, func() {}, nil
	}, &calls
}

func rowSourceAndDigest(t *testing.T, f *backfillFixture, opusName string) (string, string) {
	t.Helper()
	var source, digest string
	if err := f.rt.searchStore.db.QueryRow(
		`SELECT row_source, opus_sha256 FROM meeting_index WHERE opus_name = ?`, opusName).Scan(&source, &digest); err != nil {
		t.Fatalf("read meeting_index: %v", err)
	}
	return source, digest
}

func TestBackfillAcceptsAMarkedArchiveCopyOfTheSameAudio(t *testing.T) {
	f := newBackfillFixture(t)
	f.writeCurrent(t, "JOB1", "sealed-audio-bytes", ingestTranscript)
	f.rt.cfg.CassiniBin = showAudioCLI(t)
	// Marked since delivery: the bytes, and the checksum the write stamped, are
	// no longer the sealed file's. The audio is.
	const marked = "sealed-audio-bytes +marks"
	archive, calls := stubArchiveCopy(t, marked)
	targets := []searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}

	report, err := f.rt.backfillSearchIndex(context.Background(), targets, deliveredState(marked), archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.Indexed != 1 {
		t.Fatalf("report = %+v, want indexed=1", report)
	}
	// The producer's segments, not coarse rows: the bundle was verified.
	hits := matches(t, f.rt.searchStore, "acquisition")
	if len(hits) != 1 || hits[0].SegmentID != "seg_0001" {
		t.Fatalf("hits = %+v, want the bundle's segment", hits)
	}
	// Recorded against the delivered container, so the next run's one PROPFIND
	// can say "unchanged".
	if source, digest := rowSourceAndDigest(t, f, "JOB1.opus"); source != searchRowSourceSegments || digest != digestOf(marked) {
		t.Fatalf("row_source=%q digest=%q, want segments at the delivered container's digest", source, digest)
	}

	report, err = f.rt.backfillSearchIndex(context.Background(), targets, deliveredState(marked), archive)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if report.Unchanged != 1 || *calls != 1 {
		t.Fatalf("re-run report = %+v after %d archive reads, want unchanged with no second download", report, *calls)
	}
}

// Different audio is the divergence the check was built for, marks or no marks.
func TestBackfillStillRefusesABundleWhoseAudioDiffers(t *testing.T) {
	f := newBackfillFixture(t)
	f.writeCurrent(t, "JOB1", "rebuilt-audio-never-published", `{"version":"transcript.words.v1","segments":[
	  {"id":"seg_9999","speaker":"S1","startMs":1000,"endMs":2000,"text":"undelivered rewording","words":[]}]}`)
	f.rt.cfg.CassiniBin = showAudioCLI(t)
	archive, _ := stubArchiveCopy(t, "delivered +marks")

	report, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, deliveredState("delivered +marks"), archive)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// The CLI was asked — so the refusal is a verdict on the audio, not a
	// comparison that never happened.
	if args, _ := os.ReadFile(f.rt.cfg.CassiniBin + ".args"); !strings.Contains(string(args), canonicalOpusPath(f.workRoot, "JOB1")) {
		t.Fatalf("the local recording's audio digest was never read: %q", args)
	}
	if report.Indexed != 1 {
		t.Fatalf("report = %+v, want the archive to index it", report)
	}
	if got := matches(t, f.rt.searchStore, "undelivered"); len(got) != 0 {
		t.Errorf("indexed a transcript of audio that was never delivered: %+v", got)
	}
	if source, _ := rowSourceAndDigest(t, f, "JOB1.opus"); source != searchRowSourceWords {
		t.Errorf("row_source = %q, want the archive's words", source)
	}
}

// Unknown is not a match: with no CLI to read the audio digests, the bundle
// stays unverified and the archive's words are used, as before marks existed.
func TestBackfillDoesNotTrustAnAudioDigestItCannotRead(t *testing.T) {
	f := newBackfillFixture(t)
	f.writeCurrent(t, "JOB1", "sealed-audio-bytes", ingestTranscript)
	const marked = "sealed-audio-bytes +marks"
	archive, _ := stubArchiveCopy(t, marked)

	if _, err := f.rt.backfillSearchIndex(context.Background(),
		[]searchBackfillTarget{{JobID: "JOB1", OpusName: "JOB1.opus"}}, deliveredState(marked), archive); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if source, _ := rowSourceAndDigest(t, f, "JOB1.opus"); source != searchRowSourceWords {
		t.Errorf("row_source = %q, want words: an unreadable audio digest verifies nothing", source)
	}
}
