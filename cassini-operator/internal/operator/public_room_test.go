package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A public Talk conversation is one anyone with the link can join, so a
// recording of it is not participant-private (D-552). Before this, room type
// was never read at all: a public meeting got exactly the same
// participant-only ACL as a private one.

func aclFor(rules []aclRule, kind, id string) (aclRule, bool) {
	for _, r := range rules {
		if r.Type == kind && r.ID == id {
			return r, true
		}
	}
	return aclRule{}, false
}

func TestRecordingACLDeniesEveryoneForAPrivateRoom(t *testing.T) {
	rules := recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false)

	everyone, ok := aclFor(rules, "group", ncRecordingsEveryoneGroup)
	if !ok {
		t.Fatalf("no everyone-group rule in %+v", rules)
	}
	if everyone.Permissions != 0 {
		t.Fatalf("everyone permissions = %d, want 0 (denied) for a private room", everyone.Permissions)
	}
	alice, ok := aclFor(rules, "user", "alice")
	if !ok || alice.Permissions != aclPermRead {
		t.Fatalf("participant grant missing or wrong: %+v", rules)
	}
}

func TestRecordingACLGrantsEveryoneForAPublicRoom(t *testing.T) {
	// The virtual everyone group contains every account from creation, so granting
	// it read is what "readable by anyone" means without an anonymous route.
	rules := recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, true)

	everyone, ok := aclFor(rules, "group", ncRecordingsEveryoneGroup)
	if !ok {
		t.Fatalf("no everyone-group rule in %+v", rules)
	}
	if everyone.Permissions != aclPermRead {
		t.Fatalf("everyone permissions = %d, want read for a public room", everyone.Permissions)
	}
	// The owner keeps full control either way.
	owner, ok := aclFor(rules, "user", ncRecordingsOwner)
	if !ok || owner.Permissions != aclMaskAll {
		t.Fatalf("owner rule missing or narrowed: %+v", rules)
	}
}

func TestPublicRoomWithNoGrantableParticipantsStillWidensAccess(t *testing.T) {
	// The case the old early return got exactly backwards: a public room is the
	// one most likely to be full of link guests, who yield no grantable
	// principal at all — so the most public kind of meeting produced the most
	// private recording.
	for _, tc := range []struct {
		name        string
		public      bool
		wantApplied bool
	}{
		{name: "public room", public: true, wantApplied: true},
		{name: "private room", public: false, wantApplied: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			state := seedTalkJob(t, rt, "job-guests")
			state.RoomPublic = tc.public

			applied := false
			gotPublic := false
			rt.applyNCFilesAccessFn = func(_ context.Context, _ string, m []aclMapping, public bool) error {
				applied = true
				gotPublic = public
				if len(m) != 0 {
					t.Errorf("mappings = %#v, want none", m)
				}
				return nil
			}
			// A room of link guests: no local principal to grant.
			rt.fetchTalkParticipants = func(context.Context, string, string) ([]aclMapping, error) {
				return nil, nil
			}

			if err := rt.applyNCFilesAccessStrict(context.Background(), "job-guests"); err != nil {
				t.Fatalf("applyNCFilesAccessStrict() error = %v", err)
			}
			if applied != tc.wantApplied {
				t.Fatalf("ACL applied = %v, want %v", applied, tc.wantApplied)
			}
			if applied && gotPublic != tc.public {
				t.Fatalf("applier got public = %v, want %v", gotPublic, tc.public)
			}
		})
	}
}

func TestRoomFetcherReadsThePublicConversationType(t *testing.T) {
	for _, tc := range []struct {
		roomType   int
		wantPublic bool
	}{
		{roomType: 1, wantPublic: false}, // one-to-one
		{roomType: 2, wantPublic: false}, // group
		{roomType: 3, wantPublic: true},  // public
		{roomType: 6, wantPublic: false}, // note-to-self
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ocs": map[string]any{
					"data": map[string]any{"displayName": "Standup", "type": tc.roomType},
				},
			})
		}))
		fetch := ExAppConfig{NextcloudURL: srv.URL, AppSecret: "s", AppID: "a"}.talkRoomNameFetcher()
		info, err := fetch(context.Background(), "alice", "tok")
		srv.Close()
		if err != nil {
			t.Fatalf("type %d: fetch() error = %v", tc.roomType, err)
		}
		if info.Public != tc.wantPublic {
			t.Fatalf("type %d: public = %v, want %v", tc.roomType, info.Public, tc.wantPublic)
		}
		if info.Name != "Standup" {
			t.Fatalf("type %d: name = %q", tc.roomType, info.Name)
		}
	}
}

func TestPublicnessSurvivesARestartViaTheBinding(t *testing.T) {
	// Publicness is decided at record time and frozen. A rerun or a
	// post-restart publish reads it back from the persisted binding, so a room
	// flipped public afterwards cannot widen a recording made while it was
	// private (nor the reverse narrow one people were told they could see).
	encoded, err := encodeTalkBinding(&talkRoomState{
		BackendURL: "https://nc.example",
		RoomToken:  "tok",
		Owner:      "alice",
		RoomPublic: true,
	})
	if err != nil {
		t.Fatalf("encodeTalkBinding() error = %v", err)
	}
	if !strings.Contains(encoded, `"room_public":true`) {
		t.Fatalf("binding does not persist publicness: %s", encoded)
	}
	state, err := decodeTalkBinding(encoded)
	if err != nil {
		t.Fatalf("decodeTalkBinding() error = %v", err)
	}
	if !state.RoomPublic {
		t.Fatalf("publicness lost through the binding round-trip")
	}

	// A binding written before this change has no room_public field at all;
	// absent must read as non-public rather than as an error.
	legacy, err := decodeTalkBinding(`{"backend_url":"https://nc.example","room_token":"tok","owner":"alice"}`)
	if err != nil {
		t.Fatalf("legacy binding failed to decode: %v", err)
	}
	if legacy.RoomPublic {
		t.Fatalf("a pre-D-552 binding must read as non-public")
	}
}

func TestRoomLookupFailureLeavesTheRecordingNonPublic(t *testing.T) {
	// Fail closed: without the room object we do not know it is public, and an
	// over-restricted recording is recoverable by a rerun where an over-shared
	// one is not.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.talkRoomNameRetryGap = 0
	seedTalkJob(t, rt, "job-nolookup")
	rt.fetchTalkRoomName = func(context.Context, string, string) (talkRoomInfo, error) {
		return talkRoomInfo{}, errors.New("room request returned 503")
	}

	rt.resolveTalkRoomName("job-nolookup", "alice", "tok123")

	binding, ok := rt.talkBindingForJob("job-nolookup")
	if !ok {
		t.Fatalf("expected a binding")
	}
	if binding.Public {
		t.Fatalf("a failed room lookup must leave the recording non-public")
	}
}
