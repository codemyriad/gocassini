package operator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// The setup plan: what a storage mode still needs, as steps something can
// EXECUTE rather than as shell lines a human retypes (D-671).
//
// The first pass reported prose and `occ` commands and stopped there, because
// the ExApp genuinely cannot perform these writes: on every currently-shipping
// Nextcloud, `POST /cloud/groups`, `POST /cloud/users` and the Group Folders
// writes answer 403 to an act-as-user request (D-661). What changed is not that
// finding but where the request comes from.
//
//	                      ┌── ExApp act-as ──▶ no session token ──▶ 403
//	  the same write ─────┤
//	                      └── the admin's browser ──▶ real session ──▶ 200
//	                          (Nextcloud's own password-confirmation dialog
//	                           supplies the confirmation; the password never
//	                           leaves that dialog)
//
// So the plan is computed here — this is the layer that knows which
// prerequisite is missing, the folder id, and the group names — and executed
// there. Each step says which of the two can do it:
//
//	Browser: true    non-strict. The admin's session can do it after
//	                 Nextcloud's own confirmation dialog.
//	Browser: false   `#[PasswordConfirmationRequired(strict: true)]`. Strict
//	                 requires the password ON THE REQUEST — no session, however
//	                 recently confirmed, satisfies it (measured; see
//	                 spike-x1-browser-password-confirmation.md). Only the app
//	                 installs are in this class, and they are attempted by the
//	                 backend instead, which succeeds where the release or the
//	                 administrator's bypass range allows it.

// Setup actions. The front-end has one executor per action, so these are a
// contract: renaming one silently turns a step into a no-op.
const (
	setupActionEnableApp        = "enable_app"
	setupActionCreateGroup      = "create_group"
	setupActionCreateUser       = "create_user"
	setupActionCreateTeamFolder = "create_team_folder"
	setupActionMapGroup         = "map_group"
	setupActionEnableFolderACL  = "enable_folder_acl"
	setupActionDelegateManager  = "delegate_manager"
)

// storageSetupStep is one missing thing, and how to make it exist.
type storageSetupStep struct {
	// ID is stable and unique within a plan, so a UI can key progress on it.
	ID     string `json:"id"`
	Action string `json:"action"`
	// Title is what the confirmation prompt lists. One line, plain language:
	// this is what an administrator is agreeing to.
	Title string `json:"title"`
	// Args carries the action's parameters. Strings only — a plan crosses an
	// HTTP boundary and gets executed, so it stays boring on purpose.
	Args map[string]string `json:"args,omitempty"`
	// Browser reports whether the administrator's browser can perform this.
	Browser bool `json:"browser"`
	// Occ is the equivalent command, for the administrator who would rather run
	// it themselves — and the single source of the printed recipe.
	Occ string `json:"occ,omitempty"`
	// AppURL is where Nextcloud's OWN interface performs this, for the steps
	// the browser cannot. Relative to the Nextcloud root.
	AppURL string `json:"app_url,omitempty"`
}

