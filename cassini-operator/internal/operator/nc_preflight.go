package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
)

// Preflight for the two native Nextcloud apps the recordings substrate depends
// on and that an ExApp cannot install for itself (D-585 outcome 1).
//
// Before this, neither absence was asked about:
//
//	groupfolders     never probed at all. Its absence surfaced as
//	                 "ensure group folder failed" — a message that names
//	                 neither the cause nor the fix.
//	group_everyone   probed only indirectly, through groupExists("everyone"),
//	                 which cannot tell "the app is not installed" from
//	                 "the OCS call failed".
//
// Nextcloud AIO does not ship Team folders enabled, so a missing prerequisite is
// the common case rather than the exotic one, and telling an administrator which
// app to install is most of the value of noticing at all.
//
// The distinction that matters is NOT-INSTALLED vs CALL-FAILED. Only the first
// is actionable, and conflating them turns an `occ app:install` into an
// investigation. GET /ocs/v2.php/cloud/apps?filter=enabled gives it cleanly:
// verified against a live Nextcloud 34, the ids genuinely disappear from the
// list when the apps are disabled (53 -> 52 -> 51), so an id absent from a 2xx
// response means not installed, while a non-2xx or a transport error means the
// question could not be asked.
const (
	// ncAppGroupFolders supplies the shared tree and its advanced ACLs.
	ncAppGroupFolders = "groupfolders"
	// ncAppEveryoneGroup supplies the virtual `everyone` group, which is what
	// gives every account a read-only mount from the moment it is created —
	// no membership sweep, no convergence delay (#171).
	ncAppEveryoneGroup = "group_everyone"
)

// errAppMissing means a required app is definitely not enabled: the instance
// answered, and the id was not in its list. Actionable.
var errAppMissing = errors.New("required Nextcloud app is not enabled")

// errAppCheckFailed means the question could not be asked. Not the same thing,
// and not fixed by installing anything.
var errAppCheckFailed = errors.New("could not determine which Nextcloud apps are enabled")

// ncRequiredNativeApps is the ordered list reported in /status. Order is the
// dependency order an administrator would install them in.
var ncRequiredNativeApps = []string{ncAppGroupFolders, ncAppEveryoneGroup}

// enabledApps returns the set of app ids Nextcloud reports as enabled, acting as
// the resolved administrator (the route is admin-gated; an ExApp's app-scoped
// session gets 401 — see spike-x1).
func (c ExAppConfig) enabledApps(ctx context.Context, client *http.Client) (map[string]bool, error) {
	status, body, err := c.apiGet(ctx, client, c.ocsURL("/cloud/apps")+"?filter=enabled")
	if err != nil {
		return nil, err
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("GET /cloud/apps?filter=enabled -> %d: %s", status, snippet(body))
	}
	var payload struct {
		OCS struct {
			Data struct {
				Apps []string `json:"apps"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode /cloud/apps: %w", err)
	}
	set := make(map[string]bool, len(payload.OCS.Data.Apps))
	for _, id := range payload.OCS.Data.Apps {
		set[id] = true
	}
	return set, nil
}

// preflightNativeApps reports one status per required app and, when something is
// wrong, an error saying which KIND of wrong it is. The per-app list is returned
// in both cases: a caller reading /status after a failure needs to see which app
// was missing, not merely that one was.
func (c ExAppConfig) preflightNativeApps(ctx context.Context, client *http.Client) ([]ncPrerequisiteStatus, error) {
	enabled, err := c.enabledApps(ctx, client)
	if err != nil {
		out := make([]ncPrerequisiteStatus, 0, len(ncRequiredNativeApps))
		for _, app := range ncRequiredNativeApps {
			out = append(out, ncPrerequisiteStatus{
				Name:   app,
				State:  ncPrerequisiteUnknown,
				Detail: err.Error(),
			})
		}
		return out, fmt.Errorf("%w: %v", errAppCheckFailed, err)
	}

	out := make([]ncPrerequisiteStatus, 0, len(ncRequiredNativeApps))
	var missing []string
	for _, app := range ncRequiredNativeApps {
		if enabled[app] {
			out = append(out, ncPrerequisiteStatus{Name: app, State: ncPrerequisiteEnabled})
			continue
		}
		missing = append(missing, app)
		out = append(out, ncPrerequisiteStatus{
			Name:   app,
			State:  ncPrerequisiteMissing,
			Detail: fmt.Sprintf("run `occ app:install %s && occ app:enable %s`", app, app),
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return out, fmt.Errorf("%w: %v", errAppMissing, missing)
	}
	return out, nil
}

// firstMissingApp names the app to put in the recorded step, so /status carries
// `app_missing:groupfolders` rather than a list a monitor has to parse.
func firstMissingApp(prereqs []ncPrerequisiteStatus) string {
	for _, p := range prereqs {
		if p.State == ncPrerequisiteMissing {
			return p.Name
		}
	}
	return ""
}
