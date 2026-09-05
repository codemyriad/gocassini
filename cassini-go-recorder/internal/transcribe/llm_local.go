package transcribe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func resolveSummaryBackend(cfg LLMConfig) LLMConfig {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("CASSINI_SUMMARY_BACKEND")))
	server := strings.TrimSpace(os.Getenv("CASSINI_LLM_SERVER"))
	if backend == "" {
		backend = "remote"
	}
	if backend == "auto" {
		// Explicit remote credentials retain their historical precedence. A bare
		// CLI remains opt-in; images advertise their bundled server.
		backend = "remote"
		if cfg.APIKey == "" && server != "" {
			backend = "local"
		}
	}
	cfg.Backend = backend
	if backend != "local" {
		return cfg
	}
	cfg.APIKey, cfg.BaseURL = "", "" // local mode cannot inherit a remote destination
	cfg.Model = bundledSummarySpec().ID
	cfg.ServerPath = server
	if server == "" {
		cfg.ServerPath = "llama-server"
	}
	cfg.ContextSize = 16384
	if v := os.Getenv("CASSINI_LLM_CONTEXT_SIZE"); v != "" {
		cfg.ContextSize, _ = strconv.Atoi(v) // invalid values fail before launch
	}
	cfg.TimeoutSec = 900
	if v := os.Getenv("CASSINI_LLM_TIMEOUT_SEC"); v != "" {
		cfg.TimeoutSec, _ = strconv.Atoi(v)
	}
	cfg.Device = strings.ToLower(strings.TrimSpace(os.Getenv("CASSINI_LLM_DEVICE")))
	if cfg.Device == "" || cfg.Device == "auto" {
		cfg.Device = "cpu"
		if envBool("CASSINI_LLM_CUDA_CAPABLE") && hasNVIDIAGPU() {
			cfg.Device = "cuda"
		}
	}
	return cfg
}

type summaryServer struct {
	url    string
	key    string
	cmd    *exec.Cmd
	done   chan error
	cancel context.CancelFunc
	logs   boundedSummaryLog
	client *http.Client
}

type boundedSummaryLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedSummaryLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 8192 {
		b.data = append([]byte(nil), b.data[len(b.data)-8192:]...)
	}
	return len(p), nil
}
func (b *boundedSummaryLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

func startSummaryServer(ctx context.Context, cfg LLMConfig, model string) (*summaryServer, error) {
	if cfg.Device != "cpu" && cfg.Device != "cuda" {
		return nil, fmt.Errorf("invalid local LLM device %q (use cpu, cuda, auto)", cfg.Device)
	}
	if cfg.ContextSize < 8192 || cfg.ContextSize > 131072 {
		return nil, fmt.Errorf("local LLM context must be between 8192 and 131072 tokens")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	s := &summaryServer{url: fmt.Sprintf("http://127.0.0.1:%d", port), key: hex.EncodeToString(key), cancel: cancel, done: make(chan error, 1)}
	layers := "0"
	if cfg.Device == "cuda" {
		layers = "999"
	}
	s.cmd = exec.CommandContext(childCtx, cfg.ServerPath,
		"--model", model, "--alias", cfg.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--ctx-size", strconv.Itoa(cfg.ContextSize), "--parallel", "1", "--n-gpu-layers", layers,
		"--jinja", "--chat-template-kwargs", `{"enable_thinking":false}`, "--reasoning-format", "deepseek",
		"--no-context-shift", "--no-webui", "--api-key", s.key)
	if cfg.Device == "cuda" {
		s.cmd.Args = append(s.cmd.Args, "--device", "CUDA0")
	}
	setSummaryProcessAttributes(s.cmd)
	// Do not pass remote credentials, proxy settings or LLAMA_ARG_* overrides
	// into a process whose sole role is local inference.
	for _, name := range []string{"PATH", "LD_LIBRARY_PATH", "HOME", "TMPDIR", "CUDA_VISIBLE_DEVICES"} {
		if v, ok := os.LookupEnv(name); ok {
			s.cmd.Env = append(s.cmd.Env, name+"="+v)
		}
	}
	s.cmd.Stdout, s.cmd.Stderr = &s.logs, &s.logs
	s.cmd.WaitDelay = 3 * time.Second
	if err := s.cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start bundled summary runtime: %w", err)
	}
	go func() { s.done <- s.cmd.Wait() }()
	s.client = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("local runtime redirected request") }}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-s.done:
			cancel()
			s.client.CloseIdleConnections()
			return nil, fmt.Errorf("summary runtime exited before readiness (%v): %s", err, s.logs.String())
		case <-ctx.Done():
			s.close()
			return nil, fmt.Errorf("summary runtime readiness: %w", ctx.Err())
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, "GET", s.url+"/health", nil)
			req.Header.Set("Authorization", "Bearer "+s.key)
			resp, err := s.client.Do(req)
			if err == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return s, nil
				}
			}
		}
	}
}

