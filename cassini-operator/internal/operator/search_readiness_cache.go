package operator

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Archive coverage is an O(archive + index) diagnostic. The operator panel
// polls every five seconds, so reuse a recent result rather than scanning the
// whole corpus on every read. A new storage probe, a changed local catalog, or
// an in-process index write invalidates it before the TTL expires.
const searchReadinessCacheTTL = 30 * time.Second

type searchReadinessSource struct {
	mode             string
	modeResolved     bool
	accessControlled bool
	probeAt          string
	catalogMod       int64
	catalogSize      int64
	catalogError     string
}

type searchReadinessCache struct {
	mu       sync.Mutex
	valid    bool
	at       time.Time
	source   searchReadinessSource
	revision uint64
	check    readinessCheck
}

func (rt *Runtime) searchReadinessSource() searchReadinessSource {
	source := searchReadinessSource{mode: rt.resolvedPublishSinkName()}
	if source.mode == publishSinkNextcloudFiles {
		source.accessControlled, source.modeResolved = ncStorage.mode()
		_, source.probeAt, _ = ncAccessSubstrate.lastProbeWithTime()
		return source
	}
	if rt.cfg.SiteRoot == "" {
		return source
	}
	info, err := os.Stat(filepath.Join(rt.cfg.SiteRoot, "catalog.json"))
	if err != nil {
		source.catalogError = err.Error()
		return source
	}
	source.catalogMod = info.ModTime().UnixNano()
	source.catalogSize = info.Size()
	return source
}

func (rt *Runtime) cachedSearchReadinessCheck(ctx context.Context) readinessCheck {
	source := rt.searchReadinessSource()
	cache := &rt.searchReadiness
	cache.mu.Lock()
	defer cache.mu.Unlock()
	revision := rt.searchRevision()
	if cache.valid && time.Since(cache.at) < searchReadinessCacheTTL && cache.source == source && cache.revision == revision && rt.searchRevision() == revision {
		return cache.check
	}
	check := rt.searchReadinessCheck(ctx)
	// A canceled request or an archive/index update during the scan can leave
	// this result incomplete. A later GET must retry rather than reuse it.
	if ctx.Err() == nil && rt.searchRevision() == revision && rt.searchReadinessSource() == source {
		cache.check = check
		cache.source = source
		cache.revision = revision
		cache.at = time.Now()
		cache.valid = true
	}
	return check
}

func (rt *Runtime) searchRevision() uint64 {
	if rt.searchStore == nil {
		return 0
	}
	return rt.searchStore.revision.Load()
}

func (rt *Runtime) invalidateSearchReadiness() {
	cache := &rt.searchReadiness
	cache.mu.Lock()
	cache.valid = false
	cache.mu.Unlock()
}
