package operator

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The Doctor's words are the operator's English, and the app words them again
// in its reader's language by code, step id and reason code (D-921). Whatever
// this source can emit is listed in doctorCatalog.json, which the app's own
// test (cassini-app/src/operator/doctorText.test.ts) holds against its
// dictionary. So a code added here without a wording there fails one test or
// the other instead of silently staying English.
//
// Read from the source rather than kept by hand: the list is every string
// literal in a code position of these files, so a new row cannot be missed by
// forgetting to register it. After adding or renaming one, regenerate with
//
//	CASSINI_UPDATE_DOCTOR_CATALOG=1 go test ./internal/operator -run TestDoctorCatalog
//
// and give it English in cassini-viewer/src/i18n/en/app/doctor_checks.ts.
const doctorCatalogPath = "../../../cassini-app/src/operator/doctorCatalog.json"

// The files that build the Doctor's rows. A row type built anywhere else
// fails the test, so this list cannot quietly fall behind.
var doctorCatalogSources = map[string][]string{
	".": {
		"recording_readiness.go", "search_readiness.go", "nc_sharing_policy.go",
		"talk_signaling_mode.go", "readiness_prerequisites.go", "recording_test_tool.go",
		"recorded_at_files.go", "recorded_at_migration.go",
	},
	"../../../cassini-go-recorder/internal/talk":    {"readiness.go", "unreachable.go"},
	"../../../cassini-go-recorder/internal/cassini": {"doctor.go"},
}

// Composite types whose Code (or code) is a check's message code.
var doctorCheckTypes = map[string]bool{
	"readinessCheck": true, "ConnectionCheck": true, "unreachableFinding": true,
	"sharingPolicyFinding": true, "sharingPolicyIssue": true,
}

// Composite types whose ID is a remedy step's id.
var doctorStepTypes = map[string]bool{"readinessStep": true, "ConnectionStep": true}

// Variants, sharing issues and re-index outcomes pick a sentence within a
// code, so a new one is as much a new sentence as a new code is. Errors are
// the codes of refusals the operator's routes word for a reader.
type doctorCatalog struct {
	Messages       []string `json:"messages"`
	Variants       []string `json:"variants"`
	SharingIssues  []string `json:"sharing_issues"`
	Reindex        []string `json:"reindex"`
	Steps          []string `json:"steps"`
	BlockedReasons []string `json:"blocked_reasons"`
	Errors         []string `json:"errors"`
}

// doctorSets is what the scan collects, one set per catalog list.
type doctorSets struct {
	messages, variants, issues, reindex, steps, reasons, errors map[string]bool
}

