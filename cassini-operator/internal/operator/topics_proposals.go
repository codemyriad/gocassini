package operator

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Recurring-topic proposals (docs/proposals/automatic-tagging, "Recurring-
// topic proposals"): per Talk room, passages that no topic covers are grouped
// by meaning, and a group that recurs over several meetings and days becomes
// a proposal people can turn into a topic. Pure Go over the stored vectors.

const (
	proposalMinWords       = 40
	proposalJoinCosine     = 0.90
	proposalMinMeetings    = 3
	proposalMinDays        = 3
	proposalMaxOpenDefault = 12
	proposalWindow         = 90 * 24 * time.Hour
	proposalRoomEvery      = 6 * time.Hour
	proposalNightly        = 24 * time.Hour
	proposalEdgeShare      = 0.05
	proposalNearestCosine  = 0.90
	proposalMatchOverlap   = 0.5
	proposalMatchCentroid  = 0.95
)

// Proposal states.
const (
	proposalOpen      = "open"
	proposalApproved  = "approved"
	proposalLinked    = "linked"
	proposalDismissed = "dismissed"
	proposalStale     = "stale"
	proposalRecompute = "recompute"
)

const topicMetaNightly = "proposals_nightly"

// proposalMaxOpen caps the open proposals of a room (a variable for tests).
var proposalMaxOpen = proposalMaxOpenDefault

// topicMeetingInfo is what clustering needs from a meeting's catalog entry.
type topicMeetingInfo struct {
	RoomID string
	At     time.Time
}

// meetingInfo reads the room and date of meetings from the metadata index.
func (s *meetingMetadataStore) meetingInfo(ctx context.Context, names []string) (map[string]topicMeetingInfo, error) {
	out := map[string]topicMeetingInfo{}
	if s == nil || len(names) == 0 {
		return out, nil
	}
	set, _ := json.Marshal(uniqueNames(names))
	rows, err := s.db.QueryContext(ctx, `SELECT opus_name,entry_json FROM meeting_metadata WHERE opus_name IN (SELECT value FROM json_each(?)) ORDER BY indexed_at`, string(set))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		var entry struct {
			RoomID    string `json:"roomId"`
			DateLabel string `json:"dateLabel"`
		}
		if json.Unmarshal([]byte(raw), &entry) != nil {
			continue
		}
		info := topicMeetingInfo{RoomID: entry.RoomID}
		label := strings.TrimSpace(entry.DateLabel)
		for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04:05Z07:00", "2006-01-02"} {
			if at, err := time.Parse(layout, label[:min(len(label), len(layout))]); err == nil {
				info.At = at
				break
			}
		}
		out[name] = info
	}
	return out, rows.Err()
}

// proposalCandidate is a passage no topic covers.
type proposalCandidate struct {
	Opus     string
	At       time.Time
	StartMS  int64
	EndMS    int64
	Duration int64
	Vector   []float32
}

func (c proposalCandidate) key() string { return fmt.Sprintf("%s\x00%d", c.Opus, c.StartMS) }

// proposalGroup is one cluster of candidates.
type proposalGroup struct {
	Members  []proposalCandidate
	Centroid []float32
}

func (g proposalGroup) meetings() map[string]bool {
	out := map[string]bool{}
	for _, m := range g.Members {
		out[m.Opus] = true
	}
	return out
}

func (g proposalGroup) days() int {
	days := map[string]bool{}
	for _, m := range g.Members {
		days[m.At.UTC().Format(time.DateOnly)] = true
	}
	return len(days)
}

// edges says whether most of the group's passages sit in the first or last
// 5% of their meeting: greetings and wrap-ups.
func (g proposalGroup) edges() bool {
	n := 0
	for _, m := range g.Members {
		if m.Duration <= 0 {
			continue
		}
		margin := int64(float64(m.Duration) * proposalEdgeShare)
		if m.StartMS < margin || m.EndMS > m.Duration-margin {
			n++
		}
	}
	return n*2 > len(g.Members)
}

