package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// The workflow registry is compiled into the recorder, a separate Go module.
// The ADMIN settings endpoint reads it through `cassini insight workflows --json`.
const (
	workflowsTimeout  = 15 * time.Second
	maxWorkflowsBytes = 4 << 20
)

// workflowView is the validated CLI response served by the settings endpoint.
type workflowView struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Origin      string `json:"origin"`
	// Instruction contains the rendered system prompt whose bytes SHA256 identifies.
	Instruction string `json:"instruction"`
	// Error is set on a custom template that cannot run, saying why. Such an
	// entry has no hash and can be neither chosen nor run (D-819).
	Error string `json:"error,omitempty"`
}

// envTemplatesDir is the variable the recorder reads custom templates from
// (internal/insight/workflows: TemplatesDirEnv).
const envTemplatesDir = "CASSINI_TEMPLATES_DIR"

// originBuiltIn is the recorder's origin for a workflow compiled into it
// (internal/insight/workflows: OriginBuiltIn).
const originBuiltIn = "Built in"

// runnable reports whether a listed workflow can be chosen and run.
func (w workflowView) runnable() bool { return w.Error == "" }

func (rt *Runtime) settingsWorkflowsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	bin := strings.TrimSpace(rt.cfg.CassiniBin)
	if bin == "" {
		// A missing binary is a service failure, not an empty registry.
		writeJSONError(w, http.StatusServiceUnavailable, "no cassini binary is configured, so the workflow registry cannot be read")
		return
	}

	entries, err := rt.readWorkflowRegistry(r.Context())
	if err != nil {
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the workflow registry could not be read")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// readWorkflowRegistry lists the workflows this deployment can run, custom
// templates included, through the recorder CLI. The listing is cached while
// the recorder binary and the templates directory stay as they were.
func (rt *Runtime) readWorkflowRegistry(ctx context.Context) ([]workflowView, error) {
	return rt.listWorkflows(ctx, false)
}

// refreshWorkflowRegistry lists the registry afresh, whatever the cache
// holds, and keeps the result: a save checks its file with it.
func (rt *Runtime) refreshWorkflowRegistry(ctx context.Context) ([]workflowView, error) {
	return rt.listWorkflows(ctx, true)
}

// listWorkflows reads the listing and logs what the recorder warned about
// while making it: an unreadable templates directory is listed as the
// built-ins alone, and this log line is where an administrator finds out why
// their templates are missing.
func (rt *Runtime) listWorkflows(ctx context.Context, fresh bool) ([]workflowView, error) {
	entries, warning, err := rt.workflowListing.read(ctx, strings.TrimSpace(rt.cfg.CassiniBin), rt.cfg.TemplatesRoot, fresh)
	if warning != "" {
		rt.logger.Printf("workflow registry: %s", warning)
	}
	return entries, err
}

// workflowListingCache is the last registry listing, process-local (D-819).
// Every template GET and the Generate card's mount read the registry, and
// each read used to spawn the recorder. The listing depends only on the
// recorder binary and the files in the templates directory, so it is kept
// under a key made of those: the binary's path, size and mtime, and the directory's mtime
// with each template file's name, size and mtime (a file edited in place
// leaves the directory's mtime alone). Saves and deletes also drop it
// explicitly, under templatesMu.
type workflowListingCache struct {
	mu         sync.Mutex
	key        string
	entries    []workflowView
	generation uint64
}

// invalidate drops the cached listing; a listing begun before this is not
// kept.
func (c *workflowListingCache) invalidate() {
	c.mu.Lock()
	c.key, c.entries = "", nil
	c.generation++
	c.mu.Unlock()
}

// read returns the listing and, when it spawned the recorder for it, what the
// recorder printed on stderr ("" for a cached listing).
func (c *workflowListingCache) read(ctx context.Context, bin, templatesDir string, fresh bool) ([]workflowView, string, error) {
	key, cacheable := workflowListingKey(bin, templatesDir)
	c.mu.Lock()
	if !fresh && cacheable && c.key == key && c.entries != nil {
		entries := slices.Clone(c.entries)
		c.mu.Unlock()
		return entries, "", nil
	}
	generation := c.generation
	c.mu.Unlock()

	entries, warning, err := readWorkflowRegistry(ctx, bin, templatesDir)
	if err != nil {
		return nil, "", err
	}
	c.mu.Lock()
	if cacheable && c.generation == generation {
		c.key, c.entries = key, slices.Clone(entries)
	}
	c.mu.Unlock()
	return entries, warning, nil
}

// workflowListingKey says what a listing was read from. It is not cacheable
// when the binary or the directory cannot be inspected. The binary's size and
// mtime are in the key so that one replaced at the same path (a standalone or
// dev deploy, without an operator restart) is listed afresh.
func workflowListingKey(bin, templatesDir string) (string, bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\x00", bin)
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return "", false
	}
	binInfo, err := os.Stat(resolved)
	if err != nil {
		return "", false
	}
	fmt.Fprintf(&b, "%d:%d\x00%s\x00", binInfo.Size(), binInfo.ModTime().UnixNano(), templatesDir)
	dir := strings.TrimSpace(templatesDir)
	if dir == "" {
		return b.String(), true
	}
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return b.String() + "absent", true
	}
	if err != nil {
		return "", false
	}
	fmt.Fprintf(&b, "%d", info.ModTime().UnixNano())
	items, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, item := range items {
		if item.IsDir() || !strings.HasSuffix(item.Name(), ".md") {
			continue
		}
		fi, err := item.Info()
		if err != nil {
			return "", false
		}
		fmt.Fprintf(&b, "\x00%s:%d:%d", item.Name(), fi.Size(), fi.ModTime().UnixNano())
	}
	return b.String(), true
}

