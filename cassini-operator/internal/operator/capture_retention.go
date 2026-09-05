package operator

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Source-capture storage safety (D-698).
//
// Uploads land under CaptureRoot, which on an AppAPI deploy is the SAME volume
// as the job database, the work root and the published site. Nothing bounded
// them: any account that can create a room could post 512MiB per request until
// the volume was full, and an ordinary pilot filled it slowly just by working.
// A full volume does not merely stop capture — it fails the recording in
// flight, the SQLite job store and publishing, for everybody. That is the only
// way this prototype can cost other people's meetings, so the bounds are
// checked here before anything is written, and the sweep below is what keeps
// the root from growing without end.
//
// The layout, and what the sweep will NEVER remove:
//
//	captureRoot/
//	  upload-XXXXXXX/                  staging. Removed only when it is older
//	                                   than captureStagingGrace: a younger one
//	                                   has a request streaming into it right now.
//	  <room>/<owner>/<callStartMS>/     a promoted capture. NEVER removed while it
//	                                   is younger than the configured age — a
//	                                   rerun of that meeting still discovers it
//	                                   (DiscoverSourceCaptures), and a build that
//	                                   has not run yet is the whole point of
//	                                   collecting the audio.
//	  <room>/<owner>/<callStartMS>.superseded/
//	                                   set aside by promoteCapture while it swaps
//	                                   in a re-upload. Removed only when the live
//	                                   sibling that replaced it exists. A crash
//	                                   between promoteCapture's two renames
//	                                   leaves this holding the ONLY copy of that
//	                                   participant's audio, and removing it then
//	                                   would be the sweep destroying exactly what
//	                                   the careful promotion refused to lose.
//
// Stated plainly, because an age cap IS a decision to lose something: past the
// configured age, a rerun of an old meeting no longer gets that participant's
// source audio. The alternative is the unbounded growth this exists to stop.
const (
	// captureStagingPrefix names a directory an upload is still writing into.
	// os.MkdirTemp appends randomness to it; the sweep matches on the prefix.
	captureStagingPrefix = "upload-"

	// captureSupersededSuffix is the name promoteCapture gives a capture it has
	// set aside. cassini-go-recorder's DiscoverSourceCaptures filters on the
	// same literal (internal/transcribe/sourceaudio.go); the two must move
	// together, which TestSupersededSuffixIsTheNameDiscoveryFiltersOn pins.
	captureSupersededSuffix = ".superseded"

	// captureStagingGrace is how long a staging directory is left alone before
	// the sweep calls it orphaned. A directory's mtime is set when the last
	// file was created in it, so an upload streaming one large segment looks
	// idle for as long as that segment takes; a day of slack means the sweep
	// cannot delete a body still on the wire, and the worst it costs is one
	// abandoned upload's bytes for one more day.
	captureStagingGrace = 24 * time.Hour
)

const (
	envCaptureMinFreeDiskMB = "CASSINI_CAPTURE_MIN_FREE_DISK_MB"
	envCaptureOwnerQuotaMB  = "CASSINI_CAPTURE_OWNER_QUOTA_MB"
	envCaptureTotalQuotaMB  = "CASSINI_CAPTURE_TOTAL_QUOTA_MB"
	envCaptureMaxAgeHours   = "CASSINI_CAPTURE_MAX_AGE_HOURS"
)

const (
	// defaultCaptureMinFreeDiskMB sits above the recorder's own hard floor —
	// cassini-go-recorder's doctor fails a working directory under 2GiB — and
	// above it by more than one upload's 512MiB ceiling. Capture therefore
	// stops accepting while a recording can still start: a participant loses
	// their own uploaded copy, which is a spare, rather than everyone losing
	// the meeting.
	defaultCaptureMinFreeDiskMB = 4096
	// defaultCaptureOwnerQuotaMB bounds one account. An hour of 128 kbit/s mono
	// Opus is ~57MB, so this is roughly 35 hours of one participant's captures
	// before their oldest have to age out.
	defaultCaptureOwnerQuotaMB = 2048
	// defaultCaptureTotalQuotaMB bounds the root as a whole, so a pilot with
	// more participants than anyone counted still cannot reach the free-space
	// floor.
	defaultCaptureTotalQuotaMB = 20480
	// defaultCaptureMaxAgeHours is fourteen days: long enough that a meeting
	// noticed late can still be rerun with its source audio, short enough that
	// an abandoned upload is not kept for a year.
	defaultCaptureMaxAgeHours = 24 * 14
)

