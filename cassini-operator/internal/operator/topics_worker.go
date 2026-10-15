package operator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The topics worker (docs/proposals/automatic-tagging, "Per-meeting
// pipeline"): one meeting at a time, it embeds the delivered transcript with
// `cassini topics embed`, stores the vectors and scores them against the
// library. It yields to recordings, waits for memory, and never counts a
// wait as a failed attempt.

// topicSettings is the topic_suggestions block of settings.json.
type topicSettings struct {
	Enabled bool `json:"enabled"`
	// Threads is the embed's CPU thread count, 0 for the default.
	Threads int `json:"threads,omitempty"`
}

const (
	// The cassini CLI's `topics` exit codes.
	topicsCLIExitUnavailable = 3
	topicsCLIExitModelFault  = 4
	topicsCLIExitTranscript  = 5

	// topicsEmbedTimeout bounds one embed once it holds the inference lock;
	// waiting for the lock behind a build is not timed.
	topicsEmbedTimeout = 10 * time.Minute
	// topicsMemoryRetry is how long a meeting waits after low memory.
	topicsMemoryRetry = 5 * time.Minute
	// topicsEmbedMemMB is the embed's peak (0.58 GB measured) with a margin,
	// as modelBuildPeakMB gives the probe.
	topicsEmbedMemMB = 1536
	// topicsReconcileEvery is the reconciler's period.
	topicsReconcileEvery = time.Hour
	topicsVersionsTTL    = time.Minute
	maxTopicsCLIStderr   = 2048
)

// Worker-facing reasons in status and in topic_meeting.reason.
const (
	topicReasonModelUnavailable = "model-unavailable"
	topicReasonModelFault       = "model-fault"
)

// errTopicsLockWait is a deferral: the embed was stopped before it held the
// inference lock.
var errTopicsLockWait = errors.New("waiting for the inference lock")

type topicsService struct {
	rt      *Runtime
	exapp   ExAppConfig
	store   *topicsStore
	vectors *topicsVectorStore
	logger  *log.Logger
	wake    chan struct{}
	start   sync.Once

	versionsMu sync.Mutex
	versionsAt time.Time
	versions   topicVersions
	// seenVersions are the versions the worker last ran with.
	seenVersions topicVersions

	statsMu sync.Mutex
	stats   topicsWorkerStats

	// Seams for tests.
	archive   func(ctx context.Context, opusName, dir string) (path, digest string, err error)
	delivered func(ctx context.Context, opusName string) (digest string, exists bool, err error)
	targets   func(ctx context.Context) ([]searchBackfillTarget, error)
	memory    func(ctx context.Context) error
}

// topicsWorkerStats are content-free counters for ADMIN status.
type topicsWorkerStats struct {
	Embedded         int     `json:"embedded"`
	Rescored         int     `json:"rescored"`
	Preemptions      int     `json:"preemptions"`
	MemoryDeferrals  int     `json:"memoryDeferrals"`
	LockWaitSeconds  float64 `json:"lockWaitSeconds"`
	TokensPerSecond  float64 `json:"tokensPerSecond"`
	LastError        string  `json:"lastError,omitempty"`
	LastReconcileUTC string  `json:"lastReconcileUtc,omitempty"`
	Paused           string  `json:"paused,omitempty"`
}

// newTopicsService opens the vectors sidecar; nil when topic suggestions
// cannot run here (no durable annotations store).
func newTopicsService(rt *Runtime, exapp ExAppConfig, logger *log.Logger) *topicsService {
	store := rt.annotationReads()
	if store == nil || strings.TrimSpace(rt.cfg.CassiniBin) == "" {
		return nil
	}
	vectors, err := openTopicsVectorStore(topicsVectorsPath(rt.cfg.DBPath), logger)
	if err != nil {
		logger.Printf("topic vectors unavailable (%v); topic suggestions are off", err)
		return nil
	}
	vectors.lifecycle = rt.store
	s := &topicsService{rt: rt, exapp: exapp, store: newTopicsStore(store), vectors: vectors, logger: logger, wake: make(chan struct{}, 1)}
	s.store.lifecycle = rt.store
	if exapp.appAPIActive() {
		client := exapp.archiveDeliveredState()
		s.delivered = client
		s.targets = exapp.archiveBackfillTargets
		s.archive = func(ctx context.Context, opusName, dir string) (string, string, error) {
			local := filepath.Join(dir, opusName)
			digest, err := exapp.downloadArchiveOpus(ctx, &http.Client{Timeout: archiveOpusReadTimeout}, opusName, local)
			return local, digest, err
		}
	}
	s.memory = func(ctx context.Context) error {
		limits := resourceLimitsFromEnv()
		return limits.waitForMemory(ctx, topicsEmbedMemMB+limits.cpuMemHeadroomMB, rt.logger.Printf)
	}
	return s
}

func (s *topicsService) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf("topics: "+format, args...)
	}
}

