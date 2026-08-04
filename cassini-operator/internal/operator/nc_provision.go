package operator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Automatic provisioning of the Nextcloud-native recordings access model
// (companion to D-534). Recordings live in a group folder ("Team folder") with
// advanced ACL and a default-deny floor, owned/managed by the recordings owner,
// and readable by every logged-in user so anyone can traverse to the meetings
// they were granted (per-file ACLs — applied at publish, webdav_acl.go —
// restrict each recording to its participants). Before this file that whole
// topology was a manual, documented `occ groupfolders:*` + group setup an admin
// had to run once.
//
// An ExApp cannot run `occ`; it can only reach Nextcloud over HTTP. But every
// step of the manual recipe has an HTTP equivalent that an admin-acting caller
// can drive: the Group Folders app routes (/index.php/apps/groupfolders/...) and
// the core OCS provisioning API (/ocs/v2.php/cloud/...). This provisioner runs
// those calls on the AppAPI "enabled" edge — the first moment outbound
// act-as-user auth is accepted — so installing/enabling the ExApp makes the
// canonical directory, groups, and permissions appear with no manual step.
//
//	on enabled edge
//	  ├── resolve administrator + ensure `cassini` owner  (OCS provisioning API)
//	  ├── require the virtual "everyone" group            (Everyone Group app)
//	  ├── ensure "Cassini" Team folder (default-deny)     (groupfolders addFolder)
//	  ├── assign everyone READ + owner group ALL          (groupfolders groups)
//	  ├── enable ACL + delegate `cassini` manager         (groupfolders acl/manageACL)
//	  ├── migrate legacy recording-viewers leaf ACLs      (WebDAV PROPFIND/PROPPATCH)
//	  ├── root ACL: cassini ALL + everyone READ           (WebDAV PROPPATCH)
//	  ├── remove legacy viewer/admin mappings/manager     (groupfolders APIs)
//	  └── MKCOL Cassini/Recordings/meetings               (WebDAV)
//
// Every step is idempotent and safe to re-run on every enable. A failure still
// does not block startup, but since D-554 it is no longer merely logged: each
// outcome is recorded (nc_access_status.go) and reported by GET /status, because
// an ExApp whose group folder does not exist answers every request while serving
// nobody their recordings.

const (
	// ncRecordingsOwnerGroup is the group whose members get a write-capable mount
	// of the recordings group folder. The owner (ncRecordingsOwner) must be a
	// member so it can create/update recordings; under the default-deny ACL floor
	// a write mount alone is insufficient (see the root container ACL below), but
	// it is still required — ACL-management permission does not grant WebDAV
	// create/write. It is deliberately narrow: only the dedicated recordings
	// service account belongs to this group. The virtual `everyone` group remains
	// read-only and supplies the broad mount/traversal capability.
	ncRecordingsOwnerGroup = ncRecordingsOwner

	// Two identities are deliberately distinct (D-532):
	//
	//	ownership     `cassini` service account   owns the tree, writes recordings,
	//	                                        and manages per-path ACLs
	//	provisioning  instance administrator     creates users, groups, and folders
	//
	// The identity holding every recording must not also have instance-admin
	// rights. The owner account is therefore created before any step acts as it.
	defaultNextcloudAdminUser = "admin"
	envNCAdminUser            = "CASSINI_NC_ADMIN_USER"

	// Before D-532 the built-in admin group supplied the write-capable mount.
	// Remove that mapping only after the service account proves it can manage the
	// migrated root, so the dedicated owner group is the sole write principal.
	ncLegacyRecordingsOwner      = "admin"
	ncLegacyRecordingsOwnerGroup = "admin"

	ncProvisionTimeout = 90 * time.Second
)

// provisionMu serializes provisioning: EnabledCallback is dispatched in a
// goroutine and can fire more than once (admin double-action, AppAPI retry), and
// the find-then-create folder step is not atomic — without this two concurrent
// runs could create duplicate "Cassini" folders.
var provisionMu sync.Mutex

// resolvedProvisioningUser caches the administrator selected for privileged
// setup. An instance's administrator does not change during one container run.
var resolvedProvisioningUser atomic.Pointer[any]

