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
//	  ├── resolve administrator + ensure `cassini` owner  (OCS provisioning API)
//	  └── when AccessControl is on:
//	      ├── require the virtual "everyone" group       (Everyone Group app)
//	      ├── ensure "Cassini" Team folder (default-deny) (groupfolders addFolder)
//	      ├── assign everyone READ + owner group ALL      (groupfolders groups)
//	      ├── enable ACL + delegate `cassini` manager     (groupfolders acl/manageACL)
//	      ├── migrate legacy recording-viewers leaf ACLs  (WebDAV PROPFIND/PROPPATCH)
//	      ├── root ACL: cassini ALL + everyone READ       (WebDAV PROPPATCH)
//	      ├── remove legacy viewer/admin mappings/manager (groupfolders APIs)
//	      └── MKCOL Cassini/Recordings/meetings           (WebDAV)
//
// Everything is idempotent and best-effort: a failure is logged and never
// blocks startup or delivery, and each step is safe to re-run on every enable.

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

func (c ExAppConfig) resolveProvisioningUser(ctx context.Context, client *http.Client, logger *log.Logger) {
	if strings.TrimSpace(os.Getenv(envNCAdminUser)) != "" {
		return
	}
	admins, err := c.groupMembers(ctx, client, "admin")
	if err != nil || len(admins) == 0 {
		if logger != nil {
			logger.Printf("nc provision: admin lookup yielded no usable account (%v); acting as %q; set %s if this is wrong", err, c.provisioningUserFallback(), envNCAdminUser)
		}
		return
	}
	chosen := admins[0]
	for _, candidate := range admins {
		if candidate == defaultNextcloudAdminUser {
			chosen = candidate
			break
		}
	}
	var boxed any = chosen
	resolvedProvisioningUser.Store(&boxed)
	if logger != nil {
		logger.Printf("nc provision: acting as administrator %q", chosen)
	}
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
		// OCS decodes this as a PHP array. The scalar `groups` form returns a
		// bare 400 on live Nextcloud and leaves every act-as-cassini call broken.
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

// provisionNCFilesAccess first establishes the ownership/provisioning identities,
// then creates the idempotent group-folder + ACL topology when access control is
// enabled. The identity tier runs in both modes because uploads always act as
// the recordings owner. No-op only outside AppAPI. Best-effort: every failure is
// logged and never propagated. Runs on the enabled edge before startup sync.
func (c ExAppConfig) provisionNCFilesAccess(ctx context.Context, logger *log.Logger) {
	if !c.appAPIActive() {
		return
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()
	client := &http.Client{Timeout: ncProvisionTimeout}

	// 0. The administrator creates the service account before any DAV call acts
	//    as it. The narrow owner group supplies its write-capable Team-folder mount.
	c.resolveProvisioningUser(ctx, client, logger)
	if err := c.ensureRecordingsOwnerAccount(ctx, client, logger); err != nil {
		logger.Printf("nc provision: ensure recordings account %q failed: %v — recordings setup incomplete", ncRecordingsOwner, err)
		return
	}
	if !c.AccessControl {
		return
	}

	// 1. Require the virtual all-users group supplied by the Everyone Group app
	//    (or an equivalent group backend). Never create a static group with this
	//    name: an empty ordinary group would silently reintroduce the new-account
	//    mount race this topology exists to remove.
	hasEveryone, err := c.groupExists(ctx, client, ncRecordingsEveryoneGroup)
	if err != nil {
		logger.Printf("nc provision: check required universal group %q: %v — access control setup incomplete", ncRecordingsEveryoneGroup, err)
		return
	}
	if !hasEveryone {
		logger.Printf("nc provision: required universal group %q is unavailable; install/enable the Everyone Group app (group_everyone) — access control setup incomplete", ncRecordingsEveryoneGroup)
		return
	}

	// 2. The group folder with its default-deny floor (only settable at creation).
	folder, err := c.ensureRecordingsFolder(ctx, client)
	if err != nil {
		logger.Printf("nc provision: ensure group folder %q failed: %v — access control setup incomplete", ncRecordingsMount, err)
		return
	}
	folderID, ok := folder.idInt()
	if !ok {
		logger.Printf("nc provision: group folder %q has no usable id — aborting", ncRecordingsMount)
		return
	}

	// 3. Mount mappings: the virtual all-users group gets a READ capability
	//    ceiling; the narrow owner group gets ALL so the service account can
	//    write. ACL-manager status alone cannot elevate a READ mount to write.
	c.ensureFolderGroup(ctx, client, folderID, ncRecordingsEveryoneGroup, aclPermRead, logger)
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

	// 6. Narrow the root to the owner while migrating existing leaves. Otherwise
	//    a new user (who was never in the legacy static group) could inherit the
	//    new broad allow through an old private leaf that only denies
	//    recording-viewers. Any migration failure leaves the tree owner-only — an
	//    availability failure rather than an access leak — for the next retry.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, ownerOnlyContainerACLRules()); err != nil {
		logger.Printf("nc provision: establish owner-only migration floor on %q: %v", ncRecordingsMount, err)
		return
	}
	if err := c.selfHealLeafProtection(ctx, client, logger); err != nil {
		logger.Printf("nc provision: migrate/protect recording ACLs: %v — leaving root owner-only", err)
		return
	}
	if err := c.protectExistingCatalog(ctx, client); err != nil {
		logger.Printf("nc provision: migrate existing catalog ACL: %v — leaving root owner-only", err)
		return
	}

	// 7. The migration is complete, so grant everyone READ at the root. Each leaf
	//    now safely overrides that inherited grant as private or public.
	if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, ncRecordingsMount, containerACLRules()); err != nil {
		logger.Printf("nc provision: root container ACL %q: %v", ncRecordingsMount, err)
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
	for _, dir := range []string{ncRecordingsRoot, ncRecordingsRoot + "/meetings"} {
		if err := c.davMkcol(ctx, client, ncRecordingsOwner, dir); err != nil {
			logger.Printf("nc provision: mkcol %s: %v", dir, err)
		}
	}

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
