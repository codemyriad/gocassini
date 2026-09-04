package operator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// One read-only look at what the Nextcloud side of this install actually is
// (D-616 first pass).
//
// Before the opt-in there was nothing to look at: there was one storage model,
// provisioning built it, and the only question was whether each build step
// worked. With two models the question comes first — WHICH model is this
// instance set up for? — and it has to be answered without changing anything,
// because the answer is what decides whether a change would be safe.
//
//	                       ┌───────────────────────────────┐
//	                       │  administrator (act-as probe) │
//	                       └───────────────┬───────────────┘
//	                                       │
//	   ┌───────────────────────────────────┼───────────────────────────────┐
//	   ▼                                   ▼                               ▼
//	native apps                     `cassini` account                group folder
//	groupfolders                    exists?                          mount `Cassini`
//	group_everyone                                                   acl on?
//	   │                                   │                         everyone READ?
//	   │                            `everyone` group                 cassini ALL?
//	   │                            exists?                          cassini manages?
//	   └───────────────────────────────────┼───────────────────────────────┘
//	                                       ▼
//	                    Cassini/Recordings          (in the Team folder)
//	                    CassiniNoACL/Recordings     (the private home tree)
//	                    both read as `cassini`; they cannot shadow each other
//
// Every call here is a GET or a PROPFIND. Nothing is created, nothing is
// mapped, nothing is PROPPATCHed. That is the first-pass contract: the
// prerequisites for either model are the administrator's to set up, and the app
// only says which ones are missing.

// ncStorageProbe is what one probe run learned. Every field is an observation,
// never a decision — the decisions are accessControlReady/defaultReady below,
// so a caller can report the same facts under either mode.
type ncStorageProbe struct {
	// AdminUser is the account the probe resolved and acted as.
	AdminUser string
	// Prereqs is the per-app native prerequisite report, in the same shape
	// /status has carried since D-585.
	Prereqs []ncPrerequisiteStatus
	// NativeApps is true when both groupfolders and group_everyone are enabled.
	NativeApps bool
	// ServiceAccount is true when the `cassini` account exists. It is the one
	// prerequisite BOTH models need: every WebDAV write acts as it, in a Team
	// folder and in a private home alike.
	ServiceAccount bool
	// OwnerGroup is true when the narrow `cassini` group exists. Tracked apart
	// from the account because the two can genuinely come apart — an
	// administrator who ran `occ user:add` without `--group` has one and not the
	// other — and the Team folder's write mapping is onto the GROUP, so a plan
	// that inferred the group from the account would emit a mapping step for a
	// group that does not exist.
	OwnerGroup bool
	// EveryoneGroup is true when the virtual all-users group is present. Only
	// checked when group_everyone is enabled — an ordinary group of that name
	// would be a trap, not a substitute (see nc_provision.go step 1).
	EveryoneGroup bool

	// FolderProbed says the Team-folder question was ANSWERED — either the
	// folder list was read, or `groupfolders` is definitely not enabled and so
	// nothing can be mounted. It is NOT the same as FolderPresent being false,
	// and conflating the two is how "we could not look" becomes "there is
	// nothing there": the default model's whole safety argument is that no Team
	// folder shadows the service account's home, and an unanswered question is
	// not evidence of that.
	FolderProbed bool
	// Folder and the booleans under it describe the `Cassini` Team folder.
	// FolderPresent says it exists; FolderMounted says at least one group maps
	// to it, which is what makes it appear in anybody's Files — and therefore
	// what makes it shadow a same-named home directory.
	Folder        gfFolder
	FolderPresent bool
	FolderMounted bool
	ACLEnabled    bool
	EveryoneRead  bool
	OwnerAll      bool
	OwnerManages  bool

	// DefaultRootShadowed is true when a Team folder is mounted over the DEFAULT
	// model's own root — the one thing that could stop `CassiniNoACL/Recordings`
	// being the service account's private directory.
	//
	// It should never be true. Cassini never creates a folder there and nothing
	// suggests one; it exists because the default model's entire safety argument
	// is "this tree is private", and that claim is worth checking rather than
	// assuming. Note the asymmetry with the first pass: the check used to be
	// about `Cassini`, which the access-controlled model legitimately mounts, so
	// an unanswered question had to be treated as dangerous. Nothing legitimately
	// mounts anything here, so an unanswered question is not evidence of a
	// hazard — see ncStorageServesAsOwner.
	DefaultRootShadowed bool
	// DefaultRootProbed says the question above was ANSWERED. It is the same
	// distinction FolderProbed draws, for the same reason: DefaultRootShadowed is
	// only ever assigned when the folder list was actually read, so a `false`
	// otherwise means "we could not look", and reading that as "nothing is
	// mounted" is how an unanswered question becomes a clean bill of health.
	DefaultRootProbed bool

	// ACLRecordingsRoot is true when the access-controlled archive's meetings
	// collection is reachable as the service account, and ACLArchiveMeetings is
	// how many recordings are in it.
	//
	// The COUNT is what makes the upgrade latch possible. An access-controlled
	// install that upgrades into this build has nothing recorded, falls back to
	// `default`, and — since the roots no longer collide — would find nothing in
	// its way and quietly start publishing into a fresh empty private tree while
	// its real archive sat unread in the Team folder. Knowing the Team folder
	// still holds recordings is what turns that into a refusal.
	ACLRecordingsRoot  bool
	ACLArchiveMeetings int
	// DefaultRecordingsRoot and DefaultArchiveMeetings are the same pair for the
	// default model's own root. Its absence is not a fault: the tree is created
	// on demand by the preflight.
	DefaultRecordingsRoot  bool
	DefaultArchiveMeetings int
}

