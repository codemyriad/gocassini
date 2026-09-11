package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// GET/PUT <base>/storage — the admin surface of the storage-mode opt-in
// (D-616 first pass).
//
// It is deliberately NOT part of /status. /status answers "is this deployment
// healthy" and promises every check on it is cheap; this answers "which storage
// model is this archive under, could it be the other one, and switch it" — a
// question with a verb attached. Putting a PUT that moves every recording in an
// instance behind the health endpoint would be the wrong shape for both.
//
//	GET   read the recorded mode + the last probe, and say what each mode
//	      would need. Never probes Nextcloud itself: the preflight's record is
//	      the source, exactly as /status uses it.
//	PUT   move the archive and record the new mode. One call, no partial
//	      states to poll — the transition holds the provisioning lock for its
//	      whole duration and re-runs the preflight before answering.
//
// ADMIN at the AppAPI proxy (appinfo/info.xml), like every other operator
// route. Nothing here is safe for a non-administrator to read, let alone call:
// the blockers name accounts and folder ids, and the PUT relocates the archive.

// storageModeOption describes one of the two models: whether it is the active
// one, whether it could be switched to, and — when it could not — what is
// missing and the commands that would fix it.
//
// The copy lives here rather than in the UI because a wrong instruction is a
// worse failure than a missing one, and this is the layer that knows the folder
// id, the group names and which prerequisite is actually absent. The Svelte
// component renders it and decides nothing.
type storageModeOption struct {
	Mode      string `json:"mode"`
	Label     string `json:"label"`
	Active    bool   `json:"active"`
	Available bool   `json:"available"`
	// Summary is what this mode means, in one sentence.
	Summary string `json:"summary"`
	// Consequence is what switching TO it would do to the archive that is
	// already there. It is the body of the confirmation prompt.
	Consequence string `json:"consequence"`
	// Blocker is the sentence naming what is missing. Empty when Available.
	Blocker string `json:"blocker,omitempty"`
	// Step is the machine-readable form of Blocker, keyed the same way
	// /status's recordings_access.step is.
	Step string `json:"step,omitempty"`
	// Instructions are shell lines an administrator can run. Derived from Setup
	// below, so the printed recipe and the executed one cannot drift.
	Instructions []string `json:"instructions,omitempty"`
	// Setup is the same thing as something to EXECUTE rather than retype
	// (D-671). Empty when the mode is already available.
	Setup []storageSetupStep `json:"setup,omitempty"`
	// Root is where this model keeps recordings, and Archive is what is actually
	// there right now.
	//
	// Both are reported for BOTH models, always (D-708). The question the setup
	// wizard is built around — "which of these should this Nextcloud use" — is
	// answered by what is already in each of them, and until this existed the
	// only counts that ever left the operator were for the mode already in force.
	Root    string              `json:"root"`
	Archive storageArchiveFacts `json:"archive"`
}

// storageArchiveFacts is one recordings root as the last probe saw it.
type storageArchiveFacts struct {
	// Probed says the tree was actually listed. Everything else is meaningless
	// when it is false, and false must never be rendered as "empty".
	Probed   bool `json:"probed"`
	Present  bool `json:"present"`
	Meetings int  `json:"meetings"`
	Catalog  bool `json:"catalog"`
}

func archiveFactsFor(facts ncArchiveFacts) storageArchiveFacts {
	return storageArchiveFacts{
		Probed:   facts.Probed,
		Present:  facts.Present,
		Meetings: facts.Meetings(),
		Catalog:  facts.Catalog,
	}
}

// storageServiceAccount is the `cassini` account, and the one thing about it an
// administrator has to be able to do (D-708).
//
// Cassini never sees the password. It is generated in the administrator's own
// browser, set through Nextcloud's provisioning API on their session, and shown
// once — which is why there is no field here for it and never will be: the
// operator authenticates as this account through AppAPI's act-as header, signed
// with the app secret, so a password on the operator's volume would be a
// credential at rest for an account nothing authenticates as.
//
// What the operator DOES supply is the copy: whether the account is there, and
// the `occ` line that resets it where Nextcloud refuses the browser.
type storageServiceAccount struct {
	User string `json:"user"`
	// Known says the probe answered. Exists is meaningless without it.
	Known  bool `json:"known"`
	Exists bool `json:"exists"`
	// ResetOcc is the equivalent command, for the administrator who would rather
	// run it — and the fallback for the releases where Nextcloud demands the
	// password on the request itself.
	ResetOcc string `json:"reset_occ"`
}

