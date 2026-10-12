package operator

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Every accepted speaker edit queues a refine: a republish of the meeting for
// all its readers, usually a call to the summary model, and for a new split a
// diarization of a whole track. Anyone who can read a meeting can save, so
// one person saving in a loop, by hand or by script, would keep the build
// worker and the summary model busy for everyone. Each person gets a number
// of saves per rolling hour, counted across all meetings.
//
// Only saves that queued a refine count. A save refused for any reason (a
// stale revision, a busy job, an invalid document) or one that asks for what
// the recording already carries queues nothing and costs nothing. Reading is
// never limited.
//
// The count lives in memory, so a restart of the operator forgets it.

const (
	// envSpeakerEditsPerHour sets the saves one person may make per rolling
	// hour; 0 turns the limit off.
	envSpeakerEditsPerHour     = "CASSINI_SPEAKER_EDITS_PER_HOUR"
	defaultSpeakerEditsPerHour = 30
	speakerEditsLimitWindow    = time.Hour
)

type speakerEditLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	// saves holds, per user, when each save in the window was made, oldest
	// first.
	saves map[string][]time.Time
}

func newSpeakerEditLimiter(limit int, window time.Duration) *speakerEditLimiter {
	return &speakerEditLimiter{limit: limit, window: window, saves: map[string][]time.Time{}}
}

func speakerEditLimiterFromEnv() *speakerEditLimiter {
	return newSpeakerEditLimiter(envIntDefault(envSpeakerEditsPerHour, defaultSpeakerEditsPerHour), speakerEditsLimitWindow)
}

// reserve counts one save of user's at now. When user has used every save in
// the window it counts nothing and says how long until the oldest leaves the
// window. release takes the save back, for a save that then queued nothing.
func (l *speakerEditLimiter) reserve(user string, now time.Time) (release func(), retryAfter time.Duration, ok bool) {
	if l == nil || l.limit <= 0 {
		return func() {}, 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forgetBefore(now.Add(-l.window))
	saves := l.saves[user]
	if len(saves) >= l.limit {
		return nil, saves[0].Add(l.window).Sub(now), false
	}
	l.saves[user] = append(saves, now)
	return func() { l.release(user, now) }, 0, true
}

func (l *speakerEditLimiter) release(user string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	saves := l.saves[user]
	for i := len(saves) - 1; i >= 0; i-- {
		if saves[i].Equal(at) {
			l.saves[user] = append(saves[:i:i], saves[i+1:]...)
			break
		}
	}
	if len(l.saves[user]) == 0 {
		delete(l.saves, user)
	}
}

// forgetBefore drops every save made at or before cutoff, and the users left
// with none, so the map holds only the people who saved in the window.
func (l *speakerEditLimiter) forgetBefore(cutoff time.Time) {
	for user, saves := range l.saves {
		kept := 0
		for kept < len(saves) && !saves[kept].After(cutoff) {
			kept++
		}
		if kept == len(saves) {
			delete(l.saves, user)
		} else if kept > 0 {
			l.saves[user] = append([]time.Time(nil), saves[kept:]...)
		}
	}
}

// writeSpeakerEditsRateLimited answers 429 with how long, in milliseconds,
// until the caller can save again, and the same as Retry-After in seconds.
func writeSpeakerEditsRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	ms := max((retryAfter+time.Millisecond-1)/time.Millisecond, 1)
	seconds := (ms + 999) / 1000
	w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate-limited", "retryAfterMs": int64(ms)})
}
