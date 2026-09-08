package operator

import (
	"strings"
	"sync"
	"time"
)

// Rate limiting search (D-623 §8).
//
// A search is the most expensive thing a caller can ask this app to do, and the
// cost does not land here — it lands on Nextcloud. Every query resolves the
// caller's visible set live, which is one whole-archive PROPFIND as that
// caller, plus a catalog GET as the recordings owner: a request the caller
// could not otherwise cause. An agent in a loop therefore amplifies itself into
// sustained load on somebody else's Nextcloud.
//
// Deliberately NOT solved with a cache. Caching the visible set would trade
// away the property the whole access model rests on: the scan is re-run per
// request, so revocation, group changes and deletions take effect immediately
// and no permission-change signal is needed. A limiter costs a caller latency;
// a cache costs everyone correctness.
//
// A token bucket rather than a fixed window: a person runs a handful of
// searches in a burst and then stops, which a fixed window punishes at exactly
// the wrong moment. The burst is what a human does; the refill is what a loop
// gets.
const (
	// searchBurst is how many searches a caller may run back to back.
	searchBurst = 10
	// searchRefillInterval is how quickly one token comes back — 30 a minute
	// sustained, which no human reaches and a loop hits immediately.
	searchRefillInterval = 2 * time.Second
	// searchLimiterIdleTTL is how long an idle caller's bucket is kept. Without
	// it the map grows once per account that ever searched and never shrinks.
	searchLimiterIdleTTL = 30 * time.Minute
)

// searchRateLimiter bounds searches per caller.
//
// Per caller rather than global: one agent looping must not lock everybody else
// out of a feature they are using correctly.
type searchRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*searchBucket
	// now is injectable so the tests can exercise refill without sleeping.
	now func() time.Time

	burst  int
	refill time.Duration
}

type searchBucket struct {
	tokens int
	// last is when tokens were last recomputed, and doubles as the idle clock.
	last time.Time
}

func newSearchRateLimiter() *searchRateLimiter {
	return &searchRateLimiter{
		buckets: map[string]*searchBucket{},
		now:     time.Now,
		burst:   searchBurst,
		refill:  searchRefillInterval,
	}
}

// allow reports whether this caller may run a search now, and how long to wait
// if not.
//
// The wait is returned so the answer can say when to come back rather than just
// refusing — a client told only "too many" has to guess, and guessing badly is
// how a polite client becomes an impolite one.
func (l *searchRateLimiter) allow(caller string) (bool, time.Duration) {
	key := strings.TrimSpace(caller)
	if l == nil || key == "" {
		// No identity to attribute the cost to. The handler refuses these
		// before reaching here; not limiting is the safe direction, because a
		// bucket keyed on "" would be shared by every anonymous request.
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.evictIdleLocked(now)

	bucket, seen := l.buckets[key]
	if !seen {
		bucket = &searchBucket{tokens: l.burst, last: now}
		l.buckets[key] = bucket
	} else {
		// Refill by whole intervals only, and carry the remainder by advancing
		// `last` by exactly what was granted. Setting last=now would discard a
		// partial interval on every call, so a caller polling faster than the
		// refill would never regain a token at all.
		if elapsed := now.Sub(bucket.last); elapsed >= l.refill {
			granted := int(elapsed / l.refill)
			bucket.tokens += granted
			if bucket.tokens > l.burst {
				bucket.tokens = l.burst
			}
			bucket.last = bucket.last.Add(time.Duration(granted) * l.refill)
		}
	}

	if bucket.tokens <= 0 {
		wait := l.refill - now.Sub(bucket.last)
		if wait < 0 {
			wait = 0
		}
		return false, wait
	}
	bucket.tokens--
	// An idle caller is one that has not searched, so the clock only advances
	// on a full bucket — otherwise a spender would look idle and be evicted
	// mid-burst, handing itself a fresh one.
	if bucket.tokens == l.burst-1 {
		bucket.last = now
	}
	return true, 0
}

// evictIdleLocked drops buckets nobody has used for a while, so the map is
// bounded by active callers rather than by every account that ever searched.
func (l *searchRateLimiter) evictIdleLocked(now time.Time) {
	for key, bucket := range l.buckets {
		if now.Sub(bucket.last) > searchLimiterIdleTTL {
			delete(l.buckets, key)
		}
	}
}