// storageStatusResponse is the body of both GET and a successful PUT, so the UI
// re-renders from one shape either way.
type storageStatusResponse struct {
	// Mode is "" when no preflight has resolved one yet — which is not the same
	// as "default", and the UI has to be able to tell them apart.
	Mode       string `json:"mode"`
	ModeSource string `json:"mode_source,omitempty"`
	// ModeConfirmed says a mode is settled: recorded, and not going to be
	// re-taken. It is true for every recorded mode since D-753, the enabled
	// edge's own resolution included, and false only while nothing is resolved.
	// Where the mode came from is ModeSource's job, not this one's.
	ModeConfirmed bool `json:"mode_confirmed"`
	// AwaitingChoice is always false (D-753) and kept in the shape so a client
	// built against the first pass still parses this response.
	//
	// It used to say that nothing was recorded at all, which was the state the
	// setup wizard existed to end. Nothing waits for an answer now: the enabled
	// edge resolves the mode from the archive it finds, and an edge that could
	// not read the archive reports that through State/Step — a fault to look at,
	// not a question to answer.
	AwaitingChoice bool `json:"awaiting_choice"`
	// FirstRun says the one-time dialog that tells an administrator who will be
	// able to read recordings has not been answered on this install (D-755).
	//
	// Per INSTALL, not per browser: the dialog states a fact about the archive,
	// and a second administrator opening the app is not a second first run. See
	// storageFirstRun for when it is false without anybody having answered.
	FirstRun bool `json:"first_run"`
	// Migration is how far a running mode switch has got, and null when none is
	// running. The switch itself is a blocking request holding provisionMu, so
	// this is only ever non-null to a CONCURRENT reader — which is the entire
	// point of it.
	Migration *storageMigrationProgress `json:"migration"`
	// ServiceAccount is the account every recording is written and read as, and
	// what an administrator can do about its password.
	ServiceAccount storageServiceAccount `json:"service_account"`
	// MigrationClean is false when a mode switch did not finish tidying up. The
	// archive is still complete at Mode's own root — that is the invariant — but
	// the OTHER root holds leftovers, and there is a button for it.
	MigrationClean bool `json:"migration_clean"`
	// PendingCleanup names that other root. Empty when MigrationClean.
	PendingCleanup string `json:"pending_cleanup,omitempty"`
	// StrandedRoot and StrandedRecordings report an archive sitting in the mode
	// that is NOT in force — the `Cassini` Team folder still holding recordings
	// on an instance running the default model, or the other way round.
	//
	// It is not an error: publishing and reading both work, against the root the
	// recorded mode names. It is the thing an administrator most needs told,
	// because the symptom is "my recordings are gone" and the cause is a mode
	// nobody switched. Switching modes copies them across.
	StrandedRoot       string `json:"stranded_root,omitempty"`
	StrandedRecordings int    `json:"stranded_recordings,omitempty"`
	// OK/State/Step/Detail mirror recordings_access, so an administrator
	// reading this page and one reading /status see the same verdict.
	OK        bool                `json:"ok"`
	State     string              `json:"state"`
	Step      string              `json:"step,omitempty"`
	Detail    string              `json:"detail,omitempty"`
	CheckedAt string              `json:"checked_at,omitempty"`
	Modes     []storageModeOption `json:"modes"`
	// Transition is present only on the PUT that performed one.
	Transition *storageTransitionResult `json:"transition,omitempty"`
	// Installs is present only on the POST that attempted app installs.
	Installs []appInstallOutcome `json:"installs,omitempty"`
	// Preview is present only on the POST that asked what a mode switch would
	// do. Nothing has happened when it is set.
	Preview *storageTransitionPreview `json:"preview,omitempty"`
}

// storageAction is the POST body. Two verbs share one route because AppAPI
// learns an ExApp's routes at REGISTRATION time — an already-installed app does
// not get a new one until it is re-registered, so a second path would 404 for
// exactly the administrators who most need it.
type storageAction struct {
	Action string `json:"action"`
	// AccessControlEnabled names the mode a `preview` asks about. Ignored by
	// every other action.
	AccessControlEnabled *bool `json:"access_control_enabled"`
}