func normalizedSum(vectors [][]float32) []float32 {
	if len(vectors) == 0 {
		return nil
	}
	sum := make([]float64, len(vectors[0]))
	for _, v := range vectors {
		for i, x := range v {
			sum[i] += float64(x)
		}
	}
	var norm float64
	for _, x := range sum {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	out := make([]float32, len(sum))
	for i, x := range sum {
		if norm > 0 {
			out[i] = float32(x / norm)
		}
	}
	return out
}

// clusterProposals groups candidates (deterministic for a fixed input): in
// meeting and time order each joins the nearest centroid at cosine 0.90 or
// starts a group; then one reassignment against the final centroids. Groups
// on at least 3 meetings and 3 days are returned, largest first; the cap of
// 12 open proposals is applied after matching, so decided themes do not use
// it up.
func clusterProposals(ctx context.Context, candidates []proposalCandidate) ([]proposalGroup, error) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.At.Equal(b.At) {
			return a.At.Before(b.At)
		}
		if a.Opus != b.Opus {
			return a.Opus < b.Opus
		}
		return a.StartMS < b.StartMS
	})
	// Each group keeps its running sum and its normalised centroid, updated
	// only when a passage joins: comparing a passage with every group costs a
	// dot product per group, not a normalisation.
	type acc struct {
		sum      []float64
		centroid []float32
		members  []int
	}
	normalise := func(sum []float64, into []float32) {
		var norm float64
		for _, x := range sum {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		for i, x := range sum {
			into[i] = 0
			if norm > 0 {
				into[i] = float32(x / norm)
			}
		}
	}
	var groups []*acc
	for i, c := range candidates {
		// A long room is many comparisons: stop as soon as a recording
		// takes precedence.
		if i%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var best *acc
		bestCos := proposalJoinCosine
		for _, g := range groups {
			if cos := topicCosine(c.Vector, g.centroid); cos >= bestCos {
				best, bestCos = g, cos
			}
		}
		if best == nil {
			best = &acc{sum: make([]float64, len(c.Vector)), centroid: make([]float32, len(c.Vector))}
			groups = append(groups, best)
		}
		for d, x := range c.Vector {
			best.sum[d] += float64(x)
		}
		normalise(best.sum, best.centroid)
		best.members = append(best.members, i)
	}
	assigned := make([][]int, len(groups))
	for i, c := range candidates {
		if i%256 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		best, bestCos := -1, proposalJoinCosine
		for g := range groups {
			if cos := topicCosine(c.Vector, groups[g].centroid); cos > bestCos || (cos == bestCos && best < 0) {
				best, bestCos = g, cos
			}
		}
		if best >= 0 {
			assigned[best] = append(assigned[best], i)
		}
	}
	var out []proposalGroup
	for _, members := range assigned {
		if len(members) == 0 {
			continue
		}
		var g proposalGroup
		var vectors [][]float32
		for _, i := range members {
			g.Members = append(g.Members, candidates[i])
			vectors = append(vectors, candidates[i].Vector)
		}
		g.Centroid = normalizedSum(vectors)
		if g.recurs() {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := len(out[i].meetings()), len(out[j].meetings())
		if mi != mj {
			return mi > mj
		}
		if len(out[i].Members) != len(out[j].Members) {
			return len(out[i].Members) > len(out[j].Members)
		}
		return out[i].Members[0].key() < out[j].Members[0].key()
	})
	return out, nil
}

// recurs says whether a group is on enough meetings and days to propose.
func (g proposalGroup) recurs() bool {
	return len(g.meetings()) >= proposalMinMeetings && g.days() >= proposalMinDays
}

