package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestAnnotationsSchema7And8UpgradeToTopicsKeepingMarksAndReceipts(t *testing.T) {
	// 7 is the last release; 8 adds the meeting title (D-862) before topics.
	for _, from := range []int{7, 8} {
		t.Run(fmt.Sprintf("from %d", from), func(t *testing.T) { testAnnotationsUpgradeToTopics(t, from) })
	}
}

func testAnnotationsUpgradeToTopics(t *testing.T, from int) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), annotationsStoreFilename)
	store, err := openAnnotationStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result annotateResult
	if err := json.Unmarshal([]byte(annTestApplied), &result); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, "kept.opus", result); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO annotation_receipt(caller,request_id,opus_name,request_hash,response,snapshot) VALUES('alice','r1','kept.opus','h','{}',1)`); err != nil {
		t.Fatal(err)
	}
	// Back to what schema 7 or 8 looked like: no topic tables.
	for _, table := range []string{"topic_library", "topic_library_event", "topic_tag_alias", "topic_verdict", "topic_receipt", "topic_receipt_target",
		"topic_text_vector", "topic_meeting", "topic_evidence", "topic_suggestion", "topic_proposal_member", "topic_proposal", "topic_room_dirty", "topic_meta"} {
		if _, err := store.db.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, from)); err != nil {
		t.Fatal(err)
	}
	store.Close()

	store, err = openAnnotationStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if v, err := store.userVersion(); err != nil || v != 9 {
		t.Fatalf("version %d %v", v, err)
	}
	var marks, receipts, topics int
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_tag WHERE opus_name='kept.opus'`).Scan(&marks)
	store.db.QueryRow(`SELECT COUNT(*) FROM annotation_receipt`).Scan(&receipts)
	store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'topic_%'`).Scan(&topics)
	if marks == 0 || receipts != 1 || topics != 14 {
		t.Fatalf("marks %d receipts %d topic tables %d", marks, receipts, topics)
	}
	got, err := store.document(ctx, "kept.opus")
	if err != nil || string(got.Annotations) != string(result.Annotations) {
		t.Fatalf("the document survives: %v", err)
	}
}

func TestTopicScoresWriteSuggestionsEvidenceAndDirectVerdicts(t *testing.T) {
	ctx := context.Background()
	store := newTopicsStore(newTestAnnotationStore(t))
	// The meeting already carries "hiring" (desired snapshot: a mark).
	if err := store.Record(ctx, "m.opus", guardTestDoc(1, "tag_hire_local", "Hiring")); err != nil {
		t.Fatal(err)
	}
	topic := func(id, label string) topicForScoring {
		return topicForScoring{TagID: id, LabelFolded: label, DefinitionSHA: "sha-" + id, Vector: []float32{1, 0, 0, 0}}
	}
	passages := []topicPassage{
		{StartMS: 0, EndMS: 9000, WordCount: 50, Vector: []float32{1, 0, 0, 0}},
		{StartMS: 9500, EndMS: 19000, WordCount: 50, Vector: []float32{1, 0, 0, 0}},
	}
	in := topicScoringInputs{Controls: [][]float32{{0, 0, 0, 1}}, Topics: []topicForScoring{
		topic("tag_release", "release"), topic("tag_hire", "hiring"), topic("tag_decided", "decided"), topic("tag_rejected", "rejected"),
	}}
	if _, err := store.db.Exec(`INSERT INTO topic_suggestion(opus_name,tag_id,state,rule,support_words,definition_sha,scorer_key,created_at,updated_at) VALUES('m.opus','tag_decided','decided','short-strong',40,'x','y',1,1);
INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at) VALUES('m.opus','tag_rejected','rejected','review','alice',1)`); err != nil {
		t.Fatal(err)
	}
	apply := func(scores []topicMeetingScore) {
		t.Helper()
		if err := store.inTx(ctx, func(tx *sql.Tx) error { return store.applyScores(ctx, tx, "m.opus", "payload", scores) }); err != nil {
			t.Fatal(err)
		}
	}
	apply(scoreMeetingTopics(passages, in))

	var state, rule string
	var words int
	if err := store.db.QueryRow(`SELECT state,rule,support_words FROM topic_suggestion WHERE opus_name='m.opus' AND tag_id='tag_release'`).Scan(&state, &rule, &words); err != nil || state != "pending" || rule != "supporting-words" || words != 100 {
		t.Fatalf("release: %s %s %d %v", state, rule, words, err)
	}
	var evidence int
	store.db.QueryRow(`SELECT COUNT(*) FROM topic_evidence WHERE opus_name='m.opus' AND tag_id='tag_release' AND counted=1`).Scan(&evidence)
	if evidence != 1 {
		t.Fatalf("one joined span of evidence, got %d", evidence)
	}
	var verdict, source string
	if err := store.db.QueryRow(`SELECT verdict,source FROM topic_verdict WHERE opus_name='m.opus' AND tag_id='tag_hire'`).Scan(&verdict, &source); err != nil || verdict != "confirmed" || source != "direct" {
		t.Fatalf("a carried topic is confirmed directly: %s %s %v", verdict, source, err)
	}
	var n int
	store.db.QueryRow(`SELECT COUNT(*) FROM topic_suggestion WHERE tag_id IN ('tag_hire','tag_rejected')`).Scan(&n)
	if n != 0 {
		t.Fatal("no suggestion for a carried or rejected topic")
	}
	store.db.QueryRow(`SELECT state FROM topic_suggestion WHERE tag_id='tag_decided'`).Scan(&state)
	if state != "decided" {
		t.Fatalf("a decided row is never touched: %s", state)
	}

	// The transcript changes and the topic no longer qualifies: withdrawn.
	apply(scoreMeetingTopics([]topicPassage{{WordCount: 50, Vector: []float32{0, 0, 0, 1}}}, in))
	store.db.QueryRow(`SELECT state FROM topic_suggestion WHERE tag_id='tag_release'`).Scan(&state)
	if state != "withdrawn" {
		t.Fatalf("release after the change: %s", state)
	}
}

func TestTopicEnqueueKeepsBlockedRowsAndRaisesPriority(t *testing.T) {
	ctx := context.Background()
	store := newTopicsStore(newTestAnnotationStore(t))
	if _, err := store.enqueue(ctx, "a.opus", "a", topicPriorityBackfill, topicEnqueue{}); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.enqueue(ctx, "a.opus", "a", topicPriorityPublish, topicEnqueue{Digest: "d", Attempt: 2}); err != nil || !changed {
		t.Fatalf("a publish raises a backfill row: %v %v", changed, err)
	}
	var priority, attempt int
	store.db.QueryRow(`SELECT priority,published_attempt FROM topic_meeting WHERE opus_name='a.opus'`).Scan(&priority, &attempt)
	if priority != topicPriorityPublish || attempt != 2 {
		t.Fatalf("priority %d attempt %d", priority, attempt)
	}
	store.db.Exec(`UPDATE topic_meeting SET state='blocked' WHERE opus_name='a.opus'`)
	if changed, _ := store.enqueue(ctx, "a.opus", "a", topicPriorityBackfill, topicEnqueue{}); changed {
		t.Fatal("the reconciler and backfill never unblock a meeting")
	}
	if err := store.enqueuePublished(ctx, "a.opus", "a", "d2", 3); err != nil {
		t.Fatal(err)
	}
	var state string
	store.db.QueryRow(`SELECT state FROM topic_meeting WHERE opus_name='a.opus'`).Scan(&state)
	if state != topicStateQueued {
		t.Fatalf("a new delivery unblocks it: %s", state)
	}
}
