package operator

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"time"
)

func roomDefault(info talkRoomInfo) (string, string) {
	if isOneToOneTalkRoom(info.Type) {
		if len(info.TitleParticipants) == 2 {
			return sanitizeTalkRoomName(info.TitleParticipants[0].Name + " & " + info.TitleParticipants[1].Name), "participants"
		}
		return info.Name, "talk"
	}
	if info.Type != 0 {
		return info.Name, "talk-group"
	}
	return info.Name, "metadata"
}

func (rt *Runtime) registerPublishedRoom(ctx context.Context, jobID string, raw json.RawMessage) error {
	var e roomEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if e.RoomID == "" || rt.meetingMetadata == nil {
		return nil
	}
	job, err := rt.store.GetJob(ctx, jobID)
	if err != nil || job.TalkBinding == nil {
		return rt.meetingMetadata.registerRoom(ctx, e.RoomID, e.RoomName)
	}
	state, err := decodeTalkBinding(*job.TalkBinding)
	if err != nil {
		return err
	}
	name, source := roomDefault(talkRoomInfo{Name: state.RoomName, Type: state.RoomType, TitleParticipants: state.TitleParticipants})
	return rt.meetingMetadata.observeRoom(ctx, e.RoomID, name, source, job.CreatedAt)
}

// roomNameRetryState paces reconciliation for one room: each pass that ends
// without a terminal source doubles the wait before the room is tried again.
type roomNameRetryState struct {
	failures int
	next     time.Time
}

const (
	roomNameRetryBase = 5 * time.Minute
	roomNameRetryCap  = 6 * time.Hour
)

func (rt *Runtime) roomNameRetryDue(roomID string) bool {
	rt.roomNameRetryMu.Lock()
	defer rt.roomNameRetryMu.Unlock()
	state, ok := rt.roomNameRetry[roomID]
	return !ok || !time.Now().Before(state.next)
}

func (rt *Runtime) deferRoomNameRetry(roomID string) {
	rt.roomNameRetryMu.Lock()
	defer rt.roomNameRetryMu.Unlock()
	if rt.roomNameRetry == nil {
		rt.roomNameRetry = map[string]roomNameRetryState{}
	}
	state := rt.roomNameRetry[roomID]
	// 5min<<7 already exceeds the cap; bounding the shift keeps it from
	// overflowing after weeks of failures.
	delay := min(roomNameRetryBase<<min(state.failures, 7), roomNameRetryCap)
	state.failures++
	state.next = time.Now().Add(delay)
	rt.roomNameRetry[roomID] = state
}

func (rt *Runtime) clearRoomNameRetry(roomID string) {
	rt.roomNameRetryMu.Lock()
	defer rt.roomNameRetryMu.Unlock()
	delete(rt.roomNameRetry, roomID)
}

