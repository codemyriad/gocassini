package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// The topic suggestions scorer (docs/proposals/automatic-tagging, "Scoring"):
// pure Go over passage vectors from `cassini topics embed` and definition
// vectors from `cassini topics embed-texts`. It reads no text.
//
// topicsScorerVersion names every constant below, the two control
// definitions, the validated languages and the expected detector version.
// Changing any of them changes topicsScorerKey, which rescores every meeting
// without embedding it again.
const topicsScorerVersion = 2

// topicScorerParams are the scorer's constants.
type topicScorerParams struct {
	// A passage supports a topic when it has at least MinSupportWords words,
	// its cosine to the definition is at least Threshold, and that cosine
	// minus the best control's is at least Margin.
	Threshold       float64 `json:"threshold"`
	Margin          float64 `json:"margin"`
	MinSupportWords int     `json:"minSupportWords"`
	// Consecutive supporting passages form a span unless the gap between
	// them is over SpanGapMS or their own cosine is under SpanJoinCosine.
	SpanGapMS      int64   `json:"spanGapMs"`
	SpanJoinCosine float64 `json:"spanJoinCosine"`
	// A meeting qualifies on QualifyWords words from spans of at least
	// SubstantialSpanWords words, or on one span of at least StrongSpanWords
	// words whose every passage scores StrongMinScore and StrongMinMargin.
	SubstantialSpanWords int     `json:"substantialSpanWords"`
	QualifyWords         int     `json:"qualifyWords"`
	StrongSpanWords      int     `json:"strongSpanWords"`
	StrongMinScore       float64 `json:"strongMinScore"`
	StrongMinMargin      float64 `json:"strongMinMargin"`
}

// topicScorerV1 holds the prototype's constants. Gate 1 (evaluation.md)
// re-validated Threshold and Margin on int8 vectors.
var topicScorerV1 = topicScorerParams{
	Threshold: 0.85, Margin: 0.05, MinSupportWords: 12,
	SpanGapMS: 2500, SpanJoinCosine: 0.88,
	SubstantialSpanWords: 25, QualifyWords: 80,
	StrongSpanWords: 40, StrongMinScore: 0.88, StrongMinMargin: 0.08,
}

// topicControl is a built-in definition no meeting should be about. A passage
// that is about as close to a control as to a topic is generic talk, not
// evidence for the topic.
type topicControl struct {
	ID         string `json:"id"`
	Definition string `json:"definition"`
}

// topicControls are the prototype's two controls, written in several
// languages. Text in a language scores higher against a definition in the
// same language, so a margin over English controls alone was far too easy to
// clear for an Italian definition on an Italian meeting and far too hard for
// an English one (Gate 2, evaluation.md). The best control is the highest of
// all of them, so the margin follows each passage's language with no
// language logic. Adding languages left the English decisions unchanged.
var topicControls = []topicControl{
	{ID: "control-gardening", Definition: "Growing vegetables, planting flowers, watering gardens, composting, soil fertility and pruning trees."},
	{ID: "control-astronomy", Definition: "Observing stars, planets and galaxies through telescopes, orbital mechanics, astrophysics and astronomical observations."},
	{ID: "control-gardening-it", Definition: "Coltivare ortaggi, piantare fiori, annaffiare il giardino, fare il compost, la fertilità del terreno e la potatura degli alberi."},
	{ID: "control-astronomy-it", Definition: "Osservare stelle, pianeti e galassie con il telescopio, meccanica orbitale, astrofisica e osservazioni astronomiche."},
	{ID: "control-gardening-de", Definition: "Gemüse anbauen, Blumen pflanzen, den Garten gießen, kompostieren, Bodenfruchtbarkeit und Bäume schneiden."},
	{ID: "control-astronomy-de", Definition: "Sterne, Planeten und Galaxien durch Teleskope beobachten, Himmelsmechanik, Astrophysik und astronomische Beobachtungen."},
	{ID: "control-gardening-fr", Definition: "Cultiver des légumes, planter des fleurs, arroser le jardin, faire du compost, la fertilité du sol et la taille des arbres."},
	{ID: "control-astronomy-fr", Definition: "Observer les étoiles, les planètes et les galaxies au télescope, la mécanique orbitale, l'astrophysique et les observations astronomiques."},
	{ID: "control-gardening-es", Definition: "Cultivar hortalizas, plantar flores, regar el jardín, hacer compost, la fertilidad del suelo y la poda de árboles."},
	{ID: "control-astronomy-es", Definition: "Observar estrellas, planetas y galaxias con telescopios, mecánica orbital, astrofísica y observaciones astronómicas."},
	{ID: "control-gardening-pt", Definition: "Cultivar legumes, plantar flores, regar o jardim, fazer compostagem, a fertilidade do solo e a poda de árvores."},
	{ID: "control-astronomy-pt", Definition: "Observar estrelas, planetas e galáxias com telescópios, mecânica orbital, astrofísica e observações astronómicas."},
	{ID: "control-gardening-nl", Definition: "Groenten kweken, bloemen planten, de tuin water geven, composteren, bodemvruchtbaarheid en bomen snoeien."},
	{ID: "control-astronomy-nl", Definition: "Sterren, planeten en sterrenstelsels door telescopen bekijken, hemelmechanica, astrofysica en astronomische waarnemingen."},
}

