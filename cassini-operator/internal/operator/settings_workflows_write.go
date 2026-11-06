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
	"time"
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
// The recorder owns the format and the registry. A save writes the candidate
// into a private staging directory first and asks the recorder whether it
// resolves there; only a file the recorder will run is then renamed into the
// templates root. A refused file is answered 400 with the recorder's own
// reason and was never where a summary build or an insight run could read it.
//
// The version is the operator's to keep: every save writes the previous
// version plus one, so two different instructions are never recorded under the
// same id and version by this editor. A delete records the version it removes
// in a ledger (templateVersionLedger), so an id that is deleted and created
// again continues from there rather than reusing numbers earlier documents
// already carry.

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
		// A file whose stem is not a usable id ("Team notes.md") is listed
		// broken under that stem. It can never be saved under it, but it can
		// be deleted, or the panel could show it and offer no way to remove it.
		if r.Method == http.MethodDelete && safeTemplateStem(id) {
			rt.deleteWorkflow(w, r, id)
			return
		}
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
		// A file the recorder cannot parse is listed with the version its
		// front matter still gives (salvageCustomTemplate), so saving over it
		// does not go back to 1.
		if n := listedVersion(entry); n >= version {
			version = n + 1
		}
	}
	ledger, err := readTemplateVersionLedger(dir)
	if err != nil {
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "the template versions could not be read")
		return
	}
	if n := ledger[id]; n >= version {
		version = n + 1
	}

	path := filepath.Join(dir, id+".md")
	if _, err := os.Stat(path); err == nil && createOnly(r) {
		writeJSONError(w, http.StatusPreconditionFailed, fmt.Sprintf("a custom template %q already exists", id))
		return
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		rt.logger.Printf("settings workflows: stat %s: %v", path, err)
		writeJSONError(w, http.StatusInternalServerError, "the existing template could not be read")
		return
	}

	data := []byte(request.file(version))
	checked, err := rt.checkTemplateFile(r, dir, id, data)
	if err != nil {
		var refused *templateRefusedError
		if errors.As(err, &refused) {
			writeJSONError(w, http.StatusBadRequest, "the template was not saved: "+refused.reason)
			return
		}
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the template was not saved: the workflow registry could not be read to check it")
		return
	}
	if err := writeTemplateFile(dir, path, data); err != nil {
		rt.logger.Printf("settings workflows: write %s: %v", path, err)
		writeJSONError(w, http.StatusInternalServerError, "the template could not be written")
		return
	}
	// The listing the next GET serves. The file is saved whether or not this
	// works; the entry checked in staging is the same bytes, so the same hash.
	if _, err := rt.refreshWorkflowRegistry(r.Context()); err != nil {
		rt.workflowListing.invalidate()
		rt.logger.Printf("settings workflows: list after saving %s: %v", id, err)
	}
	writeJSON(w, http.StatusOK, checked)
}

// templateRefusedError is a candidate file the recorder will not run, with
// the recorder's reason.
type templateRefusedError struct{ reason string }

func (e *templateRefusedError) Error() string { return e.reason }

// checkTemplateFile asks the recorder whether data, saved as <id>.md, would
// run, without putting it where anything else reads templates: the file is
// written alone into a dot-named staging directory inside the templates root
// (which the recorder and the listing cache both skip) and listed from there.
// The built-ins are still in that listing, so a file shadowing one is refused
// as it would be in place.
func (rt *Runtime) checkTemplateFile(r *http.Request, dir, id string, data []byte) (workflowView, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return workflowView{}, fmt.Errorf("create the templates directory: %w", err)
	}
	// A staging directory an hour old was left by a save that never reached
	// its deferred cleanup (the operator was killed mid-check) and would
	// otherwise stay on the volume for good. A younger one may be another
	// process's check in progress, which templatesMu does not cover.
	if stale, err := filepath.Glob(filepath.Join(dir, ".check-*")); err == nil {
		for _, leftover := range stale {
			if info, err := os.Stat(leftover); err != nil || time.Since(info.ModTime()) < staleStagingAge {
				continue
			}
			if err := os.RemoveAll(leftover); err != nil {
				rt.logger.Printf("settings workflows: remove %s: %v", leftover, err)
			}
		}
	}
	staging, err := os.MkdirTemp(dir, ".check-")
	if err != nil {
		return workflowView{}, fmt.Errorf("create a staging directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			rt.logger.Printf("settings workflows: remove %s: %v", staging, err)
		}
	}()
	if err := os.WriteFile(filepath.Join(staging, id+".md"), data, 0o644); err != nil {
		return workflowView{}, fmt.Errorf("write the candidate template: %w", err)
	}
	entries, warning, err := readWorkflowRegistry(r.Context(), strings.TrimSpace(rt.cfg.CassiniBin), staging)
	if err != nil {
		return workflowView{}, err
	}
	if warning != "" {
		rt.logger.Printf("settings workflows: checking %s: %s", id, warning)
	}
	for _, entry := range entries {
		if entry.ID != id || entry.Origin == originBuiltIn {
			continue
		}
		if !entry.runnable() {
			return workflowView{}, &templateRefusedError{reason: entry.Error}
		}
		return entry, nil
	}
	return workflowView{}, &templateRefusedError{reason: fmt.Sprintf("%q did not appear in the registry when it was checked", id)}
}

