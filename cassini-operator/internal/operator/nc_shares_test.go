package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if _, err := cfg.createRecordingShare(context.Background(), server.Client(), "CassiniRecordings/meetings/a.opus", aclMapping{Type: "user", ID: "alice"}, ncShareRead|ncShareReshare); err != nil {
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
	if err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", []aclMapping{{Type: "user", ID: "alice"}}, true); err != nil {
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
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []aclMapping{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}, {Type: "user", ID: "carol"}}
	if err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false); err != nil {
		t.Fatal(err)
	}
	if !shares["alice"] || !shares["carol"] || shares["bob"] {
		t.Fatalf("shares = %v", shares)
	}
	if warning := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles).Warning; !strings.Contains(warning, "bob") {
		t.Fatalf("warning = %q, want skipped recipient", warning)
	}
}

func TestRecordingShareRequiresValidStarter(t *testing.T) {
	cfg := ExAppConfig{}
	for _, audience := range [][]aclMapping{nil, {{Type: "user", ID: ncRecordingsOwner}}, {{Type: "group", ID: "staff"}}} {
		if err := cfg.reconcileRecordingShares(context.Background(), nil, "recording.opus", audience, false); err == nil {
			t.Fatalf("accepted audience %v", audience)
		}
	}
}

func TestRecordingShareFailsWhenGroupAudienceIsRefused(t *testing.T) {
	starterShared := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.FormValue("shareWith") == "staff" {
				_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":404,"message":"group unavailable"},"data":[]}}`)
				return
			}
			starterShared = true
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":{"id":1}}}`)
			return
		}
		list := `[]`
		if starterShared {
			list = `[{"id":1,"share_type":0,"share_with":"alice","permissions":1}]`
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":100},"data":`+list+`}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret"}
	audience := []aclMapping{{Type: "user", ID: "alice"}, {Type: "group", ID: "staff"}}
	if err := cfg.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/a.opus", audience, false); err == nil || !strings.Contains(err.Error(), "staff") {
		t.Fatalf("group refusal must fail publication: %v", err)
	}
}