func (s *summaryServer) close() {
	s.cancel()
	<-s.done
	s.client.CloseIdleConnections()
}

func (s *summaryServer) post(ctx context.Context, path string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.url+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("summary runtime %s returned HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(result)
}

func (s *summaryServer) checkBudget(ctx context.Context, system, user string, limit int) error {
	var template struct {
		Prompt string `json:"prompt"`
	}
	if err := s.post(ctx, "/apply-template", map[string]any{"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}, "chat_template_kwargs": map[string]bool{"enable_thinking": false}}, &template); err != nil {
		return fmt.Errorf("apply summary template: %w", err)
	}
	if template.Prompt == "" {
		return fmt.Errorf("summary runtime returned an empty chat template")
	}
	var tokens struct {
		Tokens []int `json:"tokens"`
	}
	if err := s.post(ctx, "/tokenize", map[string]any{"content": template.Prompt, "add_special": true, "parse_special": true}, &tokens); err != nil {
		return fmt.Errorf("count summary tokens: %w", err)
	}
	// Reserve all 4096 output tokens and a small template margin. Never shift
	// away the beginning of a meeting or publish a partial summary.
	if len(tokens.Tokens) == 0 || len(tokens.Tokens)+4096+64 > limit {
		return fmt.Errorf("summary input has %d tokens; context %d requires 4160 tokens reserved for output/template (chunking is not yet supported)", len(tokens.Tokens), limit)
	}
	return nil
}

func localMeetingSummary(cfg LLMConfig, system, user string, progress io.Writer) (string, error) {
	if cfg.TimeoutSec <= 0 {
		return "", fmt.Errorf("local LLM timeout must be positive")
	}
	if cfg.Device != "cpu" && cfg.Device != "cuda" {
		return "", fmt.Errorf("invalid local LLM device %q", cfg.Device)
	}
	if cfg.ContextSize < 8192 || cfg.ContextSize > 131072 {
		return "", fmt.Errorf("local LLM context must be between 8192 and 131072 tokens")
	}
	if _, err := exec.LookPath(cfg.ServerPath); err != nil {
		return "", fmt.Errorf("local summary runtime unavailable: %w", err)
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = defaultCacheDir()
	}
	// Serialize local model residency across recorder processes. STT from this
	// build has already closed; the operator serializes artifact builds.
	lockDir := filepath.Join(cfg.CacheDir, "summary-runtime")
	var result string
	err := withCacheLock(lockDir, func() error {
		if err := checkSummaryMemory(cfg); err != nil {
			return err
		}
		model, err := ensureSummaryModel(cfg.CacheDir, progress)
		if err != nil {
			return err
		}
		if err := checkSummaryMemory(cfg); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSec)*time.Second)
		defer cancel()
		s, err := startSummaryServer(ctx, cfg, model)
		if err != nil {
			return err
		}
		defer s.close()
		if err := s.checkBudget(ctx, system, user, cfg.ContextSize); err != nil {
			return err
		}
		cfg.BaseURL, cfg.APIKey = s.url+"/v1", s.key
		body, err := chatCompletion(cfg, system, user)
		if err != nil {
			return err
		}
		body = stripMarkdownFences(strings.TrimSpace(body))
		if err := validateLocalSummary(body); err != nil {
			return err
		}
		result = body + "\n"
		return nil
	})
	return result, err
}

func validateLocalSummary(body string) error {
	var want, got []string
	for _, line := range strings.Split(summaryV0Template, "\n") {
		if strings.HasPrefix(line, "#") {
			want = append(want, line)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			got = append(got, line)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || !strings.HasPrefix(body, "# Meeting Summary\n") {
		return fmt.Errorf("local summary does not match the seven required headings")
	}
	if strings.Contains(body, "<think>") || strings.Contains(body, "</think>") {
		return fmt.Errorf("local summary contains reasoning markup")
	}
	for i, heading := range want {
		if i == 0 {
			continue
		}
		section := strings.SplitN(body, heading+"\n", 2)[1]
		if i+1 < len(want) {
			section = strings.SplitN(section, want[i+1]+"\n", 2)[0]
		}
		if strings.TrimSpace(section) == "" {
			return fmt.Errorf("local summary has empty section %s", heading)
		}
	}
	return nil
}
