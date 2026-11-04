package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Custom insight templates, written from the panel (D-819).
//
//	PUT    operator/settings/workflows/<id>   {name, description, instruction}
//	DELETE operator/settings/workflows/<id>
//
// A PUT carrying `If-None-Match: *` creates and never replaces: it is answered
// 412 when <id>.md is already there. The panel sends it for a new template,
// whose id it derives from the name and the list it last loaded, so a list
// that is stale or failed to load cannot turn "New template" into an
// overwrite of somebody else's.
//
// A template is a file, <id>.md, in the templates root on the app's volume.
// The recorder owns the format and the registry; the operator writes the file
// and then asks the recorder whether it resolves, by listing the registry
// again. A file the recorder will not run is put back the way it was and the
// save is answered 400 with the recorder's own reason, so a template that
// would fail at Generate is refused at Save instead.
//
// The version is the operator's to keep: every save writes the previous
// version plus one, so two different instructions are never recorded under the
// same id and version by this editor.

const (
	maxTemplateInstructionBytes = 200 << 10
	maxTemplateFieldRunes       = 500
)

type workflowWriteRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Instruction string `json:"instruction"`
}

func (rt *Runtime) settingsWorkflowHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/settings/workflows/"), "/")
	if id == "" {
		rt.settingsWorkflowsHandler(w, r)
		return
	}
	if !llmTemplateIDPattern.MatchString(id) || utf8.RuneCountInString(id) > maxLLMFieldRunes {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("%q is not a template id: use letters, digits, '.', '_' or '-', starting with a letter or digit", id))
		return
	}
	switch r.Method {
	case http.MethodPut:
		rt.putWorkflow(w, r, id)
	case http.MethodDelete:
		rt.deleteWorkflow(w, r, id)
	default:
		writeMethodNotAllowed(w, "PUT, DELETE")
	}
}

func (rt *Runtime) templatesRoot() (string, error) {
	dir := strings.TrimSpace(rt.cfg.TemplatesRoot)
	if dir == "" {
		return "", errors.New("no templates directory is configured")
	}
	if strings.TrimSpace(rt.cfg.CassiniBin) == "" {
		return "", errors.New("no cassini binary is configured, so a template cannot be checked")
	}
	return dir, nil
}

func (rt *Runtime) putWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	dir, err := rt.templatesRoot()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	var request workflowWriteRequest
	// JSON escaping can double an instruction's bytes (every newline is
	// two), so the cap leaves room for that: an instruction that is too long
	// is refused by validate, with a message that says so.
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*maxTemplateInstructionBytes+64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "the template could not be read: "+err.Error())
		return
	}
	if err := request.validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	rt.templatesMu.Lock()
	defer rt.templatesMu.Unlock()

	before, err := rt.readWorkflowRegistry(r.Context())
	if err != nil {
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the workflow registry could not be read")
		return
	}
	version := 1
	for _, entry := range before {
		if entry.ID != id {
			continue
		}
		if entry.Origin == originBuiltIn {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("%q is a built-in template and cannot be changed; start a custom template from it instead", id))
			return
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(entry.Version, "v")); err == nil && n >= version {
			version = n + 1
		}
	}

	path := filepath.Join(dir, id+".md")
	previous, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		rt.logger.Printf("settings workflows: read %s: %v", path, readErr)
		writeJSONError(w, http.StatusInternalServerError, "the existing template could not be read")
		return
	}
	if existed && createOnly(r) {
		writeJSONError(w, http.StatusPreconditionFailed, fmt.Sprintf("a custom template %q already exists", id))
		return
	}
	// The version comes from the listing above. A file the recorder cannot
	// parse is listed with the version its front matter still gives
	// (salvageCustomTemplate), so saving over it does not go back to 1, and
	// the operator keeps no second reader of the format.
	if err := writeTemplateFile(dir, path, []byte(request.file(version))); err != nil {
		rt.logger.Printf("settings workflows: write %s: %v", path, err)
		writeJSONError(w, http.StatusInternalServerError, "the template could not be written")
		return
	}
	revert := func() {
		var err error
		if existed {
			err = writeTemplateFile(dir, path, previous)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			rt.logger.Printf("settings workflows: revert %s: %v", path, err)
		}
	}

	// The one fresh listing a save needs, which the cache then keeps.
	after, err := rt.refreshWorkflowRegistry(r.Context())
	if err != nil {
		revert()
		rt.workflowListing.invalidate()
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the template was not saved: the workflow registry could not be read to check it")
		return
	}
	for _, entry := range after {
		if entry.ID != id || entry.Origin == originBuiltIn {
			continue
		}
		if !entry.runnable() {
			revert()
			rt.workflowListing.invalidate()
			writeJSONError(w, http.StatusBadRequest, "the template was not saved: "+entry.Error)
			return
		}
		writeJSON(w, http.StatusOK, entry)
		return
	}
	revert()
	rt.workflowListing.invalidate()
	writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("the template was not saved: %q did not appear in the registry after it was written", id))
}

