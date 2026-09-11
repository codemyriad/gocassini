package operator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
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
	// ServiceAccountAttempt is what the enabled edge's attempt to CREATE the
	// account ran into, or "" when there was nothing to create and nothing was
	// attempted (nc_owner_account.go). It belongs with the facts because it
	// changed them: since D-754 one write happens between this read and the
	// mode-dependent gates, and "Cassini asked and Nextcloud refused" is a
	// different thing for an administrator to read than "it is not there".
	ServiceAccountAttempt string
	// ServiceAccountCreated is true when this run's attempt actually made the
	// account (D-754). It is the strongest evidence of a FRESH install there is:
	// every recording in either model is written and read as that account, so an
	// account that did not exist a moment ago cannot own an archive, and nothing
	// this instance is hiding can be one.
	ServiceAccountCreated bool
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

	// ACLArchive and DefaultArchive are what each model's root actually holds.
	//
	// BOTH are read on every probe, under either mode, so the setup wizard can
	// show an administrator what exists before they choose a mode. A declared
	// development/CI mode is also checked against recordings it was not told
	// about. None of that can be answered by looking at the mode in force.
	ACLArchive     ncArchiveFacts
	DefaultArchive ncArchiveFacts

	// DuplicateNames are the names present under BOTH roots' `meetings`
	// collections, sorted. They make an incompatible development/CI declaration
	// explainable without choosing an archive on the administrator's behalf.
	//
	// Empty is only meaningful when both roots were probed; ArchivesComparable
	// is the guard.
	DuplicateNames []string

	// DeliveredRecordings is the only fact here that does not come from
	// Nextcloud: how many recordings this operator's OWN job database says it
	// has published (Store.CountDeliveredRecordings). DeliveredRecordingsProbed
	// says the count was taken, drawing the same "answered" distinction
	// FolderProbed does — a count nobody could take is not a zero.
	//
	// It answers "has this install been recording here before" for the one
	// resolution arm where Nextcloud cannot be believed: with `groupfolders`
	// disabled a Team-folder archive is invisible, and an operator that has
	// delivered nothing has no archive that could be hiding there.
	DeliveredRecordings       int
	DeliveredRecordingsProbed bool
}

// ncArchiveFacts is one recordings root, as the service account sees it.
type ncArchiveFacts struct {
	// Root is the path, so a caller reporting these facts does not have to know
	// which mode they belong to.
	Root string
	// Probed says the `meetings` collection was actually listed. False means
	// nobody could look — never that the tree is empty. Every writer fails
	// closed on it; readers deliberately do not (see ncStorageServesAsOwner).
	Probed bool
	// Present says the collection exists. A root that has never been used is
	// absent rather than empty, and that is not a fault: the preflight creates
	// it on demand.
	Present bool
	// Entries is every child of `meetings`.
	Entries []davEntry
	// Catalog says a catalog.json sits beside `meetings`. It is the only index
	// there is, so migration must copy or remove it along with the recordings.
	Catalog bool
	// CatalogProbed distinguishes "there is no catalog" from "the root could not
	// be listed", for the same reason Probed exists.
	CatalogProbed bool
}

// Meetings is how many recordings are under this root.
func (a ncArchiveFacts) Meetings() int { return len(a.Entries) }

// Names is the sorted basenames, for set arithmetic and for messages.
func (a ncArchiveFacts) Names() []string {
	out := make([]string, 0, len(a.Entries))
	for _, entry := range a.Entries {
		out = append(out, entry.Name)
	}
	sort.Strings(out)
	return out
}

// Populated reports whether this root holds recordings. It answers false for an
// unprobed root, so every caller must ask Probed as well when the difference
// matters — which for a write it always does.
func (a ncArchiveFacts) Populated() bool { return a.Probed && len(a.Entries) > 0 }

// archiveFor names the facts for one model.
func (p ncStorageProbe) archiveFor(accessControlled bool) ncArchiveFacts {
	if accessControlled {
		return p.ACLArchive
	}
	return p.DefaultArchive
}

