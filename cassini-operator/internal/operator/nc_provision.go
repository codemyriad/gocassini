package operator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultNextcloudAdminUser = "admin"
	envNCAdminUser            = "CASSINI_NC_ADMIN_USER"
	ncProvisionTimeout        = 90 * time.Second
)

var provisionMu sync.RWMutex
var resolvedProvisioningUser atomic.Pointer[any]

func (c ExAppConfig) provisioningUser() string {
	if cached := resolvedProvisioningUser.Load(); cached != nil {
		if name, _ := (*cached).(string); name != "" {
			return name
		}
	}
	return c.provisioningUserFallback()
}

func (c ExAppConfig) provisioningUserFallback() string {
	if configured := strings.TrimSpace(os.Getenv(envNCAdminUser)); configured != "" {
		return configured
	}
	return defaultNextcloudAdminUser
}

func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "Cw1!" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func (c ExAppConfig) ocsURL(suffix string) string {
	return strings.TrimRight(c.NextcloudURL, "/") + "/ocs/v2.php" + suffix
}

func ocsRefusal(status int, body []byte) string {
	if status/100 != 2 {
		return fmt.Sprintf("HTTP %d: %s", status, snippet(body))
	}
	var env struct {
		OCS struct {
			Meta struct {
				Status     string `json:"status"`
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	meta := env.OCS.Meta
	// 100 (v1) and 200 (v2) are success; anything else with status "failure" is
	// a refusal wearing an HTTP 200.
	if meta.Status == "failure" || (meta.StatusCode != 0 && meta.StatusCode != 100 && meta.StatusCode != 200) {
		if meta.Message != "" {
			return fmt.Sprintf("OCS %d: %s", meta.StatusCode, meta.Message)
		}
		return fmt.Sprintf("OCS %d", meta.StatusCode)
	}
	return ""
}

func (c ExAppConfig) userExists(ctx context.Context, client *http.Client, userID string) (bool, error) {
	status, body, err := c.apiGet(ctx, client, c.ocsURL("/cloud/users/"+url.PathEscape(userID)))
	if err != nil {
		return false, err
	}
	// HTTP 2xx plus the id below is the evidence; the OCS meta code is NOT part
	// of the test. /ocs/v1.php answers 100 for OK and /ocs/v2.php answers 200 —
	// requiring 100 made this always-false against a real v2 endpoint, which is
	// how a service account that plainly existed was reported missing on the
	// sandbox. An explicit not-found still short-circuits.
	if status/100 != 2 {
		return false, nil
	}
	if code := ocsStatusCode(body); code == 998 || code == 404 {
		return false, nil
	}
	var env struct {
		OCS struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return false, nil
	}
	return env.OCS.Data.ID == userID, nil
}

func parseOCSUserList(body []byte) ([]string, error) {
	var env struct {
		OCS struct {
			Data struct {
				Users []string `json:"users"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode user list: %w", err)
	}
	return env.OCS.Data.Users, nil
}

func ocsStatusCode(body []byte) int {
	var env struct {
		OCS struct {
			Meta struct {
				StatusCode int `json:"statuscode"`
			} `json:"meta"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return -1
	}
	return env.OCS.Meta.StatusCode
}

func (c ExAppConfig) apiGet(ctx context.Context, client *http.Client, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, withFormatJSON(rawURL), nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeaders(req)
	return doReadBody(client, req)
}

func (c ExAppConfig) apiGetAs(ctx context.Context, client *http.Client, actAs, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, withFormatJSON(rawURL), nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeadersAs(req, actAs)
	return doReadBody(client, req)
}

func (c ExAppConfig) apiPostForm(ctx context.Context, client *http.Client, rawURL string, form url.Values) (int, []byte, error) {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, withFormatJSON(rawURL), strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.ContentLength = int64(len(body))
	return doReadBody(client, req)
}

func (c ExAppConfig) setAppAPIProvisionHeaders(req *http.Request) {
	c.setAppAPIProvisionHeadersAs(req, c.provisioningUser())
}

func (c ExAppConfig) setAppAPIProvisionHeadersAs(req *http.Request, userID string) {
	auth := base64.StdEncoding.EncodeToString([]byte(userID + ":" + c.AppSecret))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("AUTHORIZATION-APP-API", auth)
	req.Header.Set("EX-APP-ID", c.AppID)
	req.Header.Set("EX-APP-VERSION", c.AppVersion)
	if c.AAVersion != "" {
		req.Header.Set("AA-VERSION", c.AAVersion)
	}
}

func doReadBody(client *http.Client, req *http.Request) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func withFormatJSON(rawURL string) string {
	if strings.Contains(rawURL, "?") {
		return rawURL + "&format=json"
	}
	return rawURL + "?format=json"
}

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
