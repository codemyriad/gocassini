package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"cassini-operator/internal/operator/appapi"
)

// Accepting pending recording shares (D-885). The counterpart of the pending
// list on /setup: the person asks, and the operator accepts AS them, through
// the same Nextcloud endpoint their own Accept button would use. Nothing is
// ever accepted unasked.

// acceptPendingShare accepts one pending share AS the caller: AppAPI runs the
// call in the caller's own session, so Nextcloud itself checks that the share
// is theirs to accept. Never called as the recordings owner.
func (c ExAppConfig) acceptPendingShare(ctx context.Context, client *http.Client, caller, shareID string) error {
	if strings.TrimSpace(caller) == "" || strings.TrimSpace(shareID) == "" {
		return fmt.Errorf("Nextcloud caller and share id are required")
	}
	_, err := c.shareCall(ctx, client, caller, http.MethodPost, c.shareAPIURL()+"/pending/"+url.PathEscape(shareID), url.Values{})
	return err
}

type sharesAcceptRequest struct {
	ShareIDs []string `json:"share_ids"`
	// All accepts every recording share waiting for the caller.
	All bool `json:"all"`
}

type sharesAcceptResponse struct {
	Accepted []string             `json:"accepted"`
	Failed   []shareAcceptFailure `json:"failed"`
	// Sharing is the caller's view after the accepts, so the app can show
	// what is still waiting without a second request.
	Sharing setupSharing `json:"sharing"`
}

type shareAcceptFailure struct {
	ShareID string `json:"share_id"`
	Reason  string `json:"reason"`
}

const (
	// maxShareAcceptIDs bounds one request's share_ids, and one round of
	// "all".
	maxShareAcceptIDs = 100
	// maxShareAcceptRounds bounds "all": a thousand recordings waiting for
	// one person is not a case to serve in one request.
	maxShareAcceptRounds    = 10
	maxShareAcceptBodyBytes = 64 << 10
	shareAcceptNotWaiting   = "Not a recording waiting for you."
)

// sharesAcceptHandler accepts pending recording shares AS the caller, and
// only shares in the caller's own pending list that are Cassini's recordings:
// the list is read again first, and an id outside it is reported as failed,
// never forwarded. Never for another user, never as the recordings owner.
func (rt *Runtime) sharesAcceptHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/shares/accept" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	// USER-gated by the manifest: an absent identity means the AppAPI
	// middleware did not run, which is an outage, not an answer (D-701).
	caller := strings.TrimSpace(appapi.UserID(r.Context()))
	if caller == "" {
		writeJSONError(w, http.StatusBadGateway, "no verified caller identity")
		return
	}
	var request sharesAcceptRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShareAcceptBodyBytes))
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "the request body must be JSON with share_ids or all")
		return
	}
	if !request.All && len(request.ShareIDs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "name the shares to accept in share_ids, or send all: true")
		return
	}
	if len(request.ShareIDs) > maxShareAcceptIDs {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("at most %d shares can be accepted in one request", maxShareAcceptIDs))
		return
	}
	sink, ok := rt.sink().(*directSharesPublishSink)
	if !ok {
		writeJSONError(w, http.StatusConflict, "Only meetings published to Nextcloud are shared.")
		return
	}
	// Without the operator's records nothing can be told apart from a retired
	// meeting, so nothing is accepted: the same guard sharingForCaller applies.
	if rt.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "operations unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ctx := r.Context()
	waiting, ordered, err := rt.recordingSharesWaitingFor(ctx, sink, caller)
	if err != nil {
		rt.logger.Printf("accept shares for %s: %v", caller, err)
		writeJSONError(w, http.StatusBadGateway, "Cassini could not read the shares waiting for you. Try again.")
		return
	}
	response := sharesAcceptResponse{Accepted: []string{}, Failed: []shareAcceptFailure{}}
	seen := map[string]bool{}
	accept := func(ids []string) (accepted int) {
		for _, raw := range ids {
			id := strings.TrimSpace(raw)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if !waiting[id] {
				response.Failed = append(response.Failed, shareAcceptFailure{ShareID: id, Reason: shareAcceptNotWaiting})
				continue
			}
			if err := sink.cfg.acceptPendingShare(ctx, sink.client, caller, id); err != nil {
				rt.logger.Printf("accept share %s for %s: %v", id, caller, err)
				response.Failed = append(response.Failed, shareAcceptFailure{ShareID: id, Reason: shareRefusalReason(err)})
				continue
			}
			response.Accepted = append(response.Accepted, id)
			accepted++
		}
		return accepted
	}
	if !request.All {
		accept(request.ShareIDs)
	} else {
		// Everything waiting, at most maxShareAcceptIDs a round: after each
		// full round the list is read again, until nothing new waits. A share
		// Nextcloud refused stays pending, and is attempted once; a round
		// that accepts nothing ends it, so a share refused for good cannot
		// keep it going. Whatever was attempted is in accepted or failed;
		// what the round cap leaves untried is in neither, and the refreshed
		// view still lists it.
		for round := 0; round < maxShareAcceptRounds; round++ {
			if round > 0 {
				waiting, ordered, err = rt.recordingSharesWaitingFor(ctx, sink, caller)
				if err != nil {
					rt.logger.Printf("accept shares for %s: round %d: %v", caller, round+1, err)
					break
				}
			}
			untried := []string{}
			for _, id := range ordered {
				if !seen[id] {
					untried = append(untried, id)
				}
			}
			if len(untried) == 0 {
				break
			}
			full := len(untried) >= maxShareAcceptIDs
			if full {
				untried = untried[:maxShareAcceptIDs]
			}
			if accept(untried) == 0 || !full {
				break
			}
		}
	}
	response.Sharing = rt.sharingForCaller(ctx, caller)
	writeJSON(w, http.StatusOK, response)
}

// recordingSharesWaitingFor reads, as the caller, the pending shares that are
// Cassini's recordings: the set of their ids, and the ids in Nextcloud's
// order. Only these may be accepted.
func (rt *Runtime) recordingSharesWaitingFor(ctx context.Context, sink *directSharesPublishSink, caller string) (waiting map[string]bool, ordered []string, err error) {
	readCtx, cancel := context.WithTimeout(ctx, sharingReadTimeout)
	defer cancel()
	pending, err := sink.cfg.pendingShares(readCtx, sink.client, caller)
	if err != nil {
		return nil, nil, fmt.Errorf("pending shares: %w", err)
	}
	retired, err := rt.store.retiredMeetingJobs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("retired meetings: %w", err)
	}
	waiting = map[string]bool{}
	for _, entry := range groupPendingRecordingShares(pending, retired) {
		for _, id := range entry.ShareIDs {
			waiting[id] = true
			ordered = append(ordered, id)
		}
	}
	return waiting, ordered, nil
}