// proposalCandidates are the passages of a room's meetings that no active
// topic covers at the suggestion gate.
func (t *topicsService) proposalCandidates(ctx context.Context, meetings map[string]topicMeetingInfo, inputs topicScoringInputs) ([]proposalCandidate, error) {
	var out []proposalCandidate
	names := make([]string, 0, len(meetings))
	for name := range meetings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		set, ok, err := t.vectors.meeting(ctx, name)
		if err != nil {
			return nil, err
		}
		if !ok || set.ModelRevision != inputs.Versions.ModelRevision || set.RecipeVersion != inputs.Versions.RecipeVersion || set.ChunkerVersion != inputs.Versions.ChunkerVersion {
			continue
		}
		var duration int64
		for _, p := range set.Passages {
			duration = max(duration, p.EndMS)
		}
		for _, p := range set.Passages {
			if p.WordCount < proposalMinWords || coveredByATopic(p.Vector, inputs) {
				continue
			}
			out = append(out, proposalCandidate{Opus: name, At: meetings[name].At, StartMS: p.StartMS, EndMS: p.EndMS, Duration: duration, Vector: p.Vector})
		}
	}
	return out, nil
}

// coveredByATopic says whether a passage supports some active topic at the
// same gate suggestions use.
func coveredByATopic(vector []float32, inputs topicScoringInputs) bool {
	if len(inputs.Controls) == 0 {
		return false
	}
	control := topicCosine(vector, inputs.Controls[0])
	for _, c := range inputs.Controls[1:] {
		control = max(control, topicCosine(vector, c))
	}
	for _, topic := range inputs.Topics {
		score := topicCosine(vector, topic.Vector)
		if p := topic.params(); score >= p.Threshold && score-control >= p.Margin {
			return true
		}
	}
	return false
}

// storedProposal is a topic_proposal row with its members.
type storedProposal struct {
	ID, State, Prior                   string
	Centroid                           []float32
	ModelRevision, Recipe, Chunker     string
	Members                            []proposalCandidate
	DecidedBy                          string
	ApprovedTag, Linked, NearestTag    string
	DraftName                          string
	Hints                              []string
	Flags                              map[string]bool
	MeetingCount, DayCount, PassageCnt int
	CreatedAt                          int64
}

