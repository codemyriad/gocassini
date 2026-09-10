package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"
)

// GET and POST annotations/meetings/<id> — one meeting's marks (D-737, design
// doc §3).
//
// Both resolve the id the way meetings-context does: through the caller's own
// filtered catalog (readableMeetingsForCaller), the one access-control path
// every read of this archive shares. An id outside that set is a 404 identical
// to an id that does not exist, so a recording someone may not read never
// reveals that it exists — and a write gets exactly the same answer, so marking
// is not a way to probe the archive either.
//
// A read is made AS THE CALLER, so Nextcloud re-checks the ACL on the bytes. A
// write cannot be: the recordings mount gives ordinary accounts a READ ceiling.
// After the same visibility check the service account rewrites the file with
// If-Match, so a concurrent writer is refused rather than overwritten, and a
// refusal starts the whole read-apply-write again from a fresh ETag.
//
// Status discipline is search's: failure is loud (502), denial is empty (404).

const (
	// annotateWriteAttempts is how many read-apply-write rounds one POST makes
	// before giving up on a meeting other writers keep changing. Ops are
	// idempotent — re-marking is a no-op and unmarking the absent is reported —
	// so every round after the first is safe.
	annotateWriteAttempts = 3

	// annotateRequestTimeout bounds one request: up to three rounds of
	// download, rewrite and upload of a recording of tens of megabytes. Finite,
	// because a request that never ends holds a connection, a staging directory
	// and the meeting's write lock.
	annotateRequestTimeout = 5 * time.Minute

	// annotateIndexTimeout bounds recording a committed write in the
	// projection. That runs detached from the request, so a caller who hangs up
	// after the PUT does not leave the projection describing the old file.
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
// its digests. The audio digest is identity and the container digest names
// bytes; both are the projection's business, not a caller's.
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

// annotateFailure is how a step says what the caller is told. public is safe to
// return; cause is for the log. A 404 carries no public text, because it is
// answered exactly as any other missing path is.
type annotateFailure struct {
	status int
	public string
	cause  error
	// committed is set when the file was already rewritten before the step
	// failed, so the projection no longer describes it.
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

// readMeeting answers GET annotations/meetings/<id>.
func (s *annotationService) readMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()

	result, err := s.showMeeting(ctx, caller, meetingID)
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
func (s *annotationService) showMeeting(ctx context.Context, caller, meetingID string) (annotateResult, error) {
	relPath, err := s.visibleRecording(ctx, caller, meetingID)
	if err != nil {
		return annotateResult{}, err
	}
	staging, err := os.MkdirTemp("", "cassini-annotations-*")
	if err != nil {
		return annotateResult{}, &annotateFailure{status: http.StatusInternalServerError, public: "annotations unavailable", cause: err}
	}
	// Every path, a cancelled request included: the directory holds a whole
	// recording, outside the access model.
	defer os.RemoveAll(staging)

	local := filepath.Join(staging, "meeting.opus")
	status, err := s.exapp.stageAnnotatedRecording(ctx, s.client, annotationReadIdentity(caller, relPath), relPath, local, maxAnnotateRecordingBytes)
	if err != nil {
		if deniedOrAbsent(status) {
			// The second gate disagreed with the catalog — the ACL changed between
			// the scan and the fetch, or the recording went. Same answer as the
			// first gate gives.
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

// writeMeeting answers POST annotations/meetings/<id>.
func (s *annotationService) writeMeeting(w http.ResponseWriter, r *http.Request, caller, meetingID string) {
	request, refusal := readAnnotateWriteRequest(w, r)
	if refusal != nil {
		writeJSONError(w, refusal.status, refusal.message)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()

	result, relPath, err := s.commitMeeting(ctx, caller, meetingID, request)
	if err != nil {
		var failure *annotateFailure
		if errors.As(err, &failure) && failure.committed {
			s.markUnavailable(ctx, path.Base(relPath), "the committed write could not be verified")
		}
		s.answerFailure(w, r, "write meeting="+meetingID, err)
		return
	}
	s.recordCommitted(ctx, meetingID, relPath, result)
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

// commitMeeting applies one batch and commits it, re-reading on every 412. It
// returns the recording's archive-relative path whenever it got that far.
func (s *annotationService) commitMeeting(ctx context.Context, caller, meetingID string, request annotateWriteRequest) (annotateResult, string, error) {
	relPath, err := s.visibleRecording(ctx, caller, meetingID)
	if err != nil {
		return annotateResult{}, "", err
	}
	ops, namespace, err := s.resolveVocabulary(ctx, request.Ops)
	if errors.Is(err, errAnnotationIndexBuilding) {
		return annotateResult{}, relPath, &annotateFailure{status: http.StatusServiceUnavailable,
			public: "the tag index is being rebuilt after a restart — try again in a few minutes", cause: err}
	}
	if err != nil {
		return annotateResult{}, relPath, &annotateFailure{status: http.StatusBadGateway, public: "the tag vocabulary is unavailable", cause: err}
	}

	release, err := annotationWriteLocks.acquire(ctx, relPath)
	if err != nil {
		return annotateResult{}, relPath, &annotateFailure{
			status: http.StatusConflict,
			public: "conflict",
			cause:  fmt.Errorf("meeting=%s: another write held it until the request ended: %w", meetingID, err),
		}
	}
	defer release()

	staging, err := os.MkdirTemp("", "cassini-annotations-*")
	if err != nil {
		return annotateResult{}, relPath, &annotateFailure{status: http.StatusInternalServerError, public: "annotations unavailable", cause: err}
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
		return result, relPath, err
	}
	return annotateResult{}, relPath, &annotateFailure{
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
		// In the caller's catalog and gone from Files: removed since the catalog
		// was read. As absent as any other absent meeting.
		return annotateResult{}, annotateNotFound(fmt.Errorf("%s is in the catalog but not in Files (served as 404)", relPath))
	}
	if state.ETag == "" {
		// Without an ETag the write could only be unconditional, which is exactly
		// the lost update If-Match is here to prevent.
		return annotateResult{}, annotateUnavailable(fmt.Errorf("inspect %s: Nextcloud reported no ETag, so it cannot be written without risking a concurrent write", relPath))
	}
	if underACL && !everyoneRuleGovernsRead(state.Rules) {
		// The D-594 state: a delivered recording every account can read. It is
		// the publish path's to repair — it denies before it touches the leaf —
		// and not this route's to refresh the content of while it stays exposed.
		return annotateResult{}, &annotateFailure{
			status: http.StatusBadGateway,
			public: "the recording's access rule could not be confirmed",
			cause:  fmt.Errorf("refusing to rewrite %s: it carries no effective %q rule; republishing the meeting repairs it", relPath, ncRecordingsEveryoneGroup),
		}
	}

	in := filepath.Join(staging, fmt.Sprintf("in-%d.opus", attempt))
	out := filepath.Join(staging, fmt.Sprintf("out-%d.opus", attempt))
	// A round that loses the race leaves nothing behind for the next one, so
	// three rounds never hold six recordings.
	defer os.Remove(in)
	defer os.Remove(out)

	status, err := s.exapp.stageAnnotatedRecording(ctx, s.client, ncRecordingsOwner, relPath, in, maxAnnotateRecordingBytes)
	if err != nil {
		if status == http.StatusNotFound {
			return annotateResult{}, annotateNotFound(fmt.Errorf("%s went between its PROPFIND and its GET (served as 404)", relPath))
		}
		return annotateResult{}, annotateUnavailable(fmt.Errorf("fetch %s: %w", relPath, err))
	}
	// What the recording holds before the batch, so that a batch which changes
	// nothing can be recognised by its result and cost no upload.
	inDigest, err := fileSHA256(in)
	if err != nil {
		return annotateResult{}, annotateUnavailable(fmt.Errorf("digest %s: %w", in, err))
	}

	result, err := runAnnotateApply(ctx, s.bin, in, out, ops, opts)
	if err != nil {
		return annotateResult{}, annotateApplyFailure(err)
	}
	// A batch that changed nothing — a retry of marks already there, an unmark
	// of one already gone — leaves the bytes as they were, and `cassini annotate`
	// says so by answering the input's own digest. Re-uploading tens of
	// megabytes would buy nothing but a new ETag, which would make every other
	// writer's next attempt retry.
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
	if err := s.verifyCommitted(ctx, relPath, info.Size(), underACL); err != nil {
		return annotateResult{}, err
	}
	return result, nil
}

// verifyCommitted is putAssetBytes's post-condition, for its two reasons.
//
// Nextcloud commits an interrupted upload as a truncated recording with the
// same fileid and ACL, and nothing downstream can tell. And a leaf that lost its
// rules between the PROPFIND and the PUT — deleted in the Files UI, restored
// from trash — became a NEW leaf when this PUT landed, with a new fileid, no
// rules, and the whole recording in it. Either way the write has already
// happened, so the only honest answer is a loud 502 and a log line an operator
// can act on — never a 200.
//
// The rule check applies in the Team folder only: in the default model's
// private root a leaf has no rules by design, and the check would fail every
// write there.
func (s *annotationService) verifyCommitted(ctx context.Context, relPath string, size int64, underACL bool) error {
	unverified := func(cause error) error {
		return &annotateFailure{status: http.StatusBadGateway, public: "the write could not be verified", cause: cause, committed: true}
	}
	state, err := s.exapp.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, relPath)
	switch {
	case err != nil:
		return unverified(fmt.Errorf("verify %s after writing it: %w", relPath, err))
	case !state.Exists:
		return unverified(fmt.Errorf("verify %s: it is not there after a successful upload", relPath))
	case state.Size != size:
		return unverified(fmt.Errorf("verify %s: Nextcloud stored %d bytes of %d — the recording is truncated; republish the meeting to restore it", relPath, state.Size, size))
	case underACL && !everyoneRuleGovernsRead(state.Rules):
		return unverified(fmt.Errorf("verify %s: it carries no effective %q rule after the write — it is readable by every account; republish the meeting to restore its rules", relPath, ncRecordingsEveryoneGroup))
	}
	return nil
}

// annotateApplyFailure maps a refusal from `cassini annotate apply` to what the
// caller is told (design doc §3).
//
// Only an invalid-ops refusal carries the CLI's own words back: they describe
// the caller's ops. The same words may quote a label, which is user content, so
// they are the one CLI message that is NOT logged. Every other message may name
// local paths, so it is logged and not returned.
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
// through the caller's own catalog, or refuses with a 404 that is the same for
// absent and unreadable.
func (s *annotationService) visibleRecording(ctx context.Context, caller, meetingID string) (string, error) {
	readable, _, ok := s.exapp.readableMeetingsForCaller(ctx, s.client, caller, s.logger)
	if !ok {
		return "", annotateUnavailable(fmt.Errorf("resolve the readable meetings of caller=%s", caller))
	}
	relPath, permitted := readable[meetingID]
	if !permitted {
		return "", annotateNotFound(fmt.Errorf("caller=%s asked for meeting=%s, which is not in their readable set (served as 404)", caller, meetingID))
	}
	return relPath, nil
}

// recordCommitted brings the projection up to date with a committed write —
// the file first, SQLite second. A failure here costs coverage and never a
// mark, since the file already carries it, so it is logged, the meeting is
// marked unavailable so the vocabulary reports partial coverage rather than a
// false complete one, and the caller still gets their 200.
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

// markUnavailable records that the projection no longer describes opusName.
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

// index is the projection, or nil where it could not be opened.
func (s *annotationService) index() annotationIndex {
	if s.rt == nil {
		return nil
	}
	return s.rt.annotations
}

// answerFailure logs a failure's cause and answers with its status. A 404 is
// answered exactly as any other missing path, so a recording the caller may not
// read and one that does not exist cannot be told apart.
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

// deniedOrAbsent reports whether an upstream status means the caller may not
// have the file, which is always answered as its absence.
func deniedOrAbsent(status int) bool {
	return status == http.StatusNotFound || status == http.StatusUnauthorized || status == http.StatusForbidden
}
