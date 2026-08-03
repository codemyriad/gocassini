package operator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
// (companion to D-534). When per-participant access control is enabled, the
// recordings must live in a group folder ("Team folder") with advanced ACL and
// a default-deny floor, owned/managed by the recordings owner, and readable by
// every logged-in user so anyone can traverse to the meetings they were granted
// (per-file ACLs — applied at publish, webdav_acl.go — restrict each recording
// to its participants). Before this file that whole topology was a manual,
// documented `occ groupfolders:*` + group setup an admin had to run once.
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
//	  ├── ensure viewer group exists                     (OCS provisioning)
//	  ├── ensure "Cassini" group folder (default-deny)   (groupfolders addFolder)
//	  ├── assign viewer group READ + owner group ALL     (groupfolders groups)
//	  ├── enable advanced ACL + delegate owner manager   (groupfolders acl/manageACL)
//	  ├── root container ACL: owner ALL + viewer READ    (WebDAV PROPPATCH)
//	  │     → owner can write under default-deny; everyone can traverse
//	  ├── MKCOL Cassini/Recordings/meetings              (WebDAV) so the dir exists
//	  └── reconcile every user into the viewer group     (OCS provisioning)
//	        + a periodic reconcile so new accounts converge
//
// Everything is idempotent and best-effort: a failure is logged and never
// blocks startup or delivery, and each step is safe to re-run on every enable.

const (
	// ncRecordingsOwnerGroup is the group whose members get a write-capable mount
	// of the recordings group folder. The owner (ncRecordingsOwner) must be a
	// member so it can create/update recordings; under the default-deny ACL floor
	// a write mount alone is insufficient (see the root container ACL below), but
	// it is still required — ACL-management permission does not grant WebDAV
	// create/write.
	ncRecordingsOwnerGroup = ncRecordingsOwner

	// Two identities, deliberately distinct (D-532):
	//
	//	ownership     ncRecordingsOwner ("cassini")   owns the tree, writes the
	//	                                              recordings, manages their ACLs
	//	provisioning  the instance administrator      creates groups/folders/users
	//
	// They cannot be the same principal. Every provisioning route below —
	// groupfolders management, OCS user and group provisioning — requires admin
	// rights, which a service account must not have: it would make the identity
	// that holds every recording also able to reconfigure the instance.
	//
	// The recordings account is created by the provisioner, so it exists before
	// anything acts as it.

	ncProvisionTimeout = 90 * time.Second

	// defaultNextcloudAdminUser is the conventional administrator id, used only
	// as a fallback when one cannot be discovered.
	defaultNextcloudAdminUser = "admin"
	// envNCAdminUser pins the provisioning identity on instances where
	// discovery is wrong or undesirable.
	envNCAdminUser = "CASSINI_NC_ADMIN_USER"

	// viewerReconcile* bound the "keep every user in the viewer group" sweep. The
	// enabled-edge reconcile covers everyone present at install; the periodic one
	// converges accounts created later (Nextcloud has no built-in "all users"
	// group and an ExApp cannot hook user creation), so a brand-new user gains
	// read/traversal of the recordings directory within one interval.
	viewerUserPageSize      = 200
	viewerReconcileMaxUsers = 50000
	viewerReconcileInterval = 15 * time.Minute
)

// reconcileTickerOnce guards the single background reconcile goroutine: the
// enabled edge can fire more than once (re-enable, restart), but only one ticker
// should run per process.
// resolvedProvisioningUser caches the discovered administrator for the process
// lifetime; an instance's administrator does not change under a running
// container.
var resolvedProvisioningUser atomic.Pointer[any]

var reconcileTickerOnce sync.Once

// provisionMu serializes provisioning: EnabledCallback is dispatched in a
// goroutine and can fire more than once (admin double-action, AppAPI retry), and
// the find-then-create folder step is not atomic — without this two concurrent
// runs could create duplicate "Cassini" folders.
var provisionMu sync.Mutex

