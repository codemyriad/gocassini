package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// AppAPI lifecycle callbacks. These are invoked by AppAPI directly (not by
// browser traffic through the proxy) but they still pass through the AppAPI
// auth middleware because AppAPI signs them with AUTHORIZATION-APP-API.
//
//   PUT  /enabled?enabled=1   admin enabled the app, return {"error": ""}
//   PUT  /enabled?enabled=0   admin disabled the app, return {"error": ""}
//   POST /init                run any first-time setup, return {}
//
// Both endpoints are stateless: nothing reads the enabled flag or the init
// time back, so neither is stored and a re-run answers the same way.

// LifecycleHandlers wires PUT /enabled and POST /init onto a mux.
type LifecycleHandlers struct {
	Logger *log.Logger
	// InitProgressReporter is called once /init has run, in a goroutine so
	// the HTTP response can return immediately. It signals progress=100 to
	// AppAPI via the OCS endpoint. Nil disables the callback (dev / tests).
	InitProgressReporter func()
	// UIRegistrar is called after PUT /enabled?enabled=1 has been answered,
	// in a goroutine for the same reason as InitProgressReporter: it calls
	// back into Nextcloud (AppAPI's ExApp UI OCS API, see uiRegistrar in
	// exapp.go), which can deadlock a single-worker PHP setup if done before
	// the /enabled response is written. Nil disables it (dev / tests).
	UIRegistrar func()
	// EnabledCallback runs asynchronously after an enabled/disabled request
	// was answered. ExApp integrations use the enabled=true edge for
	// callbacks that AppAPI would reject while registration is still disabled.
	EnabledCallback func(bool)
}

func (h *LifecycleHandlers) logger() *log.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return log.Default()
}

// Register attaches the lifecycle endpoints to the given mux at the root.
// AppAPI calls these on the container itself, NOT through the operator's
// CASSINI_OPERATOR_BASE_PATH prefix.
func (h *LifecycleHandlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("/enabled", h.handleEnabled)
	mux.HandleFunc("/init", h.handleInit)
}

func (h *LifecycleHandlers) handleEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	enabledRaw := strings.TrimSpace(r.URL.Query().Get("enabled"))
	if enabledRaw == "" {
		// AppAPI normally sends `?enabled=1` or `?enabled=0`, but some
		// versions post the flag as a JSON body. Accept either.
		var body struct {
			Enabled any `json:"enabled"`
		}
		if r.Body != nil {
			defer r.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			if err == nil && len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
				enabledRaw = fmt.Sprintf("%v", body.Enabled)
			}
		}
	}

	enabled, err := parseEnabledFlag(enabledRaw)
	if err != nil {
		h.logger().Printf("lifecycle enabled: bad flag %q: %v", enabledRaw, err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.logger().Printf("lifecycle enabled -> %v", enabled)
	// AppAPI expects `{"error": ""}` on success, `{"error": "..."}` to refuse.
	writeJSON(w, http.StatusOK, map[string]string{"error": ""})
	// AppAPI's lifecycle docs have ExApps register their UI (top-menu
	// entries) "during the enabled method call". Registration is an upsert,
	// so re-enabling just refreshes it; on disable AppAPI hides the entries
	// itself, so there is nothing to undo.
	if enabled && h.UIRegistrar != nil {
		go h.UIRegistrar()
	}
	if h.EnabledCallback != nil {
		go h.EnabledCallback(enabled)
	}
}

func (h *LifecycleHandlers) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.logger().Printf("lifecycle init: ok")
	// AppAPI doesn't require a specific body shape on init success — empty is fine.
	writeJSON(w, http.StatusOK, struct{}{})
	// AppAPI's --wait-finish polls until init_progress=100; the container has
	// to report that back via OCS. We have no async setup work, so signal 100
	// immediately, in a goroutine so the response above isn't blocked.
	if h.InitProgressReporter != nil {
		go h.InitProgressReporter()
	}
}

func parseEnabledFlag(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, errors.New("missing enabled flag")
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	// fall back to strconv for any other boolean-like input
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid enabled flag %q", raw)
	}
	return b, nil
}
