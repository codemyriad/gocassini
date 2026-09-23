package operator

import (
	"context"
	"encoding/json"
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
	if err != nil || len(assets) != 1 || assets[0] != "meetings/"+d.JobID+".opus" {
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

	before, err := s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, remote)
	if err != nil {
		return "", fmt.Errorf("inspect delivered recording: %w", err)
	}
	priorSuccess, err := s.rt.store.HasSuccessfulPublish(ctx, d.JobID)
	if err != nil {
		return "", err
	}
	item := upload{local: local, remote: remote, size: info.Size()}
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
		overlaid, err := overlayMeetingEntry(entry, d.RoomName)
		if err != nil {
			return "", err
		}
		if err := s.rt.meetingMetadata.Put(ctx, after.FileID, path.Base(remote), overlaid); err != nil {
			return "", fmt.Errorf("index recording metadata: %w", err)
		}
	}
	if !priorSuccess || !before.Exists {
		audience, public, err := s.audienceForJob(ctx, d.JobID)
		if err != nil {
			return "", err
		}
		if err := s.cfg.reconcileRecordingShares(ctx, s.client, remote, audience, public); err != nil {
			return "", err
		}
	}
	carriedMap := map[string]annotateResult{}
	if carried != nil {
		carriedMap[remote] = *carried
	}
	s.indexDeliveredMarks(ctx, []upload{item}, carriedMap)
	return root, nil
}

func overlayMeetingEntry(entry json.RawMessage, roomName string) (json.RawMessage, error) {
	if strings.TrimSpace(roomName) == "" {
		return entry, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil {
		return nil, err
	}
	name, err := json.Marshal(roomName)
	if err != nil {
		return nil, err
	}
	fields["roomName"] = name
	return json.Marshal(fields)
}

func (s *directSharesPublishSink) audienceForJob(ctx context.Context, jobID string) ([]aclMapping, bool, error) {
	binding, ok := s.rt.talkBindingForJob(jobID)
	if !ok {
		return nil, false, fmt.Errorf("recording %s has no saved Talk owner or room audience", jobID)
	}
	audience, captured, err := s.rt.store.JobRoomAudience(ctx, jobID)
	if err != nil {
		return nil, false, err
	}
	if !captured {
		if s.rt.fetchTalkParticipants == nil {
			return nil, false, fmt.Errorf("no captured Talk audience for %s and no participant lookup is available", jobID)
		}
		audience, _, err = s.rt.resolveRecordingAudience(ctx, jobID, binding.Owner, binding.RoomToken)
		if err != nil {
			return nil, false, fmt.Errorf("resolve recording audience: %w", err)
		}
	}
	// The starter is a known local account even if Talk's roster omits them.
	audience = append([]aclMapping{{Type: "user", ID: binding.Owner}}, audience...)
	return audience, binding.Public, nil
}
