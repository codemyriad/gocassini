package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func TestHandleSignalingEventRoomAudience(t *testing.T) {
	b := newBot(&botConfig{Index: 1})
	b.setSignalingSessionID("self-session")

	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "room",
			"join": []any{
				map[string]any{"sessionid": "self-session"},
				map[string]any{"sessionid": "peer-a"},
			},
			"change": []any{
				map[string]any{"sessionId": "peer-b"},
			},
			"leave": []any{"peer-a"},
		},
	})

	got := b.audienceSnapshot()
	sort.Strings(got)

	want := []string{"peer-b"}
	if !equalStringSlices(got, want) {
		t.Fatalf("audience mismatch: got=%v want=%v", got, want)
	}
}

func TestHandleSignalingEventParticipantsAudience(t *testing.T) {
	b := newBot(&botConfig{Index: 1})
	b.setSignalingSessionID("self-session")
	b.addAudienceSession("stale")

	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "participants",
			"update": map[string]any{
				"users": []any{
					map[string]any{"sessionId": "peer-a", "inCall": 7},
					map[string]any{"sessionId": "peer-b", "inCall": "5"},
					map[string]any{"sessionId": "peer-c", "inCall": 0},
					map[string]any{"sessionId": "peer-internal", "internal": true, "inCall": 7},
					map[string]any{"sessionId": "self-session", "inCall": 7},
				},
			},
		},
	})

	got := b.audienceSnapshot()
	sort.Strings(got)
	want := []string{"peer-a", "peer-b", "stale"}
	if !equalStringSlices(got, want) {
		t.Fatalf("audience mismatch after users update: got=%v want=%v", got, want)
	}

	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "participants",
			"update": map[string]any{
				"all":    true,
				"incall": 0,
			},
		},
	})

	got = b.audienceSnapshot()
	if len(got) != 0 {
		t.Fatalf("expected cleared audience on all/incall=0, got=%v", got)
	}
}

func TestOCSClientAddsBasicAuthWhenConfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok {
			t.Fatalf("expected basic auth")
		}
		if user != "alice" || password != "secret" {
			t.Fatalf("basic auth=%s:%s want alice:secret", user, password)
		}
		writeOCSTestResponse(t, w, map[string]any{"token": "room-token"})
	}))
	defer server.Close()

	client := newOCSClient(server.URL, false, "alice", "secret")
	defer client.Close()
	if err := client.getRoom(context.Background(), "room-token"); err != nil {
		t.Fatalf("getRoom: %v", err)
	}
}

