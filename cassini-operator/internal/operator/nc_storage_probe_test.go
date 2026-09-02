package operator

import (
	"strings"
	"testing"
)

// readyProbe is the shape of an instance the provisioner built: both apps, the
// service account, the universal group, and a Team folder mapped, ACL'd and
// managed by the owner. Every test below starts from it and removes one thing.
func readyProbe() ncStorageProbe {
	return ncStorageProbe{
		AdminUser: "admin",
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteEnabled},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteEnabled},
		},
		NativeApps:     true,
		ServiceAccount: true,
		OwnerGroup:     true,
		EveryoneGroup:  true,
		Folder:         gfFolder{ID: "7", MountPoint: ncRecordingsMount, ACL: true},
		FolderProbed:   true,
		FolderPresent:  true,
		FolderMounted:  true,
		ACLEnabled:     true,
		EveryoneRead:   true,
		OwnerAll:       true,
		OwnerManages:   true,
		RecordingsRoot: true,
	}
}

// The derivation is the upgrade latch. Every install that exists today was
// built by the provisioner, so it has to resolve to access control — anything
// else turns a restricted archive into an org-wide one on the next enable, with
// nobody ever being asked.
func TestDeriveKeepsAccessControlForAnInstallThatAlreadyHasIt(t *testing.T) {
	if !readyProbe().deriveAccessControlEnabled() {
		t.Fatal("a fully provisioned instance must derive access control ON")
	}
}

