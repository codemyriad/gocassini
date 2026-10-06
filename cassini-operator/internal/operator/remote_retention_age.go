package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// recordedAtLocal has no timezone: retain its calendar date without a local
// timezone conversion. createdAtUtc is required even when the recording date exists.
func setMeetingRetentionAge(m *meetingLifecycle) error {
	created, err := time.Parse(time.RFC3339Nano, m.CreatedAtUTC)
	if err != nil {
		return fmt.Errorf("invalid createdAtUtc: %w", err)
	}
	anchor, source := created, "createdAtUtc"
	if m.RecordedAtLocal != "" {
		anchor, err = time.Parse("2006-01-02T15:04:05", m.RecordedAtLocal)
		if err != nil {
			return fmt.Errorf("invalid recordedAtLocal: %w", err)
		}
		source = "recordedAtLocal"
	}
	m.Anchor, m.AnchorSource = anchor.UTC().Format(time.RFC3339Nano), source
	return nil
}

func (s *annotationService) loadMeetingRetentionAge(ctx context.Context, m *meetingLifecycle) error {
	// Once adopted, annotations and republishing do not reset the original age.
	if m.AnchorSource == "createdAtUtc" || m.AnchorSource == "recordedAtLocal" {
		return setMeetingRetentionAge(m)
	}
	entries, err := s.exapp.meetingMetadata.EntriesFor(ctx, []int64{m.FileID})
	if err != nil {
		return err
	}
	if raw := entries[m.FileID]; len(raw) > 0 {
		var dates struct {
			CreatedAtUTC    string `json:"createdAtUtc"`
			RecordedAtLocal string `json:"recordedAtLocal"`
		}
		if err := json.Unmarshal(raw, &dates); err != nil {
			return err
		}
		m.CreatedAtUTC, m.RecordedAtLocal = dates.CreatedAtUTC, dates.RecordedAtLocal
		if dates.CreatedAtUTC != "" {
			return setMeetingRetentionAge(m)
		}
	}
	// Older catalog entries omit these fields. Read the delivered file through
	// the format-owning CLI, never substitute job dates or filename guesses.
	state, err := s.exapp.davRetentionLeaf(ctx, s.client, m.Path)
	if err != nil {
		return err
	}
	if !state.Exists || state.FileID != m.FileID || !strongDAVETag(state.ETag) {
		return fmt.Errorf("meeting identity unavailable")
	}
	dir, err := os.MkdirTemp("", "cassini-retention-age-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "meeting.opus")
	if _, _, _, err := s.exapp.davDownloadFile(ctx, retentionDAVClient(s.client), ncRecordingsOwner, m.Path, file, 0, state.ETag); err != nil {
		return err
	}
	raw, err := exec.CommandContext(ctx, s.rt.cfg.CassiniBin, "inspect", "--meeting-times", file).Output()
	if err != nil {
		return err
	}
	var dates struct {
		CreatedAtUTC    string `json:"createdAtUtc"`
		RecordedAtLocal string `json:"recordedAtLocal"`
	}
	if err := json.Unmarshal(raw, &dates); err != nil {
		return err
	}
	m.CreatedAtUTC, m.RecordedAtLocal = dates.CreatedAtUTC, dates.RecordedAtLocal
	return setMeetingRetentionAge(m)
}
