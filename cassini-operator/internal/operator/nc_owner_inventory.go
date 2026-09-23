package operator

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// ownerRecordingNames is a recovery and archive-membership lookup. It is used
// only for file IDs missing from the disposable metadata index; normal lists
// need one caller OCS request and local SQLite reads, with no owner DAV call.
func (c ExAppConfig) ownerRecordingNames(ctx context.Context, client *http.Client) (map[int64]string, error) {
	if c.sharePaths != nil {
		c.sharePaths.mu.Lock()
		if time.Now().Before(c.sharePaths.ownerExpires) {
			cached := c.sharePaths.ownerNames
			c.sharePaths.mu.Unlock()
			return cached, nil
		}
		c.sharePaths.mu.Unlock()
	}
	relDir := ncRecordingsRoot + "/meetings"
	endpoint := c.davFileURL(ncRecordingsOwner, relDir)
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	requestBody := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/><d:resourcetype/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	c.setAppAPIDAVHeadersForUser(req, ncRecordingsOwner)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return map[int64]string{}, nil
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("owner recording inventory returned %d", resp.StatusCode)
	}
	const maxInventoryBytes = 64 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxInventoryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxInventoryBytes {
		return nil, fmt.Errorf("owner recording inventory exceeds %d bytes", maxInventoryBytes)
	}
	var multi struct {
		Responses []struct {
			Href     string `xml:"href"`
			Propstat []struct {
				Status     string    `xml:"status"`
				FileID     string    `xml:"prop>fileid"`
				Collection *struct{} `xml:"prop>resourcetype>collection"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(raw, &multi); err != nil {
		return nil, fmt.Errorf("decode owner recording inventory: %w", err)
	}
	if len(multi.Responses) == 0 {
		return nil, fmt.Errorf("owner recording inventory was empty")
	}
	names := make(map[int64]string, len(multi.Responses))
	parent := strings.TrimSuffix(u.Path, "/")
	for _, item := range multi.Responses {
		href, parseErr := url.Parse(item.Href)
		if parseErr != nil || path.Dir(strings.TrimSuffix(href.Path, "/")) != parent {
			continue
		}
		name := path.Base(href.Path)
		if !strings.HasSuffix(name, ".opus") || path.Base(name) != name {
			continue
		}
		for _, stat := range item.Propstat {
			if !strings.Contains(stat.Status, " 200 ") || stat.Collection != nil {
				continue
			}
			fileID, parseErr := strconv.ParseInt(strings.TrimSpace(stat.FileID), 10, 64)
			if parseErr == nil && fileID > 0 {
				names[fileID] = name
			}
		}
	}
	if c.sharePaths != nil {
		c.sharePaths.mu.Lock()
		c.sharePaths.ownerNames = names
		c.sharePaths.ownerExpires = time.Now().Add(5 * time.Minute)
		c.sharePaths.mu.Unlock()
	}
	return names, nil
}