// prereqEnabled reports whether one native app was positively reported as
// enabled. An `unknown` state — the check itself failed — is not enabled.
func prereqEnabled(prereqs []ncPrerequisiteStatus, name string) bool {
	for _, p := range prereqs {
		if p.Name == name {
			return p.State == ncPrerequisiteEnabled
		}
	}
	return false
}

// prereqsAnswered reports whether Nextcloud actually told us which apps are
// enabled. It is the difference between "that app is off" and "we could not
// ask", which for the Team-folder question decides whether a `false` means
// anything at all.
func prereqsAnswered(prereqs []ncPrerequisiteStatus) bool {
	if len(prereqs) == 0 {
		return false
	}
	for _, p := range prereqs {
		if p.State == ncPrerequisiteUnknown {
			return false
		}
	}
	return true
}

// storageProbeStep is a machine-readable name for the thing that is missing,
// in the same vocabulary /status has always used for provisioning steps, so a
// monitor or a test can key on it.
const (
	storageStepServiceAccount = "owner_account"
	storageStepUniversalGroup = "universal_group"
	storageStepGroupFolder    = "group_folder"
	storageStepFolderACL      = "group_folder_acl"
	storageStepFolderManager  = "group_folder_manager"
	// `group_folder_mount` and `group_folder_unknown` used to live here. Both
	// were reasons to refuse the DEFAULT model — a `Cassini` Team folder was
	// mounted over the path it wrote to, or nobody could say whether one was.
	// Since the two models have separate roots, neither question is about the
	// default model's tree any more, and neither is emitted. They are named here
	// rather than silently dropped because a monitor keyed on them will now see
	// nothing, which is the correct outcome and an alarming one to discover.
	//
	// storageStepDefaultRootShadowed means a Team folder is mounted over the
	// default model's own root, which is the one thing that could stop it being
	// the service account's private directory. It replaces the first pass's
	// `group_folder_mount`, which fired whenever the ACCESS-CONTROLLED model's
	// folder was mounted — a state that is now perfectly ordinary, because an
	// opt-out leaves that folder in place, emptied.
	storageStepDefaultRootShadowed = "default_root_shadowed"
	// storageStepDefaultRootUnknown means nobody could say whether anything is
	// mounted over the default model's root. Disqualifying for a WRITE, because
	// the model's safety argument is that the tree is private and this is the
	// only thing that checks it.
	storageStepDefaultRootUnknown = "default_root_unknown"
	// storageStepModeMismatch means the recorded mode and the storage disagree.
	// Nothing is missing; the two just are not the same thing, and writing
	// under that disagreement is how recordings end up somewhere nobody is
	// looking (or somewhere everybody can read).
	storageStepModeMismatch = "mode_mismatch"
)

