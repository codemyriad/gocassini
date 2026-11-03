package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

// gateVector points at the topic [1,0,0,0] with cosine c and away from the
// test control [0,0,0,1], so its margin is c too.
func gateVector(c float64) []float32 {
	return []float32{float32(c), float32(math.Sqrt(1 - c*c)), 0, 0}
}

func gateSample(meeting string, c float64, positive bool) topicGateSample {
	return topicGateSample{Meeting: meeting, Positive: positive, Passages: []topicPassage{{StartMS: 0, EndMS: 60000, WordCount: 120, Vector: gateVector(c)}}}
}

var gateTopic, gateControls = []float32{1, 0, 0, 0}, [][]float32{{0, 0, 0, 1}}

func TestAGateFitsRejectionsAboveTheGlobalGate(t *testing.T) {
	var samples []topicGateSample
	for i := range 22 {
		samples = append(samples, gateSample(fmt.Sprintf("p%d", i), 0.75, true))
	}
	for i := range 8 {
		samples = append(samples, gateSample(fmt.Sprintf("n%d", i), 0.705, false))
	}
	gate, ok := fitTopicGate(samples, gateTopic, gateControls)
	// Every gate from 0.71 to 0.75 decides all; the one nearest 0.68/0.06 wins.
	if !ok || gate.Threshold != 0.71 || gate.Margin != 0.06 || gate.HeldOutAccuracy != 1 || gate.GlobalAccuracy != 22.0/30 || gate.Rejections != 8 {
		t.Fatalf("%+v %v", gate, ok)
	}
}

func TestAGateNeedsToBeatTheGlobalOneOnHeldOutMeetings(t *testing.T) {
	// Verdicts the score does not explain: confirms and rejections at the
	// same scores. Any fit is noise, and leaving meetings out shows it.
	var samples []topicGateSample
	for i := range 24 {
		c := []float64{0.705, 0.725, 0.745}[i%3]
		samples = append(samples, gateSample(fmt.Sprintf("m%d", i), c, i%2 == 0))
	}
	if gate, ok := fitTopicGate(samples, gateTopic, gateControls); ok {
		t.Fatalf("a fit on noise was kept: %+v", gate)
	}
}

func TestAGateLeavesOutEveryVerdictOfAMeetingTogether(t *testing.T) {
	// Each held-out meeting is the only one rejected at its score: without
	// it, nothing argues for a higher gate, so a per-verdict fit would look
	// right only by seeing the meeting's twin verdict.
	var samples []topicGateSample
	for i := range 20 {
		samples = append(samples, gateSample(fmt.Sprintf("p%d", i), 0.78, true))
	}
	for i := range 3 {
		s := gateSample(fmt.Sprintf("n%d", i), 0.705+float64(i)*0.02, false)
		samples = append(samples, s, s)
	}
	gate, _ := fitTopicGate(samples, gateTopic, gateControls)
	if gate.HeldOutAccuracy >= 1 {
		t.Fatalf("held-out accuracy must count each meeting out of its own fit: %+v", gate)
	}
}

type gateFixture struct {
	s   *topicsService
	v   topicVersions
	sha string
}

func newGateFixture(t *testing.T) gateFixture {
	t.Helper()
	s, _ := newTopicsTestService(t)
	embedTopicTestControls(t, s)
	f := gateFixture{s: s, v: topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}, sha: topicDefinitionSHA("Exes.")}
	if _, err := s.store.db.Exec(`INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,status,origin,revision,created_by,created_at,updated_by,updated_at) VALUES('tag_x','X','x','Exes.',?,'active','manual',1,'a',1,'a',1)`, f.sha); err != nil {
		t.Fatal(err)
	}
	if err := s.store.putTextVector(context.Background(), topicTextDefinition, f.sha, f.v, gateTopic); err != nil {
		t.Fatal(err)
	}
	return f
}

// seed adds a checked meeting whose one passage scores c, with a verdict of
// the given source unless verdict is "". withVectors false leaves its vectors
// out, as right after a model change.
func (f gateFixture) seed(t *testing.T, name string, c float64, verdict, source string, withVectors bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.store.db.Exec(`INSERT INTO topic_meeting(opus_name,job_id,state,priority,embed_key,model_revision,recipe_version,chunker_version,queued_at) VALUES(?,?,'done',0,?,?,?,?,1)`,
		name, meetingStem(name), "k"+name, f.v.ModelRevision, f.v.RecipeVersion, f.v.ChunkerVersion); err != nil {
		t.Fatal(err)
	}
	if withVectors {
		f.addVectors(t, name, c)
	}
	if verdict != "" {
		f.s.store.db.Exec(`INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,at,definition_sha,scorer_key) VALUES(?,'tag_x',?,?,1,?,?)`, name, verdict, source, f.sha, topicsScorerKey())
	}
	_ = ctx
}

