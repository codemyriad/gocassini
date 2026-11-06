package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordingSharesUseCallerAndOCSEnvelope(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("OCS-APIRequest") != "true" || r.Header.Get("EX-APP-ID") != "cassini" {
			t.Errorf("missing AppAPI share headers: %+v", r.Header)
		}
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("AUTHORIZATION-APP-API"))
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("shared_with_me") == "true":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":[{"id":"9","share_type":0,"uid_file_owner":"cassini","file_source":42,"permissions":1,"path":"/Shares/a.opus"}]}}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":[]}}`)
		case r.Method == http.MethodPost:
			if got := r.FormValue("permissions"); got != "17" {
				t.Errorf("permissions = %q", got)
			}
			if got := r.FormValue("path"); got != "/CassiniRecordings/meetings/a.opus" {
				t.Errorf("path = %q", got)
			}
			if got := r.FormValue("note"); got != "Recording of a. Open it in Cassini." {
				t.Errorf("note = %q", got)
			}
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":{"id":"10","share_type":0,"permissions":17}}}`)
		default:
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	got, err := cfg.receivedShares(context.Background(), server.Client(), "alice")
	if err != nil || len(got) != 1 || got[0].FileSource != 42 {
		t.Fatalf("received shares = %+v, %v", got, err)
	}
	if p, err := got[0].recipientPath(); err != nil || p != "Shares/a.opus" {
		t.Fatalf("recipient path = %q, %v", p, err)
	}
	if _, err := cfg.ownerSharesForPath(context.Background(), server.Client(), "CassiniRecordings/meetings/a.opus"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.createRecordingShare(context.Background(), server.Client(), "CassiniRecordings/meetings/a.opus", sharePrincipal{Type: "user", ID: "alice"}, ncShareRead|ncShareReshare, "Recording of a. Open it in Cassini."); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.Contains(calls[0], "shared_with_me=true") || !strings.Contains(calls[1], "path=") {
		t.Fatalf("calls = %v", calls)
	}
	if calls[0] == calls[1] || calls[1] != calls[2] {
		// The owner list and create have different methods but the same actor.
		actor := func(call string) string { return call[strings.LastIndexByte(call, ' ')+1:] }
		if actor(calls[0]) == actor(calls[1]) || actor(calls[1]) != actor(calls[2]) {
			t.Fatalf("incorrect act-as identities: %v", calls)
		}
	}
}

func TestRecordingShareRefusalsFailClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"sharing disabled"},"data":[]}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	if _, err := cfg.receivedShares(context.Background(), server.Client(), "alice"); err == nil || !strings.Contains(err.Error(), "sharing disabled") {
		t.Fatalf("refusal = %v", err)
	}
	for _, bad := range []string{"", "/a/../b.opus", "/a//b.opus", "/a\\b.opus"} {
		if _, err := (ncShare{ID: 1, Path: bad}).recipientPath(); err == nil {
			t.Errorf("accepted unsafe recipient path %q", bad)
		}
	}
}

func TestPublicShareFallsBackToReadWhenInstanceRefusesResharing(t *testing.T) {
	permissions := 0
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			if r.FormValue("permissions") == "17" {
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"resharing disabled"},"data":[]}}`)
				return
			}
			permissions = ncShareRead
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"id":"7","share_type":0,"share_with":"alice","permissions":1}}}`)
			return
		}
		list := `[]`
		if permissions != 0 {
			list = `[{"id":"7","share_type":0,"share_with":"alice","permissions":1}]`
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":`+list+`}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	if _, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", []sharePrincipal{{Type: "user", ID: "alice"}}, true, sharePublish, ""); err != nil {
		t.Fatal(err)
	}
	if permissions != ncShareRead || posts != 2 {
		t.Fatalf("permissions=%d posts=%d, want read-only fallback", permissions, posts)
	}
}

func TestRecordingSharesKeepValidRecipientsWhenOneIsRefused(t *testing.T) {
	shares := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			principal := r.FormValue("shareWith")
			if principal == "bob" {
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":404,"message":"user not found"},"data":[]}}`)
				return
			}
			shares[principal] = true
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":{"id":1}}}`)
			return
		}
		items := []map[string]any{}
		for principal := range shares {
			items = append(items, map[string]any{"id": 1, "share_type": 0, "share_with": principal, "permissions": 1})
		}
		encoded, _ := json.Marshal(items)
		_, _ = fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":%s}}`, encoded)
	}))
	defer server.Close()
	defer ncAccessSubstrate.reset()
	ncAccessSubstrate.reset()
	journal := newSupportJournal(t.TempDir())
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", supportJournal: journal}
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}}
	skipped, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false, sharePublish, "")
	if err != nil {
		t.Fatal(err)
	}
	if !shares["alice"] || !shares["carol"] || shares["bob"] {
		t.Fatalf("shares = %v", shares)
	}
	if want := []skippedShare{{Type: "user", ID: "bob", Reason: "user not found"}}; !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %+v, want %+v", skipped, want)
	}
	if warning := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles).Warning; !strings.Contains(warning, "bob") {
		t.Fatalf("warning = %q, want skipped recipient", warning)
	}
	// Unlike the in-memory warning, this evidence remains after a restart.
	events, failed := newSupportJournal(journal.workRoot).read(time.Time{})
	if failed {
		t.Fatal("support journal unavailable")
	}
	journaled := false
	for _, event := range events {
		if event.Event == "share_skipped" && event.OCS == 404 {
			journaled = true
		}
	}
	if !journaled {
		t.Fatal("successful publication lost skipped-recipient evidence")
	}
}

