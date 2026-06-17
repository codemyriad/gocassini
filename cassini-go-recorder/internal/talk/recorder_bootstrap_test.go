package talk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gocassini/internal/config"
	"gocassini/internal/nextcloud"

	"github.com/gorilla/websocket"
)

// bootstrapServerOptions selects the HTTP status each OCS endpoint answers
// with; zero values default to 200 OK.
type bootstrapServerOptions struct {
	roomStatus        int
	participantStatus int
	joinCallStatus    int
}

func (o bootstrapServerOptions) status(v int) int {
	if v == 0 {
		return http.StatusOK
	}
	return v
}

// newBootstrapTestServer serves the OCS endpoints bootstrap touches plus a
// minimal /spreed signaling websocket that answers hello and room joins.
func newBootstrapTestServer(t *testing.T, opts bootstrapServerOptions) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/spreed":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				var msg map[string]any
				if err := conn.ReadJSON(&msg); err != nil {
					return
				}
				id, _ := msg["id"].(string)
				switch msg["type"] {
				case "hello":
					_ = conn.WriteJSON(map[string]any{"id": id, "type": "hello", "hello": map[string]any{"sessionid": "sig-session"}})
				case "room":
					_ = conn.WriteJSON(map[string]any{"id": id, "type": "room", "room": map[string]any{"roomid": "room-1"}})
				}
			}
		case strings.HasSuffix(r.URL.Path, "/participants/active"):
			writeOCSResponse(w, opts.status(opts.participantStatus), `{"sessionId":"nc-session"}`)
		case strings.Contains(r.URL.Path, "/signaling/settings"):
			writeOCSResponse(w, http.StatusOK, fmt.Sprintf(`{"server":%q,"helloAuthParams":{"2.0":{"token":"t"}},"stunservers":[],"turnservers":[]}`, server.URL))
		case strings.Contains(r.URL.Path, "/api/v4/call/"):
			writeOCSResponse(w, opts.status(opts.joinCallStatus), `{}`)
		case strings.Contains(r.URL.Path, "/api/v4/room/"):
			writeOCSResponse(w, opts.status(opts.roomStatus), `{}`)
		case strings.Contains(r.URL.Path, "/guest/"):
			writeOCSResponse(w, http.StatusOK, `{}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return server
}

func writeOCSResponse(w http.ResponseWriter, status int, data string) {
	ocsStatus := "ok"
	if status >= 400 {
		ocsStatus = "failure"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"ocs":{"meta":{"status":%q,"statuscode":%d,"message":"test"},"data":%s}}`, ocsStatus, status, data)
}

func newBootstrapTestRecorder(serverURL string) *Recorder {
	return &Recorder{
		cfg:            config.Config{GuestName: "CassiniRecorder", JoinFlags: 1},
		baseURL:        serverURL,
		connectBaseURL: serverURL,
		roomToken:      "room-1",
		ocs:            nextcloud.NewOCSClient(serverURL, false),
	}
}

func TestBootstrapFailsFastOnJoinRejections(t *testing.T) {
	tests := []struct {
		name           string
		opts           bootstrapServerOptions
		wantStage      string
		wantUnjoinable bool
	}{
		{
			name:           "room lookup rejected for guest",
			opts:           bootstrapServerOptions{roomStatus: http.StatusNotFound},
			wantStage:      "room check failed",
			wantUnjoinable: true,
		},
		{
			name:           "room join rejected",
			opts:           bootstrapServerOptions{participantStatus: http.StatusForbidden},
			wantStage:      "participants/active failed",
			wantUnjoinable: true,
		},
		{
			name:           "call join rejected without recording consent",
			opts:           bootstrapServerOptions{joinCallStatus: http.StatusBadRequest},
			wantStage:      "join call failed",
			wantUnjoinable: true,
		},
		{
			name:           "call join rejected for unknown conversation",
			opts:           bootstrapServerOptions{joinCallStatus: http.StatusNotFound},
			wantStage:      "join call failed",
			wantUnjoinable: true,
		},
		{
			name:           "transient server failure is not unjoinable",
			opts:           bootstrapServerOptions{roomStatus: http.StatusInternalServerError},
			wantStage:      "room check failed",
			wantUnjoinable: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newBootstrapTestServer(t, tc.opts)
			defer server.Close()

			r := newBootstrapTestRecorder(server.URL)
			defer func() {
				if r.signaling != nil {
					_ = r.signaling.Close()
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := r.bootstrap(ctx)
			if err == nil {
				t.Fatalf("expected bootstrap to fail")
			}
			if !strings.Contains(err.Error(), tc.wantStage) {
				t.Fatalf("expected error from stage %q, got %v", tc.wantStage, err)
			}
			if got := errors.Is(err, ErrUnjoinable); got != tc.wantUnjoinable {
				t.Fatalf("ErrUnjoinable mismatch: got=%t want=%t err=%v", got, tc.wantUnjoinable, err)
			}
		})
	}
}

func TestBootstrapSucceedsWhenJoinAccepted(t *testing.T) {
	server := newBootstrapTestServer(t, bootstrapServerOptions{})
	defer server.Close()

	r := newBootstrapTestRecorder(server.URL)
	defer func() {
		if r.signaling != nil {
			_ = r.signaling.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := r.bootstrap(ctx); err != nil {
		t.Fatalf("unexpected bootstrap error: %v", err)
	}
	if r.nextcloudSessionID != "nc-session" {
		t.Fatalf("unexpected nextcloud session id: %q", r.nextcloudSessionID)
	}
	if r.signalingSessionID != "sig-session" {
		t.Fatalf("unexpected signaling session id: %q", r.signalingSessionID)
	}
}

func TestLogBootstrapFailureEmitsUnjoinableMarker(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantMarker bool
	}{
		{
			name:       "unjoinable failure carries the marker",
			err:        fmt.Errorf("room check failed: %w", ErrUnjoinable),
			wantMarker: true,
		},
		{
			name:       "generic failure only logs the stopping line",
			err:        errors.New("signaling settings failed: boom"),
			wantMarker: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(prev)

			logBootstrapFailure(tc.err)

			logs := buf.String()
			if got := strings.Contains(logs, "talk recorder unjoinable:"); got != tc.wantMarker {
				t.Fatalf("unjoinable marker mismatch: got=%t want=%t logs=%q", got, tc.wantMarker, logs)
			}
			if !strings.Contains(logs, "talk recorder stopping: "+tc.err.Error()) {
				t.Fatalf("expected stopping line with error detail, got %q", logs)
			}
		})
	}
}