// listedVersion is the integer of a listed "vN" version, or 0.
func listedVersion(entry workflowView) int {
	n, err := strconv.Atoi(strings.TrimPrefix(entry.Version, "v"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// templateVersionLedger is the file, in the templates root, that remembers
// the highest version each deleted id reached: {"<id>": N}. It is dot-named,
// so the recorder never reads it as a template. Only a delete writes it — a
// file that is present carries its own version in the listing — and a file
// removed by hand from a shell bypasses it.
const templateVersionLedger = ".versions.json"

// staleStagingAge is how old a .check-* directory must be before a save
// removes it as a leftover. A check is one recorder listing, far shorter.
const staleStagingAge = time.Hour

func readTemplateVersionLedger(dir string) (map[string]int, error) {
	raw, err := os.ReadFile(filepath.Join(dir, templateVersionLedger))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", templateVersionLedger, err)
	}
	ledger := map[string]int{}
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return nil, fmt.Errorf("decode %s: %w", templateVersionLedger, err)
	}
	return ledger, nil
}

func writeTemplateVersionLedger(dir string, ledger map[string]int) error {
	raw, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	return writeTemplateFile(dir, filepath.Join(dir, templateVersionLedger), append(raw, '\n'))
}

// safeTemplateStem reports whether a listed file stem can be turned back into
// a path inside the templates root: one path element, not a dot file.
func safeTemplateStem(stem string) bool {
	return stem != "" && !strings.HasPrefix(stem, ".") && !strings.ContainsAny(stem, "/\\\x00") && len(stem) <= 255
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
	// The listing says whether a built-in answers to the id and which version
	// the file being removed reached; without it neither is known.
	entries, err := rt.readWorkflowRegistry(r.Context())
	if err != nil {
		rt.logger.Printf("settings workflows: %v", err)
		writeJSONError(w, http.StatusBadGateway, "the template was not deleted: the workflow registry could not be read")
		return
	}
	builtIn, removed := false, 0
	for _, entry := range entries {
		if entry.ID != id {
			continue
		}
		if entry.Origin == originBuiltIn {
			builtIn = true
		} else {
			removed = listedVersion(entry)
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
	for _, use := range []struct{ step, label, template string }{
		{"summary", "meeting summaries", settings.Summary.Template},
		{"insight", "insights", settings.Insight.Template},
	} {
		if !builtIn && strings.TrimSpace(use.template) == id {
			// step names the setting for the panel, which must not have to
			// read it out of a sentence that also quotes the id.
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": fmt.Sprintf("%q is the template for %s; choose another one there before deleting it", id, use.label),
				"step":  use.step,
			})
			return
		}
	}
	// Remembered before the file goes, so the id never reuses a version a
	// document may already carry.
	if removed > 0 {
		ledger, err := readTemplateVersionLedger(dir)
		if err == nil && removed > ledger[id] {
			ledger[id] = removed
			err = writeTemplateVersionLedger(dir, ledger)
		}
		if err != nil {
			rt.logger.Printf("settings workflows: record the version of %s: %v", id, err)
			writeJSONError(w, http.StatusInternalServerError, "the template was not deleted: its version could not be recorded")
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
