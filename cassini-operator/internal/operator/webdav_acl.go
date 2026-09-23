package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

const emptyCatalogJSON = `{"version":"cassini.viewer.catalog.v1","meetings":[]}`

const ncFilesACLMediaType = "application/xml; charset=utf-8"

type catalogResolveOutcome int

const (
	catalogResolveOK catalogResolveOutcome = iota
	catalogResolveNoArchive
	catalogResolveUnavailable
	catalogResolveScanFailed
	catalogResolveNoMount
)

type resolvedCatalog struct {
	raw  []byte
	body []byte
}

func (c ExAppConfig) resolveCatalogForCaller(ctx context.Context, client *http.Client, caller string, logger *log.Logger) (resolvedCatalog, catalogResolveOutcome) {
	snapshot, err := c.directShareSnapshot(ctx, client, caller, c.meetingMetadata)
	if err != nil {
		if logger != nil {
			logger.Printf("nc shares: caller=%s: %v", caller, err)
		}
		empty := []byte(emptyCatalogJSON)
		return resolvedCatalog{raw: empty, body: empty}, catalogResolveScanFailed
	}
	body, err := json.Marshal(siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: snapshot.entries})
	if err != nil {
		empty := []byte(emptyCatalogJSON)
		return resolvedCatalog{raw: empty, body: empty}, catalogResolveUnavailable
	}
	return resolvedCatalog{raw: body, body: body}, catalogResolveOK
}

func (c ExAppConfig) serveFilteredCatalog(ctx context.Context, w http.ResponseWriter, client *http.Client, caller string, logger *log.Logger) {
	resolved, outcome := c.resolveCatalogForCaller(ctx, client, caller, logger)
	if outcome == catalogResolveUnavailable || outcome == catalogResolveScanFailed {
		http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
		return
	}
	writeCatalogJSON(w, resolved.body)
}

func writeCatalogJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

type ncLeafState struct {
	Exists   bool
	FileID   int64
	Size     int64
	Checksum string
	ETag     string
}

func (c ExAppConfig) davPropfindLeafState(ctx context.Context, client *http.Client, userID, relPath string) (ncLeafState, error) {
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">` +
		`<d:prop><oc:fileid/><d:getcontentlength/><d:getetag/><oc:checksums/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.davFileURL(userID, relPath), bytes.NewReader(reqBody))
	if err != nil {
		return ncLeafState{}, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		return ncLeafState{}, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ncLeafState{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ncLeafState{}, fmt.Errorf("PROPFIND %s -> %d", relPath, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ncLeafState{}, err
	}
	var ms struct {
		Responses []struct {
			Propstat []struct {
				FileID    string   `xml:"prop>fileid"`
				Length    string   `xml:"prop>getcontentlength"`
				ETag      string   `xml:"prop>getetag"`
				Checksums []string `xml:"prop>checksums>checksum"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return ncLeafState{}, fmt.Errorf("parse leaf multistatus for %s: %w", relPath, err)
	}
	if len(ms.Responses) == 0 {
		return ncLeafState{}, fmt.Errorf("PROPFIND %s: multistatus named no resource", relPath)
	}
	state := ncLeafState{Exists: true}
	// A property the resource does not carry comes back in its own 404 propstat
	// with an empty value, so both fields are gathered across every propstat and
	// an unparseable or absent length simply leaves Size at zero.
	for _, ps := range ms.Responses[0].Propstat {
		if trimmed := strings.TrimSpace(ps.FileID); trimmed != "" {
			if n, convErr := strconv.ParseInt(trimmed, 10, 64); convErr == nil {
				state.FileID = n
			}
		}
		if trimmed := strings.TrimSpace(ps.Length); trimmed != "" {
			if n, convErr := strconv.ParseInt(trimmed, 10, 64); convErr == nil {
				state.Size = n
			}
		}
		if etag := strings.TrimSpace(ps.ETag); etag != "" {
			state.ETag = etag
		}
		for _, checksum := range ps.Checksums {
			if checksum = strings.TrimSpace(checksum); strings.HasPrefix(strings.ToLower(checksum), "sha256:") {
				state.Checksum = checksum[len("sha256:"):]
			}
		}
	}
	return state, nil
}

func (c ExAppConfig) davGetBytes(ctx context.Context, client *http.Client, userID, relPath string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.davFileURL(userID, relPath), nil)
	if err != nil {
		return nil, 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainClose(resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
