package operator

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The join key comes from each entry's audioPath, not from the catalog id —
// they coincide by convention only, and a key that drifts matches nothing in
// the visibility scan and fails silently.
func TestParseBackfillTargetsTakesTheJoinKeyFromAudioPath(t *testing.T) {
	targets, err := parseBackfillTargets([]byte(`{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"MEETING-A","jobId":"JOB-A","audioPath":"./meetings/JOB-A--attempt-002.opus"}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want 1", targets)
	}
	if targets[0].OpusName != "JOB-A--attempt-002.opus" {
		t.Errorf("join key = %q, want the audioPath basename", targets[0].OpusName)
	}
	if targets[0].JobID != "JOB-A" {
		t.Errorf("job = %q, want the explicit jobId", targets[0].JobID)
	}
}

// jobId is only carried since D-640, so a meeting published before it falls
// back to the catalog id rather than being dropped.
func TestParseBackfillTargetsFallsBackToTheCatalogID(t *testing.T) {
	targets, err := parseBackfillTargets([]byte(`{"meetings":[
	  {"id":"JOB-OLD","audioPath":"./meetings/JOB-OLD.opus"}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(targets) != 1 || targets[0].JobID != "JOB-OLD" {
		t.Fatalf("targets = %+v, want the id used as the job", targets)
	}
}

// A directory-shaped legacy entry has no basename any visibility scan can
// return, so indexing it would make it permanently unreachable.
func TestParseBackfillTargetsSkipsEntriesWithNoAudioPath(t *testing.T) {
	targets, err := parseBackfillTargets([]byte(`{"meetings":[
	  {"id":"A","artifactPath":"./meetings/A"},
	  {"id":"B","audioPath":"./meetings/B.opus"}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(targets) != 1 || targets[0].OpusName != "B.opus" {
		t.Fatalf("targets = %+v, want only the keyable entry", targets)
	}
}

func TestParseBackfillTargetsRejectsAMalformedCatalog(t *testing.T) {
	if _, err := parseBackfillTargets([]byte(`{not json`)); err == nil {
		t.Fatal("expected an error for a malformed catalog")
	}
}

// Outside an AppAPI deployment there is no archive catalog to read, so there is
// nothing to backfill FROM — a different thing from an empty archive, and it
// must not be reported as a completed run.
func TestBackfillSearchCommandRefusesOutsideAnExApp(t *testing.T) {
	t.Setenv("NEXTCLOUD_URL", "")
	t.Setenv("APP_SECRET", "")
	t.Setenv("EX_APP_ID", "")

	var stdout, stderr bytes.Buffer
	code := runBackfillSearch(context.Background(), nil, &stdout, &stderr)

	if code != backfillSearchExitNotStarted {
		t.Fatalf("exit = %d, want %d (nothing started)", code, backfillSearchExitNotStarted)
	}
	if !strings.Contains(stderr.String(), "nothing was read") {
		t.Errorf("stderr should say nothing was read: %q", stderr.String())
	}
}

func TestBackfillSearchCommandRejectsSurplusArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runBackfillSearch(context.Background(), []string{"extra"}, &stdout, &stderr); code != backfillSearchExitUsage {
		t.Fatalf("exit = %d, want %d", code, backfillSearchExitUsage)
	}
}

// The exit codes separate "nothing happened" from "the index is now partly
// rebuilt", because the index is disposable and those call for different next
// steps.
func TestBackfillSearchExitCodesAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, code := range map[string]int{
		"ok":          backfillSearchExitOK,
		"usage":       backfillSearchExitUsage,
		"not-started": backfillSearchExitNotStarted,
		"failed":      backfillSearchExitFailed,
	} {
		if other, clash := seen[code]; clash {
			t.Fatalf("%s and %s share exit code %d", name, other, code)
		}
		seen[code] = name
	}
}
