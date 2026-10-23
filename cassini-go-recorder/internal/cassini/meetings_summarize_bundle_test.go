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

	inspectpkg "gocassini/internal/inspect"
	"gocassini/internal/portable"
)

// summarizableBundle writes a ready meeting bundle and, when processing is
// given, records it in the bundle's artifact manifest as a build would.
func summarizableBundle(t *testing.T, dir string, processing map[string]any) string {
	t.Helper()
	bundleDir := filepath.Join(dir, "meeting.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if processing != nil {
		manifestPath := filepath.Join(bundleDir, "manifest.json")
		manifest, err := parseOrderedJSONObject(readFileBytes(t, manifestPath))
		if err != nil {
			t.Fatalf("parse artifact manifest: %v", err)
		}
		manifest.set("processing", mustJSON(processing))
		if err := writeOrderedJSON(manifestPath, manifest); err != nil {
			t.Fatalf("write artifact manifest: %v", err)
		}
	}
	return bundleDir
}

func runSummarize(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), append([]string{"meetings", "summarize"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func readArtifactManifest(t *testing.T, bundleDir string) map[string]any {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal(readFileBytes(t, filepath.Join(bundleDir, "manifest.json")), &manifest); err != nil {
		t.Fatalf("parse artifact manifest: %v", err)
	}
	return manifest
}

// The summary goes into the bundle the way a build with summaries on writes
// it, so sealing the bundle again carries it into the meeting file.
func TestMeetingsSummarizeWritesASummaryIntoABundle(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := summarizableBundle(t, tmp, map[string]any{
		"transcription":  map[string]any{"status": "completed", "model": "test-stt"},
		"meetingSummary": map[string]any{"status": "failed", "reason": "llm_failed"},
	})
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler("# Meeting Summary\n\nRecovered for the bundle."))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	code, stdout, stderr := runSummarize(t, bundleDir)
	if code != 0 {
		t.Fatalf("summarize failed code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, bundleDir+": summarized") {
		t.Errorf("stdout = %q, want a summarized status line", stdout)
	}
	if requests, _, _ := stub.snapshot(); requests != 1 {
		t.Errorf("LLM requests = %d, want 1", requests)
	}
	const want = "# Meeting Summary\n\nRecovered for the bundle.\n"
	if got := string(readFileBytes(t, filepath.Join(bundleDir, "summary.md"))); got != want {
		t.Errorf("summary.md = %q, want %q", got, want)
	}

	manifest := readArtifactManifest(t, bundleDir)
	files, _ := manifest["files"].(map[string]any)
	if files["summary"] != "summary.md" || files["audio"] != "meeting.webm" {
		t.Errorf("files = %v, want summary named and the rest kept", files)
	}
	// Members this command does not know about are kept as they were.
	if manifest["generatedAt"] != "2026-03-11T10:00:00Z" || manifest["wordCount"] != float64(2) {
		t.Errorf("unrelated manifest members changed: %v", manifest)
	}
	provenance, _ := manifest["provenance"].(map[string]any)
	step, _ := provenance["meetingSummary"].(map[string]any)
	if step["source"] != "backfill" || step["model"] != "test-summary-model" || step["backend"] != "openai-compatible" {
		t.Errorf("provenance.meetingSummary = %v", step)
	}
	processing, _ := manifest["processing"].(map[string]any)
	if transcription, _ := processing["transcription"].(map[string]any); transcription["status"] != "completed" {
		t.Errorf("transcription outcome changed: %v", processing)
	}
	if outcome, _ := processing["meetingSummary"].(map[string]any); outcome["status"] != "completed" || outcome["model"] != "test-summary-model" || outcome["reason"] != nil {
		t.Errorf("processing.meetingSummary = %v, want completed by test-summary-model", outcome)
	}

	// Sealed again, the meeting file carries the summary and its outcome.
	opusPath := filepath.Join(tmp, "meeting.opus")
	var packOut, packErr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", opusPath}, &packOut, &packErr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, packErr.String())
	}
	meeting, err := inspectpkg.ExtractMeeting(opusPath)
	if err != nil {
		t.Fatalf("read packed meeting: %v", err)
	}
	if string(meeting.SummaryMarkdown) != want || meeting.SummaryModel() != "test-summary-model" {
		t.Errorf("packed summary = %q by %q", meeting.SummaryMarkdown, meeting.SummaryModel())
	}
	sealed := decodePortableManifestFromOpus(t, opusPath)
	if sealed.Processing == nil || sealed.Processing.MeetingSummary == nil ||
		*sealed.Processing.MeetingSummary != (portable.SummaryStatus{Status: "completed", Model: "test-summary-model"}) {
		t.Errorf("packed processing = %+v", sealed.Processing)
	}
}

func TestMeetingsSummarizeSkipsABundleThatHasASummaryUnlessForced(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := summarizableBundle(t, t.TempDir(), nil)
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler("# Meeting Summary\n\nOne."))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	if code, _, stderr := runSummarize(t, bundleDir); code != 0 {
		t.Fatalf("first summarize failed: %s", stderr)
	}
	code, stdout, _ := runSummarize(t, bundleDir)
	if code != 0 || !strings.Contains(stdout, bundleDir+": skip (has summary)") {
		t.Fatalf("second summarize: code=%d stdout=%q", code, stdout)
	}
	if requests, _, _ := stub.snapshot(); requests != 1 {
		t.Fatalf("a skipped bundle called the model: %d requests", requests)
	}
	if code, _, stderr := runSummarize(t, "--force", bundleDir); code != 0 {
		t.Fatalf("forced summarize failed: %s", stderr)
	}
	if requests, _, _ := stub.snapshot(); requests != 2 {
		t.Fatalf("--force did not call the model: %d requests", requests)
	}
	// A bundle built before processing was recorded gets none invented.
	if _, ok := readArtifactManifest(t, bundleDir)["processing"]; ok {
		t.Error("summarize invented a processing block")
	}
}

