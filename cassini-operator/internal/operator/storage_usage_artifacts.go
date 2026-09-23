package operator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type detailedStorageUsageResponse struct {
	MeasuredAt  string                     `json:"measured_at"`
	DurationMS  float64                    `json:"duration_ms"`
	Published   []storageUsageSource       `json:"published"`
	Directories []artifactStorageUsageRoot `json:"directories"`
}

type artifactStorageUsageRoot struct {
	ID          string                    `json:"id"`
	Label       string                    `json:"label"`
	Location    string                    `json:"location"`
	Bytes       int64                     `json:"bytes"`
	Files       int                       `json:"files"`
	Collections int                       `json:"collections"`
	Formats     []artifactStorageFileType `json:"formats"`
	Error       string                    `json:"error,omitempty"`
}

type artifactStorageFileType struct {
	Extension string `json:"extension"`
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
}

func (c ExAppConfig) detailedStorageUsageHandler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/usage/details" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, rt.cachedDetailedStorageUsage())
		case http.MethodPost:
			ctx, cancel := context.WithTimeout(r.Context(), storageUsageTimeout)
			defer cancel()
			writeJSON(w, http.StatusOK, rt.refreshDetailedStorageUsage(ctx, c))
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
	})
}

func (rt *Runtime) cachedDetailedStorageUsage() detailedStorageUsageResponse {
	rt.detailedStorageUsageMu.RLock()
	defer rt.detailedStorageUsageMu.RUnlock()
	result := rt.detailedStorageUsage
	if result.Published == nil {
		result.Published = []storageUsageSource{}
	}
	if result.Directories == nil {
		result.Directories = []artifactStorageUsageRoot{}
	}
	return result
}

func (rt *Runtime) refreshDetailedStorageUsage(ctx context.Context, c ExAppConfig) detailedStorageUsageResponse {
	rt.detailedStorageUsageRefreshMu.Lock()
	defer rt.detailedStorageUsageRefreshMu.Unlock()
	result := c.scanDetailedStorageUsage(ctx, rt.cfg.WorkRoot)
	rt.detailedStorageUsageMu.Lock()
	rt.detailedStorageUsage = result
	rt.detailedStorageUsageMu.Unlock()
	return result
}

func (c ExAppConfig) scanDetailedStorageUsage(ctx context.Context, workRoot string) detailedStorageUsageResponse {
	started := time.Now()
	result := detailedStorageUsageResponse{}
	for _, root := range []struct{ id, label, path string }{
		{"default", "Default storage mode", recordingsRootFor(false)},
		{"access-controlled", "Access-controlled storage mode", recordingsRootFor(true)},
	} {
		source := storageUsageSource{ID: root.id, Label: root.label, Location: root.path}
		if strings.TrimSpace(c.NextcloudURL) == "" {
			source.Error = "Nextcloud Files is not configured"
		} else {
			source.Bytes, source.Error = c.ncArchiveLogicalBytes(ctx, root.path)
		}
		result.Published = append(result.Published, source)
	}
	for _, root := range []struct{ id, label, path string }{
		{"current", "Working archive", currentRoot(workRoot)},
		{"runs", "Build history", runsRoot(workRoot)},
	} {
		result.Directories = append(result.Directories, scanArtifactStorageRoot(ctx, root.id, root.label, root.path))
	}
	result.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	result.MeasuredAt = nowUTCString()
	return result
}

func scanArtifactStorageRoot(ctx context.Context, id, label, rootPath string) artifactStorageUsageRoot {
	root := artifactStorageUsageRoot{ID: id, Label: label, Location: rootPath, Formats: []artifactStorageFileType{}}
	formats := make(map[string]*artifactStorageFileType)
	err := filepath.WalkDir(rootPath, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			root.Collections++
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > math.MaxInt64-root.Bytes {
			return fmt.Errorf("storage size exceeds supported range")
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension == "" {
			extension = "none"
		}
		format := formats[extension]
		if format == nil {
			format = &artifactStorageFileType{Extension: extension}
			formats[extension] = format
		}
		format.Bytes += info.Size()
		format.Files++
		root.Bytes += info.Size()
		root.Files++
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return root
	}
	if err != nil {
		root.Error = err.Error()
	}
	for _, format := range formats {
		root.Formats = append(root.Formats, *format)
	}
	sort.Slice(root.Formats, func(i, j int) bool {
		if root.Formats[i].Bytes == root.Formats[j].Bytes {
			return root.Formats[i].Extension < root.Formats[j].Extension
		}
		return root.Formats[i].Bytes > root.Formats[j].Bytes
	})
	return root
}
