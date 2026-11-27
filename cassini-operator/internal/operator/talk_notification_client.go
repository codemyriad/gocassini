package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type talkNotificationClient struct {
	cfg    ExAppConfig
	client *http.Client
}

func newTalkNotificationClient(cfg ExAppConfig) *talkNotificationClient {
	return &talkNotificationClient{cfg: cfg, client: &http.Client{
		Timeout: 15 * time.Second,
		// Never forward AppAPI credentials through a redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type talkChatMessage struct {
	ID        int64  `json:"id"`
	ActorType string `json:"actorType"`
	ActorID   string `json:"actorId"`
}

func (c *talkNotificationClient) send(ctx context.Context, n talkNotification) (int64, error) {
	if n.Owner == "" || n.RoomToken == "" {
		return 0, fmt.Errorf("the recording moderator or room is missing")
	}
	endpoint := withFormatJSON(c.cfg.ocsURL("/apps/spreed/api/v1/chat/" + url.PathEscape(n.RoomToken) + "/share"))
	metadata, err := json.Marshal(map[string]string{"name": n.Message, "link": n.Link})
	if err != nil {
		return 0, fmt.Errorf("could not encode meeting reference")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"objectType": {"highlight"}, "objectId": {n.MeetingID}, "metaData": {string(metadata)}}.Encode()))
	if err != nil {
		return 0, fmt.Errorf("invalid Talk API URL")
	}
	return c.post(req, n.Owner)
}

// sendSummaryForward posts, silently, the plain message that follows the
// recording post when the meeting's summary is forwarded (#532): Talk shows a
// shared highlight as one link and nothing else.
func (c *talkNotificationClient) sendSummaryForward(ctx context.Context, n talkNotification, notice summaryNotice) (int64, error) {
	if n.Owner == "" || n.RoomToken == "" {
		return 0, fmt.Errorf("the recording moderator or room is missing")
	}
	endpoint := withFormatJSON(c.cfg.ocsURL("/apps/spreed/api/v1/chat/" + url.PathEscape(n.RoomToken)))
	reference := sha256.Sum256([]byte("summary-forward:" + n.MeetingID))
	form := url.Values{"message": {summaryForwardMessage(notice)}, "referenceId": {hex.EncodeToString(reference[:])}, "silent": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, fmt.Errorf("invalid Talk API URL")
	}
	return c.post(req, n.Owner)
}

// summaryForwardMessage names the meeting, not "its": the room list shows it
// alone. English: the operator has no locale.
func summaryForwardMessage(notice summaryNotice) string {
	message := "This meeting's summary will also be sent to " + notice.Host + "."
	if notice.Reason != "" {
		message += " " + notice.Reason
	}
	return message
}

func (c *talkNotificationClient) post(req *http.Request, owner string) (int64, error) {
	c.cfg.setAppAPIProvisionHeadersAs(req, owner)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		// Do not expose URLs, credentials, or upstream bodies in logs.
		return 0, fmt.Errorf("could not confirm the Talk request completed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("Talk returned HTTP %d for the recording moderator", resp.StatusCode)
	}
	var envelope struct {
		OCS struct {
			Meta struct {
				Status     string `json:"status"`
				StatusCode int    `json:"statuscode"`
			} `json:"meta"`
			Data talkChatMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return 0, fmt.Errorf("Talk returned an unreadable response")
	}
	code := envelope.OCS.Meta.StatusCode
	if envelope.OCS.Meta.Status == "failure" || (code != 100 && code != 200 && code != 201) {
		return 0, fmt.Errorf("Talk returned OCS %d for the recording moderator", code)
	}
	if envelope.OCS.Data.ID <= 0 {
		return 0, fmt.Errorf("Talk did not return a message ID")
	}
	return envelope.OCS.Data.ID, nil
}
