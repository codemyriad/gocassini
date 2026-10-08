package operator

import "fmt"

// Some checks cannot mean anything until another one passes.
//
// Talk recording is a chain: a High Performance Backend has to exist, Cassini
// needs that backend's credential, and only then can the connection be
// verified. Reported independently, one missing backend produced three rows
// wanting attention — the backend needing action, the credential unverified,
// and the connection needing attention — when there was exactly one thing to
// do. Worse, two of the three were unactionable: nothing an administrator does
// to the connection check helps while there is no backend to connect through.
//
// So a row whose prerequisite is unmet says what it is waiting for and stops
// asking to be looked at. The prerequisite keeps the attention, because it is
// the only row where acting changes anything.
var readinessPrerequisites = map[string][]string{
	// The credential used to sit between these two as a row of its own. It is
	// part of the backend row now — same server, same fact — so the chain is
	// one link shorter.
	"talk.discovery": {"talk.hpb", "talk.handoff"},
	// A test recording exercises every link at once: Talk hands the call over
	// (handoff), the recorder joins through the backend (hpb, authentication,
	// discovery), and the result is published to Nextcloud (storage). Listing
	// them all is what keeps the tool from appearing while it cannot succeed —
	// the old version offered itself regardless and failed for a reason that
	// was always already on screen, one row up.
	"test": {"storage", "talk.hpb", "talk.discovery", "talk.handoff"},
}

// readinessRowNames are the labels used when naming a blocker in a message.
// They match the panel's own labels: a message that calls a row something the
// reader cannot see on screen sends them hunting.
var readinessRowNames = map[string]string{
	"talk.hpb":       "High Performance Backend",
	"talk.discovery": "Talk connection",
	"talk.handoff":   "Recording credential",
	"storage":        "Recording storage",
	"test":           "Test recording",
}

// readinessProvenPrerequisites are prerequisites that must have been CHECKED
// and found good — not merely "not currently failing".
//
// The ordinary rule above treats a prerequisite nobody has run as no reason to
// suppress anything, which is right for a row reporting a FINDING: hiding one
// because a neighbour was never checked loses real information.
//
// It is wrong for the test recording, which is not a finding but an invitation.
// Observed on a stack with no High Performance Backend: the talk probe had not
// yet produced its row, so there was no `talk.hpb` for suppression to consult,
// and the panel offered "Record a test" on an installation that cannot record
// at all. The test must appear only where a test can succeed, so for it an
// unproven prerequisite — missing, or merely unverified — is also a reason to
// wait. Every row named here is emitted on every report once checked, so
// absence genuinely means "not established" rather than "nothing to say".
var readinessProvenPrerequisites = map[string][]string{
	"test": {"storage", "talk.hpb", "talk.discovery"},
}

// blockedReadinessCodes are the codes that already mean "waiting on something
// else". A row in one of these states blocks the rows that depend on IT, and is
// not rewritten by this pass — its own wording is better than anything generic.
var blockedReadinessCodes = map[string]bool{
	"internal_secret_not_needed_yet": true,
	"check_blocked":                  true,
}

// independentReadinessCodes are findings a row owns regardless of its
// prerequisites, and which must survive suppression.
//
// The distinction matters: most of what talk.discovery can report is a
// CONSEQUENCE of the chain — the credential was rejected, Nextcloud could not be
// reached, no room was chosen — and reporting those while the backend is missing
// is noise. But a Talk backend URL pointing at Cassini's own proxy is wrong
// whether or not a backend exists, nobody else will report it, and suppressing
// it would hide the single fault hardest to diagnose from the outside.
var independentReadinessCodes = map[string]bool{
	"talk_backend_url_invalid": true,
}

var nextcloudUnreachableCodes = map[string]bool{
	"nextcloud_unreachable":         true,
	"nextcloud_host_not_found":      true,
	"nextcloud_connection_refused":  true,
	"nextcloud_timeout":             true,
	"nextcloud_tls_untrusted":       true,
	"nextcloud_server_error":        true,
	"nextcloud_unexpected_response": true,
}

var probeStoppedBeforeBackendCodes = map[string]bool{
	"recording_auth_rejected":  true,
	"talk_or_room_unavailable": true,
	"test_room_invalid":        true,
}

