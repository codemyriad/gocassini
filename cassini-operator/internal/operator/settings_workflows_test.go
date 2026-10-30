package operator

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oneWorkflowJSON is what `cassini insight workflows --json` prints, trimmed to
// one entry. The real bytes are pinned in the recorder module, where the
// registry lives; what this file pins is the wiring between the two.
const oneWorkflowJSON = `[
  {
    "id": "summarise",
    "version": "v0",
    "sha256": "eba6bd6674d35522dddbd774d3fc6a1fbcdab025aa30a5993d49216d0c13f59f",
    "name": "Meeting summary",
    "description": "One document per meeting in a fixed shape.",
    "origin": "Built in",
    "instruction": "You are a meeting-summary editor.\n"
  }
]`

func TestSettingsWorkflowsServesTheRecordersRegistry(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s' '"+oneWorkflowJSON+"'\n")

	rec := httptest.NewRecorder()
	rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var entries []workflowView
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode response: %v\n%s", err, rec.Body.String())
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	// The three fields an insight document records, so a document and this
	// panel can be compared rather than assumed to agree.
	if entry.ID != "summarise" || entry.Version != "v0" || entry.SHA256 == "" {
		t.Errorf("entry = %+v, want the id, version and hash the recorder printed", entry)
	}
	// The instruction is relayed verbatim: the panel shows the bytes that reach
	// the model, not a description of them.
	if entry.Instruction != "You are a meeting-summary editor.\n" {
		t.Errorf("instruction = %q, want the bytes the CLI printed", entry.Instruction)
	}
	if entry.Description == "" || entry.Name == "" || entry.Origin == "" {
		t.Errorf("entry = %+v, want everything the panel renders", entry)
	}
}

