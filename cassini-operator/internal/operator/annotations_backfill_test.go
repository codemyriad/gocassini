package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeAnnotationArchive stands in for the delivered recordings: what each
// leaf's PROPFIND reports, and what `cassini annotate show` reads out of it.
type fakeAnnotationArchive struct {
	checksums map[string]string // OC-Checksum per leaf; "" = delivered before checksums
	stateErr  map[string]error
	results   map[string]annotateResult
	readErr   map[string]error
	reads     map[string]int
}

func newFakeAnnotationArchive() *fakeAnnotationArchive {
	return &fakeAnnotationArchive{
		checksums: map[string]string{}, stateErr: map[string]error{},
		results: map[string]annotateResult{}, readErr: map[string]error{}, reads: map[string]int{},
	}
}

// put delivers a recording whose bytes digest to container.
func (a *fakeAnnotationArchive) put(name, container string, result annotateResult) {
	result.ContainerSHA256 = container
	a.checksums[name] = container
	a.results[name] = result
}

func (a *fakeAnnotationArchive) delivered() searchDeliveredStateReader {
	return func(_ context.Context, name string) (string, bool, error) {
		if err := a.stateErr[name]; err != nil {
			return "", false, err
		}
		sum, ok := a.checksums[name]
		return sum, ok, nil
	}
}

func (a *fakeAnnotationArchive) reader() annotationArchiveReader {
	return func(_ context.Context, name string) (annotateResult, string, error) {
		a.reads[name]++
		if err := a.readErr[name]; err != nil {
			return annotateResult{}, "", err
		}
		result := a.results[name]
		return result, result.ContainerSHA256, nil
	}
}

func (a *fakeAnnotationArchive) rebuild(t *testing.T, store *annotationStore, names ...string) annotationBackfillReport {
	t.Helper()
	targets := make([]searchBackfillTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, searchBackfillTarget{JobID: strings.TrimSuffix(name, ".opus"), OpusName: name})
	}
	report, err := backfillAnnotationIndex(context.Background(), store, nil, targets, a.delivered(), a.reader())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	return report
}

func hiringFile(t *testing.T, namespace string) annotateResult {
	return annotatedFile(t, "", namespace, []testTag{{"tag_h", "hiring"}}, meetingMark("m", "tag_h"))
}

// A recording whose checksum matches the bytes it was indexed from costs one
// PROPFIND and no download — which is also what makes the rebuild resumable.
func TestAnnotationBackfillSkipsAnUnchangedRecordingWithoutDownloading(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))

	if report := archive.rebuild(t, store, "JOB1.opus"); report.Indexed != 1 {
		t.Fatalf("first run = %+v, want indexed=1", report)
	}
	report := archive.rebuild(t, store, "JOB1.opus")
	if report.Unchanged != 1 || report.Indexed != 0 {
		t.Fatalf("second run = %+v, want unchanged=1", report)
	}
	if archive.reads["JOB1.opus"] != 1 {
		t.Fatalf("downloads = %d, want 1: an unchanged recording must not be fetched again", archive.reads["JOB1.opus"])
	}
}

// A recording marked since it was indexed has a new checksum, and is read again.
func TestAnnotationBackfillRereadsAChangedRecording(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	archive.rebuild(t, store, "JOB1.opus")

	archive.put("JOB1.opus", "c2", annotatedFile(t, "", testTagNamespaceA, []testTag{{"tag_b", "budget"}}, meetingMark("m", "tag_b")))
	if report := archive.rebuild(t, store, "JOB1.opus"); report.Indexed != 1 {
		t.Fatalf("report = %+v, want indexed=1", report)
	}
	vocab, _ := store.Vocabulary(context.Background(), []string{"JOB1.opus"})
	if len(vocab) != 1 || vocab[0].Label != "budget" {
		t.Fatalf("vocabulary = %+v, want the recording's current tag", vocab)
	}
}

// A recording delivered before uploads carried checksums must be downloaded to
// learn its digest — but an unchanged one is still not rewritten.
func TestAnnotationBackfillWithoutAChecksumDownloadsButDoesNotRewrite(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	archive.checksums["JOB1.opus"] = ""
	archive.rebuild(t, store, "JOB1.opus")

	report := archive.rebuild(t, store, "JOB1.opus")
	if report.Unchanged != 1 || archive.reads["JOB1.opus"] != 2 {
		t.Fatalf("report = %+v reads=%d, want unchanged=1 after a second download", report, archive.reads["JOB1.opus"])
	}
}

