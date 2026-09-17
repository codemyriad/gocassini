package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
	// annotateWriteAttempts: ops are idempotent, so every round after the first
	// is safe.
	annotateWriteAttempts = 3

	// annotateRequestTimeout bounds three rounds of download, rewrite and upload
	// of a recording of tens of megabytes, while holding the meeting's lock.
	annotateRequestTimeout = 5 * time.Minute

	// annotateIndexTimeout bounds recording a committed write, which runs
	// detached from the request so a caller who hangs up after the PUT does not
	// leave the projection describing the old file.
	annotateIndexTimeout = 30 * time.Second
)

// annotationsReadResponse answers GET. Annotations and Resolved are null when
// the recording carries no marks.
type annotationsReadResponse struct {
	MeetingID   string          `json:"meetingId"`
	Revision    int             `json:"revision"`
	Annotations json.RawMessage `json:"annotations"`
	Resolved    *bool           `json:"resolved"`
}

// annotationsWriteResponse answers a committed POST: the CLI's result without
// its digests, which are the projection's business, not a caller's.
type annotationsWriteResponse struct {
	MeetingID   string          `json:"meetingId"`
	Revision    int             `json:"revision"`
	OperationID string          `json:"operationId"`
	Added       []string        `json:"added"`
	Removed     []string        `json:"removed"`
	NotFound    []string        `json:"notFound"`
	Annotations json.RawMessage `json:"annotations"`
	Resolved    *bool           `json:"resolved"`
}

