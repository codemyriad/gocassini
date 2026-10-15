package operator

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func uvec(v ...float32) []float32 { return normalizedSum([][]float32{v}) }

func candidateAt(meeting string, day int, start int64, v []float32) proposalCandidate {
	return proposalCandidate{Opus: meeting, At: time.Date(2026, 9, day, 10, 0, 0, 0, time.UTC), StartMS: start, EndMS: start + 30000, Duration: 600000, Vector: v}
}

func TestClusteringKeepsRecurringGroupsAndIsDeterministic(t *testing.T) {
	var candidates []proposalCandidate
	// A theme on three meetings on three days, near enough to join.
	for i, m := range []string{"a.opus", "b.opus", "c.opus"} {
		candidates = append(candidates, candidateAt(m, i+1, 100000, uvec(1, 0.1*float32(i), 0, 0)))
		candidates = append(candidates, candidateAt(m, i+1, 200000, uvec(1, 0.05, 0.05, 0)))
	}
	// Another on two meetings only: not recurring enough.
	candidates = append(candidates, candidateAt("a.opus", 1, 300000, uvec(0, 0, 1, 0)), candidateAt("b.opus", 2, 300000, uvec(0, 0, 1, 0)))
	// One theme on three meetings of the same day: not three days.
	for _, m := range []string{"d.opus", "e.opus", "f.opus"} {
		candidates = append(candidates, candidateAt(m, 5, 100000, uvec(0, 1, 0, 0)))
	}
	groups, _ := clusterProposals(context.Background(), append([]proposalCandidate(nil), candidates...))
	if len(groups) != 1 || len(groups[0].meetings()) != 3 || len(groups[0].Members) != 6 || groups[0].days() != 3 {
		t.Fatalf("groups %+v", groups)
	}
	shuffled := append([]proposalCandidate(nil), candidates...)
	rand.New(rand.NewPCG(1, 2)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	again, _ := clusterProposals(context.Background(), shuffled)
	if !reflect.DeepEqual(groups, again) {
		t.Fatal("the same input in another order gives other groups")
	}
}

func TestProposalIdentityAcrossPassesAndModels(t *testing.T) {
	v := topicVersions{ModelRevision: "m@1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	group := proposalGroup{Members: []proposalCandidate{candidateAt("a.opus", 1, 0, uvec(1, 0)), candidateAt("b.opus", 2, 0, uvec(1, 0)), candidateAt("c.opus", 3, 0, uvec(1, 0))}, Centroid: uvec(1, 0)}
	stored := []storedProposal{
		{ID: "other", ModelRevision: "m@1", Recipe: "r1", Chunker: "natural-1", Centroid: uvec(0, 1), Members: []proposalCandidate{candidateAt("x.opus", 1, 0, nil)}},
		{ID: "same", ModelRevision: "m@1", Recipe: "r1", Chunker: "natural-1", Centroid: uvec(0, 1), Members: []proposalCandidate{candidateAt("a.opus", 1, 0, nil), candidateAt("b.opus", 2, 0, nil)}},
	}
	if got := matchProposals([]proposalGroup{group}, stored, v); got[0] != 1 {
		t.Fatalf("member overlap: %v", got)
	}
	stored[1].Members = nil
	stored[1].Centroid = uvec(1, 0.01)
	if got := matchProposals([]proposalGroup{group}, stored, v); got[0] != 1 {
		t.Fatalf("centroid: %v", got)
	}
	// From another model: centroids are never compared, members match by
	// meeting and overlapping time even though starts moved.
	stored[1].ModelRevision = "m@0"
	if got := matchProposals([]proposalGroup{group}, stored, v); got[0] != -1 {
		t.Fatalf("an old-space centroid must not match: %v", got)
	}
	stored[1].Members = []proposalCandidate{{Opus: "a.opus", StartMS: 5000, EndMS: 30000}, {Opus: "b.opus", StartMS: 2000, EndMS: 28000}}
	if got := matchProposals([]proposalGroup{group}, stored, v); got[0] != 1 {
		t.Fatalf("time overlap: %v", got)
	}
}

// seedRoomMeeting makes a checked meeting in a room with uncovered passages,
// its catalog entry and its searchable text.
func seedRoomMeeting(t *testing.T, s *topicsService, name, room string, day int, fileID int64, v topicVersions, vector []float32, text string, speakerNames ...string) {
	t.Helper()
	ctx := context.Background()
	key, err := s.store.speakerKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var speakerHashes []string
	for _, name := range speakerNames {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(name))
		speakerHashes = append(speakerHashes, hex.EncodeToString(mac.Sum(nil)))
	}
	if _, err := s.store.db.Exec(`INSERT INTO topic_meeting(opus_name,job_id,state,priority,model_revision,recipe_version,chunker_version,queued_at) VALUES(?,?,'done',0,?,?,?,1)
 ON CONFLICT(opus_name) DO UPDATE SET model_revision=excluded.model_revision`, name, meetingStem(name), v.ModelRevision, v.RecipeVersion, v.ChunkerVersion); err != nil {
		t.Fatal(err)
	}
	set := topicPassageSet{EmbedKey: "k" + name, ModelRevision: v.ModelRevision, ChunkerVersion: v.ChunkerVersion, RecipeVersion: v.RecipeVersion, SpeakerTermHashes: speakerHashes}
	for i := range 2 {
		set.Passages = append(set.Passages, topicStoredPassage{topicPassage: topicPassage{StartMS: int64(100000 + i*40000), EndMS: int64(130000 + i*40000), WordCount: 60, Vector: vector}, Ordinal: i, TokenCount: 70, Boundary: "sentence"})
	}
	set.Passages = append(set.Passages, topicStoredPassage{topicPassage: topicPassage{StartMS: 900000, EndMS: 960000, WordCount: 60, Vector: uvec(0, 0, 1, 0)}, Ordinal: 2})
	// The rest of the meeting: short passages of everyday talk, too short to
	// be proposed, that naming weighs the theme's words against.
	for i := range 5 {
		set.Passages = append(set.Passages, topicStoredPassage{topicPassage: topicPassage{StartMS: int64(300000 + i*60000), EndMS: int64(350000 + i*60000), WordCount: 30, Vector: uvec(1, 0, 0, 0)}, Ordinal: 3 + i})
	}
	if err := s.vectors.replaceMeeting(ctx, name, set); err != nil {
		t.Fatal(err)
	}
	date := time.Now().AddDate(0, 0, -day).Format("2006-01-02") + " 10:00"
	entry, _ := json.Marshal(map[string]string{"id": meetingStem(name), "roomId": room, "roomName": "Team " + room, "dateLabel": date, "audioPath": "./meetings/" + name})
	if err := s.rt.meetingMetadata.Put(ctx, fileID, name, entry); err != nil {
		t.Fatal(err)
	}
	rows := []searchRow{
		{SegmentID: "1", StartMS: 100000, EndMS: 170000, SpeakerID: "spk_anna", Text: text},
		{SegmentID: "2", StartMS: 900000, EndMS: 960000, SpeakerID: "spk_anna", Text: "thanks everyone, see you next week anna"},
	}
	// The rest of the meeting: everyday words every passage shares.
	for i, filler := range []string{"the plan for the week and the team", "the notes for the review with the team", "and the budget for the quarter", "the slides for the call", "the agenda for the day"} {
		rows = append(rows, searchRow{SegmentID: fmt.Sprintf("f%d", i), StartMS: int64(300000 + i*60000), EndMS: int64(350000 + i*60000), SpeakerID: "spk_anna", Text: filler})
	}
	if err := s.rt.searchStore.ReplaceMeeting(ctx, name, "d", searchRowSourceSegments, rows); err != nil {
		t.Fatal(err)
	}
}

func TestAProposalPassFindsARecurringThemeAndKeepsADismissal(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	embedTopicTestControls(t, s)
	v := topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	for i, text := range []string{"the parking garage permits are late again", "parking permits for the garage, still waiting", "garage parking permits arrived for some"} {
		seedRoomMeeting(t, s, fmt.Sprintf("m%d.opus", i), "room-a", i+1, int64(i+1), v, uvec(0, 1, 0, 0), text)
	}
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := loadProposals(ctx, s.store.db, `room_id='room-a' AND flags_json NOT LIKE '%"edges":true%'`)
	if err != nil || len(stored) != 1 {
		t.Fatalf("proposals %+v %v", stored, err)
	}
	var wrapups int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_proposal WHERE flags_json LIKE '%"edges":true%'`).Scan(&wrapups)
	if wrapups != 1 {
		t.Fatalf("the recurring goodbye is kept, flagged as greetings and wrap-ups: %d", wrapups)
	}
	p := stored[0]
	if p.State != proposalOpen || p.MeetingCount != 3 || !strings.Contains(p.DraftName, "parking") && !strings.Contains(p.DraftName, "garage") && !strings.Contains(p.DraftName, "permits") || strings.Contains(p.DraftName, "the") {
		t.Fatalf("proposal %+v", p)
	}
	for _, h := range p.Hints {
		if h == "anna" || h == "the" {
			t.Fatalf("hints %v", p.Hints)
		}
	}
	// Dismissed, then the model changes and the room is clustered again: the
	// same theme stays dismissed.
	s.store.db.Exec(`UPDATE topic_proposal SET state='dismissed' WHERE id=?`, p.ID)
	v2 := topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	s.store.db.Exec(`UPDATE topic_proposal SET model_revision='old@0' WHERE id=?`, p.ID)
	s.store.setMeta(ctx, topicMetaNightly, "0")
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ = loadProposals(ctx, s.store.db, `id=?`, p.ID)
	if len(stored) != 1 || stored[0].ID != p.ID || stored[0].State != proposalDismissed || stored[0].ModelRevision != v2.ModelRevision {
		t.Fatalf("after the model change %+v", stored)
	}
}

func TestProposalsServeOnlyFullyVisibleOnesAndNeedAHumanDefinition(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	db := topics.store.db
	insert := func(id, state string, members ...string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO topic_proposal(id,room_id,state,centroid,model_revision,recipe_version,chunker_version,draft_name,hints_json,flags_json,meeting_count,day_count,passage_count,created_at,updated_at)
 VALUES(?,'r1',?,?,'m','r1','natural-1','parking / garage','["parking","garage","permits"]','{}',3,3,3,1,1)`, id, state, encodeTopicVector(uvec(1, 0))); err != nil {
			t.Fatal(err)
		}
		for i, m := range members {
			db.Exec(`INSERT INTO topic_proposal_member(proposal_id,opus_name,start_ms,end_ms,sim) VALUES(?,?,?,?,1)`, id, m, i*1000, i*1000+500)
		}
	}
	insert("tp_visible", proposalOpen, "MEETING1.opus", "MEETING1.opus")
	insert("tp_hidden", proposalOpen, "MEETING1.opus", "SECRET.opus")
	insert("tp_empty", proposalOpen)
	insert("tp_recompute", proposalRecompute, "MEETING1.opus")
	rec := reviewCall(h, http.MethodGet, "proposals", "alice", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "tp_visible") || strings.Contains(body, "tp_hidden") || strings.Contains(body, "tp_empty") || strings.Contains(body, "tp_recompute") {
		t.Fatalf("listing: %d %s", rec.Code, body)
	}
	if rec := reviewCall(h, http.MethodPost, "proposals/tp_hidden", "alice", `{"requestId":"x","action":"dismiss"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("acting on a hidden one: %d", rec.Code)
	}
	if rec := reviewCall(h, http.MethodPost, "proposals/tp_visible", "alice", `{"requestId":"a0","action":"approve"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("approve without a definition: %d", rec.Code)
	}
	rec = reviewCall(h, http.MethodPost, "proposals/tp_visible", "alice", `{"requestId":"a1","action":"approve","label":"Parking permits","definition":"Staff car parking and its permits."}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "terms-from-meetings") || !strings.Contains(rec.Body.String(), "parking") {
		t.Fatalf("draft words need confirming: %d %s", rec.Code, rec.Body)
	}
	rec = reviewCall(h, http.MethodPost, "proposals/tp_visible", "alice", `{"requestId":"a2","action":"approve","label":"Parking permits","definition":"Staff car parking and its permits.","confirmTerms":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Topic struct {
			TagID string `json:"tagId"`
		} `json:"topic"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	row, exists, _ := topicByID(context.Background(), db, out.Topic.TagID)
	var origin, proposal string
	db.QueryRow(`SELECT origin,COALESCE(proposal_id,'') FROM topic_library WHERE tag_id=?`, out.Topic.TagID).Scan(&origin, &proposal)
	if !exists || row.Label != "Parking permits" || origin != "proposal" || proposal != "tp_visible" {
		t.Fatalf("topic %+v %s %s", row, origin, proposal)
	}
	var verdicts int
	db.QueryRow(`SELECT COUNT(*) FROM topic_verdict`).Scan(&verdicts)
	if verdicts != 0 {
		t.Fatal("approving creates no verdicts")
	}
	_ = s
}

func TestRetiringAHiddenMemberHidesTheProposalUntilRecomputed(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	db := topics.store.db
	db.Exec(`INSERT INTO topic_proposal(id,room_id,state,centroid,model_revision,recipe_version,chunker_version,draft_name,hints_json,flags_json,meeting_count,day_count,passage_count,created_at,updated_at)
 VALUES('tp1','r1','dismissed',x'00','m','r1','natural-1','a / b','[]','{}',3,3,3,1,1)`)
	db.Exec(`INSERT INTO topic_proposal_member VALUES('tp1','MEETING1.opus',0,500,1),('tp1','OTHER.opus',0,500,1)`)
	ctx := context.Background()
	if err := topics.store.inTx(ctx, func(tx *sql.Tx) error { return forgetTopicMeeting(ctx, tx, "OTHER.opus", meetingRetireReasonExpired) }); err != nil {
		t.Fatal(err)
	}
	var state, prior string
	var name *string
	db.QueryRow(`SELECT state,COALESCE(prior_state,''),draft_name FROM topic_proposal WHERE id='tp1'`).Scan(&state, &prior, &name)
	if state != proposalRecompute || prior != proposalDismissed || name != nil {
		t.Fatalf("state %s prior %s name %v", state, prior, name)
	}
	// The recompute never ran (crashed, or naming waits): nobody sees it.
	if rec := reviewCall(h, http.MethodGet, "proposals", "alice", ""); strings.Contains(rec.Body.String(), "tp1") {
		t.Fatalf("a proposal in recompute is served: %s", rec.Body)
	}
}

func TestRecomputeRebuildsFromLiveMeetingsAndGoesStaleBelowThree(t *testing.T) {
	s, _ := newTopicsTestService(t)
	ctx := context.Background()
	embedTopicTestControls(t, s)
	v := topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	for i := range 4 {
		seedRoomMeeting(t, s, fmt.Sprintf("m%d.opus", i), "room-a", i+1, int64(i+1), v, uvec(0, 1, 0, 0), "garage parking permits again")
	}
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	var id string
	s.store.db.QueryRow(`SELECT id FROM topic_proposal WHERE meeting_count=4`).Scan(&id)
	retire := func(name string) {
		t.Helper()
		retireTopicTestMeeting(t, s, name)
		if err := s.store.inTx(ctx, func(tx *sql.Tx) error { return forgetTopicMeeting(ctx, tx, name, meetingRetireReasonExpired) }); err != nil {
			t.Fatal(err)
		}
		if err := s.proposalPass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	state := func() (string, int) {
		var st string
		var n int
		s.store.db.QueryRow(`SELECT state,meeting_count FROM topic_proposal WHERE id=?`, id).Scan(&st, &n)
		return st, n
	}
	retire("m3.opus")
	if st, n := state(); st != proposalOpen || n != 3 {
		t.Fatalf("after one retirement: %s %d", st, n)
	}
	retire("m2.opus")
	if st, n := state(); st != proposalStale || n != 2 {
		t.Fatalf("below three meetings: %s %d", st, n)
	}
}

// proposalFixture seeds n meetings of one theme in room-a and runs a pass.
func proposalFixture(t *testing.T, n int, text string, speakers ...string) (*topicsService, topicVersions, string) {
	t.Helper()
	s, _ := newTopicsTestService(t)
	embedTopicTestControls(t, s)
	v := topicVersions{ModelRevision: "embeddinggemma-2-text-int8@rev1", ChunkerVersion: "natural-1", RecipeVersion: "r1"}
	for i := range n {
		seedRoomMeeting(t, s, fmt.Sprintf("m%d.opus", i), "room-a", i+1, int64(i+1), v, uvec(0, 1, 0, 0), text, speakers...)
	}
	if err := s.proposalPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	var id string
	s.store.db.QueryRow(`SELECT id FROM topic_proposal WHERE flags_json NOT LIKE '%"edges":true%'`).Scan(&id)
	if id == "" {
		t.Fatal("no proposal")
	}
	return s, v, id
}

func proposalState(s *topicsService, id string) string {
	var state string
	s.store.db.QueryRow(`SELECT state FROM topic_proposal WHERE id=?`, id).Scan(&state)
	return state
}

func TestSpeakersNamesNeverNameAProposal(t *testing.T) {
	s, _, id := proposalFixture(t, 3, "zelda says the garage parking permits are late, zelda again", "zelda")
	var hints string
	s.store.db.QueryRow(`SELECT hints_json FROM topic_proposal WHERE id=?`, id).Scan(&hints)
	if strings.Contains(hints, "zelda") || hints == "[]" {
		t.Fatalf("hints %s", hints)
	}
}

func TestAPassNeverOverwritesAProposalWaitingForItsRecompute(t *testing.T) {
	s, v, id := proposalFixture(t, 3, "garage parking permits")
	ctx := context.Background()
	// A retirement put it into recompute; the next pass of the room still
	// builds the same group from the meetings it read before.
	s.store.db.Exec(`UPDATE topic_proposal SET prior_state=state,state='recompute',draft_name=NULL WHERE id=?`, id)
	inputs, _, _ := s.store.scoringInputs(ctx, v)
	meetings, _ := s.roomMeetings(ctx, v)
	if err := s.clusterRoom(ctx, "room-a", meetings["room-a"], inputs); err != nil {
		t.Fatal(err)
	}
	var count int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_proposal WHERE flags_json NOT LIKE '%"edges":true%'`).Scan(&count)
	if proposalState(s, id) != proposalRecompute || count != 1 {
		t.Fatalf("state %s, %d proposals: the pass overwrote it or inserted a twin", proposalState(s, id), count)
	}
}

func TestADecidedProposalKeepsItsDecisionThroughRecompute(t *testing.T) {
	s, v, id := proposalFixture(t, 3, "garage parking permits")
	ctx := context.Background()
	s.store.db.Exec(`UPDATE topic_proposal SET state='approved' WHERE id=?`, id)
	retireTopicTestMeeting(t, s, "m2.opus")
	if err := s.store.inTx(ctx, func(tx *sql.Tx) error { return forgetTopicMeeting(ctx, tx, "m2.opus", meetingRetireReasonExpired) }); err != nil {
		t.Fatal(err)
	}
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	if state := proposalState(s, id); state != proposalApproved {
		t.Fatalf("after recompute: %s", state)
	}
	// The theme comes up again in a new meeting: it stays approved.
	seedRoomMeeting(t, s, "m9.opus", "room-a", 5, 9, v, uvec(0, 1, 0, 0), "garage parking permits")
	s.store.setMeta(ctx, topicMetaNightly, "0")
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	if state := proposalState(s, id); state != proposalApproved {
		t.Fatalf("after the theme came back: %s", state)
	}
}

func TestRecomputeWaitsForMembersOfAnotherModelAndKeepsADismissal(t *testing.T) {
	s, v, id := proposalFixture(t, 4, "garage parking permits")
	ctx := context.Background()
	s.store.db.Exec(`UPDATE topic_proposal SET state='dismissed' WHERE id=?`, id)
	// One member was embedded again under another model; another is retired.
	old := v
	old.ModelRevision = "embeddinggemma-2-text-int8@rev0"
	seedRoomMeeting(t, s, "m1.opus", "room-a", 2, 2, old, uvec(0, 1, 0, 0), "garage parking permits")
	retireTopicTestMeeting(t, s, "m3.opus")
	if err := s.store.inTx(ctx, func(tx *sql.Tx) error { return forgetTopicMeeting(ctx, tx, "m3.opus", meetingRetireReasonExpired) }); err != nil {
		t.Fatal(err)
	}
	inputs, _, _ := s.store.scoringInputs(ctx, v)
	if err := s.recomputeProposals(ctx, inputs); err != nil {
		t.Fatal(err)
	}
	if state := proposalState(s, id); state != proposalRecompute {
		t.Fatalf("it must wait, hidden, for the member's new vectors: %s", state)
	}
	seedRoomMeeting(t, s, "m1.opus", "room-a", 2, 2, v, uvec(0, 1, 0, 0), "garage parking permits")
	if err := s.recomputeProposals(ctx, inputs); err != nil {
		t.Fatal(err)
	}
	if state := proposalState(s, id); state != proposalDismissed {
		t.Fatalf("the dismissal is kept: %s", state)
	}
}

func TestClusteringScalesToABusyRoom(t *testing.T) {
	var candidates []proposalCandidate
	r := rand.New(rand.NewPCG(3, 4))
	for i := range 3000 {
		v := make([]float32, 768)
		for d := range v {
			v[d] = float32(r.NormFloat64())
		}
		candidates = append(candidates, candidateAt(fmt.Sprintf("m%d.opus", i%60), 1+i%28, int64(i)*1000, normalizedSum([][]float32{v})))
	}
	start := time.Now()
	clusterProposals(context.Background(), candidates)
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("3000 passages took %s", took)
	}
}

