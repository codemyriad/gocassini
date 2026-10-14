package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ADMIN settings and status for topic suggestions
// (docs/proposals/automatic-tagging, "ADMIN routes"). Status is content-free:
// work counts and reasons, never suggestion or verdict counts.

const maxTopicThreads = 16

// topicsStatus is GET operator/settings/topics.
type topicsStatus struct {
	Enabled bool `json:"enabled"`
	Threads int  `json:"threads"`
	// Available is false where topic suggestions cannot run at all.
	Available bool `json:"available"`
	// Ready: the embedding model is installed and passed its check.
	Ready bool `json:"ready"`
	// Reason is why the worker is not running now, "" when it is.
	Reason        string `json:"reason,omitempty"`
	ModelRevision string `json:"modelRevision,omitempty"`
	ScorerVersion int    `json:"scorerVersion"`
	EnabledAt     string `json:"enabledAt,omitempty"`
	// BackfillSince is "all", a date, or "" when no past meeting was queued.
	BackfillSince string `json:"backfillSince,omitempty"`
	// Counts are topic_meeting rows by state; Unavailable by reason.
	Counts                  map[string]int    `json:"counts"`
	Unavailable             map[string]int    `json:"unavailable"`
	OldestWaitingSeconds    int64             `json:"oldestWaitingSeconds"`
	Blocked                 []topicBlockedRow `json:"blocked"`
	VectorsBytes            int64             `json:"vectorsBytes"`
	Worker                  topicsWorkerStats `json:"worker"`
	// Audit is the library's recent edits with who made them: admins only.
	Audit []topicAuditRow `json:"audit"`
	EstimateSecondsPerCheck int               `json:"estimateSecondsPerMeeting"`
}

type topicAuditRow struct {
	TagID    string `json:"tagId"`
	Revision int    `json:"revision"`
	Kind     string `json:"kind"`
	Actor    string `json:"actor"`
	At       string `json:"at"`
	Redacted bool   `json:"redacted"`
}

type topicBlockedRow struct {
	Meeting string `json:"meeting"`
	Reason  string `json:"reason"`
	Error   string `json:"error,omitempty"`
}

func (rt *Runtime) topicsSettingsHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/settings/topics"), "/")
	switch {
	case path == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, rt.topicsStatus(r.Context()))
	case path == "" && r.Method == http.MethodPut:
		rt.putTopicSettings(w, r)
	case path == "":
		writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
	case path == "backfill" && r.Method == http.MethodPost:
		rt.postTopicBackfill(w, r)
	case path == "retry" && r.Method == http.MethodPost:
		rt.postTopicRetry(w, r)
	case path == "backfill" || path == "retry":
		writeMethodNotAllowed(w, http.MethodPost)
	default:
		http.NotFound(w, r)
	}
}

func (rt *Runtime) topicsStatus(ctx context.Context) topicsStatus {
	settings := rt.currentSettings().TopicSuggestions
	st := topicsStatus{Enabled: settings.Enabled, Threads: settings.Threads, ScorerVersion: topicsScorerVersion,
		Counts: map[string]int{}, Unavailable: map[string]int{}, Blocked: []topicBlockedRow{}, Audit: []topicAuditRow{}, EstimateSecondsPerCheck: topicSecondsPerMeeting}
	s := rt.topics
	if s == nil {
		st.Reason = "unavailable"
		return st
	}
	st.Available = true
	modelReason := s.modelPaused(ctx)
	st.Ready = modelReason == ""
	if settings.Enabled {
		st.Reason = modelReason
	}
	if v, err := s.currentVersions(ctx); err == nil {
		st.ModelRevision = v.ModelRevision
	}
	st.EnabledAt, _ = s.store.meta(ctx, topicMetaEnabledAt)
	st.BackfillSince, _ = s.store.meta(ctx, topicMetaBackfillSince)
	if rows, err := s.store.db.QueryContext(ctx, `SELECT state,reason,COUNT(*) FROM topic_meeting GROUP BY state,reason`); err == nil {
		for rows.Next() {
			var state, why string
			var n int
			if rows.Scan(&state, &why, &n) == nil {
				st.Counts[state] += n
				if state == topicStateUnavailable {
					st.Unavailable[why] += n
				}
			}
		}
		rows.Close()
	}
	var oldest *int64
	if s.store.db.QueryRowContext(ctx, `SELECT MIN(queued_at) FROM topic_meeting WHERE state IN (?,?)`, topicStateQueued, topicStateRunning).Scan(&oldest) == nil && oldest != nil {
		st.OldestWaitingSeconds = max(time.Now().Unix()-*oldest, 0)
	}
	if rows, err := s.store.db.QueryContext(ctx, `SELECT opus_name,reason,last_error FROM topic_meeting WHERE state=? ORDER BY finished_at DESC LIMIT 100`, topicStateBlocked); err == nil {
		for rows.Next() {
			var b topicBlockedRow
			if rows.Scan(&b.Meeting, &b.Reason, &b.Error) == nil {
				st.Blocked = append(st.Blocked, b)
			}
		}
		rows.Close()
	}
	if rows, err := s.store.db.QueryContext(ctx, `SELECT tag_id,revision,COALESCE(json_extract(change_json,'$.kind'),''),actor,at,redacted_at IS NOT NULL FROM topic_library_event ORDER BY id DESC LIMIT 50`); err == nil {
		for rows.Next() {
			var a topicAuditRow
			var at int64
			if rows.Scan(&a.TagID, &a.Revision, &a.Kind, &a.Actor, &at, &a.Redacted) == nil {
				a.At = time.Unix(at, 0).UTC().Format(time.RFC3339)
				st.Audit = append(st.Audit, a)
			}
		}
		rows.Close()
	}
	st.VectorsBytes = s.vectors.size()
	s.statsMu.Lock()
	st.Worker = s.stats
	s.statsMu.Unlock()
	return st
}

func readTopicJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, into); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return false
	}
	return true
}

func (rt *Runtime) putTopicSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
		Threads *int  `json:"threads"`
	}
	if !readTopicJSON(w, r, &in) {
		return
	}
	s := rt.topics
	if s == nil {
		writeJSONError(w, http.StatusConflict, "Topic suggestions need Cassini running as a Nextcloud app with its annotations store.")
		return
	}
	// Serialise with every other settings writer.
	rt.settingsWriteMu.Lock()
	defer rt.settingsWriteMu.Unlock()
	current := rt.currentSettings()
	updated := current
	if in.Threads != nil {
		if *in.Threads < 0 || *in.Threads > maxTopicThreads {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("threads must be 0 (default) to %d", maxTopicThreads))
			return
		}
		updated.TopicSuggestions.Threads = *in.Threads
	}
	if in.Enabled != nil {
		updated.TopicSuggestions.Enabled = *in.Enabled
	}
	turningOn := updated.TopicSuggestions.Enabled && !current.TopicSuggestions.Enabled
	if turningOn {
		rt.invalidateModelInventory()
		if reason := s.pausedWithSettings(r.Context(), updated.TopicSuggestions); reason != "" {
			writeJSONError(w, http.StatusConflict, "Install the topic suggestions model and let its check pass before switching suggestions on.")
			return
		}
	}
	if err := Save(rt.settingsPath, updated); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("save settings: %v", err))
		return
	}
	rt.setSettings(updated)
	if turningOn {
		if at, _ := s.store.meta(r.Context(), topicMetaEnabledAt); at == "" {
			if err := s.store.setMeta(r.Context(), topicMetaEnabledAt, nowUTCString()); err != nil {
				rt.logger.Printf("topics: record enabled_at: %v", err)
			}
		}
		go func() {
			if err := s.reconcile(rt.ctx); err != nil {
				s.logf("reconcile on enable: %v", err)
			}
		}()
	}
	s.wakeUp()
	writeJSON(w, http.StatusOK, rt.topicsStatus(r.Context()))
}

// pausedWithSettings is paused for settings not saved yet.
func (s *topicsService) pausedWithSettings(ctx context.Context, settings topicSettings) string {
	if !settings.Enabled {
		return "disabled"
	}
	return s.modelPaused(ctx)
}

func (rt *Runtime) postTopicBackfill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Since string `json:"since"`
	}
	if !readTopicJSON(w, r, &in) {
		return
	}
	since, err := parseTopicSince(in.Since)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	s := rt.topics
	if s == nil || !rt.currentSettings().TopicSuggestions.Enabled {
		writeJSONError(w, http.StatusConflict, "Switch topic suggestions on first.")
		return
	}
	if s.targets == nil {
		writeJSONError(w, http.StatusConflict, "Past meetings can only be checked when Cassini runs as a Nextcloud app.")
		return
	}
	targets, err := s.targets(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("read the recordings archive: %v", err))
		return
	}
	result, err := queueTopicBackfill(r.Context(), s.store, rt.store, targets, since, 0)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.wakeUp()
	writeJSON(w, http.StatusOK, result)
}

func (rt *Runtime) postTopicRetry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Meetings []string `json:"meetings"`
	}
	if !readTopicJSON(w, r, &in) {
		return
	}
	s := rt.topics
	if s == nil {
		writeJSONError(w, http.StatusConflict, "Topic suggestions are not available here.")
		return
	}
	n, err := s.store.retryBlocked(r.Context(), in.Meetings)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.wakeUp()
	writeJSON(w, http.StatusOK, map[string]int64{"retried": n})
}

// topicsStatusBlock is the short topics block of operator/status: the same
// content-free counts, without asking the CLI anything.
func (rt *Runtime) topicsStatusBlock(ctx context.Context, enabled bool) *statusTopics {
	s := rt.topics
	if s == nil {
		return nil
	}
	block := &statusTopics{Enabled: enabled, Counts: map[string]int{}}
	s.statsMu.Lock()
	block.Paused = s.stats.Paused
	s.statsMu.Unlock()
	if rows, err := s.store.db.QueryContext(ctx, `SELECT state,COUNT(*) FROM topic_meeting GROUP BY state`); err == nil {
		for rows.Next() {
			var state string
			var n int
			if rows.Scan(&state, &n) == nil {
				block.Counts[state] = n
			}
		}
		rows.Close()
	}
	return block
}