const (
	// storageActionRecheck re-runs the enabled-edge preflight now. The browser
	// performs the setup writes itself (D-671), and the operator cannot see
	// them until it looks again — without this the Setup tab would keep showing
	// what was missing before the administrator fixed it.
	storageActionRecheck = "recheck"
	// storageActionInstallApps attempts the two native prerequisites from the
	// backend. It is the one part of the plan the browser cannot do, and the
	// backend can on releases that predate the password-confirmation hardening
	// or where the administrator has set a bypass range.
	storageActionInstallApps = "install_apps"
	// storageActionPreview reports what a mode switch WOULD do, without doing
	// any of it. The transition relocates an entire published archive and — going
	// into the Team folder — makes every already-published recording readable by
	// every account, so the confirmation has to state facts and not only policy.
	storageActionPreview = "preview"
	// storageActionAcknowledgeFirstRun records that an administrator has seen the
	// first-run dialog. It changes nothing about the archive and asks Nextcloud
	// nothing — it is a note in the operator's own settings file, so a fresh
	// install stops showing the dialog on every visit and on every browser.
	storageActionAcknowledgeFirstRun = "acknowledge_first_run"
	// storageActionFinishMigration completes a switch that stopped part way: it
	// clears the root the recorded mode does NOT name and marks the instance
	// settled. It is the one recovery action, and it is the same action whichever
	// half failed — see finishMigration.
	storageActionFinishMigration = "finish_migration"
)

// ncStorageSwitchTimeout bounds a whole mode switch, as opposed to
// ncProvisionTimeout, which bounds one HTTP call to Nextcloud.
//
// It is generous because the operation is a server-side copy of an entire
// archive and the alternative to finishing is leaving an instance unsettled. It
// exists at all so that a switch against a Nextcloud that has stopped answering
// cannot hold the provisioning lock for the life of the process.
const ncStorageSwitchTimeout = 60 * time.Minute

// storageUpdate is the PUT body: the same field name the config file uses, so
// there is one vocabulary for this decision end to end.
type storageUpdate struct {
	AccessControlEnabled *bool `json:"access_control_enabled"`
	ConfirmOverwrite     bool  `json:"confirm_overwrite,omitempty"`
}

const (
	storageLabelDefault          = "Default"
	storageLabelAccessControlled = "Access controlled"
)

// storageHandler serves GET/PUT <base>/storage. It is built from the
// ExAppConfig rather than hung off the Runtime because everything it does is
// Nextcloud-side; the Runtime only supplies the logger.
func (c ExAppConfig) storageHandler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, c.storageStatus(rt, nil))
		case http.MethodPost:
			c.handlePostStorage(w, r, rt)
		case http.MethodPut:
			c.handlePutStorage(w, r, rt)
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost+", "+http.MethodPut)
		}
	})
}