// captureLimits is the resolved storage policy. Zero means unlimited in every
// field — the deliberate escape hatch, the same shape as artifactRetentionAll,
// for an installation that bounds the volume some other way.
type captureLimits struct {
	minFreeDisk int64
	ownerQuota  int64
	totalQuota  int64
	maxAge      time.Duration
}

// captureEnvInt reads one knob. Zero is allowed and means "no bound"; a
// negative or unparseable value is an error, because silently substituting a
// default for a quota an administrator thought they had set is the failure this
// whole file exists to prevent.
func captureEnvInt(name string, def int64, unit string) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def, fmt.Errorf("invalid %s=%q: expected a whole number of %s (0 means no limit)", name, raw, unit)
	}
	if n < 0 {
		return def, fmt.Errorf("invalid %s=%q: must not be negative (0 means no limit)", name, raw)
	}
	return n, nil
}

// captureLimitsFromEnv resolves the policy for a running operator.
//
// Parse failures resolve to the default here rather than being reported:
// validateCaptureLimits rejects them at startup, so an invalid value never
// reaches a request, and if one somehow did, the default is the conservative
// answer rather than an unbounded one.
func captureLimitsFromEnv() captureLimits {
	minFreeMB, _ := captureEnvInt(envCaptureMinFreeDiskMB, defaultCaptureMinFreeDiskMB, "megabytes")
	ownerMB, _ := captureEnvInt(envCaptureOwnerQuotaMB, defaultCaptureOwnerQuotaMB, "megabytes")
	totalMB, _ := captureEnvInt(envCaptureTotalQuotaMB, defaultCaptureTotalQuotaMB, "megabytes")
	ageHours, _ := captureEnvInt(envCaptureMaxAgeHours, defaultCaptureMaxAgeHours, "hours")
	return captureLimits{
		minFreeDisk: minFreeMB << 20,
		ownerQuota:  ownerMB << 20,
		totalQuota:  totalMB << 20,
		maxAge:      time.Duration(ageHours) * time.Hour,
	}
}

// validateCaptureLimits reports the first knob an administrator got wrong, so
// startup can refuse. Same reasoning as validateArtifactRetentionName: running
// with a quota nobody asked for is worse than not starting.
func validateCaptureLimits() error {
	for _, knob := range []struct {
		name string
		def  int64
		unit string
	}{
		{envCaptureMinFreeDiskMB, defaultCaptureMinFreeDiskMB, "megabytes"},
		{envCaptureOwnerQuotaMB, defaultCaptureOwnerQuotaMB, "megabytes"},
		{envCaptureTotalQuotaMB, defaultCaptureTotalQuotaMB, "megabytes"},
		{envCaptureMaxAgeHours, defaultCaptureMaxAgeHours, "hours"},
	} {
		if _, err := captureEnvInt(knob.name, knob.def, knob.unit); err != nil {
			return err
		}
	}
	return nil
}

// captureUsage is what the capture root actually holds.
type captureUsage struct {
	// Captures counts promoted capture directories. Set-aside and staging
	// directories are not captures: nothing downstream will ever read them.
	Captures int
	// Bytes is every byte under the root, promoted or transient alike. The
	// global cap is a bound on the VOLUME, so it has to count the shapes a
	// crash can leave behind as well as the ones a build will read.
	Bytes int64
	// ByOwner attributes bytes to the account that uploaded them, which is the
	// directory level the layout is keyed by. Staging carries no owner in its
	// name, so it counts only toward Bytes.
	ByOwner map[string]int64
	// Oldest is the intake time of the oldest stored capture, taken from the
	// directory's mtime — the moment the sidecar was written into staging, and
	// the same instant the sidecar records as ReceivedAt. Reading it from the
	// filesystem avoids parsing every manifest on a walk that runs per upload.
	Oldest time.Time
}

