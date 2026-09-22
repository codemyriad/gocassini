package operator

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	storageUsageTimeout        = 30 * time.Second
	storageUsageMaxMultistatus = 64 << 20
)

// storageUsageResponse reports logical file bytes in the storage locations that
// hold recording and build artifacts. A source can fail independently: returning
// its error beside successful values is more useful than replacing the panel
// with a blank error page.
type storageUsageResponse struct {
	MeasuredAt string               `json:"measured_at"`
	Sources    []storageUsageSource `json:"sources"`
}

type storageUsageSource struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Location string `json:"location"`
	Bytes    int64  `json:"bytes"`
	Error    string `json:"error,omitempty"`
}

func (c ExAppConfig) storageUsageHandler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/usage" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), storageUsageTimeout)
		defer cancel()
		writeJSON(w, http.StatusOK, c.storageUsage(ctx, rt))
	})
}

func (c ExAppConfig) storageUsage(ctx context.Context, rt *Runtime) storageUsageResponse {
	result := storageUsageResponse{MeasuredAt: nowUTCString()}
	addLocal := func(id, label, dir string) {
		source := storageUsageSource{ID: id, Label: label, Location: "Cassini persistent storage"}
		source.Bytes, source.Error = directoryLogicalBytes(dir)
		result.Sources = append(result.Sources, source)
	}

	if strings.TrimSpace(c.NextcloudURL) == "" {
		addLocal("published", "Published meetings", rt.cfg.SiteRoot)
	} else {
		source := storageUsageSource{ID: "published", Label: "Published meetings", Location: "Nextcloud Files"}
		source.Bytes, source.Error = c.ncArchiveLogicalBytes(ctx, recordingsRootFor(ncStorage.accessControlled()))
		result.Sources = append(result.Sources, source)
	}
	addLocal("current", "Current working archive", currentRoot(rt.cfg.WorkRoot))
	addLocal("runs", "Build history", runsRoot(rt.cfg.WorkRoot))

	// A current ExApp publishes to Nextcloud Files, but an upgrade can retain a
	// pre-migration local archive. Do not add a distracting all-zero row on a
	// normal install; when it holds bytes it is material storage an admin needs
	// to see before deciding what to remove.
	if strings.TrimSpace(c.NextcloudURL) != "" {
		if legacy, err := directoryLogicalBytes(rt.cfg.SiteRoot); err != "" || legacy > 0 {
			result.Sources = append(result.Sources, storageUsageSource{
				ID:       "legacy-site",
				Label:    "Legacy local published archive",
				Location: "Cassini persistent storage",
				Bytes:    legacy,
				Error:    err,
			})
		}
	}
	return result
}

// directoryLogicalBytes is deliberately an apparent-size calculation, not an
// allocated-block calculation. It does not follow symlinks and reports a fresh
// install's absent directory as empty.
func directoryLogicalBytes(dir string) (int64, string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return 0, "storage location is not configured"
	}
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > math.MaxInt64-total {
			return fmt.Errorf("storage size exceeds supported range")
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, ""
	}
	if err != nil {
		return 0, err.Error()
	}
	return total, ""
}

// ncArchiveLogicalBytes lists each collection below root with Depth: 1 and sums
// file lengths. A recursive Depth: infinity request is not portable across DAV
// backends and makes response bounds impossible to enforce.
func (c ExAppConfig) ncArchiveLogicalBytes(ctx context.Context, root string) (int64, string) {
	client := &http.Client{Timeout: storageUsageTimeout}
	pending := []string{strings.Trim(root, "/")}
	seen := make(map[string]bool)
	var total int64

	for len(pending) > 0 {
		dir := pending[0]
		pending = pending[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true

		entries, missing, err := c.davListSizes(ctx, client, ncRecordingsOwner, dir)
		if missing && dir == strings.Trim(root, "/") {
			return 0, ""
		}
		if err != nil {
			return 0, err.Error()
		}
		for _, entry := range entries {
			if entry.collection {
				pending = append(pending, entry.relPath)
				continue
			}
			if entry.size > math.MaxInt64-total {
				return 0, "storage size exceeds supported range"
			}
			total += entry.size
		}
	}
	return total, ""
}

type davSizeEntry struct {
	relPath    string
	size       int64
	collection bool
}

func (c ExAppConfig) davListSizes(ctx context.Context, client *http.Client, userID, relDir string) ([]davSizeEntry, bool, error) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.davFileURL(userID, relDir), bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.ContentLength = int64(len(body))
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("PROPFIND %s -> %d", relDir, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, storageUsageMaxMultistatus+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > storageUsageMaxMultistatus {
		return nil, false, fmt.Errorf("PROPFIND %s: multistatus exceeds %d bytes", relDir, storageUsageMaxMultistatus)
	}
	var multistatus struct {
		Responses []struct {
			Href     string `xml:"href"`
			Propstat []struct {
				Length     string    `xml:"prop>getcontentlength"`
				Collection *struct{} `xml:"prop>resourcetype>collection"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(raw, &multistatus); err != nil {
		return nil, false, fmt.Errorf("parse storage multistatus: %w", err)
	}

	entries := make([]davSizeEntry, 0, len(multistatus.Responses))
	for _, response := range multistatus.Responses {
		relPath, err := davRelativePath(userID, response.Href)
		if err != nil || relPath == "" || relPath == strings.Trim(relDir, "/") {
			continue // the queried collection itself, or a malformed child
		}
		entry := davSizeEntry{relPath: relPath}
		for _, propstat := range response.Propstat {
			if propstat.Collection != nil {
				entry.collection = true
			}
			if propstat.Length != "" {
				if _, err := fmt.Sscan(propstat.Length, &entry.size); err != nil || entry.size < 0 {
					return nil, false, fmt.Errorf("invalid content length for %s", relPath)
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries, false, nil
}

func davRelativePath(userID, href string) (string, error) {
	u, err := url.Parse(href)
	if err != nil {
		return "", err
	}
	prefix := "/remote.php/dav/files/" + userID + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", fmt.Errorf("unexpected DAV href %q", href)
	}
	return path.Clean(strings.TrimPrefix(u.Path, prefix)), nil
}
