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
	"sync"
	"testing"

	inspectpkg "gocassini/internal/inspect"
)

// summaryLLMEnv points the summary step at an endpoint the way an operator
// would, clearing every other variable DefaultBuildConfig reads so the
// developer's own environment cannot leak into the test.
func summaryLLMEnv(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_BASE_URL", "")
	t.Setenv("LLM_BASE_URL", baseURL)
	t.Setenv("LLM_MODEL", "")
	t.Setenv("SUMMARY_BASE_URL", "")
	t.Setenv("SUMMARY_API_KEY", "")
	t.Setenv("SUMMARY_MODEL", "test-summary-model")
	t.Setenv("READABLE_BASE_URL", "")
	t.Setenv("CASSINI_SUMMARY_DISABLED", "")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "")
	t.Setenv("CASSINI_LLM_MAX_TOKENS", "")
	// The insight step's own layer, and the per-step bounds, are read by
	// `cassini insight run` (D-719). A developer with either exported would
	// otherwise silently redirect these tests at their own endpoint.
	t.Setenv("INSIGHT_BASE_URL", "")
	t.Setenv("INSIGHT_API_KEY", "")
	t.Setenv("INSIGHT_MODEL", "")
	t.Setenv("SUMMARY_TIMEOUT_SEC", "")
	t.Setenv("SUMMARY_MAX_TOKENS", "")
	t.Setenv("INSIGHT_TIMEOUT_SEC", "")
	t.Setenv("INSIGHT_MAX_TOKENS", "")
}

// stubSummaryLLM is an OpenAI-compatible /chat/completions stub. It counts
// requests and records the last Authorization header so the keyless contract
// — no header at all when no key is configured — can be asserted.
type stubSummaryLLM struct {
	mu       sync.Mutex
	requests int
	lastAuth string
	sawAuth  bool
}

func (s *stubSummaryLLM) handler(summaryBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		s.lastAuth = r.Header.Get("Authorization")
		_, s.sawAuth = r.Header["Authorization"]
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": summaryBody}},
			},
		})
	}
}

func (s *stubSummaryLLM) snapshot() (int, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.lastAuth, s.sawAuth
}

// packFixtureOpusWithSummary packs a portable meeting whose bundle already
// carried a summary.md, the way a post-summary build seals one in.
func packFixtureOpusWithSummary(t *testing.T, dir, name, summaryBody string) string {
	t.Helper()
	bundleDir := filepath.Join(dir, name+".meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "summary.md"), []byte(summaryBody), 0o644); err != nil {
		t.Fatalf("write summary.md: %v", err)
	}
	// The pack step reads the summary only when the artifact manifest names it.
	manifestPath := filepath.Join(bundleDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read artifact manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse artifact manifest: %v", err)
	}
	files, ok := manifest["files"].(map[string]any)
	if !ok {
		t.Fatal("fixture artifact manifest has no files map")
	}
	files["summary"] = "summary.md"
	updated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode artifact manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, updated, 0o644); err != nil {
		t.Fatalf("write artifact manifest: %v", err)
	}

	outPath := filepath.Join(dir, name+".opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack fixture failed code=%d stderr=%q", code, stderr.String())
	}
	return outPath
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