// The spec's own list, one row per bullet: each of these makes the derived
// default `false`.
func TestDeriveFallsBackToDefaultWhenAnythingIsMissing(t *testing.T) {
	cases := []struct {
		name  string
		mutan func(*ncStorageProbe)
	}{
		{"no cassini service account", func(p *ncStorageProbe) { p.ServiceAccount = false }},
		{"no third-party deps", func(p *ncStorageProbe) {
			p.NativeApps = false
			p.Prereqs = []ncPrerequisiteStatus{
				{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
				{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
			}
		}},
		{"no Cassini/Recordings", func(p *ncStorageProbe) {
			p.FolderPresent = false
			p.FolderMounted = false
			p.EveryoneRead = false
			p.OwnerAll = false
			p.ACLEnabled = false
			p.OwnerManages = false
			p.RecordingsRoot = false
		}},
		{"Cassini/Recordings is a private home tree, not a Team folder", func(p *ncStorageProbe) {
			p.FolderPresent = false
			p.FolderMounted = false
			p.EveryoneRead = false
			p.OwnerAll = false
			p.ACLEnabled = false
			p.OwnerManages = false
			p.PrivateRoot = true
		}},
		{"no everyone group", func(p *ncStorageProbe) { p.EveryoneGroup = false }},
		{"advanced ACL is off", func(p *ncStorageProbe) { p.ACLEnabled = false }},
		{"the owner is not an ACL manager", func(p *ncStorageProbe) { p.OwnerManages = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := readyProbe()
			tc.mutan(&probe)
			if probe.deriveAccessControlEnabled() {
				t.Fatalf("derived access control ON with %s", tc.name)
			}
		})
	}
}

// A blocker has to name the step AND the command, because the first pass
// scaffolds nothing: this text is the entire setup path.
func TestAccessControlBlockersNameTheStepAndTheCommand(t *testing.T) {
	cases := []struct {
		name       string
		mutan      func(*ncStorageProbe)
		wantStep   string
		wantInText string
	}{
		{"missing app", func(p *ncStorageProbe) {
			p.NativeApps = false
			p.Prereqs = []ncPrerequisiteStatus{
				{Name: ncAppGroupFolders, State: ncPrerequisiteEnabled},
				{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
			}
		}, "app_missing:" + ncAppEveryoneGroup, "occ app:install " + ncAppEveryoneGroup},
		{"missing service account", func(p *ncStorageProbe) { p.ServiceAccount = false },
			storageStepServiceAccount, "occ user:add"},
		{"missing universal group", func(p *ncStorageProbe) { p.EveryoneGroup = false },
			storageStepUniversalGroup, ncAppEveryoneGroup},
		{"missing Team folder", func(p *ncStorageProbe) { p.FolderPresent = false },
			storageStepGroupFolder, "occ groupfolders:create " + ncRecordingsMount},
		{"missing owner mount", func(p *ncStorageProbe) { p.OwnerAll = false },
			"mount_mapping:" + ncRecordingsOwnerGroup, "occ groupfolders:group 7 " + ncRecordingsOwnerGroup},
		{"missing everyone mount", func(p *ncStorageProbe) { p.EveryoneRead = false },
			"mount_mapping:" + ncRecordingsEveryoneGroup, "occ groupfolders:group 7 " + ncRecordingsEveryoneGroup + " read"},
		{"advanced ACL off", func(p *ncStorageProbe) { p.ACLEnabled = false },
			storageStepFolderACL, "occ groupfolders:permissions 7 --enable"},
		{"owner is not an ACL manager", func(p *ncStorageProbe) { p.OwnerManages = false },
			storageStepFolderManager, "occ groupfolders:permissions 7 -m --user " + ncRecordingsOwner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := readyProbe()
			tc.mutan(&probe)
			ok, step, detail := probe.accessControlReady()
			if ok {
				t.Fatalf("accessControlReady() = true with %s", tc.name)
			}
			if step != tc.wantStep {
				t.Fatalf("step = %q, want %q", step, tc.wantStep)
			}
			if !strings.Contains(detail, tc.wantInText) {
				t.Fatalf("detail %q does not tell the administrator to run %q", detail, tc.wantInText)
			}
		})
	}
}

// The default model needs exactly one thing, and it needs it whether or not any
// third-party app is installed — which is the whole point of the deps-free
// model.
func TestDefaultModeNeedsOnlyTheServiceAccount(t *testing.T) {
	probe := ncStorageProbe{AdminUser: "admin", ServiceAccount: true, FolderProbed: true}
	if ok, step, detail := probe.defaultReady(); !ok {
		t.Fatalf("defaultReady() = false (%s: %s) on an instance with neither app but a service account", step, detail)
	}

	probe.ServiceAccount = false
	ok, step, detail := probe.defaultReady()
	if ok {
		t.Fatal("defaultReady() = true with no service account")
	}
	if step != storageStepServiceAccount {
		t.Fatalf("step = %q, want %q", step, storageStepServiceAccount)
	}
	if !strings.Contains(detail, ncRecordingsOwner) {
		t.Fatalf("detail %q does not name the account that is missing", detail)
	}
}

// Running in default mode with a mounted Team folder is the mismatch that has
// to be caught: the mount wins the canonical path, so every write the default
// model believes it is making into a private home lands inside the shared
// folder instead (measured, D-660).
func TestSanityCatchesDefaultModeUnderAMountedTeamFolder(t *testing.T) {
	probe := readyProbe()

	ok, step, detail := probe.sanity(false)
	if ok {
		t.Fatal("sanity(default) = true while a Cassini Team folder is mapped to a group")
	}
	if !strings.HasPrefix(step, storageStepModeMismatch) {
		t.Fatalf("step = %q, want a %q-prefixed step", step, storageStepModeMismatch)
	}
	if !strings.Contains(detail, ncRecordingsMount) {
		t.Fatalf("detail %q does not name the folder that is in the way", detail)
	}

	// The same instance passes once nothing is mounted over the path.
	probe.FolderMounted = false
	if ok, step, detail := probe.sanity(false); !ok {
		t.Fatalf("sanity(default) = false (%s: %s) with no mount in the way", step, detail)
	}
}

// The default model's safety rests on nothing being mounted over the canonical
// path. "We could not look" is not evidence of that, and treating it as such is
// how an access-controlled archive gets served to everybody: the probe skips
// the Team-folder read, FolderMounted stays false, sanity passes, and the read
// proxy switches to reading as the owner.
func TestSanityRefusesDefaultModeWhenTheFolderCouldNotBeLookedAt(t *testing.T) {
	probe := ncStorageProbe{AdminUser: "admin", ServiceAccount: true, FolderProbed: false}

	ok, step, detail := probe.sanity(false)
	if ok {
		t.Fatal("sanity(default) = true without ever answering whether a Team folder is mounted")
	}
	if step != storageStepModeMismatch+":"+storageStepFolderUnknown {
		t.Fatalf("step = %q, want %q", step, storageStepModeMismatch+":"+storageStepFolderUnknown)
	}
	if !strings.Contains(detail, ncRecordingsMount) {
		t.Fatalf("detail %q does not name what could not be checked", detail)
	}
}

// Switching INTO default mode is exactly the operation that clears a mounted
// folder, so it must not be refused by the mount it is about to remove.
func TestSanityForTargetDoesNotBlockTheOptOutOnItsOwnPrecondition(t *testing.T) {
	probe := readyProbe()
	if ok, step, detail := probe.sanityForTarget(false); !ok {
		t.Fatalf("sanityForTarget(default) = false (%s: %s) — the opt-out cannot be blocked by the mount it removes", step, detail)
	}
	if ok, _, _ := probe.sanity(false); ok {
		t.Fatal("sanity(default) must still refuse to RUN in default mode under a mounted folder")
	}
}

func TestStorageModeInstructionsListEveryMissingPrerequisite(t *testing.T) {
	probe := ncStorageProbe{
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
		},
	}
	lines := strings.Join(storageModeInstructions(true, probe), "\n")
	for _, want := range []string{
		"occ user:add --group=" + ncRecordingsOwnerGroup + " " + ncRecordingsOwner,
		"occ app:install " + ncAppGroupFolders,
		"occ app:install " + ncAppEveryoneGroup,
		"occ groupfolders:create " + ncRecordingsMount,
		"occ groupfolders:permissions <id> --enable",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("access-control instructions are missing %q:\n%s", want, lines)
		}
	}

	// The default model needs no app and no folder — only the account.
	defaultLines := strings.Join(storageModeInstructions(false, probe), "\n")
	if !strings.Contains(defaultLines, "occ user:add") {
		t.Errorf("default instructions must still create the service account:\n%s", defaultLines)
	}
	if strings.Contains(defaultLines, "groupfolders") || strings.Contains(defaultLines, "app:install") {
		t.Errorf("default instructions must not ask for Team folders or third-party apps:\n%s", defaultLines)
	}
}

func TestRecordingsTreeDirsWalkTheRootOutermostFirst(t *testing.T) {
	got := recordingsTreeDirs("Cassini/Recordings")
	want := []string{"Cassini", "Cassini/Recordings", "Cassini/Recordings/meetings"}
	if len(got) != len(want) {
		t.Fatalf("recordingsTreeDirs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recordingsTreeDirs() = %v, want %v", got, want)
		}
	}
	// Derived from the root, not hard-coded: the transition builds the same
	// shape under a staging name that must NOT be the canonical mount point.
	staged := recordingsTreeDirs(ncStorageStagingRoot + "/Recordings")
	if staged[0] != ncStorageStagingRoot {
		t.Fatalf("recordingsTreeDirs(staging)[0] = %q, want %q", staged[0], ncStorageStagingRoot)
	}
}
