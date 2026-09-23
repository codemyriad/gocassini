package operator

import (
	"context"
	"fmt"
	"log"
	"time"
)

// startInitialAnnotationBuild rebuilds the tag index from the archive when it
// has never been built — a new install, a wiped volume, a schema change — so
// nobody has to remember backfill-annotations after a deploy. Writes answer 503
// until it finishes (errAnnotationIndexBuilding). A run that cannot read the
// archive or skips transient failures retries with backoff. Only a complete
// pass marks the index built; unreadable content stays outside coverage.
func (rt *Runtime) startInitialAnnotationBuild(exapp ExAppConfig, logger *log.Logger) {
	store := rt.annotationReads()
	if store == nil {
		return
	}
	built, err := store.builtOnce(rt.ctx)
	if err != nil {
		logger.Printf("annotations: %v; not starting a rebuild", err)
		return
	}
	if built {
		return
	}
	store.rebuildPending.Store(true)
	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		delay := time.Minute
		for {
			err := rt.buildAnnotationIndexOnce(exapp, store, logger)
			if err == nil {
				store.rebuildPending.Store(false)
				return
			}
			logger.Printf("annotations: first rebuild of the tag index failed (%v); retrying in %s", err, delay)
			select {
			case <-rt.ctx.Done():
				return
			case <-time.After(delay):
			}
			if delay < 10*time.Minute {
				delay *= 2
			}
		}
	}()
}

func (rt *Runtime) buildAnnotationIndexOnce(exapp ExAppConfig, store *annotationStore, logger *log.Logger) error {
	ctx, cancel := context.WithTimeout(rt.ctx, backfillAnnotationsTimeout)
	defer cancel()
	targets, err := exapp.archiveBackfillTargets(ctx, rt.cfg.DBPath)
	if err != nil {
		return fmt.Errorf("read the owner recording inventory: %w", err)
	}
	report, err := backfillAnnotationIndex(ctx, store, logger, targets,
		exapp.archiveDeliveredState(), exapp.archiveAnnotationReader(rt.cfg.CassiniBin, rt.cfg.WorkRoot))
	if err != nil {
		return err
	}
	logger.Printf("annotations: first rebuild of the tag index done — indexed=%d unchanged=%d unreadable=%d failed=%d of %d",
		report.Indexed, report.Unchanged, report.Unavailable, report.Failed, len(targets))
	if report.Failed > 0 {
		return fmt.Errorf("%d recordings still need annotation import", report.Failed)
	}
	return nil
}
