package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// defaultSealJobTimeout bounds one `cassini pack` run so a hung ffmpeg cannot
// hold the seal worker forever. It matches the publish ceiling: packing is a
// remux plus two PCM decodes over one meeting, which is generous at any
// plausible recording length, and the deadline derives from the worker's
// context so shutdown still cancels immediately.
const defaultSealJobTimeout = 30 * time.Minute

// sealTask is one job's seal work: pack this attempt's `.meeting` bundle into
// the attempt's immutable `.opus`. The meeting path travels with the task so a
// restart can rebuild it from the DB (ListQueuedSealTasks) rather than infer it.
type sealTask struct {
	JobID               string
	AttemptNumber       int
	ArtifactMeetingPath string
}

func (rt *Runtime) startSealWorker() {
	rt.workerWG.Add(1)
	go rt.sealWorker()
}

func (rt *Runtime) sealWorker() {
	defer rt.workerWG.Done()
	for {
		select {
		case <-rt.ctx.Done():
			return
		case task := <-rt.sealQueue:
			rt.runSealJob(task)
		}
	}
}

// runSealJob turns a built meeting into the immutable artifact publish will
// deliver. The order is the crash-safety argument and runs this way on purpose:
//
//  1. claim               seal/queued -> seal/running (conditional)
//  2. pack                attempt .meeting -> attempt .opus  (verified by cassini pack)
//  3. digest              sha256 of the sealed file
//  4. promote             attempt .opus -> current/<job>.opus (atomic rename)
//  5. MarkSealSucceeded   records both paths + the digest AND queues publish
//
// A crash between 4 and 5 leaves a correct canonical `.opus` and a seal/queued
// row the requeue dispatcher re-runs — packing the same bundle again produces
// the same artifact, so the retry costs nothing. A crash the other way round
// would advertise a canonical artifact that was never promoted, which is why
// the promotion comes first.
func (rt *Runtime) runSealJob(task sealTask) {
	startedAt := nowUTCString()
	claimed, err := rt.store.ClaimSealRunning(context.Background(), task.JobID, startedAt)
	if err != nil {
		rt.logger.Printf("seal start update failed id=%s: %v", task.JobID, err)
		return
	}
	if !claimed {
		// Duplicate delivery (direct enqueue plus a requeue-dispatcher re-scan)
		// or the job state changed since queueing; another worker owns it.
		rt.logger.Printf("seal claim skipped id=%s attempt=%d: job is not seal/queued", task.JobID, task.AttemptNumber)
		return
	}
	rt.logger.Printf("seal started id=%s attempt=%d meeting=%s", task.JobID, task.AttemptNumber, task.ArtifactMeetingPath)

	attemptOpus, err := rt.sealJobFn(rt.ctx, task)
	finishedAt := nowUTCString()
	if err != nil {
		rt.failSeal(task, "", err, finishedAt)
		return
	}

	digest, err := fileSHA256(attemptOpus)
	if err != nil {
		rt.failSeal(task, attemptOpus, err, finishedAt)
		return
	}

	canonicalOpus, err := promoteOpusFile(rt.cfg.WorkRoot, attemptOpus, task.JobID)
	if err != nil {
		rt.failSeal(task, attemptOpus, err, finishedAt)
		return
	}

	if err := rt.enqueuePublishAfterSeal(task, canonicalOpus, attemptOpus, digest, finishedAt); err != nil {
		rt.logger.Printf("publish queue update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, err)
		if updateErr := rt.store.MarkPublishFailed(context.Background(), task.JobID, "", "", err.Error(), finishedAt); updateErr != nil {
			rt.logger.Printf("publish queue failure update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, updateErr)
		}
		return
	}
	rt.logger.Printf("seal succeeded id=%s attempt=%d opus=%s sha256=%s canonical_opus=%s publish_queued_at=%s", task.JobID, task.AttemptNumber, attemptOpus, digest, canonicalOpus, finishedAt)
}

// failSeal ends the job at done/failed. A rerun is the retry path, exactly as it
// is for a failed build or publish: the run bundle in current/ is what it
// rebuilds from, so a failed seal loses nothing but time.
func (rt *Runtime) failSeal(task sealTask, attemptOpus string, cause error, finishedAt string) {
	rt.logger.Printf("seal failed id=%s attempt=%d opus=%s: %v", task.JobID, task.AttemptNumber, attemptOpus, cause)
	if err := rt.store.MarkSealFailed(context.Background(), task.JobID, attemptOpus, cause.Error(), finishedAt); err != nil {
		rt.logger.Printf("seal fail update failed id=%s attempt=%d: %v", task.JobID, task.AttemptNumber, err)
	}
}

// enqueueSealJobNonBlocking durably marks the job seal/queued and hands it to
// the seal worker without ever blocking the build worker: the DB row is the
// source of truth and the requeue dispatcher re-delivers any task the channel
// could not accept (full queue) or never saw (operator restart) (D-367).
func (rt *Runtime) enqueueSealJobNonBlocking(jobID string, attemptNumber int, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt string) error {
	if err := rt.store.MarkSealQueued(context.Background(), jobID, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt); err != nil {
		return err
	}
	task := sealTask{JobID: jobID, AttemptNumber: attemptNumber, ArtifactMeetingPath: attemptArtifactMeetingPath}
	select {
	case rt.sealQueue <- task:
	default:
		rt.logger.Printf("seal queue full id=%s attempt=%d: durably queued for the requeue dispatcher", jobID, attemptNumber)
		rt.kickRequeueScan()
	}
	return nil
}

// enqueuePublishAfterSeal is the only path to publish/queued. MarkSealSucceeded
// writes the sealed artifact and the publish hand-off in one transaction, so a
// queued publish always has a recorded artifact behind it; the channel send
// afterwards never blocks the seal worker on a full publish queue.
func (rt *Runtime) enqueuePublishAfterSeal(task sealTask, canonicalOpus, attemptOpus, digest, queuedAt string) error {
	if err := rt.store.MarkSealSucceeded(context.Background(), task.JobID, canonicalOpus, attemptOpus, digest, queuedAt); err != nil {
		return err
	}
	publish := publishTask{
		JobID:         task.JobID,
		AttemptNumber: task.AttemptNumber,
		OpusPath:      attemptOpus,
		OpusSHA256:    digest,
	}
	select {
	case rt.publishQueue <- publish:
	default:
		rt.logger.Printf("publish queue full id=%s attempt=%d: durably queued for the requeue dispatcher", task.JobID, task.AttemptNumber)
		rt.kickRequeueScan()
	}
	return nil
}

// executeSealCLI runs `cassini pack` for one attempt, teeing its output into the
// attempt's seal log so a pack failure is diagnosable the same way a build or
// publish failure is.
func (rt *Runtime) executeSealCLI(ctx context.Context, task sealTask) (string, error) {
	logPath, logFile, err := openAttemptLogFile(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber, "seal")
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	if err := rt.store.SetAttemptStageLogPath(context.Background(), task.JobID, task.AttemptNumber, "seal", logPath); err != nil {
		return "", err
	}
	sink := io.MultiWriter(writerOrDiscard(rt.stdout), logFile)
	// The room's display name is also the meeting title (D-462); its token is
	// what `cassini pack` derives the published room id from (D-622).
	roomToken, roomName := rt.talkRoomForJob(task.JobID)
	return packAttemptMeetingToOpus(
		ctx,
		rt.cfg.CassiniBin,
		task.ArtifactMeetingPath,
		attemptOpusPath(rt.cfg.WorkRoot, task.JobID, task.AttemptNumber),
		roomName,
		roomToken,
		roomName,
		task.JobID,
		task.AttemptNumber,
		sink,
	)
}

// executeSealCLIWithTimeout bounds one seal run so a hung `cassini pack` cannot
// hold the seal worker forever, mirroring executePublishCLIWithTimeout.
func (rt *Runtime) executeSealCLIWithTimeout(ctx context.Context, task sealTask) (string, error) {
	timeout := rt.sealJobTimeout
	if timeout <= 0 {
		return rt.executeSealCLI(ctx, task)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opusPath, err := rt.executeSealCLI(ctx, task)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return opusPath, fmt.Errorf("seal timed out after %s: %w", timeout, err)
	}
	return opusPath, err
}

// dispatchQueuedSealTasks feeds DB-queued seal rows into the seal queue. It
// reports whether an undelivered backlog remains (full channel).
func (rt *Runtime) dispatchQueuedSealTasks(dispatched map[string]struct{}) bool {
	tasks, err := rt.store.ListQueuedSealTasks(rt.ctx)
	if err != nil {
		if rt.ctx.Err() == nil {
			rt.logger.Printf("requeue scan seal failed: %v", err)
		}
		return false
	}
	backlog := false
	queued := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		key := requeueKey(task.JobID, task.AttemptNumber)
		queued[key] = struct{}{}
		if _, sent := dispatched[key]; sent {
			continue
		}
		select {
		case rt.sealQueue <- task:
			dispatched[key] = struct{}{}
		default:
			backlog = true
		}
	}
	pruneDispatched(dispatched, queued)
	return backlog
}