// topicsExpectedDetectorVersion is the language detector the validated
// languages were measured with (cassini topics embed's header). A meeting
// detected by another version counts as language unknown.
const topicsExpectedDetectorVersion = "whatlanggo-1.0.1/vote-1"

// topicsValidatedLanguages are the detected languages whose suggestions were
// measured against people's labels (Gate 2). None yet: until Gate 2 every
// suggestion carries the "not yet validated" line.
var topicsValidatedLanguages = []string{}

// topicsScorerKey identifies everything that decides a suggestion apart from
// the vectors, so a change to any of it rescores.
func topicsScorerKey() string {
	raw, _ := json.Marshal(struct {
		Version         int               `json:"version"`
		Params          topicScorerParams `json:"params"`
		Controls        []topicControl    `json:"controls"`
		Languages       []string          `json:"languages"`
		DetectorVersion string            `json:"detectorVersion"`
	}{topicsScorerVersion, topicScorerV1, topicControls, topicsValidatedLanguages, topicsExpectedDetectorVersion})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// topicsLanguageValidated says whether a meeting's detected language is one
// the scorer was validated on. Another detector's answer is unknown.
func topicsLanguageValidated(language, detectorVersion string) bool {
	return detectorVersion == topicsExpectedDetectorVersion && slices.Contains(topicsValidatedLanguages, language)
}

// topicPassage is one embedded passage of a meeting, in transcript order.
type topicPassage struct {
	StartMS   int64
	EndMS     int64
	WordCount int
	Vector    []float32
}

// topicSpan is a run of consecutive supporting passages: the evidence for a
// suggestion.
type topicSpan struct {
	StartMS      int64   `json:"startMs"`
	EndMS        int64   `json:"endMs"`
	WordCount    int     `json:"wordCount"`
	FirstPassage int     `json:"firstPassage"`
	LastPassage  int     `json:"lastPassage"`
	PeakScore    float64 `json:"peakScore"`
	MinScore     float64 `json:"minScore"`
	MinMargin    float64 `json:"minMargin"`
}

const (
	topicReasonWords  = "at least 80 supporting words"
	topicReasonStrong = "short strong discussion"
)

// topicDecision is the scorer's answer for one (meeting, topic): every span,
// including those too short to count, and the rule that qualified it, if any.
type topicDecision struct {
	Reason string
	Spans  []topicSpan
}

func (d topicDecision) qualifies() bool { return d.Reason != "" }

// scoreTopic decides one topic for one meeting. controls are the control
// definitions' vectors; at least one is required.
func scoreTopic(passages []topicPassage, topic []float32, controls [][]float32, p topicScorerParams) topicDecision {
	var d topicDecision
	var active []int
	var scores, margins []float64
	flush := func() {
		if len(active) == 0 {
			return
		}
		span := topicSpan{
			StartMS: passages[active[0]].StartMS, EndMS: passages[active[0]].EndMS,
			FirstPassage: active[0], LastPassage: active[len(active)-1],
			PeakScore: scores[0], MinScore: scores[0], MinMargin: margins[0],
		}
		for i, index := range active {
			span.StartMS = min(span.StartMS, passages[index].StartMS)
			span.EndMS = max(span.EndMS, passages[index].EndMS)
			span.WordCount += passages[index].WordCount
			span.PeakScore = max(span.PeakScore, scores[i])
			span.MinScore = min(span.MinScore, scores[i])
			span.MinMargin = min(span.MinMargin, margins[i])
		}
		d.Spans = append(d.Spans, span)
		active, scores, margins = active[:0], scores[:0], margins[:0]
	}
	for i, passage := range passages {
		score := topicCosine(passage.Vector, topic)
		control := topicCosine(passage.Vector, controls[0])
		for _, c := range controls[1:] {
			control = max(control, topicCosine(passage.Vector, c))
		}
		if passage.WordCount < p.MinSupportWords || score < p.Threshold || score-control < p.Margin {
			// A passage that does not support the topic always ends a span.
			flush()
			continue
		}
		if len(active) > 0 {
			prev := passages[active[len(active)-1]]
			if passage.StartMS-prev.EndMS > p.SpanGapMS || topicCosine(passage.Vector, prev.Vector) < p.SpanJoinCosine {
				flush()
			}
		}
		active = append(active, i)
		scores = append(scores, score)
		margins = append(margins, score-control)
	}
	flush()
	d.Reason = topicQualifies(d.Spans, p)
	return d
}

func topicQualifies(spans []topicSpan, p topicScorerParams) string {
	words := 0
	for _, s := range spans {
		if s.WordCount >= p.SubstantialSpanWords {
			words += s.WordCount
		}
	}
	if words >= p.QualifyWords {
		return topicReasonWords
	}
	for _, s := range spans {
		if s.WordCount >= p.StrongSpanWords && s.MinScore >= p.StrongMinScore && s.MinMargin >= p.StrongMinMargin {
			return topicReasonStrong
		}
	}
	return ""
}

// topicCosine is the dot product of two unit vectors, summed in float64. The
// prototype compared float32 scores with the thresholds; the two differ only
// in the last float32 digit, which decided none of Gate 1's 660 decisions on
// fp32 vectors (evaluation.md).
func topicCosine(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