func TestASplitDismissedThemeStaysDismissedAndTheCapCountsOpenOnly(t *testing.T) {
	s, v, dismissedID := proposalFixture(t, 3, "garage parking permits")
	ctx := context.Background()
	s.store.db.Exec(`UPDATE topic_proposal SET state='dismissed' WHERE id=?`, dismissedID)
	// A second theme in the same room, and room for one open proposal only.
	for i := range 3 {
		seedRoomMeeting(t, s, fmt.Sprintf("n%d.opus", i), "room-a", 10+i, int64(20+i), v, uvec(0, 0.3, 0.95, 0), "canteen menu vegetarian options")
	}
	prev := proposalMaxOpen
	proposalMaxOpen = 1
	t.Cleanup(func() { proposalMaxOpen = prev })
	s.store.setMeta(ctx, topicMetaNightly, "0")
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	var open []string
	rows, _ := s.store.db.Query(`SELECT COALESCE(draft_name,'') FROM topic_proposal WHERE state='open'`)
	for rows.Next() {
		var name string
		rows.Scan(&name)
		open = append(open, name)
	}
	rows.Close()
	if proposalState(s, dismissedID) != proposalDismissed || len(open) != 1 || !strings.Contains(open[0], "canteen") && !strings.Contains(open[0], "menu") && !strings.Contains(open[0], "vegetarian") {
		t.Fatalf("dismissed %s, open %v: the dismissed theme took the only open slot", proposalState(s, dismissedID), open)
	}
	// The dismissed theme splits: one half matches its row, the other is a
	// new group made of dismissed passages, and must not be proposed.
	s.store.db.Exec(`DELETE FROM topic_proposal_member WHERE proposal_id=? AND opus_name='m2.opus'`, dismissedID)
	s.store.db.Exec(`INSERT INTO topic_proposal(id,room_id,state,centroid,model_revision,recipe_version,chunker_version,draft_name,hints_json,flags_json,meeting_count,day_count,passage_count,created_at,updated_at)
 VALUES('tp_other','room-a','dismissed',?,?,?,?,'x','[]','{}',3,3,3,1,1)`, encodeTopicVector(uvec(0, 1, 0, 0)), v.ModelRevision, v.RecipeVersion, v.ChunkerVersion)
	s.store.db.Exec(`INSERT INTO topic_proposal_member SELECT 'tp_other',opus_name,start_ms,end_ms,sim FROM topic_proposal_member WHERE proposal_id=?`, dismissedID)
	proposalMaxOpen = 5
	s.store.setMeta(ctx, topicMetaNightly, "0")
	if err := s.proposalPass(ctx); err != nil {
		t.Fatal(err)
	}
	var garage int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_proposal WHERE state='open' AND (draft_name LIKE '%garage%' OR draft_name LIKE '%parking%' OR draft_name LIKE '%permits%')`).Scan(&garage)
	if garage != 0 {
		t.Fatal("part of a dismissed theme came back open")
	}
}

func TestRecomputeMapsMovedPassagesAndKeepsStale(t *testing.T) {
	s, v, id := proposalFixture(t, 4, "garage parking permits")
	ctx := context.Background()
	// m0 was transcribed again: its passages start 2 s later.
	set, _, _ := s.vectors.meeting(ctx, "m0.opus")
	for i := range set.Passages {
		set.Passages[i].StartMS += 2000
		set.Passages[i].EndMS += 2000
	}
	if err := s.vectors.replaceMeeting(ctx, "m0.opus", set); err != nil {
		t.Fatal(err)
	}
	s.store.db.Exec(`UPDATE topic_proposal SET state='stale' WHERE id=?`, id)
	retireTopicTestMeeting(t, s, "m3.opus")
	if err := s.store.inTx(ctx, func(tx *sql.Tx) error { return forgetTopicMeeting(ctx, tx, "m3.opus", meetingRetireReasonExpired) }); err != nil {
		t.Fatal(err)
	}
	inputs, _, _ := s.store.scoringInputs(ctx, v)
	if err := s.recomputeProposals(ctx, inputs); err != nil {
		t.Fatal(err)
	}
	var meetings int
	s.store.db.QueryRow(`SELECT meeting_count FROM topic_proposal WHERE id=?`, id).Scan(&meetings)
	if state := proposalState(s, id); state != proposalStale || meetings != 3 {
		t.Fatalf("state %s with %d meetings: moved passages must map, and stale stays stale", state, meetings)
	}
}

func TestASkippedEmbedRefreshesTheSpeakersNames(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	s.runOnce(ctx)
	cli.write("unchanged-speakers", `["h1","h2"]`)
	publishTopicTestMeeting(t, s, "job-a", "transcript A", 2)
	s.runOnce(ctx)
	set, _, _ := s.vectors.meeting(ctx, name)
	if strings.Join(set.SpeakerTermHashes, ",") != "h1,h2" {
		t.Fatalf("speaker terms %v", set.SpeakerTermHashes)
	}
}

func TestApproveRechecksTheDraftAndTheAdoptedTagInItsTransaction(t *testing.T) {
	_, h, topics := topicLibraryService(t, "MEETING1.opus")
	db := topics.store.db
	t.Cleanup(func() { proposalBeforeDecide = nil })
	for _, id := range []string{"tp_renamed", "tp_adopt"} {
		if _, err := db.Exec(`INSERT INTO topic_proposal(id,room_id,state,centroid,model_revision,recipe_version,chunker_version,draft_name,hints_json,flags_json,meeting_count,day_count,passage_count,created_at,updated_at)
 VALUES(?,'r1','open',?,'m','r1','natural-1','garage','["garage"]','{}',3,3,3,1,1)`, id, encodeTopicVector(uvec(1, 0))); err != nil {
			t.Fatal(err)
		}
		db.Exec(`INSERT INTO topic_proposal_member(proposal_id,opus_name,start_ms,end_ms,sim) VALUES(?,'MEETING1.opus',0,500,1)`, id)
	}
	// A pass renames the draft after the request's own check.
	proposalBeforeDecide = func() {
		db.Exec(`UPDATE topic_proposal SET draft_name='budget',hints_json='["budget"]' WHERE id='tp_renamed'`)
	}
	rec := reviewCall(h, http.MethodPost, "proposals/tp_renamed", "alice", `{"requestId":"r1","action":"approve","label":"Budget","definition":"Money for the year."}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "budget") {
		t.Fatalf("the renamed draft's words need confirming: %d %s", rec.Code, rec.Body)
	}
	// The visible tag "hiring" becomes a topic after the label resolved to it.
	proposalBeforeDecide = func() {
		topics.store.inTx(context.Background(), func(tx *sql.Tx) error {
			return insertLibraryTopic(context.Background(), tx, "bob", 1, libraryInsert{TagID: "tag_hiring", Label: "hiring", Definition: "People joining.", Origin: "manual"})
		})
	}
	rec = reviewCall(h, http.MethodPost, "proposals/tp_adopt", "alice", `{"requestId":"r2","action":"approve","label":"hiring","definition":"People joining.","confirmTerms":true}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "label-taken") {
		t.Fatalf("the adopted tag became a topic meanwhile: %d %s", rec.Code, rec.Body)
	}
	if proposalState(topics, "tp_adopt") != proposalOpen {
		t.Fatal("the refused approve leaves the proposal open")
	}
}
