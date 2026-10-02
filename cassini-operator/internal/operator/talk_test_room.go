package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

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
// dedicated conversation when none is configured.
//
// This exists so the test needs no configuration. It used to refuse with
// "Choose a test room first", which put a form in front of the one tool whose
// whole value is being quick to reach — and the room it asked for is one
// Cassini can perfectly well make for itself.
//
// Created as the provisioning user, through the same act-as-user path that
// reads Talk's signaling mode. roomType 3 is a PUBLIC conversation, which is
// what makes the link usable: the room belongs to Cassini's own account, so an
// administrator opening a group conversation they were never invited to would
// be turned away from the one room they were just told to join. A public room
// is joinable by its link, needs no invitee list, and can be deleted when the
// test is done.
func (rt *Runtime) ensureTestRoom(ctx context.Context, existing string) (string, error) {
	if rt.validTestRoom(existing) {
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
	status, body, err := cfg.apiPostForm(ctx, client, cfg.ocsURL("/apps/spreed/api/v4/room"), url.Values{
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
