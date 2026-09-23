package operator

import (
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
	for _, step := range searchCoverageSteps(missingWords) {
		if len(step.Commands) > 0 {
			t.Fatalf("backfill cannot create missing words: %+v", step)
		}
	}

	indexGap := searchCoverage{Untracked: 1, BackfillCandidates: 2}
	steps := searchCoverageSteps(indexGap)
	if len(steps) != 1 || len(steps[0].Commands) != 1 || steps[0].Commands[0] != "cassini-operator backfill-search" {
		t.Fatalf("index gaps need a backfill remedy: %+v", steps)
	}
	if !strings.Contains(steps[0].Label, "Cassini operator container or host") {
		t.Fatalf("backfill command omitted its execution location: %+v", steps[0])
	}
	if steps := searchCoverageSteps(searchCoverage{Silent: 1, Disabled: 1}); len(steps) != 0 {
		t.Fatalf("settled empty transcripts offered a repair: %+v", steps)
	}
}

// A shortfall nobody can act on must not colour the instance. Silent and
// Disabled were excluded from NeedsAttention for that reason; Unsupported was
// not, which made any archive holding one legacy directory-shaped meeting
// permanently amber with no action that could ever clear it (D-798).
func TestSearchCoverageIgnoresShortfallsNobodyCanClear(t *testing.T) {
	for _, settled := range []struct {
		name     string
		coverage searchCoverage
	}{
		{"legacy non-Opus archive entries", searchCoverage{Indexed: 137, Unsupported: 1}},
		{"silent after completed transcription", searchCoverage{Indexed: 137, Unavailable: 1, Silent: 1}},
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
