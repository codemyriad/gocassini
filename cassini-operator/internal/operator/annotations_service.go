package operator

import (
	"log"
	"net/http"
	"strings"

	"cassini-operator/internal/operator/appapi"
)

// annotationsURLPath is where the tags-and-marks routes are mounted: their own
// top-level prefix on the ROOT mux, beside insights, because that is where
// appinfo/info.xml declares them — `^annotations\/…`, USER (D-737).
const annotationsURLPath = "/annotations"

// annotationService serves a meeting's annotations and the tag vocabulary.
//
// Reads go through Nextcloud as the caller, so Nextcloud re-checks the ACL on
// the bytes. Writes are made by the service account after the same visibility
// check every archive read makes, because the recordings mount gives ordinary
// users a READ ceiling. The recording file is always the record; rt.annotations
// is only its rebuildable projection, and may be nil.
type annotationService struct {
	rt     *Runtime
	exapp  ExAppConfig
	bin    string
	client *http.Client
	logger *log.Logger
}

// newAnnotationService returns the service, or nil when this deployment cannot
// serve a mark at all — decided exactly as newInsightService and
// meetingsContextHandler decide. Outside an AppAPI deployment there is no
// verified caller to attribute a mark to; under the local sink there is no
// Nextcloud to read as the caller or to write into; with no CLI there is
// nothing to rewrite a recording with. In each case the routes are simply not
// mounted, which is the answer the rest of the app gives there.
func newAnnotationService(rt *Runtime, exapp ExAppConfig, logger *log.Logger) *annotationService {
	if rt == nil || !exapp.appAPIActive() || exapp.PublishSink != publishSinkNextcloudFiles {
		return nil
	}
	if strings.TrimSpace(rt.cfg.CassiniBin) == "" {
		if logger != nil {
			logger.Printf("annotations: no cassini binary configured — %s is not served", annotationsURLPath)
		}
		return nil
	}
	return &annotationService{
		rt:    rt,
		exapp: exapp,
		bin:   rt.cfg.CassiniBin,
		// Same client shape as the read proxy: no overall timeout, because
		// recordings stream and the request context governs; a hung upstream is
		// bounded on headers.
		client: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}},
		logger: logger,
	}
}

// register mounts the routes. Every answer is per-caller, so every answer is
// uncacheable, for the reasons insightNoStore gives.
func (s *annotationService) register(root *http.ServeMux) {
	root.HandleFunc(annotationsURLPath+"/", insightNoStore(s.route))
}

// route dispatches the two resources. The caller is resolved here, once, so no
// handler can forget to.
func (s *annotationService) route(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, annotationsURLPath), "/")
	resource, id, _ := strings.Cut(rest, "/")
	switch {
	case resource == "tags" && id == "":
		caller, ok := s.caller(w, r)
		if !ok {
			return
		}
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		s.serveTags(w, r, caller)
	case resource == "meetings" && id != "" && !strings.Contains(id, "/"):
		if !isPlainMeetingID(id) {
			// A statement about the id, not about what exists.
			writeJSONError(w, http.StatusBadRequest, "that is not a meeting id")
			return
		}
		caller, ok := s.caller(w, r)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.readMeeting(w, r, caller, id)
		case http.MethodPost:
			s.writeMeeting(w, r, caller, id)
		default:
			writeMethodNotAllowed(w, "GET, POST")
		}
	default:
		http.NotFound(w, r)
	}
}

// caller resolves the verified identity, or refuses. These routes are USER-gated
// by the manifest, so an absent identity means the AppAPI middleware did not run
// — an outage, not an answer (D-701).
func (s *annotationService) caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	caller := appapi.UserID(r.Context())
	if caller == "" {
		s.logf("annotations: missing caller identity on %s %s — refusing to answer", r.Method, r.URL.Path)
		writeJSONError(w, http.StatusBadGateway, "no verified caller identity")
		return "", false
	}
	return caller, true
}

func (s *annotationService) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}
