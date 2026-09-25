package operator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const publishSinkNextcloudFiles = "nextcloud-files"
const ncRecordingsContentType = "audio/ogg"
const envPublishSinkName = "CASSINI_PUBLISH_SINK"
const maxCarryAttempts = 3
const publishAnnotationsUnreadable = "marks-unreadable"

type nextcloudFilesPublishSink struct {
	cfg        ExAppConfig
	logger     *log.Logger
	client     *http.Client
	cassiniBin string
	rt         *Runtime
}

type upload struct {
	local, remote string
	size          int64
}

func (s *nextcloudFilesPublishSink) putOverDeliveredCopy(ctx context.Context, item upload, state ncLeafState) (*annotateResult, error) {
	dir, cleanup, err := newCarryDir(item)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if state, err = s.cfg.davPropfindLeafState(ctx, s.client, ncRecordingsOwner, item.remote); err != nil {
				return nil, fmt.Errorf("inspect %s: %w", item.remote, err)
			}
			if !state.Exists {
				// Re-deriving its access from here would be guessing; the next
				// publish takes the absent branch.
				return nil, fmt.Errorf("refusing to publish %s: it disappeared while this rerun was carrying its marks forward; re-run the publish", item.remote)
			}
		}
		if state.ETag == "" {
			return nil, fmt.Errorf("refusing to publish %s: Nextcloud reported no ETag for it, so it cannot be replaced without risking a mark written meanwhile; re-run the publish", item.remote)
		}
		staged, result, err := s.stageDeliveredMarks(ctx, item, dir)
		if err != nil {
			return nil, err
		}
		err = s.putAssetBytes(ctx, staged, state.ETag)
		if err == nil {
			return &result, nil
		}
		if !errors.Is(err, errDAVPreconditionFailed) {
			return nil, err
		}
		if attempt >= maxCarryAttempts {
			return nil, fmt.Errorf("refusing to publish %s: it changed under this rerun %d times — marks are being written to it right now; re-run the publish: %w",
				item.remote, attempt, err)
		}
		s.logf("nc files: %s changed while its marks were being carried (attempt %d of %d) — reading it again", item.remote, attempt, maxCarryAttempts)
	}
}

func (s *nextcloudFilesPublishSink) stageDeliveredMarks(ctx context.Context, item upload, dir string) (upload, annotateResult, error) {
	delivered := filepath.Join(dir, "delivered.opus")
	staged := filepath.Join(dir, "staged.opus")
	// A retry reuses the directory; nothing read at a stale ETag may leak into it.
	for _, p := range []string{delivered, staged} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return upload{}, annotateResult{}, err
		}
	}
	if _, _, _, err := s.cfg.davDownloadFile(ctx, s.client, ncRecordingsOwner, item.remote, delivered, 0); err != nil {
		return upload{}, annotateResult{}, fmt.Errorf("fetch the delivered %s to carry its marks: %w", item.remote, err)
	}
	result, err := runAnnotateCarry(ctx, s.cassiniBin, delivered, item.local, staged)
	if err != nil {
		return s.sealedIfDeliveredIsUnreadable(ctx, item, delivered, err)
	}
	info, err := os.Stat(staged)
	if err != nil {
		return upload{}, annotateResult{}, fmt.Errorf("carry the marks on %s: it reported success and wrote nothing: %w", item.remote, err)
	}
	if result.Resolved != nil && !*result.Resolved {
		// A different audio identity does not inherit the old recording's marks.
		s.logf("nc files: %s: different audio — discarding %d carried mark(s)", item.remote, result.Carried)
		sealed, err := runAnnotateShow(ctx, s.cassiniBin, item.local)
		return item, sealed, err
	}
	return upload{local: staged, remote: item.remote, size: info.Size()}, result, nil
}

func (s *nextcloudFilesPublishSink) sealedIfDeliveredIsUnreadable(ctx context.Context, item upload, delivered string, carryErr error) (upload, annotateResult, error) {
	if _, err := runAnnotateShow(ctx, s.cassiniBin, delivered); err == nil {
		return upload{}, annotateResult{}, fmt.Errorf("carry the marks on %s into this rerun: %w", item.remote, carryErr)
	}
	sealed, err := runAnnotateShow(ctx, s.cassiniBin, item.local)
	if err != nil {
		return upload{}, annotateResult{}, fmt.Errorf("carry the marks on %s into this rerun: %w (and the CLI cannot read the sealed file either: %v)", item.remote, carryErr, err)
	}
	s.logf("nc files: the delivered %s is unreadable — repairing from the sealed file and reconciling durable marks (%v)", item.remote, carryErr)
	return item, sealed, nil
}

func (s *nextcloudFilesPublishSink) carriesMarks(item upload) bool {
	return strings.HasSuffix(item.remote, ".opus") && strings.TrimSpace(s.cassiniBin) != ""
}

