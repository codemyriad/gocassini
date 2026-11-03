package operator

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// unit returns a 2-D unit vector at angle a radians.
func unit(a float64) []float32 { return []float32{float32(math.Cos(a)), float32(math.Sin(a))} }

// angleFor is the angle whose cosine to the x axis is c.
func angleFor(c float64) float64 { return math.Acos(c) }

// scoredPassages builds passages whose cosine to the topic (the x axis) is
// each score, every one 40 words and 1 s long, back to back.
func scoredPassages(scores ...float64) []topicPassage {
	out := make([]topicPassage, len(scores))
	for i, s := range scores {
		out[i] = topicPassage{StartMS: int64(i) * 1000, EndMS: int64(i+1) * 1000, WordCount: 40, Vector: unit(angleFor(s))}
	}
	return out
}

var (
	topicX = []float32{1, 0}
	// A control orthogonal-ish to everything in these tests: margin = score.
	farControl = [][]float32{{0, -1}}
)

// protoParams are the prototype's constants. The mechanics they switch on
// (joining spans by meaning, a minimum span length) are off in the tuned
// constants but still part of the scorer, so these tests pin them.
var protoParams = topicScorerParams{
	Threshold: 0.85, Margin: 0.05, MinSupportWords: 12,
	SpanGapMS: 2500, SpanJoinCosine: 0.88,
	SubstantialSpanWords: 25, QualifyWords: 80,
	StrongSpanWords: 40, StrongMinScore: 0.88, StrongMinMargin: 0.08,
}

func spanPassages(d topicDecision) [][2]int {
	var out [][2]int
	for _, s := range d.Spans {
		out = append(out, [2]int{s.FirstPassage, s.LastPassage})
	}
	return out
}

// From the prototype: an unsupported middle passage and a change of meaning
// both break a span.
func TestScoreTopicSpansBreakOnAMiddlePassageAndOnMeaning(t *testing.T) {
	p := protoParams
	p.Threshold, p.Margin = 0.83, 0.04
	passages := scoredPassages(0.9, 0.7, 0.9, 0.9)
	// Passages 2 and 3 score the same against the topic but point different
	// ways: their own cosine is under 0.88.
	passages[2].Vector = unit(angleFor(0.9))
	passages[3].Vector = unit(-angleFor(0.9))
	d := scoreTopic(passages, topicX, farControl, p)
	if got := spanPassages(d); !reflect.DeepEqual(got, [][2]int{{0, 0}, {2, 2}, {3, 3}}) {
		t.Fatalf("spans %v", got)
	}
}

// From the prototype: a pause over 2.5 s splits a span even when meaning is
// identical.
func TestScoreTopicSpansBreakOnAPause(t *testing.T) {
	passages := scoredPassages(0.9, 0.9)
	passages[1].StartMS, passages[1].EndMS = 10000, 11000
	if got := spanPassages(scoreTopic(passages, topicX, farControl, protoParams)); len(got) != 2 {
		t.Fatalf("spans %v", got)
	}
	// 2.5 s exactly still joins.
	passages[1].StartMS = passages[0].EndMS + 2500
	if got := spanPassages(scoreTopic(passages, topicX, farControl, protoParams)); len(got) != 1 {
		t.Fatalf("a 2.5 s gap split the span: %v", got)
	}
}

// From the prototype: a control close to the passage rejects a high score,
// and the margin is score minus the best control.
func TestScoreTopicControlsRejectGenericTalk(t *testing.T) {
	passages := []topicPassage{{StartMS: 0, EndMS: 1000, WordCount: 80, Vector: unit(angleFor(0.9))}}
	near := [][]float32{farControl[0], unit(angleFor(0.9) + 0.05)}
	if d := scoreTopic(passages, topicX, near, topicScorerV1); len(d.Spans) != 0 {
		t.Fatalf("a passage as close to a control as to the topic supported it: %+v", d.Spans)
	}
	d := scoreTopic(passages, topicX, farControl, topicScorerV1)
	if len(d.Spans) != 1 || math.Abs(d.Spans[0].MinMargin-(0.9-topicCosine(passages[0].Vector, farControl[0]))) > 1e-6 {
		t.Fatalf("spans %+v", d.Spans)
	}
}

