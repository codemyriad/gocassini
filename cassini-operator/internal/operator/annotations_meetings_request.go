package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// The POST body of annotations/meetings/<id>, and what the operator does to it
// before the CLI sees it (D-737, design doc §3).
//
// Everything here is decided before the first call to Nextcloud, the way
// meetings-context validates its query: a malformed request costs no download,
// cannot half-run, and — because none of it depends on which meeting was named
// — a refusal here says nothing about whether that meeting exists.

const (
	// maxAnnotateBodyBytes bounds one POST body. Two hundred ops of the largest
	// shape a mark takes — a 64-character label and a time range — fit in it
	// several times over, so the bound only ever refuses a body that is not a
	// batch of marks.
	maxAnnotateBodyBytes = 64 << 10

	// maxAnnotateOps bounds one batch. Every batch is one whole-file rewrite and
	// one re-upload of the recording, so a batch is one interaction's worth of
	// marks, not an import.
	maxAnnotateOps = 200
)

// annotateOpIDPattern mirrors portable.annotationIDRE — the shape the format
// gives every id, operation ids included. The CLI validates it again; checking
// it here as well keeps a caller-supplied value that is not an id out of the
// child's argv, and makes a bad one a 400 before any download.
var annotateOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// annotateWriteRequest is a parsed, validated POST body.
//
// It has no actor id field on purpose, and unknown fields are ignored rather
// than refused: a body that claims to be written by someone else is answered
// exactly as if it had not said so, because the actor is always the
// authenticated caller (format §1) and a field the operator never reads cannot
// be the way that stops being true.
type annotateWriteRequest struct {
	Ops            []json.RawMessage `json:"ops"`
	ExpectRevision *int              `json:"expectRevision"`
	ActorKind      string            `json:"actorKind"`
	OperationID    string            `json:"operationId"`
}

// annotateRequestError is a refusal of the request itself, with the status it
// is answered with. Its message describes the caller's own body, so it is safe
// to return to them.
type annotateRequestError struct {
	status  int
	message string
}

func (e *annotateRequestError) Error() string { return e.message }

func badAnnotateRequest(format string, args ...any) *annotateRequestError {
	return &annotateRequestError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// readAnnotateWriteRequest reads and validates the body. Only the batch's shape
// is checked here; whether each op is valid is the CLI's to say, from the one
// validator the format has.
func readAnnotateWriteRequest(w http.ResponseWriter, r *http.Request) (annotateWriteRequest, *annotateRequestError) {
	var request annotateWriteRequest
	tooLarge := &annotateRequestError{
		status:  http.StatusRequestEntityTooLarge,
		message: fmt.Sprintf("the request body is larger than the %d KiB one batch may be", maxAnnotateBodyBytes>>10),
	}
	if r.ContentLength > maxAnnotateBodyBytes {
		return request, tooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAnnotateBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return request, tooLarge
		}
		return request, badAnnotateRequest("the request body could not be read")
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return request, badAnnotateRequest(`the request body must be a JSON object: {"ops":[…]}`)
	}
	switch {
	case request.Ops == nil:
		return request, badAnnotateRequest("ops is required")
	case len(request.Ops) == 0:
		// Not a no-op the operator can afford: an empty batch still rewrites and
		// re-uploads the whole recording, and bumps its revision for nothing.
		return request, badAnnotateRequest("ops must hold at least one op")
	case len(request.Ops) > maxAnnotateOps:
		return request, badAnnotateRequest("a batch holds at most %d ops, got %d", maxAnnotateOps, len(request.Ops))
	}
	switch request.ActorKind {
	case "":
		request.ActorKind = "person"
	case "person", "agent":
	default:
		return request, badAnnotateRequest(`actorKind must be "person" or "agent"`)
	}
	if request.OperationID != "" && !annotateOpIDPattern.MatchString(request.OperationID) {
		return request, badAnnotateRequest("operationId must be 1-64 letters, digits, '-' or '_', starting with a letter or digit")
	}
	if request.ExpectRevision != nil && *request.ExpectRevision < 0 {
		return request, badAnnotateRequest("expectRevision cannot be negative")
	}
	return request, nil
}

