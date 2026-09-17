package operator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"time"
)

func (s *annotationService) wakeAnnotations() {
	if s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
func (s *annotationService) startAnnotationWorkers() {
	if s.rt == nil || s.rt.ctx == nil || s.rt.annotationReads() == nil {
		return
	}
	s.start.Do(func() {
		s.wake = make(chan struct{}, 2)
		for i := 0; i < 2; i++ {
			s.rt.workerWG.Add(1)
			go s.annotationWorker()
		}
		s.wakeAnnotations()
		s.wakeAnnotations()
	})
}
func (s *annotationService) annotationWorker() {
	defer s.rt.workerWG.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.rt.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		for s.rt.ctx.Err() == nil {
			name, err := s.nextAnnotation()
			if err != nil {
				s.logf("annotations: scan: %v", err)
				break
			}
			if name == "" {
				break
			}
			ctx, cancel := context.WithTimeout(s.rt.ctx, annotateRequestTimeout)
			err = s.syncAnnotation(ctx, name)
			cancel()
			if err != nil {
				s.retryAnnotation(name, err)
			}
			s.claimed.Delete(name)
		}
	}
}
func (s *annotationService) nextAnnotation() (string, error) {
	rows, err := s.rt.annotationReads().db.QueryContext(s.rt.ctx, `SELECT opus_name FROM annotation_head WHERE desired!=confirmed AND blocked=0 AND retry_at<=unixepoch() ORDER BY retry_at,desired LIMIT 100`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		if _, busy := s.claimed.LoadOrStore(name, true); !busy {
			return name, nil
		}
	}
	return "", rows.Err()
}

type annotationBlocked struct{ reason string }

func (e *annotationBlocked) Error() string { return e.reason }
func (s *annotationService) retryAnnotation(name string, err error) {
	// Public status is deliberately generic; detailed DAV errors stay in logs.
	s.logf("annotations: sync %s: %v", name, err)
	store := s.rt.annotationReads()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.rt.ctx), annotateIndexTimeout)
	defer cancel()
	blocked := 0
	message := "archive temporarily unavailable; retrying"
	var b *annotationBlocked
	if errors.As(err, &b) {
		blocked = 1
		message = b.reason
	}
	var attempts int
	_ = store.db.QueryRowContext(ctx, `SELECT attempts FROM annotation_head WHERE opus_name=?`, name).Scan(&attempts)
	delay := min(300, 5*(1<<min(attempts, 6)))
	wait := time.Duration(min(300.0, float64(delay)*(0.8+rand.Float64()*0.4))) * time.Second
	retry := time.Now().Add(wait).Unix()
	if blocked == 0 {
		time.AfterFunc(wait, s.wakeAnnotations)
	}
	if _, e := store.db.ExecContext(ctx, `UPDATE annotation_head SET attempts=attempts+1,retry_at=?,last_error=?,blocked=? WHERE opus_name=? AND desired!=confirmed`, retry, message, blocked, name); e != nil {
		s.logf("annotations: persist retry: %v", e)
	}
}

