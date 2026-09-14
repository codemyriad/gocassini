package operator

import (
	"context"
	"errors"
)

// Why an insight run failed, as the run row records it (D-749).
//
// A failed run stores ONE of these tokens and nothing else: no sentence, no
// child stderr, no HTTP status. The words a reader sees for each token live in
// the app (cassini-viewer/src/viewer/insights.ts), which is the only place they
// exist. Before this the operator stored a finished sentence, and a run that
// failed in one release carried that release's wording forever, the app's
// title and the stored line repeated each other, and the copy lived in two
// codebases. The technical detail keeps going to the operator log at the call
// site that has it; the row carries only the classification.
//
// The first four are `cassini insight run`'s own reasons, one per exit code
// (internal/insight classifies the failure; the operator only maps the code).
// The rest name the operator's own failures around the child.
const (
	// The child's own exit codes, 2 to 5.
	insightReasonBadRequest      = "bad-request"
	insightReasonNoProvider      = "no-provider"
	insightReasonProviderRefused = "provider-refused"
	insightReasonModelFailed     = "model-failed"
	// Exit 1: the answer was produced but could not be written.
	insightReasonWriteFailed = "write-failed"
	// The WebDAV PUT into the requester's files failed.
	insightReasonDeliverFailed = "deliver-failed"
	// The attempt hit its own deadline — the run-timeout backstop.
	insightReasonTimeout = "timeout"
	// The staging directory or the staged catalog could not be written.
	insightReasonStagingFailed = "staging-failed"
	// The per-caller meeting scan failed or came back empty.
	insightReasonCatalogFailed = "catalog-failed"
	// A picked meeting is not in the caller's readable set, or Nextcloud
	// answered 401/403/404 when it was fetched.
	insightReasonMeetingUnavailable = "meeting-unavailable"
	// A recording fetch failed for any other reason.
	insightReasonDownloadFailed = "download-failed"
	// `cassini meetings context` could not build the bundle.
	insightReasonAssembleFailed = "assemble-failed"
	// The operator went away under the run: its context was cancelled by a
	// shutdown, or the store's sweep found it stranded afterwards (written by
	// MarkInterruptedRunsFailed). Nothing about the request was wrong.
	insightReasonInterrupted = "interrupted"
	// Any exit code the contract does not name.
	insightReasonUnknown = "unknown"
)

// insightReasons is the whole set, in the order above. FinishAttempt refuses a
// failure that names anything else, so a sentence can never reach the row.
var insightReasons = []string{
	insightReasonBadRequest,
	insightReasonNoProvider,
	insightReasonProviderRefused,
	insightReasonModelFailed,
	insightReasonWriteFailed,
	insightReasonDeliverFailed,
	insightReasonTimeout,
	insightReasonStagingFailed,
	insightReasonCatalogFailed,
	insightReasonMeetingUnavailable,
	insightReasonDownloadFailed,
	insightReasonAssembleFailed,
	insightReasonInterrupted,
	insightReasonUnknown,
}

func isInsightReason(token string) bool {
	for _, reason := range insightReasons {
		if token == reason {
			return true
		}
	}
	return false
}

// insightExitReason maps `cassini insight run`'s exit code onto a reason.
//
// The code space is the contract — it is documented in the command's own help
// precisely so a caller need not read the message — so this switches on it and
// never on the child's text. A context that ended outranks whatever the killed
// child reported: a run stopped from outside did not fail at the model. The
// two ways it ends are different advice — the deadline is the run's own
// 60-minute bound, so a smaller selection is the fix; a cancellation is the
// operator shutting down under the run, so retrying it unchanged is (D-740).
func insightExitReason(ctx context.Context, code int) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return insightReasonTimeout
	case ctx.Err() != nil:
		return insightReasonInterrupted
	}
	switch code {
	case 1:
		return insightReasonWriteFailed
	case 2:
		return insightReasonBadRequest
	case 3:
		return insightReasonNoProvider
	case 4:
		return insightReasonProviderRefused
	case 5:
		return insightReasonModelFailed
	default:
		return insightReasonUnknown
	}
}

// insightReasonOf is the `reason` a run is served with: its stored token when
// it failed, and "" otherwise. A row written before tokens existed carries a
// sentence in `error`; that is served as it is, under `error`, and the app
// classifies it by the token prefix those sentences carried.
func insightReasonOf(status, stored string) string {
	if status != insightStatusFailed || !isInsightReason(stored) {
		return ""
	}
	return stored
}
