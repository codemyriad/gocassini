package operator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Per-topic thresholds (docs/proposals/automatic-tagging, "In production"):
// once a topic has enough verdicts on its current definition, its Threshold
// and Margin are fitted to them, and kept only when leave-one-meeting-out
// validation says they decide those verdicts better than the global gate.
// The fit lives in topic_meta and in the topic's scorer key, so a change
// rescores the topic from stored vectors.

const (
	topicGateMinVerdicts   = 20
	topicGateMinRejections = 5
	topicGateMetaPrefix    = "topic_gate:"
	// A topic is refitted at most this often while only its verdicts change.
	topicGateRefitSeconds = 3600
)

// topicGateGrid are the gates a fit chooses from: Threshold 0.60 to 0.80 and
// Margin 0 to 0.12, by 0.01, around the global 0.68/0.06.
var topicGateGrid = func() [][2]float64 {
	var grid [][2]float64
	for t := 60; t <= 80; t++ {
		for m := 0; m <= 12; m++ {
			grid = append(grid, [2]float64{float64(t) / 100, float64(m) / 100})
		}
	}
	return grid
}()

// topicGate is a topic's fit, stored as JSON under topic_gate:<tag id>.
// Threshold and Margin are the global ones when the fit did not beat them.
type topicGate struct {
	Threshold     float64 `json:"threshold"`
	Margin        float64 `json:"margin"`
	DefinitionSHA string  `json:"definitionSha"`
	ModelRevision string  `json:"modelRevision"`
	RecipeVersion string  `json:"recipeVersion"`
	// The global scorer it was fitted against: a change to it voids the fit.
	GlobalKey string `json:"globalKey"`
	// The topic's review verdicts when it was fitted (a digest of the set
	// and its counts), and how many of them had vectors and were used.
	VerdictsDigest string `json:"verdictsDigest"`
	Verdicts       int    `json:"verdicts"`
	Rejections     int    `json:"rejections"`
	Samples        int    `json:"samples"`
	// Share of those verdicts the fitted gate decides right when each
	// meeting is left out of its own fit, and the global gate's share.
	HeldOutAccuracy float64 `json:"heldOutAccuracy"`
	GlobalAccuracy  float64 `json:"globalAccuracy"`
	FittedAt        int64   `json:"fittedAt"`
}

// fitted says whether the gate differs from the global one.
func (g topicGate) fitted() bool {
	return g.Threshold != topicScorerV1.Threshold || g.Margin != topicScorerV1.Margin
}

// appliesTo says whether the gate was fitted on this definition and vector
// space; a definition edit or a model change voids it.
func (g topicGate) appliesTo(t topicForScoring, v topicVersions) bool {
	return g.DefinitionSHA == t.DefinitionSHA && g.ModelRevision == v.ModelRevision && g.RecipeVersion == v.RecipeVersion && g.GlobalKey == topicsScorerKey()
}

