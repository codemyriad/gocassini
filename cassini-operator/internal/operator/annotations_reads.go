package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path"
)

func (s *annotationService) readDocument(ctx context.Context, caller, meetingID, relPath string) (annotateResult, error) {
	store := s.rt.annotationReads()
	if store == nil {
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations store unavailable", cause: fmt.Errorf("annotations store unavailable")}
	}
	// Check the current leaf permission without downloading its media bytes.
	identity := annotationReadIdentity(caller, relPath)
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
	result, err := store.document(ctx, path.Base(relPath))
	if errors.Is(err, sql.ErrNoRows) {
		var state string
		if e := store.db.QueryRowContext(ctx, `SELECT state FROM meeting_annotations WHERE opus_name=?`, path.Base(relPath)).Scan(&state); e == nil && state == annotationsStateUnavailable {
			return annotateResult{}, &annotateFailure{status: 502, public: "the recording's annotations could not be read", cause: err}
		}
		s.importDocument(caller, meetingID, relPath)
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations are preparing; retry shortly", cause: err}
	}
	if err != nil {
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations store unavailable", cause: err}
	}
	return result, nil
}

var annotationImportSlots = make(chan struct{}, 2)

func (s *annotationService) importDocument(caller, meetingID, relPath string) {
	if _, busy := s.imports.LoadOrStore(relPath, true); busy {
		return
	}
	select {
	case annotationImportSlots <- struct{}{}:
	default:
		s.imports.Delete(relPath)
		return
	}
	ctx := s.rt.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, annotateRequestTimeout)
	go func() {
		defer cancel()
		defer s.imports.Delete(relPath)
		defer func() { <-annotationImportSlots }()
		store := s.rt.annotationReads()
		if _, err := store.document(ctx, path.Base(relPath)); err == nil {
			return
		}
		// File writers and importers must agree on which delivered version was read.
		unlock, err := annotationWriteLocks.acquire(ctx, path.Base(relPath))
		if err != nil {
			return
		}
		defer unlock()
		result, err := s.showMeeting(ctx, caller, meetingID, relPath)
		if err == nil {
			err = store.Record(ctx, path.Base(relPath), result)
		}
		if err != nil {
			s.logf("annotations: import meeting=%s: %v", meetingID, err)
		}
	}()
}