// The CLI is the only bridge to the registry, so the arguments matter: a
// different verb would silently serve a different thing.
func TestSettingsWorkflowsAsksTheCLIForJSON(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	argv := t.TempDir() + "/argv"
	rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s\\n' \"$@\" > "+argv+"\nprintf '%s' '"+oneWorkflowJSON+"'\n")

	rec := httptest.NewRecorder()
	rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	got := strings.Fields(readFileString(t, argv))
	want := []string{"insight", "workflows", "--json"}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

// A registry that cannot be read is not an empty registry. The panel has to be
// able to say "the fetch failed" rather than "this deployment ships no
// templates", and it can only do that if the two are different responses.
func TestSettingsWorkflowsFailsLoudlyRatherThanServingNothing(t *testing.T) {
	t.Run("no binary configured", func(t *testing.T) {
		rt, cleanup := newTestRuntime(t)
		defer cleanup()
		rt.cfg.CassiniBin = ""

		rec := httptest.NewRecorder()
		rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("the CLI failed", func(t *testing.T) {
		rt, cleanup := newTestRuntime(t)
		defer cleanup()
		rt.cfg.CassiniBin = writeFakeCassini(t, "echo 'no such command' >&2\nexit 2\n")

		rec := httptest.NewRecorder()
		rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("the CLI printed something else", func(t *testing.T) {
		rt, cleanup := newTestRuntime(t)
		defer cleanup()
		rt.cfg.CassiniBin = writeFakeCassini(t, "echo 'workflows=1'\n")

		rec := httptest.NewRecorder()
		rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("an entry the panel could not render", func(t *testing.T) {
		rt, cleanup := newTestRuntime(t)
		defer cleanup()
		// No hash, and nothing to show either: a half row on the screen is a
		// worse answer than saying the registry could not be read.
		rt.cfg.CassiniBin = writeFakeCassini(t, `printf '%s' '[{"id":"summarise","version":"v0"}]'`+"\n")

		rec := httptest.NewRecorder()
		rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
		}
	})
}

// Read-only: this pass ships no editor, no import and no PUT, so a write has
// to be refused by the route rather than by a missing button in the panel.
func TestSettingsWorkflowsRefusesWrites(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s' '"+oneWorkflowJSON+"'\n")

	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		rt.settingsWorkflowsHandler(rec, httptest.NewRequest(method, "/settings/workflows", strings.NewReader("{}")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
			t.Errorf("%s Allow = %q, want GET", method, allow)
		}
	}
}

// The registry answers on its own exact path rather than falling into the LLM
// settings handler's prefix, which would 404 it.
func TestSettingsWorkflowsIsRoutedAheadOfTheLLMSettingsPrefix(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s' '"+oneWorkflowJSON+"'\n")

	handler := newHTTPHandler(rt.logger, rt, ExAppConfig{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A workflow that takes the caller's question says so only through the
// {{QUESTION}} slot in its instruction, the same byte the create handler
// decides on.
func TestWorkflowListingAcceptsAWorkflowThatTakesAQuestion(t *testing.T) {
	entries := []workflowView{{
		ID:          "ask",
		Version:     "v0",
		SHA256:      "a0ff18109e752c2539dc99f3577349c7f81f251521d87c92beaf05be6366d1e3",
		Name:        "Ask your own question",
		Description: "Answers a question you type.",
		Origin:      "Built in",
		Instruction: "You are a meeting analyst.\n\n" + insightQuestionPlaceholder + "\n",
	}}
	if err := validateWorkflowListing(entries); err != nil {
		t.Fatalf("a freeform workflow was refused: %v", err)
	}
}

// A custom template takes no question and may have no description (D-819):
// it runs on the transcripts alone, and the listing must carry it.
func TestWorkflowListingAcceptsACustomTemplateWithNoDescription(t *testing.T) {
	entries := []workflowView{{
		ID:          "resumen",
		Version:     "v1",
		SHA256:      "deadbeef",
		Name:        "Resumen",
		Description: "",
		Origin:      "Custom",
		Instruction: "Resume la reunión en español.",
	}}
	if err := validateWorkflowListing(entries); err != nil {
		t.Fatalf("a custom template with no description was refused: %v", err)
	}
}

// D-819: the listing reads the templates directory the operator resolved, so
// the panel and every run see the same custom set, and a broken custom file is
// relayed with its reason rather than failing the whole listing.
func TestSettingsWorkflowsListsCustomTemplatesFromTheTemplatesRoot(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seen := filepath.Join(t.TempDir(), "env")
	rt.cfg.TemplatesRoot = filepath.Join(t.TempDir(), "templates")
	listing := `[` + strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(oneWorkflowJSON), "["), "]") +
		`,{"id":"roto","version":"","sha256":"","name":"roto","description":"","origin":"Custom","instruction":"","error":"the front matter needs a version"}]`
	rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s' \"$CASSINI_TEMPLATES_DIR\" > "+seen+"\nprintf '%s' '"+listing+"'\n")

	rec := httptest.NewRecorder()
	rt.settingsWorkflowsHandler(rec, httptest.NewRequest(http.MethodGet, "/settings/workflows", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var entries []workflowView
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Origin != "Custom" || entries[1].Error == "" {
		t.Fatalf("entries = %+v, want the built-in and the broken custom one with its reason", entries)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != rt.cfg.TemplatesRoot {
		t.Errorf("the CLI read templates from %q, want %q", got, rt.cfg.TemplatesRoot)
	}
}

func TestChildEnvNamesTheTemplatesRoot(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TemplatesRoot = "/data/operator/templates"
	if got := childEnvValue(rt.childEnv(), envTemplatesDir); got != rt.cfg.TemplatesRoot {
		t.Errorf("%s = %q, want %q", envTemplatesDir, got, rt.cfg.TemplatesRoot)
	}
}

func TestTemplatesRootDefaultsBesideTheModelCacheOnTheVolume(t *testing.T) {
	persist := t.TempDir()
	t.Setenv(envAppPersistentStorage, persist)
	t.Setenv(envTemplatesDir, "")
	t.Setenv("CASSINI_CACHE_ROOT", "")
	cfg, _, err := loadConfig(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if want := filepath.Join(persist, "operator", "templates"); cfg.TemplatesRoot != want {
		t.Errorf("TemplatesRoot = %q, want %q", cfg.TemplatesRoot, want)
	}
	if filepath.Dir(cfg.TemplatesRoot) != filepath.Dir(cfg.ModelCacheRoot) {
		t.Errorf("templates %q are not beside the model cache %q", cfg.TemplatesRoot, cfg.ModelCacheRoot)
	}
}
