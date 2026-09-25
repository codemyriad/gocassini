package operator

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

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