func TestRecordingShareRequiresValidStarter(t *testing.T) {
	cfg := ExAppConfig{}
	for _, audience := range [][]sharePrincipal{nil, {{Type: "user", ID: ncRecordingsOwner}}, {{Type: "group", ID: "staff"}}} {
		if _, err := cfg.reconcileRecordingShares(context.Background(), nil, "recording.opus", audience, false, sharePublish, ""); err == nil {
			t.Fatalf("accepted audience %v", audience)
		}
	}
}

// refusingShareServer refuses every share to the principals named in refuse,
// with Nextcloud's group-members message, and lists the rest as shared.
func refusingShareServer(t *testing.T, refuse ...string) (*httptest.Server, map[string]bool) {
	t.Helper()
	var mu sync.Mutex
	shared := map[string]bool{}
	refused := map[string]bool{}
	for _, id := range refuse {
		refused[id] = true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			who := r.FormValue("shareWith")
			if refused[who] {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Sharing is only allowed with group members"},"data":[]}}`)
				return
			}
			shared[r.FormValue("shareType")+"/"+who] = true
			_, _ = fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":{"id":%d}}}`, len(shared))
			return
		}
		items := []map[string]any{}
		for key := range shared {
			kind, who, _ := strings.Cut(key, "/")
			shareType, _ := strconv.Atoi(kind)
			items = append(items, map[string]any{"id": len(items) + 1, "share_type": shareType, "share_with": who, "permissions": 1})
		}
		encoded, _ := json.Marshal(items)
		_, _ = fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":%s}}`, encoded)
	}))
	t.Cleanup(server.Close)
	return server, shared
}

// A refused group or Team no longer fails publication (D-885): the meeting
// reaches everyone Nextcloud allows, and the group is named as skipped.
func TestRecordingShareSkipsARefusedGroupAudience(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	server, shared := refusingShareServer(t, "staff", "design-team")
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "group", ID: "staff"}, {Type: "circle", ID: "design-team"}, {Type: "user", ID: "carol"}}
	skipped, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false, sharePublish, "")
	if err != nil {
		t.Fatalf("a refused group failed publication: %v", err)
	}
	if !shared["0/alice"] || !shared["0/carol"] {
		t.Fatalf("shares = %v", shared)
	}
	want := []skippedShare{
		{Type: "group", ID: "staff", Reason: "Sharing is only allowed with group members"},
		{Type: "circle", ID: "design-team", Reason: "Sharing is only allowed with group members"},
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %+v, want %+v", skipped, want)
	}
}

// The starter is the one recipient publication cannot do without: a meeting
// its own starter cannot open is not published, even when everyone else could
// be given it.
func TestRecordingShareFailsWhenTheStarterIsRefused(t *testing.T) {
	server, shared := refusingShareServer(t, "alice")
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}}
	skipped, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false, sharePublish, "")
	var refused *starterShareRefusedError
	if !errors.As(err, &refused) || refused.Starter != "alice" || !strings.Contains(err.Error(), "starter") {
		t.Fatalf("starter refusal = %v", err)
	}
	if len(shared) != 0 {
		t.Fatalf("shared with others before the starter: %v", shared)
	}
	if want := []skippedShare{{Type: "user", ID: "alice", Reason: "Sharing is only allowed with group members"}}; !reflect.DeepEqual(skipped, want) {
		t.Fatalf("skipped = %+v, want %+v", skipped, want)
	}

	// Re-applying an audience to a meeting that is already published has
	// nothing to refuse: the starter is reported like anyone else.
	skipped, err = cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false, shareReapply, "")
	if err != nil {
		t.Fatalf("re-share failed on the starter: %v", err)
	}
	if len(skipped) != 1 || skipped[0].ID != "alice" || !shared["0/bob"] || !shared["0/carol"] {
		t.Fatalf("re-share skipped=%+v shared=%v", skipped, shared)
	}
}

// Anything that is not Nextcloud refusing a recipient says nothing about who
// may receive the recording, and still fails.
func TestRecordingShareStillFailsOnAnOutage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.FormValue("shareWith") == "staff" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":996,"message":"server error"},"data":[]}}`)
			return
		}
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":{"id":1}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":[{"id":1,"share_type":0,"share_with":"alice","permissions":1}]}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "group", ID: "staff"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cfg.reconcileRecordingShares(ctx, server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false, sharePublish, ""); err == nil || !strings.Contains(err.Error(), "staff") {
		t.Fatalf("an outage was treated as a refusal: %v", err)
	}
}

// Imports keep their rule from before D-885 until the Import tab can show
// what was skipped: a refused group or Team fails the import rather than
// vanishing; a refused individual is still skipped and reported.
func TestImportStillFailsOnARefusedGroup(t *testing.T) {
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	server, _ := refusingShareServer(t, "staff", "bob")
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []sharePrincipal{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}, {Type: "group", ID: "staff"}}
	if _, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, true, shareImport, ""); err == nil || !strings.Contains(err.Error(), "staff") {
		t.Fatalf("a refused group did not fail the import: %v", err)
	}
	skipped, err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience[:2], true, shareImport, "")
	if err != nil || len(skipped) != 1 || skipped[0].ID != "bob" {
		t.Fatalf("individual refusal on import: skipped=%+v err=%v", skipped, err)
	}
}