// probeNCStorage answers "what is this instance set up for", acting as the
// resolved administrator for the OCS/Group-Folders reads and as the service
// account for the one WebDAV read.
//
// It returns an error ONLY when the administrator could not be resolved. A
// missing app, a missing account, an absent folder are all ANSWERS — the
// probe's whole job — and each lands in the struct rather than in err.
func (c ExAppConfig) probeNCStorage(ctx context.Context, client *http.Client, logger *log.Logger) (ncStorageProbe, error) {
	var probe ncStorageProbe

	admin, err := c.resolveAdminIdentity(ctx, client, logger)
	if err != nil {
		return probe, err
	}
	probe.AdminUser = admin

	prereqs, perr := c.preflightNativeApps(ctx, client)
	probe.Prereqs = prereqs
	probe.NativeApps = perr == nil

	// The service account first, and unconditionally. It is the prerequisite
	// the default model rests on entirely, and asking about it after the native
	// apps — which is the order provisioning used — meant a deps-free instance
	// returned before anything ever looked (D-616 triage, correction 2).
	if exists, err := c.userExists(ctx, client, ncRecordingsOwner); err != nil {
		logger.Printf("nc storage: check service account %q: %v", ncRecordingsOwner, err)
	} else {
		probe.ServiceAccount = exists
	}

	if exists, err := c.groupExists(ctx, client, ncRecordingsOwnerGroup); err != nil {
		logger.Printf("nc storage: check owner group %q: %v", ncRecordingsOwnerGroup, err)
	} else {
		probe.OwnerGroup = exists
	}

	if prereqEnabled(prereqs, ncAppEveryoneGroup) {
		if exists, err := c.groupExists(ctx, client, ncRecordingsEveryoneGroup); err != nil {
			logger.Printf("nc storage: check universal group %q: %v", ncRecordingsEveryoneGroup, err)
		} else {
			probe.EveryoneGroup = exists
		}
	}

	// The Team folder is read on its own condition, not on NativeApps.
	//
	// Bundling it with the Everyone Group app is what made this dangerous: on an
	// instance where `group_everyone` is off but `groupfolders` is on and a
	// mapped `Cassini` folder is still shadowing the canonical path, the folder
	// was never looked at, `FolderMounted` stayed false, and an
	// access-controlled archive read as an unmounted one — which the default
	// model then serves to everybody. The two apps answer different questions
	// and are asked separately.
	switch {
	case !prereqsAnswered(prereqs):
		// Nextcloud did not say which apps are enabled, so we cannot even
		// conclude that a Team folder is impossible. Unanswered, not absent.
		probe.FolderProbed = false
	case !prereqEnabled(prereqs, ncAppGroupFolders):
		// The app is not enabled, so no Team folder is mounted anywhere. That is
		// an answer to BOTH folder questions, and it is the one that makes a
		// deps-free instance usable.
		probe.FolderProbed = true
		probe.DefaultRootProbed = true
	default:
		// One listing, two questions. The access-controlled model's `Cassini`
		// folder, and whether anything at all has been mounted over the default
		// model's own root — asking twice would cost two round trips and could
		// return two answers that disagree.
		folders, err := c.listFolders(ctx, client, ncRecordingsMount)
		if err != nil {
			logger.Printf("nc storage: list Team folders: %v", err)
			break
		}
		probe.FolderProbed = true
		if folder, ok := lowestIDMatch(folders, ncRecordingsMount); ok {
			probe.Folder = folder
			probe.FolderPresent = true
			probe.ACLEnabled = folder.ACL
			everyonePerms, everyoneMapped := folder.groupPerms(ncRecordingsEveryoneGroup)
			ownerPerms, ownerMapped := folder.groupPerms(ncRecordingsOwnerGroup)
			probe.EveryoneRead = everyoneMapped && everyonePerms&aclPermRead != 0
			probe.OwnerAll = ownerMapped && ownerPerms == aclMaskAll
			probe.FolderMounted = folder.anyGroupMapped()
			probe.OwnerManages = folder.hasManager("user", ncRecordingsOwner)
		}
		probe.DefaultRootProbed = true
		if shadow, ok := lowestIDMatch(folders, ncDefaultRecordingsMount); ok {
			probe.DefaultRootShadowed = shadow.anyGroupMapped()
		}
	}

	// Two WebDAV reads, one per model's root. They answer as the service account,
	// so an instance where the account does not exist would only get a 401 — skip
	// them and let the missing account be the diagnosis.
	if probe.ServiceAccount {
		// The meetings collection rather than the root, because the interesting
		// question about the other model's tree is not "does it exist" but "does
		// it still hold recordings" — see ACLArchiveMeetings.
		//
		// Counted with davPropfindChildren, not davPropfindNames: the latter keeps
		// only `.opus` basenames, and an archive may carry a legacy
		// directory-shaped export. Everything that MOVES an archive counts with
		// Children, so a safety net counting with Names would decide "there is
		// nothing here" about a tree the copy would then carry.
		if names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, ncACLRecordingsRoot+"/meetings"); err != nil {
			logger.Printf("nc storage: inspect %s as %q: %v", ncACLRecordingsRoot, ncRecordingsOwner, err)
		} else {
			probe.ACLRecordingsRoot = visible
			probe.ACLArchiveMeetings = len(names)
		}
		if names, visible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, ncDefaultRecordingsRoot+"/meetings"); err != nil {
			logger.Printf("nc storage: inspect %s as %q: %v", ncDefaultRecordingsRoot, ncRecordingsOwner, err)
		} else {
			probe.DefaultRecordingsRoot = visible
			probe.DefaultArchiveMeetings = len(names)
		}
	}
	return probe, nil
}

