package cassini

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCorruptBundleManifest(t *testing.T, bundleDir string) {
	t.Helper()
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("mkdir bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "cassini.json"), []byte(`{"kind": "meeting",`), 0o644); err != nil {
		t.Fatalf("write corrupt manifest: %v", err)
	}
}

func TestPublishSkipsCorruptBundleManifests(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	exporter := filepath.Join(tmp, "fake-exporter.sh")
	if err := os.WriteFile(exporter, []byte(`#!/usr/bin/env bash
set -euo pipefail
SOURCE_DIR=""
OUTPUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-dir)
      SOURCE_DIR="$2"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
mkdir -p "$OUTPUT_DIR"
printf '<html></html>' >"$OUTPUT_DIR/index.html"
ls "$SOURCE_DIR" >"$OUTPUT_DIR/source-listing.txt"
`), 0o755); err != nil {
		t.Fatalf("write fake exporter: %v", err)
	}

	meetingsDir := filepath.Join(tmp, "meetings")
	if err := writeReadyMeetingBundleFixture(filepath.Join(meetingsDir, "good.meeting"), "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	// One corrupt meeting bundle and one corrupt run bundle (whose manifest is
	// parsed before the Kind filter) must not abort the publish of `good`.
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "broken.meeting"))
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "job.run"))

	t.Setenv("CASSINI_EXPORTER_RUNNER", exporter)
	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingsDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected publish success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	listing, err := os.ReadFile(filepath.Join(outDir, "source-listing.txt"))
	if err != nil {
		t.Fatalf("read staged source listing: %v", err)
	}
	// The ready `.meeting` bundle is packed into a single `good.opus` portable
	// meeting file; the `.meeting` directory itself is never staged.
	if !strings.Contains(string(listing), "good.opus") {
		t.Fatalf("expected good meeting packed to good.opus, got listing=%q", string(listing))
	}
	if strings.Contains(string(listing), "broken") || strings.Contains(string(listing), "job") {
		t.Fatalf("expected corrupt bundles excluded from staging, got listing=%q", string(listing))
	}
	for _, name := range []string{"broken.meeting", "job.run"} {
		if !strings.Contains(stdout.String(), "skipped "+filepath.Join(meetingsDir, name)) {
			t.Fatalf("expected skip line for %s in stdout, got %q", name, stdout.String())
		}
	}
}

func TestPublishFailsWhenOnlyBundleIsCorrupt(t *testing.T) {
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "broken.meeting"))

	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingsDir, "--out", outDir}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected publish failure, got stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no ready meeting bundles found") {
		t.Fatalf("expected no-ready-bundles error, got stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "broken.meeting") || !strings.Contains(stderr.String(), "parse meeting bundle manifest") {
		t.Fatalf("expected corrupt bundle skip detail with path, got stderr=%q", stderr.String())
	}
}

func TestStagePublishInputUsesBundleManifestTitle(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	bundleDir := filepath.Join(meetingsDir, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	// The operator stamps the Talk room name into cassini.json after
	// promotion; a publish that re-packs the .meeting (durable .opus not
	// written yet) must carry that title, not the ULID bundle name.
	setBundleManifestTitle(t, bundleDir, "Daily Meeting")

	stagingRoot, _, skipped, err := stagePublishInput(context.Background(), meetingsDir)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)
	if len(skipped) != 0 {
		t.Fatalf("unexpected skips: %v", skipped)
	}

	stagedOpus := filepath.Join(stagingRoot, "01KWEKPZVEJWP9BYBPBX9ZRNDQ.opus")
	if got := readOpusTitleTag(t, stagedOpus); got != "Daily Meeting" {
		t.Errorf("staged TITLE = %q, want manifest title %q", got, "Daily Meeting")
	}
}

func TestStagePublishInputSkipsCorruptBundles(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	if err := writeReadyMeetingBundleFixture(filepath.Join(meetingsDir, "good.meeting"), "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "broken.meeting"))
	writeCorruptBundleManifest(t, filepath.Join(meetingsDir, "job.run"))

	stagingRoot, sourceSummary, skipped, err := stagePublishInput(context.Background(), meetingsDir)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)

	if sourceSummary != meetingsDir {
		t.Fatalf("expected source summary %q, got %q", meetingsDir, sourceSummary)
	}
	// The ready `.meeting` bundle is packed into a single `good.opus` portable
	// meeting file rather than copied as a directory.
	if _, err := os.Stat(filepath.Join(stagingRoot, "good.opus")); err != nil {
		t.Fatalf("expected good bundle packed to good.opus: %v", err)
	}
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the good bundle staged, got %d entries", len(entries))
	}
	if len(skipped) != 2 {
		t.Fatalf("expected two skipped bundles, got %+v", skipped)
	}
	for _, item := range skipped {
		if !strings.Contains(item.Reason, "parse meeting bundle manifest") {
			t.Fatalf("expected parse-failure reason, got %+v", item)
		}
		if !strings.Contains(item.Reason, item.Path) {
			t.Fatalf("expected reason to carry the manifest path, got %+v", item)
		}
	}
}