// storageSetupPlan lists what the given mode still needs, in the order it has
// to happen: identities before the folder that maps them, the folder before its
// mappings, the mappings before the ACL that governs them.
//
// It is empty for a mode that is already available, which is what makes "is
// there anything to do" and "what is there to do" the same question.
func storageSetupPlan(accessControlled bool, p ncStorageProbe) []storageSetupStep {
	var steps []storageSetupStep

	if !p.ServiceAccount {
		steps = append(steps,
			storageSetupStep{
				ID:      "group",
				Action:  setupActionCreateGroup,
				Title:   fmt.Sprintf("Create the %q group", ncRecordingsOwnerGroup),
				Args:    map[string]string{"group": ncRecordingsOwnerGroup},
				Browser: true,
				Occ:     "occ group:add " + ncRecordingsOwnerGroup,
			},
			storageSetupStep{
				ID:     "account",
				Action: setupActionCreateUser,
				Title:  fmt.Sprintf("Create the %q service account, which owns every recording", ncRecordingsOwner),
				Args: map[string]string{
					"user":         ncRecordingsOwner,
					"group":        ncRecordingsOwnerGroup,
					"display_name": "Cassini recordings",
				},
				Browser: true,
				Occ:     fmt.Sprintf("occ user:add --group=%s %s", ncRecordingsOwnerGroup, ncRecordingsOwner),
			},
		)
	}

	if !accessControlled {
		return steps
	}

	// The two apps. Strict password confirmation, so the browser is out; the
	// backend attempts them and Nextcloud's own Apps page is the fallback.
	for _, prereq := range p.Prereqs {
		if prereq.State != ncPrerequisiteMissing {
			continue
		}
		steps = append(steps, storageSetupStep{
			ID:      "app:" + prereq.Name,
			Action:  setupActionEnableApp,
			Title:   fmt.Sprintf("Install and enable the %s app", nativeAppDisplayName(prereq.Name)),
			Args:    map[string]string{"app": prereq.Name},
			Browser: false,
			Occ:     fmt.Sprintf("occ app:install %s && occ app:enable %s", prereq.Name, prereq.Name),
			AppURL:  "/settings/apps",
		})
	}

	if !p.FolderPresent {
		steps = append(steps, storageSetupStep{
			ID:      "folder",
			Action:  setupActionCreateTeamFolder,
			Title:   fmt.Sprintf("Create the %q Team folder, where recordings live", ncRecordingsMount),
			Args:    map[string]string{"mount": ncRecordingsMount},
			Browser: true,
			Occ:     "occ groupfolders:create " + ncRecordingsMount,
		})
	}
	// The mapping/ACL steps address the folder by MOUNT POINT rather than by id,
	// because on a plan that also creates the folder there is no id yet. The
	// executor resolves it once, after the folder exists.
	folderRef := map[string]string{"mount": ncRecordingsMount}
	occFolder := "<id>"
	if id, ok := p.Folder.idInt(); ok && p.FolderPresent {
		occFolder = fmt.Sprintf("%d", id)
	}

	if !p.OwnerAll {
		steps = append(steps, storageSetupStep{
			ID:      "mount:" + ncRecordingsOwnerGroup,
			Action:  setupActionMapGroup,
			Title:   fmt.Sprintf("Give the %q group write access to the Team folder", ncRecordingsOwnerGroup),
			Args:    mergeArgs(folderRef, map[string]string{"group": ncRecordingsOwnerGroup, "permissions": fmt.Sprintf("%d", aclMaskAll)}),
			Browser: true,
			Occ:     fmt.Sprintf("occ groupfolders:group %s %s read write share delete", occFolder, ncRecordingsOwnerGroup),
		})
	}
	if !p.EveryoneRead {
		steps = append(steps, storageSetupStep{
			ID:      "mount:" + ncRecordingsEveryoneGroup,
			Action:  setupActionMapGroup,
			Title:   fmt.Sprintf("Give everyone read access to the Team folder, so people can reach the meetings they were in"),
			Args:    mergeArgs(folderRef, map[string]string{"group": ncRecordingsEveryoneGroup, "permissions": fmt.Sprintf("%d", aclPermRead)}),
			Browser: true,
			Occ:     fmt.Sprintf("occ groupfolders:group %s %s read", occFolder, ncRecordingsEveryoneGroup),
		})
	}
	if !p.ACLEnabled {
		steps = append(steps, storageSetupStep{
			ID:      "acl",
			Action:  setupActionEnableFolderACL,
			Title:   "Turn on advanced permissions, which is what restricts a recording to its meeting",
			Args:    folderRef,
			Browser: true,
			Occ:     "occ groupfolders:permissions " + occFolder + " --enable",
		})
	}
	if !p.OwnerManages {
		steps = append(steps, storageSetupStep{
			ID:      "manager",
			Action:  setupActionDelegateManager,
			Title:   fmt.Sprintf("Let %q manage those permissions, so it can set each recording's audience", ncRecordingsOwner),
			Args:    mergeArgs(folderRef, map[string]string{"user": ncRecordingsOwner}),
			Browser: true,
			Occ:     fmt.Sprintf("occ groupfolders:permissions %s -m --user %s", occFolder, ncRecordingsOwner),
		})
	}
	return steps
}

