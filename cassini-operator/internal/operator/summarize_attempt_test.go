package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSummarizeCLI stands in for `cassini meetings summarize`. Every call is
// appended to "$0.calls", and the SUMMARY_MODEL it was given to "$0.models".
// --estimate prints a two-word estimate per bundle; otherwise it writes
// summary.md and names it in the bundle's manifest, as the real command does,
// or, with "$0.llm-fails" present, reports the failure the real one would.
func fakeSummarizeCLI(t *testing.T) string {
	t.Helper()
	return writeFakeCassini(t, `echo "$*" >> "$0.calls"
[ "$1 $2" = "meetings summarize" ] || { echo "unexpected command: $*" >&2; exit 1; }
shift 2
estimate=""
bundles=""
for a in "$@"; do
  case "$a" in
    --estimate) estimate=1 ;;
    --*) ;;
    *) bundles="$bundles $a" ;;
  esac
done
sep=""
printf '['
for b in $bundles; do
  if [ -n "$estimate" ]; then
    printf '%s{"path":"%s","status":"estimated","words":2,"estimatedInputTokens":900}' "$sep" "$b"
  elif [ -f "$0.llm-fails" ]; then
    printf '%s{"path":"%s","status":"failed","error":"generate summary: upstream said 502"}]\n' "$sep" "$b"; exit 1
  else
    echo "${SUMMARY_MODEL:-unset}" >> "$0.models"
    printf '# Summary\n\nWritten.\n' > "$b/summary.md"
    printf '{"version":"cassini.meeting-artifact.v1","files":{"summary":"summary.md"}}' > "$b/manifest.json"
    printf '%s{"path":"%s","status":"summarized","model":"%s","words":2}' "$sep" "$b" "$SUMMARY_MODEL"
  fi
  sep=","
done
printf ']\n'
`)
}

// newSummarizeRuntime is newTestRuntime with the attempt seam wired the way
// run.go wires it, the stand-in CLI, a summary endpoint and one published
// job. A summarize must never run the ASR build.
func newSummarizeRuntime(t *testing.T, jobID string) (*Runtime, string) {
	t.Helper()
	rt, cleanup := newTestRuntime(t)
	t.Cleanup(cleanup)
	bin := fakeSummarizeCLI(t)
	rt.cfg.CassiniBin = bin
	rt.buildJobFn = rt.withSpeakerEdits(func(ctx context.Context, task buildTask) (string, error) {
		t.Errorf("attempt %d ran an ASR build", task.AttemptNumber)
		return "", errors.New("unexpected build")
	})
	rt.setLLMSettings(LLMSettings{
		Providers: []LLMProvider{{ID: "p1", Name: "Hosted", BaseURL: "https://llm.example.test/v1", Model: "summary-model"}},
		Summary:   LLMStep{Enabled: true, Provider: "p1"},
	})
	seedSpeakerJob(t, rt.store, rt.cfg.WorkRoot, jobID)
	return rt, bin
}