// handlePostStorage runs one of the two setup actions and answers with the
// refreshed state, so a caller never has to follow up with a GET to find out
// what changed.
func (c ExAppConfig) handlePostStorage(w http.ResponseWriter, r *http.Request, rt *Runtime) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	var in storageAction
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
			return
		}
	}

	// The preflight runs on a context the client cannot cancel.
	//
	// It WRITES the deployment's recorded health, and its failure path records
	// `unavailable`/`degraded` — so a browser that navigates away mid-probe
	// would leave the operator reporting a broken substrate that is fine, with
	// publishing and recording refused until the next enable. The request's own
	// deadline is the wrong lifetime for a side effect that outlives the
	// request; the probe carries its own.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ncProvisionTimeout)
	defer cancel()

	switch in.Action {
	case storageActionRecheck, "":
		// An empty body means recheck: it is the harmless action, and the one a
		// caller reaching for "look again" would guess.
		c.preflightNCStorage(ctx, rt.logger)
		writeJSON(w, http.StatusOK, c.storageStatus(rt, nil))
	case storageActionPreview:
		if in.AccessControlEnabled == nil {
			writeJSONError(w, http.StatusBadRequest, "access_control_enabled is required and must be true or false")
			return
		}
		preview, err := c.previewStorageModeSwitch(ctx, *in.AccessControlEnabled, rt.logger)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		// Answered alongside the current state, so the panel renders the diff
		// and the mode it is diffing against from one response — two round
		// trips could straddle a concurrent change.
		resp := c.storageStatus(rt, nil)
		resp.Preview = &preview
		writeJSON(w, http.StatusOK, resp)
	case storageActionFinishMigration:
		result, err := c.finishStorageMigration(ctx, rt.logger)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		c.preflightNCStorage(ctx, rt.logger)
		writeJSON(w, http.StatusOK, c.storageStatus(rt, &result))
	case storageActionAcknowledgeFirstRun:
		// No preflight and no probe: nothing about Nextcloud has changed, and a
		// dialog dismissal must not cost an administrator a round of probing.
		if err := acknowledgeStorageFirstRun(rt.logger); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c.storageStatus(rt, nil))
	case storageActionInstallApps:
		installs, err := c.attemptAppInstalls(ctx, rt.logger)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		// Re-probe regardless of the outcome: a partial success has to be
		// visible, and an install that worked is visible immediately (measured
		// at 0 s — the writer and the reader are the same worker pool).
		c.preflightNCStorage(ctx, rt.logger)
		resp := c.storageStatus(rt, nil)
		resp.Installs = installs
		writeJSON(w, http.StatusOK, resp)
	default:
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unknown action %q; expected %q, %q, %q, %q or %q", in.Action, storageActionRecheck, storageActionInstallApps, storageActionPreview, storageActionFinishMigration, storageActionAcknowledgeFirstRun))
	}
}

// attemptAppInstalls probes for what is missing and tries to install it. The
// probe is re-run rather than read from the record, because the administrator
// has very likely just changed something and a stale list would install the
// wrong set — or nothing.
func (c ExAppConfig) attemptAppInstalls(ctx context.Context, logger *log.Logger) ([]appInstallOutcome, error) {
	if !c.appAPIActive() {
		return nil, fmt.Errorf("apps can only be installed in a Nextcloud (AppAPI) deployment")
	}
	client := &http.Client{Timeout: ncProvisionTimeout}
	probe, err := c.probeNCStorage(ctx, client, logger)
	if err != nil {
		return nil, fmt.Errorf("could not inspect this Nextcloud: %w", err)
	}
	return c.installMissingApps(ctx, client, probe, logger), nil
}

// acknowledgeStorageFirstRun writes the acknowledgement down and mirrors it into
// this process.
//
// It takes no lock and touches no Nextcloud: the flag is not part of the mode
// state machine, and SaveStorageSettings carries it across every step of one,
// so there is nothing here for a concurrent switch to race with.
//
// A missing settings path is not an error, for the same reason it is not one in
// recordStorageMode: an operator without a persistent volume still runs, it just
// cannot outlive its container — and refusing to dismiss a dialog there would
// leave that deployment showing it forever.
func acknowledgeStorageFirstRun(logger *log.Logger) error {
	path := ncStorage.settingsPath()
	if path == "" {
		ncStorage.setFirstRunAcknowledged(true)
		if logger != nil {
			logger.Printf("nc storage: no settings path configured; the first-run acknowledgement governs this process only")
		}
		return nil
	}
	if err := AcknowledgeStorageFirstRun(path); err != nil {
		return fmt.Errorf("could not record that the first-run dialog was answered: %w", err)
	}
	ncStorage.setFirstRunAcknowledged(true)
	return nil
}