// firstPathSegment returns the first path component of a slash path, e.g.
// "Cassini/Recordings" -> "Cassini". It names the group folder mount point,
// which is the root the recordings tree lives under.
func firstPathSegment(p string) string {
	p = strings.Trim(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// ncRecordingsMount is the group folder mount point ("Cassini"): the first
// segment of the canonical recordings root.
var ncRecordingsMount = firstPathSegment(ncRecordingsRoot)

// provisioningUser is the administrator used only for privileged setup. The
// explicit environment override wins; otherwise a discovered administrator is
// cached, with the conventional `admin` id as the fallback.
func (c ExAppConfig) provisioningUser() string {
	if cached := resolvedProvisioningUser.Load(); cached != nil {
		if name, _ := (*cached).(string); name != "" {
			return name
		}
	}
	return c.provisioningUserFallback()
}

func (c ExAppConfig) provisioningUserFallback() string {
	if configured := strings.TrimSpace(os.Getenv(envNCAdminUser)); configured != "" {
		return configured
	}
	return defaultNextcloudAdminUser
}

// ensureRecordingsOwnerAccount creates the dedicated recordings owner and
// ensures it belongs to the narrow write-capable owner group. Existing accounts
// are never re-passworded or disabled. The generated password only satisfies
// the OCS create contract; AppAPI act-as-user authentication never uses it.
func (c ExAppConfig) ensureRecordingsOwnerAccount(ctx context.Context, client *http.Client, logger *log.Logger) error {
	if err := c.ensureGroup(ctx, client, ncRecordingsOwnerGroup); err != nil {
		return fmt.Errorf("ensure owner group %q: %w", ncRecordingsOwnerGroup, err)
	}
	password, err := randomPassword()
	if err != nil {
		return fmt.Errorf("generate service account password: %w", err)
	}
	status, body, err := c.apiPostForm(ctx, client, c.ocsURL("/cloud/users"), url.Values{
		"userid":      {ncRecordingsOwner},
		"password":    {password},
		"displayname": {"Cassini recordings"},
		// "groups[]", not "groups". OCS decodes this field as a PHP array, and
		// a scalar makes Nextcloud answer a bare 400 with an empty body — so
		// the account is never created, every act-as-cassini call 401s, and
		// nothing downstream can be provisioned. Verified against a live
		// Nextcloud 32: "groups=" -> 400, "groups[]=" -> 200.
		"groups[]": {ncRecordingsOwnerGroup},
	})
	if err != nil {
		return fmt.Errorf("create service account: %w", err)
	}
	switch {
	case status/100 == 2 && ocsStatusCode(body) != 102:
		if logger != nil {
			logger.Printf("nc provision: created recordings service account %q", ncRecordingsOwner)
		}
	case ocsStatusCode(body) == 102 || strings.Contains(strings.ToLower(string(body)), "already exists"):
		// Membership is still re-asserted below because the group may have been
		// created after an existing account.
	default:
		return fmt.Errorf("create service account -> %d: %s", status, snippet(body))
	}
	if err := c.addUserToGroup(ctx, client, ncRecordingsOwner, ncRecordingsOwnerGroup); err != nil {
		return fmt.Errorf("add %q to %q: %w", ncRecordingsOwner, ncRecordingsOwnerGroup, err)
	}
	return nil
}

func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "Cw1!" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// provisionNCFilesAccess first establishes the ownership/provisioning
// identities, then creates (idempotently) the group folder + ACL topology the
// access-control model needs. No-op only outside AppAPI. Runs on the enabled
// edge (in the EnabledCallback goroutine), before the archive startup sync.
//
// Every step used to be best-effort in the strict sense that nothing recorded
// whether it worked: failures were logged and forgotten. They are still
// non-fatal to startup — an operator that cannot provision should still come up
// so an admin can look at it — but each outcome is now recorded and reported
// through /status, because an ExApp whose group folder does not exist serves
// nobody their recordings and must say so rather than degrade silently (D-554,
// D-545 AC-7).
func (c ExAppConfig) provisionNCFilesAccess(ctx context.Context, logger *log.Logger) {
	if !c.appAPIActive() {
		return
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()
	// Recorded before the first step so a run that dies part-way still reports
	// as an ExApp with a broken substrate, not as a standalone with none.
	ncAccessSubstrate.markApplicable()
	client := &http.Client{Timeout: ncProvisionTimeout}

	// P1. Resolve the administrator BEFORE anything acts as one. Proceeding as
	//     an account that may not exist is what made every downstream failure
	//     look like a Nextcloud fault rather than a naming one (D-585).
	admin, err := c.resolveAdminIdentity(ctx, client, logger)
	if err != nil {
		logger.Printf("nc provision: %v — set %s to an account in the \"admin\" group; recordings setup skipped", err, envNCAdminUser)
		if errors.Is(err, errAdminRouteMissing) {
			ncAccessSubstrate.degraded("administrator_probe", err)
		} else {
			ncAccessSubstrate.unavailable("administrator", fmt.Errorf("%w; set %s to an account in the \"admin\" group", err, envNCAdminUser))
		}
		return
	}
	ncAccessSubstrate.setAdminUser(admin)

	// P2. Both native prerequisites, checked explicitly so a missing one is
	//     NAMED rather than surfacing later as an opaque folder-create failure.
	//     Nextcloud AIO does not ship Team folders enabled, so this is the
	//     common case (D-585 outcome 1).
	prereqs, perr := c.preflightNativeApps(ctx, client)
	ncAccessSubstrate.setPrerequisites(prereqs)
	if perr != nil {
		if missing := firstMissingApp(prereqs); missing != "" {
			logger.Printf("nc provision: required Nextcloud app %q is not enabled — run `occ app:install %s && occ app:enable %s`; recordings setup skipped", missing, missing, missing)
			ncAccessSubstrate.unavailable("app_missing:"+missing, fmt.Errorf("the %q app is not enabled; an ExApp cannot install it", missing))
			return
		}
		logger.Printf("nc provision: %v — recordings setup skipped", perr)
		ncAccessSubstrate.degraded("app_check_failed", perr)
		return
	}

	// 0. The administrator creates the service account before any DAV call acts
	//    as it. The narrow owner group supplies its write-capable Team-folder mount.
	if err := c.ensureRecordingsOwnerAccount(ctx, client, logger); err != nil {
		logger.Printf("nc provision: ensure recordings account %q failed: %v — recordings setup incomplete", ncRecordingsOwner, err)
		ncAccessSubstrate.unavailable("owner_account", fmt.Errorf("the recordings owner account %q could not be created: %w", ncRecordingsOwner, err))
		return
	}

	// 1. Require the virtual all-users group supplied by the Everyone Group app
	//    (or an equivalent group backend). Never create a static group with this
	//    name: an empty ordinary group would silently reintroduce the new-account
	//    mount race this topology exists to remove.
	hasEveryone, err := c.groupExists(ctx, client, ncRecordingsEveryoneGroup)
	if err != nil {
		logger.Printf("nc provision: check required universal group %q: %v — access control setup incomplete", ncRecordingsEveryoneGroup, err)
		ncAccessSubstrate.degraded("universal_group_probe", fmt.Errorf("could not check whether the universal group %q exists: %w", ncRecordingsEveryoneGroup, err))
		return
	}
	if !hasEveryone {
		logger.Printf("nc provision: required universal group %q is unavailable; install/enable the Everyone Group app (group_everyone) — access control setup incomplete", ncRecordingsEveryoneGroup)
		ncAccessSubstrate.unavailable("universal_group", fmt.Errorf("the universal group %q does not exist; the Everyone Group app (%s) is enabled but produced no group", ncRecordingsEveryoneGroup, ncAppEveryoneGroup))
		return
	}

	// 2. The group folder with its default-deny floor (only settable at creation).
	folder, err := c.ensureRecordingsFolder(ctx, client)
	if err != nil {
		logger.Printf("nc provision: ensure group folder %q failed: %v — access control setup incomplete", ncRecordingsMount, err)
		// The likeliest cause an admin can act on: the Group folders ("Team
		// folders") app is one of the two native prerequisites an ExApp cannot
		// install for itself.
		ncAccessSubstrate.unavailable("group_folder", fmt.Errorf("the %q Team folder could not be created: %w", ncRecordingsMount, err))
		return
	}
	folderID, ok := folder.idInt()
	if !ok {
		logger.Printf("nc provision: group folder %q has no usable id — aborting", ncRecordingsMount)
		ncAccessSubstrate.degraded("folder_id", fmt.Errorf("the %q Team folder has no usable id", ncRecordingsMount))
		return
	}

	// 3. Mount mappings: the virtual all-users group gets a READ capability
	//    ceiling; the narrow owner group gets ALL so the service account can
	//    write. ACL-manager status alone cannot elevate a READ mount to write.
	//    Both abort: without the everyone mapping nobody has the folder, and
	//    without the owner mapping the service account cannot write into it.
	//    Continuing to the root grant would report success over a folder that
	//    serves nobody.
	for _, mapping := range []struct {
		group string
		perms int
	}{
		{ncRecordingsEveryoneGroup, aclPermRead},
		{ncRecordingsOwnerGroup, aclMaskAll},
	} {
		if err := c.ensureFolderGroup(ctx, client, folderID, mapping.group, mapping.perms, logger); err != nil {
			ncAccessSubstrate.degraded("mount_mapping:"+mapping.group, fmt.Errorf("the %q mount mapping on the %q Team folder could not be installed: %w", mapping.group, ncRecordingsMount, err))
			return
		}
	}

	// 4. Advanced ACL on (idempotent). This aborts rather than logging on,
	//    because step 7 below unconditionally grants `everyone` READ at the
	//    mount root: without advanced ACL there is no default-deny floor and no
	//    per-leaf override, so continuing would make every registered account
	//    able to read every recording. Before D-554 this path was reachable only
	//    behind a flag that defaulted off; it now runs on every install.
	if err := c.folderSetACL(ctx, client, folderID, true); err != nil {
		logger.Printf("nc provision: enable advanced ACL folder=%d: %v — refusing to widen the root without it", folderID, err)
		ncAccessSubstrate.degraded("acl_enable", fmt.Errorf("advanced ACL could not be enabled on the %q Team folder, so the default-deny floor is missing: %w", ncRecordingsMount, err))
		return
	}

	// 5. Delegate the owner as ACL manager so it can PROPPATCH per-path rules.
	//    Re-adding an existing manager is a 500, so only add when absent.
	if !folder.hasManager("user", ncRecordingsOwner) {
		if err := c.folderManageACL(ctx, client, folderID, "user", ncRecordingsOwner, true); err != nil {
			// Not fatal: the migration below acts as the owner and will fail
			// loudly if the delegation really did not take. Recorded so a
			// half-provisioned folder is never reported as provisioned.
			logger.Printf("nc provision: delegate ACL manager folder=%d user=%s: %v", folderID, ncRecordingsOwner, err)
			ncAccessSubstrate.degraded("acl_manager", fmt.Errorf("%q could not be delegated as ACL manager of the %q Team folder: %w", ncRecordingsOwner, ncRecordingsMount, err))
		}
	}

	// 6. Narrow the root to the owner while migrating existing leaves. Otherwise
	//    a new user (who was never in the legacy static group) could inherit the
	//    new broad allow through an old private leaf that only denies
	//    recording-viewers. Any migration failure leaves the tree owner-only — an
	//    availability failure rather than an access leak — for the next retry.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, ownerOnlyContainerACLRules()); err != nil {
		logger.Printf("nc provision: establish owner-only migration floor on %q: %v", ncRecordingsMount, err)
		ncAccessSubstrate.degraded("migration_floor", fmt.Errorf("the owner-only migration floor could not be applied to %q: %w", ncRecordingsMount, err))
		return
	}
	if err := c.selfHealLeafProtection(ctx, client, logger); err != nil {
		logger.Printf("nc provision: migrate/protect recording ACLs: %v — leaving root owner-only", err)
		ncAccessSubstrate.degraded("migration", fmt.Errorf("legacy recording ACLs could not be migrated, so the tree is left owner-only: %w", err))
		return
	}
	if err := c.protectExistingCatalog(ctx, client); err != nil {
		logger.Printf("nc provision: migrate existing catalog ACL: %v — leaving root owner-only", err)
		ncAccessSubstrate.degraded("catalog_migration", fmt.Errorf("the existing catalog ACL could not be migrated, so the tree is left owner-only: %w", err))
		return
	}

	// 7. The migration is complete, so grant everyone READ at the root. Each leaf
	//    now safely overrides that inherited grant as private or public.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, containerACLRules()); err != nil {
		logger.Printf("nc provision: root container ACL %q: %v", ncRecordingsMount, err)
		ncAccessSubstrate.degraded("root_acl", fmt.Errorf("the root container ACL could not be applied to %q, so nobody can traverse to the recordings: %w", ncRecordingsMount, err))
		return
	}

	// Old mount mappings are no longer authoritative. Remove them only after the
	// virtual audience and dedicated owner are proven through the migrated root.
	// Leave the ordinary groups themselves untouched in case an administrator
	// reused them elsewhere.
	for _, legacyGroup := range []string{ncLegacyRecordingsViewerGroup, ncLegacyRecordingsOwnerGroup} {
		if legacyGroup == ncRecordingsOwnerGroup || !folder.hasGroup(legacyGroup) {
			continue
		}
		if err := c.removeFolderGroup(ctx, client, folderID, legacyGroup); err != nil {
			logger.Printf("nc provision: remove legacy group %q from folder=%d: %v", legacyGroup, folderID, err)
		}
	}
	if ncLegacyRecordingsOwner != ncRecordingsOwner && folder.hasManager("user", ncLegacyRecordingsOwner) {
		if err := c.folderManageACL(ctx, client, folderID, "user", ncLegacyRecordingsOwner, false); err != nil {
			logger.Printf("nc provision: remove legacy ACL manager folder=%d user=%s: %v", folderID, ncLegacyRecordingsOwner, err)
		}
	}

	// 8. Materialize the canonical collections so the directory exists right after
	//    install, before any recording. MKCOL of the mount root is a harmless 405.
	mkcolFailed := false
	for _, dir := range []string{ncRecordingsRoot, ncRecordingsRoot + "/meetings"} {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			logger.Printf("nc provision: mkcol %s: %v", dir, err)
			ncAccessSubstrate.degraded("mkcol:"+dir, fmt.Errorf("the canonical collection %q could not be created: %w", dir, err))
			mkcolFailed = true
		}
	}
	if mkcolFailed {
		return
	}

	ncAccessSubstrate.succeed()
	logger.Printf("nc provision: recordings access control provisioned folder_id=%d mount=%s root=%s owner=%s audience_group=%s", folderID, ncRecordingsMount, ncRecordingsRoot, ncRecordingsOwner, ncRecordingsEveryoneGroup)
}

