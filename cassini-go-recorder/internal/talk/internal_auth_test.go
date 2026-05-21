package talk

import (
	"testing"

	"gocassini/internal/config"
)

func TestBuildInternalHelloAuthParams(t *testing.T) {
	params, err := buildInternalHelloAuthParams("https://cloud.example.test/", "internal-secret-123")
	if err != nil {
		t.Fatalf("buildInternalHelloAuthParams() error = %v", err)
	}
	random, _ := params["random"].(string)
	token, _ := params["token"].(string)
	backend, _ := params["backend"].(string)
	if len(random) != 64 {
		t.Fatalf("random len = %d, want 64", len(random))
	}
	if want := internalHelloToken(random, "internal-secret-123"); token != want {
		t.Fatalf("token = %q, want %q", token, want)
	}
	if backend != "https://cloud.example.test" {
		t.Fatalf("backend = %q", backend)
	}
}

func TestBuildHelloRequestUsesInternalAuthMode(t *testing.T) {
	r := &Recorder{
		cfg: config.Config{
			TalkAuthMode:                config.TalkAuthModeHPBInternal,
			TalkSignalingInternalSecret: "internal-secret-123",
		},
		baseURL: "https://cloud.example.test",
	}

	req, err := r.buildHelloRequest("2.0", "https://cloud.example.test/ocs/v2.php/apps/spreed/api/v3/signaling/backend", map[string]any{"ticket": "ignored"})
	if err != nil {
		t.Fatalf("buildHelloRequest() error = %v", err)
	}
	hello := asMap(req["hello"])
	auth := asMap(hello["auth"])
	if got := auth["type"]; got != "internal" {
		t.Fatalf("auth.type = %#v", got)
	}
	params := asMap(auth["params"])
	if got := params["backend"]; got != "https://cloud.example.test" {
		t.Fatalf("backend = %#v", got)
	}
}

func TestRoomJoinRequestOmitsSessionIDForInternalMode(t *testing.T) {
	r := &Recorder{
		cfg:                config.Config{TalkAuthMode: config.TalkAuthModeHPBInternal},
		roomToken:          "room-42",
		nextcloudSessionID: "nextcloud-session",
	}
	room := asMap(r.roomJoinRequest()["room"])
	if _, ok := room["sessionid"]; ok {
		t.Fatalf("did not expect sessionid in internal room join request: %#v", room)
	}
}

func TestRoomJoinRequestIncludesSessionIDForGuestMode(t *testing.T) {
	r := &Recorder{
		cfg:                config.Config{TalkAuthMode: config.TalkAuthModeGuestParticipant},
		roomToken:          "room-42",
		nextcloudSessionID: "nextcloud-session",
	}
	room := asMap(r.roomJoinRequest()["room"])
	if got := room["sessionid"]; got != "nextcloud-session" {
		t.Fatalf("sessionid = %#v", got)
	}
}
