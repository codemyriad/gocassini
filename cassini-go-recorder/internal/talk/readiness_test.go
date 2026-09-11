package talk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gocassini/internal/config"
)

func TestProbeConnectionNeverJoinsOrCaptures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		server    bool
		authError bool
		mcu       bool
		code      string
	}{
		{"healthy", 200, true, false, true, "hpb_authenticated"},
		{"missing HPB", 200, false, false, false, "hpb_missing"},
		{"wrong recording credential", 403, false, false, false, "recording_auth_rejected"},
		{"wrong internal credential", 200, true, true, true, "signaling_auth_failed"},
		{"no media backend", 200, true, false, false, "hpb_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hello atomic.Int32
			var unexpected atomic.Int32
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/spreed" {
					upgrader := websocket.Upgrader{}
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					for {
						var msg map[string]any
						if conn.ReadJSON(&msg) != nil {
							return
						}
						if msg["type"] != "hello" {
							unexpected.Add(1)
							return
						}
						hello.Add(1)
						if tc.authError {
							_ = conn.WriteJSON(map[string]any{"id": msg["id"], "type": "error", "error": map[string]any{"code": "auth_failed", "message": "private upstream detail"}})
							continue
						}
						features := []string{}
						if tc.mcu {
							features = append(features, "mcu")
						}
						_ = conn.WriteJSON(map[string]any{"id": msg["id"], "type": "hello", "hello": map[string]any{"sessionid": "probe-session", "server": map[string]any{"features": features}}})
					}
				}
				if r.URL.Path != "/ocs/v2.php/apps/spreed/api/v3/signaling/settings" {
					unexpected.Add(1)
					http.NotFound(w, r)
					return
				}
				if r.Method != "GET" || r.URL.Query().Get("token") != "testroom" {
					unexpected.Add(1)
				}
				ws := ""
				if tc.server {
					ws = server.URL
				}
				writeOCSResponse(w, tc.status, fmt.Sprintf(`{"server":%q,"helloAuthParams":{"2.0":{}}}`, ws))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			checks := ProbeConnection(ctx, config.Config{CallURL: server.URL + "/call/testroom", TalkAuthMode: config.TalkAuthModeHPBInternal, TalkRecordingSecret: "recording", TalkSignalingInternalSecret: "internal"})
			if len(checks) == 0 || checks[len(checks)-1].Code != tc.code {
				t.Fatalf("checks=%+v", checks)
			}
			if unexpected.Load() != 0 {
				t.Fatal("probe joined a room or sent an unexpected request")
			}
			if tc.server && hello.Load() != 1 {
				t.Fatalf("hello count=%d", hello.Load())
			}
		})
	}
}

func TestProbeDiscoversMissingHPBBeforeAskingForItsSecret(t *testing.T) {
	t.Setenv(talkSignalingInternalSecretEnv, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOCSResponse(w, 200, `{"server":""}`)
	}))
	defer server.Close()
	checks := ProbeConnection(context.Background(), config.Config{CallURL: server.URL + "/call/testroom", TalkAuthMode: config.TalkAuthModeHPBInternal, TalkRecordingSecret: "recording"})
	if len(checks) != 2 || checks[1].Code != "hpb_missing" {
		t.Fatalf("checks=%+v", checks)
	}
}