func (f gateFixture) addVectors(t *testing.T, name string, c float64) {
	t.Helper()
	set := topicPassageSet{EmbedKey: "k" + name, ModelRevision: f.v.ModelRevision, ChunkerVersion: f.v.ChunkerVersion, RecipeVersion: f.v.RecipeVersion,
		Passages: []topicStoredPassage{{topicPassage: topicPassage{StartMS: 0, EndMS: 60000, WordCount: 120, Vector: gateVector(c)}}}}
	if err := f.s.vectors.replaceMeeting(context.Background(), name, set); err != nil {
		t.Fatal(err)
	}
}

func (f gateFixture) gate(t *testing.T) (topicGate, bool) {
	t.Helper()
	gates, err := topicGates(context.Background(), f.s.store.db)
	if err != nil {
		t.Fatal(err)
	}
	g, ok := gates["tag_x"]
	return g, ok
}

// expire lets the next pass refit at once.
func (f gateFixture) expire(t *testing.T) {
	t.Helper()
	g, _ := f.gate(t)
	g.FittedAt = 0
	raw, _ := json.Marshal(g)
	f.s.store.db.Exec(`UPDATE topic_meta SET value=? WHERE key=?`, string(raw), topicGateMetaPrefix+"tag_x")
}

func TestFittedGatesRescoreTheirTopicAndLapseWithTheDefinition(t *testing.T) {
	f := newGateFixture(t)
	s, v, ctx := f.s, f.v, context.Background()
	seed := func(name string, c float64, verdict string) { f.seed(t, name, c, verdict, "review", true) }
	for i := range 22 {
		seed(fmt.Sprintf("p%d.opus", i), 0.75, "confirmed")
	}
	for i := range 8 {
		seed(fmt.Sprintf("n%d.opus", i), 0.705, "rejected")
	}
	// A meeting no one has looked at, just above the global gate.
	seed("open.opus", 0.705, "")
	s.queueRescore(ctx, "tag_x")
	if err := s.rescorePending(ctx, v); err != nil {
		t.Fatal(err)
	}
	suggestion := func() (string, string) {
		var state, key string
		s.store.db.QueryRow(`SELECT state,scorer_key FROM topic_suggestion WHERE opus_name='open.opus' AND tag_id='tag_x'`).Scan(&state, &key)
		return state, key
	}
	if state, key := suggestion(); state != "pending" || key != topicsScorerKey() {
		t.Fatalf("under the global gate: %s %s", state, key)
	}
	if err := s.fitGates(ctx, v); err != nil {
		t.Fatal(err)
	}
	if queue, _ := s.store.rescoreQueue(ctx); queue["tag_x"] == 0 {
		t.Fatal("a fitted gate queues its topic's rescore")
	}
	inputs, _, _ := s.store.scoringInputs(ctx, v)
	fitted := inputs.Topics[0]
	if fitted.Params.Threshold != 0.71 || fitted.ScorerKey == topicsScorerKey() || fitted.ScorerKey != topicScorerKeyFor(fitted.Params) {
		t.Fatalf("scoring inputs: %+v %s", fitted.Params, fitted.ScorerKey)
	}
	if err := s.rescorePending(ctx, v); err != nil {
		t.Fatal(err)
	}
	if state, _ := suggestion(); state != "withdrawn" {
		t.Fatalf("the fitted gate withdraws it: %s", state)
	}
	// Nothing changed: no refit, no rescore.
	if err := s.fitGates(ctx, v); err != nil {
		t.Fatal(err)
	}
	if queue, _ := s.store.rescoreQueue(ctx); len(queue) != 0 {
		t.Fatalf("an unchanged topic is not rescored: %v", queue)
	}
	// A new definition voids the fit: its verdicts were about the old one.
	newSHA := topicDefinitionSHA("Exes and wyes.")
	s.store.db.Exec(`UPDATE topic_library SET definition='Exes and wyes.',definition_sha=? WHERE tag_id='tag_x'`, newSHA)
	s.store.putTextVector(ctx, topicTextDefinition, newSHA, v, gateTopic)
	inputs, _, _ = s.store.scoringInputs(ctx, v)
	if inputs.Topics[0].Params != topicScorerV1 || inputs.Topics[0].ScorerKey != topicsScorerKey() {
		t.Fatalf("a fit on another definition is ignored: %+v", inputs.Topics[0].Params)
	}
	if err := s.fitGates(ctx, v); err != nil {
		t.Fatal(err)
	}
	if gates, _ := topicGates(ctx, s.store.db); len(gates) != 0 {
		t.Fatalf("too few verdicts on the new definition: the gate goes, %v", gates)
	}
}

