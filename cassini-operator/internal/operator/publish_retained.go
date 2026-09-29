package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Caller owns the legacy recording's writer lock. Never PUT audio to an evicted
// document, including a rerun whose captured audio differs from the original.
func (s *directSharesPublishSink) refreshRetained(ctx context.Context, m meetingLifecycle, local string, entry json.RawMessage, room string) (string, error) {
	if m.State != "active" {
		return "", fmt.Errorf("meeting lifecycle operation is in progress")
	}
	dir, cleanup, err := newCarryDir(upload{local: local, remote: m.Path})
	if err != nil {
		return "", err
	}
	defer cleanup()
	state, err := s.cfg.davRetentionLeaf(ctx, s.client, m.Path)
	if err != nil {
		return "", err
	}
	if !state.Exists || state.FileID != m.FileID {
		return "", fmt.Errorf("retained meeting identity changed")
	}
	delivered := filepath.Join(dir, "delivered.json")
	if _, _, _, err = s.cfg.davDownloadFile(ctx, retentionDAVClient(s.client), ncRecordingsOwner, m.Path, delivered, maxAnnotateRecordingBytes, state.ETag); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "refreshed.json")
	cmd := exec.CommandContext(ctx, s.cassiniBin, "extract", "transcription", "--out", out, "--age-anchor", m.Anchor, "--anchor-source", m.AnchorSource, "--refresh-from", delivered, local)
	cmd.Env = contextChildEnv(os.Environ())
	if output, e := cmd.CombinedOutput(); e != nil {
		return "", fmt.Errorf("refresh retained meeting: %w: %.1024s", e, output)
	}
	result, err := runAnnotateShow(ctx, s.cassiniBin, out)
	if err != nil {
		return "", err
	}
	store := s.rt.annotationReads()
	if store == nil {
		return "", fmt.Errorf("durable annotations unavailable")
	}
	if err = store.prepareAnnotationRepublish(ctx, m.Name, result); err != nil {
		return "", err
	}
	if err = s.cfg.davRetentionPut(ctx, s.client, m.Path, out, state.ETag); err != nil {
		return "", err
	}
	after, err := s.cfg.davRetentionLeaf(ctx, s.client, m.Path)
	if err != nil {
		return "", err
	}
	if !after.Exists || after.FileID != m.FileID {
		return "", fmt.Errorf("retained file identity changed during refresh")
	}
	digest, _, _, err := s.cfg.davDownloadFile(ctx, retentionDAVClient(s.client), ncRecordingsOwner, m.Path, filepath.Join(dir, "verified.json"), maxAnnotateRecordingBytes, after.ETag)
	if err != nil {
		return "", err
	}
	if digest != result.ContainerSHA256 {
		return "", fmt.Errorf("retained refresh verification failed")
	}
	if err = store.recordAnnotationDelivery(ctx, m.Name, result); err != nil {
		return "", err
	}
	if s.rt.meetingMetadata != nil {
		entry, err = overlayMeetingEntry(entry, room)
		if err != nil {
			return "", err
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(entry, &fields); err != nil {
			return "", err
		}
		delete(fields, "audioPath")
		fields["documentPath"], _ = json.Marshal("meetings/" + filepath.Base(m.Path))
		fields["representation"] = json.RawMessage(`"transcription"`)
		fields["mediaState"] = json.RawMessage(`"evicted"`)
		entry, err = json.Marshal(fields)
		if err != nil {
			return "", err
		}
		if err = s.rt.meetingMetadata.Put(ctx, m.FileID, m.Name, entry); err != nil {
			return "", err
		}
	}
	return ncRecordingsRoot, nil
}