// measureCaptureRoot walks the capture root. It never creates anything: a
// deployment that has never collected has no root, and asking about it must not
// bring one into existence.
//
// Errors are propagated rather than skipped. An under-reported total would let
// an upload past a quota that is actually spent, which is the one outcome this
// measurement exists to prevent.
func measureCaptureRoot(root string) (captureUsage, error) {
	usage := captureUsage{ByOwner: map[string]int64{}}
	if strings.TrimSpace(root) == "" {
		return usage, nil
	}
	rooms, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return usage, nil
		}
		return usage, fmt.Errorf("read capture root: %w", err)
	}
	for _, room := range rooms {
		if !room.IsDir() {
			continue
		}
		roomDir := filepath.Join(root, room.Name())
		if strings.HasPrefix(room.Name(), captureStagingPrefix) {
			bytes, err := captureDirBytes(roomDir)
			if err != nil {
				return usage, err
			}
			usage.Bytes += bytes
			continue
		}
		owners, err := os.ReadDir(roomDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return usage, fmt.Errorf("read capture room %s: %w", roomDir, err)
		}
		for _, owner := range owners {
			if !owner.IsDir() {
				continue
			}
			ownerDir := filepath.Join(roomDir, owner.Name())
			captures, err := os.ReadDir(ownerDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return usage, fmt.Errorf("read capture owner %s: %w", ownerDir, err)
			}
			for _, capture := range captures {
				if !capture.IsDir() {
					continue
				}
				bytes, err := captureDirBytes(filepath.Join(ownerDir, capture.Name()))
				if err != nil {
					return usage, err
				}
				// Bytes is every byte on the volume, set-aside copies included,
				// because the disk does not care why they are there and the
				// bound it feeds is retryable.
				usage.Bytes += bytes
				if strings.HasSuffix(capture.Name(), captureSupersededSuffix) {
					// ByOwner deliberately excludes them. It feeds the per-owner
					// quota, whose refusal is TERMINAL — the client deletes its
					// only copy — and a set-aside directory is this
					// participant's own previous copy of a call they are
					// re-uploading right now, about to be removed by the sweep.
					// Charging it would let their old copy destroy their new
					// one.
					continue
				}
				usage.ByOwner[owner.Name()] += bytes
				if strings.HasSuffix(capture.Name(), captureSupersededSuffix) {
					continue
				}
				usage.Captures++
				info, err := capture.Info()
				if err != nil {
					// A promotion moved it while we walked. Its bytes are
					// already counted; its age is not worth failing over.
					continue
				}
				if usage.Oldest.IsZero() || info.ModTime().Before(usage.Oldest) {
					usage.Oldest = info.ModTime()
				}
			}
		}
	}
	return usage, nil
}

// captureDirBytes sums the regular files under one directory. A directory that
// vanished mid-walk is a concurrent promotion, not a failure.
func captureDirBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return total, fmt.Errorf("measure %s: %w", dir, err)
	}
	return total, nil
}

// probeCaptureFreeBytes is a package var so a test can make the volume look
// full without filling one, the same way resource.go injects its memory probe.
var probeCaptureFreeBytes = captureFreeBytes

// captureFreeBytes reports free space on the volume holding path, walking up to
// the first ancestor that exists: the capture root is created by the first
// upload, and both the floor and /status have to answer before that.
func captureFreeBytes(path string) (int64, error) {
	dir := filepath.Clean(path)
	for {
		var stat syscall.Statfs_t
		err := syscall.Statfs(dir, &stat)
		if err == nil {
			return int64(stat.Bavail) * int64(stat.Bsize), nil
		}
		parent := filepath.Dir(dir)
		if !os.IsNotExist(err) || parent == dir {
			return 0, fmt.Errorf("free space for %s: %w", dir, err)
		}
		dir = parent
	}
}