func callJobSummary(t *testing.T, rt *Runtime, jobID, method, body string) (int, map[string]any) {
	t.Helper()
	// The worker may still hold the job's artifacts after recording a state.
	rt.store.lockArtifacts(jobID)()
	req := httptest.NewRequest(method, "/jobs/"+jobID+"/summary", strings.NewReader(body))
	rec := httptest.NewRecorder()
	rt.jobDetailHandler(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s summary: %d %s", method, rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

func TestSummarizeAttemptSaysWhatItCostsThenWritesAndPublishes(t *testing.T) {
	rt, bin := newSummarizeRuntime(t, "JOB1")
	webmBefore := annTestRead(t, filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1"), "meeting.webm"))

	code, plan := callJobSummary(t, rt, "JOB1", http.MethodGet, "")
	if code != http.StatusOK || plan["available"] != true || plan["has_summary"] != false ||
		plan["calls"] != float64(1) || plan["words"] != float64(2) || plan["estimated_input_tokens"] != float64(900) ||
		plan["provider"] != "Hosted" || plan["model"] != "summary-model" || plan["external"] != true {
		t.Fatalf("plan = %d %v", code, plan)
	}
	if calls := speakersCalls(t, bin, "meetings summarize --estimate"); len(calls) != 1 {
		t.Fatalf("estimate calls = %v", calls)
	}

	if code, out := callJobSummary(t, rt, "JOB1", http.MethodPost, `{}`); code != http.StatusAccepted || out["attempt_number"] != float64(2) {
		t.Fatalf("queue = %d %v", code, out)
	}
	job := waitForJobState(t, rt.store, "JOB1", "succeeded")
	if job.CurrentAttemptNumber != 2 {
		t.Fatalf("current attempt = %d, want the summarize (2)", job.CurrentAttemptNumber)
	}
	waitForPublishedAttempt(t, rt, "JOB1", 2)

	canonical := canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1")
	if got := annTestRead(t, filepath.Join(canonical, "summary.md")); got != "# Summary\n\nWritten.\n" {
		t.Fatalf("published summary = %q", got)
	}
	if annTestRead(t, filepath.Join(canonical, "meeting.webm")) != webmBefore {
		t.Fatal("a summarize changed the audio")
	}
	writes := speakersCalls(t, bin, "meetings summarize --force --json")
	if len(writes) != 1 || !strings.HasSuffix(writes[0], attemptMeetingPath(rt.cfg.WorkRoot, "JOB1", 2)) {
		t.Fatalf("summarize calls = %v, want one on the attempt's copy", writes)
	}
	// The model call got the summary endpoint the settings name.
	if models := strings.TrimSpace(annTestRead(t, bin+".models")); models != "summary-model" {
		t.Fatalf("summary model passed = %q", models)
	}
	attempt, err := rt.store.GetJobAttempt(context.Background(), "JOB1", 2)
	if err != nil || attempt.TriggerKind != triggerKindSummarize || attempt.SummaryStatus != "completed" {
		t.Fatalf("attempt = %+v %v", attempt, err)
	}

	// It has one now: replacing it has to be asked for.
	_, plan = callJobSummary(t, rt, "JOB1", http.MethodGet, "")
	if plan["has_summary"] != true {
		t.Fatalf("plan after = %v", plan)
	}
	if code, out := callJobSummary(t, rt, "JOB1", http.MethodPost, `{}`); code != http.StatusConflict || out["error"] != "has_summary" {
		t.Fatalf("unasked replace = %d %v", code, out)
	}
	if code, _ := callJobSummary(t, rt, "JOB1", http.MethodPost, `{"replace":true}`); code != http.StatusAccepted {
		t.Fatalf("replace = %d", code)
	}
	waitForPublishedAttempt(t, rt, "JOB1", 3)
}

func TestSummarizeAttemptFailureLeavesThePublishedMeeting(t *testing.T) {
	rt, bin := newSummarizeRuntime(t, "JOB1")
	if err := os.WriteFile(bin+".llm-fails", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := callJobSummary(t, rt, "JOB1", http.MethodPost, `{}`); code != http.StatusAccepted {
		t.Fatalf("queue = %d", code)
	}
	waitForJobState(t, rt.store, "JOB1", "failed")
	attempt, err := rt.store.GetJobAttempt(context.Background(), "JOB1", 2)
	if err != nil || attempt.Error == nil || !strings.Contains(*attempt.Error, "upstream said 502") {
		t.Fatalf("attempt error = %+v %v, want the model's failure", attempt.Error, err)
	}
	if _, err := os.Stat(filepath.Join(canonicalMeetingPath(rt.cfg.WorkRoot, "JOB1"), "summary.md")); !os.IsNotExist(err) {
		t.Fatalf("published meeting changed by a failed summarize: %v", err)
	}
}

func TestSummaryUnavailableSaysWhy(t *testing.T) {
	rt, _ := newSummarizeRuntime(t, "JOB1")
	rt.setLLMSettings(LLMSettings{Providers: []LLMProvider{{ID: "p1", Name: "Local", BaseURL: "http://127.0.0.1:8080/v1"}}})
	code, plan := callJobSummary(t, rt, "JOB1", http.MethodGet, "")
	if code != http.StatusOK || plan["available"] != false || plan["reason"] != summaryUnavailableOff || plan["words"] != float64(2) {
		t.Fatalf("plan with summaries off = %d %v", code, plan)
	}
	if code, out := callJobSummary(t, rt, "JOB1", http.MethodPost, `{}`); code != http.StatusConflict || out["reason"] != summaryUnavailableOff {
		t.Fatalf("queue with summaries off = %d %v", code, out)
	}
}

func TestMissingSummariesListsWhatTheBackfillWouldWrite(t *testing.T) {
	rt, _ := newSummarizeRuntime(t, "JOB1")
	seedSpeakerJob(t, rt.store, rt.cfg.WorkRoot, "JOB2")
	// JOB2 already has one.
	writeArtifactManifest(t, canonicalMeetingPath(rt.cfg.WorkRoot, "JOB2"), func(m map[string]any) {
		m["processing"] = map[string]any{"meetingSummary": map[string]any{"status": "completed"}}
	})

	rec := httptest.NewRecorder()
	rt.missingSummariesHandler(rec, httptest.NewRequest(http.MethodGet, "/summaries/missing", nil))
	var out struct {
		Meetings                  []missingSummary `json:"meetings"`
		Calls                     int              `json:"calls"`
		TotalEstimatedInputTokens int              `json:"total_estimated_input_tokens"`
		SummariesOn               bool             `json:"summaries_on"`
		Model                     string           `json:"model"`
		External                  bool             `json:"external"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v %s", err, rec.Body.String())
	}
	if len(out.Meetings) != 1 || out.Meetings[0].JobID != "JOB1" || out.Calls != 1 || out.TotalEstimatedInputTokens != 900 ||
		!out.SummariesOn || out.Model != "summary-model" || !out.External {
		t.Fatalf("missing = %+v", out)
	}
}

func TestLocalEndpoint(t *testing.T) {
	for url, want := range map[string]bool{
		"http://127.0.0.1:11434/v1":    true,
		"http://localhost:8080/v1":     true,
		"http://192.168.1.222:8000/v1": true,
		"http://10.0.0.5/v1":           true,
		"http://ollama:11434/v1":       true,
		"http://llm.lan.local/v1":      true,
		"https://openrouter.ai/api/v1": false,
		"https://api.example.com/v1":   false,
		"http://[::1]:8080/v1":         true,
		"":                             false,
	} {
		if got := localEndpoint(url); got != want {
			t.Errorf("localEndpoint(%q) = %v, want %v", url, got, want)
		}
	}
}
