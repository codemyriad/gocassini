package nextcloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type OCSClient struct {
	baseURL string
	http    *http.Client
}

type ocsEnvelope struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

type SignalingSettings struct {
	Server    string `json:"server"`
	Signaling []struct {
		Server string `json:"server"`
	} `json:"signaling"`
	HelloAuthParams map[string]json.RawMessage `json:"helloAuthParams"`
	StunServers     []SettingICEServer         `json:"stunservers"`
	TurnServers     []SettingICEServer         `json:"turnservers"`
}

type SettingICEServer struct {
	URLs       any    `json:"urls"`
	Username   string `json:"username"`
	Credential string `json:"credential"`
}

func NewOCSClient(baseURL string, insecure bool) *OCSClient {
	jar, _ := cookiejar.New(nil)

	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		baseTransport = &http.Transport{}
	}
	transport := baseTransport.Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &OCSClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout:   20 * time.Second,
			Transport: transport,
			Jar:       jar,
		},
	}
}

func (c *OCSClient) GetRoom(ctx context.Context, roomToken string) error {
	_, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s", roomToken), nil, nil)
	return err
}

func (c *OCSClient) MarkParticipantActive(ctx context.Context, roomToken, displayName string) (string, error) {
	payload := url.Values{}
	payload.Set("force", "false")
	if displayName != "" {
		payload.Set("displayName", displayName)
	}

	raw, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", roomToken), nil, payload)
	if err != nil {
		return "", err
	}

	var data struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("decode participants/active response: %w", err)
	}
	if data.SessionID == "" {
		return "", fmt.Errorf("participants/active response missing sessionId")
	}
	return data.SessionID, nil
}

func (c *OCSClient) SetGuestName(ctx context.Context, roomToken, guestName string) error {
	payload := url.Values{}
	payload.Set("displayName", guestName)
	_, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/guest/%s/name", roomToken), nil, payload)
	return err
}

func (c *OCSClient) FetchSignalingSettings(ctx context.Context, roomToken string) (*SignalingSettings, error) {
	query := url.Values{}
	query.Set("token", roomToken)

	raw, err := c.request(ctx, http.MethodGet, "/ocs/v2.php/apps/spreed/api/v3/signaling/settings", query, nil)
	if err != nil {
		return nil, err
	}

	var out SignalingSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode signaling settings: %w", err)
	}
	return &out, nil
}

func (c *OCSClient) FetchRecordingSignalingSettings(ctx context.Context, roomToken, recordingSecret string) (*SignalingSettings, error) {
	query := url.Values{}
	query.Set("token", roomToken)

	headers, err := talkRecordingAuthHeaders(recordingSecret, nil)
	if err != nil {
		return nil, err
	}
	raw, err := c.requestWithHeaders(ctx, http.MethodGet, "/ocs/v2.php/apps/spreed/api/v3/signaling/settings", query, nil, headers)
	if err != nil {
		return nil, err
	}

	var out SignalingSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode signaling settings: %w", err)
	}
	return &out, nil
}

func (c *OCSClient) JoinCall(ctx context.Context, roomToken string, joinFlags int) error {
	payload := map[string]any{
		"flags":            joinFlags,
		"silent":           false,
		"recordingConsent": false,
		"silentFor":        []any{},
	}
	_, err := c.requestJSON(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), nil, payload)
	return err
}

func (c *OCSClient) LeaveCall(ctx context.Context, roomToken string) error {
	_, err := c.requestJSON(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), nil, map[string]any{"all": false})
	return err
}

func (c *OCSClient) LeaveParticipantActive(ctx context.Context, roomToken string) error {
	_, err := c.request(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", roomToken), nil, nil)
	return err
}

func (s *SignalingSettings) PrimarySignalingServer() string {
	if s.Server != "" {
		return s.Server
	}
	if len(s.Signaling) > 0 {
		return s.Signaling[0].Server
	}
	return ""
}

func (c *OCSClient) request(ctx context.Context, method, path string, query url.Values, form url.Values) (json.RawMessage, error) {
	return c.requestWithHeaders(ctx, method, path, query, form, nil)
}

func (c *OCSClient) requestWithHeaders(ctx context.Context, method, path string, query url.Values, form url.Values, headers http.Header) (json.RawMessage, error) {
	var body io.Reader
	var contentType string
	if form != nil {
		body = bytes.NewBufferString(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return c.doRequest(ctx, method, path, query, body, contentType, headers)
}

func (c *OCSClient) requestJSON(ctx context.Context, method, path string, query url.Values, payload any) (json.RawMessage, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON payload for %s %s: %w", method, path, err)
	}
	return c.doRequest(ctx, method, path, query, bytes.NewReader(bodyBytes), "application/json", nil)
}

func (c *OCSClient) doRequest(
	ctx context.Context,
	method, path string,
	query url.Values,
	body io.Reader,
	contentType string,
	headers http.Header,
) (json.RawMessage, error) {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, path, err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response %s %s: %w", method, path, err)
	}

	var env ocsEnvelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		return nil, fmt.Errorf("decode OCS envelope %s %s: %w", method, path, err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s failed: HTTP %d, status=%s code=%d message=%q", method, path, resp.StatusCode, env.OCS.Meta.Status, env.OCS.Meta.StatusCode, env.OCS.Meta.Message)
	}
	if env.OCS.Meta.Status != "ok" {
		return nil, fmt.Errorf("%s %s failed: status=%s code=%d message=%q", method, path, env.OCS.Meta.Status, env.OCS.Meta.StatusCode, env.OCS.Meta.Message)
	}

	return env.OCS.Data, nil
}
