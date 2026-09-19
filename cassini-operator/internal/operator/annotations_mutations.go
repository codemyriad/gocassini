package operator

import (
	ann "cassini-annotations"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"time"
)

type annotationSyncStatus struct {
	State     string `json:"state"`
	Desired   int64  `json:"desired"`
	Confirmed int64  `json:"confirmed"`
	Error     string `json:"error,omitempty"`
}

var annotationMutationLocks keyedLocks

func (s *annotationService) commitDocument(ctx context.Context, meetingID, relPath string, visible []string, caller string, request annotateWriteRequest) (annotateResult, error) {
	store := s.rt.annotationReads()
	if store == nil {
		return annotateResult{}, &annotateFailure{status: 503, public: "annotations store unavailable", cause: errors.New("no store")}
	}
	if request.RequestID != "" && !annotateOpIDPattern.MatchString(request.RequestID) {
		return annotateResult{}, badAnnotateRequest("invalid requestId")
	}
	// Authorize against the current file permission before accepting a mutation.
	if !request.Accepted {
		if _, err := s.readDocument(ctx, caller, meetingID, relPath); err != nil {
			return annotateResult{}, err
		}
	}
	if request.RetrySync {
		if _, err := store.db.ExecContext(ctx, `UPDATE annotation_head SET blocked=0,retry_at=0,attempts=0,last_error='' WHERE opus_name=?`, path.Base(relPath)); err != nil {
			return annotateResult{}, err
		}
		s.wakeAnnotations()
		return store.document(ctx, path.Base(relPath))
	}
	// Independent of the media lock: an upload never blocks a short DB commit.
	release, err := annotationMutationLocks.acquire(ctx, store.path)
	if err != nil {
		return annotateResult{}, err
	}
	defer release()
	original, _ := json.Marshal(struct {
		Meeting string
		Request annotateWriteRequest
	}{meetingID, request})
	hash := sha256.Sum256(original)
	requestHash := hex.EncodeToString(hash[:])
	if request.RequestID != "" {
		var hash string
		var encoded []byte
		e := store.db.QueryRowContext(ctx, `SELECT request_hash,response FROM annotation_receipt WHERE caller=? AND request_id=?`, caller, request.RequestID).Scan(&hash, &encoded)
		if e == nil {
			if hash != requestHash {
				return annotateResult{}, &annotateFailure{status: 409, public: "requestId was already used for a different request", cause: errors.New("request collision")}
			}
			var replay annotateResult
			e = json.Unmarshal(encoded, &replay)
			replay.Replayed = true
			return replay, e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return annotateResult{}, e
		}
	}
	var raw []byte
	var namespace string
	if request.Accepted {
		raw, err = json.Marshal(struct {
			Ops       []json.RawMessage  `json:"ops"`
			TagStyles []annotateTagStyle `json:"tagStyles,omitempty"`
		}{request.Ops, request.TagStyles})
	} else {
		raw, namespace, err = s.resolveVocabulary(ctx, request.Ops, visible)
	}
	if err != nil {
		return annotateResult{}, &annotateFailure{status: 503, public: "tag vocabulary is preparing", cause: err}
	}
	if !request.Accepted && len(request.TagStyles) > 0 {
		var envelope struct {
			Ops []json.RawMessage `json:"ops"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return annotateResult{}, err
		}
		raw, err = json.Marshal(struct {
			Ops       []json.RawMessage  `json:"ops"`
			TagStyles []annotateTagStyle `json:"tagStyles,omitempty"`
		}{envelope.Ops, request.TagStyles})
		if err != nil {
			return annotateResult{}, err
		}
	}
	batch, err := ann.ParseBatch(raw)
	if err != nil {
		return annotateResult{}, badAnnotateRequest("%v", err)
	}
	if !request.Accepted {
		if err := store.prepareInitialTagAppearance(ctx, &batch, visible); err != nil {
			return annotateResult{}, err
		}
	}
	var result annotateResult
	err = store.inTx(ctx, func(tx *sql.Tx) error {
		name := path.Base(relPath)
		if request.RequestID != "" {
			var previousHash string
			var receipt []byte
			e := tx.QueryRowContext(ctx, `SELECT request_hash,response FROM annotation_receipt WHERE caller=? AND request_id=?`, caller, request.RequestID).Scan(&previousHash, &receipt)
			if e == nil {
				if previousHash != requestHash {
					return &annotateFailure{status: 409, public: "requestId was already used for a different request", cause: errors.New("request collision")}
				}
				return json.Unmarshal(receipt, &result)
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if err := mutateAnnotationDocument(ctx, tx, name, relPath, caller, namespace, request, batch, &result); err != nil {
			return err
		}
		if request.RequestID != "" {
			receipt, err := json.Marshal(result)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO annotation_receipt(caller,request_id,opus_name,request_hash,response,snapshot) VALUES(?,?,?,?,?,?)`, caller, request.RequestID, name, requestHash, receipt, result.Sync.Desired)
			return err
		}
		return nil
	})
	if err == nil {
		s.wakeAnnotations()
	}
	return result, err
}

