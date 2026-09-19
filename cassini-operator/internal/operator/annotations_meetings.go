package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// GET and POST annotations/meetings/<id> — one meeting's marks (D-737, design
// doc §3).
//
// Both resolve the id through the caller's visible meetings, as annotations/tags
// and search do. An id outside that set is a 404 identical to one that does not
// exist, for a write as for a read, so marking is not a way to probe the archive.
//
// A read is made AS THE CALLER, so Nextcloud re-checks the ACL on the bytes. A
// write cannot be: the recordings mount gives ordinary accounts a READ ceiling,
// so the service account rewrites the file with If-Match.
//
// Status discipline is search's: failure is loud (502), denial is empty (404).

const (
	// annotateRequestTimeout bounds an archive attempt or an HTTP request.
	// HTTP mutations only perform access checks and a local transaction.
	annotateRequestTimeout = 5 * time.Minute

	// annotateIndexTimeout bounds detached retry/job bookkeeping on shutdown.
	annotateIndexTimeout = 30 * time.Second
)

// annotationsReadResponse answers GET. Annotations and Resolved are null when
// the recording carries no marks.
type annotationsReadResponse struct {
	StateToken  string                `json:"stateToken"`
	Sync        *annotationSyncStatus `json:"sync"`
	MeetingID   string                `json:"meetingId"`
	Revision    int                   `json:"revision"`
	Annotations json.RawMessage       `json:"annotations"`
	Resolved    *bool                 `json:"resolved"`
}

// annotationsWriteResponse answers a committed POST: the CLI's result without
// its digests, which are the projection's business, not a caller's.
type annotationsWriteResponse struct {
	StateToken  string                `json:"stateToken"`
	Sync        *annotationSyncStatus `json:"sync"`
	MeetingID   string                `json:"meetingId"`
	Revision    int                   `json:"revision"`
	OperationID string                `json:"operationId"`
	Added       []string              `json:"added"`
	Removed     []string              `json:"removed"`
	NotFound    []string              `json:"notFound"`
	Annotations json.RawMessage       `json:"annotations"`
	Resolved    *bool                 `json:"resolved"`
}

// annotateFailure is what the caller is told: public is safe to return, cause
// is for the log. A 404 carries no public text.
type annotateFailure struct {
	status int
	public string
	cause  error
}

func (f *annotateFailure) Error() string { return f.cause.Error() }
func (f *annotateFailure) Unwrap() error { return f.cause }

func annotateNotFound(cause error) *annotateFailure {
	return &annotateFailure{status: http.StatusNotFound, cause: cause}
}

func annotateUnavailable(cause error) *annotateFailure {
	return &annotateFailure{status: http.StatusBadGateway, public: "Nextcloud Files unavailable", cause: cause}
}

func (s *annotationService) readMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()

	relPath, _, ok := s.visibleRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	result, err := s.readDocument(ctx, caller, meetingID, relPath)
	if err != nil {
		s.answerFailure(w, r, "read meeting="+meetingID, err)
		return
	}
	writeJSON(w, http.StatusOK, annotationsReadResponse{
		MeetingID:  meetingID,
		StateToken: result.StateToken, Sync: result.Sync,
		Revision:    result.Revision,
		Annotations: result.Annotations,
		Resolved:    result.Resolved,
	})
}

// showMeeting reads the recording as the caller and reports what it carries.
func (s *annotationService) showMeeting(ctx context.Context, caller, meetingID, relPath string) (annotateResult, error) {
	staging, err := os.MkdirTemp("", "cassini-annotations-*")
	if err != nil {
		return annotateResult{}, &annotateFailure{status: http.StatusInternalServerError, public: "annotations unavailable", cause: err}
	}
	// Every path: the directory holds a whole recording, outside the access model.
	defer os.RemoveAll(staging)

	local := filepath.Join(staging, "meeting.opus")
	_, status, err := s.exapp.stageRecording(ctx, s.client, annotationReadIdentity(caller, relPath), relPath, local, maxAnnotateRecordingBytes)
	if err != nil {
		if deniedOrAbsent(status) {
			// The ACL changed, or the recording went, since the catalog was read.
			return annotateResult{}, annotateNotFound(fmt.Errorf("caller=%s denied meeting=%s at fetch -> %d (served as 404)", caller, meetingID, status))
		}
		return annotateResult{}, annotateUnavailable(fmt.Errorf("fetch meeting=%s as caller=%s: %w", meetingID, caller, err))
	}
	result, err := runAnnotateShow(ctx, s.bin, local)
	if err != nil {
		return annotateResult{}, &annotateFailure{
			status: http.StatusBadGateway,
			public: "the recording's annotations could not be read",
			cause:  fmt.Errorf("cassini annotate show meeting=%s: %w", meetingID, err),
		}
	}
	return result, nil
}

