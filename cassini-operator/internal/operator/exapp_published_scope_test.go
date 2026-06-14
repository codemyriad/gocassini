package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

// fakeJobOwners maps job id -> owner. A missing key means "not a known job"
// (found=false), which the handler treats as a site-shell path.
type fakeJobOwners map[string]string

func (f fakeJobOwners) JobOwner(_ context.Context, id string) (string, bool, error) {
	owner, ok := f[id]
	return owner, ok, nil
}

func writePublishedSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[` +
		`{"id":"jobA","title":"Alice meeting","audioPath":"jobA/audio.webm"},` +
		`{"id":"jobB","title":"Bob meeting","audioPath":"jobB/audio.webm"}]}`
	writeSiteFile(t, filepath.Join(dir, "catalog.json"), catalog)
	writeSiteFile(t, filepath.Join(dir, "index.html"), "<html>site</html>")
	writeSiteFile(t, filepath.Join(dir, "assets", "app.js"), "console.log(1)")
	writeSiteFile(t, filepath.Join(dir, "jobA", "audio.webm"), "AAAA")
	writeSiteFile(t, filepath.Join(dir, "jobB", "audio.webm"), "BBBB")
	return dir
}

func writeSiteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func getPublished(t *testing.T, h http.Handler, target, caller string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if caller != "" {
		req = req.WithContext(appapi.ContextWithUserID(req.Context(), caller))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func catalogMeetingIDs(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		Meetings []struct {
			ID string `json:"id"`
		} `json:"meetings"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode catalog: %v (body=%s)", err, body)
	}
	ids := make([]string, 0, len(doc.Meetings))
	for _, m := range doc.Meetings {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestPublishedHandlerScopesCatalogToOwner(t *testing.T) {
	dir := writePublishedSite(t)
	owners := fakeJobOwners{"jobA": "alice", "jobB": "bob"}
	h := publishedHandler(dir, "/published", nil, owners)

	rec := getPublished(t, h, "/published/catalog.json", "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if got := catalogMeetingIDs(t, rec.Body.Bytes()); len(got) != 1 || got[0] != "jobA" {
		t.Fatalf("alice catalog ids=%v, want [jobA]", got)
	}
	// Bob sees only his.
	rec = getPublished(t, h, "/published/catalog.json", "bob")
	if got := catalogMeetingIDs(t, rec.Body.Bytes()); len(got) != 1 || got[0] != "jobB" {
		t.Fatalf("bob catalog ids=%v, want [jobB]", got)
	}
	// A user who owns nothing sees an empty archive.
	rec = getPublished(t, h, "/published/catalog.json", "carol")
	if got := catalogMeetingIDs(t, rec.Body.Bytes()); len(got) != 0 {
		t.Fatalf("carol catalog ids=%v, want []", got)
	}
}

func TestPublishedHandlerGatesPerMeetingAssets(t *testing.T) {
	dir := writePublishedSite(t)
	owners := fakeJobOwners{"jobA": "alice", "jobB": "bob"}
	h := publishedHandler(dir, "/published", nil, owners)

	if rec := getPublished(t, h, "/published/jobA/audio.webm", "alice"); rec.Code != http.StatusOK {
		t.Fatalf("own asset code=%d want 200", rec.Code)
	}
	if rec := getPublished(t, h, "/published/jobB/audio.webm", "alice"); rec.Code != http.StatusNotFound {
		t.Fatalf("other-owner asset code=%d want 404", rec.Code)
	}
}

func TestPublishedHandlerServesSiteShellUnscoped(t *testing.T) {
	dir := writePublishedSite(t)
	owners := fakeJobOwners{"jobA": "alice", "jobB": "bob"}
	h := publishedHandler(dir, "/published", nil, owners)

	// Site root serves index.html; non-meeting assets are not gated.
	for _, p := range []string{"/published/", "/published/assets/app.js"} {
		if rec := getPublished(t, h, p, "carol"); rec.Code != http.StatusOK {
			t.Fatalf("shell %s code=%d want 200", p, rec.Code)
		}
	}
}

func TestPublishedHandlerUnscopedWithoutCallerIdentity(t *testing.T) {
	dir := writePublishedSite(t)
	owners := fakeJobOwners{"jobA": "alice", "jobB": "bob"}
	h := publishedHandler(dir, "/published", nil, owners)

	// No AppAPI identity (standalone/dev): the archive is served unscoped.
	rec := getPublished(t, h, "/published/catalog.json", "")
	if got := catalogMeetingIDs(t, rec.Body.Bytes()); len(got) != 2 {
		t.Fatalf("unscoped catalog ids=%v, want both", got)
	}
	if rec := getPublished(t, h, "/published/jobB/audio.webm", ""); rec.Code != http.StatusOK {
		t.Fatalf("unscoped asset code=%d want 200", rec.Code)
	}
}

func TestPublishedHandlerNilLookupServesUnscoped(t *testing.T) {
	dir := writePublishedSite(t)
	h := publishedHandler(dir, "/published", nil, nil)

	rec := getPublished(t, h, "/published/catalog.json", "alice")
	if got := catalogMeetingIDs(t, rec.Body.Bytes()); len(got) != 2 {
		t.Fatalf("nil-lookup catalog ids=%v, want both", got)
	}
}

func TestStoreJobOwnerFromTalkBinding(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := nowUTCString()
	insert := func(id string) {
		if _, err := store.db.ExecContext(ctx, `
INSERT INTO jobs (id, provider, request_json, stage, state, current_attempt_number, rerun_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "talk", "{}", "record", "queued", 1, 0, now, now); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("owned")
	insert("unowned")
	if err := store.SetJobTalkBinding(ctx, "owned",
		`{"backend_url":"https://nc.example","room_token":"r1","owner":"alice"}`); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}

	if owner, found, err := store.JobOwner(ctx, "owned"); err != nil || !found || owner != "alice" {
		t.Fatalf("JobOwner(owned) = %q, %v, %v; want alice, true, nil", owner, found, err)
	}
	if owner, found, err := store.JobOwner(ctx, "unowned"); err != nil || !found || owner != "" {
		t.Fatalf("JobOwner(unowned) = %q, %v, %v; want \"\", true, nil", owner, found, err)
	}
	if owner, found, err := store.JobOwner(ctx, "ghost"); err != nil || found || owner != "" {
		t.Fatalf("JobOwner(ghost) = %q, %v, %v; want \"\", false, nil", owner, found, err)
	}
}
