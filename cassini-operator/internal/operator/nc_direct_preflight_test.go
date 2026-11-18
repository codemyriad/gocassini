package operator

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flakyNextcloud is a Nextcloud whose storage-check endpoints can each be made
// to fail, the way a Nextcloud that is still starting fails them.
type flakyNextcloud struct {
	mu           sync.Mutex
	userBody     string
	userStatus   int    // GET /cloud/users/cassini (0 = the account exists)
	createStatus int    // POST /cloud/users (0 = "already exists")
	shareStatus  int    // GET shares (0 = ok)
	shareBody    string // body when shareStatus != 0
	shareDelay   time.Duration
	davStatus    int // MKCOL (0 = created)
	requests     atomic.Int64
}

func (f *flakyNextcloud) set(change func(*flakyNextcloud)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

func (f *flakyNextcloud) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		f.mu.Lock()
		userStatus, userBody, createStatus, shareStatus, shareBody, shareDelay, davStatus := f.userStatus, f.userBody, f.createStatus, f.shareStatus, f.shareBody, f.shareDelay, f.davStatus
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/ocs/v2.php/apps/app_api/api/v1/users":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":["admin","cassini"]}}`)
		case r.URL.Path == "/ocs/v2.php/cloud/groups/admin":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["admin"]}}}`)
		case r.URL.Path == "/ocs/v2.php/cloud/users/cassini" && r.Method == http.MethodGet:
			if userStatus != 0 {
				w.WriteHeader(userStatus)
				_, _ = io.WriteString(w, userBody)
				return
			}
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"cassini"}}}`)
		case r.URL.Path == "/ocs/v2.php/cloud/users" && r.Method == http.MethodPost:
			if createStatus != 0 {
				w.WriteHeader(createStatus)
				_, _ = io.WriteString(w, `<html>refused</html>`)
				return
			}
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":102,"message":"User already exists"}}}`)
		case r.Method == "MKCOL":
			if davStatus != 0 {
				w.WriteHeader(davStatus)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns"><d:response><d:href>/</d:href><d:propstat><d:prop><oc:owner-id>cassini</oc:owner-id><nc:mount-type></nc:mount-type></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
		case r.URL.Path == "/ocs/v2.php/apps/files_sharing/api/v1/shares":
			if shareDelay > 0 {
				select {
				case <-time.After(shareDelay):
				case <-r.Context().Done():
					return
				}
			}
			if shareStatus != 0 {
				w.WriteHeader(shareStatus)
				_, _ = io.WriteString(w, shareBody)
				return
			}
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":[]}}`)
		default:
			// Sharing-policy reads and the like: unreadable, which is advisory.
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func flakyNextcloudConfig(t *testing.T, f *flakyNextcloud) ExAppConfig {
	t.Helper()
	resetDirectSubstrate(t)
	resetProvisioningUser(t)
	t.Setenv(envNCAdminUser, "")
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return testExAppConfig(srv.URL)
}

const missingAccountBody = `{"ocs":{"meta":{"status":"failure","statuscode":998},"data":[]}}`

const shareAPIDisabledBody = `{"ocs":{"meta":{"status":"failure","statuscode":404,"message":"Share API is disabled"},"data":[]}}`

// A Nextcloud that is busy or still starting says nothing about the recordings
// account or the share API. Reading its 5xx or 429 as "the account does not
// exist" or "sharing is off" refused recordings and sent the administrator to
// occ user:add. Only a definite answer may refuse.
func TestStorageCheckRefusesRecordingOnlyForADefiniteAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *flakyNextcloud
		want ncSubstrateState
		step string
	}{
		{"account lookup 503", &flakyNextcloud{userStatus: 503}, ncSubstrateDegraded, "nextcloud_probe"},
		{"account lookup proxy 404", &flakyNextcloud{userStatus: 404, userBody: "<html>Not Found</html>"}, ncSubstrateDegraded, "nextcloud_probe"},
		{"account lookup proxy 200", &flakyNextcloud{userStatus: 200, userBody: "{}"}, ncSubstrateDegraded, "nextcloud_probe"},
		{"account lookup unauthorized", &flakyNextcloud{userStatus: 401}, ncSubstrateDegraded, "nextcloud_probe"},
		{"account lookup 429", &flakyNextcloud{userStatus: 429}, ncSubstrateDegraded, "nextcloud_probe"},
		{"account lookup 503 after create 502", &flakyNextcloud{userStatus: 503, createStatus: 502}, ncSubstrateDegraded, "nextcloud_probe"},
		{"share list 502 proxy page", &flakyNextcloud{shareStatus: 502, shareBody: `<html>Bad Gateway</html>`}, ncSubstrateDegraded, "sharing_api"},
		{"share list 503 maintenance page", &flakyNextcloud{shareStatus: 503, shareBody: `<html>maintenance</html>`}, ncSubstrateDegraded, "sharing_api"},
		{"share list 503 OCS", &flakyNextcloud{shareStatus: 503, shareBody: `{"ocs":{"meta":{"status":"failure","statuscode":503,"message":"x"},"data":[]}}`}, ncSubstrateDegraded, "sharing_api"},
		{"share list OCS 503 over HTTP 200", &flakyNextcloud{shareStatus: 200, shareBody: `{"ocs":{"meta":{"status":"failure","statuscode":503}}}`}, ncSubstrateDegraded, "sharing_api"},
		{"share list proxy 404 JSON", &flakyNextcloud{shareStatus: 404, shareBody: "{}"}, ncSubstrateDegraded, "sharing_api"},
		{"share list 429", &flakyNextcloud{shareStatus: 429}, ncSubstrateDegraded, "sharing_api"},
		// Definite answers keep their state and remedy.
		{"share API disabled", &flakyNextcloud{shareStatus: 404, shareBody: shareAPIDisabledBody}, ncSubstrateUnavailable, "sharing_api"},
		{"account missing, create refused 403", &flakyNextcloud{userStatus: 404, userBody: missingAccountBody, createStatus: 403}, ncSubstrateUnavailable, storageStepServiceAccount},
		{"account missing, create failed 500", &flakyNextcloud{userStatus: 404, userBody: missingAccountBody, createStatus: 500}, ncSubstrateUnavailable, storageStepServiceAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := flakyNextcloudConfig(t, tc.f)
			cfg.preflightDirectShares(context.Background(), discardLogger())
			snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
			if snap.State != string(tc.want) || snap.Step != tc.step {
				t.Fatalf("state=%q step=%q detail=%q, want %q at %q", snap.State, snap.Step, snap.Detail, tc.want, tc.step)
			}
			refused := ncAccessSubstrate.recordingRefusal() != ""
			if refused != (tc.want == ncSubstrateUnavailable) {
				t.Fatalf("recording refused = %v for %q", refused, tc.want)
			}
			if tc.step == storageStepServiceAccount && tc.f.createStatus == 403 && !strings.Contains(snap.Detail, "HTTP 403") {
				t.Fatalf("the create refusal is missing from %q", snap.Detail)
			}
		})
	}

	t.Run("deadline during the share call", func(t *testing.T) {
		cfg := flakyNextcloudConfig(t, &flakyNextcloud{shareDelay: 2 * time.Second})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		cfg.preflightDirectShares(ctx, discardLogger())
		if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.State != string(ncSubstrateDegraded) || ncAccessSubstrate.recordingRefusal() != "" {
			t.Fatalf("got %+v", snap)
		}
	})
}

