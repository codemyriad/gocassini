package operator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteRetentionCapabilityFailsClosed(t *testing.T) {
	for _, scenario := range []string{"home", "access-control", "encryption", "external", "object", "missing-evidence", "unsupported-version", "shared-root"} {
		t.Run(scenario, func(t *testing.T) {
			resetProvisioningUser(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data := `{"users":["admin"]}`
				switch {
				case strings.Contains(r.URL.Path, "capabilities"):
					major := 35
					if scenario == "unsupported-version" {
						major = 36
					}
					data = fmt.Sprintf(`{"version":{"major":%d,"minor":0,"micro":0}}`, major)
				case r.URL.Path == "/ocs/v2.php/cloud/apps":
					app := "files_versions"
					switch scenario {
					case "access-control":
						app = "files_accesscontrol"
					case "encryption":
						app = "encryption"
					case "external":
						app = "files_external"
					}
					data = fmt.Sprintf(`{"apps":[%q]}`, app)
				case strings.Contains(r.URL.Path, "serverinfo"):
					if scenario == "missing-evidence" {
						w.WriteHeader(404)
						return
					}
					other := 0
					if scenario == "object" {
						other = 1
					}
					data = fmt.Sprintf(`{"nextcloud":{"storage":{"num_storages_home":1,"num_storages_other":%d}}}`, other)
				case r.Method == "PROPFIND":
					mount := ""
					if scenario == "shared-root" {
						mount = "shared"
					}
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns"><d:response><d:propstat><d:status>HTTP/1.1 200 OK</d:status><d:prop><oc:owner-id>cassini</oc:owner-id><nc:mount-type>%s</nc:mount-type></d:prop></d:propstat></d:response></d:multistatus>`, mount)
					return
				}
				fmt.Fprintf(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":%s}}`, data)
			}))
			defer server.Close()
			s := &annotationService{exapp: testExAppConfig(server.URL), client: server.Client()}
			err := s.remoteRetentionCapability(context.Background())
			if (err == nil) != (scenario == "home") {
				t.Fatalf("capability %s: %v", scenario, err)
			}
		})
	}
}
