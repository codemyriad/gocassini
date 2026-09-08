package operator

import (
	"context"
	"strings"
	"testing"
)

// seedSearchable indexes one meeting's segments.
func seedSearchable(t *testing.T, store *searchStore, opusName string, segments ...searchTranscriptSegment) {
	t.Helper()
	rows := searchRowsFromSegments(segments)
	if err := store.ReplaceMeeting(context.Background(), opusName, "sha-"+opusName, searchRowSourceSegments, rows); err != nil {
		t.Fatalf("seed %s: %v", opusName, err)
	}
}

func seg(id, speaker string, start, end int64, text string) searchTranscriptSegment {
	return searchTranscriptSegment{ID: id, SpeakerID: speaker, StartMS: start, EndMS: end, Text: text}
}

// THE security property. A caller sees only what their visible set allows, and
// the bound set is what limits the result — not a filter applied afterwards.
func TestSearchReturnsOnlyVisibleMeetings(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "we discussed the acquisition"))
	seedSearchable(t, store, "THEIRS.opus", seg("s1", "S9", 1000, 4000, "we discussed the acquisition"))

	got, err := store.Search(context.Background(), searchRequest{Text: "acquisition", Visible: []string{"MINE.opus"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want exactly the visible meeting", got.Hits)
	}
	if got.Hits[0].OpusName != "MINE.opus" {
		t.Errorf("hit = %q, want MINE.opus", got.Hits[0].OpusName)
	}
}

// An empty visible set is safe BY CONSTRUCTION: json_each('[]') yields no rows,
// so an inner join against it can return nothing whatever the query says.
func TestSearchWithNoVisibleMeetingsFindsNothing(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "THEIRS.opus", seg("s1", "S9", 1000, 4000, "the acquisition"))

	for _, visible := range [][]string{nil, {}} {
		got, err := store.Search(context.Background(), searchRequest{Text: "acquisition", Visible: visible})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(got.Hits) != 0 {
			t.Fatalf("hits = %+v, want none for an empty visible set", got.Hits)
		}
	}
}

// The visible set bounds the rows BEFORE the limit. With the caller's own
// meeting buried behind many invisible matches, a post-filter with any fixed
// limit would report nothing; this must still find it.
func TestSearchLimitAppliesAfterVisibilityNotBefore(t *testing.T) {
	store := newTestSearchStore(t)
	for i := 0; i < 200; i++ {
		seedSearchable(t, store, "HIDDEN"+string(rune('A'+i%26))+string(rune('a'+i/26))+".opus",
			seg("s1", "S9", 1000, 4000, "quarterly revenue discussion"))
	}
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 5000, 9000, "quarterly revenue discussion"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "quarterly revenue", Visible: []string{"MINE.opus"}, Limit: 5,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 || got.Hits[0].OpusName != "MINE.opus" {
		t.Fatalf("hits = %+v, want the one visible meeting despite 200 hidden matches", got.Hits)
	}
}

