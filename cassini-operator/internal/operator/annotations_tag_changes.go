package operator

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Changing a tag across the meetings the caller can read (D-746): its colour
// and icon, which are the installation's and apply at once, and the rename,
// merge and delete that rewrite each of those recordings carrying it. A rewrite
// is the caller's one background job, each file on commitAndRecord, the app's
// own write path. As everywhere under annotations/, a tag none of the caller's
// meetings carries is answered exactly as one that does not exist.

const (
	maxTagChangeBodyBytes = 4 << 10

	tagJobRename = "rename"
	tagJobMerge  = "merge"
	tagJobDelete = "delete"

	tagJobRunning     = "running"
	tagJobFinished    = "finished"
	tagJobInterrupted = "interrupted"
)

type tagJobFailure struct {
	Meeting string `json:"meeting"`
	Error   string `json:"error"`
}

// tagJob lives in memory only: after a restart there is none, and since every
// op is idempotent the caller runs it again.
type tagJob struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	TagID         string          `json:"tagId"`
	Into          string          `json:"into,omitempty"`
	Actor         string          `json:"actor"`
	State         string          `json:"state"`
	Total         int             `json:"total"`
	Done          int             `json:"done"`
	Failed        []tagJobFailure `json:"failed"`
	StartedAtUTC  string          `json:"startedAtUtc"`
	FinishedAtUTC string          `json:"finishedAtUtc,omitempty"`
}

// tagJobs is each caller's current or last job, one running per caller.
// Different callers' jobs run side by side; the meeting lock serialises their
// writes to one recording.
type tagJobs struct {
	mu   sync.Mutex
	last map[string]*tagJob
}

func (j *tagJobs) snapshot(caller string) *tagJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.last[caller]
	if job == nil {
		return nil
	}
	copied := *job
	copied.Failed = append([]tagJobFailure{}, job.Failed...)
	return &copied
}

func (j *tagJobs) running(caller string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	job := j.last[caller]
	return job != nil && job.State == tagJobRunning
}

// start makes job its actor's current one, unless theirs is still running.
func (j *tagJobs) start(job *tagJob) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if current := j.last[job.Actor]; current != nil && current.State == tagJobRunning {
		return false
	}
	if j.last == nil {
		j.last = map[string]*tagJob{}
	}
	j.last[job.Actor] = job
	return true
}

func (j *tagJobs) progress(job *tagJob, failure *tagJobFailure) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job.Done++
	if failure != nil {
		job.Failed = append(job.Failed, *failure)
	}
}

func (j *tagJobs) finish(job *tagJob, state string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	job.State, job.FinishedAtUTC = state, time.Now().UTC().Format(time.RFC3339)
}

// tagScope is what a change may touch: the caller's readable meetings.
type tagScope struct {
	store   *annotationStore
	caller  string
	entries []catalogHydration
	visible []string
}

// tagEdit is the body of POST annotations/tags/<id>.
type tagEdit struct {
	Label *string `json:"label"`
	Color *string `json:"color"`
	Icon  *string `json:"icon"`
}

// routeTags serves annotations/tags and everything under it.
func (s *annotationService) routeTags(w http.ResponseWriter, r *http.Request, caller, rest string) {
	tagID, action, _ := strings.Cut(rest, "/")
	switch {
	case rest == "":
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		s.serveTags(w, r, caller)
	case rest == "job" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]*tagJob{"job": s.jobs.snapshot(caller)})
	case r.Method != http.MethodPost:
		writeMethodNotAllowed(w, http.MethodPost)
	case !annotateOpIDPattern.MatchString(tagID) || strings.Contains(action, "/"):
		http.NotFound(w, r)
	default:
		s.changeTag(w, r, caller, tagID, action)
	}
}

