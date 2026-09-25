package operator

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var testCatalogRegistry sync.Map // test server URL -> metadata index

func testMeetingFileID(name string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum32()) + 1
}

func registerTestCatalog(t *testing.T, serverURL, raw string) {
	t.Helper()
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	var catalog siteCatalog
	if err := json.Unmarshal([]byte(raw), &catalog); err == nil {
		for _, entry := range catalog.Meetings {
			var probe struct {
				AudioPath string `json:"audioPath"`
			}
			if json.Unmarshal(entry, &probe) != nil {
				continue
			}
			name := catalogEntryOpusName(probe.AudioPath, "")
			if name != "" {
				if err := metadata.Put(context.Background(), testMeetingFileID(name), name, entry); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	testCatalogRegistry.Store(serverURL, metadata)
	t.Cleanup(func() { testCatalogRegistry.Delete(serverURL); _ = metadata.Close() })
}

func serveTestShares(w http.ResponseWriter, r *http.Request, visible []string, failure int) bool {
	if !strings.Contains(r.URL.Path, "/files_sharing/api/v1/shares") {
		return false
	}
	if failure != 0 {
		w.WriteHeader(failure)
		return true
	}
	rows := make([]map[string]any, 0, len(visible))
	for _, name := range visible {
		rows = append(rows, map[string]any{
			"id": testMeetingFileID(name), "share_type": 0,
			"file_source": testMeetingFileID(name), "permissions": 1, "uid_file_owner": ncRecordingsOwner,
			"item_type": "file", "path": "/Cassini/Recordings/meetings/" + name,
		})
	}
	body, _ := json.Marshal(map[string]any{"ocs": map[string]any{"meta": map[string]any{"statuscode": 100}, "data": rows}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
	return true
}
