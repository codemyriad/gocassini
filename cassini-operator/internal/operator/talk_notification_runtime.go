package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type talkNotification struct {
	Owner     string
	RoomToken string
	Message   string
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
	return &talkNotification{Owner: strings.TrimSpace(b.Owner), RoomToken: strings.TrimSpace(b.RoomToken), Message: "Recording available: " + link}, nil
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