func TestScoreTopicThresholdMarginAndTheTwelveWordFloor(t *testing.T) {
	p := topicScorerV1
	at := func(score float64, words int, control []float32) int {
		passages := []topicPassage{{StartMS: 0, EndMS: 1000, WordCount: words, Vector: unit(angleFor(score))}}
		return len(scoreTopic(passages, topicX, [][]float32{control}, p).Spans)
	}
	if at(p.Threshold+0.001, 12, farControl[0]) != 1 || at(p.Threshold-0.001, 12, farControl[0]) != 0 {
		t.Fatalf("threshold %v", p.Threshold)
	}
	if at(0.9, 11, farControl[0]) != 0 {
		t.Fatal("an 11-word passage supported a topic")
	}
	// The control 0.86 from the passage's direction: margin 0.9 - c.
	control := func(margin float64) []float32 {
		// A control at angle b from x scores cos(b - a) with the passage.
		a := angleFor(0.9)
		return unit(a + angleFor(0.9-margin))
	}
	if at(0.9, 40, control(p.Margin+0.001)) != 1 || at(0.9, 40, control(p.Margin-0.001)) != 0 {
		t.Fatalf("margin %v", p.Margin)
	}
}

// From the prototype: the two qualifying rules.
func TestTopicQualifiesOnWordsOrOneStrongSpan(t *testing.T) {
	p := protoParams
	strong := topicSpan{WordCount: 45, MinScore: 0.9, MinMargin: 0.1}
	if got := topicQualifies([]topicSpan{strong}, p); got != topicReasonStrong {
		t.Fatalf("strong span: %q", got)
	}
	weak := strong
	weak.MinScore = 0.84
	if got := topicQualifies([]topicSpan{weak}, p); got != "" {
		t.Fatalf("weak span: %q", got)
	}
	short := strong
	short.WordCount = 10
	if got := topicQualifies(slicesRepeat(short, 10), p); got != "" {
		t.Fatalf("ten 10-word spans (under 25 each): %q", got)
	}
	long := weak
	long.WordCount = 80
	if got := topicQualifies([]topicSpan{long}, p); got != topicReasonWords {
		t.Fatalf("80 words: %q", got)
	}
	// 80 words across spans of at least 25.
	if got := topicQualifies([]topicSpan{{WordCount: 25}, {WordCount: 30}, {WordCount: 25}, {WordCount: 24}}, p); got != topicReasonWords {
		t.Fatalf("25+30+25: %q", got)
	}
	if got := topicQualifies([]topicSpan{{WordCount: 25}, {WordCount: 30}, {WordCount: 24}}, p); got != "" {
		t.Fatalf("25+30 (and 24, too short to count): %q", got)
	}
}

