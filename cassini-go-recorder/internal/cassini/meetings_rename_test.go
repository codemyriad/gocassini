package cassini

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const renameCommitted = `{"meetingId": "MEET-1", "revision": 6, "operationId": "op_r",
 "added": [], "removed": [], "notFound": [], "resolved": true}`

func TestMeetingsRenameSendsOneSetTitleOp(t *testing.T) {
	var sent []byte
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"POST " + meetingsTestAnnotationsPath + "MEET-1": func(w http.ResponseWriter, r *http.Request) {
			sent, _ = io.ReadAll(r.Body)
			answerJSON(http.StatusOK, renameCommitted)(w, r)
		},
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rename", "MEET-1", "  Q4 hiring sync  ")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var body struct {
		Ops       []map[string]string `json:"ops"`
		ActorKind string              `json:"actorKind"`
	}
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("the request body is not JSON: %v (%s)", err, sent)
	}
	if len(body.Ops) != 1 || body.Ops[0]["op"] != "set-title" || body.Ops[0]["title"] != "Q4 hiring sync" {
		t.Errorf("ops = %v, want one set-title with the trimmed title", body.Ops)
	}
	if body.ActorKind != "agent" {
		t.Errorf("actorKind = %q, want the agent default", body.ActorKind)
	}
	if want := "renamed=MEET-1 revision=6 caller=alice title=Q4 hiring sync\n"; !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout)
	}
}

func TestMeetingsRenameAcceptsAFlagBetweenIdAndTitle(t *testing.T) {
	// `rename MEET-1 --json "Q4 sync"` once swapped the positionals: flag
	// parsing stopped at the title and handed back [title, id].
	var sent []byte
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"POST " + meetingsTestAnnotationsPath + "MEET-1": func(w http.ResponseWriter, r *http.Request) {
			sent, _ = io.ReadAll(r.Body)
			answerJSON(http.StatusOK, renameCommitted)(w, r)
		},
	}))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "rename", "MEET-1", "--json", "Q4 sync")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var body struct {
		Ops []map[string]string `json:"ops"`
	}
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("the request body is not JSON: %v (%s)", err, sent)
	}
	if len(body.Ops) != 1 || body.Ops[0]["title"] != "Q4 sync" {
		t.Errorf("ops = %v, want one set-title with %q (positionals swapped?)", body.Ops, "Q4 sync")
	}
}

func TestMeetingsRenameClearSendsAnEmptyTitle(t *testing.T) {
	var sent []byte
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"POST " + meetingsTestAnnotationsPath + "MEET-1": func(w http.ResponseWriter, r *http.Request) {
			sent, _ = io.ReadAll(r.Body)
			answerJSON(http.StatusOK, renameCommitted)(w, r)
		},
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rename", "MEET-1", "--clear")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var body struct {
		Ops []map[string]string `json:"ops"`
	}
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("the request body is not JSON: %v (%s)", err, sent)
	}
	if len(body.Ops) != 1 || body.Ops[0]["op"] != "set-title" || body.Ops[0]["title"] != "" {
		t.Errorf("ops = %v, want one set-title with an empty title", body.Ops)
	}
	if !strings.Contains(stdout, "cleared=MEET-1 revision=6 caller=alice\n") {
		t.Errorf("stdout lacks the cleared line:\n%s", stdout)
	}
}

func TestMeetingsRenameRefusesBadArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		code int
	}{
		{"no title", []string{"meetings", "rename", "MEET-1"}, "rename takes a meeting id and a title", 2},
		{"blank title", []string{"meetings", "rename", "MEET-1", "   "}, "the title is empty", 2},
		{"clear plus title", []string{"meetings", "rename", "--clear", "MEET-1", "New name"}, "rename takes a meeting id and a title", 2},
		{"overlong title", []string{"meetings", "rename", "MEET-1", strings.Repeat("x", 201)}, "must be 1-200 characters", annotateExitInvalid},
		{"dot id", []string{"meetings", "rename", ".", "New name"}, "is not a meeting id", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every refusal happens before the connection config is read, so no
			// flags are wired and no request can leave the process.
			code, _, stderr := runMeetingsCLIRaw(t, tc.args...)
			if code != tc.code {
				t.Fatalf("exit=%d, want %d (stderr=%q)", code, tc.code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, stderr)
			}
		})
	}
}