// Resolve appearance once, under the mutation lock and after receipt replay.
// These are creation defaults, never restyles of an already-local definition.
func (s *annotationStore) prepareInitialTagAppearance(ctx context.Context, batch *ann.Batch, visible []string) error {
	tags, err := s.Vocabulary(ctx, visible)
	if err != nil {
		return err
	}
	batch.InitialTags = make(map[string]ann.AnnotationTag, len(tags))
	for _, tag := range tags {
		initial := ann.AnnotationTag{ID: tag.TagID, Label: tag.Label}
		if tag.Color != "" {
			color := tag.Color
			initial.Color = &color
		}
		icon := tag.Icon
		initial.Icon = &icon
		batch.InitialTags[tag.TagID] = initial
	}
	return nil
}

// mutateAnnotationDocument only uses the caller's transaction. Both single
// writes and multi-meeting batches publish documents and projections atomically.
func mutateAnnotationDocument(ctx context.Context, tx *sql.Tx, name, relPath, caller, namespace string, request annotateWriteRequest, batch ann.Batch, result *annotateResult) error {
	var data []byte
	var desired, confirmed int64
	var attempts, blocked int
	var lastError string
	var generation string
	var republishing bool
	if err := tx.QueryRowContext(ctx, `SELECT s.result_json,h.desired,h.confirmed,h.attempts,h.blocked,h.last_error,h.republish_json IS NOT NULL FROM annotation_head h JOIN annotation_snapshot s ON s.id=h.desired WHERE h.opus_name=?`, name).Scan(&data, &desired, &confirmed, &attempts, &blocked, &lastError, &republishing); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT value FROM annotations_meta WHERE key='generation'`).Scan(&generation); err != nil {
		return err
	}
	if err := json.Unmarshal(data, result); err != nil {
		return err
	}
	token := fmt.Sprintf("%s:%d", generation, desired)
	if (request.StateToken != "" && request.StateToken != token) || (request.ExpectRevision != nil && *request.ExpectRevision != result.Revision) {
		return &annotateFailure{status: 409, public: "annotations changed; reload and try again", cause: errors.New("stale annotation version")}
	}
	current, err := ann.ParseAnnotations(result.Annotations)
	if err != nil {
		return err
	}
	operation := request.OperationID
	if operation == "" {
		operation, err = ann.NewAnnotationOperationID()
		if err != nil {
			return err
		}
	}
	kind := request.ActorKind
	if kind == "" {
		kind = ann.AnnotationActorPerson
	}
	outcome, err := ann.MutateBatch(current, batch, result.DurationMS, result.AudioOpusSHA256, namespace, ann.Stamp{ActorKind: kind, ActorID: caller, OperationID: operation, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		var failure *ann.Failure
		if errors.As(err, &failure) {
			status := 400
			if failure.Code == 5 {
				status = 409
			}
			return &annotateFailure{status: status, public: err.Error(), cause: err}
		}
		return err
	}
	result.OperationID = operation
	result.Added = outcome.Added
	result.Removed = outcome.Removed
	result.NotFound = outcome.NotFound
	result.CreatedTags = nil
	result.Sync = nil
	result.StateToken = ""
	if outcome.Changed {
		for _, tag := range outcome.Doc.Tags {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM annotation_tag WHERE tag_id=?`, tag.ID).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				result.CreatedTags = append(result.CreatedTags, tag.ID)
			}
		}
		result.Annotations, err = json.Marshal(outcome.Doc)
		if err != nil {
			return err
		}
		result.Revision = outcome.Doc.Revision
		resolved := outcome.Doc.Resolved(result.AudioOpusSHA256)
		result.Resolved = &resolved
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		row, err := tx.ExecContext(ctx, `INSERT INTO annotation_snapshot(opus_name,result_json) VALUES(?,?)`, name, encoded)
		if err != nil {
			return err
		}
		desired, err = row.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE annotation_head SET desired=?,rel_path=? WHERE opus_name=?`, desired, relPath, name); err != nil {
			return err
		}
		projected := projectAnnotations(result.Annotations, result.Resolved, result.AudioOpusSHA256)
		if err := replaceAnnotationProjection(ctx, tx, name, projected, result.ContainerSHA256, annotationsStateIndexed); err != nil {
			return err
		}
	}
	result.StateToken = fmt.Sprintf("%s:%d", generation, desired)
	state := "pending"
	if attempts > 0 {
		state = "delayed"
	}
	if blocked != 0 {
		state = "blocked"
	}
	if desired == confirmed && !republishing {
		state = "saved"
	}
	result.Sync = &annotationSyncStatus{State: state, Desired: desired, Confirmed: confirmed, Error: lastError}
	return nil
}