func TestDoctorCatalog(t *testing.T) {
	sets := doctorSets{map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}}
	for dir, listed := range doctorCatalogSources {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !containsString(listed, filepath.Base(path)) {
				if typ := firstDoctorLiteral(file); typ != "" {
					t.Errorf("%s builds a %s: add it to doctorCatalogSources", path, typ)
				}
				continue
			}
			scanDoctorSource(t, path, file, sets)
		}
	}
	got := doctorCatalog{
		Messages: sortedKeys(sets.messages), Variants: sortedKeys(sets.variants), SharingIssues: sortedKeys(sets.issues),
		Reindex: sortedKeys(sets.reindex), Steps: sortedKeys(sets.steps), BlockedReasons: sortedKeys(sets.reasons),
		Errors: sortedKeys(sets.errors),
	}

	if os.Getenv("CASSINI_UPDATE_DOCTOR_CATALOG") != "" {
		raw, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(doctorCatalogPath, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(doctorCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var want doctorCatalog
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the Doctor's codes changed; regenerate %s (see doctor_catalog_test.go) and word the new ones in the app\n got: %+v\nwant: %+v", doctorCatalogPath, got, want)
	}
}

// scanDoctorSource collects every literal in a code position: a Code field of
// a row type, an assignment to .Code or code, the code argument of the
// readiness report's add helpers, a step's ID, a blocked file's code, a
// sharing issue's id, the code of a coded error response, and the values of
// "variant" and "reindex" in a params map.
func scanDoctorSource(t *testing.T, path string, file *ast.File, sets doctorSets) {
	messages, steps, reasons := sets.messages, sets.steps, sets.reasons
	walkTypedLiterals(file, func(typ string, lit *ast.CompositeLit) {
		fields := keyedFields(lit)
		switch {
		case doctorCheckTypes[typ]:
			for _, key := range []string{"Code", "code"} {
				if value, ok := stringLiteral(fields[key]); ok {
					messages[value] = true
				}
			}
			if value, ok := stringLiteral(fields["id"]); ok && typ == "sharingPolicyIssue" {
				sets.issues[value] = true
			}
		case doctorStepTypes[typ]:
			label, hasLabel := fields["Label"]
			if !hasLabel {
				return
			}
			id, ok := stringLiteral(fields["ID"])
			// A label passed in from elsewhere (doctor's advice) may have no
			// id; one written here is English this file owns, and needs one.
			if _, passedIn := label.(*ast.Ident); !ok && !passedIn {
				t.Errorf("%s: a %s with a label written here has no literal ID", path, typ)
			}
			if ok {
				steps[id] = true
			}
		case typ == "recordedAtFileBlocked":
			if value, ok := stringLiteral(fields["code"]); ok {
				reasons[value] = true
			} else {
				t.Errorf("%s: a recordedAtFileBlocked has no literal code", path)
			}
		}
	})
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			// A params map: {"variant": "…"}, {"reindex": "…"}.
			if key, ok := stringLiteral(node.Key); ok {
				if value, ok := stringLiteral(node.Value); ok {
					switch key {
					case "variant":
						sets.variants[value] = true
					case "reindex":
						sets.reindex[value] = true
					}
				}
			}
		case *ast.AssignStmt:
			if len(node.Lhs) != len(node.Rhs) {
				return true
			}
			for i, lhs := range node.Lhs {
				isCode := false
				switch target := lhs.(type) {
				case *ast.SelectorExpr:
					isCode = target.Sel.Name == "Code"
					if value, ok := stringLiteral(node.Rhs[i]); ok && target.Sel.Name == "id" {
						sets.issues[value] = true
					}
				case *ast.Ident:
					isCode = target.Name == "code"
				}
				if value, ok := stringLiteral(node.Rhs[i]); ok && isCode {
					messages[value] = true
				}
			}
		case *ast.CallExpr:
			if fn, ok := node.Fun.(*ast.Ident); ok && (fn.Name == "add" || fn.Name == "addWithSteps") && len(node.Args) >= 3 {
				if value, ok := stringLiteral(node.Args[2]); ok {
					messages[value] = true
				}
			}
			if fn, ok := node.Fun.(*ast.Ident); ok && fn.Name == "writeCodedJSONError" && len(node.Args) >= 3 {
				if value, ok := stringLiteral(node.Args[2]); ok {
					sets.errors[value] = true
				} else {
					t.Errorf("%s: writeCodedJSONError with no literal code", path)
				}
			}
		}
		return true
	})
}

// walkTypedLiterals visits every composite literal with its type's name,
// including the elided ones inside a slice literal (`[]readinessStep{{…}}`).
func walkTypedLiterals(file *ast.File, visit func(string, *ast.CompositeLit)) {
	var walk func(n ast.Node, elided string)
	walk = func(n ast.Node, elided string) {
		ast.Inspect(n, func(child ast.Node) bool {
			lit, ok := child.(*ast.CompositeLit)
			if !ok {
				return true
			}
			typ := typeName(lit.Type)
			if lit.Type == nil {
				typ = elided
			}
			if typ != "" {
				visit(typ, lit)
			}
			inner := ""
			switch t := lit.Type.(type) {
			case *ast.ArrayType:
				inner = typeName(t.Elt)
			case *ast.MapType:
				inner = typeName(t.Value)
			}
			for _, elt := range lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					elt = kv.Value
				}
				walk(elt, inner)
			}
			return false
		})
	}
	walk(file, "")
}

func firstDoctorLiteral(file *ast.File) string {
	found := ""
	walkTypedLiterals(file, func(typ string, lit *ast.CompositeLit) {
		if found == "" && len(lit.Elts) > 0 && (doctorCheckTypes[typ] || doctorStepTypes[typ] || typ == "recordedAtFileBlocked") {
			found = typ
		}
	})
	return found
}

func typeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func keyedFields(lit *ast.CompositeLit) map[string]ast.Expr {
	fields := map[string]ast.Expr{}
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok {
				fields[key.Name] = kv.Value
			}
		}
	}
	return fields
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil && value != ""
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