func newCarryDir(item upload) (string, func(), error) {
	dir, err := os.MkdirTemp(filepath.Dir(item.local), ".carry-")
	if err != nil {
		return "", nil, fmt.Errorf("carry scratch for %s: %w", item.remote, err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func (s *nextcloudFilesPublishSink) indexDeliveredMarks(ctx context.Context, uploads []upload, carried map[string]annotateResult) {
	if s.rt == nil || s.rt.annotations == nil {
		return
	}
	if s.rt.annotationReads() != nil {
		// Durable heads were reconciled with the verified upload, before any
		// later catalog/ACL failure could skip this callback.
		return
	}
	index := s.rt.annotations
	for _, item := range uploads {
		if !s.carriesMarks(item) {
			continue
		}
		name := path.Base(item.remote)
		result, ok := carried[item.remote]
		if !ok {
			var err error
			if result, err = runAnnotateShow(ctx, s.cassiniBin, item.local); err != nil {
				s.markAnnotationsUnavailable(ctx, index, name, fmt.Errorf("read marks: %w", err))
				continue
			}
		}
		if err := index.Record(ctx, name, result); err != nil {
			s.markAnnotationsUnavailable(ctx, index, name, fmt.Errorf("record marks: %w", err))
		}
	}
}

func (s *nextcloudFilesPublishSink) markAnnotationsUnavailable(ctx context.Context, index annotationIndex, name string, cause error) {
	s.logf("annotations index: %s: %v — recorded as unavailable", name, cause)
	if err := index.MarkUnavailable(ctx, name, publishAnnotationsUnreadable); err != nil {
		s.logf("annotations index: %s: could not record it unavailable either: %v", name, err)
	}
}

func (s *nextcloudFilesPublishSink) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *nextcloudFilesPublishSink) putAssetBytes(ctx context.Context, item upload, ifMatch string) error {
	store := s.rt.annotationReads()
	var delivered annotateResult
	if store != nil && s.carriesMarks(item) {
		var err error
		delivered, err = runAnnotateShow(ctx, s.cassiniBin, item.local)
		if err != nil || delivered.Unsupported {
			return fmt.Errorf("cannot inspect replacement annotations: %v", err)
		}
		if err = store.prepareAnnotationRepublish(ctx, path.Base(item.remote), delivered); err != nil {
			return err
		}
	}
	if _, _, err := s.cfg.davPutFileIfMatch(ctx, s.client, ncRecordingsOwner, item.remote, item.local, ncRecordingsContentType, ifMatch); err != nil {
		return fmt.Errorf("put %s: %w", item.remote, err)
	}

	if err := s.cfg.verifyUploadedLeaf(ctx, s.client, item.remote, item.size); err != nil {
		return fmt.Errorf("refusing to publish: %w; re-run the publish", err)
	}
	if store != nil && s.carriesMarks(item) {
		dir, cleanup, err := newCarryDir(item)
		if err != nil {
			return err
		}
		defer cleanup()
		verified := filepath.Join(dir, "verified.opus")
		if _, _, err = s.cfg.stageRecording(ctx, s.client, ncRecordingsOwner, item.remote, verified, maxAnnotateRecordingBytes); err != nil {
			return err
		}
		digest, err := fileSHA256(verified)
		if err != nil {
			return err
		}
		if digest != delivered.ContainerSHA256 {
			return fmt.Errorf("replacement recording content verification failed")
		}
		return store.recordAnnotationDelivery(ctx, path.Base(item.remote), delivered)
	}
	return nil
}

func (c ExAppConfig) verifyUploadedLeaf(ctx context.Context, client *http.Client, relPath string, size int64) error {
	state, err := c.davPropfindLeafState(ctx, client, ncRecordingsOwner, relPath)
	switch {
	case err != nil:
		return fmt.Errorf("verify %s: %w", relPath, err)
	case !state.Exists:
		return fmt.Errorf("verify %s: it is not there after a successful upload", relPath)
	case state.Size != size:
		return fmt.Errorf("%s: Nextcloud stored %d bytes of %d — the upload was interrupted and the remote copy is truncated", relPath, state.Size, size)
	}
	return nil
}

func defaultPublishSinkFor(exapp ExAppConfig) string {
	if exapp.Active {
		return publishSinkNextcloudFiles
	}
	return publishSinkLocal
}

func newPublishSinkFor(name string, cfg Config, exapp ExAppConfig, rt *Runtime, logger *log.Logger) (publishSink, error) {
	switch name {
	case publishSinkNextcloudFiles:
		if !exapp.appAPIActive() {
			return nil, fmt.Errorf(
				"sink %q needs NEXTCLOUD_URL, APP_ID and APP_SECRET; set CASSINI_PUBLISH_SINK=%s to keep recordings on this machine instead",
				publishSinkNextcloudFiles, publishSinkLocal)
		}
		return &directSharesPublishSink{&nextcloudFilesPublishSink{
			cfg: exapp, logger: logger, client: &http.Client{Timeout: ncFilesUploadTimeout},
			cassiniBin: cfg.CassiniBin, rt: rt,
		}}, nil
	default:
		return newPublishSink(name, cfg, logger)
	}
}