// finishStorageMigration is the handler-side wrapper for the recovery. It takes
// the same lock as the switch and the preflight, because clearing a root while
// one of those is copying into it would be the one way to lose an archive that
// the copy-then-flip ordering otherwise makes impossible.
func (c ExAppConfig) finishStorageMigration(ctx context.Context, logger *log.Logger) (storageTransitionResult, error) {
	if !c.appAPIActive() {
		return storageTransitionResult{}, fmt.Errorf("storage can only be repaired in a Nextcloud (AppAPI) deployment")
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()
	client := &http.Client{Timeout: ncProvisionTimeout}
	return c.finishMigration(ctx, client, logger)
}

func (c ExAppConfig) handlePutStorage(w http.ResponseWriter, r *http.Request, rt *Runtime) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	var in storageUpdate
	if err := json.Unmarshal(raw, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request JSON: %v", err))
		return
	}
	if in.AccessControlEnabled == nil {
		writeJSONError(w, http.StatusBadRequest, "access_control_enabled is required and must be true or false")
		return
	}
	want := *in.AccessControlEnabled

	// Everything below happens inside switchStorageMode, under the provisioning
	// lock, and that is load-bearing rather than tidy.
	//
	// "Already there" has two readings and the first pass only implemented one.
	// A SETTLED instance answers its current state: a double-click, or a retry of
	// a request whose response was lost, must not re-run a copy. An UNSETTLED one
	// is the state QA got stuck in — a switch that stopped after the flip, so the
	// recorded mode already equals the request while a stale copy sits at the
	// other root — and short-circuiting there made the one action that would
	// repair it unreachable from the UI.
	//
	// Deciding either of those HERE would decide it outside the lock, which is
	// how two concurrent PUTs for the same target both get past it and the second
	// one migrates a root onto itself.
	//
	// The context is deliberately not the request's. This is the one call that
	// COPIES an entire archive over WebDAV and rewrites the recorded mode; a
	// browser that navigates away, or a proxy that gives up, must not abort it
	// half way. Same reasoning as the POST handler above, with more at stake.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ncStorageSwitchTimeout)
	defer cancel()

	result, err := c.switchStorageMode(ctx, want, in.ConfirmOverwrite, rt.logger)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errTransitionNotReady) || errors.Is(err, errOverwriteConfirmationRequired) {
			// Nothing was touched and nothing is wrong with the operator — the
			// instance simply is not set up for the mode that was asked for.
			status = http.StatusConflict
		}
		rt.logger.Printf("storage mode switch to %s failed: %v", storageModeName(want), err)
		writeJSONError(w, status, err.Error())
		return
	}
	if result.Mode == "" {
		// A no-op: the mode asked for is the one in force and the instance is
		// settled. Nothing moved and nothing needs re-probing, so this stays as
		// cheap as the double-click that usually causes it.
		writeJSON(w, http.StatusOK, c.storageStatus(rt, nil))
		return
	}
	c.preflightNCStorage(ctx, rt.logger)
	writeJSON(w, http.StatusOK, c.storageStatus(rt, &result))
}

// storageStatus renders the current record. It reads the preflight's snapshot
// and never probes Nextcloud, for the same reason /status does not: this is a
// page an administrator may refresh, and the probe is a handful of round-trips.
func (c ExAppConfig) storageStatus(rt *Runtime, transition *storageTransitionResult) storageStatusResponse {
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	mode, source := ncStorage.snapshot()
	clean := ncStorage.migrationClean()
	probe, probed := ncAccessSubstrate.lastProbe()
	_, resolved := ncStorage.mode()
	resp := storageStatusResponse{
		Mode:           mode,
		ModeSource:     source,
		ModeConfirmed:  ncStorage.confirmedMode(),
		AwaitingChoice: false,
		FirstRun:       storageFirstRun(ncStorage.acknowledgedFirstRun(), resolved, probed, probe),
		Migration:      ncStorageMigration.snapshot(),
		ServiceAccount: storageServiceAccount{
			User:     ncRecordingsOwner,
			Known:    probed,
			Exists:   probed && probe.ServiceAccount,
			ResetOcc: fmt.Sprintf("occ user:resetpassword %s", ncRecordingsOwner),
		},
		MigrationClean: clean,
		OK:             access.OK,
		State:          access.State,
		Step:           access.Step,
		Detail:         access.Detail,
		CheckedAt:      access.CheckedAt,
		Transition:     transition,
	}
	if current, resolved := ncStorage.mode(); resolved {
		if !clean {
			resp.PendingCleanup = recordingsRootFor(!current)
		} else if probed {
			// Only when the instance is settled. While a migration is unfinished
			// the leftovers ARE the other root's contents, and calling them
			// "stranded" would invite a switch where the answer is a cleanup.
			if stranded := probe.strandedArchiveMeetings(current); stranded > 0 {
				resp.StrandedRoot = recordingsRootFor(!current)
				resp.StrandedRecordings = stranded
			}
		}
	}
	resp.Modes = []storageModeOption{
		storageOption(false, mode, probe, probed),
		storageOption(true, mode, probe, probed),
	}
	return resp
}

