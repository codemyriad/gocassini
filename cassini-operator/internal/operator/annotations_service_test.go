package operator

import (
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

func TestNewAnnotationServiceMountsOnlyWhereAMarkCanBeServed(t *testing.T) {
	rt := &Runtime{}
	rt.cfg.CassiniBin = "cassini"
	nc := testExAppConfig("http://nextcloud.invalid")
	nc.PublishSink = publishSinkNextcloudFiles
	if newAnnotationService(rt, nc, nil) == nil {
		t.Fatal("an AppAPI deployment on the Nextcloud sink with a CLI must mount the routes")
	}
	local := nc
	local.PublishSink = publishSinkLocal
	if newAnnotationService(rt, local, nil) != nil {
		t.Fatal("the local sink has no Nextcloud to read as the caller — the routes must not mount")
	}
	if newAnnotationService(rt, ExAppConfig{PublishSink: publishSinkNextcloudFiles}, nil) != nil {
		t.Fatal("outside AppAPI there is no verified caller — the routes must not mount")
	}
	noCLI := &Runtime{}
	if newAnnotationService(noCLI, nc, nil) != nil {
		t.Fatal("without the CLI there is nothing to rewrite a recording with")
	}
}

func TestAnnotationRoutes(t *testing.T) {
	s := &annotationService{rt: &Runtime{}, logger: log.New(ioDiscard{}, "", 0)}
	mux := http.NewServeMux()
	s.register(mux)

	const id = "01M25DX5Q0SASAXEZFVF0X2RJ1"
	cases := []struct {
		name, method, path, caller string
		want                       int
	}{
		{"tags without a caller is an outage", http.MethodGet, "/annotations/tags", "", http.StatusBadGateway},
		{"tags without a projection", http.MethodGet, "/annotations/tags", "alice", http.StatusServiceUnavailable},
		{"tags trailing slash", http.MethodGet, "/annotations/tags/", "alice", http.StatusServiceUnavailable},
		{"tags are read-only", http.MethodPost, "/annotations/tags", "alice", http.StatusMethodNotAllowed},
		{"read a meeting", http.MethodGet, "/annotations/meetings/" + id, "alice", http.StatusNotImplemented},
		{"write a meeting", http.MethodPost, "/annotations/meetings/" + id, "alice", http.StatusNotImplemented},
		{"meeting without a caller", http.MethodPost, "/annotations/meetings/" + id, "", http.StatusBadGateway},
		{"no PUT", http.MethodPut, "/annotations/meetings/" + id, "alice", http.StatusMethodNotAllowed},
		{"no meeting id", http.MethodGet, "/annotations/meetings/", "alice", http.StatusNotFound},
		{"nested path", http.MethodGet, "/annotations/meetings/" + id + "/x", "alice", http.StatusNotFound},
		{"unknown resource", http.MethodGet, "/annotations/other", "alice", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.caller != "" {
				req = req.WithContext(appapi.WithUserID(req.Context(), tc.caller))
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s %s: want %d, got %d (%s)", tc.method, tc.path, tc.want, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); tc.want != http.StatusNotFound && got != "no-store" {
				t.Fatalf("per-caller answers must be uncacheable, got Cache-Control %q", got)
			}
		})
	}
}