// changeTag answers POST annotations/tags/<id>, /merge and /delete. The body is
// judged before Nextcloud is asked anything.
func (s *annotationService) changeTag(w http.ResponseWriter, r *http.Request, caller, tagID, action string) {
	var edit tagEdit
	var merge struct {
		Into string `json:"into"`
	}
	var body any
	switch action {
	case "":
		body = &edit
	case "merge":
		body = &merge
	case "delete":
		body = &struct{}{}
	default:
		http.NotFound(w, r)
		return
	}
	if !readTagChangeBody(w, r, body) {
		return
	}
	if action == "merge" && merge.Into == "" {
		writeJSONError(w, http.StatusBadRequest, "into is required")
		return
	}
	if refusal := checkTagEdit(edit); refusal != "" {
		writeJSONError(w, http.StatusBadRequest, refusal)
		return
	}
	store := s.rt.annotationReads()
	if store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, tagIndexUnavailableMessage)
		return
	}
	entries, ok := s.exapp.resolveVisibleMeetings(r.Context(), w, s.client, caller, s.logger, "annotations tags")
	if !ok {
		return
	}
	scope := tagScope{store: store, caller: caller, entries: entries, visible: visibleOpusNames(entries)}
	tags, err := store.Vocabulary(r.Context(), scope.visible)
	if err != nil {
		s.tagIndexFailed(w, caller, err)
		return
	}
	tag, found := findTag(tags, tagID)
	if !found {
		http.NotFound(w, r)
		return
	}
	var job *tagJob
	switch action {
	case "":
		s.editTag(w, r, scope, tag, edit)
		return
	case "merge":
		into, found := findTag(tags, merge.Into)
		if !found {
			http.NotFound(w, r)
			return
		}
		if into.TagID == tag.TagID {
			writeJSONError(w, http.StatusBadRequest, "a tag cannot be merged into itself")
			return
		}
		job = s.startTagJob(w, r, scope, tagJob{Kind: tagJobMerge, TagID: tag.TagID, Into: into.TagID},
			map[string]any{"op": "merge-tag", "tagId": tag.TagID, "into": map[string]string{"id": into.TagID, "label": into.Label}})
	case "delete":
		job = s.startTagJob(w, r, scope, tagJob{Kind: tagJobDelete, TagID: tag.TagID},
			map[string]any{"op": "unmark-tag", "tagId": tag.TagID})
	}
	if job != nil {
		writeJSON(w, http.StatusAccepted, map[string]*tagJob{"job": job})
	}
}

// editTag saves a colour or icon at once, for everyone, and starts a rename
// job for a new label.
func (s *annotationService) editTag(w http.ResponseWriter, r *http.Request, scope tagScope, tag tagVocabularyEntry, edit tagEdit) {
	rename := ""
	if edit.Label != nil && strings.TrimSpace(*edit.Label) != tag.Label {
		rename = strings.TrimSpace(*edit.Label)
		// Among the caller's meetings only: a wider check would say the label
		// is in use somewhere they cannot read (design doc §3).
		owner, taken, err := scope.store.labelOwner(r.Context(), rename, tag.TagID, scope.visible)
		if err != nil {
			s.tagIndexFailed(w, scope.caller, err)
			return
		}
		if taken {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "label-exists", "tagId": owner})
			return
		}
		if s.jobs.running(scope.caller) {
			writeJSONError(w, http.StatusConflict, "busy")
			return
		}
	}
	if edit.Color != nil || edit.Icon != nil {
		err := s.styles.update(func(styles map[string]tagStyle) {
			style := styles[tag.TagID]
			if edit.Color != nil {
				style.Color = *edit.Color
			}
			if edit.Icon != nil {
				style.Icon = *edit.Icon
			}
			styles[tag.TagID] = style.changedBy(scope.caller)
		})
		if err != nil {
			s.logf("annotations: save the style of %s: %v", tag.TagID, err)
			writeJSONError(w, http.StatusInternalServerError, "the tag's colour could not be saved")
			return
		}
	}
	var job *tagJob
	if rename != "" {
		if job = s.startTagJob(w, r, scope, tagJob{Kind: tagJobRename, TagID: tag.TagID},
			map[string]any{"op": "relabel", "tagId": tag.TagID, "label": rename}); job == nil {
			return
		}
		tag.Label = rename
	}
	tags := []tagVocabularyEntry{tag}
	s.withStyles(tags)
	writeJSON(w, http.StatusOK, map[string]any{"tag": tags[0], "job": job})
}

// startTagJob starts op over each of the caller's recordings carrying the tag,
// or answers why not and returns nil.
func (s *annotationService) startTagJob(w http.ResponseWriter, r *http.Request, scope tagScope, job tagJob, op any) *tagJob {
	targets, err := scope.store.tagCarriers(r.Context(), job.TagID, scope.visible)
	if err != nil {
		s.tagIndexFailed(w, scope.caller, err)
		return nil
	}
	raw, err := json.Marshal(op)
	if err == nil {
		job.ID, err = newTagJobID()
	}
	if err != nil {
		s.logf("annotations: start a %s job: %v", job.Kind, err)
		writeJSONError(w, http.StatusInternalServerError, "the job could not be started")
		return nil
	}
	job.Actor, job.State, job.Total, job.Failed = scope.caller, tagJobRunning, len(targets), []tagJobFailure{}
	job.StartedAtUTC = time.Now().UTC().Format(time.RFC3339)
	if !s.jobs.start(&job) {
		writeJSONError(w, http.StatusConflict, "busy")
		return nil
	}
	titles := make(map[string]string, len(scope.entries))
	for _, entry := range scope.entries {
		titles[entry.opusName] = entry.title
	}
	go s.runTagJob(&job, targets, raw, titles)
	return s.jobs.snapshot(scope.caller)
}

