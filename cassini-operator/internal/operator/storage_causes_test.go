package operator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The cause table is copy, and copy in this repo is tested where it is written
// (storage_causes.go). Two things are worth failing a build over: a step that
// nobody wrote a sentence for, and a sentence that smuggles the log line back
// into the first thing a person reads.

// everyRecordedStep is every step this package passes to
// ncAccessSubstrate.unavailable or .degraded, in the shape it is recorded in.
//
// It is still a written list, because a step is not always a constant at the
// call site — sanity() and accessControlReady() decide one and RETURN it, and no
// amount of reading the call site says which. But it is no longer only a written
// list: TestEveryRecordedStepIsListed parses this package and fails when a step
// that IS legible at the call site is missing from here, which is the drift that
// actually happened (`owner_account` went years without a sentence).
//
// Absent on purpose: `storage_mode_undecided` and `storage_mode_unconfirmed`.
// The operator resolves a mode when it is enabled (D-753), so neither can be
// reached, and storage_causes.go says why there is no copy for them.
var everyRecordedStep = []string{
	// nc_owner_account.go, nc_provision.go, nc_storage_probe.go
	storageStepServiceAccount,
	storageStepUniversalGroup,
	storageStepGroupFolder,
	storageStepFolderACL,
	storageStepFolderManager,
	storageStepDeclaredConflict,
	storageStepModeUnresolved,
	storageStepModeMismatch + ":" + storageStepDefaultRootShadowed,
	storageStepModeMismatch + ":" + storageStepDefaultRootUnknown,
	// nc_provision.go, nc_storage_preflight.go
	"administrator",
	"administrator_probe",
	"app_missing:" + ncAppGroupFolders,
	"app_missing:" + ncAppEveryoneGroup,
	"app_check_failed",
	"universal_group_probe",
	"folder_id",
	"legacy_deny_floor",
	"mount_mapping:" + ncRecordingsOwnerGroup,
	"mount_mapping:" + ncRecordingsEveryoneGroup,
	"acl_enable",
	"acl_manager",
	"migration_floor",
	"migration",
	"catalog_migration",
	"root_acl",
	"mkcol:" + ncACLRecordingsRoot,
	"recordings_tree",
}

func TestEveryRecordedStepHasACause(t *testing.T) {
	for _, step := range everyRecordedStep {
		t.Run(step, func(t *testing.T) {
			if strings.TrimSpace(storageCauseFor(step)) == "" {
				t.Fatalf("step %q has no cause sentence; add one to storageCauses", step)
			}
			if strings.TrimSpace(storageUserCauseFor(step)) == "" {
				t.Fatalf("step %q has no user-level cause sentence; add one to storageCauses", step)
			}
		})
	}
}

// A cause is what the notice OPENS with, so it has to read as a sentence rather
// than as the log line it replaces. The detail field is where the enum name,
// the path and the `occ` recipe belong, and they are still there.
func TestCausesAreSentencesAndNotLogLines(t *testing.T) {
	for _, step := range everyRecordedStep {
		t.Run(step, func(t *testing.T) {
			for _, tc := range []struct {
				audience string
				sentence string
			}{
				{"admin", storageCauseFor(step)},
				{"user", storageUserCauseFor(step)},
			} {
				for _, banned := range []string{
					// An enum name. Every step in this package is snake_case,
					// so the underscore is the whole tell.
					"_",
					// A path, and with it every folder and root constant.
					"/",
					// A command, and the endpoint that carries the full report.
					"occ",
					"status",
					// The HTTP verbs the detail strings quote.
					"PROPFIND",
					"MKCOL",
				} {
					if strings.Contains(tc.sentence, banned) {
						t.Fatalf("%s cause for %q contains %q: %s", tc.audience, step, banned, tc.sentence)
					}
				}
				if !strings.HasSuffix(strings.TrimSpace(tc.sentence), ".") {
					t.Fatalf("%s cause for %q is not a sentence: %s", tc.audience, step, tc.sentence)
				}
			}
		})
	}
}

// /setup is USER-level at the proxy. Its half of the table may say what kind of
// thing broke and nothing about which account, folder or app it was — the same
// contract TestSetupWithholdsEverythingAdminOnly holds the rest of the route to.
func TestUserCausesNameNothingAdminOnly(t *testing.T) {
	for _, step := range everyRecordedStep {
		t.Run(step, func(t *testing.T) {
			sentence := storageUserCauseFor(step)
			for _, banned := range []string{
				ncRecordingsOwner,
				ncRecordingsMount,
				ncDefaultRecordingsMount,
				ncAppGroupFolders,
				ncAppEveryoneGroup,
				ncRecordingsEveryoneGroup,
				envStorageMode,
				envNCAdminUser,
			} {
				if strings.Contains(sentence, banned) {
					t.Fatalf("user cause for %q names the admin-only %q: %s", step, banned, sentence)
				}
			}
		})
	}
}

