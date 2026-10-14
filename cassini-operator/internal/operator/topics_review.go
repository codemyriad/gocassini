package operator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Reviewing suggestions (docs/proposals/automatic-tagging, "Review: states
// and API"). A confirm writes an ordinary whole-meeting mark, credited to the
// person, and the verdict in the same transaction. Nothing reaches the file
// before a confirm, and no score is ever served.

const (
	topicDecisionConfirm = "confirm"
	topicDecisionReject  = "reject"
	topicDecisionUndo    = "undo"
	topicDecisionReopen  = "reopen"
)

// topicSuggestionView is one pending suggestion for a meeting.
type topicSuggestionView struct {
	TagID        string              `json:"tagId"`
	Label        string              `json:"label"`
	Definition   string              `json:"definition"`
	Color        *string             `json:"color"`
	Icon         *string             `json:"icon"`
	Rule         string              `json:"rule"`
	SupportWords int                 `json:"supportWords"`
	Evidence     []topicEvidenceView `json:"evidence"`
}

type topicEvidenceView struct {
	StartMS   int64 `json:"startMs"`
	EndMS     int64 `json:"endMs"`
	WordCount int   `json:"wordCount"`
	Counted   bool  `json:"counted"`
}

type topicDecidedView struct {
	TagID   string `json:"tagId"`
	Label   string `json:"label"`
	Verdict string `json:"verdict"`
	Source  string `json:"source"`
	By      string `json:"by,omitempty"`
	At      string `json:"at"`
}

// topicMeetingView is GET annotations/topics/meetings/<id>.
type topicMeetingView struct {
	Enabled           bool                  `json:"enabled"`
	State             string                `json:"state"`
	Reason            string                `json:"reason,omitempty"`
	Language          string                `json:"language"`
	ValidatedLanguage bool                  `json:"validatedLanguage"`
	Confirmable       bool                  `json:"confirmable"`
	BlockedReason     string                `json:"blockedReason,omitempty"`
	Suggestions       []topicSuggestionView `json:"suggestions"`
	Decided           []topicDecidedView    `json:"decided"`
}

func (s *annotationService) routeTopicReview(w http.ResponseWriter, r *http.Request, caller string, parts []string) bool {
	switch {
	case len(parts) == 2 && parts[0] == "meetings" && r.Method == http.MethodGet:
		s.serveMeetingSuggestions(w, r, caller, parts[1])
	case len(parts) == 3 && parts[0] == "meetings" && r.Method == http.MethodPost:
		s.decideSuggestion(w, r, caller, parts[1], parts[2])
	case len(parts) == 1 && parts[0] == "coverage" && r.Method == http.MethodGet:
		s.serveTopicCoverage(w, r, caller)
	case len(parts) >= 1 && (parts[0] == "meetings" || parts[0] == "coverage"):
		writeMethodNotAllowed(w, "GET, POST")
	default:
		return s.routeTopicQueue(w, r, caller, parts)
	}
	return true
}

func (s *annotationService) serveMeetingSuggestions(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if !isPlainMeetingID(meetingID) {
		writeJSONError(w, http.StatusBadRequest, "that is not a meeting id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()
	_, opusName, _, ok := s.visibleRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	// As the caller, right now: a share Nextcloud stopped honouring since the
	// list was built serves nothing.
	if _, err := s.authorizeRecording(ctx, caller, opusName); err != nil {
		s.answerFailure(w, r, "topics meeting="+meetingID, err)
		return
	}
	view, err := s.rt.topics.meetingView(ctx, opusName)
	if err != nil {
		s.answerFailure(w, r, "topics meeting="+meetingID, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// meetingView is what the meeting page shows of one meeting's suggestions.
func (t *topicsService) meetingView(ctx context.Context, opusName string) (topicMeetingView, error) {
	view := topicMeetingView{Enabled: true, State: "not-checked", Language: "unknown", Suggestions: []topicSuggestionView{}, Decided: []topicDecidedView{}}
	var state, reason string
	var language, detector sql.NullString
	err := t.store.db.QueryRowContext(ctx, `SELECT state,reason,language,detector_version FROM topic_meeting WHERE opus_name=?`, opusName).Scan(&state, &reason, &language, &detector)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return view, err
	default:
		view.State, view.Reason = state, reason
		if state == topicStateQueued || state == topicStateRunning {
			view.State = "waiting"
		}
		if state == topicStateDone && language.String != "" && language.String != "unknown" && detector.String == topicsExpectedDetectorVersion {
			view.Language = language.String
		}
		view.ValidatedLanguage = topicsLanguageValidated(language.String, detector.String)
	}
	view.Confirmable = true
	if doc, err := t.store.document(ctx, opusName); err == nil && doc.Resolved != nil && !*doc.Resolved {
		view.Confirmable, view.BlockedReason = false, "marks-unresolved"
	}
	rows, err := t.store.db.QueryContext(ctx, `SELECT s.tag_id,l.label,l.definition,l.color,l.icon,s.rule,s.support_words FROM topic_suggestion s
 JOIN topic_library l ON l.tag_id=s.tag_id AND l.status='active'
 WHERE s.opus_name=? AND s.state='pending' AND NOT EXISTS(SELECT 1 FROM topic_verdict v WHERE v.opus_name=s.opus_name AND v.tag_id=s.tag_id)
 ORDER BY s.support_words DESC, l.label_folded`, opusName)
	if err != nil {
		return view, err
	}
	for rows.Next() {
		var v topicSuggestionView
		if err := rows.Scan(&v.TagID, &v.Label, &v.Definition, &v.Color, &v.Icon, &v.Rule, &v.SupportWords); err != nil {
			rows.Close()
			return view, err
		}
		v.Evidence = []topicEvidenceView{}
		view.Suggestions = append(view.Suggestions, v)
	}
	if err := rows.Close(); err != nil {
		return view, err
	}
	for i := range view.Suggestions {
		ev, err := t.store.db.QueryContext(ctx, `SELECT start_ms,end_ms,word_count,counted FROM topic_evidence WHERE opus_name=? AND tag_id=? ORDER BY ordinal`, opusName, view.Suggestions[i].TagID)
		if err != nil {
			return view, err
		}
		for ev.Next() {
			var e topicEvidenceView
			if err := ev.Scan(&e.StartMS, &e.EndMS, &e.WordCount, &e.Counted); err != nil {
				ev.Close()
				return view, err
			}
			view.Suggestions[i].Evidence = append(view.Suggestions[i].Evidence, e)
		}
		if err := ev.Close(); err != nil {
			return view, err
		}
	}
	decided, err := t.store.db.QueryContext(ctx, `SELECT v.tag_id,COALESCE(l.label,v.tag_id),v.verdict,v.source,COALESCE(v.actor_id,''),v.at FROM topic_verdict v
 LEFT JOIN topic_library l ON l.tag_id=v.tag_id WHERE v.opus_name=? ORDER BY v.at DESC`, opusName)
	if err != nil {
		return view, err
	}
	defer decided.Close()
	for decided.Next() {
		var d topicDecidedView
		var at int64
		if err := decided.Scan(&d.TagID, &d.Label, &d.Verdict, &d.Source, &d.By, &at); err != nil {
			return view, err
		}
		d.At = time.Unix(at, 0).UTC().Format(time.RFC3339)
		view.Decided = append(view.Decided, d)
	}
	return view, decided.Err()
}

// topicDecisionRequest is the body of POST annotations/topics/meetings/<id>/<tagId>.
type topicDecisionRequest struct {
	RequestID string `json:"requestId"`
	Decision  string `json:"decision"`
}

// topicDerivedID derives a stable id from parts: hex for request ids, an
// annotation operation id ("op_" and 26 base32 letters) when asked.
func topicDerivedID(operation bool, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	if operation {
		return "op_" + base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding).EncodeToString(sum[:16])
	}
	return hex.EncodeToString(sum[:])
}

func (s *annotationService) decideSuggestion(w http.ResponseWriter, r *http.Request, caller, meetingID, tagID string) {
	var in topicDecisionRequest
	if !readTopicRequest(w, r, &in) {
		return
	}
	if !annotateOpIDPattern.MatchString(in.RequestID) {
		writeJSONError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	switch in.Decision {
	case topicDecisionConfirm, topicDecisionReject, topicDecisionUndo, topicDecisionReopen:
	default:
		writeJSONError(w, http.StatusBadRequest, "decision must be confirm, reject, undo or reopen")
		return
	}
	if !isPlainMeetingID(meetingID) || !annotateOpIDPattern.MatchString(tagID) {
		http.NotFound(w, r)
		return
	}
	if !s.topicsOn(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()
	relPath, opusName, visible, ok := s.visibleRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	if _, err := s.authorizeRecording(ctx, caller, opusName); err != nil {
		s.answerFailure(w, r, "topics decide meeting="+meetingID, err)
		return
	}
	var body any
	var err error
	switch in.Decision {
	case topicDecisionConfirm:
		body, err = s.confirmSuggestion(ctx, caller, meetingID, opusName, relPath, visible, tagID, in.RequestID)
	case topicDecisionUndo:
		body, err = s.undoSuggestion(ctx, caller, meetingID, opusName, relPath, visible, tagID, in.RequestID)
	default:
		body, err = s.recordVerdictChange(ctx, caller, meetingID, opusName, relPath, tagID, in)
	}
	if err != nil {
		var refusal *topicError
		if errors.As(err, &refusal) {
			writeJSON(w, refusal.status, refusal.body)
			return
		}
		s.answerFailure(w, r, "topics decide meeting="+meetingID, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// confirmSuggestion marks the meeting with the topic, as the caller, and
// records the verdict in the same transaction.
func (s *annotationService) confirmSuggestion(ctx context.Context, caller, meetingID, opusName, relPath string, visible []string, tagID, clientRequest string) (any, error) {
	t := s.rt.topics
	requestID := topicDerivedID(false, "topic-confirm", opusName, tagID, caller, clientRequest)
	operationID := topicDerivedID(true, "topic-confirm-op", requestID)
	// Replay first: after the first confirm the suggestion is no longer
	// pending, so the checks below would refuse a retry.
	if body, ok, err := s.replayConfirm(ctx, caller, requestID, opusName, tagID); err != nil || ok {
		return body, err
	}
	topic, exists, err := topicByID(ctx, t.store.db, tagID)
	if err != nil {
		return nil, err
	}
	if !exists || topic.Status != topicStatusActive {
		return nil, refuseTopic(http.StatusConflict, "topic-retired")
	}
	var state string
	err = t.store.db.QueryRowContext(ctx, `SELECT state FROM topic_suggestion WHERE opus_name=? AND tag_id=?`, opusName, tagID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && state != "pending") {
		// The same confirm may have just committed.
		if body, ok, err := s.replayConfirm(ctx, caller, requestID, opusName, tagID); err != nil || ok {
			return body, err
		}
		return nil, refuseTopic(http.StatusConflict, "not-pending")
	}
	if err != nil {
		return nil, err
	}
	store := s.rt.annotationReads()
	namespace, err := store.Namespace(ctx)
	if err != nil {
		if errors.Is(err, errAnnotationIndexBuilding) {
			return nil, &annotateFailure{status: http.StatusServiceUnavailable, public: "tag index preparing", cause: err}
		}
		return nil, err
	}
	// Mark the tag the file already knows by this name, so one file never
	// shows two chips with one label.
	markID, markLabel, alias := tagID, topic.Label, false
	if doc, err := store.document(ctx, opusName); err == nil {
		var projected projectedDocument
		if len(doc.Annotations) > 0 && json.Unmarshal(doc.Annotations, &projected) == nil {
			for _, tag := range projected.Tags {
				if tag.ID == tagID {
					markID, markLabel, alias = tagID, tag.Label, false
					break
				}
				if foldTagLabel(tag.Label) == foldTagLabel(topic.Label) {
					markID, markLabel, alias = tag.ID, tag.Label, true
				}
			}
		}
	}
	op, _ := json.Marshal(map[string]any{"op": "mark", "tag": map[string]string{"id": markID, "label": markLabel}, "target": map[string]string{"kind": "meeting"}})
	style := annotateTagStyle{Label: markLabel}
	if topic.Color != nil {
		style.Color = *topic.Color
	}
	if topic.Icon != nil {
		style.Icon = *topic.Icon
	}
	var verdict topicDecidedView
	request := annotateWriteRequest{
		Accepted: true, ActorKind: "person", RequestID: requestID, OperationID: operationID,
		Ops: []json.RawMessage{op}, TagStyles: []annotateTagStyle{style}, Namespace: namespace,
		InTx: func(ctx context.Context, tx *sql.Tx, written annotateResult) error {
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM topic_suggestion WHERE opus_name=? AND tag_id=?`, opusName, tagID).Scan(&state); err != nil || state != "pending" {
				return refuseTopic(http.StatusConflict, "not-pending")
			}
			// Another write may have added a tag with this name since it was
			// chosen: never leave two chips with one name.
			if duplicateTagLabel(written.Annotations, markLabel) {
				return refuseTopic(http.StatusConflict, "retry")
			}
			now := t.store.unix()
			var rule string
			var words int
			var peak, margin sql.NullFloat64
			var defSHA, scorer string
			if err := tx.QueryRowContext(ctx, `SELECT rule,support_words,peak,min_margin,definition_sha,scorer_key FROM topic_suggestion WHERE opus_name=? AND tag_id=?`, opusName, tagID).Scan(&rule, &words, &peak, &margin, &defSHA, &scorer); err != nil {
				return err
			}
			local := sql.NullString{String: markID, Valid: markID != tagID}
			if _, err := tx.ExecContext(ctx, `INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at,operation_id,local_tag_id,payload_sha256,definition_sha,scorer_key,rule,support_words,peak,min_margin)
 VALUES(?,?,'confirmed','review',?,?,?,?,(SELECT payload_sha256 FROM topic_meeting WHERE opus_name=?),?,?,?,?,?,?)`,
				opusName, tagID, caller, now, operationID, local, opusName, defSHA, scorer, rule, words, peak, margin); err != nil {
				if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "constraint") {
					return refuseTopic(http.StatusConflict, "already-decided")
				}
				return err
			}
			if alias {
				if _, err := tx.ExecContext(ctx, `INSERT INTO topic_tag_alias(alias_tag_id,tag_id,created_at) VALUES(?,?,?) ON CONFLICT(alias_tag_id) DO NOTHING`, markID, tagID, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='decided',updated_at=? WHERE opus_name=? AND tag_id=?`, now, opusName, tagID); err != nil {
				return err
			}
			verdict = topicDecidedView{TagID: tagID, Label: topic.Label, Verdict: "confirmed", Source: "review", By: caller, At: time.Unix(now, 0).UTC().Format(time.RFC3339)}
			return nil
		},
	}
	result, err := s.commitDocument(ctx, meetingID, opusName, relPath, visible, caller, request)
	if err != nil {
		var failure *annotateFailure
		if errors.As(err, &failure) && failure.status == http.StatusConflict {
			if strings.Contains(failure.public, "different audio") {
				return nil, refuseTopic(http.StatusConflict, "marks-unresolved")
			}
			if strings.Contains(failure.public, "requestId was already used") {
				// The library's look changed between two tries: the first
				// confirm stands.
				if body, ok, err := s.replayConfirm(ctx, caller, requestID, opusName, tagID); err != nil || ok {
					return body, err
				}
			}
		}
		return nil, err
	}
	if result.Replayed {
		body, _, err := s.replayConfirm(ctx, caller, requestID, opusName, tagID)
		return body, err
	}
	return map[string]any{"verdict": verdict, "annotations": topicAnnotationsResponse(meetingID, result)}, nil
}

// duplicateTagLabel says whether a document has two tags with label.
func duplicateTagLabel(annotations json.RawMessage, label string) bool {
	var doc projectedDocument
	if len(annotations) == 0 || json.Unmarshal(annotations, &doc) != nil {
		return false
	}
	n := 0
	for _, tag := range doc.Tags {
		if foldTagLabel(tag.Label) == foldTagLabel(label) {
			n++
		}
	}
	return n > 1
}

// replayConfirm answers a confirm whose derived request already committed.
func (s *annotationService) replayConfirm(ctx context.Context, caller, requestID, opusName, tagID string) (any, bool, error) {
	store := s.rt.annotationReads()
	var response []byte
	err := store.db.QueryRowContext(ctx, `SELECT response FROM annotation_receipt WHERE caller=? AND request_id=?`, caller, requestID).Scan(&response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var result annotateResult
	if err := json.Unmarshal(response, &result); err != nil {
		return nil, false, err
	}
	// The verdict this confirm wrote, if it still stands (an undo or a later
	// decision may have replaced it).
	var verdict *topicDecidedView
	var v topicDecidedView
	var at int64
	err = store.db.QueryRowContext(ctx, `SELECT v.verdict,v.source,COALESCE(v.actor_id,''),v.at,COALESCE(l.label,v.tag_id) FROM topic_verdict v LEFT JOIN topic_library l ON l.tag_id=v.tag_id
 WHERE v.opus_name=? AND v.tag_id=? AND v.operation_id=?`, opusName, tagID, result.OperationID).Scan(&v.Verdict, &v.Source, &v.By, &at, &v.Label)
	switch {
	case err == nil:
		v.TagID, v.At = tagID, time.Unix(at, 0).UTC().Format(time.RFC3339)
		verdict = &v
	case !errors.Is(err, sql.ErrNoRows):
		return nil, false, err
	}
	return map[string]any{"verdict": verdict, "annotations": topicAnnotationsResponse(meetingStem(opusName), result), "replayed": true}, true, nil
}

func topicAnnotationsResponse(meetingID string, result annotateResult) annotationsWriteResponse {
	return annotationsWriteResponse{
		MeetingID: meetingID, StateToken: result.StateToken, Sync: result.Sync, Revision: result.Revision,
		OperationID: result.OperationID, Added: nonNilStrings(result.Added), Removed: nonNilStrings(result.Removed),
		NotFound: nonNilStrings(result.NotFound), Annotations: result.Annotations, Resolved: result.Resolved,
	}
}

// undoSuggestion takes back a confirm: the mark it wrote is undone in the
// file and the suggestion is pending again at once, in one transaction. A
// rejection is undone like a reopen.
func (s *annotationService) undoSuggestion(ctx context.Context, caller, meetingID, opusName, relPath string, visible []string, tagID, clientRequest string) (any, error) {
	t := s.rt.topics
	requestID := topicDerivedID(false, "topic-undo", opusName, tagID, caller, clientRequest)
	// Replays first: a successful undo removed the verdict this looks up.
	var response []byte
	if err := t.store.db.QueryRowContext(ctx, `SELECT response FROM annotation_receipt WHERE caller=? AND request_id=?`, caller, requestID).Scan(&response); err == nil {
		var result annotateResult
		if err := json.Unmarshal(response, &result); err != nil {
			return nil, err
		}
		return map[string]any{"annotations": topicAnnotationsResponse(meetingID, result), "replayed": true}, nil
	}
	var used int
	if err := t.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_receipt WHERE caller=? AND request_id=?`, caller, clientRequest).Scan(&used); err == nil && used > 0 {
		return s.recordVerdictChange(ctx, caller, meetingID, opusName, relPath, tagID, topicDecisionRequest{RequestID: clientRequest, Decision: topicDecisionReopen})
	}
	var verdict, source string
	var operation sql.NullString
	err := t.store.db.QueryRowContext(ctx, `SELECT verdict,source,operation_id FROM topic_verdict WHERE opus_name=? AND tag_id=?`, opusName, tagID).Scan(&verdict, &source, &operation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuseTopic(http.StatusConflict, "not-decided")
	}
	if err != nil {
		return nil, err
	}
	if verdict == "rejected" {
		return s.recordVerdictChange(ctx, caller, meetingID, opusName, relPath, tagID, topicDecisionRequest{RequestID: clientRequest, Decision: topicDecisionReopen})
	}
	if source != "review" || !operation.Valid {
		return nil, refuseTopic(http.StatusConflict, "not-undoable")
	}
	op, _ := json.Marshal(map[string]string{"op": "undo-operation", "operationId": operation.String})
	request := annotateWriteRequest{
		Accepted: true, ActorKind: "person", RequestID: requestID, Ops: []json.RawMessage{op},
		InTx: func(ctx context.Context, tx *sql.Tx, written annotateResult) error {
			// The confirm's mark may be gone already and the tag on the file
			// again under another write: then this is no longer that confirm.
			if len(written.Removed) == 0 {
				if carried, err := carriedTopicTags(ctx, tx, opusName); err != nil {
					return err
				} else if carried.ids[tagID] {
					return refuseTopic(http.StatusConflict, "confirmed")
				}
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM topic_verdict WHERE opus_name=? AND tag_id=? AND operation_id=?`, opusName, tagID, operation.String)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return refuseTopic(http.StatusConflict, "not-decided")
			}
			_, err = tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='pending',updated_at=? WHERE opus_name=? AND tag_id=? AND state='decided'`, t.store.unix(), opusName, tagID)
			return err
		},
	}
	result, err := s.commitDocument(ctx, meetingID, opusName, relPath, visible, caller, request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"annotations": topicAnnotationsResponse(meetingID, result)}, nil
}

// recordVerdictChange rejects or reopens: no file changes, so idempotency is
// a topic_receipt naming the meeting (retirement expires it).
func (s *annotationService) recordVerdictChange(ctx context.Context, caller, meetingID, opusName, relPath, tagID string, in topicDecisionRequest) (any, error) {
	t := s.rt.topics
	// Re-check the share as the caller, right now.
	if _, err := s.readDocument(ctx, caller, meetingID, opusName, relPath); err != nil {
		return nil, err
	}
	hash := topicRequestHash(in.Decision, opusName, tagID)
	var out any
	rescore := false
	err := t.store.inTx(ctx, func(tx *sql.Tx) error {
		var stored string
		var response []byte
		err := tx.QueryRowContext(ctx, `SELECT request_hash,response FROM topic_receipt WHERE caller=? AND request_id=?`, caller, in.RequestID).Scan(&stored, &response)
		if err == nil {
			if stored != hash {
				return refuseTopic(http.StatusConflict, "request-collision")
			}
			var replay map[string]any
			_ = json.Unmarshal(response, &replay)
			replay["replayed"] = true
			out = replay
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := t.store.notRetired(ctx, opusName); err != nil {
			return annotateNotFound(err)
		}
		now := t.store.unix()
		var verdict sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT verdict FROM topic_verdict WHERE opus_name=? AND tag_id=?`, opusName, tagID).Scan(&verdict); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var body map[string]any
		switch in.Decision {
		case topicDecisionReject:
			if verdict.Valid {
				return refuseTopic(http.StatusConflict, "already-decided")
			}
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM topic_suggestion WHERE opus_name=? AND tag_id=?`, opusName, tagID).Scan(&state); err != nil || state != "pending" {
				return refuseTopic(http.StatusConflict, "not-pending")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at,payload_sha256,definition_sha,scorer_key,rule,support_words,peak,min_margin)
 SELECT s.opus_name,s.tag_id,'rejected','review',?,?,m.payload_sha256,s.definition_sha,s.scorer_key,s.rule,s.support_words,s.peak,s.min_margin
 FROM topic_suggestion s LEFT JOIN topic_meeting m ON m.opus_name=s.opus_name WHERE s.opus_name=? AND s.tag_id=?`, caller, now, opusName, tagID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='decided',updated_at=? WHERE opus_name=? AND tag_id=?`, now, opusName, tagID); err != nil {
				return err
			}
			body = map[string]any{"verdict": topicDecidedView{TagID: tagID, Verdict: "rejected", Source: "review", By: caller, At: time.Unix(now, 0).UTC().Format(time.RFC3339)}}
		case topicDecisionReopen:
			if !verdict.Valid {
				return refuseTopic(http.StatusConflict, "not-decided")
			}
			if verdict.String == "confirmed" {
				// A confirm is reopened only once its tag is off the file:
				// while the meeting carries it, it would be confirmed again.
				carried, err := carriedTopicTags(ctx, tx, opusName)
				if err != nil {
					return err
				}
				topic := topicForScoring{TagID: tagID}
				_ = tx.QueryRowContext(ctx, `SELECT label_folded FROM topic_library WHERE tag_id=?`, tagID).Scan(&topic.LabelFolded)
				if carried.has(ctx, tx, topic) {
					return refuseTopic(http.StatusConflict, "confirmed")
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM topic_verdict WHERE opus_name=? AND tag_id=?`, opusName, tagID); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='pending',updated_at=? WHERE opus_name=? AND tag_id=? AND state='decided'`, now, opusName, tagID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				// Never a suggestion (a direct verdict): score it now.
				rescore = true
			}
			body = map[string]any{"reopened": tagID}
		}
		raw, _ := json.Marshal(body)
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_receipt(caller,request_id,request_hash,opus_name,response,created_at) VALUES(?,?,?,?,?,?)`, caller, in.RequestID, hash, opusName, raw, now); err != nil {
			return err
		}
		out = body
		return nil
	})
	if err != nil {
		return nil, err
	}
	if rescore {
		if err := t.rescoreMeetingTopic(ctx, opusName, tagID); err != nil {
			t.logf("reopen rescore %s %s: %v", opusName, tagID, err)
		}
	}
	return out, nil
}

// rescoreMeetingTopic scores one (meeting, topic) from stored vectors.
func (t *topicsService) rescoreMeetingTopic(ctx context.Context, opusName, tagID string) error {
	versions, err := t.currentVersions(ctx)
	if err != nil {
		return err
	}
	inputs, ready, err := t.store.scoringInputs(ctx, versions)
	if err != nil || !ready {
		return err
	}
	for _, topic := range inputs.Topics {
		if topic.TagID != tagID {
			continue
		}
		set, ok, err := t.vectors.meeting(ctx, opusName)
		if err != nil || !ok || set.ModelRevision != versions.ModelRevision {
			return err
		}
		single := topicScoringInputs{Versions: versions, Controls: inputs.Controls, Topics: []topicForScoring{topic}, ScorerKey: inputs.ScorerKey}
		scores := scoreMeetingTopics(set.passages(), single)
		return t.store.inTx(ctx, func(tx *sql.Tx) error {
			// As the rescorer does: the meeting, its vectors and the topic
			// must still be the ones scored.
			var key, state string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(embed_key,''),state FROM topic_meeting WHERE opus_name=?`, opusName).Scan(&key, &state); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			if key != set.EmbedKey || state != topicStateDone {
				return nil
			}
			var status, sha string
			if err := tx.QueryRowContext(ctx, `SELECT status,definition_sha FROM topic_library WHERE tag_id=?`, tagID).Scan(&status, &sha); err != nil || status != topicStatusActive || sha != topic.DefinitionSHA {
				return nil
			}
			if err := t.store.notRetired(ctx, opusName); err != nil {
				return nil
			}
			return t.store.applyScores(ctx, tx, opusName, set.PayloadSHA256, scores)
		})
	}
	return nil
}

// topicCoverage is GET annotations/topics/coverage, over the caller's
// visible meetings (optionally one room's).
type topicCoverage struct {
	Visible             int            `json:"visible"`
	Checked             int            `json:"checked"`
	Waiting             int            `json:"waiting"`
	NotChecked          int            `json:"notChecked"`
	Unavailable         map[string]int `json:"unavailable"`
	Blocked             int            `json:"blocked"`
	CheckedSince        string         `json:"checkedSince,omitempty"`
	UnvalidatedLanguage int            `json:"unvalidatedLanguage"`
	UnknownLanguage     int            `json:"unknownLanguage"`
}

func (s *annotationService) serveTopicCoverage(w http.ResponseWriter, r *http.Request, caller string) {
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	room := r.URL.Query().Get("room")
	var names []string
	for _, entry := range scope.entries {
		if entry.opusName != "" && (room == "" || entry.roomID == room) {
			names = append(names, entry.opusName)
		}
	}
	coverage, err := s.rt.topics.coverage(r.Context(), names)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	writeJSON(w, http.StatusOK, coverage)
}

func (t *topicsService) coverage(ctx context.Context, names []string) (topicCoverage, error) {
	names = uniqueNames(names)
	c := topicCoverage{Visible: len(names), Unavailable: map[string]int{}}
	set, _ := json.Marshal(names)
	rows, err := t.store.db.QueryContext(ctx, `SELECT state,reason,COALESCE(language,''),COALESCE(detector_version,'') FROM topic_meeting WHERE opus_name IN (SELECT value FROM json_each(?))`, string(set))
	if err != nil {
		return c, err
	}
	known := 0
	for rows.Next() {
		var state, reason, language, detector string
		if err := rows.Scan(&state, &reason, &language, &detector); err != nil {
			rows.Close()
			return c, err
		}
		known++
		switch state {
		case topicStateDone:
			c.Checked++
			switch {
			case topicsLanguageValidated(language, detector):
			case language == "" || language == "unknown" || detector != topicsExpectedDetectorVersion:
				c.UnknownLanguage++
			default:
				c.UnvalidatedLanguage++
			}
		case topicStateQueued, topicStateRunning:
			c.Waiting++
		case topicStateUnavailable:
			c.Unavailable[reason]++
		case topicStateBlocked:
			c.Blocked++
		}
	}
	if err := rows.Close(); err != nil {
		return c, err
	}
	c.NotChecked = c.Visible - known
	since, _ := t.store.meta(ctx, topicMetaBackfillSince)
	if since == "" {
		if at, _ := t.store.meta(ctx, topicMetaEnabledAt); at != "" {
			since = at
		}
	}
	if since != "all" {
		c.CheckedSince = since
	}
	return c, nil
}

// routeTopicQueue serves the cross-meeting review routes (S6).
func (s *annotationService) routeTopicQueue(w http.ResponseWriter, r *http.Request, caller string, parts []string) bool {
	return false
}

