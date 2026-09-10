package cassini

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The server side of these routes is the app's (D-737). These tests pin the
// CLI's half of the contract against fakes of the shapes the design fixes:
// what goes on the wire, and how every answer — including the refusals — is
// phrased for the agent reading it.

const (
	meetingsTestAnnotationsPath = "/index.php/apps/app_api/proxy/gocassini/annotations/meetings/"
	meetingsTestTagsPath        = "/index.php/apps/app_api/proxy/gocassini/annotations/tags"
)

const annotatedMeetingAnswer = `{
  "meetingId": "MEET-1", "revision": 4, "resolved": true,
  "annotations": {
    "format": "cassini.annotations.v1", "revision": 4,
    "audioOpusSha256": "8e1f7499c6d5fba88c3bd9b69ecd3de1b07ae0cff65152c942c5e99062d01cbc",
    "tagNamespace": "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
    "tags": [{"id": "tag_a", "label": "hiring"}],
    "items": [
      {"id": "mk_1", "tagId": "tag_a", "target": {"kind": "meeting"},
       "createdAtUtc": "2026-09-10T11:23:54Z", "actor": {"kind": "person", "id": "alice"}, "operationId": "op_1"},
      {"id": "mk_2", "tagId": "tag_a", "target": {"kind": "time-range", "startMs": 869000, "endMs": 884000},
       "createdAtUtc": "2026-09-10T11:24:10Z", "actor": {"kind": "agent", "id": "bob smith"}, "operationId": "op_2"}
    ]
  }
}`

const tagsVocabulary = `{
  "tags": [
    {"tagId": "tag_a", "namespace": "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726", "label": "hiring", "meetings": 4, "marks": 9},
    {"tagId": "tag_b", "namespace": "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726", "label": "budget review", "meetings": 1, "marks": 1}
  ],
  "coverage": {"visible": 12, "indexed": 12}}`

const annotateCommitted = `{"meetingId": "MEET-1", "revision": 5, "operationId": "op_x",
  "added": ["mk_new"], "removed": [], "notFound": [],
  "annotations": {"format": "cassini.annotations.v1", "revision": 5}, "resolved": true}`

const oneMarkOps = `{"ops": [{"op": "mark", "tag": {"label": "hiring"}, "target": {"kind": "meeting"}}]}`

// serveRoutes answers "METHOD path" keys from routes and 404s everything else,
// which is also what an app without the annotation routes answers.
func serveRoutes(routes map[string]http.HandlerFunc) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if handle, ok := routes[r.Method+" "+r.URL.Path]; ok {
			handle(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func answerJSON(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

func writeOpsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requestedPath(fake *meetingsFakeNextcloud, path string) bool {
	for _, requested := range fake.requests {
		if requested == path {
			return true
		}
	}
	return false
}

func TestMeetingsAnnotationsPrintsEachMark(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestAnnotationsPath + "MEET-1": answerJSON(http.StatusOK, annotatedMeetingAnswer),
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "annotations", "MEET-1")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{
		"meeting=MEET-1 revision=4 tags=1 marks=2 resolved=yes caller=alice\n",
		"mark=mk_1 target=meeting actor=person:alice created=2026-09-10T11:23:54Z operation=op_1 tag_id=tag_a tag=hiring\n",
		// A range reads as where to listen; a user id with a space stays one field.
		`mark=mk_2 target=14:29-14:44 actor=agent:"bob smith" created=2026-09-10T11:24:10Z operation=op_2 tag_id=tag_a tag=hiring`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if fake.lastUser != "alice" || !fake.lastAuthOK {
		t.Errorf("request was not authenticated as the caller: user=%q ok=%v", fake.lastUser, fake.lastAuthOK)
	}
}

func TestMeetingsAnnotationsJSONReEmitsTheAnswer(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestAnnotationsPath + "MEET-1": answerJSON(http.StatusOK, annotatedMeetingAnswer),
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "annotations", "MEET-1", "--json")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var got, want any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if err := json.Unmarshal([]byte(annotatedMeetingAnswer), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("--json did not re-emit the app's answer:\n%s", stdout)
	}
}

// Every document the app can hand back reads as something, and none of them as
// a failure: the format asks readers to skip what they cannot use.
func TestMeetingsAnnotationsSaysWhatItIsNotShowing(t *testing.T) {
	cases := []struct {
		name       string
		answer     string
		wantStdout []string
		wantStderr string
		denyStdout string
	}{
		{
			name:       "no marks",
			answer:     `{"meetingId": "MEET-1", "revision": 0, "annotations": null, "resolved": null}`,
			wantStdout: []string{"marks=0 resolved=-", "note=this meeting carries no marks"},
			denyStdout: "mark=",
		},
		{
			name:       "marks made against other audio",
			answer:     strings.Replace(annotatedMeetingAnswer, `"resolved": true`, `"resolved": false`, 1),
			wantStdout: []string{"resolved=no", "made against different audio", "mark=mk_1"},
		},
		{
			name:       "a format this build does not read",
			answer:     `{"meetingId": "MEET-1", "revision": 7, "annotations": {"format": "cassini.annotations.v9"}, "resolved": null}`,
			wantStdout: []string{"meeting=MEET-1 revision=7", "in a format this cassini build does not read"},
			denyStdout: "mark=",
		},
		{
			name:       "a v1 document that does not parse",
			answer:     `{"meetingId": "MEET-1", "revision": 2, "annotations": {"format": "cassini.annotations.v1", "revision": "two"}, "resolved": true}`,
			wantStdout: []string{"meeting=MEET-1 revision=2"},
			wantStderr: "marks could not be read",
			denyStdout: "mark=",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
				"GET " + meetingsTestAnnotationsPath + "MEET-1": answerJSON(http.StatusOK, tc.answer),
			}))
			code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "annotations", "MEET-1")
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			for _, want := range tc.wantStdout {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if tc.wantStderr != "" && !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr lacks %q:\n%s", tc.wantStderr, stderr)
			}
			if tc.denyStdout != "" && strings.Contains(stdout, tc.denyStdout) {
				t.Errorf("stdout must not contain %q:\n%s", tc.denyStdout, stdout)
			}
		})
	}
}

