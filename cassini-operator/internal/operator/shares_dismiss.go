package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cassini-operator/internal/operator/appapi"
)

// Dismissing a Browse notice (D-885). Browse tells a person, one meeting at a
// time, about each published recording Nextcloud would not share with them,
// and each recording they started that was not published because Nextcloud
// refused them. They may dismiss each notice, and a dismissed notice is never
// shown to them again. Only their own: the dismissal is keyed by the caller
// AppAPI names, and hides nothing from anyone else, administrators' run views
// included.

type sharesDismissRequest struct {
	JobIDs []string `json:"job_ids"`
}

type sharesDismissResponse struct {
	Dismissed []string `json:"dismissed"`
}

const (
	maxShareDismissIDs       = 100
	maxShareDismissBodyBytes = 16 << 10
)

func (rt *Runtime) sharesDismissHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/shares/dismiss" {
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
	var request sharesDismissRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShareDismissBodyBytes))
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "the request body must be JSON with job_ids")
		return
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, raw := range request.JobIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeJSONError(w, http.StatusBadRequest, "name the meetings to dismiss in job_ids")
		return
	}
	if len(ids) > maxShareDismissIDs {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("at most %d notices can be dismissed in one request", maxShareDismissIDs))
		return
	}
	if rt.store == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "operations unavailable")
		return
	}
	if err := rt.store.DismissShareNotices(r.Context(), caller, ids); err != nil {
		rt.logger.Printf("dismiss share notices for %s: %v", caller, err)
		writeJSONError(w, http.StatusInternalServerError, "Cassini could not dismiss the notice. Try again.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, sharesDismissResponse{Dismissed: ids})
}
