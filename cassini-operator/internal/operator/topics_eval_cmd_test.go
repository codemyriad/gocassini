package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTopicsEvalFixture writes synthetic embed outputs: 3-D vectors, a topic
// "build" on the x axis, "travel" on y, and both controls on -z.
func writeTopicsEvalFixture(t *testing.T) (vectors, definitions string) {
	t.Helper()
	dir := t.TempDir()
	vectors = filepath.Join(dir, "vectors")
	if err := os.Mkdir(vectors, 0o755); err != nil {
		t.Fatal(err)
	}
	header := func(count int) map[string]any {
		return map[string]any{"format": "cassini.topic-passages.v1", "modelRevision": "m@r", "recipeVersion": "r1", "count": count,
			"language": "en", "detectorVersion": topicsExpectedDetectorVersion}
	}
	// At angle a from the x axis, in the x-y plane.
	vec := func(a float64) []float32 { return []float32{float32(math.Cos(a)), float32(math.Sin(a)), 0} }
	write := func(path string, lines ...any) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, l := range lines {
			_ = enc.Encode(l)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	passage := func(i int, words int, v []float32) map[string]any {
		return map[string]any{"ordinal": i, "startMs": i * 1000, "endMs": (i + 1) * 1000, "wordCount": words, "vector": v}
	}
	// Meeting A: 80 words close to "build". Meeting B: 80 words loosely near
	// "build" (0.86), in at 0.85 and out at 0.87, then 40 words exactly on
	// "travel": a short strong discussion at every gate.
	write(filepath.Join(vectors, "meeting-a.jsonl"), header(2), passage(0, 40, vec(0.1)), passage(1, 40, vec(0.1)))
	write(filepath.Join(vectors, "meeting-b.jsonl"), header(3), passage(0, 40, vec(math.Acos(0.86))), passage(1, 40, vec(math.Acos(0.86))), passage(2, 40, vec(math.Pi/2)))
	definitions = filepath.Join(dir, "definitions.jsonl")
	write(definitions,
		map[string]any{"format": "cassini.topic-text-vectors.v1", "modelRevision": "m@r", "recipeVersion": "r1", "count": 4},
		map[string]any{"id": "build", "vector": []float32{1, 0, 0}},
		map[string]any{"id": "travel", "vector": []float32{0, 1, 0}},
		map[string]any{"id": "control-gardening", "vector": []float32{0, 0, -1}},
		map[string]any{"id": "control-astronomy", "vector": []float32{0, 0, -1}},
	)
	return vectors, definitions
}

func TestTopicsEvalSweepsAndComparesWithAReference(t *testing.T) {
	vectors, definitions := writeTopicsEvalFixture(t)
	reference := filepath.Join(t.TempDir(), "reference.json")
	ref := `{"meetings":["meeting-a","meeting-b"],"assignments":[{"meeting":"meeting-a","topic":"build"},{"meeting":"meeting-b","topic":"travel"}]}`
	if err := os.WriteFile(reference, []byte(ref), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{topicsEvalCommand, "--vectors", vectors, "--definitions", definitions, "--reference", reference, "--sweep", "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "meeting-") {
		t.Fatalf("stdout names meetings: %s", stdout.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var report topicsEvalReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Meetings != 2 || report.Passages != 5 || strings.Join(report.Topics, ",") != "build,travel" || report.Languages["en"] != 2 || len(report.Gates) != 4 {
		t.Fatalf("report %+v", report)
	}
	byGate := map[float64]topicsEvalGateResult{}
	for _, g := range report.Gates {
		byGate[g.Threshold] = g
	}
	// 0.85 (the default, reported first): A/build, B/build and B/travel. The
	// reference has A/build and B/travel: 3 of 4 pairs agree.
	if g := report.Gates[0]; g.Threshold != 0.85 || g.Assignments != 3 || g.Strong != 1 || g.Pairs != 4 || g.Agreeing != 3 ||
		strings.Join(g.OnlyHere, ",") != "meeting-b\tbuild" || len(g.OnlyThere) != 0 || g.PerTopic["build"] != 2 {
		t.Fatalf("0.85 gate %+v", g)
	}
	if g := byGate[0.87]; g.Assignments != 2 || g.Agreeing != 4 || g.Agreement != 1 {
		t.Fatalf("0.87 gate %+v", g)
	}
}

func TestTopicsEvalRefusesMixedVectorSpaces(t *testing.T) {
	vectors, definitions := writeTopicsEvalFixture(t)
	raw, _ := os.ReadFile(definitions)
	if err := os.WriteFile(definitions, bytes.Replace(raw, []byte(`"m@r"`), []byte(`"m@other"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{topicsEvalCommand, "--vectors", vectors, "--definitions", definitions}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "embedded with") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	// A definitions file without the controls cannot score.
	if err := os.WriteFile(definitions, []byte(`{"format":"cassini.topic-text-vectors.v1","modelRevision":"m@r","recipeVersion":"r1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), []string{topicsEvalCommand, "--vectors", vectors, "--definitions", definitions}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "control") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

// A reference the inputs do not cover is refused, not scored: a meeting
// without vectors would agree with every negative, and an assignment to an
// undefined topic would never be compared.
func TestTopicsEvalRefusesAReferenceTheInputsDoNotCover(t *testing.T) {
	vectors, definitions := writeTopicsEvalFixture(t)
	for _, tc := range []struct{ name, ref, want string }{
		{"meeting without vectors", `{"meetings":["meeting-a","meeting-c"],"assignments":[]}`, "meeting-c has no vectors file"},
		{"undefined topic", `{"meetings":["meeting-a"],"assignments":[{"meeting":"meeting-a","topic":"cooking"}]}`, "topic cooking"},
		{"assignment outside the meetings", `{"meetings":["meeting-a"],"assignments":[{"meeting":"meeting-b","topic":"build"}]}`, "meeting-b, which is not in its meetings"},
	} {
		reference := filepath.Join(t.TempDir(), "reference.json")
		if err := os.WriteFile(reference, []byte(tc.ref), 0o644); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{topicsEvalCommand, "--vectors", vectors, "--definitions", definitions, "--reference", reference}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), tc.want) {
			t.Fatalf("%s: code %d stderr %q, want %q", tc.name, code, stderr.String(), tc.want)
		}
	}
}

func TestTopicsEvalReportsLabelAgreementAndPrecisionPerLanguage(t *testing.T) {
	vectors, definitions := writeTopicsEvalFixture(t)
	labels := filepath.Join(t.TempDir(), "labels.jsonl")
	lines := ""
	for _, l := range []struct {
		meeting, topic string
		alice, bob     bool
	}{{"meeting-a", "build", true, true}, {"meeting-b", "build", true, false}, {"meeting-b", "travel", true, true}, {"meeting-a", "travel", false, false}} {
		lines += fmt.Sprintf(`{"meeting":%q,"topic":%q,"labeller":"alice","inTopic":%v}`+"\n", l.meeting, l.topic, l.alice)
		lines += fmt.Sprintf(`{"meeting":%q,"topic":%q,"labeller":"bob","inTopic":%v}`+"\n", l.meeting, l.topic, l.bob)
	}
	os.WriteFile(labels, []byte(lines), 0o644)
	report, err := topicsEval(vectors, definitions, "", labels, false)
	if err != nil {
		t.Fatal(err)
	}
	a := report.Agreement
	if a == nil || a.Labellers != 2 || a.MultiLabelled != 4 || a.Agreeing != 3 || a.Disputed != 1 || a.Kappa == nil || math.Abs(*a.Kappa-0.5) > 1e-9 {
		t.Fatalf("agreement %+v", a)
	}
	// At 0.85: the two undisputed positives are assigned, the negative is
	// not. The disputed pair is in a band with no other labelled pair.
	en := report.Gates[0].Labelled["en"]
	if en.Labelled != 3 || en.Positive != 2 || en.Assigned != 2 || *en.Precision != 1 || *en.Recall != 1 || en.UnestimatedPairs != 1 {
		t.Fatalf("en %+v", en)
	}
	if _, err := topicsEval(vectors, definitions, "", writeLabels(t, `{"meeting":"meeting-z","topic":"build","labeller":"a","inTopic":true}`), false); err == nil {
		t.Fatal("a label on a meeting without vectors is refused")
	}
	if _, err := topicsEval(vectors, definitions, "", writeLabels(t, `{"meeting":"meeting-a","topic":"build","labeller":"a"}`), false); err == nil {
		t.Fatal("a label without an answer is refused")
	}
}

func writeLabels(t *testing.T, content string) string {
	path := filepath.Join(t.TempDir(), "labels.jsonl")
	os.WriteFile(path, []byte(content+"\n"), 0o644)
	return path
}

func TestLabelledPairsStandForTheirScoreBand(t *testing.T) {
	var pairs []topicsEvalPair
	truth := map[[2]string]bool{}
	assigned := map[string]bool{}
	// Below the gate, band 45: ten pairs, two labelled, one of them in the
	// topic, so about five missed. Above, band 50: four pairs, two labelled,
	// both right.
	for i := range 10 {
		pairs = append(pairs, topicsEvalPair{Meeting: fmt.Sprintf("low%d", i), Topic: "t", Language: "it", Band: 45})
	}
	truth[[2]string{"low0", "t"}], truth[[2]string{"low1", "t"}] = true, false
	for i := range 4 {
		name := fmt.Sprintf("high%d", i)
		pairs = append(pairs, topicsEvalPair{Meeting: name, Topic: "t", Language: "it", Band: 50})
		assigned[name+"\tt"] = true
	}
	truth[[2]string{"high0", "t"}], truth[[2]string{"high1", "t"}] = true, true
	it := topicsEvalLabelled(pairs, assigned, truth)["it"]
	if *it.SampleRecall != 2.0/3 || math.Abs(*it.Recall-4.0/9) > 1e-9 || *it.Precision != 1 || it.UnestimatedPairs != 0 {
		t.Fatalf("%+v recall %v", it, *it.Recall)
	}
}
