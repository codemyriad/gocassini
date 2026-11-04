package operator

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func postAccept(t *testing.T, rt *Runtime, caller, body string) (*httptest.ResponseRecorder, sharesAcceptResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := callerReq(http.MethodPost, "/shares/accept", caller)
	req.Body = http.NoBody
	if body != "" {
		req = httptest.NewRequest(http.MethodPost, "/shares/accept", strings.NewReader(body))
		if caller != "" {
			req = req.WithContext(callerReq(http.MethodPost, "/shares/accept", caller).Context())
		}
	}
	rt.sharesAcceptHandler(rec, req)
	var resp sharesAcceptResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode accept response: %v %s", err, rec.Body.String())
		}
	}
	return rec, resp
}

func waitingFake() *sharingFake {
	return &sharingFake{
		groups: []string{"staff"},
		pending: []map[string]any{
			pendingShare(34, ncShareTypeUser, "/CassiniRecordings/meetings/J1.opus"),
			pendingShare(35, ncShareTypeGroup, "/CassiniRecordings/meetings/J1.opus"),
			pendingShare(40, ncShareTypeUser, "/CassiniRecordings/meetings/J2.json"),
			// A colleague's document, pending too: never Cassini's to accept.
			{"id": "50", "share_type": 0, "permissions": 0, "uid_owner": "bob", "uid_file_owner": "bob", "item_type": "file", "path": "/Documents/plan.pdf"},
		},
	}
}