// containerACLRules grants the owner full control and the virtual all-users
// group read on a recordings container. The owner writes under default-deny;
// every account can traverse; each leaf overrides the broad grant with either a
// private deny + participant allows or a public allow (webdav_acl.go).
func containerACLRules() []aclRule {
	return append(ownerOnlyContainerACLRules(), aclRule{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: aclPermRead})
}

func ownerOnlyContainerACLRules() []aclRule {
	return []aclRule{{Type: "user", ID: ncRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll}}
}

// --- Group Folders app HTTP surface (/index.php/apps/groupfolders/...) --------
//
// These are the app's own (non-OCS) routes, but sending OCS-APIRequest: true
// bypasses CSRF and returns an OCS-wrapped JSON envelope. They are guarded by
// RequireGroupFolderAdmin, so these calls act as the separately resolved
// administrator, never as the recordings owner. PasswordConfirmationRequired
// is skipped for the synthetic AppAPI session (no confirmable password token),
// the same way it is for basic auth.

// gfFolder is the subset of a Group Folders folder record this code reads.
type gfFolder struct {
	ID         json.Number     `json:"id"`
	MountPoint string          `json:"mount_point"`
	Groups     json.RawMessage `json:"groups"`
	Manage     []gfManage      `json:"manage"`
}

