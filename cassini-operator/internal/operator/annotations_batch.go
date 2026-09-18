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
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
)

const maxAnnotationBatchMeetings = 100

type annotationBatchRequest struct {
	annotateWriteRequest
	MeetingIDs []string `json:"meetingIds"`
}

type annotationBatchResponse struct {
	Results []annotationsWriteResponse `json:"results"`
	Tags    []tagVocabularyEntry       `json:"tags"`
}

func (s *annotationService) writeAnnotationBatch(w http.ResponseWriter, r *http.Request, caller string) {
	var request annotationBatchRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAnnotateBodyBytes))
	if err != nil {
		writeJSONError(w, 413, "annotation batch is too large")
		return
	}
	if err = json.Unmarshal(body, &request); err != nil {
		writeJSONError(w, 400, "invalid annotation batch")
		return
	}
	checked, refusal := validateAnnotateWriteRequest(request.annotateWriteRequest)
	if refusal != nil {
		s.answerFailure(w, r, "batch", refusal)
		return
	}
	request.annotateWriteRequest = checked
	if !annotateOpIDPattern.MatchString(request.RequestID) || request.RetrySync || request.StateToken != "" || request.ExpectRevision != nil {
		writeJSONError(w, 400, "batch requires requestId and does not accept retrySync or a shared revision precondition")
		return
	}
	if len(request.MeetingIDs) == 0 || len(request.MeetingIDs) > maxAnnotationBatchMeetings {
		writeJSONError(w, 400, fmt.Sprintf("select between 1 and %d meetings", maxAnnotationBatchMeetings))
		return
	}
	seen := map[string]bool{}
	for _, id := range request.MeetingIDs {
		if !isPlainMeetingID(id) || seen[id] {
			writeJSONError(w, 400, "meetingIds must contain unique valid meeting IDs")
			return
		}
		seen[id] = true
	}
	// This endpoint is for applying/removing whole-meeting tags to a selection.
	opsBody, _ := json.Marshal(struct {
		Ops []json.RawMessage `json:"ops"`
	}{request.Ops})
	parsed, err := ann.ParseOps(opsBody)
	if err != nil {
		s.answerFailure(w, r, "batch", badAnnotateRequest("%v", err))
		return
	}
	for _, op := range parsed {
		if op.Op == "mark" && (op.Tag == nil || (op.Tag.ID == "" && op.Tag.Label == nil)) {
			writeJSONError(w, 400, "mark requires a tag ID or label")
			return
		}
		if (op.Op != "mark" && op.Op != "unmark-tag") || op.Target == nil || op.Target.Kind != ann.AnnotationTargetMeeting {
			writeJSONError(w, 400, "batch supports only whole-meeting mark and unmark-tag operations")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), annotateRequestTimeout)
	defer cancel()
	if s.rt.annotationReads() == nil {
		writeJSONError(w, 503, "annotations store unavailable")
		return
	}
	entries, ok := s.exapp.resolveVisibleMeetings(ctx, w, s.client, caller, s.logger, "annotations batch")
	if !ok {
		return
	}
	_, root := ncArchiveReadIdentity(caller)
	paths := make(map[string]string, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.opusName, ".opus") {
			paths[entry.id] = root + "/meetings/" + entry.opusName
		}
	}
	for _, id := range request.MeetingIDs {
		if paths[id] == "" {
			http.NotFound(w, r)
			return
		}
	}
	// Bound remote permission checks, and finish all network access before taking
	// the mutation lock or beginning the transaction. Imports may return 503.
	errs := make([]error, len(request.MeetingIDs))
	var wg sync.WaitGroup
	jobs := make(chan int)
	for i := 0; i < min(4, len(request.MeetingIDs)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				id := request.MeetingIDs[index]
				_, errs[index] = s.readDocument(ctx, caller, id, paths[id])
			}
		}()
	}
	for i := range request.MeetingIDs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			s.answerFailure(w, r, "batch access", err)
			return
		}
	}
	response, err := s.commitAnnotationBatch(ctx, caller, request, paths, visibleOpusNames(entries))
	if err != nil {
		s.answerFailure(w, r, "batch", err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *annotationService) commitAnnotationBatch(ctx context.Context, caller string, request annotationBatchRequest, paths map[string]string, visible []string) (annotationBatchResponse, error) {
	store := s.rt.annotationReads()
	var response annotationBatchResponse
	release, err := annotationMutationLocks.acquire(ctx, store.path)
	if err != nil {
		return response, err
	}
	defer release()
	encoded, _ := json.Marshal(request)
	sum := sha256.Sum256(encoded)
	hash := hex.EncodeToString(sum[:])
	var previousHash string
	var receipt []byte
	err = store.db.QueryRowContext(ctx, `SELECT request_hash,response FROM annotation_batch_receipt WHERE caller=? AND request_id=?`, caller, request.RequestID).Scan(&previousHash, &receipt)
	if err == nil {
		if previousHash != hash {
			return response, &annotateFailure{status: 409, public: "requestId was already used for a different batch", cause: errors.New("batch request collision")}
		}
		err = json.Unmarshal(receipt, &response)
		return response, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return response, err
	}
	raw, namespace, err := s.resolveVocabulary(ctx, request.Ops, visible)
	if err != nil {
		return response, &annotateFailure{status: 503, public: "tag vocabulary is preparing", cause: err}
	}
	var envelope struct {
		Ops []json.RawMessage `json:"ops"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return response, err
	}
	raw, err = json.Marshal(struct {
		Ops       []json.RawMessage  `json:"ops"`
		TagStyles []annotateTagStyle `json:"tagStyles,omitempty"`
	}{envelope.Ops, request.TagStyles})
	if err != nil {
		return response, err
	}
	batch, err := ann.ParseBatch(raw)
	if err != nil {
		return response, badAnnotateRequest("%v", err)
	}
	if namespace == "" {
		namespace, err = ann.NewNamespace()
		if err != nil {
			return response, err
		}
	}
	// New labels share one identity across the entire batch, even in an empty
	// installation. Explicit IDs have already been checked against visibility.
	newIDs := map[string]string{}
	for i := range batch.Ops {
		if batch.Ops[i].Op == "mark" && batch.Ops[i].Tag.ID == "" {
			label := foldTagLabel(*batch.Ops[i].Tag.Label)
			id := newIDs[label]
			if id == "" {
				id, err = ann.NewAnnotationTagID()
				if err != nil {
					return response, err
				}
				newIDs[label] = id
			}
			batch.Ops[i].Tag.ID = id
		}
	}
	results := make([]annotateResult, len(request.MeetingIDs))
	response.Results = make([]annotationsWriteResponse, 0, len(results))
	response.Tags = []tagVocabularyEntry{}
	err = store.inTx(ctx, func(tx *sql.Tx) error {
		tags := map[string]bool{}
		for i, id := range request.MeetingIDs {
			if err := mutateAnnotationDocument(ctx, tx, path.Base(paths[id]), paths[id], caller, namespace, request.annotateWriteRequest, batch, &results[i]); err != nil {
				return err
			}
			result := results[i]
			response.Results = append(response.Results, annotationWriteResponse(id, result))
			var doc projectedDocument
			if err := json.Unmarshal(result.Annotations, &doc); err != nil {
				return err
			}
			for _, tag := range doc.Tags {
				if !tags[tag.ID] {
					tags[tag.ID] = true
					entry := tagVocabularyEntry{TagID: tag.ID, Label: tag.Label, Namespace: doc.TagNamespace}
					if tag.Color != nil {
						entry.Color = *tag.Color
					}
					if tag.Icon != nil {
						entry.Icon = *tag.Icon
					}
					response.Tags = append(response.Tags, entry)
				}
			}
		}
		// Return newly selected styles with the response so new tags render without
		// another vocabulary request. Styles retain their existing separate store.
		created := map[string]bool{}
		for _, result := range results {
			for _, id := range result.CreatedTags {
				created[id] = true
			}
		}
		_ = created
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO annotation_batch_receipt(caller,request_id,request_hash,response) VALUES(?,?,?,?)`, caller, request.RequestID, hash, encoded); err != nil {
			return err
		}
		for i, id := range request.MeetingIDs {
			if _, err = tx.ExecContext(ctx, `INSERT INTO annotation_batch_target(caller,request_id,opus_name,snapshot) VALUES(?,?,?,?)`, caller, request.RequestID, path.Base(paths[id]), results[i].Sync.Desired); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return annotationBatchResponse{}, err
	}
	s.wakeAnnotations()
	s.wakeAnnotations()
	return response, nil
}

func annotationWriteResponse(id string, result annotateResult) annotationsWriteResponse {
	return annotationsWriteResponse{MeetingID: id, StateToken: result.StateToken, Sync: result.Sync, Revision: result.Revision, OperationID: result.OperationID, Added: nonNilStrings(result.Added), Removed: nonNilStrings(result.Removed), NotFound: nonNilStrings(result.NotFound), Annotations: result.Annotations, Resolved: result.Resolved}
}
