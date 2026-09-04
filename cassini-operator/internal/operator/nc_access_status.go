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
	// mode and modeSource are the resolved storage model and where it came
	// from (D-616). Empty until the preflight has resolved one, which is a
	// different thing from `default` and must stay distinguishable: the UI
	// branches on it, and "nobody has decided yet" is not a decision.
	mode       string
	modeSource string
	// probe is the last read-only look at Nextcloud. It is kept whole rather
	// than reduced to a verdict, because /storage has to answer "is the OTHER
	// mode available" — the mode you are not in, whose readiness no single
	// state field could carry.
	probe    ncStorageProbe
	hasProbe bool
	// checkedAtUTC is when the preflight last ran to completion or gave up.
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

// beginRun clears the previous run's verdict so this run reports its own.
//
// It exists because succeed() deliberately refuses to overwrite a recorded
// degradation — which is right WITHIN one run, where a non-fatal step must
// survive the steps after it, and wrong ACROSS runs, where it made a failure
// permanent for the life of the process. An administrator who installs the
// missing app and re-enables Cassini, or who fixes a mode mismatch from the
// Setup tab, gets a run in which everything works and a status that still says
// what was wrong before it. Publishing and recording stay refused, and the
// documented remedy appears to do nothing.
//
// Applicability, the resolved administrator, the mode and the probe are NOT
// cleared: they are either properties of the deployment or values this run is
// about to overwrite anyway.
func (s *ncAccessSubstrateStatus) beginRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = ""
	s.step = ""
	s.detail = ""
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

// setMode records the resolved storage model. Like setAdminUser it deliberately
// does not touch the state: the mode is context for whatever outcome follows,
// and a mode that is resolved says nothing about whether the storage under it
// is usable.
func (s *ncAccessSubstrateStatus) setMode(mode, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
	s.modeSource = source
}

// setProbe records the last read-only look at Nextcloud.
func (s *ncAccessSubstrateStatus) setProbe(probe ncStorageProbe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probe = probe
	s.hasProbe = true
}

// lastProbe returns the recorded probe, and whether there has been one. The
// /storage endpoint reads it rather than re-probing, for the same reason
// /status does not: every check it promises is cheap, and this one is a
// handful of round-trips to Nextcloud.
func (s *ncAccessSubstrateStatus) lastProbe() (ncStorageProbe, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probe, s.hasProbe
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

// ncStorageServesAsOwner reports whether the read proxy may fetch the archive
// as the owning service account instead of as the caller — which also decides
// WHICH root it reads, because the two are one question (webdav_upload.go).
//
// This is D-668's guard against the default model's read path failing open, and
// the split roots changed what it has to check rather than whether it has to.
//
// The first pass required the recorded mode AND `usable()`, because both models
// addressed `Cassini/Recordings`: a recorded `default` on an instance whose
// `Cassini` Team folder was still mapped would have served every authenticated
// account every recording in that folder, past its per-recording ACLs, as the
// ACL manager. That was reproduced end to end, and it is why the probe's
// agreement was made load-bearing.
//
// That state no longer exists. The default model reads
// `CassiniNoACL/Recordings`, which the `Cassini` Team folder cannot shadow, so a
// mapped Team folder is no longer evidence of anything about the private tree —
// and after an opt-out it is the ORDINARY state, since the emptied folder is
// left in place. What remains is one narrower question: has a Team folder been
// mounted over the default root itself?
//
//	no probe yet     serve as the owner. The claim being made is about a path
//	                 nothing Cassini does could have mounted anything over, so
//	                 an unasked question is not a hazard here the way it was
//	                 when the path was shared. This is what stops a restarted
//	                 container serving an empty archive to everybody until
//	                 somebody re-enables the app (D-669's window, for reads).
//	probe says yes   fail closed. Something IS mounted at CassiniNoACL, so the
//	                 tree is not private and owner-identity reads would hand it
//	                 to whoever that folder is mapped to.
func ncStorageServesAsOwner() bool {
	accessControlled, resolved := ncStorage.mode()
	if !resolved || accessControlled {
		return false
	}
	probe, probed := ncAccessSubstrate.lastProbe()
	return !probed || !probe.DefaultRootShadowed
}

// recordingRefusal reports why a recording must not be started at all, or ""
// when it may go ahead.
//
// It is deliberately NARROWER than usable(). The publish gate refuses anything
// short of `provisioned`, which is right for a write that is about to happen;
// refusing to RECORD is a bigger claim, because the meeting is happening now
// and there is no second chance at it. So only `unavailable` refuses — a named
// prerequisite is absent, no amount of waiting fixes it, and capturing an hour
// of audio that provably cannot be published wastes the call rather than
// saving it.
//
//	unavailable  refuse. Something is missing and the step names it.
//	degraded     record. A call failed; it may not fail again by publish time.
//	unknown      record. The preflight runs on the AppAPI enabled edge, never on
//	             start (D-541), so a container that restarted an hour ago is
//	             here — and it is very probably fine. Refusing every recording
//	             until somebody re-enables the app would turn a reboot into an
//	             outage.
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
	s.mode = ""
	s.modeSource = ""
	s.probe = ncStorageProbe{}
	s.hasProbe = false
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
		Mode:        s.mode,
		ModeSource:  s.modeSource,
		CheckedAt:   s.checkedAtUTC,
	}
	if s.mode != "" {
		out.Root = recordingsRootFor(s.mode == storageModeAccessControlled)
		clean := ncStorage.migrationClean()
		out.MigrationClean = &clean
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
