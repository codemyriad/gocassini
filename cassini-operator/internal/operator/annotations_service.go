package operator

import (
	"log"
	"net/http"
	"strings"

	"cassini-operator/internal/operator/appapi"
)

// annotationsURLPath is on the ROOT mux, beside insights, where
// appinfo/info.xml declares it — `^annotations\/…`, USER (D-737).
const annotationsURLPath = "/annotations"

// annotationService serves a meeting's annotations and the tag vocabulary.
// rt.annotations is the rebuildable projection, and may be nil.
type annotationService struct {
	rt     *Runtime
	exapp  ExAppConfig
	bin    string
	client *http.Client
	logger *log.Logger
	styles *tagStyleStore
	jobs   tagJobs
}

// newAnnotationService returns nil where no mark can be served, as
// newInsightService decides: outside AppAPI there is no verified caller, under
// the local sink no Nextcloud, and with no CLI nothing to rewrite a recording.
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
		// As the read proxy: recordings stream, so the request context governs.
		client: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: ncFilesProxyHeadersTTL}},
		logger: logger,
		styles: newTagStyleStore(rt.cfg),
	}
}

// register mounts the routes; every answer is per-caller, so uncacheable.
func (s *annotationService) register(root *http.ServeMux) {
	root.HandleFunc(annotationsURLPath+"/", insightNoStore(s.route))
}

// route resolves the caller once, so no handler can forget to.
func (s *annotationService) route(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, annotationsURLPath), "/")
	resource, id, _ := strings.Cut(rest, "/")
	switch {
	case resource == "tags":
		caller, ok := s.caller(w, r)
		if !ok {
			return
		}
		s.routeTags(w, r, caller, id)
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
