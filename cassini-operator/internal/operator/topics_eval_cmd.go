package operator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// topicsEvalCommand runs the scorer offline over a folder of `cassini topics
// embed` outputs (docs/proposals/automatic-tagging, "Evaluation plan"). It is
// a development command: nothing in the running operator calls it, and it
// reads and writes only the files it is given.
const topicsEvalCommand = "topics-eval"

// topicsEvalGates are the gates --sweep reports around the default 0.68/0.06
// of recipe r2. Gate 1 swept the prototype's 0.83/0.04 to 0.89/0.08 on r1.
var topicsEvalGates = [][2]float64{{0.64, 0.04}, {0.66, 0.05}, {0.68, 0.06}, {0.70, 0.07}, {0.72, 0.08}}

type topicsEvalMeeting struct {
	ID       string
	Language string
	Passages []topicPassage
	revision string
	recipe   string
}

type topicsEvalReference struct {
	Meetings    []string `json:"meetings"`
	Assignments []struct {
		Meeting string `json:"meeting"`
		Topic   string `json:"topic"`
	} `json:"assignments"`
}

// topicsEvalGateResult is one gate's outcome. Pairs are "meeting\ttopic".
type topicsEvalGateResult struct {
	Threshold   float64        `json:"threshold"`
	Margin      float64        `json:"margin"`
	Assignments int            `json:"assignments"`
	Strong      int            `json:"shortStrongAssignments"`
	PerTopic    map[string]int `json:"perTopic"`
	// Against --reference, over its meetings times every topic.
	Pairs     int      `json:"pairs,omitempty"`
	Agreeing  int      `json:"agreeing,omitempty"`
	Agreement float64  `json:"agreement,omitempty"`
	OnlyHere  []string `json:"onlyHere,omitempty"`
	OnlyThere []string `json:"onlyReference,omitempty"`
	// Against --labels, per detected language and "all".
	Labelled map[string]topicsEvalLabelStats `json:"labelled,omitempty"`
	assigned map[string]bool
}

type topicsEvalReport struct {
	Meetings        int                    `json:"meetings"`
	Passages        int                    `json:"passages"`
	Topics          []string               `json:"topics"`
	ModelRevision   string                 `json:"modelRevision"`
	RecipeVersion   string                 `json:"recipeVersion"`
	ScorerVersion   int                    `json:"scorerVersion"`
	Controls        []string               `json:"controls"`
	Languages       map[string]int         `json:"languages"`
	Gates           []topicsEvalGateResult `json:"gates"`
	ReferenceTopics int                    `json:"referenceAssignments,omitempty"`
	Agreement       *topicsEvalAgreement   `json:"labelAgreement,omitempty"`
}

func runTopicsEval(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(topicsEvalCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	vectorsDir := fs.String("vectors", "", "folder of `cassini topics embed` outputs, one <meeting>.jsonl each")
	definitions := fs.String("definitions", "", "`cassini topics embed-texts` output: every topic and both controls")
	reference := fs.String("reference", "", "assignments to compare with: {\"meetings\": [...], \"assignments\": [{\"meeting\", \"topic\"}]}")
	labels := fs.String("labels", "", "people's labels, JSON lines {\"meeting\", \"topic\", \"labeller\", \"inTopic\"}: agreement, precision and estimated recall per language")
	sweep := fs.Bool("sweep", false, "also report the gates 0.64/0.04, 0.66/0.05, 0.70/0.07 and 0.72/0.08")
	out := fs.String("out", "", "write the full report, with per-pair differences, here")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *vectorsDir == "" || *definitions == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: cassini-operator topics-eval --vectors <dir> --definitions <file> [--reference <file>] [--labels <file>] [--sweep] [--out <file>]")
		return 2
	}
	report, err := topicsEval(*vectorsDir, *definitions, *reference, *labels, *sweep)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", topicsEvalCommand, err)
		return 1
	}
	if *out != "" {
		raw, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(*out, append(raw, '\n'), 0o600); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", topicsEvalCommand, err)
			return 1
		}
	}
	// Stdout carries aggregates only: no meeting ids.
	summary := report
	summary.Gates = nil
	for _, g := range report.Gates {
		g.OnlyHere, g.OnlyThere = nil, nil
		summary.Gates = append(summary.Gates, g)
	}
	raw, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Fprintln(stdout, string(raw))
	return 0
}

