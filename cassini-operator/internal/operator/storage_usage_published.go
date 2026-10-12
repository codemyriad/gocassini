package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type storageDateKey struct {
	Path   string
	FileID int64
	ETag   string
}
type storagePublication struct{ root, extension, date string }

// Read jobs independently of local artifact availability: local retention can
// remove the archive while the published Nextcloud file is still present.
func (rt *Runtime) storagePublications(ctx context.Context) (map[string]storagePublication, error) {
	rows, err := rt.store.db.QueryContext(ctx, `SELECT j.id, j.artifact_site_path,
 a.artifact_opus_path, a.publish_finished_at FROM jobs j
 LEFT JOIN job_attempts a ON a.job_id=j.id AND a.state='succeeded'
 ORDER BY j.id, a.attempt_number DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storagePublication{}
	for rows.Next() {
		var id string
		var root, artifact, published sql.NullString
		if err := rows.Scan(&id, &root, &artifact, &published); err != nil {
			return nil, err
		}
		if _, exists := out[id]; exists {
			continue
		}
		date := ""
		if t := retentionAnchor(&published.String); !t.IsZero() {
			date = t.UTC().Format("2006-01-02")
		}
		out[id] = storagePublication{strings.Trim(root.String, "/"), filepath.Ext(artifact.String), date}
	}
	return out, rows.Err()
}

// This is informational dating, not retention eligibility. Unlike retention's
// stricter validation, either valid timestamp is enough to date the chart.
func storageMetadataDate(recorded, created string) string {
	if t, err := time.Parse("2006-01-02T15:04:05", recorded); err == nil {
		return t.Format("2006-01-02")
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return ""
}
func storageRawMetadataDate(raw json.RawMessage) string {
	var dates struct {
		Recorded string `json:"recordedAtLocal"`
		Created  string `json:"createdAtUtc"`
	}
	if json.Unmarshal(raw, &dates) != nil {
		return ""
	}
	return storageMetadataDate(dates.Recorded, dates.Created)
}

func (rt *Runtime) publishedStorageCategory(ctx context.Context, c ExAppConfig, entries []davSizeEntry) (*storageUsageCategory, string) {
	result := &storageUsageCategory{ID: "published", Days: []storageUsageDay{}}
	days := map[string]*storageUsageDay{}
	publications, lookupErr := rt.storagePublications(ctx)
	ids := make([]int64, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.fileID)
	}
	metadata, metadataErr := rt.meetingMetadata.EntriesFor(ctx, ids)
	var problems []string
	if lookupErr != nil {
		problems = append(problems, "Could not read publication dates: "+lookupErr.Error())
	}
	if metadataErr != nil {
		problems = append(problems, "Could not read recording metadata: "+metadataErr.Error())
	}
	cache := map[storageDateKey]string{}
	failed := 0
	var firstError error
	for _, entry := range entries {
		result.Bytes += entry.size
		result.Files++
		date := ""
		name := path.Base(entry.relPath)
		if isMeetingFile(name) && path.Base(path.Dir(entry.relPath)) == "meetings" && lookupErr == nil {
			publication, knownJob := publications[strings.TrimSuffix(name, path.Ext(name))]
			if knownJob {
				// A different root or format may contain an older retained copy. Never
				// date that copy using a later publication's timestamp.
				if entry.relPath == publication.root+"/meetings/"+name && path.Ext(name) == publication.extension {
					date = publication.date
				}
			} else {
				key := storageDateKey{entry.relPath, entry.fileID, entry.etag}
				var err error
				date, err = rt.orphanStorageDate(ctx, c, entry, metadata[entry.fileID], key)
				if err != nil {
					failed++
					if firstError == nil {
						firstError = err
					}
				} else if entry.fileID > 0 && strongDAVETag(entry.etag) {
					cache[key] = date
				}
			}
		}
		if date == "" {
			result.UndatedBytes += entry.size
			result.UndatedFiles++
			continue
		}
		day := days[date]
		if day == nil {
			day = &storageUsageDay{Date: date}
			days[date] = day
		}
		day.Bytes += entry.size
		day.Files++
	}
	rt.publishedStorageDates = cache
	for _, day := range days {
		result.Days = append(result.Days, *day)
	}
	sort.Slice(result.Days, func(i, j int) bool { return result.Days[i].Date < result.Days[j].Date })
	if failed > 0 {
		problems = append(problems, fmt.Sprintf("Could not inspect dates for %d published files: %v", failed, firstError))
	}
	return result, strings.Join(problems, "; ")
}

func (rt *Runtime) orphanStorageDate(ctx context.Context, c ExAppConfig, entry davSizeEntry, raw json.RawMessage, key storageDateKey) (string, error) {
	// Identity must match the scanned file before using any cached description.
	if entry.fileID > 0 {
		if date := storageRawMetadataDate(raw); date != "" {
			return date, nil
		}
		m, ok, err := rt.store.meetingLifecycle(ctx, path.Base(entry.relPath))
		if err != nil {
			return "", err
		}
		if ok && m.FileID == entry.fileID && m.Path == entry.relPath {
			if date := storageMetadataDate(m.RecordedAtLocal, m.CreatedAtUTC); date != "" {
				return date, nil
			}
		}
		if strongDAVETag(entry.etag) {
			if date, ok := rt.publishedStorageDates[key]; ok {
				return date, nil
			}
		}
	}
	if !strongDAVETag(entry.etag) {
		return "", fmt.Errorf("%s has no stable ETag for reading dates", entry.relPath)
	}
	if entry.size <= 0 {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "cassini-storage-date-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "meeting"+path.Ext(entry.relPath))
	client := retentionDAVClient(&http.Client{Timeout: storageUsageTimeout})
	// Bound the download to the size observed in the scan and reject a changed
	// file. Inspection owns both the OPUS and JSON meeting formats.
	_, written, _, err := c.davDownloadFile(ctx, client, ncRecordingsOwner, entry.relPath, file, entry.size, entry.etag)
	if err != nil {
		return "", err
	}
	if written != entry.size {
		return "", fmt.Errorf("%s changed size during calculation", entry.relPath)
	}
	raw, err = exec.CommandContext(ctx, rt.cfg.CassiniBin, "inspect", "--meeting-times", file).Output()
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", entry.relPath, err)
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("inspect %s: invalid timestamp response", entry.relPath)
	}
	return storageRawMetadataDate(raw), nil
}
