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
// archive at all retries with backoff; a run that reads it marks the index
// built, with any unreadable recordings left outside coverage.
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
	go func() {
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
	targets, err := exapp.archiveBackfillTargets(ctx)
	if err != nil {
		return fmt.Errorf("read the archive catalog: %w", err)
	}
	report, err := backfillAnnotationIndex(ctx, store, logger, targets,
		exapp.archiveDeliveredState(), exapp.archiveAnnotationReader(rt.cfg.CassiniBin, rt.cfg.WorkRoot))
	if err != nil {
		return err
	}
	logger.Printf("annotations: first rebuild of the tag index done — indexed=%d unchanged=%d unreadable=%d failed=%d of %d",
		report.Indexed, report.Unchanged, report.Unavailable, report.Failed, len(targets))
	return nil
}