func TestStagePublishInputPacksReadyBundleToValidOpus(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingsDir := filepath.Join(tmp, "meetings")
	if err := writeReadyMeetingBundleFixture(filepath.Join(meetingsDir, "good.meeting"), "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}

	stagingRoot, _, skipped, err := stagePublishInput(context.Background(), meetingsDir)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)
	if len(skipped) != 0 {
		t.Fatalf("expected no skipped bundles, got %+v", skipped)
	}

	opusPath := filepath.Join(stagingRoot, "good.opus")
	if err := verifyPortableMeetingInput(opusPath); err != nil {
		t.Fatalf("expected staged good.opus to be a valid portable meeting: %v", err)
	}
	// Only the packed `.opus` is staged; the `.meeting` directory is not.
	if _, err := os.Stat(filepath.Join(stagingRoot, "good")); !os.IsNotExist(err) {
		t.Fatalf("expected no staged .meeting directory, stat err = %v", err)
	}
}

func TestStagePublishInputPassesThroughOpusFile(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingDir := filepath.Join(tmp, "good.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	opusInput := filepath.Join(tmp, "daily-standup.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, opusInput, portablePackOptions{Title: "Daily Standup"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}

	stagingRoot, sourceSummary, skipped, err := stagePublishInput(context.Background(), opusInput)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)
	if len(skipped) != 0 {
		t.Fatalf("expected no skipped bundles, got %+v", skipped)
	}
	if sourceSummary != opusInput {
		t.Fatalf("expected source summary %q, got %q", opusInput, sourceSummary)
	}
	staged := filepath.Join(stagingRoot, "daily-standup.opus")
	if err := verifyPortableMeetingInput(staged); err != nil {
		t.Fatalf("expected staged .opus pass-through to be valid: %v", err)
	}
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one staged artifact, got %d", len(entries))
	}
}

// TestStagePublishInputPrefersOpusSiblingOverMeeting reproduces the operator's
// current/ layout once D-428 ships: a `.meeting` bundle next to a durable
// `.opus` of the same id (current/<id>.meeting + current/<id>.opus). Publish
// must stage the `.opus` and skip the `.meeting` rather than packing both to
// meetings/<id>.opus and erroring on a meeting-id collision.
func TestStagePublishInputPrefersOpusSiblingOverMeeting(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	current := filepath.Join(tmp, "current")
	meetingDir := filepath.Join(current, "job1.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	// The operator packs the durable sibling beside the bundle at promotion (D-428).
	if err := packMeetingBundle(context.Background(), meetingDir, filepath.Join(current, "job1.opus"), portablePackOptions{Title: "job1"}); err != nil {
		t.Fatalf("pack durable opus sibling: %v", err)
	}

	stagingRoot, _, skipped, err := stagePublishInput(context.Background(), current)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)

	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		t.Fatalf("read staging root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one staged artifact, got %d: %v", len(entries), entries)
	}
	if err := verifyPortableMeetingInput(filepath.Join(stagingRoot, "job1.opus")); err != nil {
		t.Fatalf("expected staged job1.opus to be valid: %v", err)
	}
	foundSkip := false
	for _, s := range skipped {
		if strings.Contains(s.Reason, "superseded by durable .opus sibling") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("expected the .meeting bundle to be skipped as superseded, got skipped=%+v", skipped)
	}
}

// TestStagePublishInputPacksMeetingWhenOpusSiblingInvalid guards against losing
// a meeting: if the durable `.opus` sibling is unverifiable, publish must fall
// back to packing the `.meeting` bundle instead of silently dropping both.
func TestStagePublishInputPacksMeetingWhenOpusSiblingInvalid(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	current := filepath.Join(tmp, "current")
	meetingDir := filepath.Join(current, "job2.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(current, "job2.opus"), []byte("not an opus file"), 0o644); err != nil {
		t.Fatalf("write bogus opus sibling: %v", err)
	}

	stagingRoot, _, _, err := stagePublishInput(context.Background(), current)
	if err != nil {
		t.Fatalf("stagePublishInput() error = %v", err)
	}
	defer os.RemoveAll(stagingRoot)

	if err := verifyPortableMeetingInput(filepath.Join(stagingRoot, "job2.opus")); err != nil {
		t.Fatalf("expected job2.opus to be packed from the .meeting fallback: %v", err)
	}
}