// accessControlReady reports whether every prerequisite of the access-
// controlled model is satisfied, and when it is not, the step to act on plus a
// sentence saying what to do about it.
//
// The order is the order an administrator installs them in, so the first thing
// reported is the first thing to do.
func (p ncStorageProbe) accessControlReady() (ok bool, step, detail string) {
	if missing := firstMissingApp(p.Prereqs); missing != "" {
		return false, "app_missing:" + missing,
			fmt.Sprintf("the %q app is not enabled; an ExApp cannot install it — run `occ app:install %s && occ app:enable %s`", missing, missing, missing)
	}
	if !p.NativeApps {
		return false, "app_check_failed",
			"Nextcloud did not answer which apps are enabled, so the access-controlled prerequisites could not be checked"
	}
	if !p.ServiceAccount {
		return false, storageStepServiceAccount, missingServiceAccountDetail()
	}
	if !p.EveryoneGroup {
		return false, storageStepUniversalGroup,
			fmt.Sprintf("the universal group %q does not exist; the %s app is enabled but produced no group", ncRecordingsEveryoneGroup, ncAppEveryoneGroup)
	}
	if !p.FolderPresent {
		return false, storageStepGroupFolder,
			fmt.Sprintf("there is no %q Team folder — create it with `occ groupfolders:create %s`", ncRecordingsMount, ncRecordingsMount)
	}
	folderID, _ := p.Folder.idInt()
	if !p.OwnerAll {
		return false, "mount_mapping:" + ncRecordingsOwnerGroup,
			fmt.Sprintf("the %q group has no write mount of the %q Team folder — run `occ groupfolders:group %d %s read write share delete`", ncRecordingsOwnerGroup, ncRecordingsMount, folderID, ncRecordingsOwnerGroup)
	}
	if !p.EveryoneRead {
		return false, "mount_mapping:" + ncRecordingsEveryoneGroup,
			fmt.Sprintf("the %q group has no read mount of the %q Team folder, so nobody can traverse to the recordings — run `occ groupfolders:group %d %s read`", ncRecordingsEveryoneGroup, ncRecordingsMount, folderID, ncRecordingsEveryoneGroup)
	}
	if !p.ACLEnabled {
		return false, storageStepFolderACL,
			fmt.Sprintf("advanced ACL is off on the %q Team folder, so there is no default-deny floor and every account could read every recording — run `occ groupfolders:permissions %d --enable`", ncRecordingsMount, folderID)
	}
	if !p.OwnerManages {
		return false, storageStepFolderManager,
			fmt.Sprintf("%q is not an ACL manager of the %q Team folder, so it cannot write a recording's audience — run `occ groupfolders:permissions %d -m --user %s`", ncRecordingsOwner, ncRecordingsMount, folderID, ncRecordingsOwner)
	}
	return true, "", ""
}

