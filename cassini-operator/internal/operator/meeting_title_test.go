package operator

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultMeetingTitleSnapshotsConversationMembers(t *testing.T) {
	for _, roomType := range []int{talkRoomTypeOneToOne, talkRoomTypeFormerOneToOne} {
		t.Run(fmt.Sprint(roomType), func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			seedTalkJob(t, rt, "job-pair")
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if got := r.Header.Get("AUTHORIZATION-APP-API"); got != base64.StdEncoding.EncodeToString([]byte("admin:s3cret")) {
					t.Errorf("lookup must act as recording owner; got auth %q", got)
				}
				switch r.URL.Path {
				case "/ocs/v2.php/apps/spreed/api/v4/room/tok123":
					fmt.Fprintf(w, `{"ocs":{"data":{"displayName":"Alice","type":%d}}}`, roomType)
				case "/ocs/v2.php/apps/spreed/api/v4/room/tok123/participants":
					// Both are room members even when Alice sends no media.
					// The recorder is a guest, not one of the account pair.
					fmt.Fprint(w, `{"ocs":{"data":[
                        {"actorType":"users","actorId":"alice","displayName":"Alice","inCall":0},
                        {"actorType":"users","actorId":"admin","displayName":"admin","inCall":1},
                        {"actorType":"guests","actorId":"recorder","displayName":"CassiniRecorder"}
                    ]}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			rt.fetchTalkRoomName = ExAppConfig{NextcloudURL: server.URL, AppSecret: "s3cret", AppID: "gocassini"}.talkRoomNameFetcher()
			rt.resolveTalkRoomName("job-pair", "admin", "tok123")
			if got := rt.defaultMeetingTitle("job-pair", "Alice"); got != "admin & Alice" {
				t.Fatalf("title = %q, want admin & Alice without any capture roster", got)
			}
			state, _ := rt.lookupTalkJobState("job-pair")
			want := []talkTitleParticipant{{ID: "admin", Name: "admin"}, {ID: "alice", Name: "Alice"}}
			if !reflect.DeepEqual(state.TitleParticipants, want) || state.RoomType != roomType || state.RoomPublic {
				t.Fatalf("snapshot = %+v", state)
			}
			// Reruns/restarts must not query today's membership or display names.
			rt.recordMu.Lock()
			delete(rt.talkJobs, "job-pair")
			rt.recordMu.Unlock()
			server.Close()
			if got := rt.defaultMeetingTitle("job-pair", "Alice"); got != "admin & Alice" {
				t.Fatalf("persisted title = %q, want admin & Alice", got)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want room + membership at start only", requests)
			}
		})
	}
}

func TestOneToOneTitleParticipants(t *testing.T) {
	user := func(id, name string) participantRow {
		return participantRow{ActorType: "users", ActorID: id, DisplayName: name}
	}
	cases := []struct {
		name string
		rows []participantRow
		want []talkTitleParticipant
	}{
		{"account name is a valid display name", []participantRow{user("alice", "Alice"), user("admin", "admin")},
			[]talkTitleParticipant{{ID: "admin", Name: "admin"}, {ID: "alice", Name: "Alice"}}},
		{"duplicate sessions use account identity", []participantRow{user("admin", "admin"), user("alice", "Alice"), user("admin", "admin")},
			[]talkTitleParticipant{{ID: "admin", Name: "admin"}, {ID: "alice", Name: "Alice"}}},
		{"namesakes remain two people", []participantRow{user("u2", "Alex"), user("u1", "Alex")},
			[]talkTitleParticipant{{ID: "u1", Name: "Alex"}, {ID: "u2", Name: "Alex"}}},
		{"legacy userId and safe names", []participantRow{{ActorType: "users", UserID: "admin", DisplayName: " admin\n"}, user("alice", "Alice")},
			[]talkTitleParticipant{{ID: "admin", Name: "admin"}, {ID: "alice", Name: "Alice"}}},
		{"unknown member name", []participantRow{user("admin", "admin"), user("alice", "")}, nil},
		{"unknown member identity", []participantRow{user("admin", "admin"), user("", "Alice")}, nil},
		{"inconsistent duplicate identity", []participantRow{user("admin", "admin"), user("alice", "Alice"), user("alice", "Other")}, nil},
		{"one member", []participantRow{user("admin", "admin")}, nil},
		{"three members", []participantRow{user("admin", "admin"), user("alice", "Alice"), user("bob", "Bob")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oneToOneTitleParticipants(tc.rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("participants = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDefaultMeetingTitleFallsBackToRoomName(t *testing.T) {
	for _, roomType := range []int{0, 1, 2, 3, 5} {
		t.Run(fmt.Sprint(roomType), func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			seedTalkJob(t, rt, "job-fallback")
			// A pre-upgrade binding or failed membership lookup has no pair.
			rt.storeTalkRoomInfo("job-fallback", talkRoomInfo{Name: "Room", Type: roomType})
			if got := rt.defaultMeetingTitle("job-fallback", "Room"); got != "Room" {
				t.Fatalf("missing snapshot title = %q, want Room", got)
			}
			if !isOneToOneTalkRoom(roomType) {
				rt.storeTalkRoomInfo("job-fallback", talkRoomInfo{Name: "Room", Type: roomType,
					TitleParticipants: []talkTitleParticipant{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}})
				if got := rt.defaultMeetingTitle("job-fallback", "Room"); got != "Room" {
					t.Fatalf("non-1:1 title = %q, want Room", got)
				}
			}
		})
	}
}

func TestTalkRoomNameFetcherSkipsMembershipForGroupAndPublicRooms(t *testing.T) {
	for _, roomType := range []int{2, 3} {
		t.Run(fmt.Sprint(roomType), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/participants") {
					t.Error("group/public title must not fetch a participant pair")
					http.Error(w, "unexpected", 500)
					return
				}
				fmt.Fprintf(w, `{"ocs":{"data":{"displayName":"Standup","type":%d}}}`, roomType)
			}))
			defer server.Close()
			fetch := ExAppConfig{NextcloudURL: server.URL, AppSecret: "s", AppID: "a"}.talkRoomNameFetcher()
			info, err := fetch(context.Background(), "admin", "tok")
			if err != nil || info.Name != "Standup" || info.Public != (roomType == 3) || len(info.TitleParticipants) != 0 {
				t.Fatalf("info = %+v, err = %v", info, err)
			}
		})
	}
}

func TestOneToOneMembershipLookupFailureKeepsRoomMetadata(t *testing.T) {
	for _, failure := range []string{"forbidden", "invalid-json", "missing-member", "temporary"} {
		t.Run(failure, func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			seedTalkJob(t, rt, "job-pair")
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/participants") {
					fmt.Fprint(w, `{"ocs":{"data":{"displayName":"Alice","type":1}}}`)
					return
				}
				attempts++
				switch failure {
				case "invalid-json":
					fmt.Fprint(w, "not JSON")
				case "missing-member":
					fmt.Fprint(w, `{"ocs":{"data":[{"actorType":"users","actorId":"admin","displayName":"admin"}]}}`)
				case "temporary":
					if attempts > 1 {
						fmt.Fprint(w, `{"ocs":{"data":[{"actorType":"users","actorId":"admin","displayName":"admin"},{"actorType":"users","actorId":"alice","displayName":"Alice"}]}}`)
						return
					}
					fallthrough
				default:
					http.Error(w, "forbidden", http.StatusForbidden)
				}
			}))
			defer server.Close()
			rt.fetchTalkRoomName = ExAppConfig{NextcloudURL: server.URL, AppSecret: "s", AppID: "a"}.talkRoomNameFetcher()
			rt.talkRoomNameRetryGap = time.Millisecond
			rt.resolveTalkRoomName("job-pair", "admin", "tok123")
			state, _ := rt.lookupTalkJobState("job-pair")
			if state.RoomName != "Alice" || state.RoomType != 1 || state.RoomPublic {
				t.Fatalf("membership failure lost room metadata: %+v", state)
			}
			want, wantAttempts := "Alice", talkRoomNameAttempts
			if failure == "temporary" {
				want, wantAttempts = "admin & Alice", 2
			}
			if got := rt.defaultMeetingTitle("job-pair", "Alice"); got != want || attempts != wantAttempts {
				t.Fatalf("title = %q, attempts = %d; want %q, %d", got, attempts, want, wantAttempts)
			}
		})
	}
}
