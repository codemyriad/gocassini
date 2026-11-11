package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// directSharesPublishSink writes one private file and uses built-in Nextcloud
// shares for the room audience.
type directSharesPublishSink struct{ *nextcloudFilesPublishSink }

func (s *directSharesPublishSink) Name() string { return publishSinkNextcloudFiles }

func (s *directSharesPublishSink) Deliver(ctx context.Context, d publishDelivery) (string, error) {
	if s.rt == nil || s.rt.store == nil {
		return "", fmt.Errorf("direct share publication has no job store")
	}
	if !ncAccessSubstrate.usable() {
		// A startup probe can fail during a brief Nextcloud outage. Retry once
		// at publication, when the result matters, before refusing the job.
		s.cfg.preflightDirectShares(ctx, s.rt.logger)
	}
	if !ncAccessSubstrate.usable() {
		snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
		return "", fmt.Errorf("Nextcloud recording shares are not ready (%s: %s)", snap.Step, snap.Detail)
	}

	incoming, ok, err := loadSiteCatalog(d.AttemptSitePath)
	if err != nil {
		return "", err
	}
	if !ok || len(incoming.Meetings) != 1 {
		return "", fmt.Errorf("attempt site must describe exactly one meeting")
	}
	entry := incoming.Meetings[0]
	assets, err := catalogEntryAssets(entry)
	if err != nil || len(assets) != 1 || (assets[0] != "meetings/"+d.JobID+".opus" && assets[0] != "meetings/"+d.JobID+".json") {
		return "", fmt.Errorf("attempt site does not describe the sealed recording for %s", d.JobID)
	}
	local := filepath.Join(d.AttemptSitePath, assets[0])
	info, err := os.Stat(local)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return "", fmt.Errorf("sealed recording %s is missing or empty: %v", local, err)
	}
	if want := d.AssetDigests[filepath.ToSlash(assets[0])]; want != "" {
		got, err := fileSHA256(local)
		if err != nil {
			return "", err
		}
		if got != want {
			return "", fmt.Errorf("sealed recording %s failed sha256 verification", local)
		}
	}
	root := ncRecordingsRoot
	remote := root + "/" + filepath.ToSlash(assets[0])
	for _, dir := range recordingsTreeDirs(root) {
		if err := s.cfg.davMkcol(ctx, s.client, ncRecordingsOwner, dir); err != nil {
			return "", fmt.Errorf("create private archive directory %s: %w", dir, err)
		}
	}
	release, err := annotationWriteLocks.acquire(ctx, path.Base(remote))
	if err != nil {
		return "", err
	}
	defer release()
	if err := s.cfg.meetingNotRetired(ctx, path.Base(remote)); err != nil {
		return "", err
	}

	before, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, remote)
	if err != nil {
		return "", fmt.Errorf("inspect delivered recording: %w", err)
	}
	priorSuccess, err := s.rt.store.HasSuccessfulPublish(ctx, d.JobID)
	if err != nil {
		return "", err
	}
	item := upload{local: local, remote: remote, size: info.Size()}
	// Only a refine or a summarize re-seals the same meeting.webm, so only they
	// can be sure the audio identity is unchanged. A rerun replaying speaker
	// edits re-encodes the audio like any rerun, and keeps today's rerun rule.
	if kind, edits, err := s.rt.store.AttemptSpeakerEdits(ctx, d.JobID, d.AttemptNumber); err == nil && ((edits != nil && kind == triggerKindRefine) || kind == triggerKindSummarize) {
		item.keepMarks = true
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var carried *annotateResult
	if before.Exists && s.carriesMarks(item) {
		carried, err = s.putOverDeliveredCopy(ctx, item, before)
	} else {
		err = s.putAssetBytes(ctx, item, "")
	}
	if err != nil {
		return "", err
	}
	after, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, remote)
	if err != nil || !after.Exists || after.FileID <= 0 {
		return "", fmt.Errorf("Nextcloud did not report a file ID for %s after upload: %v", remote, err)
	}
	if before.Exists && before.FileID > 0 && before.FileID != after.FileID {
		return "", fmt.Errorf("Nextcloud changed %s from file ID %d to %d; existing shares must be repaired before publication succeeds", remote, before.FileID, after.FileID)
	}
	if s.rt.meetingMetadata != nil {
		// The entry is derived metadata. No Nextcloud catalog is written.
		// Room-name overlay is retained while local catalog export exists.
		overlaid, err := overlayMeetingEntry(entry, d.RoomName, d.Audience)
		if err != nil {
			return "", err
		}
		if err := s.rt.meetingMetadata.Put(ctx, after.FileID, path.Base(remote), overlaid); err != nil {
			return "", fmt.Errorf("index recording metadata: %w", err)
		}
		// An entry whose file does not state recordedAtUtc gets the instant the
		// operator can derive, and its file is queued for it. Best-effort: a
		// failure costs the instant until the next startup pass, never the
		// meeting. Runs under the recording's write lock, so no file rewrite
		// for this meeting is in flight.
		if err := s.rt.stampPublishedRecordedAt(ctx, after.FileID, path.Base(remote)); err != nil {
			s.rt.logger.Printf("recorded-at: stamp published job=%s: %v (the startup pass will retry)", d.JobID, err)
		}
	}
	// Best-effort, like the room stamp at build time: a failed registration
	// costs the room default, never the meeting. The reconcile worker's next
	// pass retries it from the indexed entry.
	if err := s.rt.registerPublishedRoom(ctx, d.JobID, entry); err != nil {
		s.rt.logger.Printf("register room default job=%s: %v (reconciliation will retry)", d.JobID, err)
	}

	prior, err := s.rt.store.JobShareOutcome(ctx, d.JobID)
	if err != nil {
		return "", err
	}
	// A refused starter stopped the last reconciliation before anyone was
	// shared with, so finishing it cannot undo a deliberate audience edit —
	// even where an earlier publish (to another sink) makes this a republish.
	starterWasRefused := prior != nil && prior.Status == shareOutcomeStarterRefused
	if !priorSuccess || !before.Exists || starterWasRefused {
		audience, public, err := s.audienceForJob(ctx, d.JobID)
		if err != nil {
			return "", err
		}
		// Whatever the last reconciliation found, this one replaces it; one
		// that fails for another reason must not leave the old one standing.
		if err := s.rt.store.ClearJobShareOutcome(ctx, d.JobID); err != nil {
			return "", err
		}
		// The note on each share names the meeting for its recipient; the
		// facts are read now, after the entry was indexed above.
		note := s.rt.meetingFactsFor(ctx, d.JobID).shareNote(false)
		skipped, err := s.cfg.reconcileRecordingShares(ctx, s.client, remote, audience, public, sharePublish, note)
		var starterRefused *starterShareRefusedError
		switch {
		case errors.As(err, &starterRefused):
			s.recordShareOutcome(ctx, d.JobID, shareOutcomeStarterRefused, skipped)
			return "", err
		case err != nil:
			return "", err
		default:
			// Recipients Nextcloud's limit on new shares held back, the
			// starter included, do not stop the publish: the run names them,
			// and a re-share reaches them once the limit allows.
			s.recordShareOutcome(ctx, d.JobID, shareOutcomeStatus(skipped), skipped)
		}
	}
	carriedMap := map[string]annotateResult{}
	if carried != nil {
		carriedMap[remote] = *carried
	}
	s.indexDeliveredMarks(ctx, []upload{item}, carriedMap)
	return root, nil
}