// ArchivesComparable reports whether both roots were read, which is what makes
// DuplicateNames and "content in both directories" answerable at all.
func (p ncStorageProbe) ArchivesComparable() bool {
	return p.ACLArchive.Probed && p.DefaultArchive.Probed
}

// duplicateNames is the intersection of two sorted name sets.
func duplicateNames(a, b ncArchiveFacts) []string {
	if len(a.Entries) == 0 || len(b.Entries) == 0 {
		return nil
	}
	present := make(map[string]bool, len(b.Entries))
	for _, entry := range b.Entries {
		present[entry.Name] = true
	}
	var out []string
	for _, entry := range a.Entries {
		if present[entry.Name] {
			out = append(out, entry.Name)
		}
	}
	sort.Strings(out)
	return out
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

// ncDeliveredRecordings is how the probe asks the operator's own job database
// how much it has published, without a Store to ask.
//
// A package-level hook for the same reason ncStorage and ncAccessSubstrate are
// package-level: the enabled edge runs from an ExAppConfig, which carries no
// Store and no Runtime, and threading one through every probe to answer a
// single yes/no question would be a larger change than the question is worth.
// Run() registers the store's counter once; a process that never opened one
// (and every test that does not care) leaves it nil, which reads as "could not
// count" rather than as zero.
var ncDeliveredRecordings struct {
	mu    sync.RWMutex
	count func(context.Context) (int, error)
}

// setDeliveredRecordingsCounter registers the job database's counter. Passing
// nil unregisters it, which is what a test restoring the previous value does.
func setDeliveredRecordingsCounter(count func(context.Context) (int, error)) {
	ncDeliveredRecordings.mu.Lock()
	ncDeliveredRecordings.count = count
	ncDeliveredRecordings.mu.Unlock()
}

// countDeliveredRecordings answers the count and whether anybody could take it.
func countDeliveredRecordings(ctx context.Context) (int, bool, error) {
	ncDeliveredRecordings.mu.RLock()
	count := ncDeliveredRecordings.count
	ncDeliveredRecordings.mu.RUnlock()
	if count == nil {
		return 0, false, nil
	}
	n, err := count(ctx)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

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

	// Both roots, on every probe, under either mode. They answer as the service
	// account, so an instance where the account does not exist would only get a
	// 401 — skip them and let the missing account be the diagnosis.
	probe.ACLArchive.Root = ncACLRecordingsRoot
	probe.DefaultArchive.Root = ncDefaultRecordingsRoot
	if probe.ServiceAccount {
		c.probeArchives(ctx, client, &probe, logger)
	}

	// This operator's own history, which Nextcloud cannot be asked for and which
	// no amount of it being switched off can change.
	if count, probed, err := countDeliveredRecordings(ctx); err != nil {
		logger.Printf("nc storage: count delivered recordings: %v", err)
	} else {
		probe.DeliveredRecordings = count
		probe.DeliveredRecordingsProbed = probed
	}
	return probe, nil
}

// probeArchives reads both recordings roots as the service account and derives
// the one fact that needs the two of them.
//
// Split out of probeNCStorage because the account can come into existence DURING
// the enabled edge (D-754): the probe that ran before the create skipped both
// roots, so ArchivesComparable() is false and a mode cannot be resolved from it.
// Re-reading them as the account that now exists is what turns a fresh install
// into `default` rather than into `storage_mode_unresolved`.
func (c ExAppConfig) probeArchives(ctx context.Context, client *http.Client, probe *ncStorageProbe, logger *log.Logger) {
	probe.ACLArchive = c.probeArchiveAt(ctx, client, ncACLRecordingsRoot, logger)
	probe.DefaultArchive = c.probeArchiveAt(ctx, client, ncDefaultRecordingsRoot, logger)
	probe.DuplicateNames = nil
	if probe.ArchivesComparable() {
		probe.DuplicateNames = duplicateNames(probe.ACLArchive, probe.DefaultArchive)
	}
}

// probeArchiveAt reads one recordings root: what is under `meetings`, when each
// entry was last written, and whether a catalog sits beside it.
//
// The meetings collection rather than the root, because the interesting question
// about a model's tree is not "does it exist" but "does it still hold
// recordings".
//
// Listed with davPropfindEntries, not davPropfindNames: the latter keeps only
// `.opus` basenames, and an archive may carry a legacy directory-shaped export.
// Everything that MOVES an archive works from this listing, so a probe that
// filtered by extension would decide "there is nothing here" about a tree the
// copy would then carry.
func (c ExAppConfig) probeArchiveAt(ctx context.Context, client *http.Client, root string, logger *log.Logger) ncArchiveFacts {
	facts := ncArchiveFacts{Root: root}
	entries, visible, err := c.davPropfindEntries(ctx, client, ncRecordingsOwner, root+"/meetings")
	if err != nil {
		// Not probed. A failed listing is not an empty tree, and the whole
		// conflict model rests on that distinction.
		logger.Printf("nc storage: inspect %s as %q: %v", root, ncRecordingsOwner, err)
		return facts
	}
	facts.Probed = true
	facts.Present = visible
	facts.Entries = entries

	siblings, siblingsVisible, err := c.davPropfindChildren(ctx, client, ncRecordingsOwner, root)
	if err != nil {
		logger.Printf("nc storage: inspect %s as %q: %v", root, ncRecordingsOwner, err)
		return facts
	}
	facts.CatalogProbed = true
	if siblingsVisible {
		for _, name := range siblings {
			if name == ncSiteCatalogName {
				facts.Catalog = true
				break
			}
		}
	}
	return facts
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
		return false, storageStepServiceAccount, p.serviceAccountDetail()
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
		return false, storageStepServiceAccount, p.serviceAccountDetail()
	}
	return true, "", ""
}

