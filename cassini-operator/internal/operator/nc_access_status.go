package operator

import (
	"fmt"
	"sync"
)

// Provisioning health, made observable (D-554 outcome 3, D-545 AC-7) and then
// made diagnosable (D-585).
//
// Every step of provisionNCFilesAccess used to be best-effort in the strongest
// sense: a failure was a logger.Printf and nothing else. Nothing recorded it,
// nothing reported it, and the operator came up looking healthy. That is the
// worst possible shape for this particular failure, because an ExApp whose
// group folder does not exist still answers every request — it just serves
// nobody their recordings.
//
//	enabled edge ──▶ provisionNCFilesAccess ──▶ record each step's outcome
//	                                                     │
//	                                            ncAccessSubstrateStatus
//	                                                     │
//	                          GET /status ◀──────────────┘   no live traffic
//
// It reports the last recorded outcome rather than probing Nextcloud, because
// /status promises that every check is cheap ("no transcription, no doctor
// subprocess") and this condition only changes when provisioning runs.
//
// D-554 recorded a bool, which could say that something was wrong but not what
// kind of wrong. The distinction an administrator actually needs is between a
// named thing being ABSENT and a call having FAILED:
//
//	unavailable  a named thing is missing — an app, an administrator. The admin
//	             can act on it directly, and the step names what to install or set.
//	degraded     a call failed. The topology is not proven and the cause is not
//	             something this operator can name.
//
// Both mean recordings reach nobody, so both answer 503; they differ in what an
// administrator does next, which is why the step is machine-readable.
type ncSubstrateState string

const (
	// ncSubstrateProvisioned means no provisioning step aborted. Steps that are
	// recorded but non-fatal (folderManageACL, the MKCOL loop) can still have
	// failed, so this is a floor rather than a proof that every mapping is
	// present — the install-shaped check in ci-e2e-install-exapp.sh is what
	// proves the topology itself.
	ncSubstrateProvisioned ncSubstrateState = "provisioned"
	// ncSubstrateDegraded means a provisioning call failed.
	ncSubstrateDegraded ncSubstrateState = "degraded"
	// ncSubstrateUnavailable means a named prerequisite is absent.
	ncSubstrateUnavailable ncSubstrateState = "unavailable"
	// ncSubstrateNotApplicable means this deployment serves no recordings from
	// Nextcloud Files, so there is no substrate to expect.
	ncSubstrateNotApplicable ncSubstrateState = "not_applicable"
	// ncSubstrateUnknown means a substrate IS expected and provisioning has not
	// run in this process yet. Reachable after a bare container restart, because
	// provisioning is driven by the AppAPI enabled edge (D-541).
	ncSubstrateUnknown ncSubstrateState = "unknown"
)

// ncPrerequisiteStatus reports one native Nextcloud app an ExApp cannot install
// for itself. "missing" is actionable — install it; "unknown" means the check
// itself could not be completed, which is a different problem.
type ncPrerequisiteStatus struct {
	Name   string
	State  string // "enabled" | "missing" | "unknown"
	Detail string
}

const (
	ncPrerequisiteEnabled = "enabled"
	ncPrerequisiteMissing = "missing"
	ncPrerequisiteUnknown = "unknown"
)

type ncAccessSubstrateStatus struct {
	mu sync.Mutex
	// applicable is false for a standalone operator, and for an ExApp pinned to
	// the local sink: neither serves recordings from Nextcloud Files, so neither
	// can be broken for want of a substrate.
	applicable bool
	state      ncSubstrateState
	// step is machine-readable and stable, so a test or a monitor can key on it:
	// "app_missing:group_everyone", "administrator", "mount_mapping:everyone".
	step   string
	detail string
	// adminUser is the account provisioning resolved and acted as. Empty until
	// resolution succeeds — its absence is itself the diagnosis when the step is
	// "administrator".
	adminUser string
	prereqs   []ncPrerequisiteStatus
	// checkedAtUTC is when provisioning last ran to completion or gave up.
	// Empty means it has not run yet.
	checkedAtUTC string
}

// ncAccessSubstrate is the process-wide record. It is a package-level singleton
// for the same reason provisionMu and resolvedProvisioningUser are: provisioning
// is driven by the AppAPI enabled callback, which has no Runtime in scope.
var ncAccessSubstrate ncAccessSubstrateStatus

// markApplicable records that this deployment is expected to have a substrate.
//
// Called ONCE, at operator startup, for an AppAPI deployment whose resolved sink
// is Nextcloud Files — and nowhere else. Applicability is a property of the
// deployment, not of whether provisioning happened to run, so the recorders
// below deliberately do not set it:
//
//   - marking it at startup rather than inside the provisioner is what makes a
//     restarted container report `unknown` instead of passing itself off as a
//     standalone operator with nothing to provision (D-541 made visible);
//   - keeping it OUT of the recorders is what stops an ExApp pinned to
//     CASSINI_PUBLISH_SINK=local from reporting an unhealthy substrate it serves
//     nothing from. Provisioning still runs there — it is idempotent and cheap —
//     but its outcome is not this deployment's health, and `local` is the escape
//     hatch the publish gate itself points at.
func (s *ncAccessSubstrateStatus) markApplicable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = true
}

