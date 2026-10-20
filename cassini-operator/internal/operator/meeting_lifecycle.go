package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"time"
)

var meetingProjectionLocks keyedLocks

var errMeetingRetired = errors.New("meeting has expired")

type meetingLifecycle struct {
	CreatedAtUTC    string `json:"createdAtUtc"`
	RecordedAtLocal string `json:"recordedAtLocal"`
	Name            string `json:"name"`
	FileID          int64  `json:"fileId"`
	Path            string `json:"documentPath"`
	State           string `json:"state"`
	Anchor          string `json:"ageAnchor"`
	AnchorSource    string `json:"anchorSource"`
	// Reason distinguishes why a tombstone exists: "expired" for retention
	// expiry, "deleted" for a user-initiated delete. Legacy rows carry "".
	Reason string `json:"reason,omitempty"`
}

// retireReasonLabel is the user-facing word for a tombstoned meeting.
// Rows written before the reason column existed are retention expiries.
func retireReasonLabel(reason string) string {
	if reason == meetingRetireReasonDeleted {
		return meetingRetireReasonDeleted
	}
	return meetingRetireReasonExpired
}

const (
	meetingRetireReasonExpired = "expired"
	meetingRetireReasonDeleted = "deleted"
)

func (s *Store) ensureMeetingLifecycleSchema() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meeting_lifecycle(
 name TEXT PRIMARY KEY, file_id INTEGER NOT NULL UNIQUE, document_path TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL, age_anchor TEXT NOT NULL,
 anchor_source TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS remote_retention_operation(
 name TEXT PRIMARY KEY REFERENCES meeting_lifecycle(name), operation_json BLOB NOT NULL,
 status TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL);`)
	if err != nil {
		return err
	}
	for _, column := range []string{"created_at_utc", "recorded_at_local", "reason"} {
		var present int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('meeting_lifecycle') WHERE name=?`, column).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			if _, err := s.db.Exec(`ALTER TABLE meeting_lifecycle ADD COLUMN ` + column + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	return nil
}

func logicalMeetingName(name string) string {
	if path.Base(name) != name {
		return ""
	}
	if isMeetingFile(name) {
		return name
	}
	return ""
}

func (s *Store) meetingLifecycle(ctx context.Context, name string) (meetingLifecycle, bool, error) {
	var m meetingLifecycle
	if s == nil {
		return m, false, nil
	}
	err := s.db.QueryRowContext(ctx, `SELECT name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason FROM meeting_lifecycle WHERE name=?`, logicalMeetingName(name)).Scan(&m.Name, &m.FileID, &m.Path, &m.State, &m.Anchor, &m.AnchorSource, &m.CreatedAtUTC, &m.RecordedAtLocal, &m.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// Adoption is an explicit operation after verifying managed provenance. A
// conflicting file ID/anchor cannot silently replace an existing logical record.
func (s *Store) adoptMeetingLifecycle(ctx context.Context, m meetingLifecycle) error {
	if logicalMeetingName(m.Name) != m.Name || m.FileID <= 0 || m.State != "active" || m.AnchorSource == "" {
		return fmt.Errorf("invalid managed meeting lifecycle")
	}
	if err := retentionLeafPath(m.Path); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, m.Anchor); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_lifecycle(name,file_id,document_path,state,age_anchor,anchor_source,created_at_utc,recorded_at_local,reason) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO NOTHING`, m.Name, m.FileID, m.Path, m.State, m.Anchor, m.AnchorSource, m.CreatedAtUTC, m.RecordedAtLocal, m.Reason)
	if err != nil {
		return err
	}
	current, _, err := s.meetingLifecycle(ctx, m.Name)
	if err != nil {
		return err
	}
	if current.FileID != m.FileID || current.Anchor != m.Anchor {
		return fmt.Errorf("managed meeting identity or original age conflicts")
	}
	return nil
}

func (c ExAppConfig) currentOwnerMeetingPath(ctx context.Context, name string) (string, error) {
	name = logicalMeetingName(name)
	if name == "" {
		return "", fmt.Errorf("invalid meeting name")
	}
	m, ok, err := c.lifecycle.meetingLifecycle(ctx, name)
	if err != nil {
		return "", err
	}
	if ok {
		if m.State == "retiring" || m.State == "retired" {
			return "", errMeetingRetired
		}
		return m.Path, nil
	}
	return ncRecordingsRoot + "/meetings/" + name, nil
}

func (c ExAppConfig) meetingNotRetired(ctx context.Context, name string) error {
	_, err := c.currentOwnerMeetingPath(ctx, name)
	return err
}

// retiredMeetingReason answers why name is tombstoned — "expired" or
// "deleted" — and "" while it is still served.
func (c ExAppConfig) retiredMeetingReason(ctx context.Context, name string) string {
	m, ok, err := c.lifecycle.meetingLifecycle(ctx, logicalMeetingName(name))
	if err != nil || !ok || (m.State != "retiring" && m.State != "retired") {
		return ""
	}
	return retireReasonLabel(m.Reason)
}
