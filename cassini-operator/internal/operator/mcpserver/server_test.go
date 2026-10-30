package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type callerKey struct{}

// fakeReader answers every tool from fixed values, keyed by caller where the
// answer depends on who is asking.
type fakeReader struct {
	lists map[string][]string // caller -> meeting ids
	err   error
}

func (f fakeReader) Rooms(context.Context, string) (RoomsResult, error) {
	return RoomsResult{Version: "v", Source: "nextcloud-files", Rooms: []Room{{Room: "rm_a", Meetings: 1}}}, f.err
}

func (f fakeReader) List(_ context.Context, caller string, in ListInput) (ListResult, error) {
	if f.err != nil {
		return ListResult{}, f.err
	}
	result := ListResult{Version: "v", Source: "nextcloud-files", Meetings: []map[string]any{}}
	for _, id := range f.lists[caller] {
		result.Meetings = append(result.Meetings, map[string]any{"id": id})
	}
	if in.Room != "" {
		result.Filter = &ListFilter{Room: in.Room}
	}
	return result, nil
}

func (f fakeReader) Tags(context.Context, string) (TagsResult, error) {
	return TagsResult{Tags: []Tag{}, Meetings: []MeetingTags{}}, f.err
}

func (f fakeReader) Search(_ context.Context, _ string, in SearchInput) (SearchResult, error) {
	return SearchResult{Hits: []SearchHit{{MeetingID: "m1", SegmentID: "s1", StartMS: 60_000, EndMS: 65_000, Matched: "exact"}}}, f.err
}

func (f fakeReader) Context(_ context.Context, _ string, in ContextInput) (ContextResult, error) {
	return ContextResult{Version: "cassini.meetings.context.v1", Meetings: []MeetingContext{}}, f.err
}

func (f fakeReader) Annotations(_ context.Context, _ string, in AnnotationsInput) (AnnotationsResult, error) {
	return AnnotationsResult{MeetingID: in.ID}, f.err
}

// serve mounts the handler behind a stand-in for the AppAPI middleware: the
// X-Test-User header becomes the authenticated caller.
func serve(t *testing.T, reader Reader, logs *bytes.Buffer) string {
	t.Helper()
	handler := NewHandler(reader, func(ctx context.Context) string {
		caller, _ := ctx.Value(callerKey{}).(string)
		return caller
	}, log.New(logs, "", 0))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user := r.Header.Get("X-Test-User"); user != "" {
			r = r.WithContext(context.WithValue(r.Context(), callerKey{}, user))
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type asUser struct{ user string }

func (a asUser) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if a.user != "" {
		r.Header.Set("X-Test-User", a.user)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, endpoint, user string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: asUser{user}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// The catalogue is the CLI's read verbs, every one marked read-only, each
// described in a sentence or two: descriptions ride along on every request an
// agent makes, so a long catalogue costs money and crowds out the meeting.
func TestToolsAreTheReadVerbsAllReadOnly(t *testing.T) {
	var logs bytes.Buffer
	session := connect(t, serve(t, fakeReader{}, &logs), "alice")
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %s is not marked read-only", tool.Name)
		}
		if len(tool.Description) > 160 {
			t.Errorf("tool %s description is %d characters; keep it short", tool.Name, len(tool.Description))
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %s advertises no output schema", tool.Name)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "annotations,context,list,rooms,search,tags" {
		t.Fatalf("tools = %s", got)
	}
}

// The identity the tools act as is the caller's, so two people asking the same
// question get their own answers — and every call names its caller in the log.
func TestToolsActAsTheCaller(t *testing.T) {
	var logs bytes.Buffer
	reader := fakeReader{lists: map[string][]string{"alice": {"a1", "a2"}, "bob": {"b1"}}}
	endpoint := serve(t, reader, &logs)

	for user, want := range map[string]string{"alice": "a1,a2", "bob": "b1"} {
		result := call(t, connect(t, endpoint, user), "list", nil)
		if result.IsError {
			t.Fatalf("%s: list failed: %s", user, resultText(result))
		}
		var got ListResult
		raw, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, meeting := range got.Meetings {
			ids = append(ids, meeting["id"].(string))
		}
		if strings.Join(ids, ",") != want {
			t.Fatalf("%s sees %v, want %s", user, ids, want)
		}
		if !strings.Contains(logs.String(), "mcp: tool=list caller="+user+" ") {
			t.Fatalf("no audit line for %s: %s", user, logs.String())
		}
	}
}

// A call that arrives without an identity is refused, never answered empty.
func TestToolsRefuseWithoutIdentity(t *testing.T) {
	var logs bytes.Buffer
	reader := fakeReader{lists: map[string][]string{"": {"leak"}}}
	result := call(t, connect(t, serve(t, reader, &logs), ""), "list", nil)
	if !result.IsError {
		t.Fatalf("an anonymous call was answered: %s", resultText(result))
	}
	if !strings.Contains(resultText(result), "identity did not reach Cassini") {
		t.Fatalf("refusal = %q", resultText(result))
	}
	if strings.Contains(resultText(result), "leak") {
		t.Fatal("the refusal carried data")
	}
}

// The reader's refusal is what the agent reads, as a tool error it can act on
// rather than a protocol failure.
func TestReaderRefusalIsAToolError(t *testing.T) {
	var logs bytes.Buffer
	reader := fakeReader{err: errors.New("from 2026-09-01 is after to 2026-08-01; did you swap them?")}
	result := call(t, connect(t, serve(t, reader, &logs), "alice"), "list", map[string]any{"from": "2026-09-01", "to": "2026-08-01"})
	if !result.IsError || !strings.Contains(resultText(result), "did you swap them?") {
		t.Fatalf("result = %+v %q", result.IsError, resultText(result))
	}
	if strings.Contains(logs.String(), "2026-09-01") {
		t.Fatalf("arguments reached the log: %s", logs.String())
	}
}

// Stateless with JSON replies: one POST, one JSON body. There is no event
// stream to open and no session to end.
func TestServerIsStatelessJSON(t *testing.T) {
	var logs bytes.Buffer
	endpoint := serve(t, fakeReader{}, &logs)

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(method, endpoint, nil)
		req.Header.Set("X-Test-User", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s answered %d, want 405", method, resp.StatusCode)
		}
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	req.Header.Set("X-Test-User", "alice")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content type = %q, want application/json", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("Mcp-Session-Id") != "" {
		t.Fatal("a stateless server issued a session")
	}
}
