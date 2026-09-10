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

// The POST body of annotations/meetings/<id> (D-737, design doc §3). It is
// validated before the first call to Nextcloud, so a malformed request costs no
// download and says nothing about whether the meeting exists.

const (
	// maxAnnotateBodyBytes fits two hundred of the largest marks several times
	// over, so it only refuses a body that is not a batch of marks.
	maxAnnotateBodyBytes = 64 << 10

	// maxAnnotateOps: every batch is one whole-file rewrite and re-upload, so a
	// batch is one interaction's worth of marks, not an import.
	maxAnnotateOps = 200
)

// annotateOpIDPattern mirrors portable.annotationIDRE. Checked here as well as
// by the CLI to keep a value that is not an id out of the child's argv.
var annotateOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// annotateWriteRequest is a parsed, validated POST body. It has no actor id on
// purpose: the actor is always the authenticated caller (format §1), and a body
// claiming otherwise is answered as if it had not.
type annotateWriteRequest struct {
	Ops            []json.RawMessage `json:"ops"`
	ExpectRevision *int              `json:"expectRevision"`
	ActorKind      string            `json:"actorKind"`
	OperationID    string            `json:"operationId"`
}

// badAnnotateRequest describes the caller's own body, so it is safe to return.
func badAnnotateRequest(format string, args ...any) *annotateFailure {
	message := fmt.Sprintf(format, args...)
	return &annotateFailure{status: http.StatusBadRequest, public: message, cause: errors.New(message)}
}

// readAnnotateWriteRequest reads and validates the batch's shape; whether each
// op is valid is the CLI's to say.
func readAnnotateWriteRequest(w http.ResponseWriter, r *http.Request) (annotateWriteRequest, *annotateFailure) {
	var request annotateWriteRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAnnotateBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			refusal := badAnnotateRequest("the request body is larger than the %d KiB one batch may be", maxAnnotateBodyBytes>>10)
			refusal.status = http.StatusRequestEntityTooLarge
			return request, refusal
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
		// An empty batch would still rewrite the recording and bump its revision.
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

// resolveVocabulary renders the ops document the CLI reads, giving every `mark`
// that names its tag only by label the id this installation already uses for
// it, and returns the installation's tag namespace.
//
// The CLI can only resolve a label within the file it rewrites, so "hiring" on
// two meetings would mint two ids (design doc §3, "Vocabulary resolution"). The
// lookup is among the caller's readable meetings only, so it cannot reveal a
// label on a meeting they cannot open.
//
// With no projection, labels resolve within the file: the documented degraded
// mode, logged and not refused. A projection that FAILS is refused (502):
// proceeding would write a freshly minted id or namespace into the recording
// for good on the strength of a lookup that did not happen.
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
// Anything else, a malformed op included, passes through for the CLI to judge.
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

// withMarkTagID sets tag.id on one mark op, keeping every other field as sent.
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
