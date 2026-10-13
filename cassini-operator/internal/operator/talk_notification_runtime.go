package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type talkNotification struct {
	Owner     string
	RoomToken string
	Message   string
	MeetingID string
	Link      string
}

// Only the Nextcloud Files sink posts recording links, using its existing
// AppAPI configuration and the moderator saved with the recording.
func (s *directSharesPublishSink) talkNotificationClient() *talkNotificationClient {
	if !s.cfg.appAPIActive() {
		return nil
	}
	return newTalkNotificationClient(s.cfg)
}

// Read before marking publication successful. Existing successful attempts
// suppress announcements on republish; no notification state is persisted.
func (s *Store) prepareTalkNotification(ctx context.Context, jobID, appID string) (*talkNotification, error) {
	var request string
	var binding sql.NullString
	var published bool
	err := s.db.QueryRowContext(ctx, `SELECT request_json, talk_binding,
 EXISTS(SELECT 1 FROM job_attempts WHERE job_id = jobs.id AND state = 'succeeded' AND stage = 'done')
 FROM jobs WHERE id = ?`, jobID).Scan(&request, &binding, &published)
	if err != nil {
		return nil, err
	}
	if published || !binding.Valid || strings.TrimSpace(binding.String) == "" {
		return nil, nil
	}
	var b talkJobBinding
	var req TriggerRequest
	if json.Unmarshal([]byte(binding.String), &b) != nil || json.Unmarshal([]byte(request), &req) != nil || strings.TrimSpace(b.Owner) == "" || strings.TrimSpace(b.RoomToken) == "" {
		return nil, fmt.Errorf("the saved recording moderator or room is unavailable")
	}
	// Use the public URL from the authenticated recording request, not the
	// potentially internal Nextcloud API/backend address.
	link, err := talkMeetingURL(req.BaseURL, appID, jobID)
	if err != nil {
		return nil, err
	}
	return &talkNotification{Owner: strings.TrimSpace(b.Owner), RoomToken: strings.TrimSpace(b.RoomToken), MeetingID: jobID, Link: link, Message: "Cassini · Meeting available"}, nil
}

// Metadata is best effort and comes only from this published attempt, never
// from the room's invited audience. The label is visible to everyone in chat.
func (n *talkNotification) enrich(sitePath, bundlePath string) {
	parts := []string{"Cassini"}
	type notificationCatalogEntry struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Date       string `json:"dateLabel"`
		DurationMS int64  `json:"digestDurationMs"`
	}
	var entry notificationCatalogEntry
	catalog, ok, err := loadSiteCatalog(sitePath)
	if err == nil && ok {
		for _, raw := range catalog.Meetings {
			if json.Unmarshal(raw, &entry) == nil && entry.ID == n.MeetingID {
				break
			}
			entry = notificationCatalogEntry{}
		}
	}
	if title := notificationText(entry.Title, 100); title != "" {
		parts = append(parts, title)
	} else {
		parts = append(parts, "Meeting available")
	}
	if date := notificationText(entry.Date, 40); date != "" {
		parts = append(parts, date)
	}
	if entry.DurationMS > 0 {
		minutes := entry.DurationMS / 60000
		if minutes == 0 {
			parts = append(parts, "<1 min")
		} else {
			parts = append(parts, fmt.Sprintf("%d min", minutes))
		}
	}
	var transcript struct {
		Speakers []struct {
			Label string `json:"label"`
		} `json:"speakers"`
	}
	if raw, err := os.ReadFile(filepath.Join(bundlePath, "transcript.words.v1.json")); err == nil && json.Unmarshal(raw, &transcript) == nil {
		names := []string{}
		seen := map[string]bool{}
		for _, speaker := range transcript.Speakers {
			name := notificationText(speaker.Label, 40)
			if name != "" && !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
		sort.Strings(names)
		if len(names) > 0 {
			label := strings.Join(names[:min(3, len(names))], ", ")
			if len(names) > 3 {
				label += fmt.Sprintf(" +%d", len(names)-3)
			}
			parts = append(parts, "Speakers: "+label)
		}
	}
	n.Message = strings.Join(parts, " · ")
}

func notificationText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

func talkMeetingURL(base, appID, jobID string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || appID == "" || strings.ContainsAny(appID, "/?#") {
		return "", fmt.Errorf("The recording's public Nextcloud URL is unavailable; no link was posted.")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/index.php/apps/app_api/embedded/" + appID + "/viewer"
	u.RawPath = ""
	u.Fragment = "meeting=" + url.QueryEscape(jobID)
	return u.String(), nil
}
