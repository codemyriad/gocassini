package operator

import (
	"sync"
	"time"
)

type ncSubstrateState string

const (
	ncSubstrateProvisioned   ncSubstrateState = "provisioned"
	ncSubstrateDegraded      ncSubstrateState = "degraded"
	ncSubstrateUnavailable   ncSubstrateState = "unavailable"
	ncSubstrateNotApplicable ncSubstrateState = "not_applicable"
	ncSubstrateUnknown       ncSubstrateState = "unknown"
)

type ncAccessSubstrateStatus struct {
	mu           sync.Mutex
	applicable   bool
	state        ncSubstrateState
	step         string
	detail       string
	adminUser    string
	probe        ncStorageProbe
	hasProbe     bool
	checkedAtUTC string
	shareWarning string
}

var ncAccessSubstrate ncAccessSubstrateStatus

func (s *ncAccessSubstrateStatus) markApplicable() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = true
}

func (s *ncAccessSubstrateStatus) beginRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Keep the last complete result while a new probe runs. A concurrent
	// publication must not fail solely because an admin opened readiness.
	if s.state == "" {
		s.state = ncSubstrateUnknown
	}
}

func (s *ncAccessSubstrateStatus) record(state ncSubstrateState, step string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == ncSubstrateUnavailable && state == ncSubstrateDegraded {
		return
	}
	s.state = state
	s.step = step
	if cause != nil {
		s.detail = cause.Error()
	}
	s.checkedAtUTC = time.Now().UTC().Format(time.RFC3339)
}

func (s *ncAccessSubstrateStatus) unavailable(step string, cause error) {
	s.record(ncSubstrateUnavailable, step, cause)
}

func (s *ncAccessSubstrateStatus) degraded(step string, cause error) {
	s.record(ncSubstrateDegraded, step, cause)
}

func (s *ncAccessSubstrateStatus) succeed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = ncSubstrateProvisioned
	s.step = ""
	s.detail = ""
	s.checkedAtUTC = time.Now().UTC().Format(time.RFC3339)
}

func (s *ncAccessSubstrateStatus) setAdminUser(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adminUser = user
}

func (s *ncAccessSubstrateStatus) setProbe(probe ncStorageProbe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probe = probe
	s.hasProbe = true
}

func (s *ncAccessSubstrateStatus) warnShare(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shareWarning == "" {
		s.shareWarning = message
	} else if len(s.shareWarning)+len(message) < 8192 {
		s.shareWarning += "\n" + message
	}
}

func (s *ncAccessSubstrateStatus) lastProbe() (ncStorageProbe, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probe, s.hasProbe
}

func (s *ncAccessSubstrateStatus) usable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == ncSubstrateProvisioned
}

func (s *ncAccessSubstrateStatus) recordingRefusal() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.applicable || s.state != ncSubstrateUnavailable {
		return ""
	}
	if s.detail != "" {
		return s.detail
	}
	return s.step
}

func (s *ncAccessSubstrateStatus) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applicable = false
	s.state = ""
	s.step = ""
	s.detail = ""
	s.adminUser = ""
	s.probe = ncStorageProbe{}
	s.hasProbe = false
	s.checkedAtUTC = ""
	s.shareWarning = ""
}

func (s *ncAccessSubstrateStatus) snapshot(publishSink string) statusRecordingsAccess {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := statusRecordingsAccess{
		Applicable: s.applicable, PublishSink: publishSink, State: string(s.state),
		Step: s.step, Detail: s.detail, Cause: storageCauseFor(s.step),
		AdminUser: s.adminUser, CheckedAt: s.checkedAtUTC, Warning: s.shareWarning,
		Mode: "direct_shares", Root: ncRecordingsRoot,
	}
	if !s.applicable {
		out.State = string(ncSubstrateNotApplicable)
		out.OK = true
		out.Detail = "recordings are not served from Nextcloud Files"
		out.Step = ""
		out.Cause = ""
		return out
	}
	if s.checkedAtUTC == "" {
		out.State = string(ncSubstrateUnknown)
		out.Detail = "recordings setup has not been checked yet"
		return out
	}
	out.OK = s.state == ncSubstrateProvisioned
	return out
}