func (s *annotationService) writeMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	request, refusal := readAnnotateWriteRequest(w, r)
	if refusal != nil {
		writeJSONError(w, refusal.status, refusal.public)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()

	relPath, visible, ok := s.visibleRecording(ctx, w, r, caller, meetingID)
	if !ok {
		return
	}
	result, err := s.commitAndRecord(ctx, meetingID, relPath, visible, caller, request)
	if err != nil {
		s.answerFailure(w, r, "write meeting="+meetingID, err)
		return
	}
	writeJSON(w, http.StatusOK, annotationsWriteResponse{
		MeetingID:  meetingID,
		StateToken: result.StateToken, Sync: result.Sync,
		Revision:    result.Revision,
		OperationID: result.OperationID,
		Added:       nonNilStrings(result.Added),
		Removed:     nonNilStrings(result.Removed),
		NotFound:    nonNilStrings(result.NotFound),
		Annotations: result.Annotations,
		Resolved:    result.Resolved,
	})
}

// commitAndRecord is the one write path, for a batch of marks and a tag job
// alike: commit the batch, keep the colours of tags it created, index it.
func (s *annotationService) commitAndRecord(ctx context.Context, meetingID, relPath string, visible []string, caller string, request annotateWriteRequest) (annotateResult, error) {
	result, err := s.commitDocument(ctx, meetingID, relPath, visible, caller, request)
	if err == nil && !result.Replayed {
		s.styleNewTags(ctx, request, result)
	}
	return result, err
}

// styleNewTags keeps the colour a batch chose for each tag it created: one no
// indexed recording carried before. An existing tag keeps the one it has.
func (s *annotationService) styleNewTags(ctx context.Context, request annotateWriteRequest, result annotateResult) {
	store := s.rt.annotationReads()
	if len(request.TagStyles) == 0 || store == nil {
		return
	}
	marked := map[string]bool{}
	for _, op := range request.Ops {
		if _, label, ok := markOpTag(op); ok {
			marked[foldTagLabel(label)] = true
		}
	}
	ids := map[string]string{}
	for _, tag := range projectAnnotations(result.Annotations, result.Resolved, "").tags {
		ids[foldTagLabel(tag.label)] = tag.id
	}
	chosen := map[string]tagStyle{}
	for _, style := range request.TagStyles {
		label := foldTagLabel(style.Label)
		if id := ids[label]; marked[label] && id != "" {
			if slices.Contains(result.CreatedTags, id) {
				chosen[id] = tagStyle{Color: style.Color, Icon: style.Icon}
			}

		}
	}
	if len(chosen) == 0 {
		return
	}
	if err := s.styles.update(func(styles map[string]tagStyle) { maps.Copy(styles, chosen) }); err != nil {
		s.logf("annotations: keep the colours of new tags: %v", err)
	}
}

func (s *annotationService) visibleRecording(ctx context.Context, w http.ResponseWriter, r *http.Request, caller, meetingID string) (string, []string, bool) {
	entries, ok := s.exapp.resolveVisibleMeetings(ctx, w, s.client, caller, s.logger, "annotations meetings")
	if !ok {
		return "", nil, false
	}
	_, root := ncArchiveReadIdentity(caller)
	for _, entry := range entries {
		if entry.id == meetingID && strings.HasSuffix(entry.opusName, ".opus") {
			return root + "/meetings/" + entry.opusName, visibleOpusNames(entries), true
		}
	}
	s.answerFailure(w, r, "meeting="+meetingID, annotateNotFound(
		fmt.Errorf("caller=%s asked for meeting=%s, which is not in their readable set (served as 404)", caller, meetingID)))
	return "", nil, false
}

// recordCommitted brings the projection up to date with a committed write. A
// failure costs coverage, never a mark: it is logged, the meeting is marked
// unavailable, and the caller still gets their 200.
func (s *annotationService) index() annotationIndex {
	if s.rt == nil {
		return nil
	}
	return s.rt.annotations
}

// answerFailure logs a failure's cause and answers with its status; a 404 is
// answered exactly as any other missing path.
func (s *annotationService) answerFailure(w http.ResponseWriter, r *http.Request, what string, err error) {
	var failure *annotateFailure
	if !errors.As(err, &failure) {
		failure = annotateUnavailable(err)
	}
	s.logf("annotations: %s: %v", what, failure.cause)
	if failure.status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	writeJSONError(w, failure.status, failure.public)
}

func deniedOrAbsent(status int) bool {
	return status == http.StatusNotFound || status == http.StatusUnauthorized || status == http.StatusForbidden
}