// topicScorerKeyFor is a topic's scorer key: the global key, with its fitted
// gate folded in when it has one.
func topicScorerKeyFor(p topicScorerParams) string {
	if p.Threshold == topicScorerV1.Threshold && p.Margin == topicScorerV1.Margin {
		return topicsScorerKey()
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%g\n%g", topicsScorerKey(), p.Threshold, p.Margin)))
	return hex.EncodeToString(sum[:])
}

// topicGateSample is one verdict: a meeting's passages and whether people
// said the topic is in it.
type topicGateSample struct {
	Meeting  string
	Passages []topicPassage
	Positive bool
}

// fitTopicGate chooses a gate for one topic from its verdicts. Each grid gate
// is scored by how many verdicts it decides right; ties go to the gate
// nearest the global one. The fit is validated by leaving each meeting out:
// the gate chosen without it must decide it. ok is false when the held-out
// accuracy does not beat the global gate's accuracy on the same verdicts.
func fitTopicGate(samples []topicGateSample, topic []float32, controls [][]float32) (gate topicGate, ok bool) {
	gate = topicGate{Threshold: topicScorerV1.Threshold, Margin: topicScorerV1.Margin}
	if len(samples) == 0 {
		return gate, false
	}
	right := make([][]bool, len(topicGateGrid))
	totals := make([]int, len(topicGateGrid))
	for g, cut := range topicGateGrid {
		p := topicScorerV1
		p.Threshold, p.Margin = cut[0], cut[1]
		right[g] = make([]bool, len(samples))
		for i, s := range samples {
			right[g][i] = scoreTopic(s.Passages, topic, controls, p).qualifies() == s.Positive
			if right[g][i] {
				totals[g]++
			}
		}
	}
	byMeeting := map[string][]int{}
	for i, s := range samples {
		byMeeting[s.Meeting] = append(byMeeting[s.Meeting], i)
	}
	best := func(without []int) int {
		chosen, chosenRight := -1, -1
		for g := range topicGateGrid {
			n := totals[g]
			for _, i := range without {
				if right[g][i] {
					n--
				}
			}
			if n > chosenRight || (n == chosenRight && topicGateDistance(g) < topicGateDistance(chosen)) {
				chosen, chosenRight = g, n
			}
		}
		return chosen
	}
	heldOut := 0
	for _, members := range byMeeting {
		g := best(members)
		for _, i := range members {
			if right[g][i] {
				heldOut++
			}
		}
	}
	global := 0
	p := topicScorerV1
	for _, s := range samples {
		if scoreTopic(s.Passages, topic, controls, p).qualifies() == s.Positive {
			global++
		}
	}
	n := float64(len(samples))
	gate = topicGate{Threshold: p.Threshold, Margin: p.Margin, Samples: len(samples), HeldOutAccuracy: float64(heldOut) / n, GlobalAccuracy: float64(global) / n}
	for _, s := range samples {
		if !s.Positive {
			gate.Rejections++
		}
	}
	if heldOut <= global {
		return gate, false
	}
	chosen := topicGateGrid[best(nil)]
	gate.Threshold, gate.Margin = chosen[0], chosen[1]
	return gate, true
}

// topicGateDistance is how far a grid gate is from the global one; -1 (none
// chosen yet) is the farthest.
func topicGateDistance(g int) float64 {
	if g < 0 {
		return math.Inf(1)
	}
	return math.Abs(topicGateGrid[g][0]-topicScorerV1.Threshold) + math.Abs(topicGateGrid[g][1]-topicScorerV1.Margin)
}

// topicGates reads every stored gate by tag id.
func topicGates(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[string]topicGate, error) {
	rows, err := q.QueryContext(ctx, `SELECT key,value FROM topic_meta WHERE key LIKE ?`, topicGateMetaPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gates := map[string]topicGate{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var g topicGate
		if json.Unmarshal([]byte(raw), &g) == nil {
			gates[strings.TrimPrefix(key, topicGateMetaPrefix)] = g
		}
	}
	return gates, rows.Err()
}

// fitGates refits every active topic whose verdicts on its current
// definition changed since its last fit, and drops fits a definition edit or
// a model change voided. A topic whose effective gate changes is queued for
// a rescore. Vectors are read outside any annotations transaction.
func (t *topicsService) fitGates(ctx context.Context, versions topicVersions) error {
	inputs, ready, err := t.store.scoringInputs(ctx, versions)
	if err != nil || !ready {
		return err
	}
	gates, err := topicGates(ctx, t.store.db)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, topic := range inputs.Topics {
		active[topic.TagID] = true
		if ctx.Err() != nil {
			return ctx.Err()
		}
		verdicts, err := t.gateVerdicts(ctx, topic)
		if err != nil {
			return err
		}
		digest, rejections := topicVerdictsDigest(verdicts)
		old, had := gates[topic.TagID]
		before := topicScorerV1
		if had && old.appliesTo(topic, versions) {
			before.Threshold, before.Margin = old.Threshold, old.Margin
			// Refit when the verdicts changed or more of their meetings may
			// have vectors now, at most once per topicGateRefitSeconds.
			current := old.VerdictsDigest == digest && old.Samples == old.Verdicts
			if current || t.store.unix()-old.FittedAt < topicGateRefitSeconds {
				continue
			}
		}
		if len(verdicts) < topicGateMinVerdicts || rejections < topicGateMinRejections {
			if had {
				if err := t.setGate(ctx, topic.TagID, nil, before != topicScorerV1); err != nil {
					return err
				}
			}
			continue
		}
		samples, err := t.gateSamples(ctx, verdicts, versions)
		if err != nil {
			return err
		}
		sampleRejections := 0
		for _, s := range samples {
			if !s.Positive {
				sampleRejections++
			}
		}
		gate := topicGate{Threshold: topicScorerV1.Threshold, Margin: topicScorerV1.Margin, Samples: len(samples)}
		// The minimums hold for the verdicts that can be scored: right
		// after a model change, few meetings have vectors in the new space.
		if len(samples) >= topicGateMinVerdicts && sampleRejections >= topicGateMinRejections {
			if fit, ok := fitTopicGate(samples, topic.Vector, inputs.Controls); ok {
				gate = fit
			} else {
				gate.HeldOutAccuracy, gate.GlobalAccuracy = fit.HeldOutAccuracy, fit.GlobalAccuracy
			}
		}
		gate.GlobalKey, gate.VerdictsDigest, gate.Verdicts, gate.Rejections = topicsScorerKey(), digest, len(verdicts), rejections
		gate.DefinitionSHA, gate.ModelRevision, gate.RecipeVersion = topic.DefinitionSHA, versions.ModelRevision, versions.RecipeVersion
		gate.FittedAt = t.store.unix()
		changed := before.Threshold != gate.Threshold || before.Margin != gate.Margin
		if err := t.setGate(ctx, topic.TagID, &gate, changed); err != nil {
			return err
		}
		if changed {
			t.logf("topic %s: gate %.2f/%.2f from %d verdicts (held out %.2f, global %.2f)", topic.TagID, gate.Threshold, gate.Margin, len(samples), gate.HeldOutAccuracy, gate.GlobalAccuracy)
		}
	}
	for id := range gates {
		if !active[id] {
			// Retired or merged: its gate goes with it.
			if err := t.setGate(ctx, id, nil, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// topicGateVerdict is one review verdict a fit learns from.
type topicGateVerdict struct {
	Meeting  string
	Positive bool
}

// gateVerdicts reads one topic's verdicts on its current definition that
// came from reviewing its own suggestions. Direct confirms (the meeting
// already carried the tag) are left out: they cover every score, while
// rejections only cover scores that were suggested, so they would pull every
// fit toward a looser gate no one has checked. Rejections copied by a merge
// are left out too: the merged topic's confirms are not copied, so they would
// pull the other way.
func (t *topicsService) gateVerdicts(ctx context.Context, topic topicForScoring) ([]topicGateVerdict, error) {
	rows, err := t.store.db.QueryContext(ctx, `SELECT opus_name,verdict FROM topic_verdict WHERE tag_id=? AND definition_sha=? AND source='review' ORDER BY opus_name`, topic.TagID, topic.DefinitionSHA)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []topicGateVerdict
	for rows.Next() {
		var v topicGateVerdict
		var verdict string
		if err := rows.Scan(&v.Meeting, &verdict); err != nil {
			return nil, err
		}
		v.Positive = verdict == "confirmed"
		out = append(out, v)
	}
	return out, rows.Err()
}

// topicVerdictsDigest names a verdict set, so a swap of one verdict for
// another refits even when the counts stay the same.
func topicVerdictsDigest(verdicts []topicGateVerdict) (string, int) {
	h := sha256.New()
	rejections := 0
	for _, v := range verdicts {
		fmt.Fprintf(h, "%s\t%v\n", v.Meeting, v.Positive)
		if !v.Positive {
			rejections++
		}
	}
	return hex.EncodeToString(h.Sum(nil)), rejections
}

// gateSamples reads the vectors of the verdicts' meetings. Meetings without
// vectors in the current space are left out.
func (t *topicsService) gateSamples(ctx context.Context, verdicts []topicGateVerdict, versions topicVersions) ([]topicGateSample, error) {
	var out []topicGateSample
	for _, v := range verdicts {
		set, ok, err := t.vectors.meeting(ctx, v.Meeting)
		if err != nil {
			return nil, err
		}
		if !ok || set.ModelRevision != versions.ModelRevision || set.RecipeVersion != versions.RecipeVersion {
			continue
		}
		out = append(out, topicGateSample{Meeting: v.Meeting, Positive: v.Positive, Passages: set.passages()})
	}
	return out, nil
}

// setGate stores or (nil) removes a topic's gate, queueing a rescore in the
// same transaction when rescore is set.
func (t *topicsService) setGate(ctx context.Context, tagID string, gate *topicGate, rescore bool) error {
	return t.store.inTx(ctx, func(tx *sql.Tx) error {
		key := topicGateMetaPrefix + tagID
		if gate == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM topic_meta WHERE key=?`, key); err != nil {
				return err
			}
		} else {
			raw, err := json.Marshal(gate)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO topic_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(raw)); err != nil {
				return err
			}
		}
		if rescore {
			return addRescoreTx(ctx, tx, tagID)
		}
		return nil
	})
}

// effectiveScorerKeyTx is the scorer key a topic scores under right now, read
// inside a transaction: a rescore computed under another gate is stale.
func effectiveScorerKeyTx(ctx context.Context, tx *sql.Tx, t topicForScoring, v topicVersions) (string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM topic_meta WHERE key=?`, topicGateMetaPrefix+t.TagID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return topicsScorerKey(), nil
	}
	if err != nil {
		return "", err
	}
	var g topicGate
	if json.Unmarshal([]byte(raw), &g) != nil || !g.appliesTo(t, v) {
		return topicsScorerKey(), nil
	}
	p := topicScorerV1
	p.Threshold, p.Margin = g.Threshold, g.Margin
	return topicScorerKeyFor(p), nil
}
