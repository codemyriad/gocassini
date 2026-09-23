package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Nextcloud's OCS Share API is the permission boundary for recordings. The
// archive index may describe a file, but only a share returned to the current
// caller can make it visible in Cassini. DAV still checks every actual read.
const (
	ncShareTypeUser  = 0
	ncShareTypeGroup = 1
	ncShareTypeTeam  = 7
	ncShareRead      = 1
	ncShareReshare   = 16
	ncShareReplyMax  = 64 << 20
)

type ncShare struct {
	ID           ncShareID `json:"id"`
	ShareType    int       `json:"share_type"`
	ShareWith    string    `json:"share_with"`
	Permissions  int       `json:"permissions"`
	FileSource   int64     `json:"file_source"`
	FileTarget   string    `json:"file_target"`
	Path         string    `json:"path"`
	UIDFileOwner string    `json:"uid_file_owner"`
	ItemType     string    `json:"item_type"`
}

// Nextcloud serializes share IDs as strings on some server versions and as
// numbers on others. Both forms identify the same OCS share.
type ncShareID int64

func (id *ncShareID) UnmarshalJSON(raw []byte) error {
	value := strings.Trim(string(raw), `"`)
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return err
	}
	*id = ncShareID(n)
	return nil
}

func (s ncShare) recipientPath() (string, error) {
	value := strings.TrimSpace(s.Path)
	if value == "" {
		value = strings.TrimSpace(s.FileTarget)
	}
	if value == "" || strings.ContainsAny(value, "\\\x00") {
		return "", fmt.Errorf("share %d has no valid recipient path", s.ID)
	}
	// Never let an OCS path escape the caller's DAV home. PathEscape in
	// davFileURL protects URL syntax, while this rejects traversal semantics.
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("share %d has an invalid recipient path", s.ID)
		}
	}
	return strings.TrimPrefix(path.Clean(value), "/"), nil
}

func (c ExAppConfig) shareAPIURL() string {
	return strings.TrimRight(c.NextcloudURL, "/") + "/ocs/v2.php/apps/files_sharing/api/v1/shares"
}

