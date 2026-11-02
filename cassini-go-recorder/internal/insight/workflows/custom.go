package workflows

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gocassini/internal/insight"
)

// Custom templates (D-819).
//
// A deployment's own templates are markdown files in one directory, named by
// TemplatesDirEnv. The operator points that at the app's persistent volume, so
// a template survives an app update; from a shell it is whatever directory the
// caller names. Each file is one template:
//
//	---
//	name: "Resumen de la reunión"
//	description: "Resumen, decisiones y próximos pasos."
//	version: 3
//	---
//	<the instruction, exactly as the model receives it>
//
// The id is the file stem. The body is the whole instruction in its final form:
// there is no {{TEMPLATE}} splice for a custom file, because the person editing
// it is editing the bytes the model reads, and a second half to splice in would
// be a second place for the output shape to live.
//
// A custom template takes the caller's question exactly as the built-in `ask`
// does: when its instruction carries {{QUESTION}}, and only then. Whether it
// takes one is read off the bytes (Workflow.TakesQuestion), never declared, so
// there is no `question` field, and a file carrying one is refused rather than
// read as something it no longer means. Other inputs a person fills in when
// they run a template (more placeholders, named ones) are a feature of their
// own and do not exist yet.
//
// A custom template is hashed by insight.NewWorkflow like a built-in, so every
// document it writes records id, version and SHA-256 exactly as one written by a
// built-in does. What the lint gate does for a built-in — refuse new bytes under
// an old version — the editor does for a custom file by bumping version on every
// save; a file edited by hand without a bump still hashes differently, so the
// record still names the bytes that ran.
//
// A file that will not run is never dropped silently. It is listed in the
// catalogue as broken, with the reason, so the panel can say why a template an
// administrator added is not on offer, and naming it on `insight run` is refused
// with that reason.

// TemplatesDirEnv names the directory custom templates are read from. Unset, or
// naming a directory that does not exist, means built-ins only.
const TemplatesDirEnv = "CASSINI_TEMPLATES_DIR"

// OriginCustom is the origin of a workflow read from the templates directory.
const OriginCustom = "Custom"

// customTemplateExt is the only file extension read as a template.
const customTemplateExt = ".md"

// maxCustomTemplateBytes bounds one template file. A prompt this size is far
// beyond anything a model will be sent usefully; the bound is here so a stray
// large file in the directory cannot make every listing slow.
const maxCustomTemplateBytes = 256 << 10

// maxCustomIDRunes matches the operator's bound on a template id.
const maxCustomIDRunes = 200

// customIDPattern is the operator's llmTemplateIDPattern: the id ends up on a
// `cassini insight run --workflow` command line and in a settings field, and a
// file whose stem could not be named there could never be chosen.
var customIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidCustomID reports whether id can name a custom template file.
func ValidCustomID(id string) bool {
	return customIDPattern.MatchString(id) && utf8.RuneCountInString(id) <= maxCustomIDRunes
}

// CustomTemplate is one parsed template file.
type CustomTemplate struct {
	ID          string
	Name        string
	Description string
	Version     int
	Instruction string
}

// VersionLabel is the version string a record carries for this template.
// Built-ins are "v0", "v1"…; a custom template's integer reads the same way.
func (c CustomTemplate) VersionLabel() string {
	return "v" + strconv.Itoa(c.Version)
}

