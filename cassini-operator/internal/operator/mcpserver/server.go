// Package mcpserver is Cassini's MCP server (D-813): the CLI's meeting reads,
// offered as MCP tools to an agent outside Cassini, read as the person who
// connected it.
//
// It imports the MCP SDK and the standard library, and nothing from Cassini.
// What it reads through is the Reader interface; who is asking comes from a
// function the host supplies. That keeps the boundary the hub design asks for:
// moving the server into a separate app later is a move, not a rewrite.
//
// The server is stateless Streamable HTTP with plain JSON replies — every call
// is one POST answered by one JSON body, with no session and no event stream —
// because that is what passes through the AppAPI proxy unchanged.
//
// Every tool is read-only and says so: careful clients skip the per-call
// confirmation for tools carrying the read-only hint. Write tools will come
// with the hub's proposals, and will answer "proposed, confirm here" rather
// than a result, so nothing here assumes a call always completes in one step.
package mcpserver

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reader is the read service the tools sit on. caller is the Nextcloud user
// the request was authenticated as, never empty.
//
// An error a Reader returns is shown to the agent verbatim, so it must be safe
// to: say what the caller can do about it, never what went wrong inside. A
// meeting the caller may not read is reported exactly as one that does not
// exist.
type Reader interface {
	Rooms(ctx context.Context, caller string) (RoomsResult, error)
	List(ctx context.Context, caller string, in ListInput) (ListResult, error)
	Tags(ctx context.Context, caller string) (TagsResult, error)
	Annotations(ctx context.Context, caller string, in AnnotationsInput) (AnnotationsResult, error)
	Search(ctx context.Context, caller string, in SearchInput) (SearchResult, error)
	Context(ctx context.Context, caller string, in ContextInput) (ContextResult, error)
}

// CallerFunc returns the authenticated caller of the request ctx belongs to,
// or "" when the request carried no identity.
type CallerFunc func(ctx context.Context) string

// errNoIdentity is the refusal for a call that reached the server without an
// authenticated caller. Refused rather than answered empty: an agent that read
// "no meetings" here would act on a false negative it cannot detect.
var errNoIdentity = errors.New("your Nextcloud identity did not reach Cassini; this is not an empty result")

// Version is reported to clients as the server's version.
const Version = "1"

// NewHandler returns the MCP endpoint. Mount it behind the AppAPI middleware:
// caller reads the identity that middleware verified. Every call is logged
// with its caller to logger, which is the audit trail for now.
func NewHandler(reader Reader, caller CallerFunc, logger *log.Logger) http.Handler {
	server := NewServer(reader, caller, logger)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
}

// NewServer builds the server and registers the tools.
func NewServer(reader Reader, caller CallerFunc, logger *log.Logger) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "cassini", Title: "Cassini meetings", Version: Version}, &mcp.ServerOptions{
		Instructions: "Read the meetings this Nextcloud account may read. " +
			"Find meetings with list (or rooms, then list by room), find where something was said with search, " +
			"then read what was said with context; cite the minute.",
	})
	t := tools{reader: reader, caller: caller, logger: logger}
	addTool(server, t, "rooms",
		"Which conversations does this account have recordings from? Each room's id is what list takes as room.",
		func(ctx context.Context, caller string, _ struct{}) (RoomsResult, error) {
			return reader.Rooms(ctx, caller)
		})
	addTool(server, t, "list",
		"Which meetings may this account read, optionally only a room's, a date range's or a tag's? Newest first.",
		reader.List)
	addTool(server, t, "search",
		"Where was this said, across the meetings this account may read? References only (meeting, speaker, startMs), never the words; read them with context.",
		reader.Search)
	addTool(server, t, "context",
		"Give me those meetings as one document to read: summary and transcript, each passage with its speaker and startMs. fromMs/toMs read just a stretch.",
		reader.Context)
	addTool(server, t, "tags",
		"Which tags are on the meetings this account may read? A tag's label or id is what list takes as tag.",
		func(ctx context.Context, caller string, _ struct{}) (TagsResult, error) {
			return reader.Tags(ctx, caller)
		})
	addTool(server, t, "annotations",
		"What is marked in that meeting, and by whom?",
		reader.Annotations)
	return server
}

// tools is what every tool call shares.
type tools struct {
	reader Reader
	caller CallerFunc
	logger *log.Logger
}

// readOnly is the hint every tool carries.
var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true}

// addTool registers one read-only tool whose handler is call, run as the
// request's caller. A call with no caller is refused before call runs, and
// every call is logged with its caller, tool, duration and outcome — never
// its arguments, which can carry what someone searched for.
func addTool[In, Out any](server *mcp.Server, t tools, name, description string, call func(context.Context, string, In) (Out, error)) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description, Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
			var zero Out
			started := time.Now()
			caller := ""
			if t.caller != nil {
				caller = t.caller(ctx)
			}
			if caller == "" {
				t.logf("mcp: tool=%s caller=- refused: no identity", name)
				return nil, zero, errNoIdentity
			}
			out, err := call(ctx, caller, in)
			elapsed := time.Since(started).Milliseconds()
			if err != nil {
				t.logf("mcp: tool=%s caller=%s ms=%d error", name, caller, elapsed)
				return nil, zero, err
			}
			t.logf("mcp: tool=%s caller=%s ms=%d ok", name, caller, elapsed)
			return nil, out, nil
		})
}

func (t tools) logf(format string, args ...any) {
	if t.logger != nil {
		t.logger.Printf(format, args...)
	}
}
