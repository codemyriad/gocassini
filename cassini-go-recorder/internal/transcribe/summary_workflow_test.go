package transcribe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The insight seam splices these two halves itself and hashes the result. If
// the exported bytes ever stopped being the bytes the pipeline sends, an
// insight document's workflow hash would describe a prompt nothing runs.
func TestExportedSummaryPromptIsThePipelinesOwn(t *testing.T) {
	if !strings.Contains(SummaryPromptV0(), summaryTemplatePlaceholder) {
		t.Fatalf("the exported prompt has no %s to splice a template into", summaryTemplatePlaceholder)
	}
	if strings.TrimSpace(SummaryTemplateV0()) == "" {
		t.Fatal("the exported template is empty")
	}
	spliced := strings.Replace(SummaryPromptV0(), summaryTemplatePlaceholder, SummaryTemplateV0(), 1)
	if spliced != summarySystemPrompt(summaryV0Template) {
		t.Error("splicing the exported halves does not reproduce the prompt BuildMeetingSummary sends")
	}
	if SummaryWorkflowID == "" || SummaryWorkflowVersion == "" {
		t.Error("the one shipped workflow has no id or no version")
	}
}

// A caller has to be able to tell the endpoint refusing from the call not
// completing, and matching on message text would break the moment a message
// changed.
func TestChatCompletionReturnsATypedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"no key"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := ChatCompletion(context.Background(), LLMConfig{BaseURL: srv.URL, Model: "m"}, "sys", "usr")
	if err == nil {
		t.Fatal("ChatCompletion succeeded against a 401")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
	if !strings.HasPrefix(err.Error(), "API returned 401: ") {
		t.Errorf("message = %q, want the wording callers already read", err.Error())
	}
}

// The context is honoured, so an insight run that is cancelled stops asking.
func TestChatCompletionHonoursItsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ChatCompletion(ctx, LLMConfig{BaseURL: srv.URL, Model: "m"}, "sys", "usr"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// A reply the model stopped writing at its output limit is a failure, not a
// short answer: `finish_reason: "length"` leaves plausible-looking bytes behind
// with nothing in them saying they end early, so a caller that published them
// would be publishing half a document. The content is discarded with the error.
func TestChatCompletionFailsAReplyCutOffAtTheOutputLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# Summary\n\nThey agreed to"},"finish_reason":"length"}]}`))
	}))
	t.Cleanup(srv.Close)

	body, err := ChatCompletion(context.Background(), LLMConfig{BaseURL: srv.URL, Model: "m", MaxTokens: 64}, "sys", "usr")
	if err == nil {
		t.Fatal("ChatCompletion returned a truncated reply as a success")
	}
	if body != "" {
		t.Errorf("body = %q, want the cut-off content discarded", body)
	}
	var truncated *TruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("error = %v, want a *TruncatedError", err)
	}
	if truncated.MaxTokens != 64 {
		t.Errorf("MaxTokens = %d, want the bound the request carried", truncated.MaxTokens)
	}
	if !strings.Contains(err.Error(), "cut off at the model's output limit") {
		t.Errorf("message = %q, want it to say the answer was cut off", err.Error())
	}

	// And the summary step, which is the other caller, does not write it down.
	if _, err := BuildMeetingSummary(LLMConfig{BaseURL: srv.URL, Model: "m"}, sampleStreams(), sampleSegments()); !errors.As(err, &truncated) {
		t.Fatalf("BuildMeetingSummary error = %v, want the truncation to fail the summary", err)
	}
}

// Every other finish reason, and a reply that carries none at all (older
// servers), is the answer.
func TestChatCompletionAcceptsAFinishedReply(t *testing.T) {
	for _, finish := range []string{`,"finish_reason":"stop"`, ``} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}` + finish + `}]}`))
		}))
		body, err := ChatCompletion(context.Background(), LLMConfig{BaseURL: srv.URL, Model: "m"}, "sys", "usr")
		srv.Close()
		if err != nil || body != "done" {
			t.Fatalf("finish %q: body=%q err=%v, want the reply", finish, body, err)
		}
	}
}