func TestRecordingStarterStartsTalkRecordingAndPollsActive(t *testing.T) {
	polls := 0
	started := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "cassini-erlich" || password != "pw" {
			t.Fatalf("auth=%s:%s ok=%v", user, password, ok)
		}
		switch r.URL.Path {
		case "/ocs/v2.php/apps/spreed/api/v1/recording/token":
			if r.Method != http.MethodPost {
				t.Fatalf("recording method=%s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse recording form: %v", err)
			}
			if got := r.Form.Get("status"); got != "1" {
				t.Fatalf("recording status=%q want 1", got)
			}
			started = true
			writeOCSTestResponse(t, w, map[string]any{})
		case "/ocs/v2.php/apps/spreed/api/v4/room/token":
			if r.Method != http.MethodGet {
				t.Fatalf("room method=%s", r.Method)
			}
			polls++
			state := 3
			if polls >= 2 {
				state = 1
			}
			writeOCSTestResponse(t, w, map[string]any{"callRecording": state})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	b := newBot(&botConfig{
		Index:                1,
		BaseURL:              server.URL,
		RoomToken:            "token",
		GuestName:            "Erlich Bachman",
		AuthUser:             "cassini-erlich",
		AuthPass:             "pw",
		RecordingStatus:      "1",
		RecordingPollTimeout: time.Second,
	})
	defer b.http.Close()
	if err := b.startRecordingAndWait(context.Background()); err != nil {
		t.Fatalf("startRecordingAndWait: %v", err)
	}
	if !started || polls != 2 {
		t.Fatalf("started=%v polls=%d want started true polls 2", started, polls)
	}
}

func TestAuthenticatedBotJoinSkipsGuestName(t *testing.T) {
	seenGuestName := false
	seenForceTrue := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "cassini-erlich" || password != "pw" {
			t.Fatalf("auth=%s:%s ok=%v", user, password, ok)
		}
		switch r.URL.Path {
		case "/ocs/v2.php/apps/spreed/api/v4/room/token":
			if r.Method != http.MethodGet {
				t.Fatalf("room method=%s", r.Method)
			}
			writeOCSTestResponse(t, w, map[string]any{"token": "token"})
		case "/ocs/v2.php/apps/spreed/api/v4/room/token/participants/active":
			if r.Method != http.MethodPost {
				t.Fatalf("active method=%s", r.Method)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			seenForceTrue = r.Form.Get("force") == "true"
			if got := r.Form.Get("displayName"); got != "" {
				t.Fatalf("authenticated active request should omit displayName, got %q", got)
			}
			writeOCSTestResponse(t, w, map[string]any{"sessionId": "nc-session"})
		case "/ocs/v2.php/apps/spreed/api/v1/guest/token/name":
			seenGuestName = true
			writeOCSTestResponse(t, w, map[string]any{})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	b := newBot(&botConfig{
		Index:     1,
		BaseURL:   server.URL,
		RoomToken: "token",
		GuestName: "Erlich Bachman",
		AuthUser:  "cassini-erlich",
		AuthPass:  "pw",
	})
	defer b.http.Close()
	if err := b.joinConversation(context.Background()); err != nil {
		t.Fatalf("joinConversation: %v", err)
	}
	if !seenForceTrue {
		t.Fatalf("authenticated active request should use force=true")
	}
	if seenGuestName {
		t.Fatalf("authenticated join should not call guest name endpoint")
	}
}

func TestCollectExternalAudienceSessionsExcludesBotSessions(t *testing.T) {
	b1 := newBot(&botConfig{Index: 1})
	b2 := newBot(&botConfig{Index: 2})
	b1.setSignalingSessionID("bot-session-1")
	b2.setSignalingSessionID("bot-session-2")

	b1.addAudienceSession("bot-session-2")
	b1.addAudienceSession("viewer-a")
	b2.addAudienceSession("bot-session-1")
	b2.addAudienceSession("viewer-a")
	b2.addAudienceSession("viewer-b")

	external, knownBots := collectExternalAudienceSessions([]*bot{b1, b2})
	if knownBots != 2 {
		t.Fatalf("known bot session count mismatch: got=%d want=2", knownBots)
	}
	if len(external) != 2 {
		t.Fatalf("external audience count mismatch: got=%d want=2", len(external))
	}
	if _, ok := external["viewer-a"]; !ok {
		t.Fatalf("missing viewer-a in external audience: %+v", external)
	}
	if _, ok := external["viewer-b"]; !ok {
		t.Fatalf("missing viewer-b in external audience: %+v", external)
	}
	if _, ok := external["bot-session-1"]; ok {
		t.Fatalf("bot session should be filtered from external audience")
	}
}

func writeOCSTestResponse(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{
		"ocs": map[string]any{
			"meta": map[string]any{"status": "ok", "statuscode": 200, "message": "OK"},
			"data": data,
		},
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPickAudibleRotatesOneActiveBotByDefault(t *testing.T) {
	// Bot 1 is not active (still joining); rotation skips it.
	active := []int{0, 2, 3}
	current := -1
	var order []int
	for round := 0; round < 4; round++ {
		audible, next := pickAudible(4, active, current, false)
		if len(audible) != 1 || !audible[next] {
			t.Fatalf("round %d: expected exactly bot %d audible, got %v", round, next, audible)
		}
		order = append(order, next)
		current = next
	}
	want := []int{0, 2, 3, 0}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("rotation order mismatch: got=%v want=%v", order, want)
		}
	}
}

func TestPickAudibleAllAudibleUnmutesEveryActiveBot(t *testing.T) {
	audible, next := pickAudible(4, []int{0, 2, 3}, 2, true)
	if next != 2 {
		t.Fatalf("all-audible must not rotate: got current=%d want=2", next)
	}
	for _, idx := range []int{0, 2, 3} {
		if !audible[idx] {
			t.Fatalf("bot %d should be audible, got %v", idx, audible)
		}
	}
	if audible[1] {
		t.Fatalf("inactive bot 1 must not be unmuted, got %v", audible)
	}

	if audible, _ := pickAudible(4, nil, -1, true); len(audible) != 0 {
		t.Fatalf("no active bots means nobody audible, got %v", audible)
	}
}

func TestGuestBotAnnouncesNickToInCallSessionsOnce(t *testing.T) {
	b := newBot(&botConfig{Index: 1, GuestName: "Ana Silva"})
	b.setSignalingSessionID("self-session")
	var sent []map[string]any
	b.sendNick = func(data map[string]any) error {
		sent = append(sent, data)
		return nil
	}

	inCallUpdate := map[string]any{
		"event": map[string]any{
			"target": "participants",
			"update": map[string]any{
				"users": []any{
					map[string]any{"sessionId": "self-session", "inCall": 7},
					map[string]any{"sessionId": "peer-a", "inCall": 7},
					map[string]any{"sessionId": "recorder-internal", "internal": true, "inCall": 1},
					map[string]any{"sessionId": "peer-out", "inCall": 0},
					map[string]any{"sessionId": "peer-presence-only"},
				},
			},
		},
	}
	b.handleSignalingEvent(inCallUpdate)
	b.handleSignalingEvent(inCallUpdate)

	got := make([]string, 0, len(sent))
	for _, data := range sent {
		if data["type"] != "nickChanged" || data["roomType"] != "video" {
			t.Fatalf("unexpected message: %v", data)
		}
		if name := data["payload"].(map[string]any)["name"]; name != "Ana Silva" {
			t.Fatalf("expected name Ana Silva, got %v", name)
		}
		got = append(got, data["to"].(string))
	}
	sort.Strings(got)
	want := []string{"peer-a", "recorder-internal"}
	if !equalStringSlices(got, want) {
		t.Fatalf("nickChanged recipients: got=%v want=%v (once each, internal sessions included)", got, want)
	}

	// A session that leaves the call hears the name again when it returns.
	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{"target": "room", "leave": []any{"recorder-internal"}},
	})
	b.handleSignalingEvent(inCallUpdate)
	if len(sent) != 3 || sent[2]["to"] != "recorder-internal" {
		t.Fatalf("expected a fresh announcement to the returning session, got %v", sent)
	}
}

func TestAuthenticatedBotDoesNotAnnounceNick(t *testing.T) {
	b := newBot(&botConfig{Index: 1, GuestName: "alice", AuthUser: "alice"})
	b.setSignalingSessionID("self-session")
	b.sendNick = func(data map[string]any) error {
		t.Fatalf("authenticated bot sent %v", data)
		return nil
	}
	b.handleSignalingEvent(map[string]any{
		"event": map[string]any{
			"target": "participants",
			"update": map[string]any{
				"users": []any{map[string]any{"sessionId": "recorder-internal", "internal": true, "inCall": 1}},
			},
		},
	})
}
