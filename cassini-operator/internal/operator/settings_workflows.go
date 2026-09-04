package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GET operator/settings/workflows — the insight templates this deployment can
// actually run (D-718).
//
// Read-only, and it will stay that way for this pass: there is no editor, no
// import and no PUT. A prompt is immutable and authored in the repository, so
// the only honest thing this surface can do is say what shipped, with the
// version and the content hash an insight document records — which is what lets
// a reader of a document a month old tell which prompt made it.
//
// The registry is compiled into the recorder, and cassini-operator is a
// separate Go module that cannot import it. So this shells out to the CLI,
// exactly as published/meetings-context does: `cassini insight workflows --json`
// prints the same list this endpoint serves, one implementation behind both.
// Anyone can run that command against their own image and get the answer this
// panel is showing them.
//
// It rides the already-widened ^operator\/settings(\/.*)?$ route in
// appinfo/info.xml at ADMIN, alongside the LLM settings, so there is no
// manifest change and no second notion of who may read it.
const (
	// workflowsTimeout bounds the child. It reads no files, makes no network
	// call and prints a compiled-in constant, so anything approaching this is a
	// wedged process rather than slow work.
	workflowsTimeout = 15 * time.Second

	// maxWorkflowsBytes bounds what the child may print. The registry is a
	// handful of prompts — tens of kilobytes — so this is generous, and it
	// exists so a wrong binary on CassiniBin cannot stream into memory.
	maxWorkflowsBytes = 4 << 20
)

// workflowView is one workflow as this endpoint serves it.
//
// It mirrors the CLI's JSON rather than relaying its bytes, so that a
// deployment pointed at a binary printing something else fails loudly here
// instead of handing the panel whatever it printed. The fields are the CLI's,
// and adding one is a change in both places on purpose: this struct is the
// operator's statement of what the panel is promised.
type workflowView struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Name    string `json:"name"`
	// Question is what this workflow asks of a set of meetings. It is what the
	// panel discloses under the name, because a name says nothing about what
	// the model is asked to do.
	Question    string `json:"question"`
	Description string `json:"description"`
	// Origin says where the bytes came from — "Built in" for everything that
	// ships in the image. It is derived by the recorder rather than written on
	// each row, so the day a second resolver exists this column is already
	// telling the truth.
	Origin string `json:"origin"`
	// Instruction is the system prompt with its template spliced in: the exact
	// bytes sent to the model, not a description of them. A description cannot
	// be shown to track the prompt it describes; SHA256 names these bytes.
	Instruction string `json:"instruction"`
}

func (rt *Runtime) settingsWorkflowsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	bin := strings.TrimSpace(rt.cfg.CassiniBin)
	if bin == "" {
		// 503 rather than an empty list: "this deployment ships no templates"
		// and "this deployment cannot tell you what it ships" are different
		// answers, and an empty list is the one that would send an
		// administrator looking for a missing feature.
		writeJSONError(w, http.StatusServiceUnavailable, "no cassini binary is configured, so the workflow registry cannot be read")
		return
	}

	entries, err := rt.readWorkflowRegistry(r, bin)
	if err != nil {
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the workflow registry could not be read")
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// readWorkflowRegistry runs the CLI and decodes what it printed.
//
// Never nil on success: an empty registry would come back as `null` in JSON,
// and the panel has to be able to tell "nothing shipped" from "the fetch
// failed" — which it cannot do if success sometimes looks like absence.
func (rt *Runtime) readWorkflowRegistry(r *http.Request, bin string) ([]workflowView, error) {
	// The request's own context governs, so an abandoned fetch stops the child
	// rather than leaving it to finish into nothing. No process group dance
	// here: unlike `meetings context`, this verb spawns nothing of its own.
	ctx, cancel := context.WithTimeout(r.Context(), workflowsTimeout)
	defer cancel()

	// Both streams get the same bound, for different reasons: a truncated
	// stderr costs a diagnostic's tail, while a truncated stdout is JSON that
	// no longer parses — which is the loud failure a runaway child should
	// produce, rather than a bound with no failure.
	var stdout truncatingBuffer
	stdout.remaining = maxWorkflowsBytes
	var stderr truncatingBuffer
	stderr.remaining = 8 << 10

	cmd := exec.CommandContext(ctx, bin, "insight", "workflows", "--json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// This verb prints a compiled-in constant and reads nothing, so it needs
	// none of the operator's credentials. It stopped being an ADMIN-only spawn
	// when POST insights began resolving its workflow through here (D-700):
	// every logged-in caller can now start this child, and APP_SECRET in a
	// process is the ability to act as any account on the instance.
	cmd.Env = contextChildEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cassini insight workflows --json: %w: %s", err, strings.TrimSpace(stderr.buf.String()))
	}

	var entries []workflowView
	if err := json.Unmarshal(stdout.buf.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("decode the workflow registry: %w", err)
	}
	for _, entry := range entries {
		// The fields the panel cannot render a row without, checked here rather
		// than tolerated: a blank one means the recorder printed a shape this
		// build does not understand, and half a row on the screen is a worse
		// answer than saying the registry could not be read. An id with no hash
		// is the sharpest case — a workflow a document could not be traced back
		// to is the one thing this endpoint exists to carry.
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.SHA256) == "" {
			return nil, fmt.Errorf("the workflow registry holds an entry with no id or no content hash")
		}
		if strings.TrimSpace(entry.Version) == "" || strings.TrimSpace(entry.Name) == "" ||
			strings.TrimSpace(entry.Question) == "" || strings.TrimSpace(entry.Instruction) == "" {
			return nil, fmt.Errorf("workflow %q was printed without its version, name, question or instruction", entry.ID)
		}
	}
	if entries == nil {
		entries = []workflowView{}
	}
	return entries, nil
}