// readWorkflowRegistry runs the recorder CLI within the caller's context, and
// returns what it printed on stderr alongside a listing it succeeded in making.
// A successful empty registry is a non-nil slice so the API encodes [] rather than null.
func readWorkflowRegistry(ctx context.Context, bin, templatesDir string) ([]workflowView, string, error) {
	ctx, cancel := context.WithTimeout(ctx, workflowsTimeout)
	defer cancel()

	// Bound both streams and reject incomplete JSON below.
	var stdout truncatingBuffer
	stdout.remaining = maxWorkflowsBytes
	var stderr truncatingBuffer
	stderr.remaining = 8 << 10

	cmd := exec.CommandContext(ctx, bin, "insight", "workflows", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// This helper also serves USER insight requests. The child needs no
	// Nextcloud credentials; APP_SECRET would let it act as any account.
	cmd.Env = contextChildEnv(os.Environ())
	if dir := strings.TrimSpace(templatesDir); dir != "" {
		cmd.Env = setEnvKey(cmd.Env, envTemplatesDir, dir)
	}
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("cassini insight workflows --json: %w: %s", err, strings.TrimSpace(stderr.buf.String()))
	}

	var entries []workflowView
	if err := json.Unmarshal(stdout.buf.Bytes(), &entries); err != nil {
		return nil, "", fmt.Errorf("decode the workflow registry: %w", err)
	}
	if err := validateWorkflowListing(entries); err != nil {
		return nil, "", err
	}
	if entries == nil {
		entries = []workflowView{}
	}
	return entries, strings.TrimSpace(stderr.buf.String()), nil
}

// validateWorkflowListing checks the fields needed to display and identify a workflow.
func validateWorkflowListing(entries []workflowView) error {
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			return errors.New("the workflow registry holds an entry with no id")
		}
		// A broken custom template is listed so the panel can say what is
		// wrong with it; it has no hash because it has no bytes that run.
		if !entry.runnable() {
			continue
		}
		if strings.TrimSpace(entry.SHA256) == "" {
			return fmt.Errorf("workflow %q was printed with no content hash", entry.ID)
		}
		if strings.TrimSpace(entry.Version) == "" || strings.TrimSpace(entry.Name) == "" ||
			strings.TrimSpace(entry.Instruction) == "" {
			return fmt.Errorf("workflow %q was printed without its version, name or instruction", entry.ID)
		}
	}
	return nil
}

// workflowChoice is one template as somebody choosing between them sees it:
// GET operator/insights/workflows, USER-readable (D-819). Everything needed to
// pick one and to know whether it asks for a question, and never the
// instruction: the prompt bytes stay on the ADMIN settings route.
type workflowChoice struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	TakesQuestion bool   `json:"takesQuestion"`
	Origin        string `json:"origin"`
}

// workflowChoices is the USER listing: the templates, and the one a run that
// names none gets, so the Generate card can preselect it (D-819).
type workflowChoices struct {
	Default   string           `json:"default"`
	Templates []workflowChoice `json:"templates"`
}

// insightWorkflowsHandler lists the templates any caller may run, by name,
// and the deployment's default: the configured insight template when it can
// run, else the built-in summary.
// Broken custom templates are left out: nobody can run them, and why they are
// broken is the administrator's to read on the ADMIN route.
func (rt *Runtime) insightWorkflowsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if strings.TrimSpace(rt.cfg.CassiniBin) == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "no cassini binary is configured, so the templates cannot be listed")
		return
	}
	entries, err := rt.readWorkflowRegistry(r.Context())
	if err != nil {
		rt.logger.Printf("insight workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the templates could not be listed")
		return
	}
	choices := make([]workflowChoice, 0, len(entries))
	defaultID := ""
	if chosen, ok := defaultInsightWorkflow(entries, rt.currentLLMSettings().Insight.Template); ok {
		defaultID = chosen.ID
	}
	for _, entry := range entries {
		if !entry.runnable() {
			continue
		}
		choices = append(choices, workflowChoice{
			ID:            entry.ID,
			Name:          entry.Name,
			Description:   entry.Description,
			TakesQuestion: strings.Contains(entry.Instruction, insightQuestionPlaceholder),
			Origin:        entry.Origin,
		})
	}
	// no-store, as for the provider list: AppAPI caches a proxied GET for an
	// hour, and this list changes the moment an administrator saves a template.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, workflowChoices{Default: defaultID, Templates: choices})
}

// defaultInsightWorkflowID is the built-in a run falls back to when no
// insight template is configured (internal/insight/workflows: SummariseID).
const defaultInsightWorkflowID = "summarise"

// defaultInsightWorkflow is the template a run that names none gets: the
// configured insight template when it can run, else the built-in summary,
// else the first runnable entry. It is the one rule both the USER listing's
// "default" and resolveWorkflow follow, so the template the Generate card
// preselects is the one an API or CLI create without a workflow runs. ok is
// false only when nothing in the listing can run.
func defaultInsightWorkflow(entries []workflowView, configured string) (workflowView, bool) {
	configured = strings.TrimSpace(configured)
	var fallback, first workflowView
	for _, entry := range entries {
		if !entry.runnable() {
			continue
		}
		if configured != "" && entry.ID == configured {
			return entry, true
		}
		if fallback.ID == "" && entry.ID == defaultInsightWorkflowID {
			fallback = entry
		}
		if first.ID == "" {
			first = entry
		}
	}
	if fallback.ID != "" {
		return fallback, true
	}
	return first, first.ID != ""
}
