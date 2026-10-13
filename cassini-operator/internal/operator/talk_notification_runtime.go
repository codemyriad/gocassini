package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
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
	return &talkNotification{Owner: strings.TrimSpace(b.Owner), RoomToken: strings.TrimSpace(b.RoomToken), MeetingID: jobID, Link: link, Message: "Meeting available"}, nil
}

// Metadata is best effort and comes only from this published attempt.
// Keep the shared-items label compact; Talk renders it as a single link.
func (n *talkNotification) enrich(sitePath string) {
	parts := []string{}
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
	if title := notificationText(entry.Title, 80); title != "" {
		parts = append(parts, title)
	} else {
		parts = append(parts, "Meeting available")
	}
	if date := notificationDate(entry.Date); date != "" {
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
	n.Message = strings.Join(parts, " · ")
}

// Preserve the recorded calendar date rather than converting it to server time.
func notificationDate(value string) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", time.RFC3339} {
		if date, err := time.Parse(layout, value); err == nil {
			return date.Format("2 Jan")
		}
	}
	return notificationText(value, 40)
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