// captureRefusal is a storage refusal and the status the client must see.
//
// The status codes here are load-bearing and getting them the wrong way round
// destroys recordings. The client (cassini-app/src/capture/payload.ts) treats
// 413 as TERMINAL: it deletes the buffered recording from OPFS and never offers
// it again. Anything else it treats as a failure of THIS delivery — the buffer
// stays and the next Talk page load retries, bounded by its own five-attempt
// cap so a permanently-refusing deployment cannot turn every participant into a
// forever re-uploader. So:
//
//   - 413 for the per-owner quota. It is this account's own allowance, spent by
//     this account's own captures. An identical retry cannot change the answer,
//     and keeping the buffer would re-offer a meeting-sized body five more
//     times for nothing.
//   - 507 for the free-space floor and the global cap. Neither is about this
//     capture: the rest of the volume, or somebody else's uploads, filled it.
//     The sweep, a publish, or an administrator frees it, and until then the
//     participant's recording is still safe in OPFS. Answering 413 here would
//     delete one person's audio because a different meeting was large.
type captureRefusal struct {
	status  int
	reason  string
	message string
}

func (r *captureRefusal) Error() string { return r.message }

// captureAdmission is the verdict on one upload plus the byte budget it may
// still spend, so the streaming half of the check needs no second walk.
type captureAdmission struct {
	ownerRemaining int64
	totalRemaining int64
	// reserved is what this upload promised to the shared bounds, and owner is
	// who it was promised for. releaseCaptureAdmission gives them back.
	reserved int64
	owner    string
}

// remaining is how many bytes this upload may still write. Capped at the
// per-request ceiling, which MaxBytesReader enforces anyway: an unlimited quota
// must not produce a budget that overflows when the caller adds one to it.
func (a captureAdmission) remaining() int64 {
	limit := a.ownerRemaining
	if a.totalRemaining < limit {
		limit = a.totalRemaining
	}
	if limit > captureMaxUploadBytes {
		limit = captureMaxUploadBytes
	}
	// Never more than was reserved. The reservation is what every concurrent
	// caller was told to expect, so a request allowed to stream past it would
	// spend an allowance somebody else has already been promised — and the
	// bounds would stop being bounds, which is the whole point of taking one.
	if a.reserved > 0 && limit > a.reserved {
		limit = a.reserved
	}
	return limit
}

// overrun names which bound a body that outgrew its budget actually hit. The
// owner's own quota is terminal for the client; the global one is not.
//
// A tie goes to the global cap, and so to the retryable answer. Two bounds that
// ran out on the same byte do not tell us the account overspent, and the wrong
// guess here deletes a recording.
func (a captureAdmission) overrun() *captureRefusal {
	if a.ownerRemaining < a.totalRemaining {
		return &captureRefusal{
			status:  http.StatusRequestEntityTooLarge,
			reason:  "owner_quota",
			message: "this account's capture storage quota is spent",
		}
	}
	return &captureRefusal{
		status:  http.StatusInsufficientStorage,
		reason:  "total_quota",
		message: "capture storage is full on this installation; try again later",
	}
}

// overrunIfSpent reports the refusal for a budget the charge above exhausted,
// and nothing while it still has room.
func (a captureAdmission) overrunIfSpent() *captureRefusal {
	if a.ownerRemaining >= 0 && a.totalRemaining >= 0 {
		return nil
	}
	return a.overrun()
}

// credit gives back allowance for bytes that this upload will itself displace.
// It never raises the reservation, which bounds the disk rather than the quota.
func (a captureAdmission) credit(n int64) captureAdmission {
	if n <= 0 {
		return a
	}
	a.ownerRemaining += n
	a.totalRemaining += n
	return a
}

func (a captureAdmission) consume(n int64) captureAdmission {
	a.ownerRemaining -= n
	a.totalRemaining -= n
	return a
}

