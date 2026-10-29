package operator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// Scoring a meeting against the library and storing the outcome
// (docs/proposals/automatic-tagging, "Scoring"). Vectors are read and scores
// computed outside any annotations transaction; one short transaction then
// writes one meeting's rows.

// Text vector kinds in topic_text_vector.
const (
	topicTextDefinition = "definition"
	topicTextControl    = "control"
)

// topicTextSHA keys a definition or control by its text.
func topicTextSHA(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// topicVersions are the CLI's current versions (`cassini topics versions`).
type topicVersions struct {
	ModelRevision   string `json:"modelRevision"`
	ChunkerVersion  string `json:"chunkerVersion"`
	RecipeVersion   string `json:"recipeVersion"`
	DetectorVersion string `json:"detectorVersion"`
}

// embedKey is the key the CLI gives a transcript with this payload digest
// under these versions (topics.EmbedKey in the recorder; change both).
func (v topicVersions) embedKey(payloadSHA256 string) string {
	sum := sha256.Sum256([]byte(payloadSHA256 + "\n" + v.ModelRevision + "\n" + v.ChunkerVersion + "\n" + v.RecipeVersion))
	return hex.EncodeToString(sum[:])
}

func (s *topicsStore) textVector(ctx context.Context, kind, sha string, v topicVersions) ([]float32, bool, error) {
	var blob []byte
	err := s.db.QueryRowContext(ctx, `SELECT vector FROM topic_text_vector WHERE kind=? AND sha=? AND model_revision=? AND recipe_version=?`, kind, sha, v.ModelRevision, v.RecipeVersion).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	vector, err := decodeTopicVector(blob)
	return vector, err == nil, err
}

func (s *topicsStore) putTextVector(ctx context.Context, kind, sha string, v topicVersions, vector []float32) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO topic_text_vector(kind,sha,model_revision,recipe_version,vector) VALUES(?,?,?,?,?)
 ON CONFLICT(kind,sha,model_revision,recipe_version) DO UPDATE SET vector=excluded.vector`, kind, sha, v.ModelRevision, v.RecipeVersion, encodeTopicVector(vector))
	return err
}

// topicText is a text that needs a vector: a control or a definition.
type topicText struct {
	Kind, SHA, Text string
}

// missingTextVectors lists the controls and active definitions with no vector
// for the current versions.
func (s *topicsStore) missingTextVectors(ctx context.Context, v topicVersions) ([]topicText, error) {
	var want []topicText
	for _, c := range topicControls {
		want = append(want, topicText{topicTextControl, topicTextSHA(c.Definition), c.Definition})
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT definition_sha,definition FROM topic_library WHERE status='active'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t topicText
		if err := rows.Scan(&t.SHA, &t.Text); err != nil {
			rows.Close()
			return nil, err
		}
		t.Kind = topicTextDefinition
		want = append(want, t)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var missing []topicText
	for _, t := range want {
		_, ok, err := s.textVector(ctx, t.Kind, t.SHA, v)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

// topicForScoring is an active library topic with its definition vector.
type topicForScoring struct {
	TagID         string
	LabelFolded   string
	DefinitionSHA string
	Vector        []float32
	// Params are the global constants with the topic's fitted gate, if it
	// has one; ScorerKey names them.
	Params    topicScorerParams
	ScorerKey string
}

// params are the topic's scorer constants; a topic built without them gets
// the global ones.
func (t topicForScoring) params() topicScorerParams {
	if t.Params == (topicScorerParams{}) {
		return topicScorerV1
	}
	return t.Params
}

// scorerKey names the topic's constants.
func (t topicForScoring) scorerKey() string {
	if t.ScorerKey == "" {
		return topicScorerKeyFor(t.params())
	}
	return t.ScorerKey
}

// topicScoringInputs is everything a meeting is scored against.
type topicScoringInputs struct {
	Versions  topicVersions
	Controls  [][]float32
	Topics    []topicForScoring
	ScorerKey string
}

// scoringInputs loads the controls and every active topic that has a vector
// for the current versions. ready is false until both controls have one: no
// meeting is scored before then.
func (s *topicsStore) scoringInputs(ctx context.Context, v topicVersions) (topicScoringInputs, bool, error) {
	in := topicScoringInputs{Versions: v, ScorerKey: topicsScorerKey()}
	if v.ModelRevision == "" {
		return in, false, nil
	}
	for _, c := range topicControls {
		vector, ok, err := s.textVector(ctx, topicTextControl, topicTextSHA(c.Definition), v)
		if err != nil || !ok {
			return in, false, err
		}
		in.Controls = append(in.Controls, vector)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.tag_id,l.label_folded,l.definition_sha,t.vector FROM topic_library l
 JOIN topic_text_vector t ON t.kind=? AND t.sha=l.definition_sha AND t.model_revision=? AND t.recipe_version=?
 WHERE l.status='active' ORDER BY l.tag_id`, topicTextDefinition, v.ModelRevision, v.RecipeVersion)
	if err != nil {
		return in, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var t topicForScoring
		var blob []byte
		if err := rows.Scan(&t.TagID, &t.LabelFolded, &t.DefinitionSHA, &blob); err != nil {
			return in, false, err
		}
		if t.Vector, err = decodeTopicVector(blob); err != nil {
			return in, false, err
		}
		t.Params, t.ScorerKey = topicScorerV1, in.ScorerKey
		in.Topics = append(in.Topics, t)
	}
	if err := rows.Close(); err != nil {
		return in, false, err
	}
	gates, err := topicGates(ctx, s.db)
	if err != nil {
		return in, false, err
	}
	for i, t := range in.Topics {
		if g, ok := gates[t.TagID]; ok && g.appliesTo(t, v) {
			in.Topics[i].Params.Threshold, in.Topics[i].Params.Margin = g.Threshold, g.Margin
			in.Topics[i].ScorerKey = topicScorerKeyFor(in.Topics[i].Params)
		}
	}
	return in, true, nil
}

