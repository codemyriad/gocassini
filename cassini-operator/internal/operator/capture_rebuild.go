package operator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Rebuilding a meeting when a participant's capture arrives after its build
// (D-698). capture_rebuild_store.go holds the persistence; this is the policy.

const (
	// maxSourceAudioRebuilds bounds how many times ONE meeting may be
	// transcribed again on the back of a late upload.
	//
	// Three, because the cases worth covering are small and countable: one
	// slow participant, then a second slow participant a minute later, then a
	// retry. Beyond that something is wrong that another transcription will not
	// fix, and each rebuild is a full GPU transcription of an entire meeting on
	// a box that also has to record the next one. Administrator reruns are not
	// counted here; only rebuilds this mechanism scheduled.
	maxSourceAudioRebuilds = 3

	// sourceAudioRebuildQuietPeriod is how long the newest upload must have
	// been settled before a rebuild is scheduled.
	//
	// Uploads for one call arrive in a wave: every participant's browser starts
	// posting on the same "recording stopped" signal, and they finish within a
	// minute or two of each other on a comparable link. Rebuilding on the first
	// one would transcribe the meeting again for each of the others in turn.
	// Waiting out a quiet period turns the wave into one rebuild, and costs a
	// minute on a path that has already lost the race by definition.
	sourceAudioRebuildQuietPeriod = 60 * time.Second

	// sourceAudioNoteAttempts and sourceAudioNoteBackoff bound the retry ladder
	// around recording an arrival. See noteCaptureArrival.
	sourceAudioNoteAttempts = 5
)

// sourceAudioNoteBackoff is the first retry gap; each attempt doubles it. A var
// so a test can exercise the ladder without sleeping through it.
var sourceAudioNoteBackoff = 100 * time.Millisecond

// sourceAudioRebuildQuietPeriod is the configured quiet period, or the package
// default when nothing set one.
func (rt *Runtime) sourceAudioRebuildQuietPeriod() time.Duration {
	if quiet := time.Duration(rt.sourceAudioRebuildQuiet.Load()); quiet > 0 {
		return quiet
	}
	return sourceAudioRebuildQuietPeriod
}

// sourceCaptureSet is what is on disk for one recording: how many captures, who
// owns them, and a digest over everything a build would read.
type sourceCaptureSet struct {
	Count  int
	Owners []string
	// Digest covers the audio bytes and placement metadata, not delivery time.
	Digest string
}

// scannedCapture is one on-disk capture that got past the room and window
// filters, with what those filters learned about it.
type scannedCapture struct {
	dir      string
	owner    string
	startMS  int64
	endMS    int64
	fit      int
	segments []captureSegment
}

// selectOwnerScannedCaptures is selectOwnerCaptures from the recorder's
// internal/transcribe/sourceaudio.go, applied to what this scan found: the
// captures that genuinely cover this recording win over the ones that only
// reach it through the slack, and a participant whose captures still cannot be
// told apart contributes none rather than the wrong one.
func selectOwnerScannedCaptures(candidates []scannedCapture) []scannedCapture {
	best := captureWindowApart
	for _, candidate := range candidates {
		if candidate.fit > best {
			best = candidate.fit
		}
	}
	if best == captureWindowApart {
		return nil
	}
	kept := make([]scannedCapture, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.fit == best {
			kept = append(kept, candidate)
		}
	}
	if best == captureWindowWithinSlack && len(kept) > 1 {
		return nil
	}
	for i := 0; i < len(kept); i++ {
		for j := i + 1; j < len(kept); j++ {
			if captureWindowOverlapMS(kept[i].startMS, kept[i].endMS, kept[j].startMS, kept[j].endMS) > captureSessionOverlapSlackMS {
				return nil
			}
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].dir < kept[j].dir })
	return kept
}

