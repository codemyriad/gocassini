package operator

import (
	ann "cassini-annotations"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The proposal inbox (docs/proposals/automatic-tagging, "Recurring-topic
// proposals"). A proposal is served, and acted on, only by someone who can
// read every meeting it draws on: its name, hints and centroid come from all
// of them.

type proposalEvidence struct {
	MeetingID string `json:"meetingId"`
	Title     string `json:"title"`
	Date      string `json:"date"`
	StartMS   int64  `json:"startMs"`
	EndMS     int64  `json:"endMs"`
	Quote     string `json:"quote,omitempty"`
}

type proposalView struct {
	ID        string             `json:"id"`
	RoomID    string             `json:"roomId"`
	Room      string             `json:"room"`
	State     string             `json:"state"`
	DraftName string             `json:"draftName"`
	Hints     []string           `json:"hints"`
	Flags     map[string]bool    `json:"flags"`
	Nearest   *proposalNearest   `json:"nearest,omitempty"`
	Meetings  int                `json:"meetings"`
	Days      int                `json:"days"`
	Evidence  []proposalEvidence `json:"evidence"`
}

type proposalNearest struct {
	TagID string `json:"tagId"`
	Label string `json:"label"`
}

const proposalQuotes = 5

// visibleProposals lists the open and dismissed proposals of a room (all
// rooms for "") whose every member the caller can read. In SQL: a member
// exists, and none lies outside the visible set.
func (t *topicsService) visibleProposals(ctx context.Context, visible []string, roomID, id string) ([]storedProposal, error) {
	return visibleProposalsIn(ctx, t.store.db, visible, roomID, id)
}

func visibleProposalsIn(ctx context.Context, q topicQuerier, visible []string, roomID, id string) ([]storedProposal, error) {
	set, _ := json.Marshal(uniqueNames(visible))
	found, err := loadProposals(ctx, q, `state IN ('open','dismissed') AND draft_name IS NOT NULL
 AND (?1='' OR room_id=?1) AND (?2='' OR id=?2)
 AND EXISTS(SELECT 1 FROM topic_proposal_member m WHERE m.proposal_id=topic_proposal.id)
 AND NOT EXISTS(SELECT 1 FROM topic_proposal_member m WHERE m.proposal_id=topic_proposal.id AND m.opus_name NOT IN (SELECT value FROM json_each(?3)))`, roomID, id, string(set))
	if err != nil {
		return nil, err
	}
	// The members are read after the proposals, outside one transaction: a
	// recluster in between can add a meeting the caller cannot read. Check
	// again the members that will be quoted.
	readable := make(map[string]bool, len(visible))
	for _, name := range visible {
		readable[name] = true
	}
	out := found[:0]
	for _, p := range found {
		ok := len(p.Members) > 0
		for _, m := range p.Members {
			ok = ok && readable[m.Opus]
		}
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// proposalBeforeDecide runs between a decision's checks and its
// transaction; tests use it to change things in that gap.
var proposalBeforeDecide func()

func (s *annotationService) serveProposals(w http.ResponseWriter, r *http.Request, caller string) {
	if !s.rt.topicSuggestionsOn(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	room := strings.TrimPrefix(r.URL.Query().Get("room"), "id:")
	t := s.rt.topics
	stored, err := t.visibleProposals(r.Context(), scope.visible, room, "")
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	views := make([]proposalView, 0, len(stored))
	for _, p := range stored {
		view, err := t.proposalViewFor(r.Context(), p, scope.entries)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		views = append(views, view)
	}
	// Flagged ones last: routine business and greetings start collapsed.
	sort.SliceStable(views, func(i, j int) bool {
		fi := views[i].Flags["routine"] || views[i].Flags["edges"]
		fj := views[j].Flags["routine"] || views[j].Flags["edges"]
		return !fi && fj
	})
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "proposals": views})
}

// proposalViewFor renders a proposal for a caller who can read all of it.
func (t *topicsService) proposalViewFor(ctx context.Context, p storedProposal, entries []catalogHydration) (proposalView, error) {
	byName := map[string]catalogHydration{}
	for _, e := range entries {
		byName[e.opusName] = e
	}
	view := proposalView{ID: p.ID, State: p.State, DraftName: p.DraftName, Hints: p.Hints, Flags: map[string]bool{"edges": p.Flags["edges"]},
		Meetings: p.MeetingCount, Days: p.DayCount, Evidence: []proposalEvidence{}}
	if view.Hints == nil {
		view.Hints = []string{}
	}
	var roomID string
	_ = t.store.db.QueryRowContext(ctx, `SELECT room_id FROM topic_proposal WHERE id=?`, p.ID).Scan(&roomID)
	view.RoomID = roomID
	members := memberNames(p.Members)
	for _, name := range members {
		if e := byName[name]; e.roomName != "" {
			view.Room = e.roomName
			break
		}
	}
	// routine: the group covers most of the caller's own checked meetings in
	// the room over its dates. Computed here, never stored: a count over the
	// whole room would count meetings this caller cannot read.
	routine, err := t.routineFor(ctx, p, roomID, entries)
	if err != nil {
		return view, err
	}
	view.Flags["routine"] = routine
	if p.NearestTag != "" {
		var label string
		if err := t.store.db.QueryRowContext(ctx, `SELECT label FROM topic_library WHERE tag_id=? AND status='active'`, p.NearestTag).Scan(&label); err == nil {
			view.Nearest = &proposalNearest{TagID: p.NearestTag, Label: label}
		}
	}
	seen := map[string]bool{}
	for _, m := range p.Members {
		if seen[m.Opus] || len(view.Evidence) >= proposalQuotes {
			continue
		}
		seen[m.Opus] = true
		e := byName[m.Opus]
		ev := proposalEvidence{MeetingID: e.id, Title: e.title, Date: e.dateLabel, StartMS: m.StartMS, EndMS: m.EndMS}
		ev.Quote = t.rangeQuote(ctx, m.Opus, m.StartMS, m.EndMS)
		view.Evidence = append(view.Evidence, ev)
	}
	return view, nil
}

// routineFor says whether a proposal's meetings are more than half of the
// caller's visible checked meetings in its room over its date range.
func (t *topicsService) routineFor(ctx context.Context, p storedProposal, roomID string, entries []catalogHydration) (bool, error) {
	if t.rt.meetingMetadata == nil || roomID == "" {
		return false, nil
	}
	members := memberNames(p.Members)
	info, err := t.rt.meetingMetadata.meetingInfo(ctx, members)
	if err != nil {
		return false, err
	}
	var from, to time.Time
	for _, m := range info {
		if from.IsZero() || m.At.Before(from) {
			from = m.At
		}
		if m.At.After(to) {
			to = m.At
		}
	}
	var visible []string
	for _, e := range entries {
		if e.roomID == roomID {
			visible = append(visible, e.opusName)
		}
	}
	set, _ := json.Marshal(uniqueNames(visible))
	rows, err := t.store.db.QueryContext(ctx, `SELECT opus_name FROM topic_meeting WHERE state='done' AND opus_name IN (SELECT value FROM json_each(?))`, string(set))
	if err != nil {
		return false, err
	}
	var done []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return false, err
		}
		done = append(done, name)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	dates, err := t.rt.meetingMetadata.meetingInfo(ctx, done)
	if err != nil {
		return false, err
	}
	inRange := 0
	for _, m := range dates {
		if !m.At.Before(from) && !m.At.After(to) {
			inRange++
		}
	}
	return inRange > 0 && len(members)*2 > inRange, nil
}

// rangeQuote is a short quote of a time range from the search index.
func (t *topicsService) rangeQuote(ctx context.Context, opusName string, start, end int64) string {
	search := t.rt.searchStore
	if search == nil {
		return ""
	}
	rows, err := search.db.QueryContext(ctx, `SELECT text FROM segment_ref WHERE opus_name=? AND end_ms>? AND start_ms<? ORDER BY start_ms LIMIT 4`, opusName, start, end)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var text string
		if rows.Scan(&text) != nil {
			return ""
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(strings.TrimSpace(text))
	}
	quote := []rune(b.String())
	if len(quote) > topicQuoteRunes {
		return strings.TrimSpace(string(quote[:topicQuoteRunes])) + "…"
	}
	return string(quote)
}

// proposalAction is the body of POST annotations/topics/proposals/<id>.
type proposalAction struct {
	RequestID  string  `json:"requestId"`
	Action     string  `json:"action"`
	Label      *string `json:"label"`
	Definition *string `json:"definition"`
	Color      *string `json:"color"`
	TagID      string  `json:"tagId"`
	// ConfirmTerms: the person saw that their label or definition shares
	// words with the draft name, which comes from these meetings.
	ConfirmTerms bool `json:"confirmTerms"`
}

func (s *annotationService) decideProposal(w http.ResponseWriter, r *http.Request, caller, id string) {
	var in proposalAction
	if !readTopicRequest(w, r, &in) {
		return
	}
	if !annotateOpIDPattern.MatchString(in.RequestID) {
		writeJSONError(w, http.StatusBadRequest, "requestId is required")
		return
	}
	switch in.Action {
	case "approve", "link", "dismiss", "restore":
	default:
		writeJSONError(w, http.StatusBadRequest, "action must be approve, link, dismiss or restore")
		return
	}
	if in.Action == "approve" {
		if in.Label == nil || in.Definition == nil {
			writeJSONError(w, http.StatusBadRequest, "a label and a definition, written by you, are required")
			return
		}
		if refusal := checkTopicRequest(topicRequest{RequestID: in.RequestID, Label: in.Label, Definition: in.Definition, Color: in.Color}); refusal != "" {
			writeJSONError(w, http.StatusBadRequest, refusal)
			return
		}
	}
	if !s.topicsOn(w, r) {
		return
	}
	scope, ok := s.topicCaller(w, r, caller)
	if !ok {
		return
	}
	ctx := r.Context()
	t := s.rt.topics
	hash := topicRequestHash("proposal", id, in)
	if body, status, done, err := proposalReplay(ctx, t.store.db, caller, in.RequestID, hash); err != nil || done {
		if err != nil {
			s.tagIndexFailed(w, caller, err)
		} else {
			writeJSON(w, status, body)
		}
		return
	}
	// Visible means every member is readable; a proposal the caller cannot
	// fully read is answered as absent.
	visible, err := loadProposalsVisible(ctx, t, scope.visible, id)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	if visible == nil {
		http.NotFound(w, r)
		return
	}
	p := *visible
	if in.Action == "approve" {
		draft := append([]string{p.DraftName}, p.Hints...)
		if terms := sharedTopicTerms(*in.Label+" "+*in.Definition, draft); len(terms) > 0 && !in.ConfirmTerms {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "terms-from-meetings", "terms": terms})
			return
		}
	}
	// The new topic's id, as a create resolves it: a tag the caller can see
	// with this label is adopted, so one label keeps one id.
	var topicID, origin string
	if in.Action == "approve" {
		if _, err := scope.store.Namespace(ctx); err != nil {
			s.writeTopicFailure(w, caller, err)
			return
		}
		label := strings.TrimSpace(*in.Label)
		id, found, err := scope.store.ResolveLabel(ctx, label, scope.visible)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		if found {
			if existing, exists, err := topicByID(ctx, t.store.db, id); err != nil {
				s.tagIndexFailed(w, caller, err)
				return
			} else if exists {
				code := "label-taken"
				if existing.Status != topicStatusActive {
					code = "topic-retired"
				}
				writeJSON(w, http.StatusConflict, map[string]string{"error": code, "tagId": id})
				return
			}
			topicID, origin = id, "proposal"
		} else {
			if topicID, err = ann.NewAnnotationTagID(); err != nil {
				s.tagIndexFailed(w, caller, err)
				return
			}
			origin = "proposal"
		}
	}
	if in.Action == "link" {
		row, exists, err := topicByID(ctx, t.store.db, in.TagID)
		if err != nil {
			s.tagIndexFailed(w, caller, err)
			return
		}
		if !exists || row.Status != topicStatusActive {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "topic-retired"})
			return
		}
	}
	if proposalBeforeDecide != nil {
		proposalBeforeDecide()
	}
	var body map[string]any
	err = t.store.inTx(ctx, func(tx *sql.Tx) error {
		if body, err = proposalReplayInTx(ctx, tx, caller, in.RequestID, hash); err != nil || body != nil {
			return err
		}
		// Again inside the transaction: a pass since the check may have
		// added a member the caller cannot read.
		still, err := visibleProposalsIn(ctx, tx, scope.visible, "", id)
		if err != nil {
			return err
		}
		if len(still) == 0 {
			return refuseTopic(http.StatusNotFound, "not-found")
		}
		state := still[0].State
		want := proposalOpen
		if in.Action == "restore" {
			want = proposalDismissed
		}
		if state != want {
			return refuseTopic(http.StatusConflict, "proposal-"+state)
		}
		if in.Action == "approve" {
			// The words are checked against the proposal as it is now: a
			// pass may have renamed it since.
			draft := append([]string{still[0].DraftName}, still[0].Hints...)
			if terms := sharedTopicTerms(*in.Label+" "+*in.Definition, draft); len(terms) > 0 && !in.ConfirmTerms {
				return refuseTopic(http.StatusConflict, "terms-from-meetings", "terms", terms)
			}
			// An adopted tag may have become a topic since it was resolved.
			if existing, exists, err := topicByID(ctx, tx, topicID); err != nil {
				return err
			} else if exists {
				code := "label-taken"
				if existing.Status != topicStatusActive {
					code = "topic-retired"
				}
				return refuseTopic(http.StatusConflict, code, "tagId", topicID)
			}
		}
		now := t.store.unix()
		var next string
		var linked any
		switch in.Action {
		case "approve":
			label, definition := strings.TrimSpace(*in.Label), strings.TrimSpace(*in.Definition)
			if err := insertLibraryTopic(ctx, tx, caller, now, libraryInsert{TagID: topicID, Label: label, Definition: definition, Color: in.Color, Origin: origin, ProposalID: id}); err != nil {
				return err
			}
			next, linked = proposalApproved, topicID
		case "link":
			if err := addRescoreTx(ctx, tx, in.TagID); err != nil {
				return err
			}
			next, linked = proposalLinked, in.TagID
		case "dismiss":
			next = proposalDismissed
		case "restore":
			next = proposalOpen
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topic_proposal SET state=?,linked_tag_id=COALESCE(?,linked_tag_id),decided_by=?,decided_at=?,updated_at=? WHERE id=?`,
			next, linked, caller, now, now, id); err != nil {
			return err
		}
		body = map[string]any{"proposal": map[string]string{"id": id, "state": next}}
		if topicID != "" {
			body["topic"] = map[string]string{"tagId": topicID}
		}
		raw, _ := json.Marshal(body)
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_receipt(caller,request_id,request_hash,response,created_at) VALUES(?,?,?,?,?)`, caller, in.RequestID, hash, raw, now); err != nil {
			return err
		}
		for _, name := range memberNames(p.Members) {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO topic_receipt_target(caller,request_id,opus_name) VALUES(?,?,?)`, caller, in.RequestID, name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.writeTopicFailure(w, caller, err)
		return
	}
	t.wakeUp()
	writeJSON(w, http.StatusOK, body)
}

func loadProposalsVisible(ctx context.Context, t *topicsService, visible []string, id string) (*storedProposal, error) {
	found, err := t.visibleProposals(ctx, visible, "", id)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

func proposalReplay(ctx context.Context, q topicQuerier, caller, requestID, hash string) (any, int, bool, error) {
	var stored string
	var response []byte
	err := q.QueryRowContext(ctx, `SELECT request_hash,response FROM topic_receipt WHERE caller=? AND request_id=?`, caller, requestID).Scan(&stored, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if stored != hash {
		return map[string]string{"error": "request-collision"}, http.StatusConflict, true, nil
	}
	var body map[string]any
	_ = json.Unmarshal(response, &body)
	if body == nil {
		body = map[string]any{}
	}
	body["replayed"] = true
	return body, http.StatusOK, true, nil
}

func proposalReplayInTx(ctx context.Context, tx *sql.Tx, caller, requestID, hash string) (map[string]any, error) {
	body, status, done, err := proposalReplay(ctx, tx, caller, requestID, hash)
	if err != nil || !done {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, refuseTopic(status, "request-collision")
	}
	return body.(map[string]any), nil
}
