package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// A path cache avoids an O(N) OCS list for every audio range. It never grants
// access: the actual GET/HEAD uses the caller's identity and Nextcloud checks
// that identity on every request. A stale path can only yield a miss.
type recordingSharePathCache struct {
	mu           sync.Mutex
	entries      map[string]recordingSharePathEntry
	ownerNames   map[int64]string
	ownerExpires time.Time
}

type recordingSharePathEntry struct {
	expires time.Time
	paths   map[string]string
}

const recordingSharePathTTL = 60 * time.Second

var errRecordingNotShared = errors.New("recording is not shared with caller")

func (cache *recordingSharePathCache) get(caller, name string) (string, bool) {
	if cache == nil {
		return "", false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[caller]
	if !ok || time.Now().After(entry.expires) {
		return "", false
	}
	value, ok := entry.paths[name]
	return value, ok
}

func (cache *recordingSharePathCache) put(caller string, paths map[string]string) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]recordingSharePathEntry)
	}
	cache.entries[caller] = recordingSharePathEntry{
		expires: time.Now().Add(recordingSharePathTTL), paths: paths,
	}
}

type directShareSnapshot struct {
	entries []json.RawMessage
	paths   map[string]string // original opus basename -> caller-relative DAV path
}

func (c ExAppConfig) directShareSnapshot(ctx context.Context, client *http.Client, caller string, metadata *meetingMetadataStore) (directShareSnapshot, error) {
	shares, err := c.receivedShares(ctx, client, caller)
	if err != nil {
		return directShareSnapshot{}, err
	}
	ids := make([]int64, 0, len(shares))
	for _, share := range shares {
		if share.UIDFileOwner == ncRecordingsOwner && share.FileSource > 0 && share.Permissions&ncShareRead != 0 {
			ids = append(ids, share.FileSource)
		}
	}
	known, err := metadata.EntriesFor(ctx, ids)
	if err != nil {
		return directShareSnapshot{}, fmt.Errorf("read meeting metadata: %w", err)
	}
	missing := false
	for _, id := range ids {
		if known[id] == nil {
			missing = true
			break
		}
	}
	var ownerNames map[int64]string
	if missing {
		ownerNames, err = c.ownerRecordingNames(ctx, client)
		if err != nil {
			return directShareSnapshot{}, fmt.Errorf("recover meeting names: %w", err)
		}
	}
	result := directShareSnapshot{entries: []json.RawMessage{}, paths: map[string]string{}}
	for _, share := range shares {
		if share.UIDFileOwner != ncRecordingsOwner || share.FileSource <= 0 || share.Permissions&ncShareRead == 0 || (share.ItemType != "" && share.ItemType != "file") {
			continue
		}
		recipientPath, err := share.recipientPath()
		if err != nil {
			continue
		}
		entry := known[share.FileSource]
		name := ""
		if entry != nil {
			var probe struct {
				AudioPath string `json:"audioPath"`
			}
			if json.Unmarshal(entry, &probe) != nil {
				continue
			}
			name = catalogEntryOpusName(probe.AudioPath, "")
		} else {
			// The owner's current archive inventory gives the original name
			// even if the recipient renamed their share mount. It also keeps
			// unrelated files owned by the service account out of Cassini.
			name = ownerNames[share.FileSource]
			if name == "" {
				continue
			}
			fallback, marshalErr := json.Marshal(map[string]any{
				"id":        strings.TrimSuffix(name, ".opus"),
				"title":     strings.TrimSuffix(name, ".opus"),
				"audioPath": "./meetings/" + name,
			})
			if marshalErr != nil {
				return directShareSnapshot{}, marshalErr
			}
			entry = fallback
		}
		if !strings.HasSuffix(name, ".opus") || path.Base(name) != name {
			continue
		}
		if _, duplicate := result.paths[name]; duplicate {
			continue
		}
		result.paths[name] = recipientPath
		result.entries = append(result.entries, entry)
	}
	// Stable newest-first presentation even though OCS can return user, group
	// and Team shares in separate buckets.
	sort.SliceStable(result.entries, func(i, j int) bool {
		var left, right struct {
			DateLabel string `json:"dateLabel"`
		}
		_ = json.Unmarshal(result.entries[i], &left)
		_ = json.Unmarshal(result.entries[j], &right)
		return left.DateLabel > right.DateLabel
	})
	if c.sharePaths != nil {
		c.sharePaths.put(caller, result.paths)
	}
	return result, nil
}

func (c ExAppConfig) recipientRecordingPath(ctx context.Context, client *http.Client, caller, opusName string, metadata *meetingMetadataStore) (string, error) {
	if path.Base(opusName) != opusName || !strings.HasSuffix(opusName, ".opus") {
		return "", fmt.Errorf("invalid recording name")
	}
	if cached, ok := c.sharePaths.get(caller, opusName); ok {
		return cached, nil
	}
	snapshot, err := c.directShareSnapshot(ctx, client, caller, metadata)
	if err != nil {
		return "", err
	}
	rel, ok := snapshot.paths[opusName]
	if !ok {
		return "", errRecordingNotShared
	}
	return rel, nil
}

// currentRecordingPath bypasses the short media path cache. Annotation reads
// and mutations must not accept a different file placed at a revoked mount.
func (c ExAppConfig) currentRecordingPath(ctx context.Context, client *http.Client, caller, opusName string, metadata *meetingMetadataStore) (string, error) {
	snapshot, err := c.directShareSnapshot(ctx, client, caller, metadata)
	if err != nil {
		return "", err
	}
	rel, ok := snapshot.paths[opusName]
	if !ok {
		return "", errRecordingNotShared
	}
	return rel, nil
}
