package operator

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// searchReadinessCheck compares the search sidecar with an independently
// observed archive when possible. A count from meeting_index alone can never
// establish completeness: legacy recordings may have no row in it.
func (rt *Runtime) searchReadinessCheck(ctx context.Context) readinessCheck {
	check := readinessCheck{ID: "archive.search"}
	if rt.searchStore == nil {
		check.State, check.Code = "not_verified", "search_coverage_unknown"
		check.Message = "The search index is unavailable, so archive search coverage is unknown."
		check.Action = "recheck"
		check.Steps = []readinessStep{{Label: "Check that Cassini's search index database is available, then check again"}}
		return check
	}

	inventory, known, inventoryErr := rt.searchInventory(ctx)
	if inventoryErr != nil {
		check.State, check.Code = "warn", "search_archive_unreadable"
		check.Message = "Cassini could not read the archive inventory, so search coverage is unknown."
		check.Action = "recheck"
		check.Steps = []readinessStep{{Label: "Check the recordings catalog and try the archive check again"}}
		return check
	}
	var coverage searchCoverage
	var err error
	if known {
		coverage, err = rt.searchStore.CoverageForArchive(ctx, inventory.OpusNames)
		check.CheckedAt = inventory.CheckedAt
	} else {
		coverage, err = rt.searchStore.Coverage(ctx)
	}
	if err != nil {
		check.State, check.Code = "warn", "search_coverage_unknown"
		check.Message = "Cassini could not read the search index, so archive search coverage is unknown."
		check.Action = "recheck"
		check.Steps = []readinessStep{{Label: "Check the search index database and try again"}}
		return check
	}

	if !known {
		check.State, check.Code = "not_verified", "search_coverage_scope_unknown"
		check.Message = describeSearchCoverage(coverage) + " The archive has not been listed, so older recordings may be missing from this index."
		if coverage.TotalKnown() == 0 {
			check.Code = "search_coverage_empty"
			check.Message = "No meetings have been recorded yet, so search has nothing to index."
		}
		if coverage.NeedsAttention() > 0 {
			check.State, check.Code = "warn", "search_coverage_partial"
		}
		check.Action = "recheck"
		check.Steps = append([]readinessStep{{Label: "Check storage again to list the archive before judging search coverage"}}, searchCoverageSteps(coverage, rt.canBackfillSearch())...)
		rt.describeSearchBackfill(&check, coverage)
		return check
	}

	check.Message = describeSearchCoverage(coverage)
	if len(inventory.OpusNames) == 0 && coverage.TotalKnown() == 0 && coverage.NeedsAttention() == 0 {
		// An archive nobody has recorded into yet is healthy, not unresolved.
		// The listing succeeded and found nothing, which is a finding.
		check.State, check.Code = "passed", "search_archive_empty"
		check.Message = "No meetings have been recorded yet, so search has nothing to index."
	} else if coverage.NeedsAttention() > 0 {
		check.State, check.Code = "warn", "search_coverage_partial"
		check.Message += " The checked archive has recordings outside search coverage."
		check.Action = "recheck"
		check.Steps = searchCoverageSteps(coverage, rt.canBackfillSearch())
	} else if coverage.TotalKnown() == 0 {
		check.State, check.Code = "passed", "search_archive_empty"
		check.Message = "The checked recordings archive is empty. Search has no meetings to index."
	} else {
		check.State, check.Code = "passed", "search_archive_files_accounted_for"
		check.Message += " Every Opus file in the checked archive listing has an index outcome."
	}
	if coverage.NeedsAttention() > 0 && check.State == "not_verified" {
		check.State, check.Code = "warn", "search_coverage_partial"
	}
	rt.describeSearchBackfill(&check, coverage)
	return check
}

// describeSearchBackfill offers a supported repair for the observed gap.
//
// Backfill only helps meetings with no index row or an unverified bundle. It
// cannot produce words transcription never created, so a row whose whole
// shortfall is missing or failed transcription gets no button — offering one
// there would be a button that changes nothing.
func (rt *Runtime) describeSearchBackfill(check *readinessCheck, coverage searchCoverage) {
	if coverage.Untracked+coverage.BackfillCandidates == 0 {
		return
	}
	if rt.canBackfillSearch() {
		check.Repair = repairBackfillSearch
		return
	}
}