// After a server reboot Cassini often starts before Nextcloud answers, and
// the one boot check leaves storage "degraded" with a notice telling everyone
// new recordings can't be saved. The storage-usage tick looks again, but only
// in that state: a healthy install costs no request, and a real fault is not
// retried on a timer.
func TestRecheckDegradedStorageOnlyRetriesANextcloudThatDidNotAnswer(t *testing.T) {
	f := &flakyNextcloud{userStatus: 503}
	cfg := flakyNextcloudConfig(t, f)
	cfg.preflightDirectShares(context.Background(), discardLogger())
	if snap := ncAccessSubstrate.snapshot(""); snap.State != string(ncSubstrateDegraded) || snap.Step != "nextcloud_probe" {
		t.Fatalf("setup: %+v", snap)
	}
	f.set(func(f *flakyNextcloud) { f.userStatus = 0 })

	// Another check holds the lock: the tick skips rather than queueing.
	provisionMu.Lock()
	before := f.requests.Load()
	cfg.recheckDegradedStorage(context.Background(), discardLogger())
	provisionMu.Unlock()
	if f.requests.Load() != before {
		t.Fatal("the tick ran while another check held the lock")
	}

	cfg.recheckDegradedStorage(context.Background(), discardLogger())
	if !ncAccessSubstrate.usable() {
		t.Fatalf("not recovered: %+v", ncAccessSubstrate.snapshot(""))
	}

	before = f.requests.Load()
	cfg.recheckDegradedStorage(context.Background(), discardLogger())
	if f.requests.Load() != before {
		t.Fatal("the tick probed a provisioned install")
	}

	for name, broken := range map[string]*flakyNextcloud{
		"unavailable: share API disabled": {shareStatus: 404, shareBody: shareAPIDisabledBody},
		"degraded at another step":        {davStatus: http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := flakyNextcloudConfig(t, broken)
			cfg.preflightDirectShares(context.Background(), discardLogger())
			if snap := ncAccessSubstrate.snapshot(""); snap.State == string(ncSubstrateProvisioned) || snap.Step == "nextcloud_probe" {
				t.Fatalf("setup: %+v", snap)
			}
			before := broken.requests.Load()
			cfg.recheckDegradedStorage(context.Background(), discardLogger())
			if broken.requests.Load() != before {
				t.Fatal("the tick retried a fault that is not a Nextcloud that did not answer")
			}
		})
	}

	t.Run("not checked yet", func(t *testing.T) {
		f := &flakyNextcloud{}
		cfg := flakyNextcloudConfig(t, f)
		cfg.recheckDegradedStorage(context.Background(), discardLogger())
		if f.requests.Load() != 0 {
			t.Fatal("the tick ran the first check; that is the boot check's job")
		}
	})
}

func TestLocalPublishSinkSkipsNextcloudStorageReadinessCheck(t *testing.T) {
	f := &flakyNextcloud{}
	cfg := flakyNextcloudConfig(t, f)
	t.Setenv(envAppSecret, cfg.AppSecret)
	t.Setenv(envAppID, cfg.AppID)
	t.Setenv(envNextcloudURL, cfg.NextcloudURL)

	rt := &Runtime{cfg: Config{PublishSink: publishSinkLocal, DBPath: t.TempDir() + "/jobs.sqlite3"}, logger: discardLogger()}
	rt.checkRecordingReadinessScoped(context.Background(), readinessScope{storage: true})
	if count := f.requests.Load(); count != 0 {
		t.Fatalf("local publish sink sent %d storage requests to Nextcloud", count)
	}
}