// sortedKeys keeps the scan's output independent of map iteration order: the
// digest is compared against a previous scan's, so it has to be a function of
// what is on disk and nothing else.
func sortedKeys(byOwner map[string][]scannedCapture) []string {
	keys := make([]string, 0, len(byOwner))
	for key := range byOwner {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// scanSourceCapturesForRecording reports what a build for this recording would
// find, without running one.
//
// It is DiscoverSourceCaptures' selection rule — room, then the graded call
// window, then per-participant selection, skipping the `.superseded` set-aside
// — reimplemented because the operator and the recorder are separate Go
// modules. It reads sidecars and stats files; it never writes.
//
// A capture that cannot be read is skipped rather than failing the scan, for
// the same reason the build skips it: a malformed upload must not stop a
// meeting being transcribed. A directory that cannot be LISTED is an error,
// because "I could not look" and "there is nothing there" lead to opposite
// decisions here — the first must not settle a debt.
func scanSourceCapturesForRecording(root, roomToken string, window captureRecordingWindow, recordingIDs ...string) (sourceCaptureSet, error) {
	set := sourceCaptureSet{}
	root = strings.TrimSpace(root)
	token := strings.TrimSpace(roomToken)
	if root == "" || token == "" || window.StartMS <= 0 {
		return set, nil
	}
	// window.EndMS is already resolved by recordingSpanForCapture, so this and
	// the resolver ask the identical question of the identical span. Deriving a
	// second end here is how the two disagreed about a recording with no finish
	// time: the resolver attributed the upload and this found no capture for it,
	// which settled the debt and dropped the audio.
	roomDir := filepath.Join(root, token)
	owners, err := os.ReadDir(roomDir)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return set, fmt.Errorf("read capture room %s: %w", roomDir, err)
	}
	var lines []string
	seenOwners := map[string]struct{}{}
	candidates := map[string][]scannedCapture{}
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		ownerDir := filepath.Join(roomDir, owner.Name())
		calls, err := os.ReadDir(ownerDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return set, fmt.Errorf("read capture owner %s: %w", ownerDir, err)
		}
		for _, call := range calls {
			if !call.IsDir() {
				continue
			}
			// The set-aside copy of a capture being replaced sits at this same
			// depth. The build ignores it by name and so must this: counting it
			// would make a promotion look like new audio on every scan.
			if strings.HasSuffix(call.Name(), captureSupersededSuffix) {
				continue
			}
			if strings.HasPrefix(call.Name(), captureStagingPrefix) {
				continue
			}
			dir := filepath.Join(ownerDir, call.Name())
			raw, err := os.ReadFile(filepath.Join(dir, captureSidecarName))
			if err != nil {
				continue
			}
			var sidecar captureSidecar
			if err := json.Unmarshal(raw, &sidecar); err != nil {
				continue
			}
			if sidecar.Format != captureSourceFormat || sidecar.ClockStatus == "unreliable" {
				continue
			}
			if strings.TrimSpace(sidecar.OwnerUserID) == "" || sidecar.RoomToken != token {
				continue
			}
			fit := captureWindowFit(sidecar.CallStartWallMS, sidecar.CallEndWallMS, window.StartMS, window.EndMS)
			if sidecar.RecordingID != "" {
				if len(recordingIDs) == 0 || sidecar.RecordingID != recordingIDs[0] {
					continue
				}
				fit = captureWindowIntersects
			}
			if fit == captureWindowApart {
				continue
			}
			candidates[sidecar.OwnerUserID] = append(candidates[sidecar.OwnerUserID], scannedCapture{
				dir:      dir,
				owner:    sidecar.OwnerUserID,
				startMS:  sidecar.CallStartWallMS,
				endMS:    sidecar.CallEndWallMS,
				fit:      fit,
				segments: sidecar.Segments,
			})
		}
	}
	// Which of a participant's captures this recording gets, decided the way
	// the build decides it. This scan is what says whether a rebuild would find
	// anything new, so counting a capture the build will refuse would promise a
	// rebuild that changes nothing — and missing one it will splice would
	// settle a debt the audio is still owed.
	for _, owner := range sortedKeys(candidates) {
		for _, capture := range selectOwnerScannedCaptures(candidates[owner]) {
			set.Count++
			if _, seen := seenOwners[capture.owner]; !seen {
				seenOwners[capture.owner] = struct{}{}
				set.Owners = append(set.Owners, capture.owner)
			}
			for _, segment := range capture.segments {
				digest, err := captureSegmentDigest(capture.dir, segment)
				if err != nil {
					return set, err
				}
				lines = append(lines, fmt.Sprintf("%s\t%d\t%d\t%s",
					capture.owner, capture.startMS, capture.endMS, digest))
			}
		}
	}
	sort.Strings(lines)
	sort.Strings(set.Owners)
	if set.Count > 0 {
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		set.Digest = hex.EncodeToString(sum[:])
	}
	return set, nil
}

