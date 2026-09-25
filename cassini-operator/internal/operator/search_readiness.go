package operator

import (
	"context"
	"fmt"
	"strings"
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

	inventory, known, inventoryErr := rt.searchInventory()
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
		coverage.Unsupported = inventory.Unsupported
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
		check.Steps = append([]readinessStep{{Label: "Check storage again to list the archive before judging search coverage"}}, searchCoverageSteps(coverage)...)
		return check
	}

	check.Message = describeSearchCoverage(coverage)
	if len(inventory.OpusNames) == 0 && coverage.TotalKnown() == 0 && coverage.NeedsAttention() == 0 {
		// An archive nobody has recorded into yet is healthy, not unresolved.
		//
		// This has to come before the catalog branch below, because catalog.json
		// does not exist until the first meeting is published — so a brand new
		// install has no catalog for the same reason it has no meetings, and
		// reporting that as "coverage cannot be established" put an unresolved
		// row on an instance whose only fault was being new.
		check.State, check.Code = "passed", "search_archive_empty"
		check.Message = "No meetings have been recorded yet, so search has nothing to index."
	} else if !inventory.CatalogPresent {
		check.State, check.Code = "not_verified", "search_coverage_scope_unknown"
		check.Message += " The recordings catalog was not found, so this listing alone cannot establish archive coverage."
		check.Action = "recheck"
		check.Steps = append([]readinessStep{{Label: "Check why the recordings catalog is absent"}}, searchCoverageSteps(coverage)...)
	} else if coverage.NeedsAttention() > 0 {
		check.State, check.Code = "warn", "search_coverage_partial"
		check.Message += " The checked archive has recordings outside search coverage."
		check.Action = "recheck"
		check.Steps = searchCoverageSteps(coverage)
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
	return check
}

// Backfill helps only when an archive meeting has no index row or its bundle
// could not be verified. It cannot produce words transcription never created.
func searchCoverageSteps(c searchCoverage) []readinessStep {
	var steps []readinessStep
	if candidates := c.Untracked + c.BackfillCandidates; candidates > 0 {
		steps = append(steps, readinessStep{
			Label:    fmt.Sprintf("In the Cassini operator container or host, re-index the %d recording(s) with no index row or an unverified bundle", candidates),
			Commands: []string{"cassini-operator backfill-search"},
		})
	}
	if c.ModelUnavailable+c.TranscriptionFailed > 0 {
		steps = append(steps, readinessStep{Label: "Check transcription settings and model readiness. Rebuilding the search index cannot add words to already published audio"})
	}
	if c.MissingTranscript+c.UnreadableTranscript+c.UnknownEmpty+c.OtherUnavailable > 0 {
		steps = append(steps, readinessStep{Label: "Inspect the affected recording bundles and their transcription outcomes; a missing transcript needs investigation before indexing can help"})
	}
	if c.Unsupported > 0 {
		steps = append(steps, readinessStep{Label: "Inspect legacy or non-Opus archive entries; search cannot index them with an Opus join key"})
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
		{c.Unsupported, "legacy or non-Opus archive entries outside search"},
	} {
		if item.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.count, item.label))
		}
	}
	return "The search index records " + strings.Join(parts, "; ") + "."
}