func (rt *Runtime) deleteWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	dir, err := rt.templatesRoot()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	rt.templatesMu.Lock()
	defer rt.templatesMu.Unlock()

	path := filepath.Join(dir, id+".md")
	entries, listErr := rt.readWorkflowRegistry(r.Context())
	if listErr != nil {
		rt.logger.Printf("settings workflows: %v", listErr)
	}
	builtIn := false
	for _, entry := range entries {
		if entry.ID == id && entry.Origin == originBuiltIn {
			builtIn = true
		}
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if builtIn {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("%q is a built-in template and cannot be deleted", id))
			return
		}
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("there is no custom template %q", id))
		return
	}
	// A template a step is pointed at would leave that step naming nothing.
	// A file that shares a built-in's id is the exception: it never runs (the
	// recorder lists it broken), the built-in answers to the id before and
	// after, so deleting it changes nothing a step runs.
	settings := rt.currentLLMSettings()
	for _, use := range []struct{ step, template string }{
		{"meeting summaries", settings.Summary.Template},
		{"insights", settings.Insight.Template},
	} {
		if !builtIn && strings.TrimSpace(use.template) == id {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("%q is the template for %s; choose another one there before deleting it", id, use.step))
			return
		}
	}
	err = os.Remove(path)
	rt.workflowListing.invalidate()
	if err != nil {
		rt.logger.Printf("settings workflows: remove %s: %v", path, err)
		writeJSONError(w, http.StatusInternalServerError, "the template could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (q *workflowWriteRequest) validate() error {
	q.Name = strings.TrimSpace(q.Name)
	q.Description = strings.TrimSpace(q.Description)
	q.Instruction = strings.TrimSpace(strings.ReplaceAll(q.Instruction, "\r\n", "\n"))
	for _, field := range []struct{ label, value string }{
		{"name", q.Name}, {"description", q.Description},
	} {
		if utf8.RuneCountInString(field.value) > maxTemplateFieldRunes {
			return fmt.Errorf("the %s is longer than %d characters", field.label, maxTemplateFieldRunes)
		}
		if strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("the %s must be one line, with no control characters", field.label)
		}
	}
	switch {
	case q.Name == "":
		return errors.New("a template needs a name")
	case q.Instruction == "":
		return errors.New("a template needs an instruction")
	case len(q.Instruction) > maxTemplateInstructionBytes:
		return fmt.Errorf("the instruction is longer than %d bytes", maxTemplateInstructionBytes)
	case !utf8.ValidString(q.Instruction):
		return errors.New("the instruction is not valid text")
	}
	return nil
}

// file renders the request in the recorder's template format
// (internal/insight/workflows/custom.go). Values are double-quoted with Go
// escapes, which that parser reads back exactly.
func (q workflowWriteRequest) file(version int) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", strconv.Quote(q.Name))
	fmt.Fprintf(&b, "description: %s\n", strconv.Quote(q.Description))
	fmt.Fprintf(&b, "version: %d\n", version)
	b.WriteString("---\n\n")
	b.WriteString(q.Instruction)
	b.WriteString("\n")
	return b.String()
}

// createOnly reports whether a PUT asked to create and never replace.
func createOnly(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("If-None-Match")) == "*"
}

// writeTemplateFile writes a template through a dot-named temporary file in
// the same directory, which the recorder skips, and renames it into place.
func writeTemplateFile(dir, path string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}
