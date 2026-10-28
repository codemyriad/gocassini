package operator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
	"cassini-operator/internal/operator/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpUpstream stands in for Nextcloud answering share lists per user: visible
// maps a user to the .opus files shared with them.
func mcpUpstream(t *testing.T, visible map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := ""
		if raw, err := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API")); err == nil {
			user, _, _ = strings.Cut(string(raw), ":")
		}
		if serveTestShares(w, r, visible[user], 0) {
			return
		}
		t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	registerTestCatalog(t, srv.URL, meetingsListCatalog)
	return srv
}

// serveMCP mounts the operator's MCP endpoint behind a stand-in for the AppAPI
// middleware, which names the caller from X-Test-User.
func serveMCP(t *testing.T, cfg ExAppConfig, logs *bytes.Buffer) string {
	t.Helper()
	handler := newMCPHandler(cfg, searchDeps{}, nil, log.New(logs, "", 0))
	if handler == nil {
		t.Fatal("no MCP handler for an AppAPI deployment publishing to Nextcloud Files")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := r.Header.Get("X-Test-User"); user != "" {
			r = r.WithContext(appapi.WithUserID(r.Context(), user))
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type mcpTestUser struct{ user string }

func (u mcpTestUser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Test-User", u.user)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpCall(t *testing.T, endpoint, user, tool string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: mcpTestUser{user}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	if out != nil && !result.IsError {
		raw, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v", tool, err)
		}
	}
	return result
}

func mcpErrorText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func mcpListIDs(result mcpserver.ListResult) string {
	ids := make([]string, 0, len(result.Meetings))
	for _, meeting := range result.Meetings {
		ids = append(ids, meeting["id"].(string))
	}
	return strings.Join(ids, ",")
}

// Two accounts with different shares see different lists, each newest first,
// through the same access path as the published list route.
func TestMCPListIsTheCallersOwn(t *testing.T) {
	upstream := mcpUpstream(t, map[string][]string{
		"alice": {"JOB1.opus", "JOB2.opus", "JOB3.opus", "JOB4.opus"},
		"bob":   {"JOB2.opus"},
	})
	var logs bytes.Buffer
	endpoint := serveMCP(t, meetingsListConfig(upstream.URL), &logs)

	var alice, bob mcpserver.ListResult
	if result := mcpCall(t, endpoint, "alice", "list", nil, &alice); result.IsError {
		t.Fatalf("alice: %s", mcpErrorText(result))
	}
	if result := mcpCall(t, endpoint, "bob", "list", nil, &bob); result.IsError {
		t.Fatalf("bob: %s", mcpErrorText(result))
	}
	// Newest first; the undated meeting last.
	if got := mcpListIDs(alice); got != "c,b,a,d" {
		t.Fatalf("alice sees %s, want c,b,a,d", got)
	}
	if got := mcpListIDs(bob); got != "b" {
		t.Fatalf("bob sees %s, want b", got)
	}
	if alice.Source != ncFilesSourceValue {
		t.Fatalf("source = %q", alice.Source)
	}
	// Entries travel exactly as published, fields this code never reads included.
	if alice.Meetings[2]["speakerCount"] != float64(3) {
		t.Fatalf("entry a lost a field: %v", alice.Meetings[2])
	}
	for _, user := range []string{"alice", "bob"} {
		if !strings.Contains(logs.String(), "mcp: tool=list caller="+user+" ") {
			t.Fatalf("no audit line for %s:\n%s", user, logs.String())
		}
	}
}

// A filter narrows within the caller's set and says what it did, and a filter
// that can never match is refused as the caller's to fix.
func TestMCPListFilters(t *testing.T) {
	upstream := mcpUpstream(t, map[string][]string{"alice": {"JOB1.opus", "JOB2.opus", "JOB3.opus", "JOB4.opus"}})
	var logs bytes.Buffer
	endpoint := serveMCP(t, meetingsListConfig(upstream.URL), &logs)

	var got mcpserver.ListResult
	mcpCall(t, endpoint, "alice", "list", map[string]any{"from": "2026-08-01", "to": "2026-08-15"}, &got)
	if mcpListIDs(got) != "b,a" || got.ExcludedUndated != 1 || got.Excluded != 2 {
		t.Fatalf("got %s excluded=%d undated=%d", mcpListIDs(got), got.Excluded, got.ExcludedUndated)
	}
	if got.Filter == nil || got.Filter.To != "2026-08-15 23:59:59" {
		t.Fatalf("filter echo = %+v", got.Filter)
	}

	result := mcpCall(t, endpoint, "alice", "list", map[string]any{"from": "2026-09-01", "to": "2026-08-01"}, nil)
	if !result.IsError || !strings.Contains(mcpErrorText(result), "did you swap them?") {
		t.Fatalf("an inverted range was not refused: %s", mcpErrorText(result))
	}
}

// rooms is the CLI's grouping of the caller's own list.
func TestMCPRoomsGroupsTheCallersMeetings(t *testing.T) {
	upstream := mcpUpstream(t, map[string][]string{"alice": {"JOB1.opus", "JOB2.opus", "JOB3.opus", "JOB4.opus"}, "bob": {"JOB2.opus"}})
	var logs bytes.Buffer
	endpoint := serveMCP(t, meetingsListConfig(upstream.URL), &logs)

	var alice mcpserver.RoomsResult
	mcpCall(t, endpoint, "alice", "rooms", nil, &alice)
	if len(alice.Rooms) != 2 {
		t.Fatalf("rooms = %+v", alice.Rooms)
	}
	// rm_a holds a, c and the undated d; its latest is c.
	first := alice.Rooms[0]
	if first.Room != "rm_aaaaaaaaaaaaaaaa" || first.Meetings != 3 || first.Latest != "2026-08-31 23:30" || first.Earliest != "2026-08-01 09:00" {
		t.Fatalf("first room = %+v", first)
	}
	if alice.Rooms[1].Room != "rm_bbbbbbbbbbbbbbbb" || alice.Rooms[1].Meetings != 1 {
		t.Fatalf("second room = %+v", alice.Rooms[1])
	}

	var bob mcpserver.RoomsResult
	mcpCall(t, endpoint, "bob", "rooms", nil, &bob)
	if len(bob.Rooms) != 1 || bob.Rooms[0].Room != "rm_bbbbbbbbbbbbbbbb" {
		t.Fatalf("bob's rooms disclose a room they have no recording from: %+v", bob.Rooms)
	}
}

// Without the tag index, the tag tools say so rather than answering empty.
func TestMCPTagToolsWithoutIndexAreRefused(t *testing.T) {
	upstream := mcpUpstream(t, map[string][]string{"alice": {"JOB1.opus"}})
	var logs bytes.Buffer
	endpoint := serveMCP(t, meetingsListConfig(upstream.URL), &logs)

	for tool, args := range map[string]map[string]any{"tags": nil, "annotations": {"id": "a"}} {
		result := mcpCall(t, endpoint, "alice", tool, args, nil)
		if !result.IsError || !strings.Contains(mcpErrorText(result), "not an empty result") {
			t.Fatalf("%s: %s", tool, mcpErrorText(result))
		}
	}
	result := mcpCall(t, endpoint, "alice", "annotations", map[string]any{"id": "../etc"}, nil)
	if !result.IsError || !strings.Contains(mcpErrorText(result), "not a meeting id") {
		t.Fatalf("a path was accepted as a meeting id: %s", mcpErrorText(result))
	}
}

// Without a Nextcloud identity to read as, or a Nextcloud archive to read,
// there is no endpoint at all: refuse rather than guess (D-821 owns the
// standalone caller).
func TestMCPNotMountedWithoutAppAPI(t *testing.T) {
	standalone := ExAppConfig{PublishSink: publishSinkNextcloudFiles}
	if newMCPHandler(standalone, searchDeps{}, nil, nil) != nil {
		t.Fatal("a standalone operator mounted the MCP endpoint")
	}
	local := testExAppConfig("http://nextcloud.invalid")
	local.PublishSink = "local"
	if newMCPHandler(local, searchDeps{}, nil, nil) != nil {
		t.Fatal("an operator publishing locally mounted the MCP endpoint")
	}
}

// The rooms route is the same document the tool returns, so the CLI can move
// onto it.
func TestMeetingsRoomsRoute(t *testing.T) {
	srv := meetingsListUpstream{visible: []string{"JOB1.opus", "JOB2.opus"}}.server(t)
	defer srv.Close()
	cfg := meetingsListConfig(srv.URL)
	rec := httptest.NewRecorder()
	proxy := cfg.ncFilesProxy(nil, searchDeps{})
	if !isPublishedArchivePath(meetingsRoomsPath) || !proxy(rec, callerReq(http.MethodGet, "/published/"+meetingsRoomsPath, "alice"), meetingsRoomsPath) {
		t.Fatal("the rooms route is not served")
	}
	var got meetingsRoomsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(got.Rooms) != 2 || got.Rooms[0].Selector != "rm_bbbbbbbbbbbbbbbb" || got.Source != ncFilesSourceValue {
		t.Fatalf("rooms = %+v", got)
	}

	rec = httptest.NewRecorder()
	proxy(rec, callerReq(http.MethodGet, "/published/"+meetingsRoomsPath, ""), meetingsRoomsPath)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("no caller answered %d, want the loud 502", rec.Code)
	}
}