func slicesRepeat(s topicSpan, n int) []topicSpan {
	out := make([]topicSpan, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// A whole meeting: spans carry their time range, words and scores, and short
// spans are kept as evidence even when they do not count.
func TestScoreTopicMeetingRule(t *testing.T) {
	passages := scoredPassages(0.9, 0.9, 0.5, 0.9)
	passages[3].WordCount = 20
	d := scoreTopic(passages, topicX, farControl, protoParams)
	if !d.qualifies() || d.Reason != topicReasonWords || len(d.Spans) != 2 {
		t.Fatalf("decision %+v", d)
	}
	first := d.Spans[0]
	if first.StartMS != 0 || first.EndMS != 2000 || first.WordCount != 80 || math.Abs(first.PeakScore-0.9) > 1e-6 {
		t.Fatalf("first span %+v", first)
	}
	if d.Spans[1].WordCount != 20 {
		t.Fatalf("the short span was dropped: %+v", d.Spans)
	}
	// One 40-word span is not 80 words, but at 0.9 with a wide margin it is
	// a short strong discussion; at 0.87 it is neither.
	if d := scoreTopic(scoredPassages(0.9, 0.5), topicX, farControl, protoParams); d.Reason != topicReasonStrong {
		t.Fatalf("one strong 40-word span: %+v", d)
	}
	if d := scoreTopic(scoredPassages(0.87, 0.5), topicX, farControl, protoParams); d.qualifies() {
		t.Fatalf("one 40-word span at 0.87: %+v", d)
	}
}

func TestTopicsScorerKeyCoversTheControlsAndConstants(t *testing.T) {
	key := topicsScorerKey()
	saved := topicScorerV1
	t.Cleanup(func() { topicScorerV1 = saved })
	topicScorerV1.Threshold = 0.86
	if topicsScorerKey() == key {
		t.Fatal("the threshold is not in the scorer key")
	}
	topicScorerV1 = saved
	controls := topicControls
	t.Cleanup(func() { topicControls = controls })
	topicControls = append([]topicControl(nil), controls...)
	topicControls[0].Definition += " "
	if topicsScorerKey() == key {
		t.Fatal("the control definitions are not in the scorer key")
	}
	topicControls = controls
	if topicsScorerKey() != key {
		t.Fatal("the scorer key is not deterministic")
	}
	if topicsLanguageValidated("en", "other-detector") {
		t.Fatal("another detector's language counted as validated")
	}
	// Gate 2 measured English; two Italian meetings validate nothing.
	if !topicsLanguageValidated("en", topicsExpectedDetectorVersion) || topicsLanguageValidated("it", topicsExpectedDetectorVersion) {
		t.Fatal("validated languages: English only")
	}
}

// Each language has both controls, so a passage in it always has a
// same-language control to clear (Gate 2: a margin over English controls
// alone did not hold for an Italian meeting).
func TestEveryControlLanguageHasBothControls(t *testing.T) {
	ids := map[string]bool{}
	langs := map[string]int{}
	for _, c := range topicControls {
		if ids[c.ID] {
			t.Fatalf("duplicate control %s", c.ID)
		}
		ids[c.ID] = true
		kind, lang, _ := strings.Cut(strings.TrimPrefix(c.ID, "control-"), "-")
		if kind != "gardening" && kind != "astronomy" {
			t.Fatalf("unexpected control %s", c.ID)
		}
		langs[lang]++
	}
	for lang, n := range langs {
		if n != 2 {
			t.Fatalf("language %q has %d controls", lang, n)
		}
	}
	if len(langs) < 7 {
		t.Fatalf("languages: %v", langs)
	}
}

// The tuned rule (Gate 2): every supporting word counts, whatever the length
// of its span and however its passages point, and a short discussion
// qualifies on its own only when it is unmistakable.
func TestTheTunedRuleCountsEveryWord(t *testing.T) {
	p := topicScorerV1
	passages := scoredPassages(0.72, 0.5, 0.72, 0.5, 0.72, 0.5, 0.72, 0.5)
	for i := range passages {
		passages[i].WordCount = 20
	}
	if d := scoreTopic(passages, topicX, farControl, p); d.Reason != topicReasonWords {
		t.Fatalf("four 20-word spans make 80 words: %+v", d)
	}
	passages[6].WordCount = 19
	if d := scoreTopic(passages, topicX, farControl, p); d.qualifies() {
		t.Fatalf("79 words: %+v", d)
	}
	if d := scoreTopic(scoredPassages(0.77, 0.5), topicX, farControl, p); d.Reason != topicReasonStrong {
		t.Fatalf("one 40-word span at 0.77: %+v", d)
	}
	if d := scoreTopic(scoredPassages(0.75, 0.5), topicX, farControl, p); d.qualifies() {
		t.Fatalf("one 40-word span at 0.75: %+v", d)
	}
}

// A short strong decision's supporting words and lowest margin come from its
// strong spans only, the ones its evidence counts.
func TestAShortStrongSummaryCountsOnlyItsStrongSpans(t *testing.T) {
	d := topicDecision{Reason: topicReasonStrong, Spans: []topicSpan{
		{WordCount: 45, PeakScore: 0.80, MinScore: 0.77, MinMargin: 0.12},
		{WordCount: 30, PeakScore: 0.70, MinScore: 0.69, MinMargin: 0.05},
	}}
	words, peak, minMargin := topicDecisionSummary(d)
	if words != 45 || peak != 0.80 || minMargin != 0.12 {
		t.Fatalf("words %d peak %v min margin %v", words, peak, minMargin)
	}
	if !d.counts(d.Spans[0]) || d.counts(d.Spans[1]) {
		t.Fatal("the evidence counts other spans than the summary")
	}
}
