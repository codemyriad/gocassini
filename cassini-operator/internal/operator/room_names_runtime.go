package operator

import (
	"context"
	"encoding/json"
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
		jobID := e.JobID
		if jobID == "" {
			jobID = e.ID
		}
		job, err := rt.store.GetJob(ctx, jobID)
		if err != nil || job.TalkBinding == nil || job.ArtifactSitePath == nil {
			continue
		}
		// An imported ID matching a local job is insufficient. Its retained local
		// publication must agree on both meeting and room identity.
		cat, ok, err := loadSiteCatalog(*job.ArtifactSitePath)
		if err != nil || !ok || len(cat.Meetings) != 1 {
			continue
		}
		var local roomEntry
		if json.Unmarshal(cat.Meetings[0], &local) != nil || local.ID != e.ID || local.RoomID != e.RoomID {
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
		if err := rt.meetingMetadata.observeRoom(ctx, e.RoomID, name, kind, at); err != nil {
			return err
		}
	}
	return nil
}