func TestMeetingsSummarizeSealsASummaryInPlace(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpus(t, tmp, "meeting")
	before := decodePortableManifestFromOpus(t, path)

	const returned = "# Meeting Summary\n\n## Overview\n\nBackfilled overview."
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler(returned))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("summarize failed code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), path+": summarized") {
		t.Errorf("stdout = %q, want a summarized status line", stdout.String())
	}

	// Read it back with the same reader the CLI itself uses for summaries
	// (`meetings context` goes through inspect.ExtractMeeting).
	meeting, err := inspectpkg.ExtractMeeting(path)
	if err != nil {
		t.Fatalf("re-read summarized meeting: %v", err)
	}
	// BuildMeetingSummary normalises the model output to end in one newline.
	if got := string(meeting.SummaryMarkdown); got != returned+"\n" {
		t.Errorf("sealed summary = %q, want %q", got, returned+"\n")
	}

	// The metadata mirrors what a built file carries (buildPortableSummaryMetadata)...
	if got := meeting.SummaryFormat(); got != "markdown" {
		t.Errorf("summary format = %q, want markdown", got)
	}
	if got := meeting.SummaryModel(); got != "test-summary-model" {
		t.Errorf("summary model = %q, want test-summary-model", got)
	}
	after := decodePortableManifestFromOpus(t, path)
	if got := after.Summary["templateVersion"]; got != "v0" {
		t.Errorf("summary templateVersion = %v, want v0", got)
	}
	// ...with the one honest difference: provenance records, in the schema's
	// existing source field, that this summary was generated after sealing.
	if after.Provenance == nil || after.Provenance.MeetingSummary == nil {
		t.Fatal("backfilled file carries no meetingSummary provenance")
	}
	if got := after.Provenance.MeetingSummary.Source; got != "backfill" {
		t.Errorf("meetingSummary provenance source = %q, want backfill", got)
	}
	if got := after.Provenance.MeetingSummary.Model; got != "test-summary-model" {
		t.Errorf("meetingSummary provenance model = %q, want test-summary-model", got)
	}

	// The audio was not re-encoded: the file still claims — and the reader
	// above verified against — the identical integrity block.
	if after.Integrity != before.Integrity {
		t.Errorf("integrity block changed:\n before %+v\n after  %+v", before.Integrity, after.Integrity)
	}

	// A keyless endpoint gets no Authorization header at all, not an empty one.
	requests, lastAuth, sawAuth := stub.snapshot()
	if requests != 1 {
		t.Errorf("LLM requests = %d, want exactly 1", requests)
	}
	if sawAuth {
		t.Errorf("Authorization header = %q, want it omitted for a keyless endpoint", lastAuth)
	}
}

func TestMeetingsSummarizeSkipsAFileThatHasASummary(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpusWithSummary(t, tmp, "meeting", "# Meeting Summary\n\nAlready here.\n")

	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler("must never be requested"))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	before := readFileBytes(t, path)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("summarize exit=%d stderr=%q, want 0 for a skip", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), path+": skip (has summary)") {
		t.Errorf("stdout = %q, want a skip status line", stdout.String())
	}
	if requests, _, _ := stub.snapshot(); requests != 0 {
		t.Errorf("the LLM was called %d time(s) for a skipped file", requests)
	}
	if !bytes.Equal(before, readFileBytes(t, path)) {
		t.Error("a skipped file was rewritten")
	}
}

func TestMeetingsSummarizeForceReplacesAnExistingSummary(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpusWithSummary(t, tmp, "meeting", "# Meeting Summary\n\nThe old summary.\n")

	const regenerated = "# Meeting Summary\n\nRegenerated after sealing."
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler(regenerated))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", "--force", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("summarize --force failed code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), path+": summarized") {
		t.Errorf("stdout = %q, want a summarized status line", stdout.String())
	}

	meeting, err := inspectpkg.ExtractMeeting(path)
	if err != nil {
		t.Fatalf("re-read summarized meeting: %v", err)
	}
	if got := string(meeting.SummaryMarkdown); got != regenerated+"\n" {
		t.Errorf("sealed summary = %q, want the regenerated %q", got, regenerated+"\n")
	}
	// Replaced, not duplicated: exactly one summary.md attachment survives.
	manifest := decodePortableManifestFromOpus(t, path)
	count := 0
	for _, attachment := range manifest.Attachments {
		if name, _ := attachment["name"].(string); strings.EqualFold(strings.TrimSpace(name), "summary.md") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("summary.md attachments = %d, want exactly 1", count)
	}
}

func TestMeetingsSummarizeFailingLLMLeavesTheFileUntouched(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpus(t, tmp, "meeting")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	before := readFileBytes(t, path)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 when the LLM fails", code)
	}
	if !strings.Contains(stdout.String(), path+": failed: ") {
		t.Errorf("stdout = %q, want a failed status line", stdout.String())
	}
	if !bytes.Equal(before, readFileBytes(t, path)) {
		t.Error("a failed summarize modified the file; it must stay byte-identical")
	}
}

func TestMeetingsSummarizeProcessesEveryFileWhenOneFails(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	broken := filepath.Join(tmp, "broken.opus")
	if err := os.WriteFile(broken, []byte("not an ogg container"), 0o644); err != nil {
		t.Fatalf("write broken fixture: %v", err)
	}
	good := packFixtureOpus(t, tmp, "good")

	const returned = "# Meeting Summary\n\nStill made it."
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler(returned))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", broken, good}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 when any file failed", code)
	}
	if !strings.Contains(stdout.String(), broken+": failed: ") {
		t.Errorf("stdout = %q, want a failed line for the broken file", stdout.String())
	}
	if !strings.Contains(stdout.String(), good+": summarized") {
		t.Errorf("stdout = %q, want the good file summarized despite the earlier failure", stdout.String())
	}
	meeting, err := inspectpkg.ExtractMeeting(good)
	if err != nil {
		t.Fatalf("re-read the good meeting: %v", err)
	}
	if got := string(meeting.SummaryMarkdown); got != returned+"\n" {
		t.Errorf("sealed summary = %q, want %q", got, returned+"\n")
	}
}

