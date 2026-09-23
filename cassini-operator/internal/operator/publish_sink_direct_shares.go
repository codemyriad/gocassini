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

// directSharesPublishSink is the single-share-model replacement for the Team
// folder ACL sink. It uses the private service-account tree and Nextcloud's
// built-in file shares. The old sink remains in the package only while its
// storage-mode migration is being removed.
type directSharesPublishSink struct{ *nextcloudFilesPublishSink }

func (s *directSharesPublishSink) Name() string { return publishSinkNextcloudFiles }

func (s *directSharesPublishSink) Deliver(ctx context.Context, d publishDelivery) (string, error) {
	if s.rt == nil || s.rt.store == nil {
		return "", fmt.Errorf("direct share publication has no job store")
	}
	if s.cfg.sharePaths != nil && !ncAccessSubstrate.usable() {
		snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
		return "", fmt.Errorf("Nextcloud recording shares are not ready (%s: %s)", snap.Step, snap.Detail)
	}
	// A mounted Team folder at this name could turn an owner-private write
	// into a container grant. Refuse it when the setup probe has found one.
	if probe, ok := ncAccessSubstrate.lastProbe(); ok && probe.DefaultRootShadowed {
		return "", fmt.Errorf("a Team folder shadows the private recordings archive")
	}
	provisionMu.Lock()
	defer provisionMu.Unlock()

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
	root := ncDefaultRecordingsRoot
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
		carried, err = s.putOverDeliveredCopy(ctx, item, before, false)
	} else {
		err = s.putAssetBytes(ctx, item, false, "")
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
	if !priorSuccess || !before.Exists {
		audience, public, err := s.audienceForJob(ctx, d.JobID)
		if err != nil {
			return "", err
		}
		if err := s.cfg.reconcileRecordingShares(ctx, s.client, remote, audience, public); err != nil {
			return "", err
		}
	}
	if s.rt.meetingMetadata != nil {
		// The entry is derived metadata. No Nextcloud catalog is written.
		// Room-name overlay is retained while local catalog export exists.
		overlaid, err := overlayMeetingEntry(entry, d.RoomName)
		if err != nil {
			return "", err
		}
		if err := s.rt.meetingMetadata.Put(ctx, after.FileID, path.Base(remote), overlaid); err != nil {
			s.logf("meeting metadata index: %s: %v", remote, err)
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
	audience = append(audience, aclMapping{Type: "user", ID: binding.Owner})
	return audience, binding.Public, nil
}
