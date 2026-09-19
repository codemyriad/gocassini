package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Name    string `json:"name"`
	// Question is empty for freeform workflows, where the caller supplies it.
	Question    string `json:"question"`
	Description string `json:"description"`
	Origin      string `json:"origin"`
	// Instruction contains the rendered system prompt whose bytes SHA256 identifies.
	Instruction string `json:"instruction"`
}

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

	entries, err := readWorkflowRegistry(r.Context(), bin)
	if err != nil {
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the workflow registry could not be read")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// readWorkflowRegistry runs the recorder CLI within the caller's context.
// A successful empty registry is a non-nil slice so the API encodes [] rather than null.
func readWorkflowRegistry(ctx context.Context, bin string) ([]workflowView, error) {
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
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cassini insight workflows --json: %w: %s", err, strings.TrimSpace(stderr.buf.String()))
	}

	var entries []workflowView
	if err := json.Unmarshal(stdout.buf.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("decode the workflow registry: %w", err)
	}
	if err := validateWorkflowListing(entries); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []workflowView{}
	}
	return entries, nil
}

// validateWorkflowListing checks the fields needed to display and identify a workflow.
func validateWorkflowListing(entries []workflowView) error {
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.SHA256) == "" {
			return errors.New("the workflow registry holds an entry with no id or no content hash")
		}
		if strings.TrimSpace(entry.Version) == "" || strings.TrimSpace(entry.Name) == "" ||
			strings.TrimSpace(entry.Instruction) == "" {
			return fmt.Errorf("workflow %q was printed without its version, name or instruction", entry.ID)
		}
		// Freeform workflows must provide a slot for the caller's question.
		if strings.TrimSpace(entry.Question) == "" && !strings.Contains(entry.Instruction, insightQuestionPlaceholder) {
			return fmt.Errorf("workflow %q was printed with no question of its own and no %s to put one in", entry.ID, insightQuestionPlaceholder)
		}
	}
	return nil
}