// A tracked worker retries only unresolved room defaults. No catalog read waits
// for Talk, and each pass calls Talk at most once per room, sequentially.
func (rt *Runtime) startRoomNamesWorker() {
	if rt.meetingMetadata == nil || rt.fetchTalkRoomName == nil {
		return
	}
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			if err := rt.reconcileRoomNames(rt.ctx); err != nil && rt.ctx.Err() == nil {
				rt.logger.Printf("room defaults reconciliation: %v", err)
			}
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (rt *Runtime) reconcileRoomNames(ctx context.Context) error {
	if err := rt.meetingMetadata.registerMetadataRooms(ctx); err != nil {
		return err
	}
	rows, err := rt.meetingMetadata.db.QueryContext(ctx, `SELECT entry_json FROM meeting_metadata ORDER BY date_label DESC, file_id DESC`)
	if err != nil {
		return err
	}
	var entries []roomEntry
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var e roomEntry
		if json.Unmarshal(raw, &e) == nil && e.RoomID != "" {
			entries = append(entries, e)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if seen[e.RoomID] {
			continue
		}
		var source string
		if err := rt.meetingMetadata.db.QueryRowContext(ctx, `SELECT default_source FROM room_names WHERE room_id=?`, e.RoomID).Scan(&source); err != nil {
			return err
		}
		if source == "participants" || source == "talk-group" {
			continue
		}
		if !rt.roomNameRetryDue(e.RoomID) {
			continue
		}
		jobID := e.JobID
		if jobID == "" {
			jobID = e.ID
		}
		job, err := rt.store.GetJob(ctx, jobID)
		if err != nil || job.TalkBinding == nil {
			continue
		}
		published, err := rt.store.HasSuccessfulPublish(ctx, jobID)
		if err != nil {
			return err
		}
		if !published {
			continue
		}
		state, err := decodeTalkBinding(*job.TalkBinding)
		if err != nil {
			continue
		}
		// An imported ID matching a local job is insufficient. Its retained local
		// publication must agree on both meeting and room identity. This proof
		// digests the retained recording and execs the CLI, so it runs after the
		// cheap checks and under the per-room backoff.
		if !rt.roomEntryHasLocalLineage(ctx, job, e) {
			rt.deferRoomNameRetry(e.RoomID)
			continue
		}
		seen[e.RoomID] = true
		info := talkRoomInfo{Name: state.RoomName, Type: state.RoomType, TitleParticipants: state.TitleParticipants}
		name, kind := roomDefault(info)
		at := job.CreatedAt
		if kind != "participants" {
			lookupCtx, cancel := context.WithTimeout(ctx, talkRoomNameTimeout)
			fresh, fetchErr := rt.fetchTalkRoomName(lookupCtx, state.Owner, state.RoomToken)
			cancel()
			if fresh.Name != "" {
				name, kind = roomDefault(fresh)
				at = nowUTCString()
			}
			if fetchErr != nil {
				rt.logger.Printf("room default incomplete room_id=%s; will retry", e.RoomID)
			}
		}
		if kind == "participants" || kind == "talk-group" {
			rt.clearRoomNameRetry(e.RoomID)
		} else {
			rt.deferRoomNameRetry(e.RoomID)
		}
		if err := rt.meetingMetadata.observeRoom(ctx, e.RoomID, name, kind, at); err != nil {
			return err
		}
	}
	return nil
}

func (rt *Runtime) roomEntryHasLocalLineage(ctx context.Context, job Job, entry roomEntry) bool {
	if job.ArtifactSitePath != nil {
		if cat, ok, err := loadSiteCatalog(*job.ArtifactSitePath); err == nil && ok && len(cat.Meetings) == 1 {
			var local roomEntry
			if json.Unmarshal(cat.Meetings[0], &local) == nil && local.ID == entry.ID && local.RoomID == entry.RoomID {
				return true
			}
		}
	}
	// Nextcloud publication deletes its staging site and stores a remote
	// destination in artifact_site_path. Recover identity from the retained
	// portable archive, matching the last successful publication's digest.
	// Never derive an old room ID using today's pepper or trust an imported ID.
	if rt.cfg.WorkRoot == "" || rt.cfg.CassiniBin == "" || !validArtifactJob(job.ID) || entry.ID != job.ID {
		return false
	}
	var attempt int
	var digest, attemptPath string
	if err := rt.store.db.QueryRowContext(ctx, `SELECT attempt_number,COALESCE(artifact_opus_sha256,''),COALESCE(artifact_opus_path,'')
 FROM job_attempts WHERE job_id=? AND state='succeeded' AND publish_finished_at IS NOT NULL
 ORDER BY attempt_number DESC LIMIT 1`, job.ID).Scan(&attempt, &digest, &attemptPath); err != nil || digest == "" {
		return false
	}
	for _, file := range []string{canonicalOpusPath(rt.cfg.WorkRoot, job.ID), filepath.Join(currentRoot(rt.cfg.WorkRoot), job.ID+".json"), attemptPath} {
		if safeArtifactPath(rt.cfg.WorkRoot, file) != nil || verifySealedPublishInput(job.ID, file, digest) != nil {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, talkRoomNameTimeout)
		raw, err := exec.CommandContext(readCtx, rt.cfg.CassiniBin, "inspect", "--meeting-identity", file).Output()
		cancel()
		if err != nil {
			continue
		}
		var identity struct {
			RoomID        string `json:"roomId"`
			JobID         string `json:"jobId"`
			AttemptNumber int    `json:"attemptNumber"`
		}
		if json.Unmarshal(raw, &identity) == nil && identity.RoomID == entry.RoomID && identity.JobID == job.ID && identity.AttemptNumber == attempt {
			return true
		}
	}
	return false
}