// runTagJob applies op to each recording in turn, as the job's actor. A
// failure is recorded and the job goes on.
func (s *annotationService) runTagJob(job *tagJob, targets []string, op json.RawMessage, titles map[string]string) {
	// The process's context, not the request's: the job outlives the POST.
	base := s.rt.ctx
	request := annotateWriteRequest{Ops: []json.RawMessage{op}, ActorKind: "person", OperationID: job.ID}
	_, root := ncArchiveReadIdentity(job.Actor)
	state := tagJobFinished
	for _, name := range targets {
		if base.Err() != nil {
			state = tagJobInterrupted
			break
		}
		ctx, cancel := context.WithTimeout(base, annotateRequestTimeout)
		_, err := s.commitAndRecord(ctx, name, root+"/meetings/"+name, nil, job.Actor, request)
		cancel()
		var failure *tagJobFailure
		if err != nil {
			s.logf("annotations: %s job %s: %s: %v", job.Kind, job.ID, name, err)
			failure = &tagJobFailure{Meeting: cmp.Or(titles[name], name), Error: tagJobError(err)}
		}
		s.jobs.progress(job, failure)
	}
	if state == tagJobFinished {
		s.settleTagStyles(base, job)
	}
	s.jobs.finish(job, state)
}

// settleTagStyles follows a finished job into tag-styles.json: a rename or
// merge is attributed on the tag that carries on, and a tag no indexed
// recording carries any more — anyone's, not only the caller's — loses its
// entry.
func (s *annotationService) settleTagStyles(ctx context.Context, job *tagJob) {
	gone := false
	if job.Kind != tagJobRename {
		inUse, err := s.rt.annotationReads().tagInUse(ctx, job.TagID)
		if err != nil {
			s.logf("annotations: after %s job %s: %v", job.Kind, job.ID, err)
		}
		gone = err == nil && !inUse
	}
	if job.Kind == tagJobDelete && !gone {
		return
	}
	err := s.styles.update(func(styles map[string]tagStyle) {
		switch job.Kind {
		case tagJobRename:
			styles[job.TagID] = styles[job.TagID].changedBy(job.Actor)
		case tagJobMerge:
			styles[job.Into] = styles[job.Into].changedBy(job.Actor)
		}
		if gone {
			delete(styles, job.TagID)
		}
	})
	if err != nil {
		s.logf("annotations: after %s job %s: %v", job.Kind, job.ID, err)
	}
}

// tagJobError is the public text the meetings POST would answer, never the
// logged cause.
func tagJobError(err error) string {
	var failure *annotateFailure
	switch {
	case errors.As(err, &failure) && failure.status == http.StatusNotFound:
		return "the recording is not in Files"
	case failure != nil && failure.public != "":
		return failure.public
	}
	return "the recording could not be rewritten"
}

func (s *annotationService) tagIndexFailed(w http.ResponseWriter, caller string, err error) {
	s.logf("annotations tags: caller=%s: %v", caller, err)
	writeJSONError(w, http.StatusBadGateway, tagIndexUnreadableMessage)
}

func findTag(tags []tagVocabularyEntry, tagID string) (tagVocabularyEntry, bool) {
	for _, tag := range tags {
		if tag.TagID == tagID {
			return tag, true
		}
	}
	return tagVocabularyEntry{}, false
}

// readTagChangeBody decodes one JSON object strictly: a field a route does not
// know, or anything after the object, is refused. No body is an empty object.
func readTagChangeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTagChangeBodyBytes))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeJSONError(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		return false
	case err != nil:
		writeJSONError(w, http.StatusBadRequest, "the request body could not be read")
		return false
	case len(bytes.TrimSpace(raw)) == 0:
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "the body must be a JSON object with only the documented fields")
		return false
	}
	return true
}

// checkTagEdit refuses a colour, icon or label no tag may have. The label rule
// is the format's, checked once here rather than by the CLI once per file.
func checkTagEdit(edit tagEdit) string {
	switch {
	case edit.Color != nil && !tagColors[*edit.Color]:
		return "color is not a palette colour"
	case edit.Icon != nil && !tagIcons[*edit.Icon]:
		return "icon is not an icon id"
	case edit.Label == nil:
		return ""
	}
	label := strings.TrimSpace(*edit.Label)
	if n := utf8.RuneCountInString(label); n == 0 || n > maxTagParamRunes {
		return fmt.Sprintf("label must be 1-%d characters", maxTagParamRunes)
	}
	if strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return "label must not contain control characters"
	}
	return ""
}

// newTagJobID mints a job's id, which is also the operation id on every
// recording it rewrites.
func newTagJobID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("mint a job id: %w", err)
	}
	return "op_" + hex.EncodeToString(buf[:]), nil
}
