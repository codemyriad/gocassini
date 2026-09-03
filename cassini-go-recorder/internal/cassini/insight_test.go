package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/insight"
	"gocassini/internal/meetingcontext"
	"gocassini/internal/transcribe"
)

// stubInsightProvider answers without a network, because there is no model
// endpoint in a test environment and every exit code this command promises has
// to be reachable anyway.
type stubInsightProvider struct {
	reply  string
	err    error
	system string
	user   string
	calls  int
}

func (p *stubInsightProvider) Describe() insight.ProviderRef {
	return insight.ProviderRef{Kind: "stub", BaseURL: "http://model.invalid/v1", Model: "stub-model"}
}

func (p *stubInsightProvider) Complete(_ context.Context, system, user string) (string, error) {
	p.calls++
	p.system = system
	p.user = user
	return p.reply, p.err
}

// useInsightProvider substitutes the model provider for one test.
func useInsightProvider(t *testing.T, provider insight.Provider) {
	t.Helper()
	original := insightProviderFor
	insightProviderFor = func(transcribe.LLMConfig) insight.Provider { return provider }
	t.Cleanup(func() { insightProviderFor = original })
}

// writeContextBundle writes what `cassini meetings context --json --out` writes.
func writeContextBundle(t *testing.T, path string, meetings ...meetingcontext.BuildInput) string {
	t.Helper()
	bundle := meetingcontext.Bundle{}
	for _, in := range meetings {
		bundle.Meetings = append(bundle.Meetings, meetingcontext.Build(in))
	}
	buf := &bytes.Buffer{}
	if err := meetingcontext.EncodeJSON(buf, bundle); err != nil {
		t.Fatalf("encode context bundle: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write context bundle: %v", err)
	}
	return path
}

func insightMeeting(id, title, roomID, roomName string) meetingcontext.BuildInput {
	return meetingcontext.BuildInput{
		ID:        id,
		Title:     title,
		RoomID:    roomID,
		RoomName:  roomName,
		WordCount: 6,
		Segments: []meetingcontext.Segment{
			{SpeakerLabel: "Ada", StartMS: 61000, EndMS: 64000, Text: "In " + title + " we agreed to ship."},
		},
	}
}

// D-656's "done when": a run over several meetings produces an answer, and the
// file says which meetings, which workflow version and hash, and which model
// produced it.
func TestInsightRunWritesTheAnswerAndItsProvenance(t *testing.T) {
	dir := t.TempDir()
	first := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "rm_one", "Planning"))
	second := writeContextBundle(t, filepath.Join(dir, "b.json"),
		insightMeeting("mtg_b", "Tuesday", "rm_two", "Delivery"),
		insightMeeting("mtg_c", "Wednesday", "rm_two", "Delivery"),
	)
	outPath := filepath.Join(dir, "Insight.md")
	recordPath := filepath.Join(dir, "insight.json")

	summaryLLMEnv(t, "http://model.invalid/v1")
	provider := &stubInsightProvider{reply: "# Meeting Summary\n\nThree meetings, one decision.\n"}
	useInsightProvider(t, provider)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"insight", "run",
		"--context", first,
		"--context", second,
		"--out", outPath,
		"--record", recordPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if provider.calls != 1 {
		t.Fatalf("completion calls = %d, want 1", provider.calls)
	}

	document, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read the insight: %v", err)
	}
	text := string(document)
	for _, want := range []string{
		`status: "succeeded"`,
		`workflow:`,
		`  id: "summarise"`,
		`  version: "v0"`,
		`  model: "stub-model"`,
		`    - id: "mtg_a"`,
		`    - id: "mtg_b"`,
		`    - id: "mtg_c"`,
		`      roomName: "Planning"`,
		"# Meeting Summary",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the insight does not carry %s:\n%s", want, text)
		}
	}

	// The workflow's content hash is the hash of the prompt the pipeline itself
	// sends, so the document can be traced to exact bytes rather than a name.
	registry, err := insightWorkflows()
	if err != nil {
		t.Fatalf("insightWorkflows: %v", err)
	}
	summarise, ok := registry.Lookup("summarise")
	if !ok {
		t.Fatal("the registry has no summarise workflow")
	}
	if !strings.Contains(text, `  sha256: "`+summarise.SHA256+`"`) {
		t.Errorf("the insight does not carry the workflow content hash %s:\n%s", summarise.SHA256, text)
	}
	if provider.system != strings.Replace(transcribe.SummaryPromptV0(), "{{TEMPLATE}}", transcribe.SummaryTemplateV0(), 1) {
		t.Error("the prompt sent is not the pipeline's own prompt, spliced")
	}

	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	var record insight.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode the record: %v", err)
	}
	if record.Status != insight.StatusSucceeded || len(record.Context.Meetings) != 3 {
		t.Errorf("record = %+v", record)
	}
	if record.Workflow.SHA256 != summarise.SHA256 {
		t.Errorf("the record and the document disagree about the workflow hash")
	}
	if !strings.HasPrefix(record.ArtifactID, "ins_") {
		t.Errorf("artifact id = %q", record.ArtifactID)
	}
	if !strings.Contains(stdout.String(), "insight -> "+outPath) {
		t.Errorf("stdout does not name the file it wrote: %s", stdout.String())
	}
}