type gfManage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (f gfFolder) idInt() (int, bool) {
	if f.ID == "" {
		return 0, false
	}
	n, err := f.ID.Int64()
	if err != nil {
		return 0, false
	}
	return int(n), true
}

func (f gfFolder) hasManager(mtype, mid string) bool {
	for _, m := range f.Manage {
		if m.Type == mtype && m.ID == mid {
			return true
		}
	}
	return false
}

func (f gfFolder) hasGroup(group string) bool {
	var groups map[string]json.RawMessage
	if err := json.Unmarshal(f.Groups, &groups); err != nil {
		// Team folders serializes no mappings as [] and populated mappings as an
		// object keyed by group id. An empty array therefore means no match.
		return false
	}
	_, ok := groups[group]
	return ok
}

func (c ExAppConfig) gfURL(suffix string) string {
	return strings.TrimRight(c.NextcloudURL, "/") + "/index.php/apps/groupfolders" + suffix
}

func (c ExAppConfig) ocsURL(suffix string) string {
	return strings.TrimRight(c.NextcloudURL, "/") + "/ocs/v2.php" + suffix
}

// ensureRecordingsFolder returns the existing "Cassini" group folder or creates
// it with the default-deny floor (acl_default_no_permission), which can only be
// set at creation time.
func (c ExAppConfig) ensureRecordingsFolder(ctx context.Context, client *http.Client) (gfFolder, error) {
	if f, ok, err := c.findFolder(ctx, client, ncRecordingsMount); err != nil {
		return gfFolder{}, err
	} else if ok {
		return f, nil
	}
	status, body, err := c.apiPostForm(ctx, client, c.gfURL("/folders"), url.Values{
		"mountpoint":                {ncRecordingsMount},
		"acl_default_no_permission": {"1"},
	})
	if err != nil {
		return gfFolder{}, err
	}
	if status/100 != 2 {
		return gfFolder{}, fmt.Errorf("create folder -> %d: %s", status, snippet(body))
	}
	var env struct {
		OCS struct {
			Data gfFolder `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return gfFolder{}, fmt.Errorf("decode create folder response: %w", err)
	}
	if _, ok := env.OCS.Data.idInt(); !ok {
		return gfFolder{}, fmt.Errorf("create folder: no id in response: %s", snippet(body))
	}
	return env.OCS.Data, nil
}

// findFolder looks up a group folder by mount point. The list endpoint returns
// ocs.data as an object keyed by folder id (or an empty array when there are
// none), so both shapes are tolerated.
func (c ExAppConfig) findFolder(ctx context.Context, client *http.Client, mount string) (gfFolder, bool, error) {
	status, body, err := c.apiGet(ctx, client, c.gfURL("/folders"))
	if err != nil {
		return gfFolder{}, false, err
	}
	if status/100 != 2 {
		return gfFolder{}, false, fmt.Errorf("list folders -> %d: %s", status, snippet(body))
	}
	var env struct {
		OCS struct {
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return gfFolder{}, false, fmt.Errorf("decode folders list: %w", err)
	}
	// The list is an object keyed by folder id, or an empty array. Collect all
	// folders and pick the lowest-id match deterministically: if a duplicate
	// mount point ever exists, every run must resolve to the same folder rather
	// than flapping on Go's randomized map iteration.
	var asMap map[string]gfFolder
	if err := json.Unmarshal(env.OCS.Data, &asMap); err == nil {
		folders := make([]gfFolder, 0, len(asMap))
		for _, f := range asMap {
			folders = append(folders, f)
		}
		if f, ok := lowestIDMatch(folders, mount); ok {
			return f, true, nil
		}
		return gfFolder{}, false, nil
	}
	var asArr []gfFolder
	if err := json.Unmarshal(env.OCS.Data, &asArr); err == nil {
		if f, ok := lowestIDMatch(asArr, mount); ok {
			return f, true, nil
		}
	}
	return gfFolder{}, false, nil
}

// lowestIDMatch returns the folder with the given mount point that has the
// smallest id, so lookups are stable across calls.
func lowestIDMatch(folders []gfFolder, mount string) (gfFolder, bool) {
	var best gfFolder
	bestID, found := 0, false
	for _, f := range folders {
		if f.MountPoint != mount {
			continue
		}
		id, ok := f.idInt()
		if !ok {
			continue
		}
		if !found || id < bestID {
			best, bestID, found = f, id, true
		}
	}
	return best, found
}

// ensureFolderGroup assigns a group to the folder (idempotent: an
// already-assigned group is a non-fatal error we ignore) and then sets its
// permissions, which is the authoritative, idempotent source of the mount level.
// ensureFolderGroup returns an error rather than only logging, because the mount
// mapping is the whole audience mechanism: without the `everyone` READ mapping
// nobody has the folder at all, and without the owner group's ALL mapping the
// service account cannot write into it. Reporting `provisioned` after either
// failed would be exactly the silent-green shape D-585 removes.
func (c ExAppConfig) ensureFolderGroup(ctx context.Context, client *http.Client, folderID int, group string, perms int, logger *log.Logger) error {
	// addGroup: 2xx on first add, non-2xx ("Group already assigned") afterwards.
	if status, body, err := c.apiPostForm(ctx, client, c.gfURL(fmt.Sprintf("/folders/%d/groups", folderID)), url.Values{"group": {group}}); err != nil {
		logger.Printf("nc provision: add group %q to folder=%d: %v", group, folderID, err)
		return fmt.Errorf("add group %q to folder=%d: %w", group, folderID, err)
	} else if status/100 != 2 && !strings.Contains(strings.ToLower(string(body)), "already") {
		logger.Printf("nc provision: add group %q to folder=%d -> %d: %s", group, folderID, status, snippet(body))
		return fmt.Errorf("add group %q to folder=%d -> %d: %s", group, folderID, status, snippet(body))
	}
	if status, body, err := c.apiPostForm(ctx, client, c.gfURL(fmt.Sprintf("/folders/%d/groups/%s", folderID, url.PathEscape(group))), url.Values{"permissions": {fmt.Sprintf("%d", perms)}}); err != nil {
		logger.Printf("nc provision: set permissions group=%q folder=%d: %v", group, folderID, err)
		return fmt.Errorf("set permissions group=%q folder=%d: %w", group, folderID, err)
	} else if status/100 != 2 {
		logger.Printf("nc provision: set permissions group=%q folder=%d -> %d: %s", group, folderID, status, snippet(body))
		return fmt.Errorf("set permissions group=%q folder=%d -> %d: %s", group, folderID, status, snippet(body))
	}
	return nil
}

func (c ExAppConfig) removeFolderGroup(ctx context.Context, client *http.Client, folderID int, group string) error {
	status, body, err := c.apiDelete(ctx, client, c.gfURL(fmt.Sprintf("/folders/%d/groups/%s", folderID, url.PathEscape(group))))
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return fmt.Errorf("remove group %q from folder=%d -> %d: %s", group, folderID, status, snippet(body))
	}
	return nil
}

func (c ExAppConfig) folderSetACL(ctx context.Context, client *http.Client, folderID int, enabled bool) error {
	return c.folderPostExpectOK(ctx, client, fmt.Sprintf("/folders/%d/acl", folderID), url.Values{"acl": {boolForm(enabled)}})
}

func (c ExAppConfig) folderManageACL(ctx context.Context, client *http.Client, folderID int, mappingType, mappingID string, manage bool) error {
	return c.folderPostExpectOK(ctx, client, fmt.Sprintf("/folders/%d/manageACL", folderID), url.Values{
		"mappingType": {mappingType},
		"mappingId":   {mappingID},
		"manageAcl":   {boolForm(manage)},
	})
}

func (c ExAppConfig) folderPostExpectOK(ctx context.Context, client *http.Client, suffix string, form url.Values) error {
	status, body, err := c.apiPostForm(ctx, client, c.gfURL(suffix), form)
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return fmt.Errorf("POST %s -> %d: %s", suffix, status, snippet(body))
	}
	return nil
}

// --- Core OCS provisioning surface (/ocs/v2.php/cloud/...) ---------------------

// ensureGroup creates an ordinary group, treating "already exists" as success.
// It is used only for the narrow owner group; `everyone` must remain virtual.
func (c ExAppConfig) ensureGroup(ctx context.Context, client *http.Client, group string) error {
	status, body, err := c.apiPostForm(ctx, client, c.ocsURL("/cloud/groups"), url.Values{"groupid": {group}})
	if err != nil {
		return err
	}
	if status/100 == 2 || ocsStatusCode(body) == 102 || strings.Contains(strings.ToLower(string(body)), "group exists") {
		return nil
	}
	return fmt.Errorf("create group -> %d: %s", status, snippet(body))
}

func (c ExAppConfig) groupMembers(ctx context.Context, client *http.Client, group string) ([]string, error) {
	status, body, err := c.apiGet(ctx, client, c.ocsURL("/cloud/groups/"+url.PathEscape(group)))
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("group members -> %d: %s", status, snippet(body))
	}
	return parseOCSUserList(body)
}

func (c ExAppConfig) addUserToGroup(ctx context.Context, client *http.Client, userID, group string) error {
	status, body, err := c.apiPostForm(ctx, client, c.ocsURL("/cloud/users/"+url.PathEscape(userID)+"/groups"), url.Values{"groupid": {group}})
	if err != nil {
		return err
	}
	if status/100 != 2 {
		return fmt.Errorf("add user %q to %q -> %d: %s", userID, group, status, snippet(body))
	}
	return nil
}

func parseOCSUserList(body []byte) ([]string, error) {
	var env struct {
		OCS struct {
			Data struct {
				Users []string `json:"users"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode user list: %w", err)
	}
	return env.OCS.Data.Users, nil
}

// groupExists checks for an exact group id without enumerating its members. The
// virtual `everyone` backend can represent very large instances, so provisioning
// must not turn its availability check into an O(N) user listing.
func (c ExAppConfig) groupExists(ctx context.Context, client *http.Client, group string) (bool, error) {
	u := c.ocsURL("/cloud/groups") + "?search=" + url.QueryEscape(group) + "&limit=100"
	status, body, err := c.apiGet(ctx, client, u)
	if err != nil {
		return false, err
	}
	if status/100 != 2 {
		return false, fmt.Errorf("list groups -> %d: %s", status, snippet(body))
	}
	groups, err := parseOCSGroupList(body)
	if err != nil {
		return false, err
	}
	for _, candidate := range groups {
		if candidate == group {
			return true, nil
		}
	}
	return false, nil
}

// ocsStatusCode extracts ocs.meta.statuscode, or -1 if absent/unparseable.
func ocsStatusCode(body []byte) int {
	var env struct {
		OCS struct {
			Meta struct {
				StatusCode int `json:"statuscode"`
			} `json:"meta"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return -1
	}
	return env.OCS.Meta.StatusCode
}

// parseOCSGroupList reads ocs.data.groups from a provisioning response.
func parseOCSGroupList(body []byte) ([]string, error) {
	var env struct {
		OCS struct {
			Data struct {
				Groups []string `json:"groups"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode group list: %w", err)
	}
	return env.OCS.Data.Groups, nil
}

// protectExistingCatalog changes the legacy broad-group deny before the root
// switches to `everyone`. A missing catalog is the normal fresh-install state;
// startup sync will create and protect it later.
func (c ExAppConfig) protectExistingCatalog(ctx context.Context, client *http.Client) error {
	_, status, err := c.davGetBytes(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/catalog.json")
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("inspect catalog -> %d", status)
	}
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/catalog.json", catalogProtectionACLRules()); err != nil {
		return err
	}
	return nil
}

// --- HTTP helpers -------------------------------------------------------------

// apiGet issues an authenticated JSON GET as the provisioning administrator. `format=json`
// forces the OCS envelope to JSON regardless of Accept handling.
func (c ExAppConfig) apiGet(ctx context.Context, client *http.Client, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, withFormatJSON(rawURL), nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeaders(req)
	return doReadBody(client, req)
}

// apiGetAs is apiGet acting as an explicit identity rather than the resolved
// administrator. Only admin discovery needs it: every other provisioning call
// happens after resolution, when provisioningUser() is the right answer. An
// empty actAs keeps the app-scoped (system) session, which is what makes the
// account roster readable before any identity is known (nc_admin_identity.go).
func (c ExAppConfig) apiGetAs(ctx context.Context, client *http.Client, actAs, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, withFormatJSON(rawURL), nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeadersAs(req, actAs)
	return doReadBody(client, req)
}

// apiPostForm issues an authenticated form-encoded POST as the provisioning administrator.
func (c ExAppConfig) apiPostForm(ctx context.Context, client *http.Client, rawURL string, form url.Values) (int, []byte, error) {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, withFormatJSON(rawURL), strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.ContentLength = int64(len(body))
	return doReadBody(client, req)
}

func (c ExAppConfig) apiDelete(ctx context.Context, client *http.Client, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, withFormatJSON(rawURL), nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeaders(req)
	return doReadBody(client, req)
}

// setAppAPIProvisionHeaders sets AppAPI auth for the provisioning administrator plus the OCS
// request marker. OCS-APIRequest is what lets the Group Folders frontpage routes
// skip CSRF and answer JSON; the provisioning API requires it too.
func (c ExAppConfig) setAppAPIProvisionHeaders(req *http.Request) {
	c.setAppAPIProvisionHeadersAs(req, c.provisioningUser())
}

func (c ExAppConfig) setAppAPIProvisionHeadersAs(req *http.Request, userID string) {
	auth := base64.StdEncoding.EncodeToString([]byte(userID + ":" + c.AppSecret))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("AUTHORIZATION-APP-API", auth)
	req.Header.Set("EX-APP-ID", c.AppID)
	req.Header.Set("EX-APP-VERSION", c.AppVersion)
	if c.AAVersion != "" {
		req.Header.Set("AA-VERSION", c.AAVersion)
	}
}

func doReadBody(client *http.Client, req *http.Request) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func withFormatJSON(rawURL string) string {
	if strings.Contains(rawURL, "?") {
		return rawURL + "&format=json"
	}
	return rawURL + "?format=json"
}

func boolForm(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// snippet trims a response body for one-line error logging.
func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
