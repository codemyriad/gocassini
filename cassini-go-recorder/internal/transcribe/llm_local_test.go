package transcribe

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSummaryBackendRouting(t *testing.T) {
	for _, name := range []string{"CASSINI_SUMMARY_BACKEND", "CASSINI_LLM_SERVER", "CASSINI_LLM_DEVICE", "CASSINI_LLM_CONTEXT_SIZE", "CASSINI_LLM_TIMEOUT_SEC", "CASSINI_LLM_CUDA_CAPABLE", "CASSINI_SUMMARY_DISABLED", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "LLM_BASE_URL", "LLM_MODEL", "SUMMARY_MODEL"} {
		t.Setenv(name, "")
	}
	if cfg := DefaultBuildConfig().SummaryLLM; cfg.IsConfigured() {
		t.Fatal("bare CLI enabled summaries")
	}
	t.Setenv("CASSINI_LLM_SERVER", "/bundled/llama-server")
	if DefaultBuildConfig().SummaryLLM.IsConfigured() {
		t.Fatal("bundling enabled pilot without opt-in")
	}
	t.Setenv("CASSINI_SUMMARY_BACKEND", "auto")
	if cfg := DefaultBuildConfig().SummaryLLM; cfg.Backend != "local" || cfg.Model != bundledSummarySpec().ID {
		t.Fatalf("image defaults: %+v", cfg)
	}
	t.Setenv("OPENROUTER_API_KEY", "remote-secret")
	t.Setenv("LLM_BASE_URL", "https://secondary.invalid/v1")
	t.Setenv("OPENROUTER_BASE_URL", "https://primary.invalid/v1")
	if cfg := DefaultBuildConfig().SummaryLLM; cfg.Backend != "remote" || cfg.BaseURL != "https://primary.invalid/v1" {
		t.Fatalf("legacy routing: %+v", cfg)
	}
	t.Setenv("CASSINI_SUMMARY_BACKEND", "local")
	if cfg := DefaultBuildConfig().SummaryLLM; cfg.BaseURL != "" || cfg.APIKey != "" || cfg.Backend != "local" {
		t.Fatalf("local inherited remote config: %+v", cfg)
	}
	t.Setenv("CASSINI_SUMMARY_DISABLED", "1")
	if DefaultBuildConfig().SummaryLLM.IsConfigured() {
		t.Fatal("disable did not override local")
	}
}

func TestSummaryCgroupMemoryBudget(t *testing.T) {
	for _, tc := range []struct {
		limit, current, stat string
		want                 int
		ok                   bool
	}{
		{"max", "100", "", 0, false},
		{"1073741824", "805306368", "inactive_file 268435456\n", 512, true},
		{"1073741824", "2147483648", "", 0, true},
		{"1073741824", "805306368", "inactive_file 999999999999\n", 1024, true},
	} {
		got, ok := summaryCgroupFreeMB(tc.limit, tc.current, tc.stat)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("cgroup budget: %d %v, want %d %v", got, ok, tc.want, tc.ok)
		}
	}
}

func TestSummaryModelIntegrityAndOffline(t *testing.T) {
	data := []byte("synthetic model bytes")
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.Write(data) }))
	defer srv.Close()
	spec := summaryModelSpec{ID: "test-ling", File: "model.gguf", URL: srv.URL, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
	t.Setenv(envBundledModelRoot, "")
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "")
	cache := t.TempDir()
	path, err := ensureSummaryModelSpec(cache, spec, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureSummaryModelSpec(cache, spec, io.Discard); err != nil || requests != 1 {
		t.Fatalf("cache refetched: requests=%d err=%v", requests, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", len(data))), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")
	if _, err := ensureSummaryModelSpec(cache, spec, io.Discard); err == nil {
		t.Fatal("corrupt offline model accepted")
	}
	t.Setenv(envBundledModelRoot, t.TempDir())
	t.Setenv("CASSINI_BUNDLED_SUMMARY_MODEL", spec.ID)
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "")
	if _, err := ensureSummaryModelSpec(cache, spec, io.Discard); err == nil || !strings.Contains(err.Error(), "rebuild image") {
		t.Fatalf("broken bundle silently repaired: %v", err)
	}
	if requests != 1 {
		t.Fatal("broken bundle triggered download")
	}
}

func TestSummaryModelRejectsBadDownload(t *testing.T) {
	t.Setenv(envBundledModelRoot, "")
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "bad") }))
	defer srv.Close()
	cache := t.TempDir()
	spec := summaryModelSpec{ID: "test-ling", File: "model.gguf", URL: srv.URL, Size: 3, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("yes")))}
	if _, err := ensureSummaryModelSpec(cache, spec, io.Discard); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, err := os.Stat(filepath.Join(cache, "models", spec.ID, spec.File)); !os.IsNotExist(err) {
		t.Fatal("invalid model promoted")
	}
}

// Re-executed through a shell wrapper, as an actual managed child process.
func TestSummaryRuntimeHelper(t *testing.T) {
	args := os.Args
	value := func(flag string) string {
		for i, v := range args {
			if v == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	mode := value("--model")
	if mode == "" {
		return
	}
	if mode == "exit" {
		fmt.Fprintln(os.Stderr, "synthetic startup failure")
		os.Exit(2)
	}
	if os.Getenv("OPENROUTER_API_KEY") != "" || os.Getenv("LLAMA_ARG_HOST") != "" {
		os.Exit(3)
	}
	port, key := value("--port"), value("--api-key")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/health":
			if mode == "hang" {
				w.WriteHeader(503)
				return
			}
			io.WriteString(w, `{"status":"ok"}`)
		case "/apply-template":
			io.WriteString(w, `{"prompt":"templated conversation"}`)
		case "/tokenize":
			n := 20
			if mode == "overflow" {
				n = 9000
			}
			json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, n)})
		default:
			w.WriteHeader(404)
		}
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func helperSummaryConfig(t *testing.T) LLMConfig {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server")
	// Go's executable temp paths contain no single quotes on supported CI hosts.
	quoted := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "'"
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec "+quoted+" -test.run=^TestSummaryRuntimeHelper$ -- \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return LLMConfig{Backend: "local", ServerPath: path, Model: "test", Device: "cpu", ContextSize: 8192, TimeoutSec: 5}
}