func (s *topicsService) wakeUp() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *topicsService) settings() topicSettings { return s.rt.currentSettings().TopicSuggestions }

// startWorker runs the worker and the reconciler until the runtime stops.
func (s *topicsService) startWorker() {
	s.start.Do(func() {
		s.rt.workerWG.Add(1)
		go s.loop()
	})
}

func (s *topicsService) loop() {
	defer s.rt.workerWG.Done()
	if n, err := s.store.recoverRunning(s.rt.ctx); err != nil {
		s.logf("recover running rows: %v", err)
	} else if n > 0 {
		s.logf("%d meetings were running when the operator stopped; queued again", n)
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var lastReconcile time.Time
	for {
		if s.settings().Enabled && time.Since(lastReconcile) >= topicsReconcileEvery {
			if err := s.reconcile(s.rt.ctx); err != nil && s.rt.ctx.Err() == nil {
				s.logf("reconcile: %v", err)
			} else {
				lastReconcile = time.Now()
			}
		}
		for s.rt.ctx.Err() == nil && s.runOnce(s.rt.ctx) {
		}
		// Nothing to embed: recluster rooms, while no one is recording. A
		// recording that starts cancels the pass, as it cancels an embed.
		if s.settings().Enabled && s.rt.ctx.Err() == nil && s.recordingIdle() {
			if workCtx, done, err := s.rt.beginBackgroundBuild(); err == nil {
				if err := s.proposalPass(workCtx); err != nil && workCtx.Err() == nil {
					s.logf("proposals: %v", err)
				}
				done()
			}
		}
		select {
		case <-s.rt.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

// recordingIdle says whether recordings leave room for background work.
func (s *topicsService) recordingIdle() bool {
	s.rt.priorityMu.Lock()
	defer s.rt.priorityMu.Unlock()
	return s.rt.processingPolicy() == policyConcurrent || s.rt.recordingIdleLocked()
}

// paused says why the worker cannot run now, "" when it can.
func (s *topicsService) paused(ctx context.Context) string {
	return s.pausedWithSettings(ctx, s.settings())
}

// modelPaused says why the embedding model cannot run, "" when it can.
func (s *topicsService) modelPaused(ctx context.Context) string {
	models, err := s.rt.cachedModelInventory(ctx, deviceCPU)
	if err != nil {
		return topicReasonModelUnavailable
	}
	for _, m := range models {
		if m.Kind != modelKindEmbedding || m.ID != defaultEmbeddingModel {
			continue
		}
		switch {
		case m.Ready:
			return ""
		case m.Installed && (m.RuntimeSupported == nil || *m.RuntimeSupported):
			// Installed and runnable but not ready: its check failed.
			return topicReasonModelFault
		}
	}
	return topicReasonModelUnavailable
}

func (s *topicsService) setPaused(reason string) {
	s.statsMu.Lock()
	s.stats.Paused = reason
	s.statsMu.Unlock()
}

// runOnce processes at most one meeting; false when there was nothing to do.
func (s *topicsService) runOnce(ctx context.Context) bool {
	reason := s.paused(ctx)
	s.setPaused(reason)
	if reason != "" {
		return false
	}
	versions, err := s.currentVersions(ctx)
	if err != nil || versions.ModelRevision == "" {
		s.setPaused(topicReasonModelUnavailable)
		return false
	}
	// A new model or recipe makes every stored vector stale: queue them now
	// rather than at the next hourly pass.
	if s.seenVersions != (topicVersions{}) && s.seenVersions != versions {
		if err := s.reconcile(ctx); err != nil {
			s.logf("reconcile after a version change: %v", err)
		}
	}
	s.seenVersions = versions
	// Wait for recordings before claiming, so a claimed row is running.
	workCtx, done, err := s.rt.beginBackgroundBuild()
	if err != nil {
		return false
	}
	defer done()
	if err := s.ensureTextVectors(workCtx, versions); err != nil {
		if !errors.Is(err, errTopicsLockWait) && workCtx.Err() == nil {
			s.recordError(fmt.Errorf("embed definitions: %w", err))
		}
		return false
	}
	// Switching off stops new claims, even one that waited for a recording.
	if !s.settings().Enabled {
		return false
	}
	if err := s.fitGates(workCtx, versions); err != nil && workCtx.Err() == nil {
		s.recordError(fmt.Errorf("fit topic gates: %w", err))
	}
	if err := s.rescorePending(workCtx, versions); err != nil && workCtx.Err() == nil {
		s.recordError(fmt.Errorf("rescore: %w", err))
	}
	row, ok, err := s.store.claimNext(ctx, !s.buildsWaiting(ctx))
	if err != nil {
		s.logf("claim: %v", err)
		return false
	}
	if !ok {
		return false
	}
	s.process(workCtx, row, versions)
	return true
}

// buildsWaiting says whether a meeting build is queued or running: backfill
// work waits for it.
func (s *topicsService) buildsWaiting(ctx context.Context) bool {
	var n int
	err := s.rt.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE stage IN ('build','seal') AND state IN ('queued','running')`).Scan(&n)
	return err != nil || n > 0
}

func (s *topicsService) recordError(err error) {
	s.logf("%v", err)
	s.statsMu.Lock()
	s.stats.LastError = err.Error()
	s.statsMu.Unlock()
}

// process embeds and scores one claimed meeting.
func (s *topicsService) process(ctx context.Context, row topicMeetingRow, versions topicVersions) {
	base := context.WithoutCancel(ctx)
	deferred := func(retry time.Duration, reason string) {
		if err := s.store.release(base, row, time.Now().Add(retry), reason); err != nil {
			s.logf("release %s: %v", row.OpusName, err)
		}
	}
	if err := s.memory(ctx); err != nil {
		if ctx.Err() != nil {
			s.countPreemption()
			deferred(0, "")
			return
		}
		s.statsMu.Lock()
		s.stats.MemoryDeferrals++
		s.statsMu.Unlock()
		deferred(topicsMemoryRetry, "waiting for memory")
		return
	}
	tmp, err := os.MkdirTemp(s.rt.cfg.WorkRoot, "topics-")
	if err != nil {
		s.failRow(base, row, err)
		return
	}
	defer os.RemoveAll(tmp)
	input, unavailable, err := s.transcriptSource(ctx, row, tmp)
	switch {
	case ctx.Err() != nil:
		s.countPreemption()
		deferred(0, "")
		return
	case unavailable != "":
		if err := s.store.markUnavailable(base, row, unavailable); err != nil {
			s.logf("record %s unavailable: %v", row.OpusName, err)
		}
		s.logf("%s not checked (%s)", row.OpusName, unavailable)
		return
	case err != nil:
		s.failRow(base, row, err)
		return
	}
	knownKey, err := s.vectors.embedKey(ctx, row.OpusName)
	if err != nil {
		s.failRow(base, row, err)
		return
	}
	out := filepath.Join(tmp, "passages.jsonl")
	started := time.Now()
	run, err := s.runEmbed(ctx, input, out, knownKey)
	if draftFormat(err) {
		// Published in a pre-publication draft of the format, which this
		// build does not read: the job's own bundle still has the transcript.
		bundle, ok, linkErr := s.linkBundle(row, tmp)
		if linkErr != nil {
			s.failRow(base, row, linkErr)
			return
		}
		if ok {
			run, err = s.runEmbed(ctx, bundle, out, knownKey)
		}
	}
	if err != nil {
		var cliErr *topicsCLIError
		switch {
		case errors.Is(err, errTopicsLockWait) || ctx.Err() != nil:
			if errors.Is(context.Cause(ctx), errRecordingPriority) {
				s.countPreemption()
			}
			deferred(0, "")
		case errors.As(err, &cliErr) && cliErr.code == topicsCLIExitTranscript:
			if err := s.store.markUnavailable(base, row, cliErr.reason()); err != nil {
				s.logf("record %s unavailable: %v", row.OpusName, err)
			}
			s.logf("%s not checked (%s)", row.OpusName, cliErr.reason())
		case errors.As(err, &cliErr) && cliErr.code == topicsCLIExitModelFault:
			if err := s.store.blockNow(base, row, topicReasonModelFault, cliErr.Error()); err != nil {
				s.logf("block %s: %v", row.OpusName, err)
			}
			s.recordError(err)
		case errors.As(err, &cliErr) && cliErr.code == topicsCLIExitUnavailable:
			// The model or runtime went away: wait for an admin, no attempt.
			s.invalidateVersions()
			s.rt.invalidateModelInventory()
			deferred(time.Minute, topicReasonModelUnavailable)
		default:
			s.failRow(base, row, err)
		}
		return
	}
	var set topicPassageSet
	if run.unchanged {
		stored, ok, err := s.vectors.meeting(ctx, row.OpusName)
		if err != nil || !ok || stored.EmbedKey != run.embedKey {
			// The sidecar lost them since the key was read: embed again.
			if err := s.vectors.forgetMeeting(ctx, row.OpusName); err != nil {
				s.failRow(base, row, err)
				return
			}
			deferred(0, "")
			return
		}
		set = stored
		if run.speakers != nil && !slices.Equal(run.speakers, stored.SpeakerTermHashes) {
			if err := s.vectors.setSpeakerTerms(base, row.OpusName, stored.EmbedKey, run.speakers); err != nil && !errors.Is(err, errMeetingRetired) {
				s.failRow(base, row, err)
				return
			}
			set.SpeakerTermHashes = run.speakers
		}
	} else {
		set, err = readTopicPassagesFile(out)
		if err != nil {
			s.failRow(base, row, err)
			return
		}
		if err := s.vectors.replaceMeeting(base, row.OpusName, set); err != nil {
			if errors.Is(err, errMeetingRetired) {
				s.dropRetired(base, row)
				return
			}
			s.failRow(base, row, err)
			return
		}
		s.statsMu.Lock()
		s.stats.Embedded++
		if seconds := run.embedSeconds; seconds > 0 {
			s.stats.TokensPerSecond = float64(run.tokens) / seconds
		}
		s.stats.LockWaitSeconds += run.lockWait.Seconds()
		s.statsMu.Unlock()
	}
	result := topicEmbedResult{
		EmbedKey: set.EmbedKey, PayloadSHA256: set.PayloadSHA256,
		Language: set.Language, LanguageConfidence: set.LanguageConfidence, DetectorVersion: set.DetectorVersion,
		Passages: len(set.Passages), Tokens: setTokens(set),
		Versions: topicVersions{ModelRevision: set.ModelRevision, ChunkerVersion: set.ChunkerVersion, RecipeVersion: set.RecipeVersion},
	}
	// Vectors from another model never meet this one's definitions: the
	// model changed since the versions were read. Read them again, and run
	// this meeting again then, without an attempt.
	if set.ModelRevision != versions.ModelRevision || set.RecipeVersion != versions.RecipeVersion || set.ChunkerVersion != versions.ChunkerVersion {
		s.invalidateVersions()
		deferred(0, "")
		return
	}
	inputs, ready, err := s.store.scoringInputs(base, versions)
	if err != nil || !ready {
		if err == nil {
			err = errors.New("the control definitions have no vectors yet")
		}
		s.failRow(base, row, err)
		return
	}
	scores := scoreMeetingTopics(set.passages(), inputs)
	if err := s.store.finishMeeting(base, row, s.rt.store, s.vectors.generation, result, scores); err != nil {
		if errors.Is(err, errMeetingRetired) {
			s.dropRetired(base, row)
			return
		}
		s.failRow(base, row, err)
		return
	}
	s.markRoomDirty(base, row.OpusName)
	suggested := 0
	for _, sc := range scores {
		if sc.Decision.qualifies() {
			suggested++
		}
	}
	if run.unchanged {
		s.statsMu.Lock()
		s.stats.Rescored++
		s.statsMu.Unlock()
	}
	s.logf("%s done passages=%d topics=%d qualifying=%d unchanged=%t in %s", row.OpusName, len(set.Passages), len(scores), suggested, run.unchanged, time.Since(started).Round(time.Millisecond))
}

func setTokens(set topicPassageSet) int {
	n := 0
	for _, p := range set.Passages {
		n += p.TokenCount
	}
	return n
}

// dropRetired deletes the claimed row of a meeting retired while it ran.
func (s *topicsService) dropRetired(ctx context.Context, row topicMeetingRow) {
	if err := s.store.dropRetired(ctx, row); err != nil {
		s.logf("drop retired %s: %v", row.OpusName, err)
	}
}

func (s *topicsService) countPreemption() {
	s.statsMu.Lock()
	s.stats.Preemptions++
	s.statsMu.Unlock()
}

func (s *topicsService) failRow(ctx context.Context, row topicMeetingRow, err error) {
	s.recordError(fmt.Errorf("%s: %w", row.OpusName, err))
	blocked, e := s.store.fail(ctx, row, err)
	if e != nil {
		s.logf("record failure of %s: %v", row.OpusName, e)
		return
	}
	if blocked {
		s.logf("%s blocked after %d attempts", row.OpusName, topicMaxAttempts)
	} else {
		time.AfterFunc(topicRetryDelay(1), s.wakeUp)
	}
}

// transcriptSource puts the delivered recording's transcript at a private
// path under dir: the promoted local copy when it is the delivered build,
// else the archive's own copy. unavailable names a meeting that has no
// delivered recording to read.
func (s *topicsService) transcriptSource(ctx context.Context, row topicMeetingRow, dir string) (path, unavailable string, err error) {
	digest := row.DeliveredDigest
	if digest == "" && s.delivered != nil {
		var exists bool
		digest, exists, err = s.delivered(ctx, row.OpusName)
		if err != nil {
			return "", "", fmt.Errorf("read delivered state: %w", err)
		}
		if !exists {
			return "", "recording-missing", nil
		}
	}
	if digest != "" {
		if path, ok, err := s.localCopy(row, digest, dir); err != nil || ok {
			return path, "", err
		}
	}
	if s.archive == nil {
		return "", "", fmt.Errorf("the local copy is not the delivered build and the archive cannot be read")
	}
	path, _, err = s.archive(ctx, row.OpusName, dir)
	if err != nil {
		return "", "", err
	}
	return path, "", nil
}

// localCopy links the promoted recording into dir when its digest is the
// delivered one, holding the archive gates so retention and promotion cannot
// change it during the check. The link keeps the bytes for the embed.
func (s *topicsService) localCopy(row topicMeetingRow, digest, dir string) (string, bool, error) {
	if s.rt.store != nil {
		s.rt.store.artifactGate.RLock()
		defer s.rt.store.artifactGate.RUnlock()
		unlock := s.rt.store.lockArtifacts(row.JobID)
		defer unlock()
	}
	local := canonicalOpusPath(s.rt.cfg.WorkRoot, row.JobID)
	if meetingExtension(row.OpusName) == ".json" {
		local = filepath.Join(currentRoot(s.rt.cfg.WorkRoot), row.JobID+".json")
	}
	sum, err := fileSHA256(local)
	if err != nil || !strings.EqualFold(sum, digest) {
		return "", false, nil
	}
	dest := filepath.Join(dir, row.OpusName)
	if err := os.Link(local, dest); err != nil {
		if err := copyFile(local, dest, 0o600); err != nil {
			return "", false, err
		}
	}
	return dest, true, nil
}

// topicsCLIError is a `cassini topics` failure with its exit code.
type topicsCLIError struct {
	code   int
	stderr string
}

func (e *topicsCLIError) Error() string {
	return fmt.Sprintf("cassini topics exited %d: %s", e.code, e.stderr)
}

// draftFormat says whether an embed refused its input as a file in a format
// this build does not read.
func draftFormat(err error) bool {
	var cliErr *topicsCLIError
	return errors.As(err, &cliErr) && cliErr.code == topicsCLIExitTranscript && cliErr.reason() == "unsupported-format"
}

// linkBundle links the job's current bundle into dir, holding the archive
// gates while it does, as localCopy does for the recording. ok is false when
// the job has no bundle (retention removed it).
func (s *topicsService) linkBundle(row topicMeetingRow, dir string) (string, bool, error) {
	if s.rt.store != nil {
		s.rt.store.artifactGate.RLock()
		defer s.rt.store.artifactGate.RUnlock()
		unlock := s.rt.store.lockArtifacts(row.JobID)
		defer unlock()
	}
	src := canonicalMeetingPath(s.rt.cfg.WorkRoot, row.JobID)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return "", false, nil
	}
	dest := filepath.Join(dir, row.JobID+".meeting")
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := os.Link(path, target); err != nil {
			return copyFile(path, target, 0o600)
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return dest, true, nil
}

// reason is the stderr's reason word after the command prefix, as in
// "cassini topics embed: transcript-integrity: ...".
func (e *topicsCLIError) reason() string {
	line := firstLine(e.stderr)
	if _, rest, ok := strings.Cut(line, ": "); ok {
		line = rest
	}
	reason, _, _ := strings.Cut(line, ":")
	switch reason = strings.TrimSpace(reason); reason {
	case "no-transcript", "transcript-has-no-words", "transcript-integrity", "unsupported-format":
		return reason
	}
	return "no-transcript"
}

type topicsEmbedRun struct {
	unchanged    bool
	embedKey     string
	speakers     []string
	header       topicPassagesHeader
	tokens       int
	lockWait     time.Duration
	embedSeconds float64
}

// runEmbed runs `cassini topics embed`. The timeout starts once the CLI holds
// the inference lock; a cancel before that is errTopicsLockWait.
func (s *topicsService) runEmbed(ctx context.Context, input, out, knownKey string) (topicsEmbedRun, error) {
	args := []string{"topics", "embed", "--input", input, "--out", out, "--cache-root", s.rt.cfg.ModelCacheRoot}
	if knownKey != "" {
		args = append(args, "--known-key", knownKey)
	}
	if n := s.settings().Threads; n > 0 {
		args = append(args, "--threads", strconv.Itoa(n))
	}
	var run topicsEmbedRun
	started := time.Now()
	var lockedAt time.Time
	key, err := s.store.speakerKey(ctx)
	if err != nil {
		return run, err
	}
	err = s.runCLIEnv(ctx, args, []string{"CASSINI_TOPICS_SPEAKER_KEY=" + string(key)}, func(line map[string]json.RawMessage) {
		if _, ok := line["locked"]; ok {
			lockedAt = time.Now()
			run.lockWait = lockedAt.Sub(started)
		}
		if raw, ok := line["unchanged"]; ok && string(raw) == "true" {
			run.unchanged = true
			_ = json.Unmarshal(line["embedKey"], &run.embedKey)
			_ = json.Unmarshal(line["speakerTermHashes"], &run.speakers)
		}
	})
	if err != nil {
		return run, err
	}
	if !lockedAt.IsZero() {
		run.embedSeconds = time.Since(lockedAt).Seconds()
	}
	if !run.unchanged {
		header, tokens, err := readTopicPassagesHeader(out)
		if err != nil {
			return run, err
		}
		run.header, run.tokens, run.embedKey = header, tokens, header.EmbedKey
	}
	return run, nil
}

// runCLI runs `cassini <args>`, hands each stdout JSON line to onLine and arms
// topicsEmbedTimeout when the CLI reports {"locked":true}.
func (s *topicsService) runCLI(ctx context.Context, args []string, onLine func(map[string]json.RawMessage)) error {
	return s.runCLIEnv(ctx, args, nil, onLine)
}

func (s *topicsService) runCLIEnv(ctx context.Context, args, extraEnv []string, onLine func(map[string]json.RawMessage)) error {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(runCtx, s.rt.cfg.CassiniBin, args...)
	cmd.Env = append(s.rt.modelEnv(), extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Start(); err != nil {
		return err
	}
	var locked bool
	var timer *time.Timer
	errTimeout := errors.New("topics embed timed out")
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		var line map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if _, ok := line["locked"]; ok && !locked {
			locked = true
			timer = time.AfterFunc(topicsEmbedTimeout, func() { cancel(errTimeout) })
		}
		onLine(line)
	}
	err = cmd.Wait()
	if timer != nil {
		timer.Stop()
	}
	if err == nil {
		return nil
	}
	if errors.Is(context.Cause(runCtx), errTimeout) {
		return errTimeout
	}
	if ctx.Err() != nil {
		if !locked {
			return errTopicsLockWait
		}
		return ctx.Err()
	}
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > maxTopicsCLIStderr {
		msg = msg[len(msg)-maxTopicsCLIStderr:]
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &topicsCLIError{code: exitErr.ExitCode(), stderr: msg}
	}
	return fmt.Errorf("cassini %s: %w: %s", args[1], err, msg)
}

// currentVersions asks the CLI which model, chunker and recipe it would use.
func (s *topicsService) currentVersions(ctx context.Context) (topicVersions, error) {
	s.versionsMu.Lock()
	defer s.versionsMu.Unlock()
	if !s.versionsAt.IsZero() && time.Since(s.versionsAt) < topicsVersionsTTL {
		return s.versions, nil
	}
	cmd := exec.CommandContext(ctx, s.rt.cfg.CassiniBin, "topics", "versions", "--cache-root", s.rt.cfg.ModelCacheRoot)
	cmd.Env = s.rt.modelEnv()
	raw, err := cmd.Output()
	if err != nil {
		return topicVersions{}, fmt.Errorf("cassini topics versions: %w", err)
	}
	var v topicVersions
	if err := json.Unmarshal(raw, &v); err != nil {
		return topicVersions{}, fmt.Errorf("cassini topics versions: %w", err)
	}
	s.versions, s.versionsAt = v, time.Now()
	return v, nil
}

func (s *topicsService) invalidateVersions() {
	s.versionsMu.Lock()
	s.versionsAt = time.Time{}
	s.versionsMu.Unlock()
}

// ensureTextVectors embeds every control and active definition that has no
// vector for the current versions.
func (s *topicsService) ensureTextVectors(ctx context.Context, v topicVersions) error {
	missing, err := s.store.missingTextVectors(ctx, v)
	if err != nil || len(missing) == 0 {
		return err
	}
	// Embedding definitions loads the model like a meeting does.
	if err := s.memory(ctx); err != nil {
		if ctx.Err() != nil {
			return errTopicsLockWait
		}
		return err
	}
	tmp, err := os.MkdirTemp(s.rt.cfg.WorkRoot, "topics-texts-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "texts.jsonl")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	byID := map[string]topicText{}
	for i, t := range missing {
		id := fmt.Sprintf("t%d", i)
		byID[id] = t
		if err := enc.Encode(map[string]string{"id": id, "text": t.Text}); err != nil {
			return err
		}
	}
	if err := os.WriteFile(in, buf.Bytes(), 0o600); err != nil {
		return err
	}
	out := filepath.Join(tmp, "vectors.jsonl")
	args := []string{"topics", "embed-texts", "--in", in, "--out", out, "--cache-root", s.rt.cfg.ModelCacheRoot}
	if n := s.settings().Threads; n > 0 {
		args = append(args, "--threads", strconv.Itoa(n))
	}
	if err := s.runCLI(ctx, args, func(map[string]json.RawMessage) {}); err != nil {
		return err
	}
	vectors, revision, recipe, err := readTopicTextVectors(out)
	if err != nil {
		return err
	}
	if revision != v.ModelRevision || recipe != v.RecipeVersion {
		// The model changed under us: the next pass sees the new versions.
		s.invalidateVersions()
		return fmt.Errorf("definitions were embedded with %s/%s, expected %s/%s", revision, recipe, v.ModelRevision, v.RecipeVersion)
	}
	for id, vector := range vectors {
		t, ok := byID[id]
		if !ok {
			continue
		}
		if err := s.store.putTextVector(ctx, t.Kind, t.SHA, v, vector); err != nil {
			return err
		}
	}
	s.logf("embedded %d definitions and controls", len(vectors))
	return nil
}

// topicPassagesHeader is the first line of a `cassini topics embed` output.
type topicPassagesHeader struct {
	Format             string   `json:"format"`
	EmbedKey           string   `json:"embedKey"`
	PayloadSHA256      string   `json:"payloadSha256"`
	ModelRevision      string   `json:"modelRevision"`
	ChunkerVersion     string   `json:"chunkerVersion"`
	RecipeVersion      string   `json:"recipeVersion"`
	Dimensions         int      `json:"dimensions"`
	Count              int      `json:"count"`
	Language           string   `json:"language"`
	LanguageConfidence float64  `json:"languageConfidence"`
	DetectorVersion    string   `json:"detectorVersion"`
	SpeakerTermHashes  []string `json:"speakerTermHashes"`
}

func readTopicPassagesHeader(path string) (topicPassagesHeader, int, error) {
	set, header, err := readTopicPassages(path)
	return header, setTokens(set), err
}

func readTopicPassagesFile(path string) (topicPassageSet, error) {
	set, _, err := readTopicPassages(path)
	return set, err
}

func readTopicPassages(path string) (topicPassageSet, topicPassagesHeader, error) {
	var set topicPassageSet
	var header topicPassagesHeader
	f, err := os.Open(path)
	if err != nil {
		return set, header, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for i := 0; scanner.Scan(); i++ {
		if i == 0 {
			if err := json.Unmarshal(scanner.Bytes(), &header); err != nil || header.Format != "cassini.topic-passages.v1" {
				return set, header, fmt.Errorf("%s is not a cassini.topic-passages.v1 file", path)
			}
			continue
		}
		var line struct {
			Ordinal    int       `json:"ordinal"`
			StartMS    int64     `json:"startMs"`
			EndMS      int64     `json:"endMs"`
			WordCount  int       `json:"wordCount"`
			TokenCount int       `json:"tokenCount"`
			Boundary   string    `json:"boundary"`
			Vector     []float32 `json:"vector"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return set, header, fmt.Errorf("%s line %d: %w", path, i+1, err)
		}
		if len(line.Vector) != header.Dimensions {
			return set, header, fmt.Errorf("%s line %d: %d dimensions, the header says %d", path, i+1, len(line.Vector), header.Dimensions)
		}
		set.Passages = append(set.Passages, topicStoredPassage{
			topicPassage: topicPassage{StartMS: line.StartMS, EndMS: line.EndMS, WordCount: line.WordCount, Vector: line.Vector},
			Ordinal:      line.Ordinal, TokenCount: line.TokenCount, Boundary: line.Boundary,
		})
	}
	if err := scanner.Err(); err != nil {
		return set, header, err
	}
	if len(set.Passages) != header.Count {
		return set, header, fmt.Errorf("%s has %d passages, the header says %d", path, len(set.Passages), header.Count)
	}
	set.EmbedKey, set.ModelRevision, set.ChunkerVersion, set.RecipeVersion = header.EmbedKey, header.ModelRevision, header.ChunkerVersion, header.RecipeVersion
	set.PayloadSHA256, set.Language, set.LanguageConfidence, set.DetectorVersion = header.PayloadSHA256, header.Language, header.LanguageConfidence, header.DetectorVersion
	set.SpeakerTermHashes = header.SpeakerTermHashes
	return set, header, nil
}

func readTopicTextVectors(path string) (map[string][]float32, string, string, error) {
	defs, revision, recipe, err := readTopicsDefinitions(path)
	return defs, revision, recipe, err
}