// storageFirstRun decides whether this install still owes an administrator the
// one-time dialog (D-755).
//
// Two ways to be past it, and the second is the whole reason this is a rule
// rather than a stored bit:
//
//	acknowledged   somebody answered the dialog. Recorded per install, so it
//	               stays answered for every administrator and every browser.
//	past it        a mode is recorded AND the last probe found recordings. An
//	               install that has been keeping recordings under a decided mode
//	               has had its first run, whichever release it happened under —
//	               and it would be a strange thing to stop an upgrade to tell
//	               somebody how an archive they have been using for a year works.
//
// Both halves of the second rule are load-bearing. Recordings without a recorded
// mode are the upgrade the operator has not resolved yet, and a recorded mode
// without recordings is the fresh install this dialog exists for — it is
// recorded on enable now, which is exactly not evidence that anybody saw
// anything. An unprobed instance answers "not past it": failing to look is not
// evidence of an empty archive, and the cost of being wrong is one dialog.
func storageFirstRun(acknowledged, modeRecorded, probed bool, probe ncStorageProbe) bool {
	if acknowledged {
		return false
	}
	if modeRecorded && probed && storageProbeFoundRecordings(probe) {
		return false
	}
	return true
}

// storageProbeFoundRecordings asks whether EITHER root holds a recording. Both,
// rather than the one the recorded mode names, because an archive stranded in
// the other mode is still an archive somebody has been recording into — the
// reading that decides "is this install past its first run".
func storageProbeFoundRecordings(probe ncStorageProbe) bool {
	return probe.ACLArchive.Meetings() > 0 || probe.DefaultArchive.Meetings() > 0
}

// storageOption builds one mode's entry. `probed` is false before any preflight
// has run, which makes both modes unavailable rather than guessing — an
// unchecked instance is not evidence that either would work.
func storageOption(accessControlled bool, activeMode string, probe ncStorageProbe, probed bool) storageModeOption {
	name := storageModeName(accessControlled)
	option := storageModeOption{
		Mode:        name,
		Label:       storageLabelDefault,
		Active:      activeMode == name,
		Summary:     storageModeSummary(accessControlled),
		Consequence: storageModeConsequence(accessControlled),
		Root:        recordingsRootFor(accessControlled),
	}
	if probed {
		option.Archive = archiveFactsFor(probe.archiveFor(accessControlled))
	}
	if accessControlled {
		option.Label = storageLabelAccessControlled
	}
	if !probed {
		option.Blocker = "Cassini has not checked this Nextcloud since it started. Setup runs when the app is enabled, so disable and re-enable it."
		option.Step = "unknown"
		return option
	}
	ready, step, detail := probe.sanityForTarget(accessControlled)
	option.Available = ready
	if !ready {
		option.Step = step
		option.Blocker = detail
		option.Setup = storageSetupPlan(accessControlled, probe)
		option.Instructions = storageModeInstructions(accessControlled, probe)
	}
	return option
}

func storageModeSummary(accessControlled bool) string {
	if accessControlled {
		return fmt.Sprintf(
			"Recordings live in the %q Team folder, and each one is readable only by the people who were in the meeting. Needs the Team folders and Everyone Group apps.",
			ncRecordingsMount)
	}
	return fmt.Sprintf(
		"Recordings live in the %q account's own %s — a private directory nobody else has a mount of — and everyone who can open Cassini can read all of them. Needs no extra Nextcloud apps.",
		ncRecordingsOwner, ncDefaultRecordingsRoot)
}

func storageModeConsequence(accessControlled bool) string {
	if accessControlled {
		return fmt.Sprintf(
			"Every recording already published is copied into the %q Team folder and left readable by every account — Cassini does not guess who was in a past meeting. Recordings published from now on are restricted to the people in the call. You can narrow an existing one afterwards from Files → Advanced permissions.",
			ncRecordingsMount)
	}
	return fmt.Sprintf(
		"Every recording already published is copied out of the %q Team folder into the %q account's own %s, and all of their access rules are dropped: after this, everyone who can open Cassini can read every recording, including the ones that were restricted to a call's participants. The Team folder itself is emptied but left in place, so switching back later is immediate.",
		ncRecordingsMount, ncRecordingsOwner, ncDefaultRecordingsRoot)
}
