package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (s *annotationService) readDocument(ctx context.Context, caller, meetingID, relPath string) (annotateResult, error) {
	store := s.rt.annotationReads()
	if store == nil {
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations store unavailable", cause: fmt.Errorf("annotations store unavailable")}
	}
	opusName := s.exapp.recordingOriginalName(caller, relPath)
	if opusName == "" {
		return annotateResult{}, annotateUnavailable(fmt.Errorf("recording name mapping expired"))
	}
	// Check the current leaf permission without downloading its media bytes.
	identity := s.exapp.recordingReadIdentity(caller, relPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.exapp.davFileURL(identity, relPath), nil)
	if err != nil {
		return annotateResult{}, annotateUnavailable(err)
	}
	s.exapp.setAppAPIDAVHeadersForUser(req, identity)
	resp, err := s.client.Do(req)
	if err != nil {
		return annotateResult{}, annotateUnavailable(err)
	}
	drainClose(resp.Body)
	if deniedOrAbsent(resp.StatusCode) {
		return annotateResult{}, annotateNotFound(fmt.Errorf("annotation access denied: %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return annotateResult{}, annotateUnavailable(fmt.Errorf("annotation access check: %d", resp.StatusCode))
	}
	result, err := store.document(ctx, opusName)
	if errors.Is(err, sql.ErrNoRows) {
		var state string
		if e := store.db.QueryRowContext(ctx, `SELECT state FROM meeting_annotations WHERE opus_name=?`, opusName).Scan(&state); e == nil && state == annotationsStateUnavailable {
			return annotateResult{}, &annotateFailure{status: 502, public: "the recording's annotations could not be read", cause: err}
		}
		s.importDocument(caller, meetingID, opusName, relPath)
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations are preparing; retry shortly", cause: err}
	}
	if err != nil {
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations store unavailable", cause: err}
	}
	return result, nil
}

var annotationImportSlots = make(chan struct{}, 2)

// Catalog reads keep skipped imports discoverable without putting media I/O on
// the request path. Only caller-visible recordings may trigger an import.
func (s *annotationService) importListedDocuments(ctx context.Context, caller string, entries []catalogHydration) {
	store := s.rt.annotationReads()
	if store == nil {
		return
	}
	rows, err := store.db.QueryContext(ctx, `SELECT v.value FROM json_each(?) v
 LEFT JOIN annotation_head h ON h.opus_name=v.value
 LEFT JOIN meeting_annotations m ON m.opus_name=v.value
 WHERE h.opus_name IS NULL AND (m.state IS NULL OR m.state!=?)`, namesJSON(visibleOpusNames(entries)), annotationsStateUnavailable)
	if err != nil {
		s.logf("annotations: find missing imports: %v", err)
		return
	}
	missing := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		missing[name] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		s.logf("annotations: read missing imports: %v", err)
		return
	}
	_, root := ncArchiveReadIdentity(caller)
	for _, entry := range entries {
		if missing[entry.opusName] && strings.HasSuffix(entry.opusName, ".opus") {
			rel := root + "/meetings/" + entry.opusName
			if s.exapp.sharePaths != nil {
				var err error
				rel, err = s.exapp.recipientRecordingPath(ctx, s.client, caller, entry.opusName, s.exapp.meetingMetadata)
				if err != nil {
					continue
				}
			}
			s.importDocument(caller, entry.id, entry.opusName, rel)
		}
	}
}

func (s *annotationService) importDocument(caller, meetingID, opusName, relPath string) {
	if _, busy := s.imports.LoadOrStore(relPath, true); busy {
		return
	}
	ctx := s.rt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.background(func() {
		defer s.imports.Delete(relPath)
		// Keep discovered imports queued instead of dropping every recording
		// beyond the two active downloads. Shutdown cancels queued work too.
		select {
		case annotationImportSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-annotationImportSlots }()
		ctx, cancel := context.WithTimeout(ctx, annotateRequestTimeout)
		defer cancel()
		store := s.rt.annotationReads()
		if _, err := store.document(ctx, opusName); err == nil {
			return
		}
		provisionMu.RLock()
		defer provisionMu.RUnlock()
		// File writers and importers must agree on which delivered version was read.
		unlock, err := annotationWriteLocks.acquire(ctx, opusName)
		if err != nil {
			return
		}
		defer unlock()
		result, err := s.showMeeting(ctx, caller, meetingID, relPath)
		if err == nil {
			err = store.Record(ctx, opusName, result)
		} else if annotateExitCode(err) != 0 && ctx.Err() == nil {
			// Download succeeded and the CLI rejected the content. Transient DAV
			// and process-launch errors leave the document eligible for retry.
			if markErr := store.MarkUnavailable(ctx, opusName, ""); markErr != nil {
				s.logf("annotations: mark unreadable meeting=%s: %v", meetingID, markErr)
			}
		}
		if err != nil {
			s.logf("annotations: import meeting=%s: %v", meetingID, err)
		}
	}) {
		s.imports.Delete(relPath)
	}
}