// With no --out the document goes to stdout, the way every other read-only
// cassini verb behaves.
func TestInsightRunPrintsToStdoutWithoutOut(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))

	summaryLLMEnv(t, "http://model.invalid/v1")
	useInsightProvider(t, &stubInsightProvider{reply: "# Answer\n"})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"insight", "run", "--context", bundle}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "---\n") || !strings.Contains(stdout.String(), "# Answer") {
		t.Errorf("stdout = %s", stdout.String())
	}
}

// --timestamps is what lets a workflow ask the model to cite where a claim was
// made, and the record says whether it could.
func TestInsightRunRecordsWhetherTheModelCouldSeeTimestamps(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	recordPath := filepath.Join(dir, "insight.json")

	summaryLLMEnv(t, "http://model.invalid/v1")
	provider := &stubInsightProvider{reply: "# Answer\n"}
	useInsightProvider(t, provider)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--timestamps",
		"--out", filepath.Join(dir, "Insight.md"), "--record", recordPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(provider.user, "[01:01]") {
		t.Errorf("the context the model was shown carries no timestamps:\n%s", provider.user)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if !strings.Contains(string(raw), `"timestamps": true`) {
		t.Errorf("record = %s", raw)
	}
}

// A document a user asked for and did not get is not a warning. Every outcome
// gets its own exit code.
func TestInsightRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	if err := os.WriteFile(filepath.Join(dir, "not-a-bundle.json"), []byte(`{"version":"something.else"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cases := []struct {
		name     string
		baseURL  string
		provider insight.Provider
		args     []string
		want     int
	}{
		{
			name: "no --context at all",
			args: []string{"insight", "run"},
			want: 2,
		},
		{
			name: "a context bundle that is not there",
			args: []string{"insight", "run", "--context", filepath.Join(dir, "missing.json")},
			want: 2,
		},
		{
			name: "a file that is not a context bundle",
			args: []string{"insight", "run", "--context", filepath.Join(dir, "not-a-bundle.json")},
			want: 2,
		},
		{
			name: "a workflow that does not exist",
			args: []string{"insight", "run", "--context", bundle, "--workflow", "decisions"},
			want: 2,
		},
		{
			name:    "no endpoint configured",
			baseURL: "",
			args:    []string{"insight", "run", "--context", bundle},
			want:    exitInsightNoProvider,
		},
		{
			name:     "the endpoint refused",
			provider: &stubInsightProvider{err: insight.Fail(insight.ReasonProviderRefused, &transcribe.APIError{StatusCode: 401, Body: "no key"})},
			args:     []string{"insight", "run", "--context", bundle},
			want:     exitInsightRefused,
		},
		{
			name:     "the model failed",
			provider: &stubInsightProvider{err: insight.Fail(insight.ReasonModelFailed, context.DeadlineExceeded)},
			args:     []string{"insight", "run", "--context", bundle},
			want:     exitInsightModelFailed,
		},
		{
			name:     "the model answered with nothing",
			provider: &stubInsightProvider{reply: "\n\n"},
			args:     []string{"insight", "run", "--context", bundle},
			want:     exitInsightModelFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseURL := tc.baseURL
			if baseURL == "" && tc.name != "no endpoint configured" {
				baseURL = "http://model.invalid/v1"
			}
			summaryLLMEnv(t, baseURL)
			if tc.provider != nil {
				useInsightProvider(t, tc.provider)
			}
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), tc.args, &stdout, &stderr); code != tc.want {
				t.Fatalf("exit = %d, want %d\nstderr: %s", code, tc.want, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Error("a failure said nothing about why")
			}
		})
	}
}

// A failed run still writes its record, because that is the only durable
// statement of what was attempted — and it must not write a document.
func TestInsightRunRecordsAFailureAndWritesNoDocument(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	outPath := filepath.Join(dir, "Insight.md")
	recordPath := filepath.Join(dir, "insight.json")

	summaryLLMEnv(t, "http://model.invalid/v1")
	useInsightProvider(t, &stubInsightProvider{err: insight.Fail(insight.ReasonProviderRefused, &transcribe.APIError{StatusCode: 403, Body: "quota"})})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--out", outPath, "--record", recordPath,
	}, &stdout, &stderr); code != exitInsightRefused {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitInsightRefused, stderr.String())
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("a failed run wrote a document: %v", err)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	for _, want := range []string{`"status": "failed"`, `"reason": "provider-refused"`, "403"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("record is missing %s:\n%s", want, raw)
		}
	}
}

// The real provider, against a local stub endpoint rather than a model: a 4xx
// is the endpoint refusing and a 5xx is the call not completing, and the two
// need different answers from whoever ran the command.
func TestInsightRunClassifiesTheRealEndpointsFailures(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))

	cases := []struct {
		name   string
		status int
		want   int
	}{
		{"an unusable credential", http.StatusUnauthorized, exitInsightRefused},
		{"a server that broke", http.StatusInternalServerError, exitInsightModelFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			}))
			t.Cleanup(srv.Close)
			summaryLLMEnv(t, srv.URL)

			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), []string{"insight", "run", "--context", bundle}, &stdout, &stderr); code != tc.want {
				t.Fatalf("exit = %d, want %d\nstderr: %s", code, tc.want, stderr.String())
			}
		})
	}
}

// A successful run against a local stub endpoint, through the real provider:
// the prompt leaves as one chat completion and the answer comes back as the
// document. No model is involved; the point is the wiring.
func TestInsightRunAgainstAStubEndpoint(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "rm_one", "Planning"))

	var sawMessages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawMessages = len(body.Messages)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "# Meeting Summary\n\nIt shipped.\n"}}},
		})
	}))
	t.Cleanup(srv.Close)
	summaryLLMEnv(t, srv.URL)

	outPath := filepath.Join(dir, "Insight.md")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--model", "chosen-model", "--out", outPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if sawMessages != 2 {
		t.Errorf("messages = %d, want a system and a user message", sawMessages)
	}
	document, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read the insight: %v", err)
	}
	if !strings.Contains(string(document), "It shipped.") {
		t.Errorf("the answer did not reach the document:\n%s", document)
	}
	// --model is recorded, because the record has to describe the attempt that
	// produced the bytes rather than the deployment's default.
	if !strings.Contains(string(document), `  model: "chosen-model"`) {
		t.Errorf("the document does not name the model asked for:\n%s", document)
	}
}

// With no --out the document IS stdout, and YAML frontmatter is only
// frontmatter at byte 0. Anything this run wants to say about the files it
// wrote therefore goes to stderr — the same guard `cassini meetings context`
// puts on its own line, which is one of the three usages this command prints.
func TestInsightRunKeepsStdoutTheDocumentAlone(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	recordPath := filepath.Join(dir, "insight.json")

	summaryLLMEnv(t, "http://model.invalid/v1")
	useInsightProvider(t, &stubInsightProvider{reply: "# Answer\n"})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--record", recordPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "---\nversion:") {
		t.Errorf("the document does not start at byte 0 of stdout:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "insight_record ->") {
		t.Errorf("stdout carries a file announcement as well as the document:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "insight_record -> "+recordPath) {
		t.Errorf("nothing named the record that was written: %s", stderr.String())
	}
}

// The announcement still goes to stdout when the document went to a file, so a
// caller reading stdout learns both paths.
func TestInsightRunNamesBothFilesOnStdoutWithOut(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	outPath := filepath.Join(dir, "Insight.md")
	recordPath := filepath.Join(dir, "insight.json")

	summaryLLMEnv(t, "http://model.invalid/v1")
	useInsightProvider(t, &stubInsightProvider{reply: "# Answer\n"})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--out", outPath, "--record", recordPath,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, stderr.String())
	}
	for _, want := range []string{"insight -> " + outPath, "insight_record -> " + recordPath} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout does not carry %q:\n%s", want, stdout.String())
		}
	}
}

// A model answer that was produced is not discarded because a side file could
// not be written. The exit code says it was produced and something could not be
// written; the document is the part worth keeping.
func TestInsightRunKeepsTheDocumentWhenTheRecordCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	outPath := filepath.Join(dir, "Insight.md")

	summaryLLMEnv(t, "http://model.invalid/v1")
	useInsightProvider(t, &stubInsightProvider{reply: "# Answer\n\nIt shipped.\n"})

	var stdout, stderr bytes.Buffer
	// A directory is not a file this can create: --record is unwritable while
	// --out is perfectly writable.
	code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--out", outPath, "--record", dir,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstderr: %s", code, stderr.String())
	}
	document, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("the answer was thrown away with the record: %v", err)
	}
	if !strings.Contains(string(document), "It shipped.") {
		t.Errorf("the document does not carry the answer:\n%s", document)
	}
	if !strings.Contains(stderr.String(), "write record") {
		t.Errorf("the record failure was not reported: %s", stderr.String())
	}
}

// A failed re-run overwrites the record with the failure but cannot overwrite
// the document, so an earlier run's answer is left beside a record describing a
// different run. The file is the caller's and is not removed — but the pair is
// no longer a pair, and that is said out loud.
func TestInsightRunWarnsAboutAnEarlierRunsDocument(t *testing.T) {
	dir := t.TempDir()
	bundle := writeContextBundle(t, filepath.Join(dir, "a.json"), insightMeeting("mtg_a", "Monday", "", ""))
	outPath := filepath.Join(dir, "Insight.md")
	recordPath := filepath.Join(dir, "insight.json")
	if err := os.WriteFile(outPath, []byte("---\nstatus: \"succeeded\"\n---\n\n# Yesterday\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	summaryLLMEnv(t, "http://model.invalid/v1")
	useInsightProvider(t, &stubInsightProvider{err: insight.Fail(insight.ReasonModelFailed, context.DeadlineExceeded)})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{
		"insight", "run", "--context", bundle, "--out", outPath, "--record", recordPath,
	}, &stdout, &stderr); code != exitInsightModelFailed {
		t.Fatalf("exit = %d, want %d\nstderr: %s", code, exitInsightModelFailed, stderr.String())
	}
	if !strings.Contains(stderr.String(), "earlier run's document") {
		t.Errorf("nothing warned that the pair describes two runs: %s", stderr.String())
	}
	kept, err := os.ReadFile(outPath)
	if err != nil || !strings.Contains(string(kept), "# Yesterday") {
		t.Errorf("the earlier document was not left alone: %v %s", err, kept)
	}
}
