package operator

import (
	ann "cassini-annotations"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The topic library (docs/proposals/automatic-tagging, "Topic library"): the
// tags people describe so Cassini can suggest them. Anyone who can read a
// meeting may edit it, so every route answers over the caller's visible set
// and serves no user ids.

const (
	maxTopicDefinitionRunes = 1000
	maxActiveTopics         = 200
	// maxTopicEditsPerHour bounds definition edits and creates per person:
	// each one rescores the archive for that topic.
	maxTopicEditsPerHour = 30
	// topicBroadShare is the share of checked meetings above which a topic is
	// flagged "very common".
	topicBroadShare = 0.6
)

const (
	topicStatusActive  = "active"
	topicStatusRetired = "retired"
)

// topicLibraryRow is one topic_library row.
type topicLibraryRow struct {
	TagID         string
	Label         string
	Definition    string
	DefinitionSHA string
	Color, Icon   *string
	Status        string
	MergedInto    *string
	Origin        string
	Revision      int
	UpdatedAt     int64
}

// topicView is a topic as USER routes serve it.
type topicView struct {
	TagID      string     `json:"tagId"`
	Label      string     `json:"label"`
	Definition string     `json:"definition"`
	Color      *string    `json:"color"`
	Icon       *string    `json:"icon"`
	Status     string     `json:"status"`
	MergedInto *string    `json:"mergedInto,omitempty"`
	Revision   int        `json:"revision"`
	UpdatedAt  string     `json:"updatedAt"`
	Preparing  bool       `json:"preparing"`
	Broad      bool       `json:"broad"`
	Usage      topicUsage `json:"usage"`
}

type topicUsage struct {
	Meetings int `json:"meetings"`
	Pending  int `json:"pending"`
}

// topicChange is one library event: the topic as it stood after the edit.
type topicChange struct {
	Kind       string  `json:"kind"` // create | edit | definition | retire | restore | merge | redacted
	Label      string  `json:"label,omitempty"`
	Definition string  `json:"definition,omitempty"`
	Color      *string `json:"color,omitempty"`
	Icon       *string `json:"icon,omitempty"`
	Status     string  `json:"status,omitempty"`
	// Scrubbed names fields removed because a redacted revision held the
	// same text: "label", "definition".
	Scrubbed []string `json:"scrubbed,omitempty"`
}

func topicDefinitionSHA(definition string) string { return topicTextSHA(definition) }

const topicLibraryColumns = `tag_id,label,definition,definition_sha,color,icon,status,merged_into,origin,revision,updated_at`

func scanTopicRow(scan func(...any) error) (topicLibraryRow, error) {
	var r topicLibraryRow
	err := scan(&r.TagID, &r.Label, &r.Definition, &r.DefinitionSHA, &r.Color, &r.Icon, &r.Status, &r.MergedInto, &r.Origin, &r.Revision, &r.UpdatedAt)
	return r, err
}

type topicQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func topicByID(ctx context.Context, q topicQuerier, tagID string) (topicLibraryRow, bool, error) {
	r, err := scanTopicRow(q.QueryRowContext(ctx, `SELECT `+topicLibraryColumns+` FROM topic_library WHERE tag_id=?`, tagID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (s *topicsStore) library(ctx context.Context) ([]topicLibraryRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+topicLibraryColumns+` FROM topic_library ORDER BY status, label_folded, tag_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []topicLibraryRow
	for rows.Next() {
		r, err := scanTopicRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// activeTopicByLabel is the active topic with this folded label.
func (s *topicsStore) activeTopicByLabel(ctx context.Context, label string) (topicLibraryRow, bool, error) {
	r, err := scanTopicRow(s.db.QueryRowContext(ctx, `SELECT `+topicLibraryColumns+` FROM topic_library WHERE label_folded=? AND status='active'`, foldTagLabel(label)).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// routeTopics serves annotations/topics and everything under it.
func (s *annotationService) routeTopics(w http.ResponseWriter, r *http.Request, caller, rest string) {
	t := s.rt.topics
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if rest == "" {
		parts = nil
	}
	if t == nil {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		} else {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "disabled"})
		}
		return
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		s.serveTopicLibrary(w, r, caller)
	case len(parts) == 0:
		writeMethodNotAllowed(w, http.MethodGet)
	case parts[0] == "library" && len(parts) == 1 && r.Method == http.MethodPost:
		s.createTopic(w, r, caller)
	case parts[0] == "library" && len(parts) == 2 && r.Method == http.MethodPost:
		s.editTopic(w, r, caller, parts[1])
	case parts[0] == "library" && len(parts) == 3 && parts[2] == "history" && r.Method == http.MethodGet:
		s.serveTopicHistory(w, r, caller, parts[1])
	case parts[0] == "library" && len(parts) == 3 && parts[2] == "rooms" && r.Method == http.MethodGet:
		s.serveTopicRooms(w, r, caller, parts[1])
	case parts[0] == "library" && len(parts) == 4 && parts[2] == "history" && r.Method == http.MethodPost:
		s.redactTopicRevision(w, r, caller, parts[1], parts[3])
	case parts[0] == "library":
		writeMethodNotAllowed(w, "GET, POST")
	default:
		if !s.routeTopicReview(w, r, caller, parts) {
			http.NotFound(w, r)
		}
	}
}

// topicCaller resolves the caller's visible set: 502 when it cannot be
// worked out, 404 when it is empty ("can tag" means "can read a meeting").
func (s *annotationService) topicCaller(w http.ResponseWriter, r *http.Request, caller string) (tagScope, bool) {
	store := s.rt.annotationReads()
	if store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, tagIndexUnavailableMessage)
		return tagScope{}, false
	}
	entries, ok := s.exapp.resolveVisibleMeetings(r.Context(), w, s.client, caller, s.logger, "annotations topics")
	if !ok {
		return tagScope{}, false
	}
	visible := visibleOpusNames(entries)
	if len(visible) == 0 {
		http.NotFound(w, r)
		return tagScope{}, false
	}
	return tagScope{store: store, caller: caller, entries: entries, visible: visible}, true
}

// topicsOn refuses a write while topic suggestions are off.
func (s *annotationService) topicsOn(w http.ResponseWriter) bool {
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "disabled"})
		return false
	}
	return true
}

func (s *annotationService) serveTopicLibrary(w http.ResponseWriter, r *http.Request, caller string) {
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	t := s.rt.topics
	reason := t.modelPaused(r.Context())
	library, err := t.libraryViews(r.Context(), scope.visible)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	out := map[string]any{"enabled": true, "ready": reason == "", "library": library}
	if reason != "" {
		out["reason"] = reason
	}
	writeJSON(w, http.StatusOK, out)
}

// libraryViews renders the library with counts over the visible set only.
func (t *topicsService) libraryViews(ctx context.Context, visible []string) ([]topicView, error) {
	rows, err := t.store.library(ctx)
	if err != nil {
		return nil, err
	}
	names := uniqueNames(visible)
	set, _ := json.Marshal(names)
	var checked int
	if err := t.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_meeting WHERE state=? AND opus_name IN (SELECT value FROM json_each(?))`, topicStateDone, string(set)).Scan(&checked); err != nil {
		return nil, err
	}
	versions, _ := t.currentVersions(ctx)
	out := make([]topicView, 0, len(rows))
	for _, row := range rows {
		v := topicView{TagID: row.TagID, Label: row.Label, Definition: row.Definition, Color: row.Color, Icon: row.Icon,
			Status: row.Status, MergedInto: row.MergedInto, Revision: row.Revision, UpdatedAt: time.Unix(row.UpdatedAt, 0).UTC().Format(time.RFC3339)}
		carriers, err := t.store.tagCarriers(ctx, row.TagID, names)
		if err != nil {
			return nil, err
		}
		v.Usage.Meetings = len(carriers)
		var confirmed int
		if err := t.store.db.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM topic_suggestion WHERE tag_id=?1 AND state='pending' AND opus_name IN (SELECT value FROM json_each(?2))),
 (SELECT COUNT(*) FROM topic_verdict WHERE tag_id=?1 AND verdict='confirmed' AND opus_name IN (SELECT value FROM json_each(?2)))`,
			row.TagID, string(set)).Scan(&v.Usage.Pending, &confirmed); err != nil {
			return nil, err
		}
		if row.Status == topicStatusActive {
			_, ready, err := t.store.textVector(ctx, topicTextDefinition, row.DefinitionSHA, versions)
			if err != nil {
				return nil, err
			}
			v.Preparing = !ready
			v.Broad = checked > 0 && float64(v.Usage.Pending+confirmed) > topicBroadShare*float64(checked)
		}
		out = append(out, v)
	}
	return out, nil
}

// topicRequest is the body of a library write.
type topicRequest struct {
	RequestID      string  `json:"requestId"`
	ExpectRevision *int    `json:"expectRevision"`
	Label          *string `json:"label"`
	Definition     *string `json:"definition"`
	Color          *string `json:"color"`
	Icon           *string `json:"icon"`
	Status         *string `json:"status"`
	FromTagID      string  `json:"fromTagId"`
	// Separate: a retired topic owns this label, and the person chose a new
	// topic over restoring it.
	Separate bool `json:"separate"`
}

func readTopicRequest(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "could not read the request")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return false
	}
	return true
}

// checkTopicRequest validates the fields a write sets.
func checkTopicRequest(in topicRequest) string {
	if in.RequestID == "" || !annotateOpIDPattern.MatchString(in.RequestID) {
		return "requestId is required"
	}
	if refusal := checkTagEdit(tagEdit{Label: in.Label, Color: in.Color, Icon: in.Icon}); refusal != "" {
		return refusal
	}
	if in.Definition != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*in.Definition)); n == 0 || n > maxTopicDefinitionRunes {
			return fmt.Sprintf("definition must be 1-%d characters", maxTopicDefinitionRunes)
		}
	}
	if in.Status != nil && *in.Status != topicStatusActive && *in.Status != topicStatusRetired {
		return "status must be active or retired"
	}
	return ""
}

// topicReplay answers a request id that was already used: the topic it
// wrote, as it stands now (receipts keep no topic text, so a redaction
// cannot be read back through one), or 409 when the request differs. done is
// false when the id is new.
func topicReplay(ctx context.Context, q topicQuerier, caller, requestID, hash string) (body any, status int, done bool, err error) {
	var stored string
	var response []byte
	err = q.QueryRowContext(ctx, `SELECT request_hash,response FROM topic_receipt WHERE caller=? AND request_id=?`, caller, requestID).Scan(&stored, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if stored != hash {
		return map[string]string{"error": "request-collision"}, http.StatusConflict, true, nil
	}
	var receipt struct {
		TagID string `json:"tagId"`
	}
	_ = json.Unmarshal(response, &receipt)
	row, exists, err := topicByID(ctx, q, receipt.TagID)
	if err != nil {
		return nil, 0, false, err
	}
	if !exists {
		return map[string]string{"error": "not-found"}, http.StatusNotFound, true, nil
	}
	return map[string]any{"topic": topicViewOf(row), "replayed": true}, http.StatusOK, true, nil
}

func topicRequestHash(parts ...any) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// replayed carries a replay out of a transaction.
type topicReplayed struct {
	status int
	body   any
}

func (r *topicReplayed) Error() string { return "replayed" }

// replayInTx answers a used request id from inside the write transaction,
// so two identical requests cannot both pass the check.
func replayInTx(ctx context.Context, tx *sql.Tx, caller, requestID, hash string) error {
	body, status, done, err := topicReplay(ctx, tx, caller, requestID, hash)
	if err != nil {
		return err
	}
	if done {
		return &topicReplayed{status: status, body: body}
	}
	return nil
}

func insertTopicReceipt(ctx context.Context, tx *sql.Tx, caller, requestID, hash, tagID string, now int64) error {
	response, _ := json.Marshal(map[string]string{"tagId": tagID})
	_, err := tx.ExecContext(ctx, `INSERT INTO topic_receipt(caller,request_id,request_hash,response,created_at) VALUES(?,?,?,?,?)`, caller, requestID, hash, response, now)
	return err
}

// topicEditsThisHour counts a person's writes that rescore the archive in the
// last hour: creates, definition edits and restores. A redacted event counts
// too, whatever it was, so redaction cannot reset the count.
func topicEditsThisHour(ctx context.Context, tx *sql.Tx, actor string, now int64) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_library_event WHERE actor=? AND at>?
 AND (redacted_at IS NOT NULL OR json_extract(change_json,'$.kind') IN ('create','definition','restore'))`, actor, now-3600).Scan(&n)
	return n, err
}

func appendTopicEvent(ctx context.Context, tx *sql.Tx, tagID string, revision int, actor string, at int64, change topicChange) error {
	raw, _ := json.Marshal(change)
	_, err := tx.ExecContext(ctx, `INSERT INTO topic_library_event(tag_id,revision,actor,at,change_json) VALUES(?,?,?,?,?)`, tagID, revision, actor, at, raw)
	return err
}

// topicError is a refusal with a code the UI acts on.
type topicError struct {
	status int
	body   map[string]any
}

func (e *topicError) Error() string { return fmt.Sprint(e.body["error"]) }

func refuseTopic(status int, code string, extra ...any) *topicError {
	body := map[string]any{"error": code}
	for i := 0; i+1 < len(extra); i += 2 {
		body[extra[i].(string)] = extra[i+1]
	}
	return &topicError{status: status, body: body}
}

func (s *annotationService) writeTopicFailure(w http.ResponseWriter, caller string, err error) {
	var refusal *topicError
	switch {
	case errors.As(err, &refusal):
		writeJSON(w, refusal.status, refusal.body)
	case errors.As(err, new(*topicReplayed)):
		var replay *topicReplayed
		errors.As(err, &replay)
		writeJSON(w, replay.status, replay.body)
	case errors.Is(err, errAnnotationIndexBuilding):
		writeJSONError(w, http.StatusServiceUnavailable, "the tag index is preparing; try again shortly")
	default:
		s.tagIndexFailed(w, caller, err)
	}
}

func (s *annotationService) createTopic(w http.ResponseWriter, r *http.Request, caller string) {
	var in topicRequest
	if !readTopicRequest(w, r, &in) {
		return
	}
	if in.Label == nil || in.Definition == nil {
		writeJSONError(w, http.StatusBadRequest, "label and definition are required")
		return
	}
	if refusal := checkTopicRequest(in); refusal != "" {
		writeJSONError(w, http.StatusBadRequest, refusal)
		return
	}
	if !s.topicsOn(w) {
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	ctx := r.Context()
	t := s.rt.topics
	label, definition := strings.TrimSpace(*in.Label), strings.TrimSpace(*in.Definition)
	hash := topicRequestHash("create", label, definition, in.Color, in.Icon, in.FromTagID, in.Separate)
	if body, status, done, err := topicReplay(ctx, t.store.db, caller, in.RequestID, hash); err != nil || done {
		if err != nil {
			s.tagIndexFailed(w, caller, err)
		} else {
			writeJSON(w, status, body)
		}
		return
	}
	// The library never adopts an id the caller cannot see: same label, two
	// ids across access groups, rather than a label leaked across them.
	if _, err := scope.store.Namespace(ctx); err != nil {
		s.writeTopicFailure(w, caller, err)
		return
	}
	tagID, origin := "", "manual"
	if in.FromTagID != "" {
		visible, err := scope.store.TagVisible(ctx, in.FromTagID, scope.visible)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		if !visible {
			http.NotFound(w, r)
			return
		}
		tagID, origin = in.FromTagID, "tag"
	} else {
		id, found, err := scope.store.ResolveLabel(ctx, label, scope.visible)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		if found {
			tagID, origin = id, "tag"
		}
	}
	minted := false
	if tagID != "" && in.Separate {
		if existing, exists, err := topicByID(ctx, t.store.db, tagID); err == nil && exists && existing.Status == topicStatusRetired {
			tagID = ""
		}
	}
	if tagID == "" {
		var err error
		if tagID, err = ann.NewAnnotationTagID(); err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		origin, minted = "manual", true
	}
	var view topicView
	err := t.store.inTx(ctx, func(tx *sql.Tx) error {
		if err := replayInTx(ctx, tx, caller, in.RequestID, hash); err != nil {
			return err
		}
		if !minted {
			// Re-checked here: a concurrent create or merge may have made
			// this tag a topic since the lookup above.
			existing, exists, err := topicByID(ctx, tx, tagID)
			if err != nil {
				return err
			}
			if exists && existing.Status == topicStatusActive {
				return refuseTopic(http.StatusConflict, "label-taken", "tagId", tagID)
			}
			if exists {
				return refuseTopic(http.StatusConflict, "topic-retired", "tagId", tagID)
			}
		}
		now := t.store.unix()
		if err := insertLibraryTopic(ctx, tx, caller, now, libraryInsert{TagID: tagID, Label: label, Definition: definition, Color: in.Color, Icon: in.Icon, Origin: origin}); err != nil {
			return err
		}
		row, _, err := topicByID(ctx, tx, tagID)
		if err != nil {
			return err
		}
		view = topicViewOf(row)
		view.Preparing = true
		return insertTopicReceipt(ctx, tx, caller, in.RequestID, hash, tagID, now)
	})
	if err != nil {
		s.writeTopicFailure(w, caller, err)
		return
	}
	t.wakeUp()
	writeJSON(w, http.StatusOK, map[string]any{"topic": view})
}

// libraryInsert is a new library topic.
type libraryInsert struct {
	TagID, Label, Definition string
	Color, Icon              *string
	Origin, ProposalID       string
}

// insertLibraryTopic adds a topic inside tx, under the library's rules: the
// hourly limit, the 200-topic cap and one active topic per label. It queues
// the topic's rescore in the same transaction.
func insertLibraryTopic(ctx context.Context, tx *sql.Tx, caller string, now int64, in libraryInsert) error {
	if edits, err := topicEditsThisHour(ctx, tx, caller, now); err != nil {
		return err
	} else if edits >= maxTopicEditsPerHour {
		return refuseTopic(http.StatusTooManyRequests, "rate-limited")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_library WHERE status='active'`).Scan(&active); err != nil {
		return err
	}
	if active >= maxActiveTopics {
		return refuseTopic(http.StatusConflict, "library-full", "limit", maxActiveTopics)
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT tag_id FROM topic_library WHERE label_folded=? AND status='active'`, foldTagLabel(in.Label)).Scan(&owner); err == nil {
		return refuseTopic(http.StatusConflict, "label-taken", "tagId", owner)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var proposal any
	if in.ProposalID != "" {
		proposal = in.ProposalID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,color,icon,status,origin,proposal_id,revision,created_by,created_at,updated_by,updated_at)
 VALUES(?,?,?,?,?,?,?,'active',?,?,1,?,?,?,?)`, in.TagID, in.Label, foldTagLabel(in.Label), in.Definition, topicDefinitionSHA(in.Definition), in.Color, in.Icon, in.Origin, proposal, caller, now, caller, now); err != nil {
		return err
	}
	if err := appendTopicEvent(ctx, tx, in.TagID, 1, caller, now, topicChange{Kind: "create", Label: in.Label, Definition: in.Definition, Color: in.Color, Icon: in.Icon, Status: topicStatusActive}); err != nil {
		return err
	}
	return addRescoreTx(ctx, tx, in.TagID)
}

func topicViewOf(row topicLibraryRow) topicView {
	return topicView{TagID: row.TagID, Label: row.Label, Definition: row.Definition, Color: row.Color, Icon: row.Icon, Status: row.Status,
		MergedInto: row.MergedInto, Revision: row.Revision, UpdatedAt: time.Unix(row.UpdatedAt, 0).UTC().Format(time.RFC3339)}
}

func (s *annotationService) editTopic(w http.ResponseWriter, r *http.Request, caller, tagID string) {
	var in topicRequest
	if !readTopicRequest(w, r, &in) {
		return
	}
	if refusal := checkTopicRequest(in); refusal != "" {
		writeJSONError(w, http.StatusBadRequest, refusal)
		return
	}
	if in.ExpectRevision == nil {
		writeJSONError(w, http.StatusBadRequest, "expectRevision is required")
		return
	}
	if !s.topicsOn(w) {
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	ctx := r.Context()
	t := s.rt.topics
	hash := topicRequestHash("edit", tagID, in)
	if body, status, done, err := topicReplay(ctx, t.store.db, caller, in.RequestID, hash); err != nil || done {
		if err != nil {
			s.tagIndexFailed(w, caller, err)
		} else {
			writeJSON(w, status, body)
		}
		return
	}
	// A new name or look is also written into the caller's own files, as
	// the Tag manager does: check now what would stop that, before anything
	// is saved.
	current, exists, err := topicByID(ctx, t.store.db, tagID)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	renaming := in.Label != nil && strings.TrimSpace(*in.Label) != current.Label
	restyling := (in.Color != nil && !sameStringPtr(in.Color, current.Color)) || (in.Icon != nil && !sameStringPtr(in.Icon, current.Icon))
	carriers := 0
	if renaming || restyling {
		names, err := scope.store.tagCarriers(ctx, tagID, scope.visible)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		carriers = len(names)
		if carriers > 0 && s.jobs.running(caller) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "busy"})
			return
		}
	}
	if renaming {
		// Another tag on the caller's meetings already has this name.
		owner, taken, err := scope.store.labelOwner(ctx, strings.TrimSpace(*in.Label), tagID, scope.visible)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		if taken {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "label-exists", "tagId": owner})
			return
		}
	}
	var before, after topicLibraryRow
	err = t.store.inTx(ctx, func(tx *sql.Tx) error {
		if err := replayInTx(ctx, tx, caller, in.RequestID, hash); err != nil {
			return err
		}
		row, exists, err := topicByID(ctx, tx, tagID)
		if err != nil {
			return err
		}
		if !exists {
			return refuseTopic(http.StatusNotFound, "not-found")
		}
		if row.Revision != *in.ExpectRevision {
			return refuseTopic(http.StatusConflict, "revision-conflict", "topic", topicViewOf(row))
		}
		before, after = row, row
		now := t.store.unix()
		kinds := []string{}
		rescore := false
		if in.Label != nil && strings.TrimSpace(*in.Label) != row.Label {
			after.Label = strings.TrimSpace(*in.Label)
			kinds = append(kinds, "edit")
		}
		if in.Color != nil && !sameStringPtr(in.Color, row.Color) {
			after.Color = in.Color
			kinds = append(kinds, "edit")
		}
		if in.Icon != nil && !sameStringPtr(in.Icon, row.Icon) {
			after.Icon = in.Icon
			kinds = append(kinds, "edit")
		}
		if in.Definition != nil && strings.TrimSpace(*in.Definition) != row.Definition {
			after.Definition = strings.TrimSpace(*in.Definition)
			after.DefinitionSHA = topicDefinitionSHA(after.Definition)
			kinds = append(kinds, "definition")
			rescore = true
		}
		if in.Status != nil && *in.Status != row.Status {
			after.Status = *in.Status
			if after.Status == topicStatusRetired {
				kinds = append(kinds, "retire")
			} else {
				kinds = append(kinds, "restore")
				after.MergedInto = nil
				rescore = true
			}
		}
		if len(kinds) == 0 {
			return insertTopicReceipt(ctx, tx, caller, in.RequestID, hash, tagID, now)
		}
		if rescore {
			if edits, err := topicEditsThisHour(ctx, tx, caller, now); err != nil {
				return err
			} else if edits >= maxTopicEditsPerHour {
				return refuseTopic(http.StatusTooManyRequests, "rate-limited")
			}
		}
		if after.Status == topicStatusActive {
			var owner string
			err := tx.QueryRowContext(ctx, `SELECT tag_id FROM topic_library WHERE label_folded=? AND status='active' AND tag_id!=?`, foldTagLabel(after.Label), tagID).Scan(&owner)
			if err == nil {
				return refuseTopic(http.StatusConflict, "label-taken", "tagId", owner)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if row.Status == topicStatusRetired {
				var active int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_library WHERE status='active'`).Scan(&active); err != nil {
					return err
				}
				if active >= maxActiveTopics {
					return refuseTopic(http.StatusConflict, "library-full", "limit", maxActiveTopics)
				}
			}
		}
		after.Revision = row.Revision + 1
		after.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `UPDATE topic_library SET label=?,label_folded=?,definition=?,definition_sha=?,color=?,icon=?,status=?,merged_into=?,revision=?,updated_by=?,updated_at=? WHERE tag_id=?`,
			after.Label, foldTagLabel(after.Label), after.Definition, after.DefinitionSHA, after.Color, after.Icon, after.Status, after.MergedInto, after.Revision, caller, now, tagID); err != nil {
			return err
		}
		kind := kinds[len(kinds)-1]
		if slicesContains(kinds, "definition") {
			kind = "definition"
		}
		if err := appendTopicEvent(ctx, tx, tagID, after.Revision, caller, now, topicChange{Kind: kind, Label: after.Label, Definition: after.Definition, Color: after.Color, Icon: after.Icon, Status: after.Status}); err != nil {
			return err
		}
		if after.Status == topicStatusRetired && row.Status == topicStatusActive {
			// Retired: its pending suggestions go; verdicts and marks stay.
			if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='withdrawn',updated_at=? WHERE tag_id=? AND state='pending'`, now, tagID); err != nil {
				return err
			}
		}
		if rescore {
			if err := addRescoreTx(ctx, tx, tagID); err != nil {
				return err
			}
		}
		return insertTopicReceipt(ctx, tx, caller, in.RequestID, hash, tagID, now)
	})
	if err != nil {
		s.writeTopicFailure(w, caller, err)
		return
	}
	var job *tagJob
	if after.Revision != before.Revision && carriers > 0 && (after.Label != before.Label || !sameStringPtr(after.Color, before.Color) || !sameStringPtr(after.Icon, before.Icon)) {
		job = s.restyleLibraryTag(ctx, scope, before, after)
	}
	t.wakeUp()
	writeJSON(w, http.StatusOK, map[string]any{"topic": topicViewOf(after), "job": anonymousTagJob(job)})
}

// anonymousTagJob is a job without its actor: library responses serve no
// user ids.
func anonymousTagJob(job *tagJob) *tagJob {
	if job == nil {
		return nil
	}
	copied := *job
	copied.Actor = ""
	return &copied
}

func sameStringPtr(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func slicesContains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// restyleLibraryTag starts the Tag manager's relabel and restyle job over the
// caller's recordings carrying the tag; nil when there are none.
func (s *annotationService) restyleLibraryTag(ctx context.Context, scope tagScope, before, after topicLibraryRow) *tagJob {
	targets, err := scope.store.tagCarriers(ctx, after.TagID, scope.visible)
	if err != nil || len(targets) == 0 {
		return nil
	}
	ops := []any{}
	kind := tagJobRestyle
	if after.Label != before.Label {
		kind = tagJobRename
		ops = append(ops, map[string]any{"op": "relabel", "tagId": after.TagID, "label": after.Label})
	}
	if !sameStringPtr(after.Color, before.Color) || !sameStringPtr(after.Icon, before.Icon) {
		op := map[string]any{"op": "restyle", "tagId": after.TagID}
		if after.Color != nil {
			op["color"] = *after.Color
		}
		if after.Icon != nil {
			op["icon"] = *after.Icon
		}
		ops = append(ops, op)
	}
	job, _, message := s.startTagJobFor(ctx, scope, tagJob{Kind: kind, TagID: after.TagID}, map[string]any{"ops": ops})
	if job == nil {
		s.logf("topics: library edit of %s did not start a tag job: %s", after.TagID, message)
	}
	return job
}

// topicRevisionView is one revision as the history serves it.
type topicRevisionView struct {
	Revision   int     `json:"revision"`
	Kind       string  `json:"kind"`
	Label      *string `json:"label,omitempty"`
	Definition *string `json:"definition,omitempty"`
	At         string  `json:"at"`
	Redacted   bool    `json:"redacted"`
	CanRedact  bool    `json:"canRedact"`
}

type topicEventRow struct {
	id       int64
	revision int
	actor    string
	at       int64
	change   topicChange
	redacted bool
}

func (s *topicsStore) topicEvents(ctx context.Context, tagID string) ([]topicEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,revision,actor,at,change_json,redacted_at IS NOT NULL FROM topic_library_event WHERE tag_id=? ORDER BY revision,id`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []topicEventRow
	for rows.Next() {
		var e topicEventRow
		var raw []byte
		if err := rows.Scan(&e.id, &e.revision, &e.actor, &e.at, &raw, &e.redacted); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &e.change)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *annotationService) serveTopicHistory(w http.ResponseWriter, r *http.Request, caller, tagID string) {
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if _, ok := s.topicCaller(w, r, caller); !ok {
		return
	}
	t := s.rt.topics
	row, exists, err := topicByID(r.Context(), t.store.db, tagID)
	if err != nil || !exists {
		if err != nil {
			s.tagIndexFailed(w, caller, err)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	events, err := t.store.topicEvents(r.Context(), tagID)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	admin := false
	adminKnown := false
	out := []topicRevisionView{}
	for _, e := range events {
		v := topicRevisionView{Revision: e.revision, Kind: e.change.Kind, At: time.Unix(e.at, 0).UTC().Format(time.RFC3339), Redacted: e.redacted}
		if !e.redacted {
			label, definition := e.change.Label, e.change.Definition
			v.Label, v.Definition = &label, &definition
			if slicesContains(e.change.Scrubbed, "label") {
				v.Label = nil
			}
			if slicesContains(e.change.Scrubbed, "definition") {
				v.Definition = nil
			}
			if e.revision != row.Revision {
				if e.actor == caller {
					v.CanRedact = true
				} else {
					if !adminKnown {
						admin, adminKnown = s.isAdmin(r.Context(), caller), true
					}
					v.CanRedact = admin
				}
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

// isAdmin says whether the caller is a Nextcloud administrator, by the probe
// the setup uses: only an admin may list the admin group.
func (s *annotationService) isAdmin(ctx context.Context, caller string) bool {
	if s.isAdminFn != nil {
		return s.isAdminFn(ctx, caller)
	}
	status, _, err := s.exapp.apiGetAs(ctx, &http.Client{Timeout: 10 * time.Second}, caller, withFormatJSON(s.exapp.ocsURL("/cloud/groups/admin")))
	return err == nil && status >= 200 && status < 300
}

func (s *annotationService) redactTopicRevision(w http.ResponseWriter, r *http.Request, caller, tagID, revisionText string) {
	var in struct {
		RequestID string `json:"requestId"`
		Action    string `json:"action"`
	}
	if !readTopicRequest(w, r, &in) {
		return
	}
	revision, err := strconv.Atoi(revisionText)
	if err != nil || in.Action != "redact" || !annotateOpIDPattern.MatchString(in.RequestID) {
		writeJSONError(w, http.StatusBadRequest, "send {requestId, action: \"redact\"} to a revision number")
		return
	}
	if !s.topicsOn(w) {
		return
	}
	if _, ok := s.topicCaller(w, r, caller); !ok {
		return
	}
	ctx := r.Context()
	t := s.rt.topics
	events, err := t.store.topicEvents(ctx, tagID)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	row, exists, err := topicByID(ctx, t.store.db, tagID)
	if err != nil || !exists {
		if err != nil {
			s.tagIndexFailed(w, caller, err)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	if revision == row.Revision {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "current-revision"})
		return
	}
	var authors []string
	for _, e := range events {
		if e.revision == revision {
			authors = append(authors, e.actor)
		}
	}
	if len(authors) == 0 {
		http.NotFound(w, r)
		return
	}
	if !slicesContains(authors, caller) && !s.isAdmin(ctx, caller) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not-allowed"})
		return
	}
	// The redacted text may have been copied into later revisions (a rename
	// keeps the definition). Every copy that is not the topic's current text
	// goes too; current text is the topic's own, and is edited instead.
	var redacted topicChange
	for _, e := range events {
		if e.revision == revision && !e.redacted {
			redacted = e.change
		}
	}
	scrubLabel := redacted.Label != "" && redacted.Label != row.Label
	scrubDefinition := redacted.Definition != "" && redacted.Definition != row.Definition
	now := t.store.unix()
	err = t.store.inTx(ctx, func(tx *sql.Tx) error {
		tombstone, _ := json.Marshal(topicChange{Kind: "redacted"})
		if _, err := tx.ExecContext(ctx, `UPDATE topic_library_event SET change_json=?,redacted_by=?,redacted_at=? WHERE tag_id=? AND revision=? AND redacted_at IS NULL`,
			tombstone, caller, now, tagID, revision); err != nil {
			return err
		}
		for _, e := range events {
			if e.revision == revision || e.redacted || e.revision == row.Revision {
				continue
			}
			change := e.change
			touched := false
			if scrubLabel && change.Label == redacted.Label {
				change.Label, change.Scrubbed, touched = "", append(change.Scrubbed, "label"), true
			}
			if scrubDefinition && change.Definition == redacted.Definition {
				change.Definition, change.Scrubbed, touched = "", append(change.Scrubbed, "definition"), true
			}
			if !touched {
				continue
			}
			raw, _ := json.Marshal(change)
			if _, err := tx.ExecContext(ctx, `UPDATE topic_library_event SET change_json=? WHERE id=?`, raw, e.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": topicRevisionView{Revision: revision, Kind: "redacted", Redacted: true}})
}

// serveTopicRooms names the rooms of the caller's meetings that carry a tag,
// for the editor's warning before a tag from one private room is described
// for everyone.
func (s *annotationService) serveTopicRooms(w http.ResponseWriter, r *http.Request, caller, tagID string) {
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	carriers, err := scope.store.tagCarriers(r.Context(), tagID, scope.visible)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	carrying := map[string]bool{}
	for _, name := range carriers {
		carrying[name] = true
	}
	rooms := map[string]string{}
	for _, entry := range scope.entries {
		if carrying[entry.opusName] {
			key := entry.roomID
			if key == "" {
				key = entry.roomName
			}
			rooms[key] = entry.roomName
		}
	}
	names := make([]string, 0, len(rooms))
	for _, name := range rooms {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, map[string]any{"rooms": names, "roomCount": len(rooms)})
}

// libraryOn: the library shapes tagging only while topic suggestions are on.
func (s *annotationService) libraryOn() bool {
	return s.rt.topics != nil && s.rt.currentSettings().TopicSuggestions.Enabled
}

// libraryActive says whether id is an active library topic.
func (s *annotationService) libraryActive(ctx context.Context, id string) (bool, error) {
	if !s.libraryOn() {
		return false, nil
	}
	row, exists, err := topicByID(ctx, s.rt.topics.store.db, id)
	return exists && row.Status == topicStatusActive, err
}

// libraryTagByLabel is the active library topic with this label.
func (s *annotationService) libraryTagByLabel(ctx context.Context, label string) (string, bool, error) {
	if !s.libraryOn() {
		return "", false, nil
	}
	row, found, err := s.rt.topics.store.activeTopicByLabel(ctx, label)
	return row.TagID, found, err
}

// libraryTagAppearance gives a library topic its library label, colour and
// icon when a file gets it for the first time.
func (s *annotationService) libraryTagAppearance(ctx context.Context, batch *ann.Batch) error {
	if !s.libraryOn() {
		return nil
	}
	rows, err := s.rt.topics.store.library(ctx)
	if err != nil {
		return err
	}
	if batch.InitialTags == nil {
		batch.InitialTags = map[string]ann.AnnotationTag{}
	}
	for _, row := range rows {
		if row.Status != topicStatusActive {
			continue
		}
		// The library's label; its colour and icon only where it has one, so
		// a topic described without a colour keeps the tag's own.
		tag, known := batch.InitialTags[row.TagID]
		tag.ID, tag.Label = row.TagID, row.Label
		if row.Color != nil {
			tag.Color = row.Color
		}
		if row.Icon != nil {
			tag.Icon = row.Icon
		}
		if tag.Icon == nil && !known {
			empty := ""
			tag.Icon = &empty
		}
		batch.InitialTags[row.TagID] = tag
	}
	return nil
}

// libraryFollowTagEdit updates a library topic before the Tag manager's
// rename or restyle job rewrites files, so later confirms use the new label
// and colour. A label another active topic holds is refused.
func (t *topicsService) libraryFollowTagEdit(ctx context.Context, tagID, caller string, label, color, icon *string) error {
	return t.store.inTx(ctx, func(tx *sql.Tx) error {
		row, exists, err := topicByID(ctx, tx, tagID)
		if err != nil || !exists {
			return err
		}
		after := row
		if label != nil {
			after.Label = strings.TrimSpace(*label)
		}
		if color != nil {
			after.Color = color
		}
		if icon != nil {
			after.Icon = icon
		}
		if after.Label == row.Label && sameStringPtr(after.Color, row.Color) && sameStringPtr(after.Icon, row.Icon) {
			return nil
		}
		if row.Status == topicStatusActive && after.Label != row.Label {
			var owner string
			err := tx.QueryRowContext(ctx, `SELECT tag_id FROM topic_library WHERE label_folded=? AND status='active' AND tag_id!=?`, foldTagLabel(after.Label), tagID).Scan(&owner)
			if err == nil {
				return refuseTopic(http.StatusConflict, "label-exists", "tagId", owner)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		now := t.store.unix()
		if _, err := tx.ExecContext(ctx, `UPDATE topic_library SET label=?,label_folded=?,color=?,icon=?,revision=revision+1,updated_by=?,updated_at=? WHERE tag_id=?`,
			after.Label, foldTagLabel(after.Label), after.Color, after.Icon, caller, now, tagID); err != nil {
			return err
		}
		return appendTopicEvent(ctx, tx, tagID, row.Revision+1, caller, now, topicChange{Kind: "edit", Label: after.Label, Definition: after.Definition, Color: after.Color, Icon: after.Icon, Status: row.Status})
	})
}

// libraryFollowMerge applies a Tag manager merge of from into into: from is
// retired into into, its pending suggestions withdrawn, its definition given
// to into when into has none, and its rejections copied to into where into
// has no verdict (a merge says the two mean the same). into is rescored.
func (t *topicsService) libraryFollowMerge(ctx context.Context, from, into, intoLabel, caller string) error {
	rescore := false
	err := t.store.inTx(ctx, func(tx *sql.Tx) error {
		source, exists, err := topicByID(ctx, tx, from)
		if err != nil || !exists {
			return err
		}
		now := t.store.unix()
		if source.Status == topicStatusActive {
			if _, err := tx.ExecContext(ctx, `UPDATE topic_library SET status='retired',merged_into=?,revision=revision+1,updated_by=?,updated_at=? WHERE tag_id=?`, into, caller, now, from); err != nil {
				return err
			}
			if err := appendTopicEvent(ctx, tx, from, source.Revision+1, caller, now, topicChange{Kind: "merge", Label: source.Label, Definition: source.Definition, Color: source.Color, Icon: source.Icon, Status: topicStatusRetired}); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='withdrawn',updated_at=? WHERE tag_id=? AND state='pending'`, now, from); err != nil {
				return err
			}
		}
		target, targetExists, err := topicByID(ctx, tx, into)
		if err != nil {
			return err
		}
		if !targetExists {
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM topic_library WHERE status='active'`).Scan(&active); err != nil {
				return err
			}
			if active >= maxActiveTopics {
				// The library is full: into stays undescribed.
				return nil
			}
			label := strings.TrimSpace(intoLabel)
			var owner string
			if err := tx.QueryRowContext(ctx, `SELECT tag_id FROM topic_library WHERE label_folded=? AND status='active'`, foldTagLabel(label)).Scan(&owner); err == nil {
				// Another topic holds the label; into stays undescribed.
				return nil
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO topic_library(tag_id,label,label_folded,definition,definition_sha,color,icon,status,origin,revision,created_by,created_at,updated_by,updated_at)
 VALUES(?,?,?,?,?,NULL,NULL,'active','tag',1,?,?,?,?)`, into, label, foldTagLabel(label), source.Definition, source.DefinitionSHA, caller, now, caller, now); err != nil {
				return err
			}
			if err := appendTopicEvent(ctx, tx, into, 1, caller, now, topicChange{Kind: "create", Label: label, Definition: source.Definition, Status: topicStatusActive}); err != nil {
				return err
			}
		} else if target.Status != topicStatusActive {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_verdict(opus_name,tag_id,verdict,source,actor_id,at,payload_sha256,definition_sha,scorer_key,rule,support_words,peak,min_margin)
 SELECT opus_name,?,verdict,'merge',actor_id,at,payload_sha256,definition_sha,scorer_key,rule,support_words,peak,min_margin FROM topic_verdict
 WHERE tag_id=? AND verdict='rejected' ON CONFLICT(opus_name,tag_id) DO NOTHING`, into, from); err != nil {
			return err
		}
		// A copied rejection decides into's suggestion on that meeting.
		if _, err := tx.ExecContext(ctx, `UPDATE topic_suggestion SET state='decided',updated_at=? WHERE tag_id=? AND state='pending'
 AND EXISTS(SELECT 1 FROM topic_verdict v WHERE v.opus_name=topic_suggestion.opus_name AND v.tag_id=topic_suggestion.tag_id)`, now, into); err != nil {
			return err
		}
		rescore = true
		return addRescoreTx(ctx, tx, into)
	})
	if err == nil && rescore {
		t.wakeUp()
	}
	return err
}