// unavailable records that a named prerequisite is absent: an app that is not
// enabled, an administrator that could not be resolved. The step is the thing to
// act on.
func (s *ncAccessSubstrateStatus) unavailable(step string, cause error) {
	s.record(ncSubstrateUnavailable, step, cause)
}

// degraded records that a provisioning call failed. The topology is not proven,
// and unlike unavailable there is nothing specific to install.
func (s *ncAccessSubstrateStatus) degraded(step string, cause error) {
	s.record(ncSubstrateDegraded, step, cause)
}

// record stores an outcome in the same words the log carries, so an admin
// reading /status and an admin reading the log are looking at the same sentence.
func (s *ncAccessSubstrateStatus) record(state ncSubstrateState, step string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	s.step = step
	if cause != nil {
		s.detail = fmt.Sprintf("%s: %v", step, cause)
	} else {
		s.detail = step
	}
	s.checkedAtUTC = nowUTCString()
}

// succeed records a provisioning run that got all the way through.
// succeed records that provisioning ran to completion.
//
// A degradation recorded EARLIER in the same run survives it. Some steps are
// deliberately non-fatal — the ACL-manager delegation, the legacy deny floor —
// and provisioning continues past them; before this, the final succeed() wiped
// the record, so the acl_manager degradation whose own comment says it exists
// "so a half-provisioned folder is never reported as provisioned" did exactly
// that. Reaching the end means every step that must work did; it does not mean
// nothing was wrong.
func (s *ncAccessSubstrateStatus) succeed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkedAtUTC = nowUTCString()
	if s.state == ncSubstrateDegraded || s.state == ncSubstrateUnavailable {
		return
	}
	s.state = ncSubstrateProvisioned
	s.step = ""
	s.detail = ""
}

// setAdminUser records the administrator provisioning resolved. Deliberately
// does not touch the state: it is context for whatever outcome follows.
func (s *ncAccessSubstrateStatus) setAdminUser(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adminUser = user
}

// setPrerequisites records the native-app preflight result, whichever way it
// went — a caller reading /status after a failure needs to see which app was
// missing, not just that one was.
func (s *ncAccessSubstrateStatus) setPrerequisites(prereqs []ncPrerequisiteStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prereqs = append(s.prereqs[:0:0], prereqs...)
}

// usable reports whether the substrate is proven enough to write recordings
// into. The publish sink asks this because WebDAV cannot: MKCOL into the
// service account's own home returns the same 201 as MKCOL into a mounted group
// folder (D-585).
func (s *ncAccessSubstrateStatus) usable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == ncSubstrateProvisioned
}

// reset returns the record to its zero state. Only tests need it; provisioning
// itself is idempotent and simply overwrites. Fields are cleared individually
// rather than by assigning a zero struct, which would zero the mutex it holds.
func (s *ncAccessSubstrateStatus) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = false
	s.state = ""
	s.step = ""
	s.detail = ""
	s.adminUser = ""
	s.prereqs = nil
	s.checkedAtUTC = ""
}

// snapshot renders the record for /status.
func (s *ncAccessSubstrateStatus) snapshot(publishSink string) statusRecordingsAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := statusRecordingsAccess{
		Applicable:  s.applicable,
		PublishSink: publishSink,
		State:       string(s.state),
		Step:        s.step,
		Detail:      s.detail,
		AdminUser:   s.adminUser,
		CheckedAt:   s.checkedAtUTC,
	}
	for _, p := range s.prereqs {
		out.Prerequisites = append(out.Prerequisites, statusPrerequisite(p))
	}
	if !s.applicable {
		// A standalone operator — or an ExApp pinned to the local sink — is not
		// broken for want of a Nextcloud substrate it never uses. Say so
		// explicitly rather than reporting a bare false, which reads as failure.
		out.State = string(ncSubstrateNotApplicable)
		out.OK = true
		out.Detail = "recordings are not served from Nextcloud Files; no substrate is expected"
		out.Step = ""
		return out
	}
	if s.checkedAtUTC == "" {
		// Applicable, but provisioning has not run in this process. Reachable
		// after a bare container restart: the enabled edge fires on enable, not
		// on start (D-541). Reported rather than hidden, because the alternative
		// is an ExApp that cannot serve recordings and says it is fine.
		out.State = string(ncSubstrateUnknown)
		out.Detail = "provisioning has not run yet (the app has not been enabled since this operator started)"
		return out
	}
	out.OK = s.state == ncSubstrateProvisioned
	return out
}
