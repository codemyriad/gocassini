package operator

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Retention never follows redirects: AppAPI headers are credentials even when
// net/http would normally forward them to a different host.
func retentionDAVClient(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func retentionLeafPath(rel string) error {
	if path.Clean(rel) != rel || !strings.HasPrefix(rel, ncRecordingsRoot+"/meetings/") || path.Dir(rel) != ncRecordingsRoot+"/meetings" || strings.ContainsAny(rel, "\\\x00\r\n") {
		return fmt.Errorf("retention target must be a leaf in the managed meetings collection")
	}
	return nil
}

func strongDAVETag(etag string) bool {
	return len(etag) > 2 && etag[0] == '"' && etag[len(etag)-1] == '"' && !strings.ContainsAny(etag[1:len(etag)-1], "\"\r\n")
}

// davRetentionLeaf validates the actual response href and successful properties.
// A path supplied by a DAV response is never used as a mutation target.
func (c ExAppConfig) davRetentionLeaf(ctx context.Context, client *http.Client, rel string) (ncLeafState, error) {
	if err := retentionLeafPath(rel); err != nil {
		return ncLeafState{}, err
	}
	endpoint := c.davFileURL(ncRecordingsOwner, rel)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", endpoint, strings.NewReader(`<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:fileid/><d:getetag/><d:getcontentlength/></d:prop></d:propfind>`))
	if err != nil {
		return ncLeafState{}, err
	}
	c.setAppAPIDAVHeadersForUser(req, ncRecordingsOwner)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	resp, err := retentionDAVClient(client).Do(req)
	if err != nil {
		return ncLeafState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ncLeafState{}, nil
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return ncLeafState{}, fmt.Errorf("retention PROPFIND: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return ncLeafState{}, err
	}
	if len(body) > 1<<20 {
		return ncLeafState{}, fmt.Errorf("retention PROPFIND response too large")
	}
	var ms struct {
		Responses []struct {
			Href     string `xml:"href"`
			Propstat []struct {
				Status string `xml:"status"`
				ID     string `xml:"prop>fileid"`
				ETag   string `xml:"prop>getetag"`
				Size   string `xml:"prop>getcontentlength"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return ncLeafState{}, err
	}
	if len(ms.Responses) != 1 {
		return ncLeafState{}, fmt.Errorf("retention PROPFIND must name exactly one leaf")
	}
	base, _ := url.Parse(endpoint)
	href, err := url.Parse(ms.Responses[0].Href)
	if err != nil || href == nil || href.String() == "" {
		return ncLeafState{}, fmt.Errorf("retention PROPFIND invalid href")
	}
	resolved := base.ResolveReference(href)
	if resolved.Scheme != base.Scheme || resolved.Host != base.Host || resolved.Path != base.Path || resolved.RawQuery != "" || resolved.Fragment != "" || resolved.User != nil {
		return ncLeafState{}, fmt.Errorf("retention PROPFIND href does not match requested leaf")
	}
	state := ncLeafState{Exists: true, Size: -1}
	for _, ps := range ms.Responses[0].Propstat {
		fields := strings.Fields(ps.Status)
		if len(fields) < 2 || fields[1] != "200" {
			continue
		}
		if ps.ID != "" {
			state.FileID, err = strconv.ParseInt(strings.TrimSpace(ps.ID), 10, 64)
			if err != nil {
				return ncLeafState{}, err
			}
		}
		if ps.Size != "" {
			state.Size, err = strconv.ParseInt(strings.TrimSpace(ps.Size), 10, 64)
			if err != nil {
				return ncLeafState{}, err
			}
		}
		if ps.ETag != "" {
			state.ETag = strings.TrimSpace(ps.ETag)
		}
	}
	if state.FileID <= 0 || state.Size < 0 || !strongDAVETag(state.ETag) {
		return ncLeafState{}, fmt.Errorf("retention leaf lacks a valid identity, size or strong ETag")
	}
	return state, nil
}

// The caller journals intent before invoking these methods and independently
// reads identity and bytes afterwards, including after transport errors.
func (c ExAppConfig) davRetentionMutation(ctx context.Context, client *http.Client, method, rel, destination, etag string) error {
	if err := retentionLeafPath(rel); err != nil {
		return err
	}
	if !strongDAVETag(etag) {
		return fmt.Errorf("retention mutation requires a strong ETag")
	}
	if method != "MOVE" && method != http.MethodDelete {
		return fmt.Errorf("unsupported retention method")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.davFileURL(ncRecordingsOwner, rel), nil)
	if err != nil {
		return err
	}
	if method == "MOVE" {
		if err := retentionLeafPath(destination); err != nil {
			return err
		}
		if rel == destination {
			return fmt.Errorf("retention destination equals source")
		}
		req.Header.Set("Destination", c.davFileURL(ncRecordingsOwner, destination))
		req.Header.Set("Overwrite", "F")
	}
	req.Header.Set("If-Match", etag)
	c.setAppAPIDAVHeadersForUser(req, ncRecordingsOwner)
	resp, err := retentionDAVClient(client).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return fmt.Errorf("retention %s: %w", method, errDAVPreconditionFailed)
	}
	if resp.StatusCode != http.StatusNoContent && !(method == "MOVE" && resp.StatusCode == http.StatusCreated) {
		return fmt.Errorf("retention %s: HTTP %d", method, resp.StatusCode)
	}
	return nil
}

func (c ExAppConfig) davRetentionPut(ctx context.Context, client *http.Client, rel, local, etag string) error {
	if err := retentionLeafPath(rel); err != nil {
		return err
	}
	if !strongDAVETag(etag) {
		return fmt.Errorf("retention PUT requires a strong ETag")
	}
	_, _, err := c.davPutFileIfMatch(ctx, retentionDAVClient(client), ncRecordingsOwner, rel, local, "application/json", etag)
	return err
}

// Restores may place Opus bytes at a JSON filename. Read the live signature with
// the same ETag used for evaluation; filenames never establish media state.
func (c ExAppConfig) davMeetingRepresentation(ctx context.Context, client *http.Client, rel, etag string) (string, error) {
	if err := retentionLeafPath(rel); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.davFileURL(ncRecordingsOwner, rel), nil)
	if err != nil {
		return "", err
	}
	c.setAppAPIDAVHeadersForUser(req, ncRecordingsOwner)
	req.Header.Set("Range", "bytes=0-63")
	req.Header.Set("If-Match", etag)
	resp, err := retentionDAVClient(client).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		return "", fmt.Errorf("representation probe: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(string(raw), "OggS") {
		return "opus", nil
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return "transcription", nil
	}
	return "", fmt.Errorf("unrecognized meeting representation")
}

func (c ExAppConfig) davRetentionDelete(ctx context.Context, client *http.Client, rel, etag string) error {
	return c.davRetentionMutation(ctx, client, http.MethodDelete, rel, "", etag)
}
