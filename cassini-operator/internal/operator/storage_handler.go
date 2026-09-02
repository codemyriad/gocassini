package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Instructions are shell lines an administrator can run. The first pass
	// scaffolds nothing, so this IS the setup path.
	Instructions []string `json:"instructions,omitempty"`
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
}

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
		case http.MethodPut:
			c.handlePutStorage(w, r, rt)
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
		}
	})
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
