package operator

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTalkParticipantsFetcherMapsGrantableActors(t *testing.T) {
	var gotAuth, gotPath, gotOCS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("AUTHORIZATION-APP-API")
		gotPath = r.URL.Path
		gotOCS = r.Header.Get("OCS-APIRequest")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ocs":{"data":[
			{"actorType":"users","actorId":"alice","inCall":1},
			{"actorType":"users","actorId":"bob","inCall":0},
			{"actorType":"users","actorId":"alice","inCall":1},
			{"actorType":"groups","actorId":"team-eng","inCall":1},
			{"actorType":"circles","actorId":"circle-x","inCall":1},
			{"actorType":"guests","actorId":"guest-hash","inCall":1},
			{"actorType":"emails","actorId":"someone@example.com","inCall":1},
			{"actorType":"federated_users","actorId":"remote@other.example","inCall":1},
			{"actorType":"users","actorId":"","userId":"carol","inCall":1}
		]}}`))
	}))
	defer srv.Close()

	fetch := testExAppConfig(srv.URL).talkParticipantsFetcher()
	if fetch == nil {
		t.Fatal("fetcher nil with full ExApp config")
	}
	got, err := fetch(context.Background(), "admin", "roomtok")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	wantAuth := base64.StdEncoding.EncodeToString([]byte("admin:sekret"))
	if gotAuth != wantAuth {
		t.Errorf("auth = %q, want %q (acts as owner)", gotAuth, wantAuth)
	}
	if gotOCS != "true" {
		t.Errorf("OCS-APIRequest = %q, want true", gotOCS)
	}
	if !strings.HasSuffix(gotPath, "/room/roomtok/participants") {
		t.Errorf("path = %q, want …/room/roomtok/participants", gotPath)
	}

	want := []sharePrincipal{
		{Type: "user", ID: "alice"},
		{Type: "user", ID: "bob"},
		{Type: "group", ID: "team-eng"},
		{Type: "circle", ID: "circle-x"},
		{Type: "user", ID: "carol"}, // actorId empty -> userId fallback
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mappings %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mapping[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The attendee fetcher asks the same endpoint and keeps only the in-call
	// people; groups and circles are room grants, not call attendees.
	attendeeFetch := testExAppConfig(srv.URL).talkAttendeesFetcher()
	if attendeeFetch == nil {
		t.Fatal("attendee fetcher nil with full ExApp config")
	}
	attendees, err := attendeeFetch(context.Background(), "admin", "roomtok")
	if err != nil {
		t.Fatalf("attendee fetch: %v", err)
	}
	wantAttendees := []sharePrincipal{
		{Type: "user", ID: "alice"},
		{Type: "user", ID: "carol"},
	}
	if len(attendees) != len(wantAttendees) {
		t.Fatalf("attendee mappings = %v, want %v", attendees, wantAttendees)
	}
	for i := range wantAttendees {
		if attendees[i] != wantAttendees[i] {
			t.Errorf("attendee mapping[%d] = %+v, want %+v", i, attendees[i], wantAttendees[i])
		}
	}
}

func TestParticipantMappingsSkipsAndDedups(t *testing.T) {
	rows := []participantRow{
		{ActorType: "guests", ActorID: "g1"},
		{ActorType: "emails", ActorID: "e@x"},
		{ActorType: "federated_users", ActorID: "f@y"},
		{ActorType: "phones", ActorID: "+100"},
		{ActorType: "users", ActorID: "u1"},
		{ActorType: "users", ActorID: "u1"}, // dup
		{ActorType: "users", ActorID: ""},   // empty -> dropped
		{ActorType: "unknown", ActorID: "z"},
	}
	got := participantMappings(rows, shareAudienceRoom)
	if len(got) != 1 || got[0] != (sharePrincipal{Type: "user", ID: "u1"}) {
		t.Fatalf("participantMappings = %v, want [user/u1]", got)
	}
}

func TestParticipantMappingsAttendeesOnlyKeepsInCallActors(t *testing.T) {
	rows := []participantRow{
		{ActorType: "users", ActorID: "alice", InCall: 1}, // in call
		{ActorType: "users", ActorID: "bob", InCall: 3},   // in call + publishing audio
		{ActorType: "users", ActorID: "carol", InCall: 2}, // media bits, not in call
		{ActorType: "users", ActorID: "dave", InCall: 0},  // room presence only
		{ActorType: "groups", ActorID: "team-eng", InCall: 1},
	}
	got := participantMappings(rows, shareAudienceAttendees)
	want := []sharePrincipal{
		{Type: "user", ID: "alice"},
		{Type: "user", ID: "bob"},
	}
	if len(got) != len(want) {
		t.Fatalf("participantMappings(attendees only) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mapping[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTalkParticipantsFetcherNilWithoutExAppEnv(t *testing.T) {
	if (ExAppConfig{}).talkParticipantsFetcher() != nil {
		t.Error("fetcher should be nil without ExApp env")
	}
}
