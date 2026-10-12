package operator

import (
	"context"
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
	endpoint := withFormatJSON(c.cfg.ocsURL("/apps/spreed/api/v1/chat/" + url.PathEscape(n.RoomToken)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"message": {n.Message}}.Encode()))
	if err != nil {
		return 0, fmt.Errorf("invalid Talk API URL")
	}
	c.cfg.setAppAPIProvisionHeadersAs(req, n.Owner)
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
