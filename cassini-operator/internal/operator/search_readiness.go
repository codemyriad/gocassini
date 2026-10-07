package operator

import (
	"context"
	"errors"
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
		check.Message = describeSearchCoverage(coverage) + " Cassini has not listed the archive yet, so older recordings may not be counted."
		if coverage.TotalKnown() == 0 {
			check.Code = "search_coverage_empty"
			check.Message = "No meetings have been recorded yet, so search has nothing to index."
		}
		if coverage.NeedsAttention() > 0 {
			check.State, check.Code = "warn", "search_coverage_partial"
		}
		check.Action = "recheck"
		check.Steps = append([]readinessStep{{Label: "Check Recording storage again so Cassini can list the archive, then check search again"}}, searchCoverageSteps(coverage, rt.canBackfillSearch())...)
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
		check.Action = "recheck"
		check.Steps = searchCoverageSteps(coverage, rt.canBackfillSearch())
	} else if coverage.TotalKnown() == 0 {
		check.State, check.Code = "passed", "search_archive_empty"
		check.Message = "The recordings archive is empty, so search has nothing to index."
	} else {
		check.State, check.Code = "passed", "search_archive_files_accounted_for"
		check.Message += " Every recording in the archive is accounted for."
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
		check.Running = true
		check.Steps = nil
		check.Message += " Re-indexing is running now."
		return
	case err != nil:
		check.RepairFailed = true
		describeSearchRepairFailure(check, err)
	case ran && !finished.IsZero():
		check.Message += fmt.Sprintf(" The last re-index added %d, left %d unchanged, found %d not searchable and failed on %d.",
			report.Indexed, report.Unchanged, report.Unavailable, report.Failed)
	}
}

var searchRepairLogStep = readinessStep{
	Label:    "The full error is in Cassini's log. On the Docker host:",
	Commands: []string{"docker logs nc_app_gocassini 2>&1 | grep 'search backfill'"},
}

func describeSearchRepairFailure(check *readinessCheck, err error) {
	var failure *searchRepairError
	kind := ""
	if errors.As(err, &failure) {
		kind = failure.kind
	}
	switch kind {
	case searchRepairArchiveUnreadable:
		check.Code = "search_reindex_archive_unreadable"
		check.Message += " The last re-index could not list the recordings archive in Nextcloud."
		check.Steps = []readinessStep{
			{Label: "Recording storage uses the same access to the archive. If it needs attention, fix that first"},
			{Label: "If it passes, Nextcloud was probably busy or restarting, so try re-indexing again"},
			searchRepairLogStep,
		}
	case searchRepairIndexUnavailable:
		check.Code = "search_reindex_index_unavailable"
		check.Repair = ""
		check.Message += " The last re-index could not use the search index, so trying again would fail the same way."
		check.Steps = []readinessStep{
			{Label: "Check that Cassini's data volume is mounted and writable: the search index is kept on it"},
			{Label: "Then restart Cassini by disabling and re-enabling it in Nextcloud's apps"},
			searchRepairLogStep,
		}
	case searchRepairEnvironment:
		check.Code = "search_reindex_environment"
		check.Repair = ""
		check.Message += " The last re-index could not read the settings AppAPI gives Cassini, so trying again would fail the same way."
		check.Steps = []readinessStep{
			{Label: "Cassini is running without its AppAPI settings (`NEXTCLOUD_URL`, `APP_SECRET`, `APP_ID`). Deploy it through AppAPI rather than starting its container by hand"},
			searchRepairLogStep,
		}
	default:
		check.Message += " The last re-index did not finish: " + err.Error()
		check.Steps = append(check.Steps, searchRepairLogStep)
	}
}

// Backfill helps only when an archive meeting has no index row or its bundle
// could not be verified. It cannot produce words transcription never created.
func searchCoverageSteps(c searchCoverage, canBackfill bool) []readinessStep {
	var steps []readinessStep
	if candidates := c.Untracked + c.BackfillCandidates; candidates > 0 {
		// No command here on purpose. The row carries Repair instead, and the
		// panel offers a button that runs it in this process.
		label := "Check the affected recordings and their entries in the search index"
		if canBackfill && candidates == 1 {
			label = "Re-index now adds the 1 recording that is not in search yet"
		} else if canBackfill {
			label = fmt.Sprintf("Re-index now adds the %d recordings that are not in search yet", candidates)
		}
		steps = append(steps, readinessStep{Label: label})
	}
	if c.ModelUnavailable+c.TranscriptionFailed > 0 {
		steps = append(steps, readinessStep{Label: "Check transcription settings and that the model is ready. Indexing again cannot add words to recordings that were never transcribed"})
	}
	if c.MissingTranscript+c.UnreadableTranscript+c.UnknownEmpty+c.OtherUnavailable > 0 {
		steps = append(steps, readinessStep{Label: "Check the affected recordings' transcripts. A missing or unreadable transcript has to be fixed before indexing can help"})
	}
	return steps
}

// describeSearchCoverage retains the reason for every empty transcript. The
// caller chooses whether these numbers describe the whole observed archive or
// only the sidecar's current rows.
func describeSearchCoverage(c searchCoverage) string {
	var rest []string
	for _, item := range []struct {
		count     int
		one, many string
	}{
		{c.Silent, "was silent", "were silent"},
		{c.Disabled, "was recorded with transcription off", "were recorded with transcription off"},
		{c.ModelUnavailable, "was not transcribed because a model was unavailable", "were not transcribed because a model was unavailable"},
		{c.TranscriptionFailed, "failed to transcribe", "failed to transcribe"},
		{c.UnknownEmpty, "has an empty transcript for an unknown reason", "have empty transcripts for an unknown reason"},
		{c.MissingTranscript, "is missing its transcript", "are missing their transcripts"},
		{c.UnreadableTranscript, "has an unreadable transcript", "have unreadable transcripts"},
		{c.BackfillCandidates, "is waiting to be verified for search", "are waiting to be verified for search"},
		{c.OtherUnavailable, "could not be indexed for another reason", "could not be indexed for other reasons"},
		{c.Untracked, "is in the archive but not indexed yet", "are in the archive but not indexed yet"},
	} {
		switch {
		case item.count == 1:
			rest = append(rest, "1 "+item.one)
		case item.count > 1:
			rest = append(rest, fmt.Sprintf("%d %s", item.count, item.many))
		}
	}
	switch {
	case c.Indexed == 0 && len(rest) == 0:
		return "No meetings are searchable yet."
	case c.Indexed == 0:
		return "No meetings are searchable yet: " + plainList(rest) + "."
	}
	searchable := fmt.Sprintf("%d meetings are searchable.", c.Indexed)
	if c.Indexed == 1 {
		searchable = "1 meeting is searchable."
	}
	if len(rest) == 0 {
		return searchable
	}
	return searchable + " Of the others, " + plainList(rest) + "."
}

func plainList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
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
