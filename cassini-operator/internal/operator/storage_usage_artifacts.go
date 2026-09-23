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
)

type artifactStorageUsageResponse struct {
	MeasuredAt string                     `json:"measured_at"`
	Roots      []artifactStorageUsageRoot `json:"roots"`
}

type artifactStorageUsageRoot struct {
	ID          string                     `json:"id"`
	Label       string                     `json:"label"`
	Location    string                     `json:"location"`
	Bytes       int64                      `json:"bytes"`
	Files       int                        `json:"files"`
	Collections int                        `json:"collections"`
	Items       []artifactStorageUsageItem `json:"items"`
	Error       string                     `json:"error,omitempty"`
}

type artifactStorageUsageItem struct {
	Name        string                    `json:"name"`
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

func artifactStorageUsageHandler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/usage/artifacts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, rt.cachedArtifactStorageUsage())
		case http.MethodPost:
			ctx, cancel := context.WithTimeout(r.Context(), storageUsageTimeout)
			defer cancel()
			writeJSON(w, http.StatusOK, rt.refreshArtifactStorageUsage(ctx))
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
	})
}

func (rt *Runtime) cachedArtifactStorageUsage() artifactStorageUsageResponse {
	rt.artifactStorageUsageMu.RLock()
	defer rt.artifactStorageUsageMu.RUnlock()
	result := rt.artifactStorageUsage
	if result.Roots == nil {
		result.Roots = []artifactStorageUsageRoot{}
	}
	return result
}

func (rt *Runtime) refreshArtifactStorageUsage(ctx context.Context) artifactStorageUsageResponse {
	rt.artifactStorageUsageRefreshMu.Lock()
	defer rt.artifactStorageUsageRefreshMu.Unlock()
	result := scanArtifactStorageUsage(ctx, rt.cfg.WorkRoot)
	rt.artifactStorageUsageMu.Lock()
	rt.artifactStorageUsage = result
	rt.artifactStorageUsageMu.Unlock()
	return result
}

func scanArtifactStorageUsage(ctx context.Context, workRoot string) artifactStorageUsageResponse {
	result := artifactStorageUsageResponse{}
	for _, root := range []struct {
		id, label, path string
	}{
		{"current", "Current working archive", currentRoot(workRoot)},
		{"runs", "Build history", runsRoot(workRoot)},
	} {
		row := scanArtifactStorageRoot(ctx, root.id, root.label, root.path)
		result.Roots = append(result.Roots, row)
	}
	result.MeasuredAt = nowUTCString()
	return result
}

func scanArtifactStorageRoot(ctx context.Context, id, label, rootPath string) artifactStorageUsageRoot {
	root := artifactStorageUsageRoot{
		ID:       id,
		Label:    label,
		Location: rootPath,
		Items:    []artifactStorageUsageItem{},
	}
	entries, err := os.ReadDir(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return root
	}
	if err != nil {
		root.Error = err.Error()
		return root
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			root.Error = err.Error()
			return root
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		item := scanArtifactStorageItem(ctx, filepath.Join(rootPath, entry.Name()), entry.Name())
		root.Items = append(root.Items, item)
		if item.Error != "" {
			continue
		}
		if item.Bytes > math.MaxInt64-root.Bytes {
			root.Error = "storage size exceeds supported range"
			return root
		}
		root.Bytes += item.Bytes
		root.Files += item.Files
		root.Collections += item.Collections
	}
	return root
}

func scanArtifactStorageItem(ctx context.Context, itemPath, name string) artifactStorageUsageItem {
	item := artifactStorageUsageItem{Name: name, Formats: []artifactStorageFileType{}}
	formats := make(map[string]*artifactStorageFileType)
	err := filepath.WalkDir(itemPath, func(path string, entry os.DirEntry, walkErr error) error {
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
			item.Collections++
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() > math.MaxInt64-item.Bytes {
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
		item.Bytes += info.Size()
		item.Files++
		return nil
	})
	if err != nil {
		item.Error = err.Error()
	}
	for _, format := range formats {
		item.Formats = append(item.Formats, *format)
	}
	sort.Slice(item.Formats, func(i, j int) bool {
		if item.Formats[i].Bytes == item.Formats[j].Bytes {
			return item.Formats[i].Extension < item.Formats[j].Extension
		}
		return item.Formats[i].Bytes > item.Formats[j].Bytes
	})
	return item
}