// resolveVocabulary renders the ops document the CLI reads, with every `mark`
// that names its tag only by label given the id this installation already uses
// for that label, and returns the installation's tag namespace.
//
// This is what makes one word one tag across the archive (design doc §3,
// "Vocabulary resolution"). The CLI can only resolve a label within the file it
// is rewriting; left to that, "hiring" marked on two meetings would mint two
// unrelated ids. The projection knows every file, so the lookup is made here —
// among the caller's readable meetings only, because resolving across hidden
// ones would reveal whether a label exists on a meeting they cannot open.
//
// With no projection there is nothing to resolve against: labels resolve within
// the file and the CLI keeps the file's namespace, or mints one on a first
// write. That is the documented degraded mode, so it is logged and not refused.
//
// A projection that is there but FAILS is refused instead, as a 502. The two
// look alike and are not: an absent projection is a deployment state, while a
// failing one is usually transient — and proceeding would write a freshly
// minted tag id, or a namespace that is never changed again, permanently into
// the recording on the strength of a lookup that did not happen.
func (s *annotationService) resolveVocabulary(ctx context.Context, ops []json.RawMessage, visible []string) (document []byte, namespace string, err error) {
	index := s.index()
	if index == nil {
		s.logf("annotations: no projection — labels resolve within the file only, and the file keeps its own tag namespace")
		document, err = json.Marshal(struct {
			Ops []json.RawMessage `json:"ops"`
		}{ops})
		return document, "", err
	}
	if namespace, err = index.Namespace(ctx); err != nil {
		return nil, "", fmt.Errorf("read the tag namespace: %w", err)
	}
	resolved := make([]json.RawMessage, len(ops))
	for i, op := range ops {
		label, ok := unresolvedMarkLabel(op)
		if !ok {
			resolved[i] = op
			continue
		}
		tagID, found, err := index.ResolveLabel(ctx, label, visible)
		if err != nil {
			// The label is user content, so the error is reported by position.
			return nil, "", fmt.Errorf("resolve the label of op %d: %w", i, err)
		}
		if !found {
			resolved[i] = op
			continue
		}
		if resolved[i], err = withMarkTagID(op, tagID); err != nil {
			return nil, "", fmt.Errorf("fill the tag id of op %d: %w", i, err)
		}
	}
	document, err = json.Marshal(struct {
		Ops []json.RawMessage `json:"ops"`
	}{resolved})
	return document, namespace, err
}

// unresolvedMarkLabel reports the label of a `mark` op that names no tag id.
// Anything else — including an op too malformed to read — is passed through
// untouched for the CLI to accept or refuse.
func unresolvedMarkLabel(op json.RawMessage) (string, bool) {
	var probe struct {
		Op  string `json:"op"`
		Tag *struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"tag"`
	}
	if err := json.Unmarshal(op, &probe); err != nil || probe.Op != "mark" || probe.Tag == nil {
		return "", false
	}
	if strings.TrimSpace(probe.Tag.ID) != "" || strings.TrimSpace(probe.Tag.Label) == "" {
		return "", false
	}
	return probe.Tag.Label, true
}

// withMarkTagID sets tag.id on one mark op, keeping every other field of the op
// and of its tag as the caller sent them.
func withMarkTagID(op json.RawMessage, tagID string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(op, &fields); err != nil {
		return nil, err
	}
	var tag map[string]json.RawMessage
	if err := json.Unmarshal(fields["tag"], &tag); err != nil {
		return nil, err
	}
	id, err := json.Marshal(tagID)
	if err != nil {
		return nil, err
	}
	tag["id"] = id
	if fields["tag"], err = json.Marshal(tag); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}
