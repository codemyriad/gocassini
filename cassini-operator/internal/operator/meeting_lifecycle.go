package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

const transcriptionSuffix = ".cassini.transcription.json"

var meetingProjectionLocks keyedLocks

var errMeetingRetired = errors.New("meeting has expired")

type meetingLifecycle struct {
	Name           string `json:"name"`
	FileID         int64  `json:"fileId"`
	Path           string `json:"documentPath"`
	Representation string `json:"representation"`
	State          string `json:"state"`
	Anchor         string `json:"ageAnchor"`
	AnchorSource   string `json:"anchorSource"`
	DocumentID     string `json:"documentId"`
}

func (s *Store) ensureMeetingLifecycleSchema() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meeting_lifecycle(
 name TEXT PRIMARY KEY, file_id INTEGER NOT NULL UNIQUE, document_path TEXT NOT NULL UNIQUE,
 representation TEXT NOT NULL, state TEXT NOT NULL, age_anchor TEXT NOT NULL,
 anchor_source TEXT NOT NULL, document_id TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS remote_retention_operation(
 name TEXT PRIMARY KEY REFERENCES meeting_lifecycle(name), operation_json BLOB NOT NULL,
 status TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL);`)
	return err
}

func logicalMeetingName(name string) string {
	if path.Base(name) != name {
		return ""
	}
	if strings.HasSuffix(name, transcriptionSuffix) {
		return strings.TrimSuffix(name, transcriptionSuffix) + ".opus"
	}
	if strings.HasSuffix(name, ".opus") {
		return name
	}
	return ""
}

func (s *Store) meetingLifecycle(ctx context.Context, name string) (meetingLifecycle, bool, error) {
	var m meetingLifecycle
	if s == nil {
		return m, false, nil
	}
	err := s.db.QueryRowContext(ctx, `SELECT name,file_id,document_path,representation,state,age_anchor,anchor_source,document_id FROM meeting_lifecycle WHERE name=?`, logicalMeetingName(name)).Scan(&m.Name, &m.FileID, &m.Path, &m.Representation, &m.State, &m.Anchor, &m.AnchorSource, &m.DocumentID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// Adoption is an explicit operation after verifying managed provenance. A
// conflicting file ID/anchor cannot silently replace an existing logical record.
func (s *Store) adoptMeetingLifecycle(ctx context.Context, m meetingLifecycle) error {
	if logicalMeetingName(m.Name) != m.Name || m.FileID <= 0 || m.Representation != "opus" && m.Representation != "transcription" || m.State != "active" || m.AnchorSource == "" {
		return fmt.Errorf("invalid managed meeting lifecycle")
	}
	if err := retentionLeafPath(m.Path); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, m.Anchor); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meeting_lifecycle(name,file_id,document_path,representation,state,age_anchor,anchor_source,document_id) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(name) DO NOTHING`, m.Name, m.FileID, m.Path, m.Representation, m.State, m.Anchor, m.AnchorSource, m.DocumentID)
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

func meetingDocumentPath(document, audio string) string {
	if document != "" {
		return document
	}
	return audio
}