func TestMeetingsSummarizeOutWritesACopyAndKeepsTheInput(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpus(t, tmp, "meeting")
	outPath := filepath.Join(tmp, "with-summary.opus")

	const returned = "# Meeting Summary\n\nWritten to a copy."
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler(returned))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	before := readFileBytes(t, path)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", "--out", outPath, path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("summarize --out failed code=%d stderr=%q", code, stderr.String())
	}
	if !bytes.Equal(before, readFileBytes(t, path)) {
		t.Error("--out modified the input file")
	}
	meeting, err := inspectpkg.ExtractMeeting(outPath)
	if err != nil {
		t.Fatalf("read --out file: %v", err)
	}
	if got := string(meeting.SummaryMarkdown); got != returned+"\n" {
		t.Errorf("--out summary = %q, want %q", got, returned+"\n")
	}
}

func TestMeetingsSummarizeRefusesWithoutAnLLMEndpoint(t *testing.T) {
	// Everything cleared: no base URL, no key. The command must refuse on
	// configuration alone, before touching any file — the input named here
	// does not even exist.
	summaryLLMEnv(t, "")
	t.Setenv("SUMMARY_MODEL", "")

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "summarize", "never-read.opus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 with no LLM endpoint configured", code)
	}
	for _, want := range []string{"LLM_BASE_URL", "OPENROUTER_API_KEY", "SUMMARY_BASE_URL", "CASSINI_SUMMARY_DISABLED"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to name %s", stderr.String(), want)
		}
	}
}

func TestMeetingsSummarizeConfigurationErrors(t *testing.T) {
	t.Run("no files", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{"meetings", "summarize"}, &stdout, &stderr); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
	})
	t.Run("flags after files are refused", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "summarize", "a.opus", "--force"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "flags must come before") {
			t.Errorf("stderr = %q, want the flag-ordering explanation", stderr.String())
		}
	})
	t.Run("non-opus input", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "summarize", "meeting.mkv"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "not a .opus file") {
			t.Errorf("stderr = %q, want the .opus requirement", stderr.String())
		}
	})
	t.Run("out with multiple inputs", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"meetings", "summarize", "--out", "o.opus", "a.opus", "b.opus"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "--out only works with a single input") {
			t.Errorf("stderr = %q, want the single-input rule", stderr.String())
		}
	})
}

func TestMeetingsSummarizeUsage(t *testing.T) {
	t.Run("family usage lists summarize", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{"meetings"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(stdout.String(), "summarize") {
			t.Errorf("meetings usage does not mention summarize:\n%s", stdout.String())
		}
	})
	t.Run("help exits zero", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{"meetings", "summarize", "--help"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d stderr=%q", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "cassini meetings summarize") {
			t.Errorf("stderr = %q, want the summarize usage", stderr.String())
		}
	})
}

// The built-in template is written for a working meeting between colleagues.
// A conference talk is not one, and the summary it produces reads as though it
// were ("the main outcome was...", "action items"). --from-file seals a
// summary somebody wrote for the recording it actually is.
func TestMeetingsSummarizeFromFileSealsAnAuthoredSummary(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpus(t, tmp, "talk")
	before := decodePortableManifestFromOpus(t, path)

	const authored = "# It Takes a Village\n\nPaula opens with a photograph of a bridge."
	summaryPath := filepath.Join(tmp, "talk.md")
	if err := os.WriteFile(summaryPath, []byte(authored+"\n\n\n"), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	// Deliberately no LLM endpoint configured: an authored summary must not
	// need one, and no request may be made.
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"meetings", "summarize", "--from-file", summaryPath, path,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("summarize failed code=%d stderr=%q", code, stderr.String())
	}

	meeting, err := inspectpkg.ExtractMeeting(path)
	if err != nil {
		t.Fatalf("re-read summarized meeting: %v", err)
	}
	// Trailing blank lines are normalised to exactly one newline, so a heredoc
	// and an editor's saved file seal to identical bytes.
	if got := string(meeting.SummaryMarkdown); got != authored+"\n" {
		t.Errorf("sealed summary = %q, want %q", got, authored+"\n")
	}

	after := decodePortableManifestFromOpus(t, path)
	// No model is claimed, because none wrote it. A model name here would make
	// an authored summary indistinguishable from a generated one.
	if got := meeting.SummaryModel(); got != "" {
		t.Errorf("summary model = %q, want it absent", got)
	}
	if after.Provenance == nil || after.Provenance.MeetingSummary == nil {
		t.Fatal("the file carries no meetingSummary provenance")
	}
	if got := after.Provenance.MeetingSummary.Source; got != "authored" {
		t.Errorf("meetingSummary provenance source = %q, want authored", got)
	}
	if got := after.Provenance.MeetingSummary.Backend; got != "authored" {
		t.Errorf("meetingSummary provenance backend = %q, want authored", got)
	}
	if got := after.Provenance.MeetingSummary.Model; got != "" {
		t.Errorf("meetingSummary provenance model = %q, want it absent", got)
	}
	if after.Integrity != before.Integrity {
		t.Errorf("integrity block changed:\n before %+v\n after  %+v", before.Integrity, after.Integrity)
	}
}

