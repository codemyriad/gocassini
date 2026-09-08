package operator

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end: undecided install, live ACL archive in the Team folder, leftovers
// in the private root. The admin picks access control. The copy dies. What can
// an ordinary account read before and after?
func TestClaimReadExposureAfterAFailedFirstSwitch(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)

	// A read server that records who asked for what.
	type req struct{ user, rel string }
	var seen []req
	reads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := relOf(r.URL.Path)
		seen = append(seen, req{davUserOf(r.URL.Path), rel})
		switch {
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"></d:multistatus>`)
		case strings.HasSuffix(rel, "catalog.json"):
			io.WriteString(w, catalogWith("priv-1"))
		default:
			io.WriteString(w, "PRIVATE-AUDIO")
		}
	}))
	defer reads.Close()
	proxy := aclProxyConfig(reads.URL).ncFilesProxy(log.New(io.Discard, "", 0))

	// Before: undecided.
	rec := httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", "alice"), "catalog.json")
	t.Logf("BEFORE catalog: requests=%+v body=%s", seen, strings.TrimSpace(rec.Body.String()))
	seen = nil

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addDir(ncRecordingsMount, ncACLRecordingsRoot, ncACLRecordingsRoot+"/meetings")
	mock.addFile(ncACLRecordingsRoot+"/meetings/acl-1.opus", "acl-audio")
	mock.addFile(ncACLRecordingsRoot+"/catalog.json", catalogWith("acl-1"))
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/priv-1.opus", "priv-audio")
	mock.addFile(ncDefaultRecordingsRoot+"/catalog.json", catalogWith("priv-1"))
	mock.failCopyOf = ncDefaultRecordingsRoot + "/meetings/priv-1.opus"

	cfg := testExAppConfig(mock.server(t).URL)
	_, err := cfg.switchStorageMode(context.Background(), true,
		storageMigrationPolicy{Strategy: storageStrategyMerge, OnConflict: storageConflictSkip}, log.New(io.Discard, "", 0))
	t.Logf("switch err = %v", err)

	// After: the failed switch's dirty mark is the record.
	rec = httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/catalog.json", "alice"), "catalog.json")
	t.Logf("AFTER catalog: requests=%+v body=%s", seen, strings.TrimSpace(rec.Body.String()))
	seen = nil
	rec = httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/meetings/priv-1.opus", "alice"), "meetings/priv-1.opus")
	t.Logf("AFTER audio: requests=%+v code=%d body=%s", seen, rec.Code, rec.Body.String())
}