// An unknown step is not a licence to invent a reason. The app has its own
// sentence for "the check did not finish", and an empty cause is what selects
// it.
func TestUnknownStepsHaveNoCause(t *testing.T) {
	for _, step := range []string{"", "something_new", "storage_mode_undecided", "storage_mode_unconfirmed"} {
		if got := storageCauseFor(step); got != "" {
			t.Fatalf("storageCauseFor(%q) = %q, want empty", step, got)
		}
		if got := storageUserCauseFor(step); got != "" {
			t.Fatalf("storageUserCauseFor(%q) = %q, want empty", step, got)
		}
	}
}

// The named families share one sentence, whatever name the step carries — and
// the two mode mismatches worth telling apart keep their own.
func TestNamedStepFamiliesShareOneSentence(t *testing.T) {
	if storageCauseFor("app_missing:"+ncAppGroupFolders) != storageCauseFor("app_missing:"+ncAppEveryoneGroup) {
		t.Fatal("the two missing apps must read the same; the app is named in the detail, not in the cause")
	}
	shadowed := storageCauseFor(storageStepModeMismatch + ":" + storageStepDefaultRootShadowed)
	unknown := storageCauseFor(storageStepModeMismatch + ":" + storageStepDefaultRootUnknown)
	if shadowed == unknown {
		t.Fatal("a mounted Team folder and an unanswered question are different causes and must read differently")
	}
	// A mismatch this build does not have a specific sentence for still gets the
	// family's.
	if storageCauseFor(storageStepModeMismatch+":something_else") == "" {
		t.Fatal("an unrecognised mode mismatch must still fall back to the family sentence")
	}
}

// Every step the source records, read OFF the source rather than out of a list
// somebody has to remember to update.
//
// This is the check the hand-maintained list could not do for itself, and the
// gap was real: `owner_account` is recorded straight from nc_provision.go and
// had no sentence of its own for as long as the table has existed. The parse is
// deliberately narrow — a step that is a literal, a constant, or a constant
// prefix joined to something computed — because those are the ones a reader
// could have resolved too, and a test that guessed at the rest would fail on
// code that is perfectly correct.
//
// A step assembled some other way (sanity() returning one it chose) is skipped
// here and lives in everyRecordedStep. So the two halves are complementary: this
// one catches what is legible, the list carries what is not.
func TestEveryRecordedStepIsListed(t *testing.T) {
	exact, prefixes := recordedStepsInSource(t)
	if len(exact) == 0 && len(prefixes) == 0 {
		t.Fatal("the source scan found no recorded steps at all; it has stopped looking at what it thinks it is looking at")
	}
	listed := make(map[string]bool, len(everyRecordedStep))
	for _, step := range everyRecordedStep {
		listed[step] = true
	}
	for _, step := range exact {
		if !listed[step] {
			t.Errorf("%q is recorded in this package and is not in everyRecordedStep, so nothing checks it has a cause sentence", step)
		}
	}
	for _, prefix := range prefixes {
		found := false
		for _, step := range everyRecordedStep {
			if strings.HasPrefix(step, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("steps named %q<something> are recorded in this package and everyRecordedStep has no example of one", prefix)
		}
	}
}

// recordedStepsInSource returns the steps passed to ncAccessSubstrate.unavailable
// and .degraded across the package's non-test files: the ones that resolve to a
// whole string, and the constant prefixes of the ones that carry a computed name.
func recordedStepsInSource(t *testing.T) (exact, prefixes []string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}

	// Every package-level string constant, so a step spelled as one resolves to
	// the same text the table is keyed on.
	consts := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if text, whole := foldStepExpr(value.Values[i], consts); whole {
						consts[name.Name] = text
					}
				}
			}
		}
	}

	seenExact := map[string]bool{}
	seenPrefix := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "unavailable" && sel.Sel.Name != "degraded" {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != "ncAccessSubstrate" {
				return true
			}
			text, whole := foldStepExpr(call.Args[0], consts)
			switch {
			case whole && text != "":
				seenExact[text] = true
			case !whole && text != "":
				seenPrefix[text] = true
			}
			return true
		})
	}
	for step := range seenExact {
		exact = append(exact, step)
	}
	for prefix := range seenPrefix {
		prefixes = append(prefixes, prefix)
	}
	return exact, prefixes
}

// foldStepExpr resolves a step expression as far as the source allows.
//
//	whole == true   the text IS the step.
//	whole == false  the text is a constant PREFIX and the rest is computed.
//	text == ""      nothing could be resolved; the caller ignores it.
func foldStepExpr(expr ast.Expr, consts map[string]string) (text string, whole bool) {
	switch node := expr.(type) {
	case *ast.ParenExpr:
		return foldStepExpr(node.X, consts)
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(node.Value)
		if err != nil {
			return "", false
		}
		return value, true
	case *ast.Ident:
		if value, ok := consts[node.Name]; ok {
			return value, true
		}
		return "", false
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return "", false
		}
		left, leftWhole := foldStepExpr(node.X, consts)
		if left == "" || !leftWhole {
			// Nothing constant at the front, so there is no prefix to check
			// either.
			return "", false
		}
		right, rightWhole := foldStepExpr(node.Y, consts)
		if right != "" && rightWhole {
			return left + right, true
		}
		return left, false
	default:
		return "", false
	}
}