func TestSummaryRuntimeLifecycleAndBudget(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "must-not-reach-child")
	t.Setenv("LLAMA_ARG_HOST", "0.0.0.0")
	for _, mode := range []string{"ok", "overflow", "exit", "hang"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			s, err := startSummaryServer(ctx, helperSummaryConfig(t), mode)
			if mode == "exit" || mode == "hang" {
				if err == nil {
					s.close()
					t.Fatal("unready child accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = s.checkBudget(ctx, "system", "transcript", 8192)
			s.close()
			if s.cmd.ProcessState == nil {
				t.Fatal("child was not reaped")
			}
			if (err != nil) != (mode == "overflow") {
				t.Fatalf("budget: %v", err)
			}
		})
	}
}

func TestSummaryCompletionRejectsTruncation(t *testing.T) {
	for _, reason := range []string{"stop", "length", "content_filter", ""} {
		t.Run(strconv.Quote(reason), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				json.NewDecoder(r.Body).Decode(&req)
				if req["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
					t.Error("thinking enabled")
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": reason, "message": map[string]string{"content": "summary"}}}})
			}))
			defer srv.Close()
			_, err := chatCompletion(LLMConfig{Backend: "local", BaseURL: srv.URL, TimeoutSec: 1}, "system", "user")
			if (err == nil) != (reason == "stop") {
				t.Fatalf("finish reason %q: %v", reason, err)
			}
		})
	}
}

func TestLocalSummaryValidation(t *testing.T) {
	valid := "# Meeting Summary\n\n## Overview\n\nNone.\n\n## Key Points\n\nNone.\n\n## Decisions\n\nNone.\n\n## Action Items\n\nNone.\n\n## Open Questions\n\nNone.\n\n## Next Step\n\nNone."
	for _, s := range []string{valid, "preamble\n" + valid, strings.Replace(valid, "## Decisions", "## Decision", 1), valid + "\n<think>reasoning</think>", strings.TrimSuffix(valid, "None.")} {
		err := validateLocalSummary(s)
		if (err == nil) != (s == valid) {
			t.Fatalf("validation: %v", err)
		}
	}
}

// Opt-in real-runtime smoke, with a caller-selected public GGUF already on disk.
func TestBundledLingIntegration(t *testing.T) {
	server, model := os.Getenv("CASSINI_TEST_LING_SERVER"), os.Getenv("CASSINI_TEST_LING_MODEL")
	if server == "" || model == "" {
		t.Skip("set CASSINI_TEST_LING_SERVER and CASSINI_TEST_LING_MODEL for real inference")
	}
	spec := bundledSummarySpec()
	bundle := t.TempDir()
	dir := filepath.Join(bundle, "models", spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(model, filepath.Join(dir, spec.File)); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envBundledModelRoot, bundle)
	t.Setenv("CASSINI_BUNDLED_SUMMARY_MODEL", spec.ID)
	t.Setenv("CASSINI_DISALLOW_MODEL_DOWNLOAD", "1")
	t.Setenv("CASSINI_SUMMARY_BACKEND", "local")
	t.Setenv("CASSINI_SUMMARY_DISABLED", "")
	t.Setenv("CASSINI_LLM_SERVER", server)
	device := os.Getenv("CASSINI_TEST_LING_DEVICE")
	if device == "" {
		device = "cpu"
	}
	t.Setenv("CASSINI_LLM_DEVICE", device)
	t.Setenv("CASSINI_LLM_CONTEXT_SIZE", "16384")
	t.Setenv("CASSINI_LLM_TIMEOUT_SEC", "900")
	t.Setenv("OPENROUTER_API_KEY", "ignored-remote-key")
	t.Setenv("OPENROUTER_BASE_URL", "http://127.0.0.1:1/must-not-be-used")
	cfg := DefaultBuildConfig().SummaryLLM
	cfg.CacheDir = t.TempDir()
	segments := []Segment{
		{SpeakerID: "Alice", Text: "The September 10 launch is cancelled. We decided on September 24, 2026 and chose PostgreSQL. Redis was only an alternative."},
		{SpeakerID: "Bob", Text: "I will send the migration checklist by September 8, 2026."},
		{SpeakerID: "Alice", Text: "We need a security review but nobody has volunteered and no deadline is agreed. The budget is still undecided."},
	}
	started := time.Now()
	body, err := BuildMeetingSummary(cfg, nil, segments)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("local %s summary completed in %s", device, time.Since(started))
	if !strings.Contains(body, "September 24") || !strings.Contains(body, "PostgreSQL") {
		t.Fatalf("lost final decision: %s", body)
	}
	if strings.Contains(body, "- [ ] Alice -") {
		t.Fatalf("invented an owner: %s", body)
	}
	if output := os.Getenv("CASSINI_TEST_LING_OUTPUT"); output != "" {
		if err := os.WriteFile(output, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err = BuildMeetingSummary(cfg, nil, []Segment{{SpeakerID: "Alice", Text: strings.Repeat("This is synthetic context overflow data. ", 6000)}})
	if err == nil || !strings.Contains(err.Error(), "reserved for output") {
		t.Fatalf("overflow was not rejected before generation: %v", err)
	}
}