func sameAnnotationDocument(a, b json.RawMessage) bool {
	var left, right bytes.Buffer
	if len(a) == 0 {
		a = []byte("null")
	}
	if len(b) == 0 {
		b = []byte("null")
	}
	if json.Compact(&left, a) != nil || json.Compact(&right, b) != nil {
		return false
	}
	// Document writers canonicalize ordering; tolerate object member order too.
	var av, bv any
	if json.Unmarshal(left.Bytes(), &av) != nil || json.Unmarshal(right.Bytes(), &bv) != nil {
		return false
	}
	aa, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return bytes.Equal(aa, bb)
}
func (s *annotationService) snapshot(ctx context.Context, id int64) (annotateResult, error) {
	var data []byte
	err := s.rt.annotationReads().db.QueryRowContext(ctx, `SELECT result_json FROM annotation_snapshot WHERE id=?`, id).Scan(&data)
	var result annotateResult
	if err == nil {
		err = json.Unmarshal(data, &result)
	}
	return result, err
}
func (s *annotationService) syncAnnotation(ctx context.Context, name string) error {
	// Storage transitions take the exclusive side; different workers may stage
	// concurrently, while republish shares the per-recording lock below.
	provisionMu.RLock()
	defer provisionMu.RUnlock()
	release, err := annotationWriteLocks.acquire(ctx, name)
	if err != nil {
		return err
	}
	defer release()
	store := s.rt.annotationReads()
	var desired, confirmed int64
	var flight sql.NullInt64
	var rel string
	if err = store.db.QueryRowContext(ctx, `SELECT desired,confirmed,in_flight,rel_path FROM annotation_head WHERE opus_name=?`, name).Scan(&desired, &confirmed, &flight, &rel); err != nil {
		return err
	}
	if desired == confirmed {
		return nil
	}
	// The storage root can move after acceptance; ownership is the catalog ID.
	rel = recordingsRootFor(ncStorage.accessControlled()) + "/meetings/" + path.Base(name)
	target, err := s.snapshot(ctx, desired)
	if err != nil {
		return err
	}
	previous, err := s.snapshot(ctx, confirmed)
	if err != nil {
		return err
	}
	state, err := s.exapp.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, rel)
	if err != nil {
		return err
	}
	if !state.Exists {
		return &annotationBlocked{"recording is missing; restore it and retry"}
	}
	if state.ETag == "" {
		return fmt.Errorf("missing ETag")
	}
	underACL := !annotationInPrivateRoot(rel)
	if underACL && !everyoneRuleGovernsRead(state.Rules) {
		return &annotationBlocked{"recording access needs repair; republish and retry"}
	}
	dir, err := os.MkdirTemp(s.rt.cfg.WorkRoot, "annotation-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in.opus"), filepath.Join(dir, "out.opus")
	if _, _, err = s.exapp.stageRecording(ctx, s.client, ncRecordingsOwner, rel, in, maxAnnotateRecordingBytes, state.ETag); err != nil {
		return err
	}
	remote, err := runAnnotateShow(ctx, s.bin, in)
	if err != nil || remote.Unsupported {
		return &annotationBlocked{"recording annotations cannot be read; repair and retry"}
	}
	// A lost PUT response is settled by the exact persisted target, never by
	// assuming the current desired head was the version that made it to Files.
	if flight.Valid {
		attempted, e := s.snapshot(ctx, flight.Int64)
		if e != nil {
			return e
		}
		if sameAnnotationDocument(remote.Annotations, attempted.Annotations) {
			if err = s.confirmAnnotation(ctx, name, flight.Int64); err != nil {
				return err
			}
			confirmed = flight.Int64
			previous = attempted
			if confirmed == desired {
				return nil
			}
		}
	}
	if !sameAnnotationDocument(remote.Annotations, previous.Annotations) {
		return &annotationBlocked{"archive annotations differ from the last confirmed state; repair before retrying"}
	}
	rendered, err := runAnnotate(ctx, s.bin, target.Annotations, "snapshot", "--out", out, "--json", in)
	if err != nil {
		return err
	}
	if !sameAnnotationDocument(rendered.Annotations, target.Annotations) {
		return &annotationBlocked{"snapshot verification failed"}
	}
	info, err := os.Stat(out)
	if err != nil {
		return err
	}
	if info.Size() > maxAnnotateRecordingBytes {
		return &annotationBlocked{"recording exceeds the annotation staging limit"}
	}
	digest, err := fileSHA256(out)
	if err != nil {
		return err
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE annotation_head SET in_flight=?,input_etag=?,output_sha256=?,output_size=? WHERE opus_name=?`, desired, state.ETag, digest, info.Size(), name); err != nil {
		return err
	}
	if _, _, err = s.exapp.davPutFileIfMatch(ctx, s.client, ncRecordingsOwner, rel, out, ncRecordingsContentType, state.ETag); err != nil {
		return err
	}
	if err = s.exapp.verifyUploadedLeaf(ctx, s.client, rel, info.Size(), underACL); err != nil {
		return err
	}
	// OC-Checksum is supplied by the uploader, so it is not independent proof.
	// Download into the input slot and verify the actual committed bytes.
	if _, _, err = s.exapp.stageRecording(ctx, s.client, ncRecordingsOwner, rel, in, maxAnnotateRecordingBytes); err != nil {
		return err
	}
	actual, err := fileSHA256(in)
	if err != nil {
		return err
	}
	if actual != digest {
		return &annotationBlocked{"uploaded recording could not be verified"}
	}
	return s.confirmAnnotation(ctx, name, desired)
}
func (s *annotationService) confirmAnnotation(ctx context.Context, name string, id int64) error {
	_, err := s.rt.annotationReads().db.ExecContext(ctx, `UPDATE annotation_head SET confirmed=?,in_flight=NULL,input_etag='',output_sha256='',output_size=0,attempts=0,retry_at=0,last_error='',blocked=0 WHERE opus_name=?`, id, name)
	return err
}