func TestStagePublishInputRejectsInvalidOpusFile(t *testing.T) {
	tmp := t.TempDir()
	bogus := filepath.Join(tmp, "bogus.opus")
	if err := os.WriteFile(bogus, []byte("not an opus file"), 0o644); err != nil {
		t.Fatalf("write bogus opus: %v", err)
	}

	stagingRoot, _, _, err := stagePublishInput(context.Background(), bogus)
	if err == nil {
		os.RemoveAll(stagingRoot)
		t.Fatalf("expected stagePublishInput to fail for an invalid .opus input")
	}
	if !strings.Contains(err.Error(), "no ready meeting bundles found") &&
		!strings.Contains(err.Error(), "invalid portable meeting") {
		t.Fatalf("expected invalid-portable-meeting error, got %v", err)
	}
}

// TestPublishPacksMeetingsForAudioPathCatalog runs the full publish pipeline
// with a fake exporter that mirrors the static-site exporter's catalog
// contract: a staged `.opus` file becomes a catalog entry with audioPath, a
// staged directory becomes an entry with artifactPath. The Go side packs ready
// bundles into `.opus`, so the resulting catalog must carry audioPath.
func TestPublishPacksMeetingsForAudioPathCatalog(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	exporter := filepath.Join(tmp, "fake-exporter.sh")
	if err := os.WriteFile(exporter, []byte(`#!/usr/bin/env bash
set -euo pipefail
SOURCE_DIR=""
OUTPUT_DIR=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-dir) SOURCE_DIR="$2"; shift 2 ;;
    --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$OUTPUT_DIR/meetings"
printf '<html></html>' >"$OUTPUT_DIR/index.html"
entries=""
for path in "$SOURCE_DIR"/*; do
  name="$(basename "$path")"
  if [[ -f "$path" && "$name" == *.opus ]]; then
    id="${name%.opus}"
    cp "$path" "$OUTPUT_DIR/meetings/$name"
    entry="{\"id\":\"$id\",\"title\":\"$id\",\"dateLabel\":\"$id\",\"audioPath\":\"./meetings/$name\"}"
  elif [[ -d "$path" ]]; then
    entry="{\"id\":\"$name\",\"title\":\"$name\",\"dateLabel\":\"$name\",\"artifactPath\":\"./meetings/$name\"}"
  else
    continue
  fi
  if [[ -n "$entries" ]]; then entries="$entries,"; fi
  entries="$entries$entry"
done
printf '{"version":"cassini.viewer.catalog.v1","meetings":[%s]}\n' "$entries" >"$OUTPUT_DIR/catalog.json"
`), 0o755); err != nil {
		t.Fatalf("write fake exporter: %v", err)
	}

	meetingsDir := filepath.Join(tmp, "meetings")
	if err := writeReadyMeetingBundleFixture(filepath.Join(meetingsDir, "good.meeting"), "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}

	t.Setenv("CASSINI_EXPORTER_RUNNER", exporter)
	outDir := filepath.Join(tmp, "site")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"publish", meetingsDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected publish success, got code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	catalogRaw, err := os.ReadFile(filepath.Join(outDir, "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	catalog := string(catalogRaw)
	if !strings.Contains(catalog, `"audioPath":"./meetings/good.opus"`) {
		t.Fatalf("expected catalog entry with audioPath -> good.opus, got %q", catalog)
	}
	if strings.Contains(catalog, "artifactPath") {
		t.Fatalf("expected no artifactPath for packed meeting, got %q", catalog)
	}
	if _, err := os.Stat(filepath.Join(outDir, "meetings", "good.opus")); err != nil {
		t.Fatalf("expected packed good.opus staged into site: %v", err)
	}
}

func TestUpdateRunBundleStatusKeepsManifestOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory is not enforced for root")
	}
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "job.run")
	bundle, err := PrepareRunBundle(runDir, false)
	if err != nil {
		t.Fatalf("PrepareRunBundle() error = %v", err)
	}
	before, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	// A read-only bundle directory makes the temp-file write fail before the
	// rename; the previous manifest must survive untouched.
	if err := os.Chmod(runDir, 0o555); err != nil {
		t.Fatalf("chmod run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o755) })

	if err := UpdateRunBundleStatus(bundle, bundleStateFailed, "record", "boom"); err == nil {
		t.Fatalf("expected manifest write failure in read-only dir")
	}
	after, err := os.ReadFile(bundle.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest after failed write: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("expected manifest unchanged after failed write, before=%q after=%q", string(before), string(after))
	}
}
