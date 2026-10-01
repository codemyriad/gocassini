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
	"talk.authentication": {"talk.hpb"},
	"talk.discovery":      {"talk.hpb", "talk.authentication"},
}

// readinessRowNames are the labels used when naming a blocker in a message.
// They match the panel's own labels: a message that calls a row something the
// reader cannot see on screen sends them hunting.
var readinessRowNames = map[string]string{
	"talk.hpb":            "High Performance Backend",
	"talk.authentication": "Signaling server credential",
	"talk.discovery":      "Talk connection",
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
	// In declared order, so a blocked prerequisite propagates down the chain in
	// one pass: no backend blocks the credential, which blocks the connection.
	for _, id := range readinessRowOrder {
		row, ok := byID[id]
		if !ok || blockedReadinessCodes[row.Code] || independentReadinessCodes[row.Code] {
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
			row.State = "not_verified"
			row.Code = "check_blocked"
			row.Message = fmt.Sprintf("Not checked: this depends on %s, which needs attention first.", name)
			// No action and no remedy of its own. Both belong to the
			// prerequisite, and offering them here splits one fix across rows.
			row.Action = ""
			row.Steps = nil
			row.Repair = ""
			break
		}
	}
}
