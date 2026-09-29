package operator

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeNCFiles models the DAV operations used by direct-share publication.
type fakeNCFiles struct {
	mu          sync.Mutex
	files       map[string][]byte
	failPUT     map[string]int
	truncatePUT map[string]int
	etags       map[string]int
	putIfMatch  []string
}

func newFakeNCFiles() *fakeNCFiles {
	return &fakeNCFiles{files: map[string][]byte{}, failPUT: map[string]int{}, truncatePUT: map[string]int{}, etags: map[string]int{}}
}

func (f *fakeNCFiles) etagFor(rel string) string { return strconv.Quote(strconv.Itoa(f.etags[rel])) }

func (f *fakeNCFiles) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Path
		if i := strings.Index(rel, "/Cassini"); i >= 0 {
			rel = rel[i+1:]
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case "PROPFIND":
			body, ok := f.files[rel]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><oc:fileid>42</oc:fileid><d:getetag>%s</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, r.URL.Path, len(body), f.etagFor(rel))
		case http.MethodPut:
			if f.failPUT[rel] > 0 {
				f.failPUT[rel]--
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusInsufficientStorage)
				return
			}
			if match := r.Header.Get("If-Match"); match != "" {
				f.putIfMatch = append(f.putIfMatch, match)
				if _, ok := f.files[rel]; !ok || match != f.etagFor(rel) {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
			}
			body, _ := io.ReadAll(r.Body)
			if n, ok := f.truncatePUT[rel]; ok && n < len(body) {
				body = body[:n]
			}
			_, existed := f.files[rel]
			f.files[rel] = body
			f.etags[rel]++
			w.Header().Set("ETag", f.etagFor(rel))
			if existed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		case http.MethodGet:
			if match := r.Header.Get("If-Match"); match != "" && match != f.etagFor(rel) {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			body, ok := f.files[rel]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
