package operator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyRetentionReadsDeliveredTimestamps(t *testing.T) {
	for _, scenario := range []string{"success", "changed identity", "changed etag", "missing createdAtUtc"} {
		t.Run(scenario, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			dates := `{"createdAtUtc":"2026-09-29T12:00:00Z","recordedAtLocal":"2026-03-05T23:59:59"}`
			if scenario == "missing createdAtUtc" {
				dates = `{"recordedAtLocal":"2026-03-05T23:59:59"}`
			}
			rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s' '"+dates+"'\n")
			gets := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "PROPFIND":
					id := 42
					if scenario == "changed identity" {
						id = 43
					}
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:fileid>%d</oc:fileid><d:getetag>"original"</d:getetag><d:getcontentlength>5</d:getcontentlength></d:prop></d:propstat></d:response></d:multistatus>`, r.URL.Path, id)
				case "GET":
					gets++
					if r.Header.Get("If-Match") != `"original"` {
						t.Error("timestamp read was not conditional")
					}
					if scenario == "changed etag" {
						w.WriteHeader(412)
						return
					}
					fmt.Fprint(w, "audio")
				default:
					t.Errorf("unexpected mutation %s", r.Method)
					w.WriteHeader(405)
				}
			}))
			defer server.Close()
			service := &annotationService{rt: rt, exapp: testExAppConfig(server.URL), client: server.Client()}
			m := meetingLifecycle{Name: "m.opus", FileID: 42, Path: ncRecordingsRoot + "/meetings/m.opus", State: "active", Anchor: "2026-09-29T12:00:00Z", AnchorSource: "recording-completed"}
			err := service.loadMeetingRetentionAge(context.Background(), &m)
			if scenario != "success" {
				if err == nil {
					t.Fatal("unsafe timestamps accepted")
				}
				if scenario == "changed identity" && gets != 0 {
					t.Fatal("downloaded replacement")
				}
				return
			}
			if err != nil || m.Anchor != "2026-03-05T23:59:59Z" || m.AnchorSource != "recordedAtLocal" {
				t.Fatal(m, err)
			}
			if err := service.loadMeetingRetentionAge(context.Background(), &m); err != nil || gets != 1 {
				t.Fatal("original timestamps not reused", gets, err)
			}
		})
	}
}