// defaultReady reports whether the deps-free model can be used. It needs one
// thing: the account that owns the tree. The tree itself is created on demand.
func (p ncStorageProbe) defaultReady() (ok bool, step, detail string) {
	if !p.ServiceAccount {
		return false, storageStepServiceAccount, missingServiceAccountDetail()
	}
	return true, "", ""
}

// strandedArchiveMeetings reports how many recordings are sitting in the model
// that is NOT in force. Zero is the ordinary answer.
func (p ncStorageProbe) strandedArchiveMeetings(accessControlled bool) int {
	if accessControlled {
		return p.DefaultArchiveMeetings
	}
	// Only a MOUNTED Team folder counts, and only when the mount question was
	// actually answered. An unmounted folder is not reachable by anybody,
	// including the switch that would carry its contents across, so reporting it
	// would offer an action that cannot work — but an UNANSWERED one is not an
	// unmounted one, and staying quiet there hides the archive rather than
	// avoiding a bad suggestion.
	if p.FolderProbed && !p.FolderMounted {
		return 0
	}
	return p.ACLArchiveMeetings
}

// storageStepStrandedACLArchive names the upgrade latch: this instance fell back
// to the default model because nothing had recorded one, but its Team folder
// still holds recordings — so it is almost certainly an access-controlled
// install that has never been told which model it is in.
const storageStepStrandedACLArchive = "access_controlled_archive"

// unclaimedAccessControlledArchive reports whether a FALLBACK default mode would
// strand an existing access-controlled archive.
//
// This is the upgrade latch, and it is the one thing the path split took away
// that had to be put back. In the first pass the latch was free: both models
// wanted `Cassini/Recordings`, so an access-controlled install falling back to
// `default` found its own Team folder in the way and refused. With separate
// roots nothing is in the way — the fallback would find an empty private tree,
// report itself healthy, and publish into it while every existing recording sat
// unread in the Team folder. No disclosure, but a silently vanished archive,
// which is not a better failure.
//
// It applies ONLY to the fallback. A recorded or declared `default` on an
// instance whose Team folder still holds recordings is an administrator's
// decision plus a tidy-up (see migration_clean), not a misconfiguration.
func (p ncStorageProbe) unclaimedAccessControlledArchive() (bool, string) {
	if p.ACLArchiveMeetings == 0 {
		return false, ""
	}
	// FolderMounted is false both when nothing is mounted and when the folder
	// list could not be read, and only the first of those means "this archive is
	// not in a Team folder". The count comes from a WebDAV PROPFIND, which
	// answered — so on an unanswered folder question the recordings are known to
	// exist at the access-controlled root and their storage is unknown, which is
	// precisely when a fallback `default` must not be recorded and never
	// reconsidered.
	if p.FolderProbed && !p.FolderMounted {
		return false, ""
	}
	return true, fmt.Sprintf(
		"nothing has recorded which storage mode this instance uses, so Cassini fell back to %q — but the %q Team folder still holds %d recording(s), which the default mode does not read. This is what an access-controlled installation looks like to a Cassini that has not been told. Turn access control on in the Setup tab, or set %s=%s and re-enable the app. If this instance really is meant to be open, turn access control on first and then switch back to the default mode: that switch is what copies those recordings into the open tree",
		storageModeDefault, ncRecordingsMount, p.ACLArchiveMeetings, envStorageMode, storageModeAccessControlled)
}

func missingServiceAccountDetail() string {
	return fmt.Sprintf(
		"the %q service account does not exist; every recording is written and read as it, so nothing can be stored without it — create it with `occ group:add %s` and `occ user:add --group=%s %s`",
		ncRecordingsOwner, ncRecordingsOwnerGroup, ncRecordingsOwnerGroup, ncRecordingsOwner)
}

