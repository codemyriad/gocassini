package nextcloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchRecordingSignalingSettingsUsesRecordingAuthHeaders(t *testing.T) {
	const secret = "recording-secret-123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/ocs/v2.php/apps/spreed/api/v3/signaling/settings" {
			t.Fatalf("path = %q", got)
		}
		if got := r.URL.Query().Get("token"); got != "room-42" {
			t.Fatalf("token = %q", got)
		}
		random := r.Header.Get(talkRecordingRandomHeader)
		if random == "" {
			t.Fatalf("missing %s header", talkRecordingRandomHeader)
		}
		if got := r.Header.Get(talkRecordingChecksumHeader); got != talkRecordingChecksum(secret, random, nil) {
			t.Fatalf("checksum = %q", got)
		}
		fmt.Fprint(w, `{"ocs":{"meta":{"status":"ok","statuscode":100,"message":"OK"},"data":{"server":"https://signaling.example.test"}}}`)
	}))
	defer server.Close()

	client := NewOCSClient(server.URL, false)
	settings, err := client.FetchRecordingSignalingSettings(context.Background(), "room-42", secret)
	if err != nil {
		t.Fatalf("FetchRecordingSignalingSettings() error = %v", err)
	}
	if got := settings.PrimarySignalingServer(); got != "https://signaling.example.test" {
		t.Fatalf("PrimarySignalingServer() = %q", got)
	}
}