func captureSegmentDigest(dir string, segment captureSegment) (string, error) {
	if !captureSafeName.MatchString(segment.AudioName) || filepath.Base(segment.AudioName) != segment.AudioName {
		return "", fmt.Errorf("invalid segment name")
	}
	h := sha256.New()
	if err := json.NewEncoder(h).Encode(segment); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(dir, segment.AudioName))
	if errors.Is(err, os.ErrNotExist) {
		// A missing segment is an explicit different input, as in the decoder.
		_, _ = io.WriteString(h, "missing")
	} else if err != nil {
		return "", err
	} else {
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sourceCaptureSetForJob is the same scan, keyed by a job rather than a window.
// Used both when a build claims its work and when a rebuild is considered, so
// the two can be compared.
func (rt *Runtime) sourceCaptureSetForJob(ctx context.Context, jobID string) (sourceCaptureSet, error) {
	binding, ok := rt.talkBindingForJob(jobID)
	if !ok || strings.TrimSpace(binding.RoomToken) == "" {
		return sourceCaptureSet{}, nil
	}
	window, err := rt.store.RecordingWindowForJob(ctx, jobID)
	if err != nil {
		return sourceCaptureSet{}, err
	}
	return scanSourceCapturesForRecording(rt.cfg.CaptureRoot, binding.RoomToken, window, jobID)
}

// noteCaptureArrival attributes one accepted upload to its recording and
// records that it arrived.
//
// Called only for an upload that actually changed the bytes on disk. An upload
// the server answered "already stored" adds nothing a rebuild could read, and
// counting it would schedule a full re-transcription of a meeting to produce
// the identical transcript.
//
// The write is retried rather than dropped. A transient database error here
// used to cost the whole feature for that participant — the audio sits on disk,
// nothing knows it is owed, and the only recovery is an administrator noticing
// and rerunning by hand. The ladder is short and bounded because this runs
// inside the upload request; the client is not waiting on the result of it in
// the happy path, where there are no retries at all.
//
// It runs after the bytes are safely promoted and does not affect the response.
// The capture is stored either way, and refusing an upload that has already
// succeeded because a counter could not be bumped would be the worse trade.
func (rt *Runtime) noteCaptureArrival(sidecar *captureSidecar, owner string, logger *log.Logger) {
	if rt == nil || rt.store == nil || sidecar == nil {
		return
	}
	// Detached from the request: a client that hangs up the moment it gets its
	// 202 must not cancel the write that makes its upload useful.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(rt.ctx), 30*time.Second)
	defer cancel()
	if sidecar.ReceiptID != "" {
		known, err := rt.store.captureReceiptKnown(ctx, sidecar.ReceiptID)
		if err != nil {
			logger.Printf("capture receipt lookup: %v", err)
			return
		}
		if known {
			return
		}
	}

	var (
		match    captureJobMatch
		resolved error
	)
	err := retrySourceAudioWrite(ctx, func() error {
		if sidecar.RecordingID != "" {
			room, err := rt.store.captureRecordingRoom(ctx, sidecar.RecordingID)
			if err != nil {
				return err
			}
			if room != sidecar.RoomToken {
				return fmt.Errorf("capture recording room mismatch")
			}
			match.JobID = sidecar.RecordingID
			return nil
		}
		match, resolved = rt.store.ResolveJobForCapture(ctx, sidecar.RoomToken, sidecar.CallStartWallMS, sidecar.CallEndWallMS)
		if errors.Is(resolved, sql.ErrNoRows) || errors.Is(resolved, ErrCaptureJobAmbiguous) {
			// Both are answers, not failures: retrying cannot change either.
			return nil
		}
		return resolved
	})
	switch {
	case err != nil:
		logger.Printf("capture rebuild: could not resolve a recording for room=%s owner=%s after %d attempts: %v; this capture reaches a transcript only if the job is rerun by hand",
			sidecar.RoomToken, owner, sourceAudioNoteAttempts, err)
		return
	case errors.Is(resolved, ErrCaptureJobAmbiguous):
		// Refused rather than guessed. Choosing between two overlapping
		// recordings of one room puts a meeting's speech in another meeting's
		// transcript, which no later correction undoes.
		logger.Printf("capture rebuild: room=%s owner=%s call=%d-%d matches more than one recording, so no rebuild is scheduled: %v",
			sidecar.RoomToken, owner, sidecar.CallStartWallMS, sidecar.CallEndWallMS, resolved)
		return
	case errors.Is(resolved, sql.ErrNoRows):
		// Ordinary: a call this operator never recorded, or one whose recording
		// predates the Talk binding. The capture stays on disk for the sweep.
		logger.Printf("capture rebuild: no recording matches room=%s owner=%s call=%d-%d; the capture is stored and no rebuild is scheduled",
			sidecar.RoomToken, owner, sidecar.CallStartWallMS, sidecar.CallEndWallMS)
		return
	}

	at := nowUTCString()
	if err := retrySourceAudioWrite(ctx, func() error {
		if sidecar.ReceiptID != "" {
			return rt.store.noteCaptureReceipt(ctx, match.JobID, sidecar.ReceiptID, at)
		}
		return rt.store.NoteSourceAudioUpload(ctx, match.JobID, at)
	}); err != nil {
		logger.Printf("capture rebuild: could not record the arrival for job=%s after %d attempts: %v; this capture will only reach a transcript if the job is rerun by hand",
			match.JobID, sourceAudioNoteAttempts, err)
		return
	}
	logger.Printf("capture rebuild: room=%s owner=%s belongs to job=%s (state=%s); it will be rebuilt if that build has already run",
		sidecar.RoomToken, owner, match.JobID, match.State)
	// The dispatcher waits out the quiet period before acting, so this only
	// shortens the delay between the last upload of a wave and the scan that
	// notices it.
	rt.kickRequeueScan()
}

// retrySourceAudioWrite runs one small database operation with a bounded
// backoff ladder. A transient busy or I/O error must not be the difference
// between a participant's audio reaching the transcript and being stored
// forever unread.
func retrySourceAudioWrite(ctx context.Context, attempt func() error) error {
	var err error
	delay := sourceAudioNoteBackoff
	for try := 0; try < sourceAudioNoteAttempts; try++ {
		if err = attempt(); err == nil {
			return nil
		}
		if errors.Is(err, ErrNoSuchJob) || errors.Is(err, context.Canceled) {
			// Retrying cannot conjure the row back, and a cancelled context
			// will not become live again.
			return err
		}
		if try == sourceAudioNoteAttempts-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		delay *= 2
	}
	return err
}

// dispatchSourceAudioRebuilds re-runs a finished meeting whose participant
// audio arrived after its build.
//
// It rides the existing requeue dispatcher rather than a timer of its own. That
// gives startup recovery for free: the first pass runs immediately after the
// operator comes up, so a rebuild owed across a restart — including a rebuild
// the operator died in the middle of, which MarkIncompleteJobsInterrupted has
// just put back in a listed state — is simply picked up, with no recovery path
// of its own to get wrong.
//
// Every rebuild goes through QueueRerunAttempt, the same path an
// administrator's rerun takes. Nothing here writes a published byte: the new
// attempt builds into its own directory, seals its own immutable `.opus` and
// publishes that (D-583), so a rebuild that dies half way leaves the previous
// meeting exactly as it was rather than a truncated replacement of it.
func (rt *Runtime) dispatchSourceAudioRebuilds() {
	if rt.store == nil {
		return
	}
	if !sourceAudioIngestEnabled() {
		// With ingestion off the rebuild would read the capture and then not
		// use it, republishing a byte-identical meeting. The debt stays owed,
		// so turning ingestion on later still picks it up.
		return
	}
	rt.reconcileCaptureReceipts()
	candidates, err := rt.store.ListJobsAwaitingSourceAudioRebuild(rt.ctx, 0)
	if err != nil {
		if rt.ctx.Err() == nil {
			rt.logger.Printf("requeue scan source-audio rebuild failed: %v", err)
		}
		return
	}
	now := time.Now().UTC()
	maxAge := captureLimitsFromEnv().maxAge
	quiet := rt.sourceAudioRebuildQuietPeriod()
	for _, candidate := range candidates {
		if rt.ctx.Err() != nil {
			return
		}
		rt.considerSourceAudioRebuild(candidate, now, quiet, maxAge)
	}
}

// considerSourceAudioRebuild decides one candidate. Split out so each refusal
// has one place to be read and one place to be tested.
func (rt *Runtime) considerSourceAudioRebuild(candidate sourceAudioRebuildCandidate, now time.Time, quiet, maxAge time.Duration) {
	// The wave. Every browser in the room starts uploading on the same signal,
	// so acting on the first arrival would transcribe the meeting again for
	// each of the others in turn.
	if !candidate.UploadedAt.IsZero() && now.Sub(candidate.UploadedAt) < quiet {
		return
	}
	// A recorder that stopped without ever writing an end. It produced no run
	// bundle, so there is nothing to rebuild from and never will be.
	//
	// Named here rather than left to fall through the capture scan below. That
	// scan would report "no capture is on disk for it any more" — the window it
	// was given is empty, so it finds nothing — which is a false statement about
	// a directory that is right there, and the one an administrator reads while
	// wondering where a participant's audio went.
	if candidate.Window.StartMS <= 0 {
		rt.logger.Printf("source audio: job=%s (state=%s) never finished recording, so it has no run bundle to rebuild from and its late upload cannot reach a transcript; not rebuilding",
			candidate.JobID, candidate.State)
		rt.settleSourceAudioDebt(candidate)
		return
	}
	// The retention bound. A capture older than the window the sweep enforces
	// is either already gone or about to be, and rebuilding a meeting from
	// audio that is being deleted underneath the build is worse than not
	// rebuilding it.
	if maxAge > 0 && !candidate.RecordedAt.IsZero() && now.Sub(candidate.RecordedAt) > maxAge {
		rt.logger.Printf("source audio: job=%s is older than the capture retention window (%s), so its late upload is not rebuilt for",
			candidate.JobID, maxAge)
		rt.settleSourceAudioDebt(candidate)
		return
	}
	if candidate.RoomToken == "" {
		// Without a room token the build is not given --source-audio-room at
		// all (build_runtime.go), so a rebuild would read nothing.
		rt.logger.Printf("source audio: job=%s has no Talk room, so its late upload cannot be placed; not rebuilding", candidate.JobID)
		rt.settleSourceAudioDebt(candidate)
		return
	}
	set, err := scanSourceCapturesForRecording(rt.cfg.CaptureRoot, candidate.RoomToken, candidate.Window, candidate.JobID)
	if err != nil {
		// "I could not look" is not "there is nothing there". Leave the debt
		// standing and try again on the next pass.
		rt.logger.Printf("source audio: could not read the captures for job=%s: %v", candidate.JobID, err)
		return
	}
	if set.Count == 0 {
		// The sweep took it, or a re-upload was refused and set aside. A
		// rebuild now would replace a meeting that HAS the audio with one that
		// does not.
		rt.logger.Printf("source audio: job=%s is owed a rebuild but no capture is on disk for it any more; not rebuilding", candidate.JobID)
		rt.settleSourceAudioDebt(candidate)
		return
	}
	if candidate.BuiltDigest != "" && set.Digest == candidate.BuiltDigest {
		// The no-op. The last successful build read exactly these bytes, so
		// another one would republish a byte-identical meeting. Saying so is
		// worth a line, because "an upload arrived and nothing happened" is
		// otherwise indistinguishable from a bug.
		rt.logger.Printf("source audio: job=%s already built from these %d captures (%s); not rebuilding",
			candidate.JobID, set.Count, strings.Join(set.Owners, ", "))
		rt.settleSourceAudioDebt(candidate)
		return
	}

	job, err := rt.store.GetJob(rt.ctx, candidate.JobID)
	if err != nil {
		if rt.ctx.Err() == nil {
			rt.logger.Printf("source audio: could not load job=%s for rebuild: %v", candidate.JobID, err)
		}
		return
	}
	rerun, err := rt.store.QueueRerunAttempt(rt.ctx, job, nowUTCString())
	if err != nil {
		if errors.Is(err, ErrJobNotEligibleForRerun) {
			rt.refuseIneligibleSourceAudioRebuild(candidate)
			return
		}
		if rt.ctx.Err() == nil {
			rt.logger.Printf("source audio: could not rebuild job=%s: %v", candidate.JobID, err)
		}
		return
	}
	if err := retrySourceAudioWrite(rt.ctx, func() error {
		return rt.store.NoteSourceAudioRebuildQueued(rt.ctx, candidate.JobID)
	}); err != nil {
		// Counted after the fact, so a failure here costs an allowance not
		// charged rather than a rebuild not run. The attempt is already
		// committed and will clear the debt when it succeeds.
		rt.logger.Printf("source audio: rebuild of job=%s is queued but was not counted against its ceiling: %v", candidate.JobID, err)
	}
	rt.logger.Printf("source audio: rebuilding job=%s as attempt %d from %d capture(s) (%s); audio arrived after its transcript was made",
		candidate.JobID, rerun.CurrentAttemptNumber, set.Count, strings.Join(set.Owners, ", "))
	rt.kickRequeueScan()
}

// refuseIneligibleSourceAudioRebuild decides what a rerun refusal meant.
//
// Two very different things arrive here. A job whose state moved since the scan
// listed it — a worker claimed it, an administrator reran it — is a race, and
// the right answer is to say nothing and let the next pass judge it again. A
// job still sitting in the state it was listed in was refused for the other
// reason QueueRerunAttempt has: there is no ready run bundle to build from,
// which is what an interrupted RECORDING leaves behind. That is permanent, and
// leaving it owed means re-deciding it every fifteen seconds for the life of
// the installation while it holds one of the scan's LIMIT slots against every
// job that could actually be rebuilt.
func (rt *Runtime) refuseIneligibleSourceAudioRebuild(candidate sourceAudioRebuildCandidate) {
	job, err := rt.store.GetJob(rt.ctx, candidate.JobID)
	if err != nil {
		return
	}
	if job.State != candidate.State {
		// Moved under us. Silent: the next pass judges it afresh, and this scan
		// runs every fifteen seconds.
		return
	}
	rt.logger.Printf("source audio: job=%s (state=%s) has no run bundle to rebuild from, so its late upload cannot reach a transcript; not rebuilding",
		candidate.JobID, candidate.State)
	rt.settleSourceAudioDebt(candidate)
}

// settleSourceAudioDebt marks the owed uploads as accounted for without
// rebuilding. Without it every refusal above would be re-decided every fifteen
// seconds for the life of the installation.
func (rt *Runtime) settleSourceAudioDebt(candidate sourceAudioRebuildCandidate) {
	if err := retrySourceAudioWrite(rt.ctx, func() error {
		return rt.store.ClearSourceAudioDebt(rt.ctx, candidate.JobID, candidate.UploadSeq)
	}); err != nil && rt.ctx.Err() == nil {
		rt.logger.Printf("source audio: could not settle the rebuild debt for job=%s: %v", candidate.JobID, err)
	}
}