var backendAwaitingProbeCodes = map[string]bool{
	"signaling_mode_unknown":  true,
	"hpb_declared_unverified": true,
}

func provenReadinessRow(row *readinessCheck) bool {
	return row != nil && (row.State == "passed" || row.State == "warn")
}

// suppressBlockedRows rewrites, in place, any row whose prerequisite is unmet.
//
// A prerequisite is unmet when it demands action, or when it is itself blocked.
// A prerequisite that merely has not RUN does not suppress anything: nothing can
// be concluded from a check nobody took, and hiding a finding on that basis
// would lose real information.
func suppressBlockedRows(checks []readinessCheck) {
	byID := make(map[string]*readinessCheck, len(checks))
	for i := range checks {
		byID[checks[i].ID] = &checks[i]
	}
	rootCauses := map[string]bool{}
	if discovery, hpb := byID["talk.discovery"], byID["talk.hpb"]; discovery != nil && hpb != nil && backendAwaitingProbeCodes[hpb.Code] {
		root := ""
		if handoff := byID["talk.handoff"]; discovery.Code == "recording_secret_missing" && handoff != nil && handoff.State == "needs_action" {
			root = "talk.handoff"
		} else if nextcloudUnreachableCodes[discovery.Code] || probeStoppedBeforeBackendCodes[discovery.Code] {
			root = "talk.discovery"
		}
		if root != "" {
			name := readinessRowNames[root]
			blockRow(hpb, name, false)
			if root == "talk.discovery" {
				rootCauses[root] = true
			} else {
				blockRow(discovery, name, false)
			}
			if test := byID["test"]; test != nil && !blockedReadinessCodes[test.Code] && provenReadinessRow(byID["storage"]) {
				blockRow(test, name, false)
			}
		}
	}
	if archive, storage := byID["archive.search"], byID["storage"]; archive != nil && storage != nil &&
		archive.Code == "search_reindex_archive_unreadable" && storage.State == "needs_action" {
		blockRow(archive, readinessRowNames["storage"], false)
	}
	// In declared order, so a blocked prerequisite propagates down the chain in
	// one pass: no backend blocks the credential, which blocks the connection.
	for _, id := range readinessRowOrder {
		row, ok := byID[id]
		if !ok || rootCauses[id] || blockedReadinessCodes[row.Code] || independentReadinessCodes[row.Code] {
			continue
		}
		blocked := false
		failedID, unprovenID := "", ""
		for _, prereqID := range readinessProvenPrerequisites[id] {
			prereq, present := byID[prereqID]
			if present && prereq.State == "needs_action" && failedID == "" {
				failedID = prereqID
			}
			if !provenReadinessRow(prereq) && unprovenID == "" {
				unprovenID = prereqID
			}
		}
		// Two different facts, said differently: a check that FAILED is
		// somebody's to fix, a check nobody has run is somebody's to run.
		if blocker := failedID; blocker != "" || unprovenID != "" {
			if blocker == "" {
				blocker = unprovenID
			}
			name := readinessRowNames[blocker]
			if name == "" {
				name = blocker
			}
			blockRow(row, name, failedID == "")
			blocked = true
		}
		if blocked {
			continue
		}
		for _, prereqID := range readinessPrerequisites[id] {
			prereq, ok := byID[prereqID]
			if !ok {
				continue
			}
			if prereq.State != "needs_action" && !blockedReadinessCodes[prereq.Code] {
				continue
			}
			name := readinessRowNames[prereqID]
			if name == "" {
				name = prereqID
			}
			blockRow(row, name, false)
			break
		}
	}
}

// blockRow rewrites a row to say what it is waiting for and stop asking to be
// looked at. No action and no remedy of its own: both belong to the
// prerequisite, and offering them here splits one fix across rows.
func blockRow(row *readinessCheck, blocker string, unchecked bool) {
	row.State = "not_verified"
	row.Code = "check_blocked"
	if unchecked {
		row.Message = fmt.Sprintf("Not checked: this depends on %s, which has not been checked yet. Run all checks first.", blocker)
	} else {
		row.Message = fmt.Sprintf("Not checked: this depends on %s, which needs attention first.", blocker)
	}
	row.Action = ""
	row.Steps = nil
	row.Repair = ""
	row.Running = false
	row.RepairFailed = false
}