// admitCaptureUpload decides, before a byte is written, whether this upload may
// be stored at all.
//
// declared is the request's Content-Length. It is believed only as far as it
// makes the answer stricter: a body that declares nothing (chunked) is charged
// the full per-request ceiling against the disk floor, and every quota is
// re-checked against the bytes that actually arrive, because a Content-Length
// can be absent, wrong, or a lie.
// captureAdmissionMu serializes admission, and captureInFlight tracks what
// admitted uploads have been allowed but not yet written.
//
// Without both, the quotas are not bounds at all: every concurrent upload reads
// the same usage snapshot, every one is told it fits, and the volume they share
// with the job database absorbs the sum. The reservation is released by
// releaseCaptureAdmission once the upload has finished, succeeded or not, at
// which point its bytes are either on disk and measurable or gone.
var (
	captureAdmissionMu sync.Mutex
	// captureInFlightDisk is what admitted uploads may still write, held at the
	// per-request ceiling because a declared length may be wrong. It guards the
	// DISK, whose refusal is retryable.
	//
	// It deliberately does not feed the per-owner quota. That refusal is
	// terminal — the client deletes its only copy — and holding a ceiling per
	// concurrent request would let a participant's own parallel uploads spend
	// their allowance and destroy a recording over a condition that lasts
	// seconds. The quota is therefore measured from disk and is approximate by
	// a bounded amount: at most the concurrent uploads in flight, which the
	// disk floor already limits.
	captureInFlightDisk int64
)

// resetCaptureAdmissions drops every outstanding reservation. It exists for
// tests, which call admitCaptureUpload directly and so never reach the
// handler's release; without it one test's reservation refuses the next one's
// upload and the failure lands somewhere unrelated.
func resetCaptureAdmissions() {
	captureAdmissionMu.Lock()
	defer captureAdmissionMu.Unlock()
	captureInFlightDisk = 0
}

// releaseCaptureAdmission gives back what an upload reserved and did not use.
func releaseCaptureAdmission(owner string, reserved int64) {
	if reserved <= 0 {
		return
	}
	captureAdmissionMu.Lock()
	defer captureAdmissionMu.Unlock()
	captureInFlightDisk -= reserved
	if captureInFlightDisk < 0 {
		captureInFlightDisk = 0
	}
}

func admitCaptureUpload(root, owner string, declared int64, limits captureLimits) (captureAdmission, *captureRefusal) {
	captureAdmissionMu.Lock()
	defer captureAdmissionMu.Unlock()

	// An undeclared length is charged the ceiling against the DISK, because a
	// volume cannot be asked to give the space back afterwards. It is not
	// charged against the owner's quota, because that refusal is terminal: the
	// client deletes its only copy, and destroying a recording that would have
	// fitted because its length was not declared in advance is exactly the
	// wrong direction. The streaming check below catches a real overrun with
	// the bytes in hand.
	need := declared
	if need <= 0 {
		need = captureMaxUploadBytes
	}
	if limits.minFreeDisk > 0 {
		free, err := probeCaptureFreeBytes(root)
		if err != nil {
			// Refusing without a reading is the safe direction, and it is
			// retryable: a volume we cannot measure is one we must not fill.
			return captureAdmission{}, &captureRefusal{
				status:  http.StatusInsufficientStorage,
				reason:  "free_space_unknown",
				message: "capture storage cannot be measured right now; try again later",
			}
		}
		if free-need-captureInFlightDisk < limits.minFreeDisk {
			return captureAdmission{}, &captureRefusal{
				status:  http.StatusInsufficientStorage,
				reason:  "disk_floor",
				message: "not enough free space to accept a capture; try again later",
			}
		}
	}

	admission := captureAdmission{
		ownerRemaining: captureMaxUploadBytes,
		totalRemaining: captureMaxUploadBytes,
	}
	if limits.ownerQuota <= 0 && limits.totalQuota <= 0 {
		// No quotas, but the free-space floor above still needs the
		// reservation: without it concurrent uploads read the same reading of
		// the disk and each believe they fit.
		captureInFlightDisk += captureMaxUploadBytes
		admission.reserved = captureMaxUploadBytes
		admission.owner = owner
		return admission, nil
	}
	usage, err := measureCaptureRoot(root)
	if err != nil {
		return captureAdmission{}, &captureRefusal{
			status:  http.StatusInsufficientStorage,
			reason:  "usage_unknown",
			message: "capture storage cannot be measured right now; try again later",
		}
	}
	if limits.ownerQuota > 0 {
		admission.ownerRemaining = limits.ownerQuota - usage.ByOwner[owner]
	}
	if limits.totalQuota > 0 {
		admission.totalRemaining = limits.totalQuota - usage.Bytes
	}
	// Charge the declared length before anything is written. The streaming
	// check catches a body that lied; this catches the ordinary case one
	// request earlier, without moving half a gigabyte first.
	//
	// Only a DECLARED length is charged here. An undeclared one has already
	// been charged against the disk above, where the reservation is what
	// matters, but charging it against a quota would refuse a small chunked
	// upload as if it were the ceiling — and that refusal is terminal, so the
	// client would delete a recording that fitted.
	if declared > 0 {
		if refusal := admission.consume(declared).overrunIfSpent(); refusal != nil {
			return captureAdmission{}, refusal
		}
	}
	// Reserved at the CEILING, deliberately, even when a smaller length was
	// declared. The declaration is the client's claim and the contract above
	// says it may be wrong; a reservation that trusted it would let several
	// requests each declare a byte and then stream half a gigabyte. What is
	// charged against the quota is still the honest declared figure — only what
	// is HELD against concurrent callers is the worst case.
	captureInFlightDisk += captureMaxUploadBytes
	admission.reserved = captureMaxUploadBytes
	admission.owner = owner
	return admission, nil
}