// recordShareOutcome is best-effort: the shares are made either way, and a
// failed write costs the run its explanation, never the meeting.
func (s *directSharesPublishSink) recordShareOutcome(ctx context.Context, jobID, status string, skipped []skippedShare) {
	if err := s.rt.store.RecordJobShareOutcome(ctx, jobID, status, skipped); err != nil {
		s.rt.logger.Printf("ERROR: record share outcome job=%s: %v", jobID, err)
	}
}

func overlayMeetingEntry(entry json.RawMessage, roomName, audience string) (json.RawMessage, error) {
	if strings.TrimSpace(roomName) == "" && strings.TrimSpace(audience) == "" {
		return entry, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil {
		return nil, err
	}
	if strings.TrimSpace(roomName) != "" {
		name, err := json.Marshal(roomName)
		if err != nil {
			return nil, err
		}
		fields["roomName"] = name
	}
	if strings.TrimSpace(audience) != "" {
		mode, err := json.Marshal(strings.TrimSpace(audience))
		if err != nil {
			return nil, err
		}
		fields["audience"] = mode
	}
	return json.Marshal(fields)
}

func (s *directSharesPublishSink) audienceForJob(ctx context.Context, jobID string) ([]sharePrincipal, bool, error) {
	binding, ok := s.rt.talkBindingForJob(jobID)
	if !ok {
		return nil, false, fmt.Errorf("recording %s has no saved Talk owner or room audience", jobID)
	}
	job, err := s.rt.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, false, err
	}
	mode := normalizeShareAudienceOrRoom(job.AudienceMode)
	audience, captured, err := s.rt.store.JobRoomAudience(ctx, jobID)
	if err != nil {
		return nil, false, err
	}
	if !captured {
		if mode == shareAudienceAttendees {
			// No publish-time Talk lookup in attendee mode: the room's current
			// call could be the next meeting in a standing channel, and its
			// people must not receive this recording (#472 review). Share with
			// the starter only and leave a warning on the run.
			if err := s.rt.store.SetJobAudienceNotice(ctx, jobID, "No attendee list was captured; only the starter received this recording."); err != nil {
				s.rt.logger.Printf("audience notice write failed job=%s: %v", jobID, err)
			}
		} else {
			if s.rt.fetchTalkParticipants == nil {
				return nil, false, fmt.Errorf("no captured Talk audience for %s and no participant lookup is available", jobID)
			}
			audience, _, err = s.rt.resolveRecordingAudience(ctx, jobID, binding.Owner, binding.RoomToken)
			if err != nil {
				return nil, false, fmt.Errorf("resolve recording audience: %w", err)
			}
		}
	} else if mode == shareAudienceAttendees && len(audience) == 0 {
		if err := s.rt.store.SetJobAudienceNotice(ctx, jobID, "No local attendees were captured; only the starter received this recording."); err != nil {
			s.rt.logger.Printf("audience notice write failed job=%s: %v", jobID, err)
		}
	} else {
		if err := s.rt.store.SetJobAudienceNotice(ctx, jobID, ""); err != nil {
			s.rt.logger.Printf("audience notice clear failed job=%s: %v", jobID, err)
		}
	}
	// The starter is a known local account even if Talk's roster omits them.
	audience = append([]sharePrincipal{{Type: "user", ID: binding.Owner}}, audience...)
	return audience, binding.Public, nil
}
