package nextcloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
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

// OCSError describes an OCS request the Talk server answered with a failure.
// It keeps the HTTP status so callers can distinguish definitive client-side
// rejections (4xx) from transient server or transport problems.
type OCSError struct {
	Method     string
	Path       string
	HTTPStatus int
	OCSStatus  string
	OCSCode    int
	Message    string
}

func (e *OCSError) Error() string {
	if e.HTTPStatus >= 400 {
		return fmt.Sprintf("%s %s failed: HTTP %d, status=%s code=%d message=%q", e.Method, e.Path, e.HTTPStatus, e.OCSStatus, e.OCSCode, e.Message)
	}
	return fmt.Sprintf("%s %s failed: status=%s code=%d message=%q", e.Method, e.Path, e.OCSStatus, e.OCSCode, e.Message)
}

// IsClientError reports whether err wraps an OCS request the server rejected
// with an HTTP 4xx status. Such rejections are definitive — for example a
// room the guest recorder cannot see (404) or a call join refused because
// recording consent is required (400) — so retrying cannot succeed. HTTP 429
// is excluded: it is Nextcloud's bruteforce/rate-limit throttling, the one
// 4xx a retry can outwait. 409 on participants/active signals an existing
// session and would support force-join, but the recorder uses a fresh guest
// actor per process, so it cannot collide with itself and 409 stays
// definitive here.
func IsClientError(err error) bool {
	var ocsErr *OCSError
	return errors.As(err, &ocsErr) &&
		ocsErr.HTTPStatus >= 400 && ocsErr.HTTPStatus < 500 &&
		ocsErr.HTTPStatus != http.StatusTooManyRequests
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
		"flags":  joinFlags,
		"silent": false,
		// The recorder is the recording, so always affirm recording consent.
		// Instances enforcing config => call => recording-consent reject a
		// join without it (HTTP 400); Talk accepts the flag either way.
		"recordingConsent": true,
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

	if resp.StatusCode >= 400 || env.OCS.Meta.Status != "ok" {
		return nil, &OCSError{
			Method:     method,
			Path:       path,
			HTTPStatus: resp.StatusCode,
			OCSStatus:  env.OCS.Meta.Status,
			OCSCode:    env.OCS.Meta.StatusCode,
			Message:    env.OCS.Meta.Message,
		}
	}

	return env.OCS.Data, nil
}