// sweepCaptureRoot removes what the root should no longer hold and returns what
// it removed, so the caller can log it: a sweep that removes silently reads, in
// an incident, exactly like one that lost data.
//
// Every removal is guarded on the thing that makes it redundant — a set-aside
// directory on the live capture that replaced it, a staging directory on being
// too old to have a request in it. Nothing here removes the last copy of
// anything. See the header comment for what it will never touch.
func sweepCaptureRoot(root string, maxAge time.Duration, now time.Time) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	var removed []string
	// Removals take the same lock promotion does, and re-check the age with it
	// held.
	//
	// Everything below decides what to delete from a directory listing taken
	// earlier, and a re-upload can promote a fresh capture into one of those
	// paths in between. Without this the sweep deletes the replacement — after
	// the browser has already discarded its only copy on the 202 — and the
	// recording is gone for good. Re-reading the age under the lock is what
	// makes the decision and the deletion refer to the same directory.
	remove := func(path, why string, age time.Duration) error {
		capturePromotionMu.Lock()
		defer capturePromotionMu.Unlock()
		if age > 0 {
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return fmt.Errorf("sweep %s: %w", path, err)
			}
			if now.Sub(info.ModTime()) < age {
				// Replaced while we were deciding. Leave it; the next sweep
				// will judge it on its own age.
				return nil
			}
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("sweep %s: %w", path, err)
		}
		removed = append(removed, fmt.Sprintf("%s (%s)", path, why))
		return nil
	}
	olderThan := func(entry os.DirEntry, age time.Duration) bool {
		info, err := entry.Info()
		if err != nil {
			// Unknown age is treated as young. A directory we cannot stat is
			// the last one to guess about.
			return false
		}
		return now.Sub(info.ModTime()) > age
	}

	rooms, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return removed, fmt.Errorf("read capture root: %w", err)
	}
	for _, room := range rooms {
		// Anything that is not a directory at this level was not put here by
		// the upload handler. Left alone.
		if !room.IsDir() {
			continue
		}
		roomDir := filepath.Join(root, room.Name())
		if strings.HasPrefix(room.Name(), captureStagingPrefix) {
			if !olderThan(room, captureStagingGrace) {
				continue
			}
			if err := remove(roomDir, "orphaned upload staging", captureStagingGrace); err != nil {
				return removed, err
			}
			continue
		}
		owners, err := os.ReadDir(roomDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("read capture room %s: %w", roomDir, err)
		}
		for _, owner := range owners {
			if !owner.IsDir() {
				continue
			}
			ownerDir := filepath.Join(roomDir, owner.Name())
			captures, err := os.ReadDir(ownerDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return removed, fmt.Errorf("read capture owner %s: %w", ownerDir, err)
			}
			for _, capture := range captures {
				if !capture.IsDir() {
					continue
				}
				dir := filepath.Join(ownerDir, capture.Name())
				if strings.HasSuffix(capture.Name(), captureSupersededSuffix) {
					live := strings.TrimSuffix(dir, captureSupersededSuffix)
					if _, err := os.Stat(live); err != nil {
						// The promotion that made this never finished, so this
						// is the only copy of somebody's audio. Keeping it
						// costs disk; removing it costs a recording.
						continue
					}
					if err := remove(dir, "superseded by a completed re-upload", 0); err != nil {
						return removed, err
					}
					continue
				}
				if maxAge <= 0 || !olderThan(capture, maxAge) {
					continue
				}
				if err := remove(dir, fmt.Sprintf("older than %s", maxAge), maxAge); err != nil {
					return removed, err
				}
			}
			// Empty room/owner directories left by the removals above are
			// noise, not risk: os.Remove refuses a directory that still holds
			// anything, and the age guard keeps it away from one an upload is
			// promoting into right now.
			if olderThan(owner, captureStagingGrace) {
				_ = os.Remove(ownerDir)
			}
		}
		if olderThan(room, captureStagingGrace) {
			_ = os.Remove(roomDir)
		}
	}
	return removed, nil
}

