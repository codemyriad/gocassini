package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
}

// storageStatusResponse is the body of both GET and a successful PUT, so the UI
// re-renders from one shape either way.
type storageStatusResponse struct {
	// Mode is "" when no preflight has resolved one yet — which is not the same
	// as "default", and the UI has to be able to tell them apart.
	Mode       string `json:"mode"`
	ModeSource string `json:"mode_source,omitempty"`
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
)

// storageUpdate is the PUT body: the same field name the config file uses, so
// there is one vocabulary for this decision end to end.
type storageUpdate struct {
	AccessControlEnabled *bool `json:"access_control_enabled"`
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
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unknown action %q; expected %q, %q or %q", in.Action, storageActionRecheck, storageActionInstallApps, storageActionPreview))
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

	// Already there: answer the current state rather than moving an archive
	// that is already where it belongs. A double-click on the active button, or
	// a retry of a request whose response was lost, must not re-run a move.
	if current, resolved := ncStorage.mode(); resolved && current == want {
		writeJSON(w, http.StatusOK, c.storageStatus(rt, nil))
		return
	}

	result, err := c.switchStorageMode(r.Context(), want, rt.logger)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errTransitionNotReady) {
			// Nothing was touched and nothing is wrong with the operator — the
			// instance simply is not set up for the mode that was asked for.
			status = http.StatusConflict
		}
		rt.logger.Printf("storage mode switch to %s failed: %v", storageModeName(want), err)
		writeJSONError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c.storageStatus(rt, &result))
}

// storageStatus renders the current record. It reads the preflight's snapshot
// and never probes Nextcloud, for the same reason /status does not: this is a
// page an administrator may refresh, and the probe is a handful of round-trips.
func (c ExAppConfig) storageStatus(rt *Runtime, transition *storageTransitionResult) storageStatusResponse {
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	mode, source := ncStorage.snapshot()
	resp := storageStatusResponse{
		Mode:       mode,
		ModeSource: source,
		OK:         access.OK,
		State:      access.State,
		Step:       access.Step,
		Detail:     access.Detail,
		CheckedAt:  access.CheckedAt,
		Transition: transition,
	}
	probe, probed := ncAccessSubstrate.lastProbe()
	resp.Modes = []storageModeOption{
		storageOption(false, mode, probe, probed),
		storageOption(true, mode, probe, probed),
	}
	return resp
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
		"Recordings live in the %q account's own %s and everyone who can open Cassini can read all of them. Needs no extra Nextcloud apps.",
		ncRecordingsOwner, ncRecordingsRoot)
}

func storageModeConsequence(accessControlled bool) string {
	if accessControlled {
		return fmt.Sprintf(
			"Every recording already published will be moved into the %q Team folder and left readable by every account — Cassini does not guess who was in a past meeting. Recordings published from now on are restricted to the people in the call. You can narrow an existing one afterwards from Files → Advanced permissions.",
			ncRecordingsMount)
	}
	return fmt.Sprintf(
		"Every recording already published will be moved out of the %q Team folder into the %q account's own home, and all of their access rules will be dropped: after this, everyone who can open Cassini can read every recording, including the ones that were restricted to a call's participants.",
		ncRecordingsMount, ncRecordingsOwner)
}
