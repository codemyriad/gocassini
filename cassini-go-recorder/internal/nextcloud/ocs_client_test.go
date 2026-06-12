package nextcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJoinCallSendsRecordingConsent(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ocs/v2.php/apps/spreed/api/v4/call/room-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode join call payload: %v", err)
		}
		fmt.Fprint(w, `{"ocs":{"meta":{"status":"ok","statuscode":200,"message":"OK"},"data":{}}}`)
	}))
	defer server.Close()

	client := NewOCSClient(server.URL, false)
	if err := client.JoinCall(context.Background(), "room-1", 1); err != nil {
		t.Fatalf("join call: %v", err)
	}

	consent, ok := body["recordingConsent"].(bool)
	if !ok || !consent {
		t.Fatalf("expected recordingConsent=true in join payload, got %v", body["recordingConsent"])
	}
	if flags, ok := body["flags"].(float64); !ok || int(flags) != 1 {
		t.Fatalf("expected flags=1 in join payload, got %v", body["flags"])
	}
}

func TestRequestErrorsExposeHTTPStatus(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		body            string
		wantClientError bool
	}{
		{
			name:            "consent rejection is a client error",
			status:          http.StatusBadRequest,
			body:            `{"ocs":{"meta":{"status":"failure","statuscode":400,"message":"Recording consent is required"},"data":[]}}`,
			wantClientError: true,
		},
		{
			name:            "unknown room is a client error",
			status:          http.StatusNotFound,
			body:            `{"ocs":{"meta":{"status":"failure","statuscode":404,"message":"Not found"},"data":[]}}`,
			wantClientError: true,
		},
		{
			name:            "rate limiting is not a client error",
			status:          http.StatusTooManyRequests,
			body:            `{"ocs":{"meta":{"status":"error","statuscode":429,"message":"throttled"},"data":[]}}`,
			wantClientError: false,
		},
		{
			name:            "server failure is not a client error",
			status:          http.StatusInternalServerError,
			body:            `{"ocs":{"meta":{"status":"error","statuscode":500,"message":"boom"},"data":[]}}`,
			wantClientError: false,
		},
		{
			name:            "OCS failure with HTTP 200 is not a client error",
			status:          http.StatusOK,
			body:            `{"ocs":{"meta":{"status":"failure","statuscode":999,"message":"nope"},"data":[]}}`,
			wantClientError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()

			client := NewOCSClient(server.URL, false)
			err := client.GetRoom(context.Background(), "room-1")
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var ocsErr *OCSError
			if !errors.As(err, &ocsErr) {
				t.Fatalf("expected *OCSError, got %T: %v", err, err)
			}
			if ocsErr.HTTPStatus != tc.status {
				t.Fatalf("HTTP status mismatch: got=%d want=%d", ocsErr.HTTPStatus, tc.status)
			}
			if got := IsClientError(err); got != tc.wantClientError {
				t.Fatalf("IsClientError mismatch: got=%t want=%t err=%v", got, tc.wantClientError, err)
			}
		})
	}
}