// Replacing a bad generated summary is the whole reason this flag exists, so
// --from-file has to compose with --force.
func TestMeetingsSummarizeFromFileForceReplacesAGeneratedSummary(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	path := packFixtureOpus(t, tmp, "talk")

	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler("# Meeting Summary\n\n## Action items\n\nNone."))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"meetings", "summarize", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("seed summarize failed code=%d stderr=%q", code, stderr.String())
	}

	const authored = "# Building a safer digital world\n\nFrank walks through the app store numbers."
	summaryPath := filepath.Join(tmp, "talk.md")
	if err := os.WriteFile(summaryPath, []byte(authored), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	// Without --force the file is skipped, not silently overwritten.
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{
		"meetings", "summarize", "--from-file", summaryPath, path,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("summarize failed code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "skip (has summary)") {
		t.Errorf("stdout = %q, want a skip line", stdout.String())
	}
	meeting, err := inspectpkg.ExtractMeeting(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if strings.Contains(string(meeting.SummaryMarkdown), "Frank walks through") {
		t.Error("the authored summary replaced the existing one without --force")
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{
		"meetings", "summarize", "--force", "--from-file", summaryPath, path,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("forced summarize failed code=%d stderr=%q", code, stderr.String())
	}
	meeting, err = inspectpkg.ExtractMeeting(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got := string(meeting.SummaryMarkdown); got != authored+"\n" {
		t.Errorf("sealed summary = %q, want the authored one", got)
	}
	// Exactly one summary.md survives: a reader takes the first it finds, so a
	// leftover duplicate would resurrect the replaced text.
	after := decodePortableManifestFromOpus(t, path)
	summaries := 0
	for _, attachment := range after.Attachments {
		if name, _ := attachment["name"].(string); strings.EqualFold(strings.TrimSpace(name), summaryAttachmentName) {
			summaries++
		}
	}
	if summaries != 1 {
		t.Errorf("summary.md attachments = %d, want exactly 1", summaries)
	}
	// The replaced summary must not still be claimed as the model's work.
	if after.Provenance.MeetingSummary.Model != "" {
		t.Errorf("provenance still names model %q after an authored replacement", after.Provenance.MeetingSummary.Model)
	}
}

func TestMeetingsSummarizeFromFileConfigurationErrors(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	pathA := packFixtureOpus(t, tmp, "a")
	pathB := packFixtureOpus(t, tmp, "b")

	empty := filepath.Join(tmp, "empty.md")
	if err := os.WriteFile(empty, []byte("   \n\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	good := filepath.Join(tmp, "good.md")
	if err := os.WriteFile(good, []byte("# Talk\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	for name, args := range map[string][]string{
		// A whitespace-only summary seals as "this meeting has a summary" and
		// shows nothing, which is worse than carrying none.
		"empty summary file": {"--from-file", empty, pathA},
		"missing file":       {"--from-file", filepath.Join(tmp, "nope.md"), pathA},
		// One authored summary cannot describe several different recordings.
		"several inputs": {"--from-file", good, pathA, pathB},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), append([]string{"meetings", "summarize"}, args...), &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr=%q", code, stderr.String())
			}
			// Refused before any file is touched.
			if meeting, err := inspectpkg.ExtractMeeting(pathA); err == nil && len(meeting.SummaryMarkdown) > 0 {
				t.Error("a refused run sealed a summary anyway")
			}
		})
	}
}