func TestTheExportHasScoresNoTextAndNothingFromRetiredMeetings(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	s.store.db.Exec(`INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,status,origin,revision,created_by,created_at,updated_by,updated_at) VALUES('tag_x','Secret label','secret label','A private definition.','dsha','active','manual',1,'a',1,'a',1)`)
	for _, name := range []string{"kept.opus", "gone.opus"} {
		s.store.db.Exec(`INSERT INTO topic_meeting(opus_name,job_id,state,priority,language,language_confidence,detector_version,queued_at) VALUES(?,?,'done',0,'it',0.9,'d1',1)`, name, meetingStem(name))
		s.store.db.Exec(`INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at,definition_sha,scorer_key,rule,support_words,peak,min_margin) VALUES(?,'tag_x','rejected','review','alice',5,'dsha','skey','short-strong',44,0.9,0.07)`, name)
	}
	retireTopicTestMeeting(t, s, "gone.opus")
	var out strings.Builder
	n, err := exportTopicVerdicts(ctx, s.store, &out)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	text := out.String()
	for _, leak := range []string{"gone", "Secret", "private", "alice"} {
		if strings.Contains(text, leak) {
			t.Fatalf("the export carries %q:\n%s", leak, text)
		}
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var header topicsExportHeader
	var row topicsExportRow
	json.Unmarshal([]byte(lines[0]), &header)
	json.Unmarshal([]byte(lines[1]), &row)
	if header.Format != topicsExportFormat || header.Rows != 1 || len(lines) != 2 {
		t.Fatalf("header %+v, %d lines", header, len(lines))
	}
	if row.Meeting != "kept.opus" || row.Verdict != "rejected" || *row.SupportWords != 44 || *row.Peak != 0.9 || row.Language != "it" || row.Rule != "short-strong" {
		t.Fatalf("row %+v", row)
	}
}

func TestDirectConfirmsNeverLoosenAGate(t *testing.T) {
	f := newGateFixture(t)
	ctx := context.Background()
	// Reviews the score cannot separate, and meetings people tagged by hand
	// below the gate: only rejections above the gate exist, so the tagged
	// ones would make any looser gate look better.
	for i := range 15 {
		f.seed(t, fmt.Sprintf("p%d.opus", i), 0.74, "confirmed", "review", true)
	}
	for i := range 5 {
		f.seed(t, fmt.Sprintf("n%d.opus", i), 0.74, "rejected", "review", true)
	}
	for i := range 6 {
		f.seed(t, fmt.Sprintf("d%d.opus", i), 0.65, "confirmed", "direct", true)
	}
	if err := f.s.fitGates(ctx, f.v); err != nil {
		t.Fatal(err)
	}
	if g, ok := f.gate(t); !ok || g.fitted() || g.Verdicts != 20 {
		t.Fatalf("direct confirms are not samples: %+v %v", g, ok)
	}
}

func TestAGateWaitsForEnoughScorableVerdictsAndRefitsWhenTheyArrive(t *testing.T) {
	f := newGateFixture(t)
	ctx := context.Background()
	// After a model change: 30 verdicts, none with vectors in the new space.
	for i := range 22 {
		f.seed(t, fmt.Sprintf("p%d.opus", i), 0.75, "confirmed", "review", false)
	}
	for i := range 8 {
		f.seed(t, fmt.Sprintf("n%d.opus", i), 0.705, "rejected", "review", false)
	}
	if err := f.s.fitGates(ctx, f.v); err != nil {
		t.Fatal(err)
	}
	g, ok := f.gate(t)
	if !ok || g.fitted() || g.Samples != 0 || g.Verdicts != 30 || math.IsNaN(g.HeldOutAccuracy) {
		t.Fatalf("no scorable verdicts: no fit, a readable record: %+v %v", g, ok)
	}
	// A few arrive: still under the minimums, still no fit.
	for i := range 3 {
		f.addVectors(t, fmt.Sprintf("p%d.opus", i), 0.75)
	}
	f.expire(t)
	f.s.fitGates(ctx, f.v)
	if g, _ := f.gate(t); g.fitted() || g.Samples != 3 {
		t.Fatalf("three scorable verdicts: %+v", g)
	}
	// All of them: the fit happens, though no verdict changed.
	for i := range 22 {
		f.addVectors(t, fmt.Sprintf("p%d.opus", i), 0.75)
	}
	for i := range 8 {
		f.addVectors(t, fmt.Sprintf("n%d.opus", i), 0.705)
	}
	f.expire(t)
	f.s.fitGates(ctx, f.v)
	if g, _ := f.gate(t); !g.fitted() || g.Samples != 30 {
		t.Fatalf("every verdict scorable: %+v", g)
	}
}

func TestSwappingAVerdictRefitsAndAScorerChangeVoidsTheGate(t *testing.T) {
	f := newGateFixture(t)
	ctx := context.Background()
	for i := range 22 {
		f.seed(t, fmt.Sprintf("p%d.opus", i), 0.75, "confirmed", "review", true)
	}
	for i := range 8 {
		f.seed(t, fmt.Sprintf("n%d.opus", i), 0.705, "rejected", "review", true)
	}
	f.s.fitGates(ctx, f.v)
	first, _ := f.gate(t)
	// One rejection moves to another meeting: the counts stay the same.
	f.s.store.db.Exec(`DELETE FROM topic_verdict WHERE opus_name='n0.opus'`)
	f.seed(t, "n9.opus", 0.725, "rejected", "review", true)
	f.expire(t)
	f.s.fitGates(ctx, f.v)
	second, _ := f.gate(t)
	if second.VerdictsDigest == first.VerdictsDigest || second.FittedAt == 0 || second.Threshold != 0.73 {
		t.Fatalf("a swapped verdict refits: %+v", second)
	}
	// A gate fitted against another global scorer does not apply.
	second.GlobalKey = "an older scorer"
	raw, _ := json.Marshal(second)
	f.s.store.db.Exec(`UPDATE topic_meta SET value=? WHERE key=?`, string(raw), topicGateMetaPrefix+"tag_x")
	inputs, _, _ := f.s.store.scoringInputs(ctx, f.v)
	if inputs.Topics[0].Params != topicScorerV1 {
		t.Fatalf("a gate from another scorer applies: %+v", inputs.Topics[0].Params)
	}
}

func TestARescoreUnderASupersededGateWritesNothing(t *testing.T) {
	f := newGateFixture(t)
	ctx := context.Background()
	f.seed(t, "open.opus", 0.705, "", "", true)
	stale, _, _ := f.s.store.scoringInputs(ctx, f.v)
	// A gate is fitted after the rescore read its inputs.
	g := topicGate{Threshold: 0.73, Margin: 0.06, GlobalKey: topicsScorerKey(), DefinitionSHA: f.sha, ModelRevision: f.v.ModelRevision, RecipeVersion: f.v.RecipeVersion}
	if err := f.s.setGate(ctx, "tag_x", &g, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.rescoreTopic(ctx, stale.Topics[0], stale); err != nil {
		t.Fatal(err)
	}
	var n int
	f.s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_suggestion`).Scan(&n)
	if n != 0 {
		t.Fatal("a rescore under the old gate must not write")
	}
}

func TestProposalsTreatAPassageAsCoveredAtTheTopicsOwnGate(t *testing.T) {
	strict := topicScorerV1
	strict.Threshold = 0.74
	inputs := topicScoringInputs{Controls: gateControls, Topics: []topicForScoring{{TagID: "t", Vector: gateTopic, Params: strict}}}
	if coveredByATopic(gateVector(0.705), inputs) {
		t.Fatal("below the topic's fitted gate, a passage is not covered")
	}
	inputs.Topics[0].Params = topicScorerV1
	if !coveredByATopic(gateVector(0.705), inputs) {
		t.Fatal("above the global gate it is")
	}
}

func TestRejectionsCopiedByAMergeAreNotFitted(t *testing.T) {
	f := newGateFixture(t)
	ctx := context.Background()
	// The merged topic's confirms were not copied, only its rejections.
	for i := range 22 {
		f.seed(t, fmt.Sprintf("p%d.opus", i), 0.75, "confirmed", "review", true)
	}
	for i := range 8 {
		f.seed(t, fmt.Sprintf("n%d.opus", i), 0.705, "rejected", "merge", true)
	}
	if err := f.s.fitGates(ctx, f.v); err != nil {
		t.Fatal(err)
	}
	if g, ok := f.gate(t); ok {
		t.Fatalf("copied rejections are not the topic's own reviews: %+v", g)
	}
}

func TestTheExportShowsOnlyAGateFittedAgainstTheCurrentScorer(t *testing.T) {
	f := newGateFixture(t)
	ctx := context.Background()
	f.seed(t, "m.opus", 0.9, "rejected", "review", true)
	g := topicGate{Threshold: 0.89, Margin: 0.05, GlobalKey: "an older scorer", DefinitionSHA: f.sha, ModelRevision: f.v.ModelRevision, RecipeVersion: f.v.RecipeVersion}
	f.s.setGate(ctx, "tag_x", &g, false)
	var out strings.Builder
	exportTopicVerdicts(ctx, f.s.store, &out)
	if strings.Contains(out.String(), `"gate"`) {
		t.Fatalf("a gate from another scorer is exported: %s", out.String())
	}
	g.GlobalKey = topicsScorerKey()
	f.s.setGate(ctx, "tag_x", &g, false)
	out.Reset()
	exportTopicVerdicts(ctx, f.s.store, &out)
	if !strings.Contains(out.String(), `"gate":[0.89,0.05],"gateModelRevision":"embeddinggemma-2-text-int8@rev1"`) {
		t.Fatalf("the current gate is exported with its model: %s", out.String())
	}
}
