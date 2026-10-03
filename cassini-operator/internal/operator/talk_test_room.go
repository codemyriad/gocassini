package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// actingUser is the Nextcloud user whose click reached this handler.
//
// AppAPI signs every proxied request with AUTHORIZATION-APP-API, which is
// base64("<userId>:<appSecret>") — the same encoding Cassini uses in the other
// direction to act as a user. The user id is the part before the first colon;
// the secret is not checked here, because AppAPI already authenticated the
// request and gated the route to administrators.
func actingUser(r *http.Request) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.Header.Get("AUTHORIZATION-APP-API")))
	if err != nil {
		return ""
	}
	user, _, found := strings.Cut(string(raw), ":")
	if !found {
		return ""
	}
	return strings.TrimSpace(user)
}

// roomIsGone reports whether the connection probe failed because the
// conversation Cassini checks with no longer exists — or because Talk itself is
// not there to hold it, which the probe cannot tell apart and which makes no
// difference here: in both cases the stored room is worth nothing.
func roomIsGone(checks []readinessCheck) bool {
	for _, c := range checks {
		if c.Code == "talk_or_room_unavailable" || c.Code == "test_room_invalid" {
			return true
		}
	}
	return false
}

// testRoomName is the conversation Cassini makes for its own recording test.
// Named so an administrator finding it in Talk knows what it is and that
// deleting it is harmless.
const testRoomName = "Cassini recording test"

// ensureTestRoom returns a room to run the recording test in, creating a
// dedicated conversation when there is not already a usable one.
//
// This exists so the test needs no configuration. It used to refuse with
// "Choose a test room first", which put a form in front of the one tool whose
// whole value is being quick to reach — and the room it asked for is one
// Cassini can perfectly well make for itself.
//
// `wantOwner` is who the room has to belong to. It matters because Talk's Start
// recording action is moderator-only and the creator of a conversation is its
// owner: a room made by Cassini's own account leaves the administrator who
// opens it an ordinary participant, with no way to start the recording they
// were just told to start. Reported from staging, and the reason this takes an
// owner at all. An empty wantOwner means Cassini's provisioning user and
// accepts any existing room — that is the connection check, which only needs a
// token to read recording settings with and does not care whose room it is.
//
// roomType 3 is a PUBLIC conversation: joinable by its link, needing no invitee
// list, and deletable when the test is done.
func (rt *Runtime) ensureTestRoom(ctx context.Context, existing, currentOwner, wantOwner string) (string, error) {
	wantOwner = strings.TrimSpace(wantOwner)
	if rt.validTestRoom(existing) && (wantOwner == "" || currentOwner == wantOwner) {
		return existing, nil
	}
	base := rt.readinessBackendURL()
	if base == "" {
		return "", fmt.Errorf("no usable Nextcloud base URL to build a test room on")
	}
	cfg, err := LoadExAppConfig()
	if err != nil {
		return "", fmt.Errorf("read the AppAPI environment: %w", err)
	}
	if !cfg.Active {
		return "", fmt.Errorf("a test room needs the AppAPI environment")
	}
	client := &http.Client{Timeout: ncProvisionTimeout}
	status, body, err := cfg.apiPostFormAs(ctx, client, wantOwner, cfg.ocsURL("/apps/spreed/api/v4/room"), url.Values{
		"roomType": {"3"},
		"roomName": {testRoomName},
	})
	if err != nil {
		return "", fmt.Errorf("create a test room: %w", err)
	}
	if status < 200 || status >= 300 {
		// Said rather than swallowed: without this the failure reads as "the
		// test did not start" with no indication that Talk refused it.
		return "", fmt.Errorf("Talk refused to create a test room (HTTP %d)", status)
	}
	var payload struct {
		OCS struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode the created test room: %w", err)
	}
	token := strings.TrimSpace(payload.OCS.Data.Token)
	if token == "" {
		return "", fmt.Errorf("Talk created a test room without a token")
	}
	roomURL := strings.TrimRight(base, "/") + "/call/" + token
	if !rt.validTestRoom(roomURL) {
		return "", fmt.Errorf("the created test room does not parse as a room URL")
	}
	return roomURL, nil
}