func loadProposals(ctx context.Context, q topicQuerier, where string, args ...any) ([]storedProposal, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,state,COALESCE(prior_state,''),centroid,model_revision,recipe_version,chunker_version,
 COALESCE(draft_name,''),COALESCE(hints_json,'[]'),flags_json,COALESCE(nearest_tag_id,''),COALESCE(linked_tag_id,''),meeting_count,day_count,passage_count,created_at
 FROM topic_proposal WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	var out []storedProposal
	for rows.Next() {
		var p storedProposal
		var centroid []byte
		var hints, flags string
		if err := rows.Scan(&p.ID, &p.State, &p.Prior, &centroid, &p.ModelRevision, &p.Recipe, &p.Chunker, &p.DraftName, &hints, &flags, &p.NearestTag, &p.Linked,
			&p.MeetingCount, &p.DayCount, &p.PassageCnt, &p.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if len(centroid) > 0 {
			p.Centroid, _ = decodeTopicVector(centroid)
		}
		_ = json.Unmarshal([]byte(hints), &p.Hints)
		_ = json.Unmarshal([]byte(flags), &p.Flags)
		out = append(out, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		members, err := q.QueryContext(ctx, `SELECT opus_name,start_ms,end_ms FROM topic_proposal_member WHERE proposal_id=? ORDER BY opus_name,start_ms`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for members.Next() {
			var m proposalCandidate
			if err := members.Scan(&m.Opus, &m.StartMS, &m.EndMS); err != nil {
				members.Close()
				return nil, err
			}
			out[i].Members = append(out[i].Members, m)
		}
		if err := members.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// proposalOverlap is the share of the smaller member set the two have in
// common: by passage, or for a stored proposal from another vector space
// (where passage starts moved), by meeting and time overlap.
func proposalOverlap(a, b []proposalCandidate, byTime bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	common := 0
	if !byTime {
		keys := map[string]bool{}
		for _, m := range b {
			keys[m.key()] = true
		}
		for _, m := range a {
			if keys[m.key()] {
				common++
			}
		}
	} else {
		for _, m := range a {
			for _, s := range b {
				if m.Opus != s.Opus {
					continue
				}
				shorter := min(m.EndMS-m.StartMS, s.EndMS-s.StartMS)
				overlap := min(m.EndMS, s.EndMS) - max(m.StartMS, s.StartMS)
				if shorter > 0 && float64(overlap) >= 0.5*float64(shorter) {
					common++
					break
				}
			}
		}
	}
	return float64(common) / float64(min(len(a), len(b)))
}

// matchProposals pairs new groups with stored proposals of the room: by
// member overlap, else (same vector space only) by centroid. It returns, per
// group, the index of its stored match or -1.
func matchProposals(groups []proposalGroup, stored []storedProposal, v topicVersions) []int {
	match := make([]int, len(groups))
	used := map[int]bool{}
	for g := range groups {
		match[g] = -1
		best, bestScore := -1, 0.0
		for s := range stored {
			if used[s] {
				continue
			}
			same := stored[s].ModelRevision == v.ModelRevision && stored[s].Recipe == v.RecipeVersion && stored[s].Chunker == v.ChunkerVersion
			score := proposalOverlap(groups[g].Members, stored[s].Members, !same)
			if score >= proposalMatchOverlap && score > bestScore {
				best, bestScore = s, score
				continue
			}
			if same && best < 0 && len(stored[s].Centroid) == len(groups[g].Centroid) && topicCosine(groups[g].Centroid, stored[s].Centroid) >= proposalMatchCentroid {
				best = s
			}
		}
		if best >= 0 {
			used[best] = true
			match[g] = best
		}
	}
	return match
}

// proposalPass reclusters the rooms that need it: dirty ones at most every
// six hours, every room once a night. Proposals left by a retirement are
// recomputed first. ctx is cancelled when a recording starts; one room's
// failure is logged and the others go on, and the nightly marker is written
// only once the nightly pass completed.
func (t *topicsService) proposalPass(ctx context.Context) error {
	versions, err := t.currentVersions(ctx)
	if err != nil || versions.ModelRevision == "" {
		return err
	}
	inputs, ready, err := t.store.scoringInputs(ctx, versions)
	if err != nil || !ready {
		return err
	}
	if err := t.recomputeProposals(ctx, inputs); err != nil {
		return err
	}
	rooms, nightly, err := t.roomsDue(ctx)
	if err != nil || (len(rooms) == 0 && !nightly) {
		return err
	}
	byRoom, err := t.roomMeetings(ctx, versions)
	if err != nil {
		return err
	}
	if nightly {
		rooms = rooms[:0]
		for room := range byRoom {
			rooms = append(rooms, room)
		}
		sort.Strings(rooms)
	}
	failed := false
	for _, room := range rooms {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := t.clusterRoom(ctx, room, byRoom[room], inputs); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed = true
			t.logf("proposals: room %s: %v", room, err)
		}
	}
	if !nightly || failed {
		return nil
	}
	// Rooms with no meeting left in the window propose nothing any more.
	set, _ := json.Marshal(rooms)
	if _, err := t.store.db.ExecContext(ctx, `UPDATE topic_proposal SET state=?,updated_at=? WHERE state=? AND room_id NOT IN (SELECT value FROM json_each(?))`,
		proposalStale, t.store.unix(), proposalOpen, string(set)); err != nil {
		return err
	}
	return t.store.setMeta(ctx, topicMetaNightly, fmt.Sprint(t.store.unix()))
}

// markRoomDirty records that a room's meetings changed.
func (t *topicsService) markRoomDirty(ctx context.Context, opusName string) {
	if t.rt.meetingMetadata == nil {
		return
	}
	info, err := t.rt.meetingMetadata.meetingInfo(ctx, []string{opusName})
	if err != nil || info[opusName].RoomID == "" {
		return
	}
	_, _ = t.store.db.ExecContext(ctx, `INSERT INTO topic_room_dirty(room_id,dirty_at) VALUES(?,?) ON CONFLICT(room_id) DO UPDATE SET dirty_at=excluded.dirty_at`, info[opusName].RoomID, t.store.unix())
}

// roomsDue lists the dirty rooms to recluster now, and says whether the
// nightly pass over every room is due.
func (t *topicsService) roomsDue(ctx context.Context) ([]string, bool, error) {
	now := t.store.unix()
	nightly, _ := t.store.meta(ctx, topicMetaNightly)
	var last int64
	fmt.Sscan(nightly, &last)
	due := now-last >= int64(proposalNightly/time.Second)
	rows, err := t.store.db.QueryContext(ctx, `SELECT d.room_id FROM topic_room_dirty d LEFT JOIN topic_meta m ON m.key='proposals_room:'||d.room_id
 WHERE COALESCE(CAST(m.value AS INTEGER),0)<=? ORDER BY d.room_id`, now-int64(proposalRoomEvery/time.Second))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var room string
		if err := rows.Scan(&room); err != nil {
			return nil, false, err
		}
		out = append(out, room)
	}
	return out, due, rows.Err()
}

// roomMeetings maps each room to its checked meetings of the last 90 days,
// embedded under the current versions.
func (t *topicsService) roomMeetings(ctx context.Context, v topicVersions) (map[string]map[string]topicMeetingInfo, error) {
	rows, err := t.store.db.QueryContext(ctx, `SELECT opus_name FROM topic_meeting WHERE state=? AND model_revision=? AND recipe_version=? AND chunker_version=?`,
		topicStateDone, v.ModelRevision, v.RecipeVersion, v.ChunkerVersion)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	info, err := t.rt.meetingMetadata.meetingInfo(ctx, names)
	if err != nil {
		return nil, err
	}
	cutoff := t.store.now().Add(-proposalWindow)
	out := map[string]map[string]topicMeetingInfo{}
	for name, m := range info {
		if m.RoomID == "" || m.At.IsZero() || m.At.Before(cutoff) {
			continue
		}
		if out[m.RoomID] == nil {
			out[m.RoomID] = map[string]topicMeetingInfo{}
		}
		out[m.RoomID][name] = m
	}
	return out, nil
}

// clusterRoom reclusters one room and stores the outcome.
func (t *topicsService) clusterRoom(ctx context.Context, room string, meetings map[string]topicMeetingInfo, inputs topicScoringInputs) error {
	candidates, err := t.proposalCandidates(ctx, meetings, inputs)
	if err != nil {
		return err
	}
	groups, err := clusterProposals(ctx, candidates)
	if err != nil {
		return err
	}
	// Every stored proposal of the room takes part in matching, those waiting
	// for a recompute included.
	stored, err := loadProposals(ctx, t.store.db, `room_id=?`, room)
	if err != nil {
		return err
	}
	match := matchProposals(groups, stored, inputs.Versions)
	decided := func(state string) bool {
		switch state {
		case proposalDismissed, proposalApproved, proposalLinked, proposalRecompute:
			return true
		}
		return false
	}
	type plan struct {
		group  int
		stored int // -1: a new proposal
		open   bool
	}
	var plans []plan
	opened := 0
	for g := range groups {
		s := match[g]
		if s >= 0 {
			if decided(stored[s].State) {
				plans = append(plans, plan{g, s, false})
				continue
			}
		} else {
			// Part of a theme someone decided about (a dismissed theme that
			// split in two, say) is not proposed again under a new id.
			suppressed := false
			for _, p := range stored {
				if !decided(p.State) {
					continue
				}
				same := p.ModelRevision == inputs.Versions.ModelRevision && p.Recipe == inputs.Versions.RecipeVersion && p.Chunker == inputs.Versions.ChunkerVersion
				if proposalOverlap(groups[g].Members, p.Members, !same) >= proposalMatchOverlap {
					suppressed = true
					break
				}
			}
			if suppressed {
				continue
			}
		}
		// The cap counts open proposals only, largest themes first.
		if opened >= proposalMaxOpen {
			continue
		}
		opened++
		plans = append(plans, plan{g, s, true})
	}
	named := map[int]namedGroup{}
	for _, p := range plans {
		g := namedGroup{group: groups[p.group], nearest: nearestTopic(groups[p.group].Centroid, inputs)}
		g.name, g.hints, g.ok = t.nameGroup(ctx, groups[p.group].Members)
		named[p.group] = g
	}
	now := t.store.unix()
	return t.store.inTx(ctx, func(tx *sql.Tx) error {
		kept := map[int]bool{}
		for _, p := range plans {
			g := named[p.group]
			id, state, insert := newProposalID(), proposalOpen, true
			if p.stored >= 0 {
				// Re-read inside the transaction: a retirement since the
				// group was built puts the proposal into recompute, which
				// only the recompute may take it out of.
				var current string
				if err := tx.QueryRowContext(ctx, `SELECT state FROM topic_proposal WHERE id=?`, stored[p.stored].ID).Scan(&current); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						continue
					}
					return err
				}
				kept[p.stored] = true
				if current == proposalRecompute {
					continue
				}
				id, state, insert = stored[p.stored].ID, current, false
				if p.open {
					state = proposalOpen
				}
			}
			// A meeting retired since the group was built: its words and
			// vectors are in the name and the centroid, so the group waits
			// for the next pass.
			if live := t.liveMembers(ctx, g.group.Members); len(live) != len(g.group.Members) {
				continue
			}
			if err := writeProposal(ctx, tx, id, room, state, g, g.group.Members, inputs.Versions, now, insert); err != nil {
				return err
			}
		}
		for s, p := range stored {
			if !kept[s] && p.State == proposalOpen {
				if _, err := tx.ExecContext(ctx, `UPDATE topic_proposal SET state=?,updated_at=? WHERE id=? AND state=?`, proposalStale, now, p.ID, proposalOpen); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM topic_room_dirty WHERE room_id=?`, room); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO topic_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "proposals_room:"+room, fmt.Sprint(now))
		return err
	})
}

type namedGroup struct {
	group   proposalGroup
	name    string
	hints   []string
	ok      bool
	nearest string
}

// liveMembers drops members of meetings retired meanwhile.
func (t *topicsService) liveMembers(ctx context.Context, members []proposalCandidate) []proposalCandidate {
	var out []proposalCandidate
	for _, m := range members {
		if errors.Is(t.store.notRetired(ctx, m.Opus), errMeetingRetired) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func newProposalID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "tp_" + hex.EncodeToString(b[:])
}

// writeProposal stores a group as a proposal. A group that could not be
// named yet keeps no name, so it is not served until it has one.
func writeProposal(ctx context.Context, tx *sql.Tx, id, room, state string, g namedGroup, members []proposalCandidate, v topicVersions, now int64, insert bool) error {
	group := proposalGroup{Members: members, Centroid: g.group.Centroid}
	flags, _ := json.Marshal(map[string]bool{"edges": group.edges()})
	hints, _ := json.Marshal(g.hints)
	var name, hintsJSON any
	if g.ok {
		name, hintsJSON = g.name, string(hints)
	}
	var nearest any
	if g.nearest != "" {
		nearest = g.nearest
	}
	if insert {
		if _, err := tx.ExecContext(ctx, `INSERT INTO topic_proposal(id,room_id,state,centroid,model_revision,recipe_version,chunker_version,draft_name,hints_json,flags_json,nearest_tag_id,meeting_count,day_count,passage_count,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, room, state, encodeTopicVector(group.Centroid), v.ModelRevision, v.RecipeVersion, v.ChunkerVersion,
			name, hintsJSON, string(flags), nearest, len(group.meetings()), group.days(), len(members), now, now); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE topic_proposal SET state=?,centroid=?,model_revision=?,recipe_version=?,chunker_version=?,draft_name=?,hints_json=?,flags_json=?,nearest_tag_id=?,
 meeting_count=?,day_count=?,passage_count=?,updated_at=? WHERE id=?`, state, encodeTopicVector(group.Centroid), v.ModelRevision, v.RecipeVersion, v.ChunkerVersion,
			name, hintsJSON, string(flags), nearest, len(group.meetings()), group.days(), len(members), now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM topic_proposal_member WHERE proposal_id=?`, id); err != nil {
			return err
		}
	}
	for _, m := range members {
		sim := topicCosine(m.Vector, group.Centroid)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO topic_proposal_member(proposal_id,opus_name,start_ms,end_ms,sim) VALUES(?,?,?,?,?)`, id, m.Opus, m.StartMS, m.EndMS, sim); err != nil {
			return err
		}
	}
	return nil
}

// nearestTopic is the active topic whose definition is at least 0.90 from a
// centroid, as a hint; "" when none is.
func nearestTopic(centroid []float32, inputs topicScoringInputs) string {
	best, bestCos := "", proposalNearestCosine
	for _, topic := range inputs.Topics {
		if cos := topicCosine(centroid, topic.Vector); cos >= bestCos {
			best, bestCos = topic.TagID, cos
		}
	}
	return best
}

// nameGroup drafts a name from the text of the member meetings only, leaving
// out the words of their speakers' names. A naming document is one stored
// passage of a member meeting (its search text in that time range), so the
// common-word cut works over passages as the design says, not over short
// utterances. ok is false while search or the vectors file does not hold a
// member: naming waits rather than name from part of the evidence.
func (t *topicsService) nameGroup(ctx context.Context, members []proposalCandidate) (string, []string, bool) {
	search := t.rt.searchStore
	if search == nil {
		return "", nil, false
	}
	key, err := t.store.speakerKey(ctx)
	if err != nil {
		return "", nil, false
	}
	byMeeting := map[string]map[int64]bool{}
	for _, m := range members {
		if byMeeting[m.Opus] == nil {
			byMeeting[m.Opus] = map[int64]bool{}
		}
		byMeeting[m.Opus][m.StartMS] = true
	}
	type segment struct {
		start, end int64
		text       string
	}
	var texts []topicNameText
	names := map[string]bool{}
	ids := map[string]bool{}
	meetingsSorted := make([]string, 0, len(byMeeting))
	for opus := range byMeeting {
		meetingsSorted = append(meetingsSorted, opus)
	}
	sort.Strings(meetingsSorted)
	for _, opus := range meetingsSorted {
		set, ok, err := t.vectors.meeting(ctx, opus)
		if err != nil || !ok {
			return "", nil, false
		}
		for _, h := range set.SpeakerTermHashes {
			names[h] = true
		}
		rows, err := search.db.QueryContext(ctx, `SELECT start_ms,end_ms,speaker_id,text FROM segment_ref WHERE opus_name=? ORDER BY start_ms`, opus)
		if err != nil {
			return "", nil, false
		}
		var segments []segment
		for rows.Next() {
			var sg segment
			var speaker string
			if err := rows.Scan(&sg.start, &sg.end, &speaker, &sg.text); err != nil {
				rows.Close()
				return "", nil, false
			}
			for _, term := range topicTerms(speaker) {
				ids[term] = true
			}
			segments = append(segments, sg)
		}
		rows.Close()
		if len(segments) == 0 {
			return "", nil, false
		}
		for _, p := range set.Passages {
			var b strings.Builder
			for _, sg := range segments {
				if sg.end > p.StartMS && sg.start < p.EndMS {
					b.WriteString(sg.text)
					b.WriteString(" ")
				}
			}
			texts = append(texts, topicNameText{Meeting: opus, Text: b.String(), InGroup: byMeeting[opus][p.StartMS]})
		}
	}
	isName := speakerTermExcluder(key, names)
	name, hints := draftTopicName(texts, func(term string) bool { return ids[term] || isName(term) })
	return name, hints, name != ""
}

// recomputeProposals rebuilds proposals a retirement left in recompute, from
// live members only and only from vectors of the current model, recipe and
// chunker. A member whose passages moved (a rerun, a new chunker) is mapped
// to the passage of its meeting it overlaps by half or more, and dropped when
// none does; a member whose meeting has no vectors any more is dropped. While
// a member still waits to be embedded under the current versions the
// proposal stays in recompute, and so stays hidden. A decided proposal
// (approved, linked, dismissed) and a stale one keep their state; an open one
// that no longer recurs turns stale; one with no member left is deleted.
func (t *topicsService) recomputeProposals(ctx context.Context, inputs topicScoringInputs) error {
	stored, err := loadProposals(ctx, t.store.db, `state=?`, proposalRecompute)
	if err != nil {
		return err
	}
	v := inputs.Versions
	for _, p := range stored {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		members := t.liveMembers(ctx, p.Members)
		info, err := t.rt.meetingMetadata.meetingInfo(ctx, memberNames(members))
		if err != nil {
			return err
		}
		var full []proposalCandidate
		var vectors [][]float32
		ready := true
		seen := map[string]bool{}
		for _, m := range members {
			set, ok, err := t.vectors.meeting(ctx, m.Opus)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if set.ModelRevision != v.ModelRevision || set.RecipeVersion != v.RecipeVersion || set.ChunkerVersion != v.ChunkerVersion {
				ready = false
				break
			}
			for _, passage := range set.Passages {
				shorter := min(m.EndMS-m.StartMS, passage.EndMS-passage.StartMS)
				overlap := min(m.EndMS, passage.EndMS) - max(m.StartMS, passage.StartMS)
				if passage.StartMS != m.StartMS && (shorter <= 0 || float64(overlap) < 0.5*float64(shorter)) {
					continue
				}
				key := fmt.Sprintf("%s\x00%d", m.Opus, passage.StartMS)
				if seen[key] {
					break
				}
				seen[key] = true
				var duration int64
				for _, q := range set.Passages {
					duration = max(duration, q.EndMS)
				}
				full = append(full, proposalCandidate{Opus: m.Opus, At: info[m.Opus].At, StartMS: passage.StartMS, EndMS: passage.EndMS, Duration: duration, Vector: passage.Vector})
				vectors = append(vectors, passage.Vector)
				break
			}
		}
		if !ready {
			continue
		}
		now := t.store.unix()
		if len(full) == 0 {
			if _, err := t.store.db.ExecContext(ctx, `DELETE FROM topic_proposal WHERE id=? AND state=?`, p.ID, proposalRecompute); err != nil {
				return err
			}
			continue
		}
		group := proposalGroup{Members: full, Centroid: normalizedSum(vectors)}
		name, hints, ok := t.nameGroup(ctx, full)
		if !ok {
			continue
		}
		state := p.Prior
		switch state {
		case proposalApproved, proposalLinked, proposalDismissed, proposalStale:
		default:
			state = proposalOpen
			if !group.recurs() {
				state = proposalStale
			}
		}
		g := namedGroup{group: group, name: name, hints: hints, ok: true, nearest: nearestTopic(group.Centroid, inputs)}
		err = t.store.inTx(ctx, func(tx *sql.Tx) error {
			var current string
			if err := tx.QueryRowContext(ctx, `SELECT state FROM topic_proposal WHERE id=?`, p.ID).Scan(&current); err != nil || current != proposalRecompute {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			if live := t.liveMembers(ctx, full); len(live) != len(full) {
				// Another retirement meanwhile: the next pass recomputes again.
				return nil
			}
			return writeProposal(ctx, tx, p.ID, "", state, g, full, v, now, false)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func memberNames(members []proposalCandidate) []string {
	var out []string
	for _, m := range members {
		out = append(out, m.Opus)
	}
	return uniqueNames(out)
}
