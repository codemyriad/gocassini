package operator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetStorageMode is mandatory in every test that resolves or switches the
// storage mode. ncStorage is a package-level singleton and the whole package's
// tests run in one binary, so a test that leaves it resolved to `default` would
// otherwise flip the publish sink and the read proxy for every later test.
func resetStorageMode(t *testing.T) {
	t.Helper()
	ncStorage.reset()
	t.Cleanup(ncStorage.reset)
}

// setStorageMode arranges a resolved mode and points the singleton at a
// throwaway settings file, so a test that triggers a persist does not write
// into the repo.
//
// It does NOT touch ncAccessSubstrate. The default model's read path requires
// both a resolved mode and a substrate the last probe agreed with, so a test
// about reading has to arrange both — see setUsableStorageMode.
func setStorageMode(t *testing.T, accessControlled bool) string {
	t.Helper()
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	ncStorage.set(accessControlled, storageModeSourceConfigured)
	return path
}

// setUsableStorageMode is setStorageMode plus a substrate the preflight proved:
// the shape of a deployment that is actually running in that mode.
func setUsableStorageMode(t *testing.T, accessControlled bool) string {
	t.Helper()
	path := setStorageMode(t, accessControlled)
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
	ncAccessSubstrate.markApplicable()
	ncAccessSubstrate.succeed()
	return path
}

func TestStorageSettingsAbsentFileIsNotADecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), storageSettingsFileName)

	settings, err := LoadStorageSettings(path)
	if err != nil {
		t.Fatalf("LoadStorageSettings() on a missing file error = %v, want nil", err)
	}
	if settings.Configured() {
		t.Fatal("a missing storage_settings.json must not read as a recorded decision")
	}
	if settings.AccessControlled() {
		t.Fatal("an absent flag must not read as access control being on")
	}
}

func TestStorageSettingsRoundTripBothValues(t *testing.T) {
	for _, want := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), storageSettingsFileName)
		if err := SaveStorageSettings(path, want, storageModeSourceUser); err != nil {
			t.Fatalf("SaveStorageSettings(%t) error = %v", want, err)
		}
		settings, err := LoadStorageSettings(path)
		if err != nil {
			t.Fatalf("LoadStorageSettings() error = %v", err)
		}
		if !settings.Configured() {
			t.Fatalf("a written flag must read back as a recorded decision (wanted %t)", want)
		}
		if settings.AccessControlled() != want {
			t.Fatalf("AccessControlled() = %t, want %t", settings.AccessControlled(), want)
		}
		wantMode := storageModeAccessControlled
		if !want {
			wantMode = storageModeDefault
		}
		if settings.Mode() != wantMode {
			t.Fatalf("Mode() = %q, want %q", settings.Mode(), wantMode)
		}
	}
}

// An unreadable file must be an ERROR, not "no decision". Falling through to a
// derivation would re-answer the question from whatever Nextcloud looks like
// now, and the derived answer for an instance whose Team folder has since been
// removed is `default` — which publishes the next recording where every account
// can read it.
func TestStorageSettingsRefusesAnUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := LoadStorageSettings(path); err == nil {
		t.Fatal("an unparseable storage_settings.json must not be reported as an absent decision")
	}
}

func TestStorageSettingsSaveIsAtomicAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, storageSettingsFileName)
	if err := SaveStorageSettings(path, true, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the temp file must be renamed away, stat error = %v", err)
	}
}

// The unresolved mode is the one that has to fail closed. A container that has
// restarted but not yet seen an enabled edge must keep reading the archive the
// access-controlled way — serving as the owner there would hand every account
// the whole archive for the length of that window.
func TestUnresolvedStorageModeFailsClosed(t *testing.T) {
	resetStorageMode(t)

	if accessControlled, resolved := ncStorage.mode(); resolved || accessControlled {
		t.Fatalf("a fresh record must report unresolved, got resolved=%t accessControlled=%t", resolved, accessControlled)
	}
	if !ncStorage.accessControlled() {
		t.Fatal("accessControlled() must answer true while the mode is unresolved, so a caller that ignores `resolved` fails closed")
	}
	mode, source := ncStorage.snapshot()
	if mode != "" || source != "" {
		t.Fatalf("snapshot() on an unresolved record = (%q, %q), want empty — \"\" is not the same answer as \"default\"", mode, source)
	}
}