// shareRequest validates both HTTP and OCS status. Nextcloud can put a refusal
// in an HTTP 200 response, so neither status alone is sufficient.
func (c ExAppConfig) shareRequest(ctx context.Context, client *http.Client, actor, method, rawURL string, form url.Values) (json.RawMessage, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	c.setAppAPIOCSHeadersForUser(req, actor)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ncShareReplyMax+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > ncShareReplyMax {
		return nil, fmt.Errorf("Nextcloud share response exceeds %d bytes", ncShareReplyMax)
	}
	var envelope struct {
		OCS struct {
			Meta struct {
				Status     string `json:"status"`
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode Nextcloud share response: %w", err)
	}
	meta := envelope.OCS.Meta
	if resp.StatusCode < 200 || resp.StatusCode >= 300 ||
		(meta.StatusCode != 100 && meta.StatusCode != 200) ||
		strings.EqualFold(meta.Status, "failure") {
		return nil, fmt.Errorf("Nextcloud share API %s: HTTP %d, OCS %d: %s", method, resp.StatusCode, meta.StatusCode, oneLineListField(meta.Message))
	}
	if len(envelope.OCS.Data) == 0 || string(envelope.OCS.Data) == "null" {
		return nil, fmt.Errorf("Nextcloud share API %s returned no data", method)
	}
	return envelope.OCS.Data, nil
}

// receivedShares is one fresh, unpaginated Nextcloud visibility snapshot.
// Never replace it with locally remembered recipients for authorization.
func (c ExAppConfig) receivedShares(ctx context.Context, client *http.Client, caller string) ([]ncShare, error) {
	if strings.TrimSpace(caller) == "" {
		return nil, fmt.Errorf("Nextcloud caller is required")
	}
	u := c.shareAPIURL() + "?shared_with_me=true"
	data, err := c.shareRequest(ctx, client, caller, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var shares []ncShare
	if err := json.Unmarshal(data, &shares); err != nil {
		return nil, fmt.Errorf("decode received shares: %w", err)
	}
	if shares == nil {
		shares = []ncShare{}
	}
	return shares, nil
}

func (c ExAppConfig) ownerSharesForPath(ctx context.Context, client *http.Client, relPath string) ([]ncShare, error) {
	u := c.shareAPIURL() + "?path=" + url.QueryEscape("/"+strings.TrimPrefix(relPath, "/"))
	data, err := c.shareRequest(ctx, client, ncRecordingsOwner, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var shares []ncShare
	if err := json.Unmarshal(data, &shares); err != nil {
		return nil, fmt.Errorf("decode owner shares: %w", err)
	}
	return shares, nil
}

func (c ExAppConfig) createRecordingShare(ctx context.Context, client *http.Client, relPath string, principal aclMapping, permissions int) (ncShare, error) {
	shareType := -1
	switch principal.Type {
	case "user":
		shareType = ncShareTypeUser
	case "group":
		shareType = ncShareTypeGroup
	case "circle":
		shareType = ncShareTypeTeam
	default:
		return ncShare{}, fmt.Errorf("unsupported share principal type %q", principal.Type)
	}
	if principal.ID == "" || (permissions != ncShareRead && permissions != ncShareRead|ncShareReshare) {
		return ncShare{}, fmt.Errorf("invalid recording share")
	}
	data, err := c.shareRequest(ctx, client, ncRecordingsOwner, http.MethodPost, c.shareAPIURL(), url.Values{
		"path":        {"/" + strings.TrimPrefix(relPath, "/")},
		"shareType":   {strconv.Itoa(shareType)},
		"shareWith":   {principal.ID},
		"permissions": {strconv.Itoa(permissions)},
	})
	if err != nil {
		return ncShare{}, err
	}
	var share ncShare
	if err := json.Unmarshal(data, &share); err != nil {
		return ncShare{}, fmt.Errorf("decode created share: %w", err)
	}
	if share.ID <= 0 {
		return ncShare{}, fmt.Errorf("Nextcloud returned a share without an ID")
	}
	return share, nil
}

func shareTypeForPrincipal(kind string) (int, bool) {
	switch kind {
	case "user":
		return ncShareTypeUser, true
	case "group":
		return ncShareTypeGroup, true
	case "circle":
		return ncShareTypeTeam, true
	default:
		return 0, false
	}
}

func shareCoversPrincipal(shares []ncShare, principal aclMapping) bool {
	kind, ok := shareTypeForPrincipal(principal.Type)
	if !ok {
		return false
	}
	for _, share := range shares {
		if share.ShareType == kind && share.ShareWith == principal.ID && share.Permissions&ncShareRead != 0 {
			return true
		}
	}
	return false
}

// reconcileRecordingShares adds only missing audience members. Existing shares
// are left exactly as an administrator or recipient changed them. This is used
// for a first publish or an interrupted first publish; completed republishes
// must skip it, since removing a recipient is an intentional audience edit.
func (c ExAppConfig) reconcileRecordingShares(ctx context.Context, client *http.Client, relPath string, audience []aclMapping, public bool) error {
	existing, err := c.ownerSharesForPath(ctx, client, relPath)
	if err != nil {
		return fmt.Errorf("list recording shares: %w", err)
	}
	seen := map[string]bool{}
	permissions := ncShareRead
	if public {
		permissions |= ncShareReshare
	}
	for _, principal := range audience {
		principal.Type = strings.TrimSpace(principal.Type)
		principal.ID = strings.TrimSpace(principal.ID)
		if principal.ID == "" || (principal.Type == "user" && principal.ID == ncRecordingsOwner) {
			continue
		}
		if _, ok := shareTypeForPrincipal(principal.Type); !ok {
			return fmt.Errorf("unsupported recording recipient type %q", principal.Type)
		}
		key := principal.Type + "\x00" + principal.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if shareCoversPrincipal(existing, principal) {
			continue
		}
		share, createErr := c.createRecordingShare(ctx, client, relPath, principal, permissions)
		if createErr != nil {
			// A network failure may arrive after the server committed the
			// share. Check that before any retry that could create another.
			if refreshed, listErr := c.ownerSharesForPath(ctx, client, relPath); listErr == nil {
				existing = refreshed
				if shareCoversPrincipal(existing, principal) {
					continue
				}
			} else {
				return fmt.Errorf("share %s %s: %w (and re-list failed: %v)", principal.Type, principal.ID, createErr, listErr)
			}
		}
		if createErr != nil && public {
			// Instance policy may refuse SHARE while allowing READ. The read
			// grant still matters; the UI can report that resharing is limited.
			share, createErr = c.createRecordingShare(ctx, client, relPath, principal, ncShareRead)
		}
		if createErr != nil {
			// A timeout can happen after Nextcloud committed a share. A retry
			// re-lists first so it will not create a duplicate.
			return fmt.Errorf("share %s %s: %w", principal.Type, principal.ID, createErr)
		}
		existing = append(existing, share)
	}
	// Check Nextcloud's state, rather than treating a successful POST as proof
	// that every expected share actually landed.
	verified, err := c.ownerSharesForPath(ctx, client, relPath)
	if err != nil {
		return fmt.Errorf("verify recording shares: %w", err)
	}
	for _, principal := range audience {
		if principal.Type == "user" && principal.ID == ncRecordingsOwner {
			continue
		}
		if !shareCoversPrincipal(verified, principal) {
			return fmt.Errorf("recording share for %s %s is missing after publication", principal.Type, principal.ID)
		}
	}
	return nil
}