// strandedArchiveMeetings reports how many recordings are sitting in the model
// that is NOT in force. Zero is the ordinary answer.
func (p ncStorageProbe) strandedArchiveMeetings(accessControlled bool) int {
	if accessControlled {
		return p.DefaultArchive.Meetings()
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
	return p.ACLArchive.Meetings()
}

// storageStepModeUndecided and storageStepModeUnconfirmed are no longer
// emitted (D-753). They were the two states in which an install refused to
// record and to publish because nobody had answered the setup wizard: nothing
// was recorded, or something was recorded that nobody had chosen. The enabled
// edge now resolves the mode from the archive it finds (storageModeFromProbe)
// and keeps any recorded one, so neither state is reachable.
//
// They are named here rather than deleted because recordingRefusal still guards
// against either coming back — re-introducing one must not silently stop every
// recording again — and because a monitor keyed on either will now see nothing,
// which is the correct outcome and an alarming one to discover.
const storageStepModeUndecided = "storage_mode_undecided"

const storageStepModeUnconfirmed = "storage_mode_unconfirmed"

// storageStepModeUnresolved means the probe could not say what this install
// already holds, so no mode was resolved on this edge.
//
// Recorded as DEGRADED rather than unavailable, and the difference is the whole
// point: nothing is missing, an answer simply did not arrive. Recording goes
// ahead (recordingRefusal), publishing waits, and the next enabled edge looks
// again. Writing an archive under a mode nobody could establish is the one step
// here that cannot be taken back.
const storageStepModeUnresolved = "storage_mode_unresolved"

// storageModeFromProbe resolves the storage model of an install that has
// recorded none and declared none, from what the read-only probe found (D-753).
// `ok` is false when the probe could not answer; `why` is the evidence, for the
// log and for /status.
//
// One rule underneath every branch: Cassini never widens an existing archive on
// its own. Every answer either keeps the audience the recordings already have,
// or starts an empty archive open.
//
//	recordings in the MOUNTED Team folder  access controlled. Adopted — the
//	                                       archive stays exactly where it is, and
//	                                       anything in the default root is
//	                                       reported as stranded rather than moved.
//	recordings at Cassini/Recordings with  default. With nothing mounted there
//	no Team folder mounted                 that path is the service account's OWN
//	                                       directory, which is where a pre-split
//	                                       install keeps its default-mode
//	                                       archive; adoptLegacyDefaultArchive
//	                                       carries it into the split root.
//	recordings in the default root only    default. Adopted.
//	nothing in either root                 default. An empty archive starts open.
//	nothing in either root, the            default. The account was made a moment
//	`groupfolders` app is not enabled,     ago, or this operator has published
//	and either the service account was     nothing ever: either way there is no
//	made on this edge or no recording      archive an open mode could strand.
//	has ever been delivered
//	nothing in either root, the            nothing. An unmounted Team folder
//	`groupfolders` app is not enabled,     cannot be read, so "empty" and
//	and this install HAS delivered         "invisible" are the same answer, and
//	recordings                             on an install with a past one of them
//	                                       is an archive.
//	no service account                     default. Every recording is written and
//	                                       read as that account, so an install
//	                                       without one has no archive to keep.
//
// It replaces the wizard the enabled edge used to wait for (D-708), which
// refused every recording on the instance until an administrator answered it —
// the cost of a question the instance can answer, charged to every call made
// before anybody saw it. What it does NOT replace is sanity(): a resolved mode
// is still checked against the instance before anything is written under it.
func storageModeFromProbe(p ncStorageProbe) (accessControlled, ok bool, why string) {
	switch {
	case !p.FolderProbed:
		// `Cassini/Recordings` is the Team folder on one install and the service
		// account's own pre-split directory on another, and the folder list is
		// the only thing that tells them apart. Answering without it would adopt
		// one as the other.
		return false, false, fmt.Sprintf(
			"Cassini could not tell whether a %q Team folder is mounted, which is what distinguishes an access-controlled archive from a pre-split one at the same path",
			ncRecordingsMount)
	case !p.ServiceAccount:
		if p.FolderPresent {
			// A Team folder that may hold an archive nothing here can read: the
			// probe reads both roots AS the service account, and there is not
			// one. Resolving `default` would strand it.
			return false, false, fmt.Sprintf(
				"the %q service account does not exist, so Cassini cannot read what the %q Team folder holds",
				ncRecordingsOwner, ncRecordingsMount)
		}
		return false, true, fmt.Sprintf(
			"the %q service account does not exist yet and there is no %q Team folder, so this install has no archive",
			ncRecordingsOwner, ncRecordingsMount)
	case !p.ArchivesComparable():
		return false, false, fmt.Sprintf(
			"Cassini could not read both recordings roots (%s: %t, %s: %t)",
			ncDefaultRecordingsRoot, p.DefaultArchive.Probed, ncACLRecordingsRoot, p.ACLArchive.Probed)
	case p.ACLArchive.Populated() && p.FolderMounted:
		return true, true, fmt.Sprintf(
			"%d recording(s) are in the %q Team folder",
			p.ACLArchive.Meetings(), ncRecordingsMount)
	case p.ACLArchive.Populated():
		return false, true, fmt.Sprintf(
			"%d recording(s) are at %s, which with no Team folder mounted is the %q account's own directory",
			p.ACLArchive.Meetings(), ncACLRecordingsRoot, ncRecordingsOwner)
	case !prereqEnabled(p.Prereqs, ncAppGroupFolders) && !p.ACLArchive.Present && !p.DefaultArchive.Present:
		// "Nothing anywhere", read off an instance whose Team-folder machinery is
		// switched off, is not evidence that there is nothing anywhere. With
		// `groupfolders` disabled the `Cassini` mount is gone from the service
		// account's files, so an access-controlled archive answers exactly like
		// an empty install: both roots 404 and neither is confirmed present.
		// Resolving `default` there records the open model permanently, and the
		// archive is stranded the moment the app comes back. An `occ upgrade`
		// window, or an app left disabled by a Nextcloud major, is long enough.
		//
		// Nextcloud cannot tell the two apart while the app is off. Two local
		// facts can, and either is enough — a deps-free install is the ordinary
		// fresh install (Nextcloud AIO ships without `groupfolders`), so this
		// must not hold one up:
		if p.ServiceAccountCreated {
			// Cassini made the account on THIS edge. Every recording in either
			// model is written and read as it, so a minute ago there was no
			// identity that could own an archive here. There is nothing to hide.
			return false, true, fmt.Sprintf(
				"the %q account was created on this enabled edge, so this install has no archive yet",
				ncRecordingsOwner)
		}
		if p.DeliveredRecordingsProbed && p.DeliveredRecordings == 0 {
			// This operator has never published a recording. Job rows are never
			// deleted, so that count is the install's whole history, and an
			// install with no history has nothing an open mode could strand.
			return false, true, fmt.Sprintf(
				"the %q app is not enabled and nothing is at %s or %s, and this install has delivered no recordings",
				ncAppGroupFolders, ncACLRecordingsRoot, ncDefaultRecordingsRoot)
		}
		// It has delivered recordings, or nobody could say. Either way this is
		// not demonstrably a fresh install, and the one place its archive could
		// be is the Team folder nothing here can currently see.
		history := fmt.Sprintf("this install has already delivered %d recording(s)", p.DeliveredRecordings)
		if !p.DeliveredRecordingsProbed {
			history = "Cassini could not count what this install has already delivered"
		}
		return false, false, fmt.Sprintf(
			"the %q app is not enabled, so a %q Team folder would be invisible here, neither %s nor %s is present, and %s",
			ncAppGroupFolders, ncRecordingsMount, ncACLRecordingsRoot, ncDefaultRecordingsRoot, history)
	case p.DefaultArchive.Populated():
		return false, true, fmt.Sprintf(
			"%d recording(s) are in %s", p.DefaultArchive.Meetings(), ncDefaultRecordingsRoot)
	default:
		return false, true, fmt.Sprintf(
			"there are no recordings in %s or %s", ncACLRecordingsRoot, ncDefaultRecordingsRoot)
	}
}

// storageModeUnresolvedDetail is the sentence the container log and /status both
// carry when the probe could not answer what this install holds.
func storageModeUnresolvedDetail(why string) string {
	return fmt.Sprintf(
		"Cassini could not work out where this install already keeps its recordings, so it has recorded no storage mode: %s. Recording is not refused for it, publishing waits, and the next time the app is enabled Cassini looks again",
		why)
}

// storageStepDeclaredConflict means CASSINI_STORAGE_MODE named a model this
// instance does not match. See declaredModeConflicts.
const storageStepDeclaredConflict = "storage_mode_declared_conflict"

// declaredModeConflicts reports why a DECLARED mode must not be believed, as a
// list of sentences. Empty means the declaration fits.
//
// The deploy option is a development and CI affordance, and the whole reason a
// harness declares a mode is that it knows what it built. So a declaration that
// disagrees with the instance is a bug in the stack rather than an instruction
// to follow, and the loudest available answer is the right one: refuse, and do
// not write it down. Writing it down would be worse than the disagreement,
// because a recorded mode is never reconsidered — the next enable would carry
// the same mistake with nothing left to notice it.
//
// It deliberately does NOT include the prerequisite check: that is sanity(), it
// runs first, and it produces a better message. What is here is the set of
// conflicts a mode's prerequisites can all be present for.
func (p ncStorageProbe) declaredModeConflicts(accessControlled bool) []string {
	var out []string
	if !p.ArchivesComparable() {
		// Failing closed on an unanswered question, which for a WRITE is the
		// only defensible direction: the declaration is about to be recorded
		// permanently, and half the evidence is missing.
		out = append(out, fmt.Sprintf(
			"Cassini could not read both recordings roots (%s: %t, %s: %t), so it cannot tell whether this declaration would strand or overwrite an existing archive",
			ncDefaultRecordingsRoot, p.DefaultArchive.Probed, ncACLRecordingsRoot, p.ACLArchive.Probed))
		return out
	}
	if len(p.DuplicateNames) > 0 {
		out = append(out, fmt.Sprintf(
			"the same %d recording(s) exist under BOTH %s and %s (%s), so which copy is authoritative is a question a deploy option cannot answer",
			len(p.DuplicateNames), ncDefaultRecordingsRoot, ncACLRecordingsRoot, strings.Join(clip(p.DuplicateNames, 3), ", ")))
	}
	if p.DefaultArchive.Populated() && p.ACLArchive.Populated() {
		out = append(out, fmt.Sprintf(
			"there are recordings in both roots (%d in %s, %d in %s), so declaring one model would leave the other one's archive unread",
			p.DefaultArchive.Meetings(), ncDefaultRecordingsRoot, p.ACLArchive.Meetings(), ncACLRecordingsRoot))
	}
	if other := p.archiveFor(!accessControlled); other.Populated() && !p.archiveFor(accessControlled).Populated() {
		out = append(out, fmt.Sprintf(
			"%d recording(s) are in %s, which the %q model does not read, and nothing here can decide whether to carry them across",
			other.Meetings(), other.Root, storageModeName(accessControlled)))
	}
	return out
}

// declaredModeConflictDetail is the sentence /status, the app and the container
// log all carry for a refused declaration.
func declaredModeConflictDetail(accessControlled bool, conflicts []string) string {
	return fmt.Sprintf(
		"%s declared the storage mode %q, but this instance does not match it: %s. That deploy option is for development and CI, where the stack knows what it built — so a disagreement is refused rather than recorded. Nothing has been written down. Fix the stack, or remove %s and choose who can see recordings in Operator › Settings",
		envStorageMode, storageModeName(accessControlled), strings.Join(conflicts, "; "), envStorageMode)
}

// The upgrade latch is gone, and its name is recorded here rather than silently
// dropped because a monitor keyed on it will now see nothing.
//
//	storageStepStrandedACLArchive = "access_controlled_archive"
//
// It existed to catch the one FALLBACK that would have been obviously wrong: an
// install that recorded nothing, fell back to the deps-free model, and would
// have published into a fresh private tree while its real archive sat unread in
// the Team folder. There is no fallback any more (D-708) — an install that has
// not been told does not publish at all — so the latch has nothing left to
// catch. The fact it read is not lost: both roots' contents are on every probe,
// and they are the first thing the setup wizard shows.

// serviceAccountDetail is what an administrator reads when the account is
// missing: the base sentence, plus what this run's create attempt ran into when
// there was one. Both gates route through it, so /status, /storage and the log
// carry the same sentence.
func (p ncStorageProbe) serviceAccountDetail() string {
	if p.ServiceAccountAttempt == "" {
		return missingServiceAccountDetail()
	}
	return missingServiceAccountDetail() + ". " + p.ServiceAccountAttempt
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
				"a Team folder is mounted at %q, which is where the default storage mode keeps its recordings. A mounted Team folder wins that path, so recordings would be written into a shared folder rather than %q's own private tree, and everyone mapped to that folder could read them. Unmap or rename that Team folder (`occ groupfolders:list`, then `occ groupfolders:group <id> <group> --delete`), or choose \"Meeting participants\" in Operator › Settings › Who can see recordings if this instance was meant to be access-controlled",
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
		fmt.Sprintf("acl_root=%t/%t/%d", p.ACLArchive.Probed, p.ACLArchive.Present, p.ACLArchive.Meetings()),
		fmt.Sprintf("default_root=%t/%t/%d", p.DefaultArchive.Probed, p.DefaultArchive.Present, p.DefaultArchive.Meetings()),
		fmt.Sprintf("duplicates=%d", len(p.DuplicateNames)),
		fmt.Sprintf("default_root_shadowed=%t", p.DefaultRootShadowed),
	}
	return strings.Join(fields, " ")
}