// provisioningActive mirrors the ExApp enabled state so the background reconcile
// sweep goes quiet after the app is disabled (AppAPI rejects the ExApp's
// act-as-user calls once disabled, which would otherwise 401 every interval).
var provisioningActive atomic.Bool

// SetProvisioningActive records the enabled/disabled edge for the background
// reconcile sweep. Called from the AppAPI enabled callback.
func SetProvisioningActive(active bool) { provisioningActive.Store(active) }

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

// provisioningUser is the identity that performs privileged Nextcloud setup.
//
// AppAPI lets an ExApp act as any user, so this is a choice rather than a
// credential: the operator acts as an administrator for the handful of calls
// that genuinely need admin rights, and as the recordings owner for everything
// else. Resolved once per process and cached — an instance's administrator does
// not change while the container runs.
//
// "admin" is the fallback, not the assumption: it is only conventionally the
// administrator's user id, and an instance whose admin is called something else
// would otherwise have every provisioning call rejected.
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

// resolveProvisioningUser discovers an administrator and caches it. Called at
// the start of provisioning, before any privileged request.
func (c ExAppConfig) resolveProvisioningUser(ctx context.Context, client *http.Client, logger *log.Logger) {
	if strings.TrimSpace(os.Getenv(envNCAdminUser)) != "" {
		return // explicitly configured; do not second-guess it
	}
	admins, err := c.groupMembers(ctx, client, "admin")
	if err != nil || len(admins) == 0 {
		// Fall back to the conventional name. If that is wrong the provisioning
		// calls fail loudly rather than silently doing nothing.
		if logger != nil && err != nil {
			logger.Printf("nc provision: admin lookup failed (%v); acting as %q", err, c.provisioningUserFallback())
		}
		return
	}
	chosen := admins[0]
	for _, candidate := range admins {
		if candidate == defaultNextcloudAdminUser {
			chosen = candidate // prefer the conventional one when present
			break
		}
	}
	var boxed any = chosen
	resolvedProvisioningUser.Store(&boxed)
	if logger != nil {
		logger.Printf("nc provision: acting as administrator %q", chosen)
	}
}

// ensureRecordingsOwnerAccount creates the recordings service account if it is
// absent, and puts it in the owner group so it gets a write-capable mount of
// the group folder.
//
// Idempotent: an existing account is left exactly as it is — never re-created,
// never re-passworded, never disabled. OCS reports "user already exists" as
// statuscode 102, which is success here.
//
// The generated password is never used again and is deliberately not logged or
// persisted: the operator authenticates as this account through AppAPI, not
// with a password. OCS simply requires one at creation.
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
		// Already there from a previous enable. Membership is still ensured
		// below, because the group could have been created after the account.
	default:
		return fmt.Errorf("create service account -> %d: %s", status, snippet(body))
	}

	if err := c.addUserToGroup(ctx, client, ncRecordingsOwner, ncRecordingsOwnerGroup); err != nil {
		return fmt.Errorf("add %q to %q: %w", ncRecordingsOwner, ncRecordingsOwnerGroup, err)
	}
	return nil
}

// randomPassword generates a password that satisfies Nextcloud's policy and is
// then discarded.
func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	// Mixed case, digits and a symbol, so a strict password policy cannot
	// reject the account we are about to depend on.
	return "Cw1!" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// provisionNCFilesAccess establishes the two identities Cassini acts as, then