func TestResolvedStorageModeIsReportedVerbatim(t *testing.T) {
	resetStorageMode(t)
	ncStorage.set(false, storageModeSourceDerived)

	accessControlled, resolved := ncStorage.mode()
	if !resolved || accessControlled {
		t.Fatalf("mode() = (%t, %t), want (false, true)", accessControlled, resolved)
	}
	if ncStorage.accessControlled() {
		t.Fatal("accessControlled() must answer false once the mode is resolved to default")
	}
	mode, source := ncStorage.snapshot()
	if mode != storageModeDefault || source != storageModeSourceDerived {
		t.Fatalf("snapshot() = (%q, %q), want (%q, %q)", mode, source, storageModeDefault, storageModeSourceDerived)
	}
}

func TestStorageSettingsPathSitsBesideTheJobDatabase(t *testing.T) {
	cfg := Config{DBPath: "/var/lib/cassini-operator/jobs.sqlite3"}
	if got, want := storageSettingsPath(cfg), "/var/lib/cassini-operator/"+storageSettingsFileName; got != want {
		t.Fatalf("storageSettingsPath() = %q, want %q — it has to survive restart on the same persistent volume as the DB", got, want)
	}
	if storageSettingsPath(cfg) == settingsPath(cfg) {
		t.Fatal("the storage mode must not share a file with the STT policy")
	}
}

// CASSINI_STORAGE_MODE declares which model a FRESH install starts in. It
// exists because deriving is a reading of whatever Nextcloud looked like on the
// first enabled edge, and on a stack still being assembled that is the wrong
// moment — the installed-ExApp e2e caught exactly that.
func TestStorageModeFromEnvAcceptsBothVocabularies(t *testing.T) {
	cases := map[string]bool{
		"default":           false,
		"DEFAULT":           false,
		"  default  ":       false,
		"off":               false,
		"access_controlled": true,
		"access-controlled": true,
		// The harness's own word, because someone reading its --storage-mode
		// flag should not have to know the app spells it differently.
		"acl-enabled": true,
		"acl":         true,
		"on":          true,
	}
	for raw, wantAccessControlled := range cases {
		got, ok, echoed := storageModeFromEnv(func(string) string { return raw })
		if !ok {
			t.Errorf("%q was not recognised", raw)
			continue
		}
		if got != wantAccessControlled {
			t.Errorf("%q = %t, want %t", raw, got, wantAccessControlled)
		}
		if echoed != strings.TrimSpace(raw) {
			t.Errorf("%q echoed back as %q; the message quotes what was set", raw, echoed)
		}
	}
}

// Unset is the ordinary case and means "derive it" — which is what makes a
// production fresh install land on the deps-free model without anyone
// declaring anything.
func TestStorageModeFromEnvUnsetMeansDerive(t *testing.T) {
	_, ok, raw := storageModeFromEnv(func(string) string { return "" })
	if ok || raw != "" {
		t.Fatalf("unset reported as (ok=%t raw=%q), want (false, \"\")", ok, raw)
	}
}

// A typo must be distinguishable from unset, or it silently starts the instance
// in a mode nobody asked for — the failure this variable exists to prevent.
func TestStorageModeFromEnvKeepsAnUnknownValueForTheErrorMessage(t *testing.T) {
	_, ok, raw := storageModeFromEnv(func(string) string { return "acl_enabld" })
	if ok {
		t.Fatal("a misspelt value was accepted")
	}
	if raw != "acl_enabld" {
		t.Fatalf("raw = %q, want the value back so the error can quote it", raw)
	}
}

// A declared mode is as explicit as a button, so it is never reconsidered.
func TestAStatedModeIsNeverReconsidered(t *testing.T) {
	for _, source := range []string{storageModeSourceUser, storageModeSourceEnv} {
		if !(StorageSettings{Source: source}).Chosen() {
			t.Errorf("source %q must count as stated", source)
		}
	}
	if (StorageSettings{Source: storageModeSourceDerived}).Chosen() {
		t.Error("a derived mode must remain reconsiderable")
	}
	// A file written before the field existed: those were all derivations.
	if (StorageSettings{}).Chosen() {
		t.Error("a file with no source must read as derived")
	}
}