// topicMeetingScore is one topic's outcome for one meeting.
type topicMeetingScore struct {
	Topic    topicForScoring
	Decision topicDecision
}

// scoreMeetingTopics scores every topic for one meeting's passages.
func scoreMeetingTopics(passages []topicPassage, in topicScoringInputs) []topicMeetingScore {
	out := make([]topicMeetingScore, 0, len(in.Topics))
	for _, t := range in.Topics {
		out = append(out, topicMeetingScore{Topic: t, Decision: scoreTopic(passages, t.Vector, in.Controls, t.params())})
	}
	return out
}

// topicEmbedResult is what one embed produced, as the CLI's header says.
type topicEmbedResult struct {
	EmbedKey           string
	PayloadSHA256      string
	Language           string
	LanguageConfidence float64
	DetectorVersion    string
	Passages           int
	Tokens             int
	Versions           topicVersions
}

// finishMeeting records a scored meeting: suggestions, direct verdicts and the
// work row, in one transaction. Nothing is written when the claim was lost,
// when the meeting was retired, or when a publish arrived meanwhile (the row
// is queued again instead).
func (s *topicsStore) finishMeeting(ctx context.Context, row topicMeetingRow, lifecycle *Store, generation string, result topicEmbedResult, scores []topicMeetingScore) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		ok, requeued, err := s.finishClaim(ctx, tx, row)
		if err != nil || !ok {
			return err
		}
		if requeued {
			// The vectors stay in the sidecar under their key, so the next run
			// skips the embed when the transcript did not change.
			_, err := tx.ExecContext(ctx, `UPDATE topic_meeting SET embed_key=?,payload_sha256=? WHERE opus_name=?`, result.EmbedKey, result.PayloadSHA256, row.OpusName)
			return err
		}
		if lifecycle != nil {
			if err := (ExAppConfig{lifecycle: lifecycle}).meetingNotRetired(ctx, row.OpusName); err != nil {
				return err
			}
		}
		if err := s.applyScores(ctx, tx, row.OpusName, result.PayloadSHA256, scores); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE topic_meeting SET state=?,claim=NULL,reason='',last_error='',attempts=0,retry_at=0,finished_at=?,
 embed_key=?,payload_sha256=?,language=?,language_confidence=?,detector_version=?,vectors_generation=?,scorer_key=?,
 model_revision=?,chunker_version=?,recipe_version=?,passages=?,tokens=? WHERE opus_name=?`,
			topicStateDone, s.unix(), result.EmbedKey, result.PayloadSHA256, result.Language, result.LanguageConfidence, result.DetectorVersion,
			generation, topicsScorerKey(), result.Versions.ModelRevision, result.Versions.ChunkerVersion, result.Versions.RecipeVersion,
			result.Passages, result.Tokens, row.OpusName)
		return err
	})
}

// applyScores writes one meeting's suggestions inside tx. A topic with a
// verdict is left alone; a topic the meeting already carries gets a direct
// confirmed verdict; a decided suggestion is never touched.
func (s *topicsStore) applyScores(ctx context.Context, tx *sql.Tx, name, payload string, scores []topicMeetingScore) error {
	carried, err := carriedTopicTags(ctx, tx, name)
	if err != nil {
		return err
	}
	now := s.unix()
	for _, sc := range scores {
		t := sc.Topic
		var verdict string
		err := tx.QueryRowContext(ctx, `SELECT verdict FROM topic_verdict WHERE opus_name=? AND tag_id=?`, name, t.TagID).Scan(&verdict)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if carried.has(ctx, tx, t) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,at,payload_sha256,definition_sha,scorer_key) VALUES(?,?,'confirmed','direct',?,?,?,?)`,
				name, t.TagID, now, payload, t.DefinitionSHA, t.scorerKey()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='withdrawn',updated_at=? WHERE opus_name=? AND tag_id=? AND state='pending'`, now, name, t.TagID); err != nil {
				return err
			}
			continue
		}
		var state string
		err = tx.QueryRowContext(ctx, `SELECT state FROM topic_suggestion WHERE opus_name=? AND tag_id=?`, name, t.TagID).Scan(&state)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state == "decided" {
			continue
		}
		d := sc.Decision
		if !d.qualifies() {
			if exists && state == "pending" {
				if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='withdrawn',updated_at=? WHERE opus_name=? AND tag_id=?`, now, name, t.TagID); err != nil {
					return err
				}
			}
			continue
		}
		words, peak, minMargin := topicDecisionSummary(d)
		rule := "supporting-words"
		if d.Reason == topicReasonStrong {
			rule = "short-strong"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_suggestion(opus_name,tag_id,state,rule,support_words,peak,min_margin,definition_sha,scorer_key,created_at,updated_at)
 VALUES(?,?,'pending',?,?,?,?,?,?,?,?) ON CONFLICT(opus_name,tag_id) DO UPDATE SET state='pending',rule=excluded.rule,support_words=excluded.support_words,
 peak=excluded.peak,min_margin=excluded.min_margin,definition_sha=excluded.definition_sha,scorer_key=excluded.scorer_key,updated_at=excluded.updated_at`,
			name, t.TagID, rule, words, peak, minMargin, t.DefinitionSHA, t.scorerKey(), now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM topic_evidence WHERE opus_name=? AND tag_id=?`, name, t.TagID); err != nil {
			return err
		}
		for i, span := range d.Spans {
			counted := span.WordCount >= topicScorerV1.SubstantialSpanWords
			if d.Reason == topicReasonStrong {
				counted = span.WordCount >= topicScorerV1.StrongSpanWords && span.MinScore >= topicScorerV1.StrongMinScore && span.MinMargin >= topicScorerV1.StrongMinMargin
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO topic_evidence(opus_name,tag_id,ordinal,start_ms,end_ms,word_count,counted) VALUES(?,?,?,?,?,?,?)`,
				name, t.TagID, i, span.StartMS, span.EndMS, span.WordCount, counted); err != nil {
				return err
			}
		}
	}
	return nil
}

// topicDecisionSummary is a qualifying decision's supporting words (those that
// count toward its rule), peak score and lowest margin.
func topicDecisionSummary(d topicDecision) (words int, peak, minMargin float64) {
	first := true
	for _, span := range d.Spans {
		if d.Reason == topicReasonWords && span.WordCount < topicScorerV1.SubstantialSpanWords {
			continue
		}
		words += span.WordCount
		if first || span.PeakScore > peak {
			peak = span.PeakScore
		}
		if first || span.MinMargin < minMargin {
			minMargin = span.MinMargin
		}
		first = false
	}
	return words, peak, minMargin
}

// topicCarried is what tags a meeting's desired annotations carry: a mark not
// uploaded yet counts.
type topicCarried struct {
	ids    map[string]bool
	labels map[string]bool
}

func (c topicCarried) has(ctx context.Context, tx *sql.Tx, t topicForScoring) bool {
	if c.ids[t.TagID] || c.labels[t.LabelFolded] {
		return true
	}
	rows, err := tx.QueryContext(ctx, `SELECT alias_tag_id FROM topic_tag_alias WHERE tag_id=?`, t.TagID)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var alias string
		if rows.Scan(&alias) == nil && c.ids[alias] {
			return true
		}
	}
	return false
}

// carriedTopicTags reads the tags with at least one mark in the meeting's
// desired snapshot, or in its indexed projection when it has none.
func carriedTopicTags(ctx context.Context, tx *sql.Tx, name string) (topicCarried, error) {
	c := topicCarried{ids: map[string]bool{}, labels: map[string]bool{}}
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT s.result_json FROM annotation_head h JOIN annotation_snapshot s ON s.id=h.desired WHERE h.opus_name=?`, name).Scan(&data)
	if err == nil {
		var result annotateResult
		if err := json.Unmarshal(data, &result); err != nil {
			return c, err
		}
		var doc projectedDocument
		if len(result.Annotations) > 0 && string(result.Annotations) != "null" {
			if err := json.Unmarshal(result.Annotations, &doc); err != nil {
				return c, err
			}
		}
		marked := map[string]bool{}
		for _, item := range doc.Items {
			marked[item.TagID] = true
		}
		for _, tag := range doc.Tags {
			if marked[tag.ID] {
				c.ids[tag.ID] = true
				c.labels[foldTagLabel(tag.Label)] = true
			}
		}
		return c, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return c, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT t.tag_id,t.label_folded FROM annotation_tag t JOIN annotation_item i ON i.opus_name=t.opus_name AND i.tag_id=t.tag_id WHERE t.opus_name=?`, name)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, label string
		if err := rows.Scan(&id, &label); err != nil {
			return c, err
		}
		c.ids[id] = true
		c.labels[label] = true
	}
	return c, rows.Err()
}
