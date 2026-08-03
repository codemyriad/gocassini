package operator

import (
	"fmt"
	"sync"
)

// Provisioning health, made observable (D-554 outcome 3, D-545 AC-7).
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
type ncAccessSubstrateStatus struct {
	mu sync.Mutex
	// applicable is false for a standalone operator: there is no Nextcloud it
	// was ever pointed at, so there is no substrate to be missing.
	applicable bool
	ok         bool
	detail     string
	// checkedAtUTC is when provisioning last ran to completion or gave up.
	// Empty means it has not run yet — the operator is up but has not seen an
	// enabled edge.
	checkedAtUTC string
}

// ncAccessSubstrate is the process-wide record. It is a package-level singleton
// for the same reason provisionMu and resolvedProvisioningUser are: provisioning
// is driven by the AppAPI enabled callback, which has no Runtime in scope.
var ncAccessSubstrate ncAccessSubstrateStatus

// markApplicable records that this deployment is expected to have a substrate.
// Called before the first step runs, so a provisioning attempt that dies
// part-way still reports as applicable rather than looking like a standalone.
func (s *ncAccessSubstrateStatus) markApplicable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = true
}

// fail records the step that stopped provisioning, in the same words the log
// carries, so an admin reading /status and an admin reading the log are looking
// at the same sentence.
func (s *ncAccessSubstrateStatus) fail(step string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = true
	s.ok = false
	if cause != nil {
		s.detail = fmt.Sprintf("%s: %v", step, cause)
	} else {
		s.detail = step
	}
	s.checkedAtUTC = nowUTCString()
}

// succeed records a provisioning run that got all the way through.
func (s *ncAccessSubstrateStatus) succeed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = true
	s.ok = true
	s.detail = ""
	s.checkedAtUTC = nowUTCString()
}

// reset returns the record to its zero state. Only tests need it; provisioning
// itself is idempotent and simply overwrites. Fields are cleared individually
// rather than by assigning a zero struct, which would zero the mutex it holds.
func (s *ncAccessSubstrateStatus) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = false
	s.ok = false
	s.detail = ""
	s.checkedAtUTC = ""
}

// snapshot renders the record for /status.
func (s *ncAccessSubstrateStatus) snapshot(publishSink string) statusRecordingsAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := statusRecordingsAccess{
		Applicable:  s.applicable,
		PublishSink: publishSink,
		OK:          s.ok,
		Detail:      s.detail,
		CheckedAt:   s.checkedAtUTC,
	}
	if !s.applicable {
		// A standalone operator is not broken for want of a Nextcloud it was
		// never given. Say so explicitly rather than reporting a bare false,
		// which reads like a failure.
		out.OK = true
		out.Detail = "not an installed ExApp; recordings are not served from Nextcloud Files"
		return out
	}
	if s.checkedAtUTC == "" {
		out.Detail = "provisioning has not run yet (the app has not been enabled since this operator started)"
	}
	return out
}
