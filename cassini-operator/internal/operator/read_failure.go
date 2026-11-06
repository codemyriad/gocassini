package operator

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

// readFailure is a read that produced no answer, in the terms every surface
// reporting it shares: status is what the published routes answer with,
// public is what the caller may be told. The cause is logged where the failure
// happens and never travels in here, so a failure can be handed to any caller
// — an HTTP client, an agent over MCP — without leaking what went wrong inside.
//
// The read functions return it so the HTTP handlers and the MCP tools answer
// the same request with the same refusal, rather than each re-deciding which
// outcomes are the caller's fault and which are an outage.
type readFailure struct {
	status int
	public string
	// retryAfter is set on a rate-limit refusal, for the Retry-After header.
	retryAfter time.Duration
}

func (f *readFailure) Error() string { return f.public }

// Status is the HTTP status the published routes answer this failure with.
func (f *readFailure) Status() int { return f.status }

// Public is the message the caller may be told.
func (f *readFailure) Public() string { return f.public }

func readFail(status int, public string) *readFailure {
	return &readFailure{status: status, public: public}
}

// readRateLimited is the refusal a rate-limited caller gets: wait is how long
// until they may ask again, rounded up to a whole second.
func readRateLimited(wait time.Duration, public string) *readFailure {
	if wait < time.Second {
		wait = time.Second
	}
	return &readFailure{status: http.StatusTooManyRequests, public: public, retryAfter: wait}
}

// writeReadFailure answers a failed read. A 404 is answered exactly as any
// other missing path, so a denial reads as absence on the wire too; anything
// that is not a readFailure is an outage the caller is told nothing about.
func writeReadFailure(w http.ResponseWriter, r *http.Request, err error) {
	var failure *readFailure
	if !errors.As(err, &failure) {
		writeJSONError(w, http.StatusInternalServerError, "the read failed")
		return
	}
	if failure.status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	if failure.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(failure.retryAfter.Seconds())))
	}
	writeJSONError(w, failure.status, failure.public)
}
