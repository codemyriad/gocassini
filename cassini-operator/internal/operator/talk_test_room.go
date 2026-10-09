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

// testRoomMine reports whether the stored test conversation belongs to the
// given user, and therefore whether Talk will let them start a recording in it.
//
// Ownership is the whole question: Talk's Start recording action is
// moderator-only and a conversation's creator is its owner. The connection
// check creates this room as Cassini's own provisioning user, because it only
// needs a token to read recording settings with and does not care whose room it
// is — so a room can exist that the administrator reading the panel cannot
// record in. An empty owner means exactly that, and is not a match for anyone.
func (rt *Runtime) testRoomMine(user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return false
	}
	s := &rt.recordingSetup
	s.mu.Lock()
	defer s.mu.Unlock()
	rt.loadRecordingSetupLocked()
	return s.state.TestRoomOwner != "" && s.state.TestRoomOwner == user
}

// testRoomMissing asks Talk whether the stored conversation is still there.
//
// Separate from the connection probe on purpose: whether a conversation exists
// is not a signaling question, and the probe cannot always get far enough to
// ask. With no High Performance Backend it never reads Talk's recording
// settings, so nothing ever reported the room gone — the URL still parsed,
// validTestRoom still accepted it, and arming re-armed the test against a
// conversation that had been deleted.
//
// Only asked about a room whose owner Cassini knows, which is every room it
// made. A room a caller named is left alone: Talk answers 404 both for a
// conversation that is gone and for a private one the asker cannot see, and
// the installed-ExApp e2e arms against exactly such a room.
//
// Asked as the owner because the conversation is private: Talk answers a
// non-participant 404 for a room that is perfectly well there, so only the
// owner's answer distinguishes gone from unseen. Only a definite 404 counts.
// Talk being unreachable or refusing the read says nothing about whether the
// room exists, and throwing away a usable room on a transient failure would
// discard a test an administrator had already armed.
func (rt *Runtime) testRoomMissing(ctx context.Context, room, owner string) (bool, error) {
	token, owner := testRoomToken(room), strings.TrimSpace(owner)
	if token == "" || owner == "" {
		return false, nil
	}
	cfg, err := LoadExAppConfig()
	if err != nil {
		return false, fmt.Errorf("read the AppAPI environment: %w", err)
	}
	if !cfg.Active {
		return false, nil
	}
	client := &http.Client{Timeout: ncProvisionTimeout}
	status, body, err := cfg.apiGetAs(ctx, client, owner, cfg.ocsURL("/apps/spreed/api/v4/room/"+url.PathEscape(token)))
	if err != nil {
		return false, fmt.Errorf("ask Talk about the test room: %w", err)
	}
	if status == http.StatusNotFound {
		return true, nil
	}
	if status < 200 || status >= 300 {
		return false, fmt.Errorf("Talk answered %s when asked about the test room", ocsRefusal(status, body))
	}
	return false, nil
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
// It returns the owner alongside the room, so the caller can record who the
// conversation belongs to rather than infer it. The connection check used to
// store no owner for a room it had just made itself, which lost the one fact
// that distinguishes a room Cassini can speak for from one a caller supplied.
//
// roomType 2 is a GROUP conversation with no invitees, so the owner is its only
// participant. It used to be 3, a PUBLIC one, for the convenience of being
// joinable by its link — but this is a maintenance conversation, and nothing
// needs that: the owner opens it as a participant, and the recorder joins
// through signaling with HPB-internal auth rather than as a Talk participant,
// which is why the installed-ExApp e2e already records in a private room. A
// public conversation is unlisted (listable 0) and so never shown to an
// ordinary user, but any authenticated user holding the token could read and
// join it. A group one answers them 404.
func (rt *Runtime) ensureTestRoom(ctx context.Context, existing, currentOwner, wantOwner string) (string, string, error) {
	wantOwner = strings.TrimSpace(wantOwner)
	if rt.validTestRoom(existing) && (wantOwner == "" || currentOwner == wantOwner) {
		return existing, currentOwner, nil
	}
	base := rt.readinessBackendURL()
	if base == "" {
		return "", "", fmt.Errorf("no usable Nextcloud base URL to build a test room on")
	}
	cfg, err := LoadExAppConfig()
	if err != nil {
		return "", "", fmt.Errorf("read the AppAPI environment: %w", err)
	}
	if !cfg.Active {
		return "", "", fmt.Errorf("a test room needs the AppAPI environment")
	}
	client := &http.Client{Timeout: ncProvisionTimeout}
	status, body, err := cfg.apiPostFormAs(ctx, client, wantOwner, cfg.ocsURL("/apps/spreed/api/v4/room"), url.Values{
		"roomType": {"2"},
		"roomName": {testRoomName},
	})
	if err != nil {
		return "", "", fmt.Errorf("create a test room: %w", err)
	}
	if status < 200 || status >= 300 {
		// Said rather than swallowed: without this the failure reads as "the
		// test did not start" with no indication that Talk refused it.
		return "", "", fmt.Errorf("Talk refused to create a test room (HTTP %d)", status)
	}
	var payload struct {
		OCS struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", fmt.Errorf("decode the created test room: %w", err)
	}
	token := strings.TrimSpace(payload.OCS.Data.Token)
	if token == "" {
		return "", "", fmt.Errorf("Talk created a test room without a token")
	}
	roomURL := strings.TrimRight(base, "/") + "/call/" + token
	if !rt.validTestRoom(roomURL) {
		return "", "", fmt.Errorf("the created test room does not parse as a room URL")
	}
	// Whoever Talk attributed the creation to is the owner, and an empty
	// wantOwner was Cassini's own provisioning user.
	owner := wantOwner
	if owner == "" {
		owner = cfg.provisioningUser()
	}
	return roomURL, owner, nil
}
