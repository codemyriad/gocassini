package operator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchCoverageRemediesOnlyBackfillIndexAndBundleGaps(t *testing.T) {
	missingWords := searchCoverage{
		ModelUnavailable:     1,
		TranscriptionFailed:  1,
		MissingTranscript:    1,
		UnreadableTranscript: 1,
		UnknownEmpty:         1,
	}
	// Backfill cannot create words transcription never made, so a shortfall that
	// is entirely missing words gets described, never offered a re-index.
	for _, step := range searchCoverageSteps(missingWords, true) {
		if strings.Contains(step.Label, "Re-index") {
			t.Fatalf("backfill cannot create missing words: %+v", step)
		}
	}

	// No step anywhere carries a command any more: the panel is ADMIN-only and
	// the operator runs the repair itself, so a shell line would only be a
	// longer way to reach the same place (review 2026-09-25).
	indexGap := searchCoverage{Untracked: 1, BackfillCandidates: 2}
	steps := searchCoverageSteps(indexGap, true)
	if len(steps) != 1 {
		t.Fatalf("index gaps need one commandless remedy: %+v", steps)
	}
	if !strings.Contains(steps[0].Label, "3 recordings") {
		t.Fatalf("remedy did not name what it would re-index: %+v", steps[0])
	}
	if steps := searchCoverageSteps(searchCoverage{Silent: 1, Disabled: 1}, true); len(steps) != 0 {
		t.Fatalf("settled empty transcripts offered a repair: %+v", steps)
	}
}

// A shortfall nobody can act on must not colour the instance. Silent and
// Disabled are excluded from NeedsAttention for that reason (D-798).
func TestSearchCoverageIgnoresShortfallsNobodyCanClear(t *testing.T) {
	for _, settled := range []struct {
		name     string
		coverage searchCoverage
	}{
		{"silent after completed transcription", searchCoverage{Indexed: 137, Unavailable: 1, Silent: 1}},
		// An empty transcript whose cause was never recorded. The cause is
		// unknown; the outcome is not, and no re-index can add words to it.
		{"empty transcript of unknown cause", searchCoverage{Indexed: 137, Unavailable: 1, UnknownEmpty: 1}},
		{"transcription intentionally off", searchCoverage{Indexed: 137, Unavailable: 1, Disabled: 1}},
	} {
		if got := settled.coverage.NeedsAttention(); got != 0 {
			t.Fatalf("%s is not actionable but counted %d against coverage", settled.name, got)
		}
	}

	// The counterpart: a gap someone CAN close still has to be reported.
	actionable := searchCoverage{Indexed: 130, Untracked: 8}
	if got := actionable.NeedsAttention(); got != 8 {
		t.Fatalf("archive recordings with no index row are actionable, counted %d", got)
	}
}

// Coverage is a check, not a read. Listing the archive costs a PROPFIND and
// reading the index is O(archive + index), while the panel polls GET every five
// seconds — so it is taken when an administrator asks, and GET reports what the
// last one found.
func TestArchiveCoverageIsTakenByACheckAndReportedByAPoll(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	if err := os.MkdirAll(rt.cfg.SiteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	row := func() readinessCheck {
		t.Helper()
		for _, c := range rt.readiness(context.Background()).Checks {
			if c.ID == "archive.search" {
				return c
			}
		}
		t.Fatal("archive.search missing")
		return readinessCheck{}
	}

	// Nobody has asked yet. That is an absence, not a verdict.
	if c := row(); c.Code != "search_coverage_not_checked" || c.State != "not_verified" {
		t.Fatalf("before any check = %+v; want an unchecked row", c)
	}

	seedSearchable(t, rt.searchStore, "LIVE.opus", seg("s1", "S1", 0, 1000, "words"))
	if err := os.WriteFile(filepath.Join(rt.cfg.SiteRoot, "catalog.json"),
		[]byte(`{"meetings":[{"id":"LIVE","audioPath":"./meetings/LIVE.opus"},{"id":"OLD","audioPath":"./meetings/OLD.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Still the old answer: a poll does not take a new one.
	if c := row(); c.Code != "search_coverage_not_checked" {
		t.Fatalf("a poll took its own coverage reading: %+v", c)
	}

	rt.recordArchiveCoverage(context.Background())
	c := row()
	if c.State != "warn" || c.Code != "search_coverage_partial" {
		t.Fatalf("after a check = %+v; the unindexed archive meeting should show", c)
	}
	if !strings.Contains(c.Message, "1 is in the archive but not indexed yet") {
		t.Fatalf("coverage did not name the gap: %q", c.Message)
	}
	if c.Repair != "" {
		t.Fatalf("a local archive offered an unsupported repair: %+v", c)
	}
	if c.CheckedAt == "" {
		t.Fatal("a finding must carry when it was taken")
	}
}

func TestLocalArchiveDoesNotOfferOrStartUnsupportedRepair(t *testing.T) {
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	t.Setenv(envAppSecret, "")
	t.Setenv(envAppAPIRequired, "false")
	if err := os.MkdirAll(rt.cfg.SiteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rt.cfg.SiteRoot, "catalog.json"), []byte(`{"meetings":[{"id":"OLD","audioPath":"./meetings/OLD.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	check := rt.searchReadinessCheck(context.Background())
	if check.State != "warn" || check.Repair != "" {
		t.Fatalf("local archive gap offered an unsupported repair: %+v", check)
	}
	if len(check.Steps) == 0 || strings.Contains(check.Steps[0].Label, "Re-index") {
		t.Fatalf("local archive remedy still suggests unsupported re-indexing: %+v", check)
	}
	rec := httptest.NewRecorder()
	rt.readinessHandler(rec, httptest.NewRequest(http.MethodPost, "/health/repair", strings.NewReader(`{"action":"backfill_search"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("local repair request returned %d: %s", rec.Code, rec.Body.String())
	}
	running, ran, _, _, _ := rt.searchRepair.snapshot()
	if running || ran {
		t.Fatal("unsupported repair started a background worker")
	}
	// AppAPI credentials alone do not make a local catalog repairable through
	// the Nextcloud archive: those are different recording destinations.
	t.Setenv(envAppSecret, "test-secret")
	check = rt.searchReadinessCheck(context.Background())
	if check.Repair != "" {
		t.Fatalf("a local archive offered a repair of a different destination: %+v", check)
	}
	rt.cfg.PublishSink = publishSinkNextcloudFiles
	check = readinessCheck{}
	rt.describeSearchBackfill(&check, searchCoverage{Untracked: 1})
	if check.Repair != repairBackfillSearch {
		t.Fatalf("supported Nextcloud archive gap offered no repair: %+v", check)
	}
}

func TestSearchCoverageReadsAsPlainSentences(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    searchCoverage
		want string
	}{
		{"all searchable", searchCoverage{Indexed: 12}, "12 meetings are searchable."},
		{"one searchable", searchCoverage{Indexed: 1}, "1 meeting is searchable."},
		{"some left out", searchCoverage{Indexed: 9, Untracked: 3, Silent: 1}, "9 meetings are searchable. Of the others, 1 was silent and 3 are in the archive but not indexed yet."},
		{"nothing searchable", searchCoverage{Untracked: 2}, "No meetings are searchable yet: 2 are in the archive but not indexed yet."},
		{"nothing at all", searchCoverage{}, "No meetings are searchable yet."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeSearchCoverage(tc.c); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
