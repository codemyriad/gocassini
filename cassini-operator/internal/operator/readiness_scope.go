package operator

import (
	"strings"
	"time"
)

// readinessScope says which probes a check runs.
//
// Every row in the checklist is established by exactly one of these, or by
// reading saved configuration and nothing at all. A reader who wants to retry
// one row should not pay for a whole-archive PROPFIND and a Talk round trip to
// do it (review 2026-09-25: "a button to run them all, or to run them
// independently").
type readinessScope struct {
	storage bool
	host    bool
	talk    bool
	archive bool
}

func allReadinessScopes() readinessScope {
	return readinessScope{storage: true, host: true, talk: true, archive: true}
}

func (s readinessScope) empty() bool {
	return !s.storage && !s.host && !s.talk && !s.archive
}

// readinessScopeFor maps row ids to the probes that establish them.
//
// Unknown ids, and rows that are read from saved configuration rather than
// probed — configuration, talk.handoff — contribute
// nothing. A request naming only those yields an empty scope, which the caller
// must refuse rather than silently run everything: a button that quietly does
// far more than it says is worse than one that does not work.
func readinessScopeFor(ids []string) readinessScope {
	var scope readinessScope
	for _, id := range ids {
		switch id := strings.TrimSpace(id); {
		case id == "storage":
			scope.storage = true
		case id == "host" || strings.HasPrefix(id, "host."):
			scope.host = true
		case id == "talk.discovery" || id == "talk.hpb":
			scope.talk = true
		case strings.HasPrefix(id, "archive."):
			scope.archive = true
		}
	}
	return scope
}

// probeCoalesceWindow collapses rapid duplicate clicks on the same row. Held
// per scope rather than globally, so retrying the host does not silently skip a
// storage check somebody asked for a second later.
const probeCoalesceWindow = 2 * time.Second

// beginProbe reports whether this scope should run now, and records that it did.
// Callers hold no lock; this takes s.mu itself.
func (s *recordingSetup) beginProbe(name string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probedAt == nil {
		s.probedAt = map[string]time.Time{}
	}
	if last, ok := s.probedAt[name]; ok && now.Sub(last) < probeCoalesceWindow {
		return false
	}
	s.probedAt[name] = now
	return true
}
