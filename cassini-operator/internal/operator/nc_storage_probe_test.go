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
		NativeApps:        true,
		ServiceAccount:    true,
		OwnerGroup:        true,
		EveryoneGroup:     true,
		Folder:            gfFolder{ID: "7", MountPoint: ncRecordingsMount, ACL: true},
		FolderProbed:      true,
		FolderPresent:     true,
		FolderMounted:     true,
		ACLEnabled:        true,
		EveryoneRead:      true,
		OwnerAll:          true,
		OwnerManages:      true,
		ACLRecordingsRoot: true,
	}
}

// A fully provisioned instance can run the access-controlled model.
//
// Note what this does NOT say: nothing here decides the MODE. The mode comes
// from storage_settings.json, or CASSINI_STORAGE_MODE, or the default — never
// from the instance. This is the sanity gate, which answers a different
// question: given a mode, is this storage able to serve it?
func TestAccessControlIsReadyOnAFullyProvisionedInstance(t *testing.T) {
	if ready, step, detail := readyProbe().accessControlReady(); !ready {
		t.Fatalf("a fully provisioned instance is not ready: %s — %s", step, detail)
	}
}

// The spec's own list, one row per bullet: each of these makes the
// access-controlled model unusable, and each must SAY so rather than being
// silently downgraded to the open model.
func TestAccessControlIsNotReadyWhenAnythingIsMissing(t *testing.T) {
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
			p.ACLRecordingsRoot = false
		}},
		{"Cassini/Recordings is a private home tree, not a Team folder", func(p *ncStorageProbe) {
			p.FolderPresent = false
			p.FolderMounted = false
			p.EveryoneRead = false
			p.OwnerAll = false
			p.ACLEnabled = false
			p.OwnerManages = false
			p.DefaultRecordingsRoot = true
		}},
		{"no everyone group", func(p *ncStorageProbe) { p.EveryoneGroup = false }},
		{"advanced ACL is off", func(p *ncStorageProbe) { p.ACLEnabled = false }},
		{"the owner is not an ACL manager", func(p *ncStorageProbe) { p.OwnerManages = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := readyProbe()
			tc.mutan(&probe)
			ready, step, _ := probe.accessControlReady()
			if ready {
				t.Fatalf("reported ready for access control with %s", tc.name)
			}
			if step == "" {
				t.Fatalf("%s was refused without naming a step", tc.name)
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

// A mounted `Cassini` Team folder is no longer a reason to refuse the default
// mode. It was, when both models addressed `Cassini/Recordings`: the mount won
// that path, so every write the default model believed it was making into a
// private home landed inside the shared folder instead (measured, D-660).
//
// Since the roots were split it is not merely harmless, it is the ORDINARY
// state: an opt-out empties the Team folder and leaves it mounted, so refusing
// here would make every opted-out instance permanently unable to publish.
func TestDefaultModeToleratesAMountedTeamFolder(t *testing.T) {
	probe := readyProbe()
	if probe.FolderMounted != true {
		t.Fatal("the fixture is supposed to have a mounted Team folder")
	}
	if ok, step, detail := probe.sanity(false); !ok {
		t.Fatalf("sanity(default) = false (%s: %s) with an emptied Team folder still mounted at %q", step, detail, ncRecordingsMount)
	}
}

// The one mount that DOES disqualify the default model is one over its own root.
// Nothing Cassini does creates it, but the model's whole safety argument is
// "this tree is private", and that claim is worth checking rather than assuming.
func TestSanityCatchesATeamFolderOverTheDefaultRoot(t *testing.T) {
	probe := readyProbe()
	probe.DefaultRootShadowed = true

	ok, step, detail := probe.sanity(false)
	if ok {
		t.Fatalf("sanity(default) = true while a Team folder is mounted at %q", ncDefaultRecordingsMount)
	}
	if step != storageStepModeMismatch+":"+storageStepDefaultRootShadowed {
		t.Fatalf("step = %q, want %q", step, storageStepModeMismatch+":"+storageStepDefaultRootShadowed)
	}
	if !strings.Contains(detail, ncDefaultRecordingsMount) {
		t.Fatalf("detail %q does not name the folder that is in the way", detail)
	}

	probe.DefaultRootShadowed = false
	if ok, step, detail := probe.sanity(false); !ok {
		t.Fatalf("sanity(default) = false (%s: %s) with nothing over the default root", step, detail)
	}
}

// An unanswerable folder list no longer disqualifies the default model, and the
// asymmetry is the point. The first pass had to refuse, because the question was
// about `Cassini` — a path the access-controlled model legitimately mounts, so
// "we could not look" could be hiding an access-controlled archive that reading
// as the owner would hand to everybody. The question is now about
// `CassiniNoACL`, which nothing legitimately mounts, so an unasked question is
// not evidence of a hazard — and treating it as one would blank a working
// archive every time Nextcloud hiccuped.
func TestDefaultModeSurvivesAFolderListItCouldNotRead(t *testing.T) {
	probe := ncStorageProbe{AdminUser: "admin", ServiceAccount: true, FolderProbed: false}
	if ok, step, detail := probe.sanity(false); !ok {
		t.Fatalf("sanity(default) = false (%s: %s) merely because the Team-folder list could not be read", step, detail)
	}
}

// Switching INTO default mode asks the same question as running in it, since
// the split. The two entry points are kept apart anyway, because the asymmetry
// was load-bearing in the first pass and losing the distinction silently is how
// it would come back wrong.
func TestSanityForTargetAgreesWithSanityForTheDefaultMode(t *testing.T) {
	probe := readyProbe()
	if ok, step, detail := probe.sanityForTarget(false); !ok {
		t.Fatalf("sanityForTarget(default) = false (%s: %s)", step, detail)
	}
	if ok, _, _ := probe.sanity(false); !ok {
		t.Fatal("sanity(default) disagreed with sanityForTarget(default)")
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
