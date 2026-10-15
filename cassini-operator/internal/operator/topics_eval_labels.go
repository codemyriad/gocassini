package operator

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
)

// Gate 2 (docs/proposals/automatic-tagging, "Evaluation plan"): people's
// labels on a sample of (meeting, topic) pairs, stratified by score band.
// topics-eval --labels reports labeller agreement, then precision and
// estimated recall per detected language: each band's labelled pairs stand
// for every pair of the corpus in that band.

// topicsEvalBandWidth is the width of a score band: pairs are grouped by the
// best cosine any of the meeting's passages has to the topic.
const topicsEvalBandWidth = 0.02

// topicsEvalLabel is one line of a labels file: one labeller's answer on
// whether a meeting is about a topic.
type topicsEvalLabel struct {
	Meeting  string `json:"meeting"`
	Topic    string `json:"topic"`
	Labeller string `json:"labeller"`
	InTopic  *bool  `json:"inTopic"`
}

// topicsEvalAgreement is how far the labellers agree.
type topicsEvalAgreement struct {
	Labellers int `json:"labellers"`
	Labels    int `json:"labels"`
	Pairs     int `json:"pairs"`
	// Pairs with two or more labels, and those whose labels all agree.
	MultiLabelled int     `json:"multiLabelled"`
	Agreeing      int     `json:"agreeing"`
	Agreement     float64 `json:"agreement"`
	// Cohen's kappa between the two labellers with the most labels, over
	// the pairs both labelled; null when it cannot be computed.
	Kappa *float64 `json:"kappa"`
	// Pairs the labellers disagree on: left out of precision and recall.
	Disputed int `json:"disputed"`
}

// topicsEvalLabelStats is one gate's outcome against the labels, for one
// detected language (or "all").
type topicsEvalLabelStats struct {
	// Labelled pairs used (undisputed), how many people said are in the
	// topic, how many the gate assigns, and how many of those are right.
	Labelled         int `json:"labelled"`
	Positive         int `json:"positive"`
	Assigned         int `json:"assigned"`
	AssignedAndRight int `json:"assignedAndPositive"`
	// Over the labelled pairs as they are.
	SamplePrecision *float64 `json:"samplePrecision"`
	SampleRecall    *float64 `json:"sampleRecall"`
	// Weighted to every pair of the corpus by score band.
	Precision *float64 `json:"precision"`
	Recall    *float64 `json:"recall"`
	// Corpus pairs in bands with no labelled pair on their side of the gate:
	// they could not be estimated and are left out of both.
	UnestimatedPairs int `json:"unestimatedPairs"`
}

// topicsEvalPair is one (meeting, topic) pair of the corpus.
type topicsEvalPair struct {
	Meeting, Topic, Language string
	Band                     int
}

func readTopicsEvalLabels(path string, meetings []topicsEvalMeeting, topicIDs []string) ([]topicsEvalLabel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	loaded := map[string]bool{}
	for _, m := range meetings {
		loaded[m.ID] = true
	}
	var labels []topicsEvalLabel
	seen := map[[3]string]bool{}
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var l topicsEvalLabel
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			return nil, fmt.Errorf("%s line %d: %v", path, line, err)
		}
		switch {
		case l.Meeting == "" || l.Topic == "" || l.Labeller == "" || l.InTopic == nil:
			return nil, fmt.Errorf("%s line %d: meeting, topic, labeller and inTopic are required", path, line)
		case !loaded[l.Meeting]:
			return nil, fmt.Errorf("%s line %d: meeting %s has no vectors file", path, line, l.Meeting)
		case !slices.Contains(topicIDs, l.Topic):
			return nil, fmt.Errorf("%s line %d: topic %s has no definition", path, line, l.Topic)
		}
		key := [3]string{l.Meeting, l.Topic, l.Labeller}
		if seen[key] {
			return nil, fmt.Errorf("%s line %d: %s labelled %s/%s twice", path, line, l.Labeller, l.Meeting, l.Topic)
		}
		seen[key] = true
		labels = append(labels, l)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("%s has no labels", path)
	}
	return labels, nil
}