func topicsEval(vectorsDir, definitionsPath, referencePath, labelsPath string, sweep bool) (topicsEvalReport, error) {
	defs, defRevision, defRecipe, err := readTopicsDefinitions(definitionsPath)
	if err != nil {
		return topicsEvalReport{}, err
	}
	report := topicsEvalReport{ScorerVersion: topicsScorerVersion, Languages: map[string]int{}}
	// The controls the file has: at least the English pair. Files embedded
	// before the controls were multilingual have only that.
	var controls [][]float32
	for _, c := range topicControls {
		v, ok := defs[c.ID]
		if !ok {
			continue
		}
		controls = append(controls, v)
		report.Controls = append(report.Controls, c.ID)
		delete(defs, c.ID)
	}
	for _, required := range []string{"control-gardening", "control-astronomy"} {
		if !slices.Contains(report.Controls, required) {
			return topicsEvalReport{}, fmt.Errorf("%s has no vector for the control %s", definitionsPath, required)
		}
	}
	for id, v := range defs {
		if len(v) != len(controls[0]) {
			return topicsEvalReport{}, fmt.Errorf("%s: %s has %d dimensions, the controls %d", definitionsPath, id, len(v), len(controls[0]))
		}
	}
	var topicIDs []string
	for id := range defs {
		topicIDs = append(topicIDs, id)
	}
	sort.Strings(topicIDs)
	paths, err := filepath.Glob(filepath.Join(vectorsDir, "*.jsonl"))
	if err != nil {
		return topicsEvalReport{}, err
	}
	sort.Strings(paths)
	report.Topics, report.ModelRevision, report.RecipeVersion = topicIDs, defRevision, defRecipe
	var meetings []topicsEvalMeeting
	for _, path := range paths {
		m, err := readTopicsPassagesFile(path)
		if err != nil {
			return topicsEvalReport{}, err
		}
		// Passages and definitions must share one vector space.
		if m.revision != defRevision || m.recipe != defRecipe {
			return topicsEvalReport{}, fmt.Errorf("%s was embedded with %s/%s, the definitions with %s/%s", path, m.revision, m.recipe, defRevision, defRecipe)
		}
		for _, p := range m.Passages {
			if len(p.Vector) != len(controls[0]) {
				return topicsEvalReport{}, fmt.Errorf("%s: a passage has %d dimensions, the definitions %d", path, len(p.Vector), len(controls[0]))
			}
		}
		meetings = append(meetings, m)
		report.Passages += len(m.Passages)
		report.Languages[m.Language]++
	}
	report.Meetings = len(meetings)
	if len(meetings) == 0 {
		return topicsEvalReport{}, fmt.Errorf("no *.jsonl files in %s", vectorsDir)
	}
	var ref *topicsEvalReference
	if referencePath != "" {
		raw, err := os.ReadFile(referencePath)
		if err != nil {
			return topicsEvalReport{}, err
		}
		ref = &topicsEvalReference{}
		if err := json.Unmarshal(raw, ref); err != nil {
			return topicsEvalReport{}, fmt.Errorf("%s: %v", referencePath, err)
		}
		if err := checkTopicsEvalReference(ref, meetings, topicIDs); err != nil {
			return topicsEvalReport{}, fmt.Errorf("%s: %v", referencePath, err)
		}
		report.ReferenceTopics = len(ref.Assignments)
	}
	var truth map[[2]string]bool
	var pairs []topicsEvalPair
	if labelsPath != "" {
		labels, err := readTopicsEvalLabels(labelsPath, meetings, topicIDs)
		if err != nil {
			return topicsEvalReport{}, err
		}
		var agreement topicsEvalAgreement
		truth, agreement = topicsEvalTruth(labels)
		report.Agreement = &agreement
		for _, m := range meetings {
			for _, topic := range topicIDs {
				pairs = append(pairs, topicsEvalPair{Meeting: m.ID, Topic: topic, Language: m.Language, Band: topicsEvalBand(m.Passages, defs[topic])})
			}
		}
	}
	gates := [][2]float64{{topicScorerV1.Threshold, topicScorerV1.Margin}}
	if sweep {
		for _, g := range topicsEvalGates {
			if g != gates[0] {
				gates = append(gates, g)
			}
		}
	}
	for _, gate := range gates {
		p := topicScorerV1
		p.Threshold, p.Margin = gate[0], gate[1]
		result := topicsEvalGateResult{Threshold: gate[0], Margin: gate[1], PerTopic: map[string]int{}, assigned: map[string]bool{}}
		for _, m := range meetings {
			for _, topic := range topicIDs {
				d := scoreTopic(m.Passages, defs[topic], controls, p)
				if !d.qualifies() {
					continue
				}
				pair := m.ID + "\t" + topic
				result.assigned[pair] = true
				result.PerTopic[topic]++
				if d.Reason == topicReasonStrong {
					result.Strong++
				}
			}
		}
		if truth != nil {
			result.Labelled = topicsEvalLabelled(pairs, result.assigned, truth)
		}
		if ref != nil {
			compareTopicsEval(&result, ref, topicIDs)
		} else {
			result.Assignments = len(result.assigned)
		}
		report.Gates = append(report.Gates, result)
	}
	return report, nil
}

