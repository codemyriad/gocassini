package operator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetentionDAVMutation(t *testing.T) {
	for _, method := range []string{"MOVE", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			status := http.StatusNoContent
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != method || r.Header.Get("If-Match") != `"current"` || r.Header.Get("AUTHORIZATION-APP-API") == "" {
					t.Errorf("missing method/precondition/auth")
				}
				if method == "MOVE" && (r.Header.Get("Overwrite") != "F" || !strings.HasSuffix(r.Header.Get("Destination"), "/new.cassini.transcription.json")) {
					t.Errorf("unsafe MOVE headers")
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()
			cfg := testExAppConfig(srv.URL)
			rel := ncRecordingsRoot + "/meetings/old.opus"
			dest := ncRecordingsRoot + "/meetings/new.cassini.transcription.json"
			if err := cfg.davRetentionMutation(context.Background(), srv.Client(), method, rel, dest, `"current"`); err != nil {
				t.Fatal(err)
			}
			status = http.StatusPreconditionFailed
			if err := cfg.davRetentionMutation(context.Background(), srv.Client(), method, rel, dest, `"current"`); !errors.Is(err, errDAVPreconditionFailed) {
				t.Fatal(err)
			}
			for _, etag := range []string{"", "*", `W/"weak"`, "\"a\r\nb\""} {
				if err := cfg.davRetentionMutation(context.Background(), srv.Client(), method, rel, dest, etag); err == nil {
					t.Fatal("accepted unsafe etag")
				}
			}
			for _, bad := range []string{"other/file", ncRecordingsRoot + "/meetings/../other", ncRecordingsRoot + "/meetings/a/b", ncRecordingsRoot + "/meetings/a\\b"} {
				if err := cfg.davRetentionMutation(context.Background(), srv.Client(), method, bad, dest, `"current"`); err == nil {
					t.Fatal("accepted unsafe path")
				}
			}
			if calls != 2 {
				t.Fatalf("invalid input reached network: %d", calls)
			}
		})
	}
}

func TestRetentionDAVRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed with credentials") }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	cfg := testExAppConfig(srv.URL)
	rel := ncRecordingsRoot + "/meetings/test.opus"
	if err := cfg.davRetentionMutation(context.Background(), srv.Client(), "DELETE", rel, "", `"a"`); err == nil {
		t.Fatal("accepted redirect")
	}
	if _, err := cfg.davRetentionLeaf(context.Background(), srv.Client(), rel); err == nil {
		t.Fatal("accepted redirect")
	}
	if err := cfg.davRetentionPut(context.Background(), srv.Client(), rel, writeLocal(t, "{}"), `"a"`); err == nil {
		t.Fatal("accepted redirect")
	}
}

func TestRetentionDAVLeaf(t *testing.T) {
	rel := ncRecordingsRoot + "/meetings/test.opus"
	href := "/remote.php/dav/files/cassini/" + rel
	propStatus := "HTTP/1.1 200 OK"
	etag := "&quot;current&quot;"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:status>%s</d:status><d:prop><oc:fileid>42</oc:fileid><d:getetag>%s</d:getetag><d:getcontentlength>123</d:getcontentlength></d:prop></d:propstat></d:response></d:multistatus>`, href, propStatus, etag)
	}))
	defer srv.Close()
	cfg := testExAppConfig(srv.URL)
	state, err := cfg.davRetentionLeaf(context.Background(), srv.Client(), rel)
	if err != nil || state.FileID != 42 || state.ETag != `"current"` || state.Size != 123 {
		t.Fatalf("%+v %v", state, err)
	}
	for _, bad := range []string{"", "https://evil.example" + href, "/remote.php/dav/files/other/" + rel, href + "?x=1", href + "/other"} {
		original := href
		href = bad
		if _, err := cfg.davRetentionLeaf(context.Background(), srv.Client(), rel); err == nil {
			t.Fatalf("accepted href %s", bad)
		}
		href = original
	}
	propStatus = "HTTP/1.1 404 Not Found"
	if _, err := cfg.davRetentionLeaf(context.Background(), srv.Client(), rel); err == nil {
		t.Fatal("accepted failed properties")
	}
	propStatus = "HTTP/1.1 200 OK"
	etag = "W/&quot;weak&quot;"
	if _, err := cfg.davRetentionLeaf(context.Background(), srv.Client(), rel); err == nil {
		t.Fatal("accepted weak etag")
	}
}