// creates (idempotently) the group folder + groups + ACL topology the
// access-control model needs and reconciles the viewer group. No-op unless
// AppAPI is active. Runs on the enabled edge (in the EnabledCallback
// goroutine), before the archive startup sync.
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

	// 0. Establish both identities before anything uses either: discover an
	//    administrator to act as, then make sure the recordings account exists.
	//    Ordering is load-bearing — every step below writes as the owner, so
	//    creating it later would point those calls at a non-existent user.
	c.resolveProvisioningUser(ctx, client, logger)
	if err := c.ensureRecordingsOwnerAccount(ctx, client, logger); err != nil {
		logger.Printf("nc provision: ensure recordings account %q failed: %v — recordings setup incomplete", ncRecordingsOwner, err)
		ncAccessSubstrate.fail("recordings owner account "+ncRecordingsOwner, err)
		return
	}
	// 1. The viewer group: the mount group whose members can traverse the
	//    directory. Everyone is reconciled into it (step 8).
	if err := c.ensureGroup(ctx, client, ncRecordingsViewerGroup); err != nil {
		logger.Printf("nc provision: ensure viewer group %q: %v", ncRecordingsViewerGroup, err)
	}

	// 2. The group folder with its default-deny floor (only settable at creation).
	folder, err := c.ensureRecordingsFolder(ctx, client)
	if err != nil {
		logger.Printf("nc provision: ensure group folder %q failed: %v — access control setup incomplete", ncRecordingsMount, err)
		// By far the most likely cause, and the one an admin can act on: the
		// Group folders ("Team folders") app is the single prerequisite an
		// ExApp cannot install for itself.
		ncAccessSubstrate.fail("group folder "+ncRecordingsMount+" (is the Group folders app enabled?)", err)
		return
	}
	folderID, ok := folder.idInt()
	if !ok {
		logger.Printf("nc provision: group folder %q has no usable id — aborting", ncRecordingsMount)
		ncAccessSubstrate.fail("group folder "+ncRecordingsMount+" has no usable id", nil)
		return
	}

	// 3. Mount mappings: viewers get READ, the owner group gets ALL (write mount).
	c.ensureFolderGroup(ctx, client, folderID, ncRecordingsViewerGroup, aclPermRead, logger)
	c.ensureFolderGroup(ctx, client, folderID, ncRecordingsOwnerGroup, aclMaskAll, logger)

	// 4. Advanced ACL on (idempotent).
	if err := c.folderSetACL(ctx, client, folderID, true); err != nil {
		logger.Printf("nc provision: enable advanced ACL folder=%d: %v", folderID, err)
	}

	// 5. Delegate the owner as ACL manager so it can PROPPATCH per-path rules.
	//    Re-adding an existing manager is a 500, so only add when absent.
	if !folder.hasManager("user", ncRecordingsOwner) {
		if err := c.folderManageACL(ctx, client, folderID, "user", ncRecordingsOwner, true); err != nil {
			logger.Printf("nc provision: delegate ACL manager folder=%d user=%s: %v", folderID, ncRecordingsOwner, err)
		}
	}

	// 6. Root container ACL. Under default-deny nobody — not even the owner — may
	//    act inside the folder without an explicit grant, so grant the owner ALL
	//    (to create/manage recordings) and the viewer group READ (to traverse).
	//    Inherited down the tree; each leaf recording overrides it at publish.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, containerACLRules()); err != nil {
		logger.Printf("nc provision: root container ACL %q: %v", ncRecordingsMount, err)
	}

	// 7. Materialize the canonical collections so the directory exists right after
	//    install, before any recording. MKCOL of the mount root is a harmless 405.
	for _, dir := range []string{ncRecordingsRoot, ncRecordingsRoot + "/meetings"} {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			logger.Printf("nc provision: mkcol %s: %v", dir, err)
		}
	}

	// 8. Put every current user in the viewer group, and keep new ones
	//    converging. This runs OFF the enabled-edge critical path (in the
	//    reconcile goroutine, which sweeps immediately then every interval) so a
	//    large user base cannot delay the archive delivery that follows.
	c.startViewerReconcileLoop(ctx, logger)

	ncAccessSubstrate.succeed()
	logger.Printf("nc provision: recordings access control provisioned folder_id=%d mount=%s root=%s owner=%s viewer_group=%s", folderID, ncRecordingsMount, ncRecordingsRoot, ncRecordingsOwner, ncRecordingsViewerGroup)
}