// checkTopicsEvalReference refuses a reference the vectors and definitions do
// not cover. A meeting without a vectors file would score no topic and so
// agree with every negative, and an assignment to a topic without a
// definition would never be compared.
func checkTopicsEvalReference(ref *topicsEvalReference, meetings []topicsEvalMeeting, topicIDs []string) error {
	loaded := map[string]bool{}
	for _, m := range meetings {
		loaded[m.ID] = true
	}
	listed := map[string]bool{}
	for _, meeting := range ref.Meetings {
		if !loaded[meeting] {
			return fmt.Errorf("meeting %s has no vectors file", meeting)
		}
		listed[meeting] = true
	}
	for _, a := range ref.Assignments {
		if !listed[a.Meeting] {
			return fmt.Errorf("an assignment names meeting %s, which is not in its meetings", a.Meeting)
		}
		if !slices.Contains(topicIDs, a.Topic) {
			return fmt.Errorf("an assignment names topic %s, which has no definition", a.Topic)
		}
	}
	return nil
}

// compareTopicsEval counts agreement over the reference's meetings times
// every topic; Assignments then counts only those meetings.
func compareTopicsEval(result *topicsEvalGateResult, ref *topicsEvalReference, topicIDs []string) {
	want := map[string]bool{}
	for _, a := range ref.Assignments {
		want[a.Meeting+"\t"+a.Topic] = true
	}
	perTopic := map[string]int{}
	for _, meeting := range ref.Meetings {
		for _, topic := range topicIDs {
			pair := meeting + "\t" + topic
			got := result.assigned[pair]
			result.Pairs++
			if got {
				result.Assignments++
				perTopic[topic]++
			}
			switch {
			case got == want[pair]:
				result.Agreeing++
			case got:
				result.OnlyHere = append(result.OnlyHere, pair)
			default:
				result.OnlyThere = append(result.OnlyThere, pair)
			}
		}
	}
	result.PerTopic = perTopic
	if result.Pairs > 0 {
		result.Agreement = float64(result.Agreeing) / float64(result.Pairs)
	}
}

func readTopicsDefinitions(path string) (map[string][]float32, string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	var header struct {
		Format        string `json:"format"`
		ModelRevision string `json:"modelRevision"`
		RecipeVersion string `json:"recipeVersion"`
	}
	defs := map[string][]float32{}
	for i := 0; scanner.Scan(); i++ {
		if i == 0 {
			if err := json.Unmarshal(scanner.Bytes(), &header); err != nil || header.Format != "cassini.topic-text-vectors.v1" {
				return nil, "", "", fmt.Errorf("%s is not a cassini.topic-text-vectors.v1 file", path)
			}
			continue
		}
		var line struct {
			ID     string    `json:"id"`
			Vector []float32 `json:"vector"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, "", "", fmt.Errorf("%s line %d: %v", path, i+1, err)
		}
		defs[line.ID] = line.Vector
	}
	return defs, header.ModelRevision, header.RecipeVersion, scanner.Err()
}

func readTopicsPassagesFile(path string) (topicsEvalMeeting, error) {
	f, err := os.Open(path)
	if err != nil {
		return topicsEvalMeeting{}, err
	}
	defer f.Close()
	m := topicsEvalMeeting{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	var header struct {
		Format          string `json:"format"`
		ModelRevision   string `json:"modelRevision"`
		RecipeVersion   string `json:"recipeVersion"`
		Count           int    `json:"count"`
		Language        string `json:"language"`
		DetectorVersion string `json:"detectorVersion"`
	}
	for i := 0; scanner.Scan(); i++ {
		if i == 0 {
			if err := json.Unmarshal(scanner.Bytes(), &header); err != nil || header.Format != "cassini.topic-passages.v1" {
				return m, fmt.Errorf("%s is not a cassini.topic-passages.v1 file", path)
			}
			continue
		}
		var line struct {
			StartMS   int64     `json:"startMs"`
			EndMS     int64     `json:"endMs"`
			WordCount int       `json:"wordCount"`
			Vector    []float32 `json:"vector"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return m, fmt.Errorf("%s line %d: %v", path, i+1, err)
		}
		m.Passages = append(m.Passages, topicPassage{StartMS: line.StartMS, EndMS: line.EndMS, WordCount: line.WordCount, Vector: line.Vector})
	}
	if err := scanner.Err(); err != nil {
		return m, err
	}
	if len(m.Passages) != header.Count {
		return m, fmt.Errorf("%s: %d passages, the header says %d", path, len(m.Passages), header.Count)
	}
	m.revision, m.recipe = header.ModelRevision, header.RecipeVersion
	m.Language = header.Language
	if header.DetectorVersion != topicsExpectedDetectorVersion {
		m.Language = "unknown"
	}
	return m, nil
}