// Repair progress is live process state, separate from the cached coverage
// observation. Add it to a copy on each read without listing the archive again.
func (rt *Runtime) describeSearchRepair(check *readinessCheck) {
	running, ran, report, err, finished := rt.searchRepair.snapshot()
	switch {
	case running:
		check.Repair = ""
		check.Message += " Re-indexing is running now."
		return
	case err != nil:
		check.Message += " The last re-index did not finish: " + err.Error()
	case ran && !finished.IsZero():
		check.Message += fmt.Sprintf(" Last re-index: %d indexed, %d unchanged, %d not searchable, %d failed.",
			report.Indexed, report.Unchanged, report.Unavailable, report.Failed)
	}
}

// Backfill helps only when an archive meeting has no index row or its bundle
// could not be verified. It cannot produce words transcription never created.
func searchCoverageSteps(c searchCoverage, canBackfill bool) []readinessStep {
	var steps []readinessStep
	if candidates := c.Untracked + c.BackfillCandidates; candidates > 0 {
		// No command here on purpose. The row carries Repair instead, and the
		// panel offers a button that runs it in this process.
		label := "Inspect the affected recording bundles and their search index entries"
		if canBackfill {
			label = fmt.Sprintf("Re-index the %d recording(s) with no index row or an unverified bundle", candidates)
		}
		steps = append(steps, readinessStep{Label: label})
	}
	if c.ModelUnavailable+c.TranscriptionFailed > 0 {
		steps = append(steps, readinessStep{Label: "Check transcription settings and model readiness. Rebuilding the search index cannot add words to already published audio"})
	}
	if c.MissingTranscript+c.UnreadableTranscript+c.UnknownEmpty+c.OtherUnavailable > 0 {
		steps = append(steps, readinessStep{Label: "Inspect the affected recording bundles and their transcription outcomes; a missing transcript needs investigation before indexing can help"})
	}
	return steps
}

// describeSearchCoverage retains the reason for every empty transcript. The
// caller chooses whether these numbers describe the whole observed archive or
// only the sidecar's current rows.
func describeSearchCoverage(c searchCoverage) string {
	parts := []string{fmt.Sprintf("%d indexed meeting(s)", c.Indexed)}
	for _, item := range []struct {
		count int
		label string
	}{
		{c.Silent, "silent after completed transcription"},
		{c.Disabled, "intentionally without transcription"},
		{c.ModelUnavailable, "without transcription because a model was unavailable"},
		{c.TranscriptionFailed, "with failed transcription"},
		{c.UnknownEmpty, "with empty transcripts of unknown cause"},
		{c.MissingTranscript, "with missing transcripts"},
		{c.UnreadableTranscript, "with unreadable transcripts"},
		{c.BackfillCandidates, "awaiting archive or bundle verification"},
		{c.OtherUnavailable, "with other indexing problems"},
		{c.Untracked, "archive Opus recordings without index rows"},
	} {
		if item.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.count, item.label))
		}
	}
	return "The search index records " + strings.Join(parts, "; ") + "."
}

// archiveCoverageState holds what the last archive check found.
//
// Coverage is a check, not a read: listing the archive costs a PROPFIND and
// reading the index is O(archive + index), and the panel polls GET every five
// seconds. So it runs when an administrator asks — alongside the media doctor
// and the Talk probe — and GET reports the finding with the time it was taken,
// exactly as the host rows do.
type archiveCoverageState struct {
	mu    sync.Mutex
	check readinessCheck
	taken bool
}

func (rt *Runtime) recordArchiveCoverage(ctx context.Context) {
	check := rt.searchReadinessCheck(ctx)
	rt.archiveCoverage.mu.Lock()
	rt.archiveCoverage.check = check
	rt.archiveCoverage.taken = true
	rt.archiveCoverage.mu.Unlock()
}

// lastArchiveCoverage reports the finding, or that no check has taken one.
func (rt *Runtime) lastArchiveCoverage() readinessCheck {
	rt.archiveCoverage.mu.Lock()
	check := rt.archiveCoverage.check
	if !rt.archiveCoverage.taken {
		check = readinessCheck{
			ID: "archive.search", State: "not_verified", Code: "search_coverage_not_checked",
			Message: "Archive search coverage has not been checked yet.", Action: "recheck",
		}
	}
	rt.archiveCoverage.mu.Unlock()
	rt.describeSearchRepair(&check)
	return check
}
