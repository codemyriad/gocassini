package operator

import (
	"strings"
	"testing"
)

// The cause table is copy, and copy in this repo is tested where it is written
// (storage_causes.go). Two things are worth failing a build over: a step that
// nobody wrote a sentence for, and a sentence that smuggles the log line back
// into the first thing a person reads.

// everyRecordedStep is every step this package passes to
// ncAccessSubstrate.unavailable or .degraded, in the shape it is recorded in.
// Grepping for those two calls is how it was assembled and how it should be
// re-checked when a new one is added.
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