// One meeting's failure never stops the run. A failed ASK keeps what was
// indexed; a file that was fetched and could not be read is a verdict.
func TestAnnotationBackfillRecordsFailuresAndCarriesOn(t *testing.T) {
	store := newTestAnnotationStore(t)
	ctx := context.Background()
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	archive.rebuild(t, store, "JOB1.opus")

	// JOB1 changed, and its download fails: rows from before are kept.
	archive.checksums["JOB1.opus"] = "c9"
	archive.readErr["JOB1.opus"] = errors.New("connection reset")
	// JOB2 was fetched and the CLI refused it.
	archive.put("JOB2.opus", "c2", annotateResult{})
	archive.readErr["JOB2.opus"] = &annotationsUnreadableError{err: errors.New("cassini annotate exited 4")}
	// JOB3's PROPFIND fails.
	archive.stateErr["JOB3.opus"] = errors.New("PROPFIND -> 503")
	// JOB4 is fine.
	archive.put("JOB4.opus", "c4", hiringFile(t, testTagNamespaceA))
	// JOB5 was never indexed, and its download fails.
	archive.put("JOB5.opus", "c5", annotateResult{})
	archive.readErr["JOB5.opus"] = errors.New("timeout")
	// JOB6 is named by the catalog but not held by the archive.

	report := archive.rebuild(t, store, "JOB1.opus", "JOB2.opus", "JOB3.opus", "JOB4.opus", "JOB5.opus", "JOB6.opus")
	if report.Indexed != 1 || report.Unavailable != 1 || report.Failed != 4 {
		t.Fatalf("report = %+v, want indexed=1 unavailable=1 failed=4", report)
	}
	if got, ok, _ := store.ResolveLabel(ctx, "hiring", allRecorded(t, store)); !ok || got != "tag_h" {
		t.Errorf("a failed download dropped JOB1's marks")
	}
	if row := readAnnotationRow(t, store, "JOB1.opus"); row.state != annotationsStateIndexed || row.marks != 1 {
		t.Errorf("JOB1 = %+v, want its earlier rows kept", row)
	}
	if row := readAnnotationRow(t, store, "JOB2.opus"); row.state != annotationsStateUnavailable || row.reason != annotationsReasonUnreadable {
		t.Errorf("JOB2 = %+v, want unavailable with a reason", row)
	}
	if row := readAnnotationRow(t, store, "JOB5.opus"); row.state != annotationsStateUnavailable || row.reason != annotationsBackfillReasonArchiveUnread {
		t.Errorf("JOB5 = %+v, want the failure recorded against it", row)
	}
	coverage, _ := store.Coverage(ctx, []string{"JOB1.opus", "JOB2.opus", "JOB3.opus", "JOB4.opus", "JOB5.opus", "JOB6.opus"})
	if coverage != (annotationCoverage{Visible: 6, Indexed: 2}) {
		t.Errorf("coverage = %+v, want visible=6 indexed=2", coverage)
	}
}

// A recording in a format this build cannot read is recorded unavailable — and
// with its bytes, so the next run does not download it again.
func TestAnnotationBackfillCountsAnUnknownFormatAsUnreadable(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", annotateResult{Annotations: json.RawMessage(`{"format":"cassini.annotations.v9"}`)})
	if report := archive.rebuild(t, store, "JOB1.opus"); report.Unavailable != 1 {
		t.Fatalf("report = %+v, want unavailable=1", report)
	}
	if report := archive.rebuild(t, store, "JOB1.opus"); report.Unchanged != 1 || archive.reads["JOB1.opus"] != 1 {
		t.Fatalf("report = %+v reads=%d, want it skipped by checksum", report, archive.reads["JOB1.opus"])
	}
}

// On a fresh volume the rebuild adopts the namespace most of the archive
// carries, so the next mark joins the existing tags.
func TestAnnotationBackfillAdoptsTheArchivesNamespace(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	archive.put("JOB2.opus", "c2", hiringFile(t, testTagNamespaceA))
	archive.put("JOB3.opus", "c3", hiringFile(t, testTagNamespaceB))

	report := archive.rebuild(t, store, "JOB1.opus", "JOB2.opus", "JOB3.opus")
	if !report.Namespace.Adopted || report.Namespace.Namespace != testTagNamespaceA {
		t.Fatalf("namespace = %+v, want %s adopted", report.Namespace, testTagNamespaceA)
	}
	if got, err := store.Namespace(context.Background()); err != nil || got != testTagNamespaceA {
		t.Fatalf("Namespace() = %q (%v), want the adopted one, not a new mint", got, err)
	}
}

