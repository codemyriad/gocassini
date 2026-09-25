package operator

import (
	"net/http"
	"net/http/httptest"

	"cassini-operator/internal/operator/appapi"
)

func callerReq(method, target, caller string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	if caller != "" {
		req = req.WithContext(appapi.WithUserID(req.Context(), caller))
	}
	return req
}