// A hit is a reference: meeting, segment, when, who. Never the words.
func TestSearchReturnsReferencesNotText(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("seg_0007", "S1", 14_000, 18_500, "the severance package"))

	got, err := store.Search(context.Background(), searchRequest{Text: "severance", Visible: []string{"MINE.opus"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	hit := got.Hits[0]
	if hit.SegmentID != "seg_0007" || hit.StartMS != 14_000 || hit.EndMS != 18_500 || hit.SpeakerID != "S1" {
		t.Fatalf("hit = %+v, want the full reference", hit)
	}
}

// Aliases: what the recogniser wrote when someone said the name.
func TestSearchFindsMistranscribedNames(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "the casino recorder joins the call"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "cassini", Visible: []string{"MINE.opus"}, UseAliases: true, AliasIndex: buildSearchAliasIndex(searchAliasGroups),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want the mistranscribed mention", got.Hits)
	}
	// And it says so, rather than implying the word was actually spoken.
	if got.Hits[0].Matched != searchMatchedAlias {
		t.Errorf("matched = %q, want %q", got.Hits[0].Matched, searchMatchedAlias)
	}
}

func TestSearchLabelsALiteralMatchAsExact(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "the cassini recorder joins"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "cassini", Visible: []string{"MINE.opus"}, UseAliases: true, AliasIndex: buildSearchAliasIndex(searchAliasGroups),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 || got.Hits[0].Matched != searchMatchedExact {
		t.Fatalf("hits = %+v, want one exact match", got.Hits)
	}
}

func TestSearchWithoutAliasesDoesNotExpand(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "the casino recorder"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "cassini", Visible: []string{"MINE.opus"}, UseAliases: false,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 0 {
		t.Fatalf("hits = %+v, want none without alias expansion", got.Hits)
	}
}

// Every word first; widen only if that finds nothing, and say it happened —
// a widened result answers a different question from the one asked.
func TestSearchWidensAndReportsIt(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus",
		seg("s1", "S1", 1000, 4000, "the deployment pipeline"),
		seg("s2", "S1", 9000, 12000, "the migration plan"))

	both, err := store.Search(context.Background(), searchRequest{
		Text: "deployment pipeline", Visible: []string{"MINE.opus"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if both.Widened || len(both.Hits) != 1 {
		t.Fatalf("expected one strict hit, got widened=%v hits=%+v", both.Widened, both.Hits)
	}

	apart, err := store.Search(context.Background(), searchRequest{
		Text: "deployment migration", Visible: []string{"MINE.opus"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !apart.Widened {
		t.Error("a query no single segment satisfies should report that it widened")
	}
	if len(apart.Hits) != 2 {
		t.Errorf("hits = %d, want both segments after widening", len(apart.Hits))
	}
}

// A single-word query must never claim it widened: there is nothing to widen.
func TestSearchDoesNotWidenASingleWordQuery(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "nothing relevant here"))

	got, err := store.Search(context.Background(), searchRequest{Text: "acquisition", Visible: []string{"MINE.opus"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.Widened || len(got.Hits) != 0 {
		t.Fatalf("got widened=%v hits=%+v, want a plain empty answer", got.Widened, got.Hits)
	}
}

func TestSearchFiltersBySpeaker(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus",
		seg("s1", "S1", 1000, 4000, "the acquisition"),
		seg("s2", "S2", 9000, 12000, "the acquisition"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "acquisition", Visible: []string{"MINE.opus"}, SpeakerID: "S2",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 || got.Hits[0].SpeakerID != "S2" {
		t.Fatalf("hits = %+v, want only S2", got.Hits)
	}
}

// A five-letter term matches as a prefix, which is what finds "recording" and
// "recorder" without a stemmer.
func TestSearchMatchesLongerTermsAsPrefixes(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "we started recording the call"))

	got, err := store.Search(context.Background(), searchRequest{Text: "record", Visible: []string{"MINE.opus"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v, want the prefix match", got.Hits)
	}
}

// The caller cannot reach the query language: terms are stripped to letters and
// digits and then quoted, so operators and syntax arrive as ordinary words.
func TestSearchTreatsQuerySyntaxAsWords(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "the acquisition closed"))

	for _, query := range []string{
		`acquisition OR "*"`,
		`acquisition NEAR/5 nothing`,
		`acquisition) OR (segment_fts`,
		`acquisition" OR "1"="1`,
	} {
		got, err := store.Search(context.Background(), searchRequest{Text: query, Visible: []string{"MINE.opus"}})
		if err != nil {
			t.Fatalf("query %q errored: %v", query, err)
		}
		for _, hit := range got.Hits {
			if hit.OpusName != "MINE.opus" {
				t.Errorf("query %q reached beyond the visible set: %+v", query, hit)
			}
		}
	}
}

func TestSearchRejectsAQueryWithNoWords(t *testing.T) {
	store := newTestSearchStore(t)
	if _, err := store.Search(context.Background(), searchRequest{Text: "  !!  ", Visible: []string{"MINE.opus"}}); err == nil {
		t.Fatal("expected an error for a query with no searchable words")
	}
}

func TestSearchCapsTheLimit(t *testing.T) {
	store := newTestSearchStore(t)
	segments := make([]searchTranscriptSegment, 0, 200)
	for i := 0; i < 200; i++ {
		segments = append(segments, seg("s"+string(rune('A'+i%26))+string(rune('a'+i/26)), "S1",
			int64(i)*1000, int64(i)*1000+900, "quarterly revenue"))
	}
	seedSearchable(t, store, "MINE.opus", segments...)

	got, err := store.Search(context.Background(), searchRequest{
		Text: "quarterly", Visible: []string{"MINE.opus"}, Limit: 10_000,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) > searchMaxLimit {
		t.Fatalf("hits = %d, want at most %d", len(got.Hits), searchMaxLimit)
	}
}

// The caller can see what was actually searched for, so an alias hit is
// explicable rather than mysterious.
func TestSearchReportsTheExpandedGroups(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "casino"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "cassini", Visible: []string{"MINE.opus"}, UseAliases: true, AliasIndex: buildSearchAliasIndex(searchAliasGroups),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Groups) != 1 || len(got.Groups[0]) < 2 {
		t.Fatalf("groups = %+v, want the alias group", got.Groups)
	}
	if !strings.Contains(strings.Join(got.Groups[0], " "), "casino") {
		t.Errorf("groups = %+v, want the variant that matched", got.Groups)
	}
}

// Multi-word alias keys are consumed as one group, longest span first.
func TestGroupQueryWordsPrefersTheLongestAliasSpan(t *testing.T) {
	groups := groupQueryWords([]string{"next", "cloud", "talk", "recording"}, true, buildSearchAliasIndex(searchAliasGroups))
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want the alias span plus one word", groups)
	}
	if len(groups[1]) != 1 || groups[1][0] != "recording" {
		t.Errorf("second group = %+v, want the leftover word", groups[1])
	}
}

func TestGroupQueryWordsLeavesUnknownWordsAlone(t *testing.T) {
	groups := groupQueryWords([]string{"quarterly", "revenue"}, true, buildSearchAliasIndex(searchAliasGroups))
	if len(groups) != 2 || len(groups[0]) != 1 || len(groups[1]) != 1 {
		t.Fatalf("groups = %+v, want one group per word", groups)
	}
}

// An operator's own alias groups are merged with the shipped ones, because the
// variants that matter are specific to a deployment's vocabulary and its model.
func TestSearchUsesOperatorConfiguredAliases(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "the ice book release"))

	configured := [][]string{{"eisbuk", "ice book"}}
	got, err := store.Search(context.Background(), searchRequest{
		Text: "eisbuk", Visible: []string{"MINE.opus"}, UseAliases: true,
		AliasIndex: mergeSearchAliasGroups(nil, configured),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 || got.Hits[0].Matched != searchMatchedAlias {
		t.Fatalf("hits = %+v, want one alias match from the configured group", got.Hits)
	}
}

// Adding one name must not silently drop every shipped group.
func TestOperatorAliasesMergeRatherThanReplace(t *testing.T) {
	index := mergeSearchAliasGroups(searchAliasGroups, [][]string{{"widget", "wodget"}})

	if group, ok := index["casino"]; !ok || len(group) < 2 {
		t.Errorf("a shipped group was lost when an operator added one: %v", group)
	}
	if group, ok := index["wodget"]; !ok || len(group) != 2 {
		t.Errorf("the configured group is missing: %v", group)
	}
}

// An operator who lists spellings for a name we also ship has seen what their
// own transcriber produces, so theirs replaces ours rather than blending.
func TestOperatorAliasesOverrideAShippedGroup(t *testing.T) {
	index := mergeSearchAliasGroups(
		[][]string{{"cassini", "casino", "casini"}},
		[][]string{{"cassini", "kassini"}})

	group, ok := index["cassini"]
	if !ok {
		t.Fatal("the configured group did not take effect")
	}
	if len(group) != 2 || group[1] != "kassini" {
		t.Errorf("group = %v, want only the operator's spellings", group)
	}
	// The shipped variants no longer resolve, so a stale spelling cannot linger
	// in a group the operator has replaced.
	if _, stale := index["casino"]; stale {
		t.Error("a shipped variant survived an operator override")
	}
}

// With no table at all, expansion is simply off rather than erroring.
func TestSearchWithNoAliasTableStillWorks(t *testing.T) {
	store := newTestSearchStore(t)
	seedSearchable(t, store, "MINE.opus", seg("s1", "S1", 1000, 4000, "the acquisition"))

	got, err := store.Search(context.Background(), searchRequest{
		Text: "acquisition", Visible: []string{"MINE.opus"}, UseAliases: true, AliasIndex: nil,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Hits) != 1 || got.Hits[0].Matched != searchMatchedExact {
		t.Fatalf("hits = %+v, want a plain exact match", got.Hits)
	}
}