// A 404 is absent or unreadable, answered identically — unless the app has no
// tags at all, which is a statement about the app and safe to make.
func TestMeetingsAnnotationsNotFound(t *testing.T) {
	t.Run("an app with tags keeps denial empty", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
			"GET " + meetingsTestAnnotationsPath + "MEET-9": answerJSON(http.StatusNotFound, `{"error": "not found"}`),
			"GET " + meetingsTestTagsPath:                   answerJSON(http.StatusOK, tagsVocabulary),
		}))
		code, _, stderr := runMeetingsCLI(t, fake.server.URL, "annotations", "MEET-9")
		if code != 1 {
			t.Fatalf("exit=%d, want 1", code)
		}
		if !strings.Contains(stderr, "no recording you can read at that id") {
			t.Errorf("stderr should use the shared 404 wording:\n%s", stderr)
		}
		for _, banned := range []string{"does not offer", "forbidden", "does not exist"} {
			if strings.Contains(stderr, banned) {
				t.Errorf("stderr must not say %q:\n%s", banned, stderr)
			}
		}
	})
	t.Run("an app without tags says so", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, serveRoutes(nil))
		code, _, stderr := runMeetingsCLI(t, fake.server.URL, "annotations", "MEET-9")
		if code != 1 {
			t.Fatalf("exit=%d, want 1", code)
		}
		if !strings.Contains(stderr, "does not offer tags and marks") {
			t.Errorf("stderr should name the real problem:\n%s", stderr)
		}
		if strings.Contains(stderr, "no recording you can read") {
			t.Errorf("an app without tags is not a missing meeting:\n%s", stderr)
		}
	})
}