// A rebuild never mints: an archive with no marks leaves that to the first
// write, when a namespace is actually needed.
func TestAnnotationBackfillNeverMints(t *testing.T) {
	store := newTestAnnotationStore(t)
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", annotateResult{})
	report := archive.rebuild(t, store, "JOB1.opus")
	if report.Namespace != (namespaceAdoption{}) {
		t.Fatalf("namespace = %+v, want nothing adopted or minted", report.Namespace)
	}
	if stored, _ := store.storedNamespace(context.Background()); stored != "" {
		t.Fatalf("stored = %q, want none", stored)
	}
}

// A namespace already stored is never overwritten by a rebuild; a different
// one in the archive is reported for a person to look at.
func TestAnnotationBackfillReportsANamespaceConflict(t *testing.T) {
	store := newTestAnnotationStore(t)
	minted, err := store.Namespace(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	archive := newFakeAnnotationArchive()
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	report := archive.rebuild(t, store, "JOB1.opus")
	if report.Namespace.Adopted || report.Namespace.Namespace != minted || report.Namespace.Archive != testTagNamespaceA {
		t.Fatalf("namespace = %+v, want %s kept and %s reported", report.Namespace, minted, testTagNamespaceA)
	}
}

// Meetings the archive no longer names are dropped — but never on an empty
// archive read, which is indistinguishable from an outage.
func TestAnnotationBackfillForgetsVanishedMeetings(t *testing.T) {
	store := newTestAnnotationStore(t)
	recordMarks(t, store, "GONE.opus", hiringFile(t, testTagNamespaceA))
	archive := newFakeAnnotationArchive()

	if report := archive.rebuild(t, store); report.Forgotten != 0 {
		t.Fatalf("an empty target list forgot %d meetings", report.Forgotten)
	}
	archive.put("JOB1.opus", "c1", hiringFile(t, testTagNamespaceA))
	if report := archive.rebuild(t, store, "JOB1.opus"); report.Forgotten != 1 {
		t.Fatalf("report = %+v, want forgotten=1", report)
	}
	if recorded, _ := store.recordedState(context.Background()); len(recorded) != 1 {
		t.Fatalf("recorded = %v, want only JOB1", recorded)
	}
}

func TestAnnotationBackfillRefusesWithoutItsInputs(t *testing.T) {
	archive := newFakeAnnotationArchive()
	ctx := context.Background()
	if _, err := backfillAnnotationIndex(ctx, nil, nil, nil, archive.delivered(), archive.reader()); err == nil {
		t.Error("no store: want an error")
	}
	store := newTestAnnotationStore(t)
	if _, err := backfillAnnotationIndex(ctx, store, nil, nil, archive.delivered(), nil); err == nil {
		t.Error("no archive: want an error — current/ is not a substitute")
	}
}

func TestBackfillAnnotationsCommandRefusesOutsideAnExApp(t *testing.T) {
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("APP_SECRET", "")
	t.Setenv("EX_APP_ID", "")
	var stdout, stderr bytes.Buffer
	if code := runBackfillAnnotations(context.Background(), nil, &stdout, &stderr); code != backfillAnnotationsExitNotStarted {
		t.Fatalf("exit = %d, want %d", code, backfillAnnotationsExitNotStarted)
	}
	if !strings.Contains(stderr.String(), "nothing was read") {
		t.Errorf("stderr should say nothing was read: %q", stderr.String())
	}
}

func TestBackfillAnnotationsCommandRejectsSurplusArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBackfillAnnotations(context.Background(), []string{"extra"}, &stdout, &stderr); code != backfillAnnotationsExitUsage {
		t.Fatalf("exit = %d, want %d", code, backfillAnnotationsExitUsage)
	}
}

func TestBackfillAnnotationsCommandIsKnownToRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{backfillAnnotationsCommand, "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Rebuild the tag index") {
		t.Errorf("help did not describe the command: %q", stderr.String())
	}
}
