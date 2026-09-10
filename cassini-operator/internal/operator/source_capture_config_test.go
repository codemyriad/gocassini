package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncSourceCaptureInitialStateWritesTheExAppConfig(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			var got map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != appAPIAppConfigPath {
					t.Fatalf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("EX-APP-ID") != "gocassini" {
					t.Fatalf("EX-APP-ID = %q", r.Header.Get("EX-APP-ID"))
				}
				wantAuth := base64.StdEncoding.EncodeToString([]byte(":" + "sekret"))
				if r.Header.Get("AUTHORIZATION-APP-API") != wantAuth {
					t.Fatalf("AUTHORIZATION-APP-API = %q", r.Header.Get("AUTHORIZATION-APP-API"))
				}
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			cfg := testExAppConfig(server.URL)
			if err := cfg.syncSourceCaptureInitialState(context.Background(), enabled, log.New(io.Discard, "", 0)); err != nil {
				t.Fatalf("sync: %v", err)
			}
			if got["configKey"] != sourceCaptureAppConfigKey || got["configValue"] != map[bool]string{false: "false", true: "true"}[enabled] || got["sensitive"] != float64(0) {
				t.Fatalf("payload = %#v", got)
			}
		})
	}
}

func TestSyncSourceCaptureInitialStateReportsARefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer server.Close()

	err := testExAppConfig(server.URL).syncSourceCaptureInitialState(
		context.Background(), true, log.New(io.Discard, "", 0),
	)
	if err == nil {
		t.Fatal("sync accepted a refused write")
	}
}
