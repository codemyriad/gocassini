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