// sweepCaptureStorage applies the policy across the whole capture root and logs
// every removal. It never fails the caller: like artifact retention this is
// housekeeping, and a meeting that published correctly must not be reported as
// failed because a directory could not be removed.
//
// It runs whether or not collection is enabled. An administrator who switched
// capture off wants what it already collected to drain, not to sit there until
// they turn the feature back on.
func (rt *Runtime) sweepCaptureStorage() {
	root := strings.TrimSpace(rt.cfg.CaptureRoot)
	if root == "" {
		return
	}
	removed, err := sweepCaptureRoot(root, captureLimitsFromEnv().maxAge, time.Now())
	for _, path := range removed {
		rt.logger.Printf("capture retention removed %s", path)
	}
	if err != nil {
		rt.logger.Printf("capture retention failed root=%s: %v", root, err)
	}
}

// captureUsageProbe coalesces concurrent walks of the capture root and caches
// the result briefly — the same singleflight-plus-TTL shape status.go uses for
// its nvidia-smi probe, and for the same reason: /status is polled, and a
// filesystem walk per poll is a cost an admin dashboard should not impose.
type captureUsageProbe struct {
	ttl time.Duration
	run func() (captureUsage, error)

	mu       sync.Mutex
	inflight chan struct{}
	at       time.Time
	usage    captureUsage
	err      error
}

func newCaptureUsageProbe(ttl time.Duration, run func() (captureUsage, error)) *captureUsageProbe {
	return &captureUsageProbe{ttl: ttl, run: run}
}

func (p *captureUsageProbe) check() (captureUsage, error) {
	p.mu.Lock()
	for {
		if !p.at.IsZero() && time.Since(p.at) < p.ttl {
			usage, err := p.usage, p.err
			p.mu.Unlock()
			return usage, err
		}
		if p.inflight == nil {
			break
		}
		wait := p.inflight
		p.mu.Unlock()
		<-wait
		p.mu.Lock()
	}
	done := make(chan struct{})
	p.inflight = done
	p.mu.Unlock()

	usage, err := p.run()

	p.mu.Lock()
	p.usage = usage
	p.err = err
	p.at = time.Now()
	p.inflight = nil
	close(done)
	p.mu.Unlock()
	return usage, err
}