func mergeArgs(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// nativeAppDisplayName is the name an administrator will recognise from the App
// Store, alongside the id `occ` wants.
func nativeAppDisplayName(app string) string {
	switch app {
	case ncAppGroupFolders:
		return "Team folders (groupfolders)"
	case ncAppEveryoneGroup:
		return "Everyone Group (group_everyone)"
	default:
		return app
	}
}

// storageModeInstructions is the printed recipe, derived from the plan so the
// two cannot drift. An earlier revision maintained them separately and the
// documentation promised an order the UI did not emit.
func storageModeInstructions(accessControlled bool, p ncStorageProbe) []string {
	steps := storageSetupPlan(accessControlled, p)
	out := make([]string, 0, len(steps)+1)
	needsFolderID := false
	for _, step := range steps {
		if step.Occ == "" {
			continue
		}
		if strings.Contains(step.Occ, "<id>") {
			needsFolderID = true
		}
		out = append(out, step.Occ)
	}
	if needsFolderID {
		// The create prints the id; every later line needs it. Say so rather
		// than leaving a literal `<id>` unexplained.
		out = append(out, "# <id> is the folder id `groupfolders:create` printed")
	}
	return out
}

// --- Attempting the app installs from the backend ------------------------------
//
// The two app installs are the one part of the plan the browser cannot do, and
// the backend can — sometimes. `POST /cloud/apps/{id}` covers absent,
// downloaded-but-disabled and already-enabled in one call, and it succeeds when
// the release predates the password-confirmation hardening, or when the
// administrator has set `allowed_no_password_confirmation_ranges` for this
// app's address. Attempting it costs one request and turns a two-step manual
// detour into nothing at all on the instances where it works.

// appInstallOutcome is one app's result. The reason is machine-readable because
// the UI does something different for each: retry, hand off, or wait.
type appInstallOutcome struct {
	App    string `json:"app"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

const (
	appInstallEnabled = "enabled"
	// appInstallNeedsPassword means Nextcloud refused because this route
	// demands the administrator's password on the request itself. Nothing the
	// operator can do about it; the administrator finishes in Nextcloud's own
	// Apps page, where that flow lives.
	appInstallNeedsPassword = "password_confirmation_required"
	// appInstallStoreProblem is the single opaque 404 the App Store returns for
	// four different causes — unknown id, incompatible app, store disabled,
	// store unreachable. They are indistinguishable from here (D-661), so this
	// must never be reported as "the App Store is down".
	appInstallStoreProblem = "app_store_unavailable"
	appInstallFailed       = "failed"
)

// installMissingApps attempts every native prerequisite the probe found
// missing, and reports one outcome per app.
//
// Deliberately no retry. A failed App Store fetch poisons Nextcloud's own cache
// for five minutes (`RETRY_AFTER_FAILURE_SECONDS`), so an immediate second
// attempt returns the same 404 even after connectivity comes back — retrying
// here would only make a transient failure look permanent.
func (c ExAppConfig) installMissingApps(ctx context.Context, client *http.Client, probe ncStorageProbe, logger *log.Logger) []appInstallOutcome {
	var out []appInstallOutcome
	for _, prereq := range probe.Prereqs {
		if prereq.State != ncPrerequisiteMissing {
			continue
		}
		out = append(out, c.installApp(ctx, client, prereq.Name, logger))
	}
	return out
}

func (c ExAppConfig) installApp(ctx context.Context, client *http.Client, app string, logger *log.Logger) appInstallOutcome {
	status, body, err := c.apiPostForm(ctx, client, c.ocsURL("/cloud/apps/"+app), nil)
	if err != nil {
		logger.Printf("nc storage: install %s: %v", app, err)
		return appInstallOutcome{App: app, Reason: appInstallFailed, Detail: err.Error()}
	}
	code := ocsStatusCode(body)
	switch {
	case status/100 == 2 && (code == 100 || code == 200):
		logger.Printf("nc storage: installed and enabled %q", app)
		return appInstallOutcome{App: app, OK: true, Reason: appInstallEnabled}
	case status == http.StatusForbidden:
		// Both password-confirmation messages land here. Reported as one
		// condition because the remedy is the same: a human, in Nextcloud.
		return appInstallOutcome{
			App:    app,
			Reason: appInstallNeedsPassword,
			Detail: fmt.Sprintf("Nextcloud requires the administrator's password on this request, which Cassini does not have and will not ask for. Install %s from Nextcloud's own Apps page, or run `occ app:install %s && occ app:enable %s`.", nativeAppDisplayName(app), app, app),
		}
	case status == http.StatusNotFound || code == 998:
		return appInstallOutcome{
			App:    app,
			Reason: appInstallStoreProblem,
			Detail: fmt.Sprintf("Nextcloud could not fetch %s from the App Store. It answers the same way whether the app is unknown, incompatible with this Nextcloud, or the store is unreachable, so Cassini cannot tell you which — and it caches the failure for five minutes, so an immediate retry will say the same thing. Install it from Nextcloud's own Apps page, or run `occ app:install %s`.", nativeAppDisplayName(app), app),
		}
	default:
		return appInstallOutcome{App: app, Reason: appInstallFailed, Detail: fmt.Sprintf("HTTP %d: %s", status, snippet(body))}
	}
}