func TestMeetingsAnnotateSendsTheBatchAsTheCaller(t *testing.T) {
	var sent []byte
	var contentType string
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"POST " + meetingsTestAnnotationsPath + "MEET-1": func(w http.ResponseWriter, r *http.Request) {
			sent, _ = io.ReadAll(r.Body)
			contentType = r.Header.Get("Content-Type")
			answerJSON(http.StatusOK, annotateCommitted)(w, r)
		},
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "annotate", "MEET-1",
		"--ops", writeOpsFile(t, oneMarkOps), "--expect-revision", "4", "--operation-id", "op_x")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("the request body is not JSON: %v (%s)", err, sent)
	}
	var file struct {
		Ops json.RawMessage `json:"ops"`
	}
	_ = json.Unmarshal([]byte(oneMarkOps), &file)
	var gotOps, wantOps any
	_ = json.Unmarshal(body["ops"], &gotOps)
	_ = json.Unmarshal(file.Ops, &wantOps)
	if !reflect.DeepEqual(gotOps, wantOps) {
		t.Errorf("ops were not forwarded as written: got %s", body["ops"])
	}
	for key, want := range map[string]string{"expectRevision": "4", "actorKind": `"agent"`, "operationId": `"op_x"`} {
		if string(body[key]) != want {
			t.Errorf("%s = %s, want %s", key, body[key], want)
		}
	}
	// The actor id is the app's to take from the authenticated caller. A body
	// that carried one would be asking the app to believe it.
	for key := range body {
		if key != "ops" && key != "expectRevision" && key != "actorKind" && key != "operationId" {
			t.Errorf("the request carries %q, which the contract does not have", key)
		}
	}
	for _, want := range []string{
		"annotated=MEET-1 revision=5 operation=op_x added=1 removed=0 not_found=0 resolved=yes caller=alice\n",
		"change=added mark=mk_new\n",
		`hint=undo this batch's marks with {"op": "undo-operation", "operationId": "op_x"}`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestMeetingsAnnotateSendsOnlyWhatWasAsked(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		stdin string
		want  map[string]string
		deny  []string
	}{
		{
			name:  "stdin, a person, no guard",
			args:  []string{"--ops", "-", "--actor-kind", "person"},
			stdin: oneMarkOps,
			want:  map[string]string{"actorKind": `"person"`},
			deny:  []string{"expectRevision", "operationId"},
		},
		{
			// Zero is a real guard: "only if it carries no marks yet". Treating it
			// as unset would drop the guard its author asked for.
			name: "an explicit zero revision is sent",
			args: []string{"--ops", "", "--expect-revision", "0"},
			want: map[string]string{"expectRevision": "0", "actorKind": `"agent"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sent []byte
			fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
				"POST " + meetingsTestAnnotationsPath + "MEET-1": func(w http.ResponseWriter, r *http.Request) {
					sent, _ = io.ReadAll(r.Body)
					answerJSON(http.StatusOK, annotateCommitted)(w, r)
				},
			}))
			if tc.stdin != "" {
				previous := meetingsStdin
				meetingsStdin = strings.NewReader(tc.stdin)
				t.Cleanup(func() { meetingsStdin = previous })
			}
			args := append([]string{"annotate", "MEET-1"}, tc.args...)
			for i, arg := range args {
				if arg == "" {
					args[i] = writeOpsFile(t, oneMarkOps)
				}
			}
			if code, _, stderr := runMeetingsCLI(t, fake.server.URL, args...); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(sent, &body); err != nil {
				t.Fatalf("body: %v (%s)", err, sent)
			}
			for key, want := range tc.want {
				if string(body[key]) != want {
					t.Errorf("%s = %s, want %s", key, body[key], want)
				}
			}
			for _, key := range tc.deny {
				if _, ok := body[key]; ok {
					t.Errorf("the request carries %s, which was not asked for: %s", key, sent)
				}
			}
		})
	}
}

// A batch that is wrong is refused before anything is sent, with the code that
// says whose problem it is.
func TestMeetingsAnnotateRefusesABadBatchBeforeSending(t *testing.T) {
	large := `{"ops": [` + strings.Repeat(`{"op": "unmark", "itemId": "mk_0123456789"},`, 2000) + `{}]}`
	cases := []struct {
		name     string
		ops      string // written to a file unless flagged below
		args     []string
		id       string
		wantExit int
		want     string
	}{
		{name: "no ops flag", args: []string{}, wantExit: 2, want: "--ops is required"},
		{name: "a file that is not there", args: []string{"--ops", "/nonexistent/ops.json"}, wantExit: 2, want: "read --ops"},
		{name: "not JSON", ops: "mark it", wantExit: 4, want: `is not an {"ops": [...]} document`},
		{name: "a bare array", ops: `[{"op": "mark"}]`, wantExit: 4, want: `is not an {"ops": [...]} document`},
		{name: "a member the envelope does not have", ops: `{"ops": [{}], "expectRevision": 3}`, wantExit: 4, want: "unknown field"},
		{name: "no ops", ops: `{"ops": []}`, wantExit: 4, want: "holds no ops"},
		{name: "ops that are not an array", ops: `{"ops": {"op": "mark"}}`, wantExit: 4, want: `"ops" must be an array`},
		{name: "two documents", ops: oneMarkOps + oneMarkOps, wantExit: 4, want: "more than one JSON document"},
		{name: "too large", ops: large, wantExit: 4, want: "larger than 64 KiB"},
		{name: "an unknown actor kind", ops: oneMarkOps, args: []string{"--actor-kind", "robot"}, wantExit: 2, want: "--actor-kind must be agent or person"},
		{name: "a negative revision", ops: oneMarkOps, args: []string{"--expect-revision", "-1"}, wantExit: 2, want: "--expect-revision must be 0 or more"},
		{name: "a dot-segment id", ops: oneMarkOps, id: "..", wantExit: 2, want: "is not a meeting id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, answerJSON(http.StatusOK, annotateCommitted))
			id := tc.id
			if id == "" {
				id = "MEET-1"
			}
			args := []string{"annotate", id}
			if tc.ops != "" {
				args = append(args, "--ops", writeOpsFile(t, tc.ops))
			}
			args = append(args, tc.args...)
			code, _, stderr := runMeetingsCLI(t, fake.server.URL, args...)
			if code != tc.wantExit {
				t.Errorf("exit=%d, want %d (stderr=%q)", code, tc.wantExit, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, stderr)
			}
			if len(fake.requests) != 0 {
				t.Errorf("a refused batch still reached the network: %v", fake.requests)
			}
		})
	}
}

// Each refusal the app can give is phrased as what it means, with the exit code
// `cassini annotate` gives the same condition.
func TestMeetingsAnnotateErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantExit int
		want     string
		deny     []string
	}{
		{"invalid ops", http.StatusBadRequest, `{"error": "ops[0]: unknown op \"smark\""}`, 4, `the app refused these ops: ops[0]: unknown op "smark"`, nil},
		{"revision moved on", http.StatusConflict, `{"error": "revision-conflict"}`, 3, "no longer at the revision you expected", nil},
		{"marks bound to other audio", http.StatusConflict, `{"error": "unresolved"}`, 5, "made against different audio", nil},
		{"retries exhausted", http.StatusConflict, `{"error": "conflict"}`, 1, "re-running the same ops is safe", nil},
		{"too large", http.StatusRequestEntityTooLarge, `{"error": "body too large"}`, 4, "too large for the app", nil},
		{"substrate outage", http.StatusBadGateway, `{"error": "nextcloud unavailable"}`, 1, "an outage, not a permissions problem", nil},
		{"absent or unreadable", http.StatusNotFound, `{"error": "not found"}`, 1, "no recording you can read at that id", []string{"does not offer"}},
		{"write not accepted", http.StatusMethodNotAllowed, "", 1, "route declarations may predate tags", []string{"only GET and HEAD"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
				"POST " + meetingsTestAnnotationsPath + "MEET-1": answerJSON(tc.status, tc.body),
				"GET " + meetingsTestTagsPath:                    answerJSON(http.StatusOK, tagsVocabulary),
			}))
			code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "annotate", "MEET-1", "--ops", writeOpsFile(t, oneMarkOps))
			if code != tc.wantExit {
				t.Errorf("exit=%d, want %d (stderr=%q)", code, tc.wantExit, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, stderr)
			}
			for _, banned := range tc.deny {
				if strings.Contains(stderr, banned) {
					t.Errorf("stderr must not say %q:\n%s", banned, stderr)
				}
			}
			if strings.Contains(stdout, "annotated=") {
				t.Errorf("a refused batch was reported as committed:\n%s", stdout)
			}
		})
	}
}

// No answer at all is the one case where the CLI cannot know whether the batch
// landed, and it must say exactly that.
func TestMeetingsAnnotateSaysAnUnansweredWriteMayHaveLanded(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "annotate", "MEET-1", "--ops", writeOpsFile(t, oneMarkOps))

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "may or may not have been committed") || !strings.Contains(stderr, "idempotent") {
		t.Errorf("stderr should say the outcome is unknown and why a retry is safe:\n%s", stderr)
	}
}

func TestMeetingsTagsPrintsTheVocabulary(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestTagsPath: answerJSON(http.StatusOK, tagsVocabulary),
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "tags")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{
		"tags=2 caller=alice indexed=12 of 12 meeting(s) you can read\n",
		"tag=tag_a meetings=4 marks=9 label=hiring\n",
		"tag=tag_b meetings=1 marks=1 label=budget review\n",
		"--tag",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "not in the tag index") {
		t.Errorf("full coverage was reported as partial:\n%s", stdout)
	}
}

func TestMeetingsTagsReportsPartialCoverage(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestTagsPath: answerJSON(http.StatusOK, strings.Replace(tagsVocabulary, `"indexed": 12`, `"indexed": 9`, 1)),
	}))

	_, stdout, _ := runMeetingsCLI(t, fake.server.URL, "tags")

	if !strings.Contains(stdout, "note=3 meeting(s) you can read are not in the tag index yet") {
		t.Errorf("stdout did not report the gap:\n%s", stdout)
	}
}

func TestMeetingsTagsFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"an app without tags", http.StatusNotFound, "does not offer tags and marks"},
		{"an index still being built", http.StatusServiceUnavailable, "still building its tag index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, answerJSON(tc.status, `{"error": "x"}`))
			code, _, stderr := runMeetingsCLI(t, fake.server.URL, "tags")
			if code != 1 {
				t.Errorf("exit=%d, want 1", code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, stderr)
			}
			if strings.Contains(stderr, "permissions") {
				t.Errorf("this is not a permissions problem:\n%s", stderr)
			}
		})
	}
}

func TestMeetingsSearchNarrowsByTag(t *testing.T) {
	const markedHit = `{"hits": [{"meetingId": "MEET-1", "title": "Standup", "segmentId": "s1",
	  "startMs": 870000, "endMs": 872000, "matched": "exact",
	  "marks": [{"tagId": "tag_a", "label": "hiring"}, {"tagId": "tag_b", "label": "budget review"}]}],
	  "coverage": {"visible": 12, "searched": 12}}`
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestTagsPath:   answerJSON(http.StatusOK, tagsVocabulary),
		"GET " + meetingsTestSearchPath: answerJSON(http.StatusOK, markedHit),
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "search", "offer", "--tag", "Hiring")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if len(fake.requests) != 2 || fake.requests[0] != meetingsTestTagsPath || fake.requests[1] != meetingsTestSearchPath {
		t.Fatalf("requested %v, want the tag check and then the search", fake.requests)
	}
	if !strings.Contains(fake.queries[1], "tag=Hiring") {
		t.Errorf("search query %q does not carry the tag", fake.queries[1])
	}
	if !strings.Contains(stdout, `marks=hiring,"budget review" title=Standup`) {
		t.Errorf("stdout did not list the moment's marks:\n%s", stdout)
	}
	// A label matched case-insensitively is a known tag, and coverage is full.
	if strings.Contains(stdout, "carries that tag") || strings.Contains(stdout, "tag index") {
		t.Errorf("stdout carried a note that does not apply:\n%s", stdout)
	}
}

// The failure the tag check exists for: an app that does not know the
// parameter would answer the untagged question as though it were narrowed.
func TestMeetingsTagNarrowingRefusesAnAppWithoutTags(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args []string
		// route is what an app without tags still answers.
		route, body string
	}{
		{"search", []string{"search", "offer", "--tag", "hiring"}, meetingsTestSearchPath, searchOneHit},
		{"list", []string{"list", "--tag", "hiring"}, meetingsTestListPath, twoMeetingCatalog},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
				"GET " + tc.route:                answerJSON(http.StatusOK, tc.body),
				"GET " + meetingsTestCatalogPath: answerJSON(http.StatusOK, twoMeetingCatalog),
			}))
			code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, tc.args...)
			if code != 1 {
				t.Fatalf("exit=%d, want 1; stdout=%q", code, stdout)
			}
			if !strings.Contains(stderr, "does not offer tags and marks") {
				t.Errorf("stderr should name the real problem:\n%s", stderr)
			}
			if requestedPath(fake, tc.route) || requestedPath(fake, meetingsTestCatalogPath) {
				t.Errorf("the unnarrowable route was still asked: %v", fake.requests)
			}
		})
	}
}

// The catalog carries no marks, so the list route's absence cannot be papered
// over with it when a tag is asked for.
func TestMeetingsListNeverFallsBackToTheCatalogForATag(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestTagsPath:    answerJSON(http.StatusOK, tagsVocabulary),
		"GET " + meetingsTestCatalogPath: answerJSON(http.StatusOK, twoMeetingCatalog),
	}))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--tag", "hiring")

	if code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%q", code, stdout)
	}
	if requestedPath(fake, meetingsTestCatalogPath) {
		t.Errorf("fell back to the catalog, which cannot narrow by tag: %v", fake.requests)
	}
	if !strings.Contains(stderr, "does not offer tags and marks") {
		t.Errorf("stderr should name the real problem:\n%s", stderr)
	}
}

func TestMeetingsListNarrowsByTag(t *testing.T) {
	const taggedList = `{"version": "cassini.viewer.catalog.v1",
	  "meetings": [{"id": "MEET-1", "title": "Standup", "dateLabel": "2026-09-01 09:00", "audioPath": "./meetings/MEET-1.opus"}],
	  "excluded": {"total": 2, "undated": 0}}`
	routes := map[string]http.HandlerFunc{
		"GET " + meetingsTestTagsPath: answerJSON(http.StatusOK, tagsVocabulary),
		"GET " + meetingsTestListPath: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			answerJSON(http.StatusOK, taggedList)(w, r)
		},
	}

	t.Run("text", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, serveRoutes(routes))
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--tag", "budget review")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if !strings.Contains(fake.queries[len(fake.queries)-1], "tag=budget+review") {
			t.Errorf("list query %q does not carry the tag", fake.queries[len(fake.queries)-1])
		}
		for _, want := range []string{`filter=tag:"budget review" excluded=2`, "meeting=MEET-1"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q:\n%s", want, stdout)
			}
		}
	})
	t.Run("json echoes the tag and keeps notes off stdout", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, serveRoutes(routes))
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--tag", "unheard-of", "--json")
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		var doc struct {
			Filter struct {
				Tag string `json:"tag"`
			} `json:"filter"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
		}
		if doc.Filter.Tag != "unheard-of" {
			t.Errorf("filter.tag = %q, want the tag", doc.Filter.Tag)
		}
		if !strings.Contains(stderr, "warning=no meeting you can read carries that tag") {
			t.Errorf("an unknown tag went unreported beside --json:\n%s", stderr)
		}
	})
}

