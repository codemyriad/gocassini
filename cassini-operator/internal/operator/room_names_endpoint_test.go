package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cassini-operator/internal/operator/appapi"
)

func TestRoomNameEndpointFreshReadAccessAndConflicts(t *testing.T) {
	ctx := context.Background()
	s, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "meetings.sqlite3"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw := json.RawMessage(`{"id":"A","audioPath":"./meetings/A.opus","roomId":"rm_one","roomName":"Alice"}`)
	if err := s.Put(ctx, 1, "A.opus", raw); err != nil {
		t.Fatal(err)
	}
	if err := s.registerRoom(ctx, "hidden", "Secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.observeRoom(ctx, "rm_one", "admin & Alice", "participants", "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	allowed, outage := true, false
	nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage {
			w.WriteHeader(503)
			return
		}
		data := "[]"
		if allowed {
			data = `[{"uid_file_owner":"cassini","file_source":1,"permissions":1,"path":"/Shared/A.opus","item_type":"file"}]`
		}
		fmt.Fprintf(w, `{"ocs":{"meta":{"statuscode":100},"data":%s}}`, data)
	}))
	defer nc.Close()
	c := ExAppConfig{NextcloudURL: nc.URL, AppID: "cassini", AppSecret: "secret", meetingMetadata: s}
	request := func(method, id, body, caller string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/rooms/"+id+"/name", strings.NewReader(body))
		if caller != "" {
			r = r.WithContext(appapi.WithUserID(r.Context(), caller))
		}
		w := httptest.NewRecorder()
		c.roomNameHandler(w, r)
		return w
	}
	read := request("GET", "rm_one", "", "alice")
	if read.Code != 200 {
		t.Fatal(read.Body.String())
	}
	var state roomNameState
	json.Unmarshal(read.Body.Bytes(), &state)
	write := request("POST", "rm_one", fmt.Sprintf(`{"customName":" Planning ","expectRevision":%d}`, state.Revision), "alice")
	if write.Code != 200 {
		t.Fatalf("read-only recipient failed: %d %s", write.Code, write.Body.String())
	}
	var saved roomNameState
	json.Unmarshal(write.Body.Bytes(), &saved)
	if saved.DisplayName != "Planning" || saved.Revision != state.Revision+1 {
		t.Fatalf("%+v", saved)
	}
	conflict := request("POST", "rm_one", fmt.Sprintf(`{"customName":"Other","expectRevision":%d}`, state.Revision), "admin")
	if conflict.Code != 409 {
		t.Fatalf("conflict: %d", conflict.Code)
	}
	for _, body := range []string{`{}`, `{"customName":"","expectRevision":3}`, `{"customName":"x\ny","expectRevision":3}`, `{"customName":12,"expectRevision":3}`, `{"customName":null,"expectRevision":3} {}`} {
		if w := request("POST", "rm_one", body, "alice"); w.Code != 400 {
			t.Fatalf("invalid %s: %d", body, w.Code)
		}
	}
	reset := request("POST", "rm_one", fmt.Sprintf(`{"customName":null,"expectRevision":%d}`, saved.Revision), "alice")
	var resetState roomNameState
	json.Unmarshal(reset.Body.Bytes(), &resetState)
	if reset.Code != 200 || resetState.DisplayName != "admin & Alice" {
		t.Fatalf("reset %d %s", reset.Code, reset.Body.String())
	}
	for _, id := range []string{"hidden", "unknown"} {
		if w := request("GET", id, "", "alice"); w.Code != 404 {
			t.Fatalf("leaked %s", id)
		}
	}
	allowed = false
	for _, method := range []string{"GET", "POST"} {
		if w := request(method, "rm_one", `{"customName":"Forbidden","expectRevision":4}`, "alice"); w.Code != 404 {
			t.Fatalf("revoked %s: %d", method, w.Code)
		}
	}
	outage = true
	if w := request("POST", "rm_one", `{"customName":"Forbidden","expectRevision":4}`, "alice"); w.Code != 503 {
		t.Fatalf("outage: %d", w.Code)
	}
	if w := request("GET", "rm_one", "", ""); w.Code != 503 {
		t.Fatal("unverified caller accepted")
	}
	got, _ := s.EntriesFor(ctx, []int64{1})
	if string(got[1]) != string(raw) {
		t.Fatal("original metadata changed")
	}
}

func TestRoomNameValidationUnicode(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{strings.Repeat("界", 200), true}, {strings.Repeat("界", 201), false}, {"   ", false}, {"a\u0085b", false}, {" <script>literal</script> ", true}} {
		_, err := validateRoomCustomName(&tc.value)
		if (err == nil) != tc.valid {
			t.Fatalf("%q: %v", tc.value, err)
		}
	}
}