// A meeting with no words has nothing to summarize: it is skipped, nothing is
// sent and nothing is written.
func TestMeetingsSummarizeSkipsAnUntranscribedBundle(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := summarizableBundle(t, t.TempDir(), map[string]any{
		"transcription": map[string]any{"status": "failed", "reason": "transcription_failed"},
	})
	before := readFileBytes(t, filepath.Join(bundleDir, "manifest.json"))
	stub := &stubSummaryLLM{}
	server := httptest.NewServer(stub.handler("unused"))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	code, stdout, _ := runSummarize(t, bundleDir)
	if code != 0 || !strings.Contains(stdout, bundleDir+": skip (no transcript)") {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
	if requests, _, _ := stub.snapshot(); requests != 0 {
		t.Fatalf("an untranscribed bundle called the model: %d requests", requests)
	}
	if !bytes.Equal(before, readFileBytes(t, filepath.Join(bundleDir, "manifest.json"))) {
		t.Error("manifest changed for a skipped bundle")
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "summary.md")); !os.IsNotExist(err) {
		t.Errorf("summary.md written for a skipped bundle: %v", err)
	}
}

func TestMeetingsSummarizeFailedCallLeavesTheBundleAsItWas(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := summarizableBundle(t, t.TempDir(), nil)
	before := readFileBytes(t, filepath.Join(bundleDir, "manifest.json"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()
	summaryLLMEnv(t, server.URL)

	code, stdout, _ := runSummarize(t, bundleDir)
	if code != 1 || !strings.Contains(stdout, bundleDir+": failed:") {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
	if !bytes.Equal(before, readFileBytes(t, filepath.Join(bundleDir, "manifest.json"))) {
		t.Error("manifest changed after a failed call")
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "summary.md")); !os.IsNotExist(err) {
		t.Errorf("summary.md written after a failed call: %v", err)
	}
}

// --estimate sizes the call for a bundle and a sealed file alike, sends
// nothing and needs no endpoint, so a cost can be shown before anything runs.
func TestMeetingsSummarizeEstimateCallsNoModel(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	bundleDir := summarizableBundle(t, tmp, nil)
	opusPath := packFixtureOpus(t, tmp, "sealed")
	before := readFileBytes(t, filepath.Join(bundleDir, "manifest.json"))
	summaryLLMEnv(t, "")

	code, stdout, stderr := runSummarize(t, "--estimate", "--json", bundleDir, opusPath)
	if code != 0 {
		t.Fatalf("estimate failed code=%d stderr=%q", code, stderr)
	}
	var reports []summarizeReport
	if err := json.Unmarshal([]byte(stdout), &reports); err != nil {
		t.Fatalf("parse --json output %q: %v", stdout, err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %+v, want one per input", reports)
	}
	for _, report := range reports {
		if report.Status != "estimated" || report.Words != 2 || report.EstimatedInputTokens <= 0 {
			t.Errorf("report = %+v, want an estimate of the two-word transcript", report)
		}
	}
	if !bytes.Equal(before, readFileBytes(t, filepath.Join(bundleDir, "manifest.json"))) {
		t.Error("an estimate changed the bundle")
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "summary.md")); !os.IsNotExist(err) {
		t.Errorf("an estimate wrote summary.md: %v", err)
	}
}

func TestMeetingsSummarizeRefusesOutForABundle(t *testing.T) {
	requireFFMediaTools(t)
	bundleDir := summarizableBundle(t, t.TempDir(), nil)
	summaryLLMEnv(t, "http://127.0.0.1:1")
	code, _, stderr := runSummarize(t, "--out", filepath.Join(t.TempDir(), "x.opus"), bundleDir)
	if code != 2 || !strings.Contains(stderr, "a meeting bundle is summarized in place") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}