// topicsEvalTruth reduces labels to one answer per pair, where the
// labellers agree, and measures their agreement.
func topicsEvalTruth(labels []topicsEvalLabel) (map[[2]string]bool, topicsEvalAgreement) {
	byPair := map[[2]string]map[string]bool{}
	perLabeller := map[string]int{}
	for _, l := range labels {
		pair := [2]string{l.Meeting, l.Topic}
		if byPair[pair] == nil {
			byPair[pair] = map[string]bool{}
		}
		byPair[pair][l.Labeller] = *l.InTopic
		perLabeller[l.Labeller]++
	}
	a := topicsEvalAgreement{Labellers: len(perLabeller), Labels: len(labels), Pairs: len(byPair)}
	truth := map[[2]string]bool{}
	for pair, answers := range byPair {
		yes := 0
		for _, v := range answers {
			if v {
				yes++
			}
		}
		agree := yes == 0 || yes == len(answers)
		if len(answers) > 1 {
			a.MultiLabelled++
			if agree {
				a.Agreeing++
			}
		}
		if !agree {
			a.Disputed++
			continue
		}
		truth[pair] = yes > 0
	}
	if a.MultiLabelled > 0 {
		a.Agreement = float64(a.Agreeing) / float64(a.MultiLabelled)
	}
	// Kappa between the two labellers with the most labels.
	names := make([]string, 0, len(perLabeller))
	for name := range perLabeller {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if perLabeller[names[i]] != perLabeller[names[j]] {
			return perLabeller[names[i]] > perLabeller[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) >= 2 {
		var both, same, yesA, yesB float64
		for _, answers := range byPair {
			x, okA := answers[names[0]]
			y, okB := answers[names[1]]
			if !okA || !okB {
				continue
			}
			both++
			if x == y {
				same++
			}
			if x {
				yesA++
			}
			if y {
				yesB++
			}
		}
		if both > 0 {
			observed := same / both
			chance := (yesA/both)*(yesB/both) + (1-yesA/both)*(1-yesB/both)
			if chance < 1 {
				k := (observed - chance) / (1 - chance)
				a.Kappa = &k
			}
		}
	}
	return truth, a
}

// topicsEvalBand is a pair's score band: the best cosine of any passage of
// the meeting to the topic, in bands of topicsEvalBandWidth.
func topicsEvalBand(passages []topicPassage, topic []float32) int {
	best := -1.0
	for _, p := range passages {
		best = max(best, topicCosine(p.Vector, topic))
	}
	return int(math.Floor(best / topicsEvalBandWidth))
}

// topicsEvalLabelled scores one gate against the labels, per language and
// overall. assigned is the gate's (meeting\ttopic) set over the corpus.
func topicsEvalLabelled(pairs []topicsEvalPair, assigned map[string]bool, truth map[[2]string]bool) map[string]topicsEvalLabelStats {
	type cell struct{ corpus, labelled, positive int }
	// Per language, band and side of the gate.
	type key struct {
		lang     string
		band     int
		assigned bool
	}
	cells := map[key]*cell{}
	out := map[string]topicsEvalLabelStats{}
	add := func(k key, labelled, positive bool) {
		for _, lang := range []string{k.lang, "all"} {
			kk := key{lang, k.band, k.assigned}
			c := cells[kk]
			if c == nil {
				c = &cell{}
				cells[kk] = c
			}
			c.corpus++
			if labelled {
				c.labelled++
				if positive {
					c.positive++
				}
			}
		}
	}
	for _, p := range pairs {
		got := assigned[p.Meeting+"\t"+p.Topic]
		positive, labelled := truth[[2]string{p.Meeting, p.Topic}]
		add(key{p.Language, p.Band, got}, labelled, positive)
		if !labelled {
			continue
		}
		for _, lang := range []string{p.Language, "all"} {
			s := out[lang]
			s.Labelled++
			if positive {
				s.Positive++
			}
			if got {
				s.Assigned++
				if positive {
					s.AssignedAndRight++
				}
			}
			out[lang] = s
		}
	}
	ratio := func(a, b float64) *float64 {
		if b == 0 {
			return nil
		}
		r := a / b
		return &r
	}
	for lang, s := range out {
		s.SamplePrecision = ratio(float64(s.AssignedAndRight), float64(s.Assigned))
		s.SampleRecall = ratio(float64(s.AssignedAndRight), float64(s.Positive))
		var tp, assignedCorpus, fn float64
		for k, c := range cells {
			if k.lang != lang {
				continue
			}
			if c.labelled == 0 {
				s.UnestimatedPairs += c.corpus
				continue
			}
			share := float64(c.positive) / float64(c.labelled) * float64(c.corpus)
			if k.assigned {
				tp += share
				assignedCorpus += float64(c.corpus)
			} else {
				fn += share
			}
		}
		s.Precision = ratio(tp, assignedCorpus)
		s.Recall = ratio(tp, tp+fn)
		out[lang] = s
	}
	return out
}
