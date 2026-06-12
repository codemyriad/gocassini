package operator

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Stage handoffs are non-blocking (D-367): producers persist state=queued
// (the durable source of truth) and then try a non-blocking channel send. The
// requeue dispatcher below is the safety net that re-delivers queued rows the
// channels dropped (full queue) or never saw (operator restart). Workers
// claim jobs atomically (queued -> running conditional UPDATE), so a rare
// duplicate delivery is skipped, never run twice. Before this, a backlogged
// or hung publish cascaded backwards — publish queue full -> build worker
// blocked -> record slot held -> every Talk start answered 503 — and queued
// work was stranded forever by a restart.
const (
	// requeueScanIdleInterval is how often the dispatcher re-scans the DB for
	// stranded queued rows when nothing is known to be backlogged.
	requeueScanIdleInterval = 15 * time.Second
	// requeueScanRetryDelay is the re-scan delay while a queue was full on
	// the previous pass.
	requeueScanRetryDelay = 500 * time.Millisecond
	// defaultPublishJobTimeout bounds one `cassini publish` run. Publish is
	// O(entire library), so the ceiling is generous — but without one a hung
	// publish holds the single publish worker forever.
	defaultPublishJobTimeout = 30 * time.Minute
)

// kickRequeueScan nudges the requeue dispatcher without blocking; concurrent
// kicks coalesce into one scan.
func (rt *Runtime) kickRequeueScan() {
	select {
	case rt.requeueKick <- struct{}{}:
	default:
	}
}

// requeueDispatcher is the single goroutine feeding DB-queued build/publish
// rows back into the worker channels. The first pass runs immediately so
// rows queued before a restart resume without operator intervention.
func (rt *Runtime) requeueDispatcher() {
	// dispatched remembers tasks already handed to a channel so a row that
	// stays queued while waiting in the channel is not re-sent every pass;
	// entries are pruned once the row leaves state=queued.
	dispatchedBuild := map[string]struct{}{}
	dispatchedPublish := map[string]struct{}{}
	for {
		buildBacklog := rt.dispatchQueuedBuildTasks(dispatchedBuild)
		publishBacklog := rt.dispatchQueuedPublishTasks(dispatchedPublish)
		delay := requeueScanIdleInterval
		if buildBacklog || publishBacklog {
			delay = requeueScanRetryDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-rt.ctx.Done():
			timer.Stop()
			return
		case <-rt.requeueKick:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func requeueKey(jobID string, attemptNumber int) string {
	return fmt.Sprintf("%s|%d", jobID, attemptNumber)
}

// dispatchQueuedBuildTasks feeds DB-queued build rows into the build queue.
// It reports whether an undelivered backlog remains (full channel).
func (rt *Runtime) dispatchQueuedBuildTasks(dispatched map[string]struct{}) bool {
	tasks, err := rt.store.ListQueuedBuildTasks(rt.ctx)
	if err != nil {
		if rt.ctx.Err() == nil {
			rt.logger.Printf("requeue scan build failed: %v", err)
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
		case rt.buildQueue <- task:
			dispatched[key] = struct{}{}
		default:
			backlog = true
		}
	}
	pruneDispatched(dispatched, queued)
	return backlog
}

// dispatchQueuedPublishTasks feeds DB-queued publish rows into the publish
// queue. It reports whether an undelivered backlog remains (full channel).
func (rt *Runtime) dispatchQueuedPublishTasks(dispatched map[string]struct{}) bool {
	tasks, err := rt.store.ListQueuedPublishTasks(rt.ctx)
	if err != nil {
		if rt.ctx.Err() == nil {
			rt.logger.Printf("requeue scan publish failed: %v", err)
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
		case rt.publishQueue <- task:
			dispatched[key] = struct{}{}
		default:
			backlog = true
		}
	}
	pruneDispatched(dispatched, queued)
	return backlog
}

// pruneDispatched forgets dispatched entries whose rows are no longer queued
// (a worker claimed them), so a later re-queue of the same job is delivered
// again.
func pruneDispatched(dispatched, queued map[string]struct{}) {
	for key := range dispatched {
		if _, stillQueued := queued[key]; !stillQueued {
			delete(dispatched, key)
		}
	}
}

// enqueuePublishJobNonBlocking durably marks the job publish/queued and hands
// it to the publish worker without ever blocking the (single) build worker on
// a full publish queue; the requeue dispatcher re-delivers any task the
// channel could not accept (D-367).
func (rt *Runtime) enqueuePublishJobNonBlocking(jobID string, attemptNumber int, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt string) error {
	if err := rt.store.MarkPublishQueued(context.Background(), jobID, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt); err != nil {
		return err
	}
	task := publishTask{JobID: jobID, AttemptNumber: attemptNumber}
	select {
	case rt.publishQueue <- task:
	default:
		rt.logger.Printf("publish queue full id=%s attempt=%d: durably queued for the requeue dispatcher", jobID, attemptNumber)
		rt.kickRequeueScan()
	}
	return nil
}

// executePublishCLIWithTimeout bounds one publish run with publishJobTimeout
// so a hung `cassini publish` cannot hold the publish worker forever (D-367).
// The deadline derives from the worker's context, so operator shutdown still
// cancels the run immediately.
func (rt *Runtime) executePublishCLIWithTimeout(ctx context.Context, task publishTask) (string, error) {
	timeout := rt.publishJobTimeout
	if timeout <= 0 {
		return rt.executePublishCLI(ctx, task)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sitePath, err := rt.executePublishCLI(ctx, task)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return sitePath, fmt.Errorf("publish timed out after %s: %w", timeout, err)
	}
	return sitePath, err
}
