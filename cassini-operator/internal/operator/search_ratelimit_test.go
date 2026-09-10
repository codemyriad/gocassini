package operator

import (
	"net/http"
	"testing"
	"time"
)

func TestSearchRateLimiterAllowsABurstThenRefuses(t *testing.T) {
	limiter := newSearchRateLimiter()
	for i := 0; i < searchBurst; i++ {
		if allowed, _ := limiter.allow("alice"); !allowed {
			t.Fatalf("search %d of the burst was refused", i+1)
		}
	}
	allowed, wait := limiter.allow("alice")
	if allowed {
		t.Fatal("the burst is unbounded")
	}
	// The answer says when to come back; a client told only "too many" has to
	// guess, and guessing badly is how a polite client becomes an impolite one.
	if wait <= 0 || wait > searchRefillInterval {
		t.Errorf("wait = %v, want a hint within one refill interval", wait)
	}
}

// One agent in a loop must not lock everybody else out of a feature they are
// using correctly.
func TestSearchRateLimiterIsPerCaller(t *testing.T) {
	limiter := newSearchRateLimiter()
	for i := 0; i < searchBurst; i++ {
		limiter.allow("loop")
	}
	if allowed, _ := limiter.allow("loop"); allowed {
		t.Fatal("expected the loop to be limited")
	}
	if allowed, _ := limiter.allow("someone-else"); !allowed {
		t.Fatal("a second caller was punished for the first one's loop")
	}
}

func TestSearchRateLimiterRefillsOverTime(t *testing.T) {
	limiter := newSearchRateLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }

	for i := 0; i < searchBurst; i++ {
		limiter.allow("alice")
	}
	if allowed, _ := limiter.allow("alice"); allowed {
		t.Fatal("expected to be out of tokens")
	}
	now = now.Add(searchRefillInterval)
	if allowed, _ := limiter.allow("alice"); !allowed {
		t.Fatal("a token did not come back after one interval")
	}
}

// Refill must carry the remainder. Advancing the clock to now on every call
// would discard a partial interval each time, so a caller polling faster than
// the refill would never regain a token at all — a limiter that turns into a
// permanent ban.
func TestSearchRateLimiterRefillsUnderConstantPolling(t *testing.T) {
	limiter := newSearchRateLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for i := 0; i < searchBurst; i++ {
		limiter.allow("poller")
	}

	granted := 0
	// Poll every quarter-interval for four intervals: roughly four tokens.
	for i := 0; i < 16; i++ {
		now = now.Add(searchRefillInterval / 4)
		if allowed, _ := limiter.allow("poller"); allowed {
			granted++
		}
	}
	if granted < 3 {
		t.Fatalf("granted %d tokens while polling; refill is being lost", granted)
	}
}

// Idle buckets are dropped so the map is bounded by active callers rather than
// by every account that has ever searched.
func TestSearchRateLimiterForgetsIdleCallers(t *testing.T) {
	limiter := newSearchRateLimiter()
	now := time.Now()
	limiter.now = func() time.Time { return now }
	limiter.allow("one-off")
	if len(limiter.buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(limiter.buckets))
	}
	now = now.Add(searchLimiterIdleTTL + time.Minute)
	limiter.allow("someone-else")
	if _, still := limiter.buckets["one-off"]; still {
		t.Error("an idle caller's bucket was kept")
	}
}

// A nil limiter must not refuse: search degrading is acceptable, search
// breaking is not.
func TestSearchRateLimiterNilAllows(t *testing.T) {
	var limiter *searchRateLimiter
	if allowed, _ := limiter.allow("alice"); !allowed {
		t.Fatal("a nil limiter refused a search")
	}
}

// The refusal must arrive before the expensive part: the whole reason to limit
// is that a search makes this app talk to Nextcloud on the caller's behalf.
func TestSearchEndpointRefusesBeforeCallingNextcloud(t *testing.T) {
	index := newTestSearchStore(t)
	var upstreamCalls int
	srv := searchUpstream{catalog: searchTestCatalog, visible: []string{"JOB1.opus"}}.server(t)
	defer srv.Close()

	cfg := searchTestConfig(srv.URL)
	limiter := newSearchRateLimiter()
	for i := 0; i < searchBurst; i++ {
		limiter.allow("alice")
	}
	deps := searchDeps{index: index, limiter: limiter}

	rec := doSearchWithDeps(t, cfg, deps, "q=acquisition", "alice")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("a 429 should say when to come back")
	}
	if upstreamCalls != 0 {
		t.Errorf("Nextcloud was called %d times for a refused search", upstreamCalls)
	}
}