// containerACLRules grants the owner full control and the viewer group read on a
// recordings container directory. Applied to the group folder root and inherited
// down: it lets the owner write under the default-deny floor and lets every
// viewer traverse to the meetings, while each leaf recording overrides the
// viewer grant with a deny + per-participant allows at publish (webdav_acl.go).
func containerACLRules() []aclRule {
	return []aclRule{
		{Type: "user", ID: ncRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll},
		{Type: "group", ID: ncRecordingsViewerGroup, Mask: aclMaskAll, Permissions: aclPermRead},
	}
}

// --- Group Folders app HTTP surface (/index.php/apps/groupfolders/...) --------
//
// These are the app's own (non-OCS) routes, but sending OCS-APIRequest: true
// bypasses CSRF and returns an OCS-wrapped JSON envelope. They are guarded by
// RequireGroupFolderAdmin, which a full admin (the owner acts as "admin")
// satisfies. PasswordConfirmationRequired is skipped for the synthetic AppAPI
// session (no confirmable password token), the same way it is for basic auth.

// gfFolder is the subset of a Group Folders folder record this code reads.
type gfFolder struct {
	ID         json.Number `json:"id"`
	MountPoint string      `json:"mount_point"`
	Manage     []gfManage  `json:"manage"`
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
func (c ExAppConfig) ensureFolderGroup(ctx context.Context, client *http.Client, folderID int, group string, perms int, logger *log.Logger) {
	// addGroup: 2xx on first add, non-2xx ("Group already assigned") afterwards.
	if status, body, err := c.apiPostForm(ctx, client, c.gfURL(fmt.Sprintf("/folders/%d/groups", folderID)), url.Values{"group": {group}}); err != nil {
		logger.Printf("nc provision: add group %q to folder=%d: %v", group, folderID, err)
	} else if status/100 != 2 && !strings.Contains(strings.ToLower(string(body)), "already") {
		logger.Printf("nc provision: add group %q to folder=%d -> %d: %s", group, folderID, status, snippet(body))
	}
	if status, body, err := c.apiPostForm(ctx, client, c.gfURL(fmt.Sprintf("/folders/%d/groups/%s", folderID, url.PathEscape(group))), url.Values{"permissions": {fmt.Sprintf("%d", perms)}}); err != nil {
		logger.Printf("nc provision: set permissions group=%q folder=%d: %v", group, folderID, err)
	} else if status/100 != 2 {
		logger.Printf("nc provision: set permissions group=%q folder=%d -> %d: %s", group, folderID, status, snippet(body))
	}
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

// ensureGroup creates a group, treating "already exists" (statuscode 102) as
// success.
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

// reconcileViewerGroupMembers adds every user missing from the viewer group.
// Returns (added, totalUsers). Diffs against current membership so it only
// issues an add per genuinely-missing user and stays quiet on steady state.
func (c ExAppConfig) reconcileViewerGroupMembers(ctx context.Context, client *http.Client, logger *log.Logger) (int, int, error) {
	members, err := c.groupMembers(ctx, client, ncRecordingsViewerGroup)
	if err != nil {
		return 0, 0, fmt.Errorf("list viewer group members: %w", err)
	}
	memberSet := make(map[string]bool, len(members))
	for _, m := range members {
		memberSet[m] = true
	}
	added, total := 0, 0
	for offset := 0; offset < viewerReconcileMaxUsers; offset += viewerUserPageSize {
		users, err := c.listUsers(ctx, client, viewerUserPageSize, offset)
		if err != nil {
			return added, total, err
		}
		for _, uid := range users {
			uid = strings.TrimSpace(uid)
			if uid == "" {
				continue
			}
			total++
			if memberSet[uid] {
				continue
			}
			if err := c.addUserToGroup(ctx, client, uid, ncRecordingsViewerGroup); err != nil {
				// Best-effort: a single user failing must not abort the sweep.
				continue
			}
			memberSet[uid] = true
			added++
		}
		if len(users) < viewerUserPageSize {
			return added, total, nil
		}
	}
	// The loop exited on the cap rather than a short final page: users beyond
	// viewerReconcileMaxUsers were not reconciled and will not see the archive.
	// Log it rather than truncate silently.
	if logger != nil {
		logger.Printf("nc provision: WARNING viewer reconcile hit the %d-user cap; accounts beyond it are not in %q and cannot see recordings", viewerReconcileMaxUsers, ncRecordingsViewerGroup)
	}
	return added, total, nil
}

func (c ExAppConfig) listUsers(ctx context.Context, client *http.Client, limit, offset int) ([]string, error) {
	u := c.ocsURL("/cloud/users") + fmt.Sprintf("?limit=%d&offset=%d", limit, offset)
	status, body, err := c.apiGet(ctx, client, u)
	if err != nil {
		return nil, err
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("list users -> %d: %s", status, snippet(body))
	}
	return parseOCSUserList(body)
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

// ensureParticipantsInViewerGroup best-effort adds each user participant to the
// viewer group so the recordings group folder mounts for them promptly. A
// participant is granted per-file read at publish, but can only reach the file
// once the folder is mounted — i.e. once they are a viewer-group member. The
// periodic reconcile would add them within the interval; doing it here lets a
// participant see their recording on the next viewer load instead of waiting.
// Group/circle participants are covered by the all-users reconcile, so only
// user mappings are handled.
func (c ExAppConfig) ensureParticipantsInViewerGroup(ctx context.Context, client *http.Client, mappings []aclMapping, logger *log.Logger) {
	for _, m := range mappings {
		if m.Type != "user" || strings.TrimSpace(m.ID) == "" {
			continue
		}
		if err := c.addUserToGroup(ctx, client, m.ID, ncRecordingsViewerGroup); err != nil && logger != nil {
			logger.Printf("nc provision: add participant %q to viewer group: %v", m.ID, err)
		}
	}
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

// parseOCSUserList reads ocs.data.users from a provisioning response.
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

// startViewerReconcileLoop starts (once per process) a background sweep that
// keeps the viewer group == all users: an immediate pass covers everyone present
// at install, and a periodic pass converges accounts created later, so a new
// account gains access to the recordings directory within one interval. Each
// sweep is bounded and skipped while the app is disabled.
func (c ExAppConfig) startViewerReconcileLoop(ctx context.Context, logger *log.Logger) {
	reconcileTickerOnce.Do(func() {
		sweep := func() {
			if !provisioningActive.Load() {
				return
			}
			sweepCtx, cancel := context.WithTimeout(ctx, ncProvisionTimeout)
			defer cancel()
			client := &http.Client{Timeout: ncProvisionTimeout}
			added, total, err := c.reconcileViewerGroupMembers(sweepCtx, client, logger)
			if err != nil {
				logger.Printf("nc provision: viewer reconcile: %v", err)
			} else if added > 0 {
				logger.Printf("nc provision: viewer reconcile added=%d of %d users", added, total)
			}
		}
		go func() {
			ticker := time.NewTicker(viewerReconcileInterval)
			defer ticker.Stop()
			sweep()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					sweep()
				}
			}
		}()
	})
}

// --- HTTP helpers -------------------------------------------------------------

// apiGet issues an authenticated JSON GET as the recordings owner. `format=json`
// forces the OCS envelope to JSON regardless of Accept handling.
func (c ExAppConfig) apiGet(ctx context.Context, client *http.Client, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, withFormatJSON(rawURL), nil)
	if err != nil {
		return 0, nil, err
	}
	c.setAppAPIProvisionHeaders(req)
	return doReadBody(client, req)
}

// apiPostForm issues an authenticated form-encoded POST as the recordings owner.
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

// setAppAPIProvisionHeaders sets the AppAPI act-as-owner auth plus the OCS
// request marker. OCS-APIRequest is what lets the Group Folders frontpage routes
// skip CSRF and answer JSON; the provisioning API requires it too.
func (c ExAppConfig) setAppAPIProvisionHeaders(req *http.Request) {
	auth := base64.StdEncoding.EncodeToString([]byte(c.provisioningUser() + ":" + c.AppSecret))
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
