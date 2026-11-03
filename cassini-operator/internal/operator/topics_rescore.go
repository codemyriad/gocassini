package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// Rescoring one topic (docs/proposals/automatic-tagging, "Rescoring"): after
// a create, a definition edit, a restore or a merge, every checked meeting is
// scored again for that topic from its stored vectors, without embedding.
// One meeting's vectors are read at a time, outside any annotations
// transaction, and each meeting is written in its own short transaction.

const topicMetaRescore = "rescore_topics"

// queueRescore records that a topic must be rescored; it survives a restart.
// Library writes queue inside their own transaction (addRescoreTx); this is
// for callers outside one.
func (t *topicsService) queueRescore(ctx context.Context, tagID string) {
	if err := t.store.inTx(ctx, func(tx *sql.Tx) error { return addRescoreTx(ctx, tx, tagID) }); err != nil {
		t.logf("queue rescore of %s: %v", tagID, err)
	}
	t.wakeUp()
}

// The queue maps each topic to a sequence number that every request bumps,
// so a pass removes a topic only when nobody asked again meanwhile.
func (s *topicsStore) rescoreQueue(ctx context.Context) (map[string]int, error) {
	var queue map[string]int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		queue, err = rescoreIDs(ctx, tx)
		return err
	})
	return queue, err
}

func addRescoreTx(ctx context.Context, tx *sql.Tx, tagID string) error {
	queue, err := rescoreIDs(ctx, tx)
	if err != nil {
		return err
	}
	queue[tagID]++
	return setRescoreIDs(ctx, tx, queue)
}

// doneRescore removes a topic scored at seq, unless it was asked for again.
func (s *topicsStore) doneRescore(ctx context.Context, tagID string, seq int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		queue, err := rescoreIDs(ctx, tx)
		if err != nil {
			return err
		}
		if queue[tagID] != seq {
			return nil
		}
		delete(queue, tagID)
		return setRescoreIDs(ctx, tx, queue)
	})
}

func rescoreIDs(ctx context.Context, tx *sql.Tx) (map[string]int, error) {
	queue := map[string]int{}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM topic_meta WHERE key=?`, topicMetaRescore).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return queue, nil
	}
	if err != nil {
		return nil, err
	}
	return queue, json.Unmarshal([]byte(raw), &queue)
}

func setRescoreIDs(ctx context.Context, tx *sql.Tx, queue map[string]int) error {
	raw, _ := json.Marshal(queue)
	_, err := tx.ExecContext(ctx, `INSERT INTO topic_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, topicMetaRescore, string(raw))
	return err
}

// rescorePending rescores each queued topic whose definition has a vector for
// the current versions. A topic still waiting for its vector stays queued:
// until then it is "preparing", and its old suggestions stay as they are.
func (t *topicsService) rescorePending(ctx context.Context, versions topicVersions) error {
	queue, err := t.store.rescoreQueue(ctx)
	if err != nil || len(queue) == 0 {
		return err
	}
	inputs, ready, err := t.store.scoringInputs(ctx, versions)
	if err != nil || !ready {
		return err
	}
	byID := map[string]topicForScoring{}
	for _, topic := range inputs.Topics {
		byID[topic.TagID] = topic
	}
	ids := make([]string, 0, len(queue))
	for id := range queue {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		seq := queue[id]
		topic, ok := byID[id]
		if !ok {
			// Retired since, or its definition is not embedded yet.
			row, exists, err := topicByID(ctx, t.store.db, id)
			if err != nil {
				return err
			}
			if !exists || row.Status != topicStatusActive {
				if err := t.store.doneRescore(ctx, id, seq); err != nil {
					return err
				}
			}
			continue
		}
		n, err := t.rescoreTopic(ctx, topic, inputs)
		if err != nil {
			return err
		}
		if err := t.store.doneRescore(ctx, id, seq); err != nil {
			return err
		}
		t.statsMu.Lock()
		t.stats.Rescored += n
		t.statsMu.Unlock()
		t.logf("rescored topic %s on %d meetings", id, n)
	}
	return nil
}

// rescoreTopic scores one topic on every checked meeting embedded under the
// current versions.
func (t *topicsService) rescoreTopic(ctx context.Context, topic topicForScoring, inputs topicScoringInputs) (int, error) {
	rows, err := t.store.db.QueryContext(ctx, `SELECT opus_name FROM topic_meeting WHERE state=? AND model_revision=? AND recipe_version=? ORDER BY opus_name`,
		topicStateDone, inputs.Versions.ModelRevision, inputs.Versions.RecipeVersion)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	single := topicScoringInputs{Versions: inputs.Versions, Controls: inputs.Controls, Topics: []topicForScoring{topic}, ScorerKey: inputs.ScorerKey}
	done := 0
	for _, name := range names {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		set, ok, err := t.vectors.meeting(ctx, name)
		if err != nil {
			return done, err
		}
		if !ok || set.ModelRevision != inputs.Versions.ModelRevision || set.RecipeVersion != inputs.Versions.RecipeVersion {
			continue
		}
		scores := scoreMeetingTopics(set.passages(), single)
		err = t.store.inTx(ctx, func(tx *sql.Tx) error {
			// The worker may hold newer passages for this meeting: then
			// this result is stale, and the worker's own scoring wins.
			var key, state string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(embed_key,''),state FROM topic_meeting WHERE opus_name=?`, name).Scan(&key, &state); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			if key != set.EmbedKey || state != topicStateDone {
				return nil
			}
			// The topic must still be the one scored: retired, merged or
			// redefined meanwhile, and this result is stale.
			var status, sha string
			if err := tx.QueryRowContext(ctx, `SELECT status,definition_sha FROM topic_library WHERE tag_id=?`, topic.TagID).Scan(&status, &sha); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return errTopicChanged
				}
				return err
			}
			if status != topicStatusActive || sha != topic.DefinitionSHA {
				return errTopicChanged
			}
			// A gate fitted since queued the topic again.
			if key, err := effectiveScorerKeyTx(ctx, tx, topic, inputs.Versions); err != nil {
				return err
			} else if key != topic.scorerKey() {
				return errTopicChanged
			}
			if err := t.store.notRetired(ctx, name); err != nil {
				if errors.Is(err, errMeetingRetired) {
					return nil
				}
				return err
			}
			return t.store.applyScores(ctx, tx, name, set.PayloadSHA256, scores)
		})
		if errors.Is(err, errTopicChanged) {
			// Its new state queued its own pass.
			return done, nil
		}
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// errTopicChanged stops a rescore pass whose topic changed under it.
var errTopicChanged = errors.New("the topic changed during its rescore")