// The operator accepts AS the caller, and only what Nextcloud lists as
// waiting for them among Cassini's recordings. An id outside that list —
// somebody else's share, a guess — is reported, never forwarded.
func TestAcceptSharesAcceptsOnlyWhatWaitsForTheCaller(t *testing.T) {
	fake := waitingFake()
	rt := sharingRuntime(t, fake)
	rec, resp := postAccept(t, rt, "alice", `{"share_ids":["34","99","50"," 34 "]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /shares/accept = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if len(resp.Accepted) != 1 || resp.Accepted[0] != "34" {
		t.Fatalf("accepted = %v", resp.Accepted)
	}
	if len(resp.Failed) != 2 || resp.Failed[0].ShareID != "99" || resp.Failed[1].ShareID != "50" ||
		resp.Failed[0].Reason != shareAcceptNotWaiting || resp.Failed[1].Reason != shareAcceptNotWaiting {
		t.Fatalf("failed = %+v", resp.Failed)
	}
	fake.mu.Lock()
	accepted := append([]string(nil), fake.accepted...)
	fake.mu.Unlock()
	if len(accepted) != 1 || accepted[0] != "34" {
		t.Fatalf("Nextcloud saw accepts for %v", accepted)
	}
	if actor := fake.actor(http.MethodPost, "/ocs/v2.php/apps/files_sharing/api/v1/shares/pending/34"); actor != "alice" {
		t.Fatalf("accepted as %q, want the caller", actor)
	}
	// The refreshed view: 34 is gone, its group share and the other meeting
	// still wait.
	if !resp.Sharing.Checked || len(resp.Sharing.Pending) != 2 {
		t.Fatalf("refreshed sharing = %+v", resp.Sharing)
	}
	ids := map[string][]string{}
	for _, entry := range resp.Sharing.Pending {
		ids[entry.JobID] = entry.ShareIDs
	}
	if !equalStrings(ids["J1"], []string{"35"}) || !equalStrings(ids["J2"], []string{"40"}) {
		t.Fatalf("still waiting = %v", ids)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// "Accept all" is every recording share waiting for the caller, and nothing
// else that happens to be pending for them.
func TestAcceptAllAcceptsEveryWaitingRecording(t *testing.T) {
	fake := waitingFake()
	rt := sharingRuntime(t, fake)
	rec, resp := postAccept(t, rt, "alice", `{"all":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /shares/accept = %d %s", rec.Code, rec.Body.String())
	}
	sort.Strings(resp.Accepted)
	if !equalStrings(resp.Accepted, []string{"34", "35", "40"}) || len(resp.Failed) != 0 {
		t.Fatalf("accepted = %v, failed = %+v", resp.Accepted, resp.Failed)
	}
	if !resp.Sharing.Checked || len(resp.Sharing.Pending) != 0 {
		t.Fatalf("refreshed sharing = %+v", resp.Sharing)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, id := range fake.accepted {
		if id == "50" {
			t.Fatal("accepted a share that is not a recording")
		}
	}
}

// Nextcloud's own refusal is reported per share, in its words; the others
// are still accepted.
func TestAcceptSharesReportsNextcloudsRefusalPerShare(t *testing.T) {
	fake := waitingFake()
	fake.acceptFail = map[string]string{"35": "Wrong share ID, share does not exist"}
	rt := sharingRuntime(t, fake)
	rec, resp := postAccept(t, rt, "alice", `{"share_ids":["34","35"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /shares/accept = %d %s", rec.Code, rec.Body.String())
	}
	if !equalStrings(resp.Accepted, []string{"34"}) || len(resp.Failed) != 1 ||
		resp.Failed[0].ShareID != "35" || resp.Failed[0].Reason != "Wrong share ID, share does not exist" {
		t.Fatalf("accepted = %v, failed = %+v", resp.Accepted, resp.Failed)
	}
}

func TestAcceptSharesRejectsWhatItCannotAct(t *testing.T) {
	fake := waitingFake()
	rt := sharingRuntime(t, fake)
	// Only POST.
	rec := httptest.NewRecorder()
	rt.sharesAcceptHandler(rec, callerReq(http.MethodGet, "/shares/accept", "alice"))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", rec.Code)
	}
	for name, body := range map[string]string{
		"not JSON":       `share_ids=34`,
		"nothing to do":  `{}`,
		"empty list":     `{"share_ids":[]}`,
		"over the limit": `{"share_ids":[` + strings.Repeat(`"1",`, maxShareAcceptIDs) + `"1"]}`,
	} {
		if rec, _ := postAccept(t, rt, "alice", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// No caller: the middleware did not run, which is an outage, not an
	// answer — and nothing was accepted as anyone.
	if rec, _ := postAccept(t, rt, "", `{"share_ids":["34"]}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("no caller = %d %s", rec.Code, rec.Body.String())
	}
	// Nextcloud cannot list what waits: nothing is accepted blind.
	fake.mu.Lock()
	fake.refuse = map[string]int{"/shares/pending": http.StatusForbidden}
	fake.mu.Unlock()
	if rec, _ := postAccept(t, rt, "alice", `{"share_ids":["34"]}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("pending list refused = %d %s", rec.Code, rec.Body.String())
	}
	fake.mu.Lock()
	if len(fake.accepted) != 0 {
		t.Fatalf("accepted %v without a caller or a pending list", fake.accepted)
	}
	fake.mu.Unlock()
	// A deployment that keeps recordings on this machine shares nothing.
	rt.publishSink = &localPublishSink{siteRoot: t.TempDir(), logger: rt.logger}
	if rec, _ := postAccept(t, rt, "alice", `{"share_ids":["34"]}`); rec.Code != http.StatusConflict {
		t.Fatalf("local sink = %d %s", rec.Code, rec.Body.String())
	}
}

// Through Nextcloud the operator is reached only on routes info.xml declares,
// so a route missing there is a button that answers 404 in production. This
// one is USER: every signed-in person accepts their own shares.
func TestAcceptSharesRouteIsDeclaredForUsers(t *testing.T) {
	raw, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	routes := regexp.MustCompile(`(?s)<route>\s*<url>([^<]+)</url>\s*<verb>([^<]+)</verb>\s*<access_level>([^<]+)</access_level>`).FindAllStringSubmatch(string(raw), -1)
	for _, route := range routes {
		pattern, err := regexp.Compile(route[1])
		if err != nil {
			continue
		}
		if pattern.MatchString("operator/shares/accept") && strings.Contains(route[2], "POST") {
			if route[3] != "USER" {
				t.Fatalf("shares/accept is declared with access %q, want USER", route[3])
			}
			return
		}
	}
	t.Fatal("no info.xml route lets POST operator/shares/accept through AppAPI")
}

func TestAcceptSharesIsMountedWhereverTheOperatorAPIIs(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	for _, tc := range []struct {
		basePath string
		url      string
		mounted  bool
	}{
		{basePath: "/operator", url: "/operator/shares/accept", mounted: true},
		{basePath: "/operator", url: "/shares/accept", mounted: false},
		{basePath: "/", url: "/shares/accept", mounted: true},
	} {
		rt.cfg.BasePath = tc.basePath
		handler := newHTTPHandler(log.New(ioDiscard{}, "", 0), rt, ExAppConfig{})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.url, strings.NewReader(`{"all":true}`)))
		// Without the AppAPI middleware there is no caller, so a mounted
		// route answers 502; an unmounted one 404.
		if mounted := rec.Code != http.StatusNotFound; mounted != tc.mounted {
			t.Fatalf("POST %s with base %q = %d, want mounted=%v", tc.url, tc.basePath, rec.Code, tc.mounted)
		}
	}
}
