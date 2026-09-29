package operator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type storageServiceAccount struct {
	User     string `json:"user"`
	Known    bool   `json:"known"`
	Exists   bool   `json:"exists"`
	ResetOcc string `json:"reset_occ"`
}

type storageStatusResponse struct {
	ServiceAccount storageServiceAccount `json:"service_account"`
	OK             bool                  `json:"ok"`
	State          string                `json:"state"`
	Step           string                `json:"step,omitempty"`
	Detail         string                `json:"detail,omitempty"`
	CheckedAt      string                `json:"checked_at,omitempty"`
	Setup          []storageSetupStep    `json:"setup"`
}

func (c ExAppConfig) storageHandler(rt *Runtime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/storage" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, c.storageStatus(rt))
		case http.MethodPost:
			var body struct {
				Action string `json:"action"`
			}
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil || json.Unmarshal(raw, &body) != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid storage action")
				return
			}
			switch body.Action {
			case "recheck":
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), ncProvisionTimeout)
				defer cancel()
				c.preflightDirectShares(ctx, rt.logger)
			default:
				writeJSONError(w, http.StatusGone, "this storage action is retired")
				return
			}
			writeJSON(w, http.StatusOK, c.storageStatus(rt))
		default:
			writeMethodNotAllowed(w, http.MethodGet+", "+http.MethodPost)
		}
	})
}

func (c ExAppConfig) storageStatus(rt *Runtime) storageStatusResponse {
	access := ncAccessSubstrate.snapshot(rt.resolvedPublishSinkName())
	probe, probed := ncAccessSubstrate.lastProbe()
	return storageStatusResponse{
		ServiceAccount: storageServiceAccount{User: ncRecordingsOwner, Known: probed, Exists: probed && probe.ServiceAccount, ResetOcc: "occ user:resetpassword " + ncRecordingsOwner},
		OK:             access.OK, State: access.State, Step: access.Step, Detail: access.Detail, CheckedAt: access.CheckedAt,
		Setup: storageSetupPlan(probe),
	}
}
