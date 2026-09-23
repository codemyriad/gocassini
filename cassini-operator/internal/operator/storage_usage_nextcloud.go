package operator

import (
	"context"
	"net/http"
	"strings"
)

func (c ExAppConfig) nextcloudStorageUsageHandler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage/usage/nextcloud" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, rt.cachedNextcloudStorageUsage())
		case http.MethodPost:
			ctx, cancel := context.WithTimeout(r.Context(), storageUsageTimeout)
			defer cancel()
			writeJSON(w, http.StatusOK, rt.refreshNextcloudStorageUsage(ctx, c))
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
	})
}

func (rt *Runtime) cachedNextcloudStorageUsage() storageUsageResponse {
	rt.nextcloudStorageUsageMu.RLock()
	defer rt.nextcloudStorageUsageMu.RUnlock()
	result := rt.nextcloudStorageUsage
	if result.Sources == nil {
		result.Sources = []storageUsageSource{}
	}
	return result
}

func (rt *Runtime) refreshNextcloudStorageUsage(ctx context.Context, c ExAppConfig) storageUsageResponse {
	rt.nextcloudStorageUsageRefreshMu.Lock()
	defer rt.nextcloudStorageUsageRefreshMu.Unlock()
	result := c.scanNextcloudStorageUsage(ctx)
	rt.nextcloudStorageUsageMu.Lock()
	rt.nextcloudStorageUsage = result
	rt.nextcloudStorageUsageMu.Unlock()
	return result
}

func (c ExAppConfig) scanNextcloudStorageUsage(ctx context.Context) storageUsageResponse {
	result := storageUsageResponse{}
	roots := []struct {
		id, label, root string
	}{
		{"default", "Default storage mode", recordingsRootFor(false)},
		{"access-controlled", "Access-controlled storage mode", recordingsRootFor(true)},
	}
	for _, root := range roots {
		source := storageUsageSource{
			ID:       root.id,
			Label:    root.label,
			Location: root.root,
		}
		if strings.TrimSpace(c.NextcloudURL) == "" {
			source.Error = "Nextcloud Files is not configured"
		} else {
			source.Bytes, _, _, _, source.Error = c.ncArchiveLogicalBytes(ctx, root.root)
		}
		result.Sources = append(result.Sources, source)
	}
	result.MeasuredAt = nowUTCString()
	return result
}