func TestMeetingsSearchReportsWhatATagCannotReach(t *testing.T) {
	partial := strings.Replace(tagsVocabulary, `"indexed": 12`, `"indexed": 9`, 1)
	fake := newMeetingsFakeNextcloud(t, serveRoutes(map[string]http.HandlerFunc{
		"GET " + meetingsTestTagsPath:   answerJSON(http.StatusOK, partial),
		"GET " + meetingsTestSearchPath: answerJSON(http.StatusOK, `{"hits": [], "coverage": {"visible": 12, "searched": 12}}`),
	}))

	_, stdout, _ := runMeetingsCLI(t, fake.server.URL, "search", "offer", "--tag", "unheard-of")

	for _, want := range []string{
		"note=no meeting you can read carries that tag",
		"note=3 meeting(s) you can read are not in the tag index yet",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// A connection that fails before any answer used to surface Go's *url.Error,
// which quotes the whole request URL, query and all.
func TestMeetingsTransportErrorsKeepTheQueryOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + listener.Addr().String()
	listener.Close()

	code, _, stderr := runMeetingsCLI(t, closedURL, "search", "severance package for Bob")

	if code == 0 {
		t.Fatal("exit=0 against a closed port")
	}
	for _, leaked := range []string{"severance", "Bob", "q="} {
		if strings.Contains(stderr, leaked) {
			t.Errorf("error output leaked %q from the query:\n%s", leaked, stderr)
		}
	}
	if !strings.Contains(stderr, "GET "+closedURL) {
		t.Errorf("stderr should still say which request failed:\n%s", stderr)
	}
}

func TestMeetingsUsageListsTheTagCommands(t *testing.T) {
	fake := httptest.NewServer(http.NotFoundHandler())
	defer fake.Close()
	_, stdout, _ := runMeetingsCLI(t, fake.URL, "--help")
	for _, want := range []string{"cassini meetings tags", "cassini meetings annotations <meeting-id>", "cassini meetings annotate <meeting-id> --ops", "--tag hiring"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("usage lacks %q:\n%s", want, stdout)
		}
	}
}