// The probe does not decide the mode. It used to — deriveAccessControlEnabled()
// answered "this instance has the whole access-controlled substrate, so it must
// want access control" — and that made who can read the archive a function of
// what Nextcloud looked like at one instant. A substrate built with `occ`
// moments earlier may not have reached the web workers this probe asks, so the
// answer was a race, and it was permanent once recorded.
//
// What replaced it is deliberately duller: the mode comes from the settings file
// or CASSINI_STORAGE_MODE or nothing, and the probe's only job is sanity() below
// — does the storage match the mode it was told? A disagreement is reported, not
// resolved.

// sanity compares a mode against the storage and reports the disagreement.
//
// The two failures it can name are different in kind:
//
//	access controlled, not ready   something the model needs is missing. The
//	                               step names it and the detail says what to run.
//	default, root shadowed         nothing is missing — but a Team folder is
//	                               mounted over `CassiniNoACL`, which is the one
//	                               thing that could stop the default model's tree
//	                               being private. A mounted Team folder wins the
//	                               path and the home directory of the same name
//	                               is renamed out of the way (measured, D-660),
//	                               so writes would land in a shared folder and
//	                               owner-identity reads would serve it to
//	                               everybody.
//
// What is deliberately NOT a mismatch any more: a mounted `Cassini` Team folder
// while the default model is in force. The first pass had to refuse there,
// because both models addressed that path. They no longer do — and an emptied
// `Cassini` folder left mounted is exactly what a completed opt-out looks like,
// so refusing would make every opted-out instance permanently unpublishable.
func (p ncStorageProbe) sanity(accessControlled bool) (ok bool, step, detail string) {
	if accessControlled {
		return p.accessControlReady()
	}
	if ready, step, detail := p.defaultReady(); !ready {
		return false, step, detail
	}
	if !p.DefaultRootProbed {
		// A write is about to be made into a tree whose privacy is the model's
		// whole safety argument, and nobody could confirm it. This gates
		// PUBLISHING (through the substrate verdict), not reading — see
		// ncStorageServesAsOwner for why the read path is deliberately more
		// permissive than this.
		return false, storageStepModeMismatch + ":" + storageStepDefaultRootUnknown,
			fmt.Sprintf(
				"Cassini could not determine whether a Team folder is mounted at %q, which is where the default storage mode keeps its recordings, so it will not assume there is none — one mounted there would put every recording into a shared folder. Check that Nextcloud is answering and re-enable Cassini",
				ncDefaultRecordingsMount)
	}
	if p.DefaultRootShadowed {
		return false, storageStepModeMismatch + ":" + storageStepDefaultRootShadowed,
			fmt.Sprintf(
				"a Team folder is mounted at %q, which is where the default storage mode keeps its recordings. A mounted Team folder wins that path, so recordings would be written into a shared folder rather than %q's own private tree, and everyone mapped to that folder could read them. Unmap or rename that Team folder (`occ groupfolders:list`, then `occ groupfolders:group <id> <group> --delete`), or turn access control on in the Setup tab if this instance was meant to be access-controlled",
				ncDefaultRecordingsMount, ncRecordingsOwner)
	}
	return true, "", ""
}

// summarizeProbe is the one line the operator log carries per preflight, so an
// administrator reading the container log sees the same facts /storage reports.
func summarizeProbe(p ncStorageProbe) string {
	fields := []string{
		fmt.Sprintf("admin=%s", p.AdminUser),
		fmt.Sprintf("service_account=%t", p.ServiceAccount),
		fmt.Sprintf("owner_group=%t", p.OwnerGroup),
		fmt.Sprintf("native_apps=%t", p.NativeApps),
		fmt.Sprintf("everyone_group=%t", p.EveryoneGroup),
		fmt.Sprintf("folder_probed=%t", p.FolderProbed),
		fmt.Sprintf("team_folder=%t", p.FolderPresent),
		fmt.Sprintf("mounted=%t", p.FolderMounted),
		fmt.Sprintf("acl=%t", p.ACLEnabled),
		fmt.Sprintf("acl_root=%t/%d", p.ACLRecordingsRoot, p.ACLArchiveMeetings),
		fmt.Sprintf("default_root=%t/%d", p.DefaultRecordingsRoot, p.DefaultArchiveMeetings),
		fmt.Sprintf("default_root_shadowed=%t", p.DefaultRootShadowed),
	}
	return strings.Join(fields, " ")
}
