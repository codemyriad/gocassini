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

// The age sources a meeting's retention can be anchored on, in the order they
// are preferred: recordedAtUtc is when the meeting happened, an instant;
// recordedAtLocal has no timezone, so its calendar date is kept without a
// local timezone conversion; createdAtUtc (when the file was packed) is the
// fallback and is required even when a recording date exists. A lifecycle row
// anchored on any of them is adopted: its age never moves again.
const (
	ageSourceRecordedAtUTC   = "recordedAtUtc"
	ageSourceRecordedAtLocal = "recordedAtLocal"
	ageSourceCreatedAtUTC    = "createdAtUtc"
)

const meetingAgeFrozenSourcesSQL = `('` + ageSourceRecordedAtUTC + `','` + ageSourceRecordedAtLocal + `','` + ageSourceCreatedAtUTC + `')`

func meetingAgeSourceFrozen(source string) bool {
	return source == ageSourceRecordedAtUTC || source == ageSourceRecordedAtLocal || source == ageSourceCreatedAtUTC
}

func setMeetingRetentionAge(m *meetingLifecycle) error {
	created, err := time.Parse(time.RFC3339Nano, m.CreatedAtUTC)
	if err != nil {
		return fmt.Errorf("invalid createdAtUtc: %w", err)
	}
	anchor, source := created, ageSourceCreatedAtUTC
	if m.RecordedAtUTC != "" {
		at, ok := parseRecordedAtUTC(m.RecordedAtUTC)
		if !ok {
			return fmt.Errorf("invalid recordedAtUtc %q", m.RecordedAtUTC)
		}
		anchor, source = at, ageSourceRecordedAtUTC
	} else if m.RecordedAtLocal != "" {
		anchor, err = time.Parse("2006-01-02T15:04:05", m.RecordedAtLocal)
		if err != nil {
			return fmt.Errorf("invalid recordedAtLocal: %w", err)
		}
		source = ageSourceRecordedAtLocal
	}
	m.Anchor, m.AnchorSource = anchor.UTC().Format(time.RFC3339Nano), source
	return nil
}

// meetingTimes is the dates part of a catalog entry and of
// `cassini inspect --meeting-times`.
type meetingTimes struct {
	CreatedAtUTC    string `json:"createdAtUtc"`
	RecordedAtLocal string `json:"recordedAtLocal"`
	RecordedAtUTC   string `json:"recordedAtUtc"`
}

func (t meetingTimes) applyTo(m *meetingLifecycle) {
	m.CreatedAtUTC, m.RecordedAtLocal, m.RecordedAtUTC = t.CreatedAtUTC, t.RecordedAtLocal, t.RecordedAtUTC
}

func (s *annotationService) loadMeetingRetentionAge(ctx context.Context, m *meetingLifecycle) error {
	// Once adopted, annotations, republishing and the recordedAtUtc backfill
	// do not reset the original age: the row's own stored dates re-derive it.
	if meetingAgeSourceFrozen(m.AnchorSource) {
		return setMeetingRetentionAge(m)
	}
	entries, err := s.exapp.meetingMetadata.EntriesFor(ctx, []int64{m.FileID})
	if err != nil {
		return err
	}
	if raw := entries[m.FileID]; len(raw) > 0 {
		var dates meetingTimes
		if err := json.Unmarshal(raw, &dates); err != nil {
			return err
		}
		dates.applyTo(m)
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
	file := filepath.Join(dir, "meeting"+meetingExtension(m.Path))
	if _, _, _, err := s.exapp.davDownloadFile(ctx, retentionDAVClient(s.client), ncRecordingsOwner, m.Path, file, 0, state.ETag); err != nil {
		return err
	}
	raw, err := exec.CommandContext(ctx, s.rt.cfg.CassiniBin, "inspect", "--meeting-times", file).Output()
	if err != nil {
		return err
	}
	var dates meetingTimes
	if err := json.Unmarshal(raw, &dates); err != nil {
		return err
	}
	dates.applyTo(m)
	return setMeetingRetentionAge(m)
}