// annotateFailure is what the caller is told: public is safe to return, cause
// is for the log. A 404 carries no public text.
type annotateFailure struct {
	status int
	public string
	cause  error
	// committed: the file was already rewritten, so the projection no longer
	// describes it.
	committed bool
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
	result, err := s.showMeeting(ctx, caller, meetingID, relPath)
	if err != nil {
		s.answerFailure(w, r, "read meeting="+meetingID, err)
		return
	}
	writeJSON(w, http.StatusOK, annotationsReadResponse{
		MeetingID:   meetingID,
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
		MeetingID:   meetingID,
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
	result, err := s.commitMeeting(ctx, meetingID, relPath, visible, caller, request)
	if err != nil {
		var failure *annotateFailure
		if errors.As(err, &failure) && failure.committed {
			s.markUnavailable(ctx, path.Base(relPath), "the committed write could not be verified")
		}
		return result, err
	}
	// Before recording, while the index still says which tags existed.
	s.styleNewTags(ctx, request, result)
	s.recordCommitted(ctx, meetingID, relPath, result)
	return result, nil
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
			inUse, err := store.tagInUse(ctx, id)
			if err != nil {
				s.logf("annotations: keep the colours of new tags: %v", err)
				return
			}
			if !inUse {
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

// commitMeeting applies one batch and commits it, re-reading on every 412.
func (s *annotationService) commitMeeting(ctx context.Context, meetingID, relPath string, visible []string, caller string, request annotateWriteRequest) (annotateResult, error) {
	ops, namespace, err := s.resolveVocabulary(ctx, request.Ops, visible)
	if errors.Is(err, errAnnotationIndexBuilding) {
		return annotateResult{}, &annotateFailure{status: http.StatusServiceUnavailable,
			public: "the tag index is being rebuilt after a restart — try again in a few minutes", cause: err}
	}
	if err != nil {
		return annotateResult{}, &annotateFailure{status: http.StatusBadGateway, public: "the tag vocabulary is unavailable", cause: err}
	}

	release, err := annotationWriteLocks.acquire(ctx, relPath)
	if err != nil {
		return annotateResult{}, &annotateFailure{
			status: http.StatusConflict,
			public: "conflict",
			cause:  fmt.Errorf("meeting=%s: another write held it until the request ended: %w", meetingID, err),
		}
	}
	defer release()

	staging, err := os.MkdirTemp("", "cassini-annotations-*")
	if err != nil {
		return annotateResult{}, &annotateFailure{status: http.StatusInternalServerError, public: "annotations unavailable", cause: err}
	}
	defer os.RemoveAll(staging)

	opts := annotateApplyOptions{
		// Always the authenticated caller. The body has no say in it.
		ActorID:        caller,
		ActorKind:      request.ActorKind,
		OperationID:    request.OperationID,
		TagNamespace:   namespace,
		ExpectRevision: request.ExpectRevision,
	}
	for attempt := 1; attempt <= annotateWriteAttempts; attempt++ {
		result, err := s.applyOnce(ctx, staging, attempt, relPath, ops, opts)
		if errors.Is(err, errDAVPreconditionFailed) {
			s.logf("annotations: meeting=%s changed while it was being written (attempt %d of %d) — re-reading it", meetingID, attempt, annotateWriteAttempts)
			continue
		}
		return result, err
	}
	return annotateResult{}, &annotateFailure{
		status: http.StatusConflict,
		public: "conflict",
		cause:  fmt.Errorf("meeting=%s changed under each of %d attempts to write it", meetingID, annotateWriteAttempts),
	}
}

// applyOnce is one round: read the ETag, fetch the bytes it names, apply the
// ops, and write the result only if the file still carries that ETag. A 412 is
// returned as errDAVPreconditionFailed, unwrapped, for the caller to retry.
func (s *annotationService) applyOnce(ctx context.Context, staging string, attempt int, relPath string, ops []byte, opts annotateApplyOptions) (annotateResult, error) {
	underACL := !annotationInPrivateRoot(relPath)

	state, err := s.exapp.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, relPath)
	if err != nil {
		return annotateResult{}, annotateUnavailable(fmt.Errorf("inspect %s: %w", relPath, err))
	}
	if !state.Exists {
		return annotateResult{}, annotateNotFound(fmt.Errorf("%s is in the catalog but not in Files (served as 404)", relPath))
	}
	if state.ETag == "" {
		return annotateResult{}, annotateUnavailable(fmt.Errorf("inspect %s: Nextcloud reported no ETag, so it cannot be written without risking a concurrent write", relPath))
	}
	if underACL && !everyoneRuleGovernsRead(state.Rules) {
		// The D-594 state is the publish path's to repair — it denies before it
		// touches the leaf — not this route's to rewrite while it stays exposed.
		return annotateResult{}, &annotateFailure{
			status: http.StatusBadGateway,
			public: "the recording's access rule could not be confirmed",
			cause:  fmt.Errorf("refusing to rewrite %s: it carries no effective %q rule; republishing the meeting repairs it", relPath, ncRecordingsEveryoneGroup),
		}
	}

	in := filepath.Join(staging, fmt.Sprintf("in-%d.opus", attempt))
	out := filepath.Join(staging, fmt.Sprintf("out-%d.opus", attempt))
	// So three rounds never hold six recordings.
	defer os.Remove(in)
	defer os.Remove(out)

	_, status, err := s.exapp.stageRecording(ctx, s.client, ncRecordingsOwner, relPath, in, maxAnnotateRecordingBytes)
	if err != nil {
		if status == http.StatusNotFound {
			return annotateResult{}, annotateNotFound(fmt.Errorf("%s went between its PROPFIND and its GET (served as 404)", relPath))
		}
		return annotateResult{}, annotateUnavailable(fmt.Errorf("fetch %s: %w", relPath, err))
	}
	inDigest, err := fileSHA256(in)
	if err != nil {
		return annotateResult{}, annotateUnavailable(fmt.Errorf("digest %s: %w", in, err))
	}

	result, err := runAnnotateApply(ctx, s.bin, in, out, ops, opts)
	if err != nil {
		return annotateResult{}, annotateApplyFailure(err)
	}
	// A batch that changed nothing answers the input's own digest. Re-uploading
	// would buy only a new ETag, making every other writer retry.
	if result.ContainerSHA256 != "" && result.ContainerSHA256 == inDigest {
		return result, nil
	}
	info, err := os.Stat(out)
	if err != nil {
		return annotateResult{}, &annotateFailure{
			status: http.StatusBadGateway,
			public: "the recording could not be rewritten",
			cause:  fmt.Errorf("cassini annotate apply reported success and wrote nothing: %w", err),
		}
	}

	if _, _, err := s.exapp.davPutFileIfMatch(ctx, s.client, ncRecordingsOwner, relPath, out, ncRecordingsContentType, state.ETag); err != nil {
		if errors.Is(err, errDAVPreconditionFailed) {
			return annotateResult{}, errDAVPreconditionFailed
		}
		return annotateResult{}, annotateUnavailable(fmt.Errorf("put %s: %w", relPath, err))
	}
	if err := s.exapp.verifyUploadedLeaf(ctx, s.client, relPath, info.Size(), underACL); err != nil {
		// The write has happened, so the only honest answer is a loud 502.
		return annotateResult{}, &annotateFailure{status: http.StatusBadGateway, public: "the write could not be verified",
			cause: fmt.Errorf("%w; republish the meeting to restore it", err), committed: true}
	}
	return result, nil
}

// annotateApplyFailure maps a refusal from `cassini annotate apply` to what the
// caller is told (design doc §3).
//
// Only an invalid-ops refusal returns the CLI's own words: they describe the
// caller's ops, and may quote a label, so they are the one message NOT logged.
// Every other message may name local paths, so it is logged and not returned.
func annotateApplyFailure(err error) *annotateFailure {
	switch annotateExitCode(err) {
	case annotateExitRevision:
		return &annotateFailure{status: http.StatusConflict, public: "revision-conflict", cause: err}
	case annotateExitInvalid:
		reason := "the ops are invalid"
		var cliErr *annotateCLIError
		if errors.As(err, &cliErr) && cliErr.Message != "" {
			reason = cliErr.Message
		}
		return &annotateFailure{
			status: http.StatusBadRequest,
			public: reason,
			cause:  fmt.Errorf("cassini annotate apply refused the ops as invalid (exit %d)", annotateExitInvalid),
		}
	case annotateExitUnresolved:
		return &annotateFailure{status: http.StatusConflict, public: "unresolved", cause: err}
	default:
		return &annotateFailure{status: http.StatusBadGateway, public: "the recording could not be rewritten", cause: fmt.Errorf("cassini annotate apply: %w", err)}
	}
}

// visibleRecording resolves meetingID to its recording's archive-relative path
// among the caller's visible meetings, and returns those meetings' opus names,
// the set a label may be resolved in. On false it has already answered: a
// failed resolution loudly, an unreadable meeting as absent.
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
func (s *annotationService) recordCommitted(ctx context.Context, meetingID, relPath string, result annotateResult) {
	index := s.index()
	if index == nil {
		s.logf("annotations: no projection — meeting=%s is committed and not indexed", meetingID)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), annotateIndexTimeout)
	defer cancel()
	opusName := path.Base(relPath)
	if err := index.Record(ctx, opusName, result); err != nil {
		s.logf("annotations: index meeting=%s after its write: %v — marking it unavailable", meetingID, err)
		s.markUnavailable(ctx, opusName, "the index could not record a committed write")
	}
}

func (s *annotationService) markUnavailable(ctx context.Context, opusName, reason string) {
	index := s.index()
	if index == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), annotateIndexTimeout)
	defer cancel()
	if err := index.MarkUnavailable(ctx, opusName, reason); err != nil {
		s.logf("annotations: mark %s unavailable in the projection: %v", opusName, err)
	}
}

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