// ParseCustomTemplate reads one template file's bytes. id is the file stem.
func ParseCustomTemplate(id string, raw []byte) (CustomTemplate, error) {
	if !ValidCustomID(id) {
		return CustomTemplate{}, fmt.Errorf("the file name %q is not a usable id: use letters, digits, '.', '_' or '-', starting with a letter or digit", id+customTemplateExt)
	}
	if len(raw) > maxCustomTemplateBytes {
		return CustomTemplate{}, fmt.Errorf("the file is %d bytes; a template may be at most %d", len(raw), maxCustomTemplateBytes)
	}
	if !utf8.Valid(raw) {
		return CustomTemplate{}, errors.New("the file is not UTF-8 text")
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")

	if !strings.HasPrefix(text, "---\n") {
		return CustomTemplate{}, errors.New("the file must start with a '---' line opening its front matter (name, description, version)")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	var header, body string
	switch {
	case end >= 0:
		header, body = rest[:end], rest[end+len("\n---\n"):]
	case strings.HasSuffix(rest, "\n---"):
		header, body = strings.TrimSuffix(rest, "\n---"), ""
	default:
		return CustomTemplate{}, errors.New("the front matter has no closing '---' line")
	}

	tpl := CustomTemplate{ID: id}
	seen := map[string]bool{}
	for n, line := range strings.Split(header, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return CustomTemplate{}, fmt.Errorf("front matter line %d is not 'key: value'", n+2)
		}
		key = strings.TrimSpace(key)
		if seen[key] {
			return CustomTemplate{}, fmt.Errorf("front matter names %q twice", key)
		}
		seen[key] = true
		parsed, err := parseFrontMatterScalar(value)
		if err != nil {
			return CustomTemplate{}, fmt.Errorf("front matter %q: %w", key, err)
		}
		switch key {
		case "name":
			tpl.Name = parsed
		case "question":
			return CustomTemplate{}, errors.New("the question field is no longer part of a template; remove it, and put {{QUESTION}} in the instruction if the template should ask for one")
		case "description":
			tpl.Description = parsed
		case "version":
			version, err := strconv.Atoi(parsed)
			if err != nil || version < 1 {
				return CustomTemplate{}, fmt.Errorf("version must be a whole number of 1 or more, got %q", parsed)
			}
			tpl.Version = version
		default:
			return CustomTemplate{}, fmt.Errorf("front matter has an unknown field %q; the fields are name, description and version", key)
		}
	}

	tpl.Instruction = strings.TrimSpace(body)
	switch {
	case strings.TrimSpace(tpl.Name) == "":
		return CustomTemplate{}, errors.New("the front matter needs a name")
	case tpl.Version == 0:
		return CustomTemplate{}, errors.New("the front matter needs a version (a whole number, bumped on every change)")
	case tpl.Instruction == "":
		return CustomTemplate{}, errors.New("the instruction (everything after the front matter) is empty")
	case strings.Contains(tpl.Instruction, insight.TemplatePlaceholder):
		return CustomTemplate{}, fmt.Errorf("the instruction carries %s, and a custom template has no place for a template splice; write the output shape into the instruction", insight.TemplatePlaceholder)
	}
	return tpl, nil
}

// Workflow hashes the template as the registry runs it.
func (c CustomTemplate) Workflow() (insight.Workflow, error) {
	return insight.NewWorkflow(insight.WorkflowSpec{
		ID:      c.ID,
		Version: c.VersionLabel(),
		System:  c.Instruction,
	})
}

// parseFrontMatterScalar reads one front matter value: a double-quoted string
// (Go/JSON escapes), a single-quoted string (two single quotes make one), or a
// plain scalar, taken as written: this is not YAML, and a '#' in a value is
// part of it ("Lists issues #1 and #2"), never a comment. Multi-line values
// are not supported; a template's long text is its body.
func parseFrontMatterScalar(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	switch {
	case value == "":
		return "", nil
	case strings.HasPrefix(value, `"`):
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("the double-quoted value %s does not close on its line", value)
		}
		return unquoted, nil
	case strings.HasPrefix(value, "'"):
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", fmt.Errorf("the single-quoted value %s does not close on its line", value)
		}
		inner := value[1 : len(value)-1]
		if strings.Contains(strings.ReplaceAll(inner, "''", ""), "'") {
			return "", fmt.Errorf("the single-quoted value %s has a stray quote; write '' for a quote", value)
		}
		return strings.ReplaceAll(inner, "''", "'"), nil
	case value == "|" || value == ">" || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">"):
		return "", errors.New("multi-line values are not supported; put the value on one line")
	default:
		return value, nil
	}
}

// salvageCustomTemplate is what can be read from a file ParseCustomTemplate
// refused, so the panel can open it to be repaired rather than start empty:
// the body after the front matter (the whole file when no front matter
// closes), and the name and version when those lines still parse.
func salvageCustomTemplate(id string, raw []byte) CustomTemplate {
	tpl := CustomTemplate{ID: id}
	if len(raw) > maxCustomTemplateBytes || !utf8.Valid(raw) {
		return tpl
	}
	text := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\ufeff")
	tpl.Instruction = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "---\n") {
		return tpl
	}
	rest := text[len("---\n"):]
	var header string
	if end := strings.Index(rest, "\n---\n"); end >= 0 {
		header, tpl.Instruction = rest[:end], strings.TrimSpace(rest[end+len("\n---\n"):])
	} else if strings.HasSuffix(rest, "\n---") {
		header, tpl.Instruction = strings.TrimSuffix(rest, "\n---"), ""
	} else {
		return tpl
	}
	for _, line := range strings.Split(header, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		parsed, err := parseFrontMatterScalar(value)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			tpl.Name = parsed
		case "description":
			tpl.Description = parsed
		case "version":
			if version, err := strconv.Atoi(parsed); err == nil && version > 0 {
				tpl.Version = version
			}
		}
	}
	return tpl
}

// customFile is one template file's outcome: a workflow, or why it is not one.
type customFile struct {
	template CustomTemplate
	workflow insight.Workflow
	err      error
}

// readCustomDir reads every template file in dir, sorted by id. A missing
// directory is no templates; an unreadable one is an error, because an empty
// list would then be a lie about what the administrator put there.
func readCustomDir(dir string) ([]customFile, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	items, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the templates directory %s: %w", dir, err)
	}
	builtIn := map[string]bool{}
	for _, item := range shipped {
		builtIn[item.ID] = true
	}
	var out []customFile
	for _, item := range items {
		name := item.Name()
		// Dot files are an editor's or the operator's own temporary files.
		if item.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, customTemplateExt) {
			continue
		}
		id := strings.TrimSuffix(name, customTemplateExt)
		file := customFile{template: CustomTemplate{ID: id}}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			file.template, err = ParseCustomTemplate(id, raw)
			if err != nil {
				file.template = salvageCustomTemplate(id, raw)
			}
		}
		if err == nil && builtIn[id] {
			err = fmt.Errorf("%q is the id of a built-in template, and a custom template cannot replace one; rename the file and point the setting at the new id", id)
		}
		if err == nil {
			file.workflow, err = file.template.Workflow()
		}
		file.err = err
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].template.ID < out[j].template.ID })
	return out, nil
}
