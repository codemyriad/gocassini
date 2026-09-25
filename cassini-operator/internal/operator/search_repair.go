package operator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Repair actions are the alternative to telling an administrator to go and run
// a command (D-798, review 2026-09-25).
//
// The panel is ADMIN-only and the operator can already do this work, so a row
// that knows its own remedy offers a button instead of a shell line. Printing
// `cassini-operator backfill-search` asked a reader to find a terminal, find
// the right container, and get the invocation right, to trigger something the
// process showing them the message could simply do.
const repairBackfillSearch = "backfill_search"

// backfillRunTimeout bounds a run started from the panel. The CLI allows two
// hours because a human is watching it; this one reports through a row that is
// read on demand, so it is generous for the same reason.
const backfillRunTimeout = 2 * time.Hour

type searchRepairState struct {
	mu        sync.Mutex
	running   bool
	startedAt time.Time
	finished  time.Time
	report    searchBackfillReport
	err       error
	ran       bool
}

func (s *searchRepairState) snapshot() (running bool, ran bool, report searchBackfillReport, err error, finished time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.ran, s.report, s.err, s.finished
}

// startSearchBackfill runs the backfill in this process, against the index the
// operator already has open.
//
// Not the CLI path: `cassini-operator backfill-search` opens its own store and
// resolves the storage mode itself because it is a separate process. Both of
// those are already true here, and a second process opening the same SQLite
// index while this one holds it is the thing to avoid.
//
// Returns false when a run is already in flight, so a second click joins the
// first rather than starting a competing pass over the archive.
func (rt *Runtime) startSearchBackfill() bool {
	state := &rt.searchRepair
	state.mu.Lock()
	if state.running {
		state.mu.Unlock()
		return false
	}
	state.running = true
	state.startedAt = time.Now()
	state.err = nil
	state.report = searchBackfillReport{}
	state.mu.Unlock()

	rt.workerWG.Add(1)
	go func() {
		defer rt.workerWG.Done()
		report, err := rt.runSearchBackfill()
		state.mu.Lock()
		state.running = false
		state.ran = true
		state.finished = time.Now()
		state.report = report
		state.err = err
		state.mu.Unlock()
		// The coverage row is computed from the index; it has just changed.
		rt.invalidateSearchReadiness()
		if err != nil {
			rt.logger.Printf("ERROR: search backfill from the health panel failed: %v", err)
			return
		}
		rt.logger.Printf("search backfill: indexed=%d unchanged=%d not-searchable=%d failed=%d",
			report.Indexed, report.Unchanged, report.Unavailable, report.Failed)
	}()
	return true
}

func (rt *Runtime) runSearchBackfill() (searchBackfillReport, error) {
	if rt.searchStore == nil {
		return searchBackfillReport{}, errors.New("the search index is not open")
	}
	cfg, err := LoadExAppConfig()
	if err != nil {
		return searchBackfillReport{}, fmt.Errorf("read the AppAPI environment: %w", err)
	}
	if !cfg.Active {
		return searchBackfillReport{}, errors.New("backfill needs the AppAPI environment")
	}
	ctx, cancel := context.WithTimeout(rt.ctx, backfillRunTimeout)
	defer cancel()
	targets, err := cfg.archiveBackfillTargets(ctx)
	if err != nil {
		return searchBackfillReport{}, fmt.Errorf("read the archive catalog: %w", err)
	}
	if len(targets) == 0 {
		return searchBackfillReport{}, nil
	}
	return rt.backfillSearchIndex(ctx, targets,
		cfg.archiveDeliveredState(),
		cfg.archiveOpusReader(rt.cfg.CassiniBin, rt.cfg.WorkRoot))
}
