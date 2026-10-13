package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in, disposable rooms on a LOCAL installed-ExApp fixture. Never point this
// at production. The runner supplies the installed ExApp's credentials without
// logging them. All created rooms/users are cleaned up, including on failure.
func TestTalkNotificationInstalledExApp(t *testing.T) {
	base := os.Getenv("CASSINI_TEST_TALK_URL")
	if base == "" {
		t.Skip("set CASSINI_TEST_TALK_URL and installed ExApp test credentials on a local fixture")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("integration test requires a loopback fixture")
	}
	cfg := ExAppConfig{NextcloudURL: base, AppID: os.Getenv("CASSINI_TEST_APP_ID"), AppSecret: os.Getenv("CASSINI_TEST_APP_SECRET"), AppVersion: os.Getenv("CASSINI_TEST_APP_VERSION")}
	if !cfg.appAPIActive() {
		t.Fatal("missing installed ExApp credentials")
	}
	owner := "admin"
	client := newTalkNotificationClient(cfg)
	setup := func(method, path string, form url.Values) (json.RawMessage, error) {
		req, err := http.NewRequest(method, withFormatJSON(cfg.ocsURL(path)), strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		cfg.setAppAPIProvisionHeadersAs(req, owner)
		// User provisioning requires password confirmation; fixture setup uses
		// its administrator password. All Talk operations still use AppAPI.
		if strings.HasPrefix(path, "/cloud/users") {
			req.Header.Del("AUTHORIZATION-APP-API")
			req.Header.Del("EX-APP-ID")
			req.SetBasicAuth(owner, os.Getenv("CASSINI_TEST_ADMIN_PASSWORD"))
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fixture setup connection failed")
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, err
		}
		code := ocsStatusCode(raw)
		if resp.StatusCode/100 != 2 || (code != 100 && code != 200 && code != 201) {
			return nil, fmt.Errorf("fixture setup %s %s HTTP %d OCS %d", method, path, resp.StatusCode, ocsStatusCode(raw))
		}
		var envelope struct {
			OCS struct {
				Data json.RawMessage `json:"data"`
			} `json:"ocs"`
		}
		err = json.Unmarshal(raw, &envelope)
		return envelope.OCS.Data, err
	}
	peer := fmt.Sprintf("d810-test-%d", time.Now().UnixNano())
	password, err := randomPassword()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup(http.MethodPost, "/cloud/users", url.Values{"userid": {peer}, "password": {password}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := setup(http.MethodDelete, "/cloud/users/"+peer, nil); err != nil {
			t.Error(err)
		}
	})
	for _, roomType := range []string{"2", "3", "1"} {
		t.Run("room-type-"+roomType, func(t *testing.T) {
			form := url.Values{"roomType": {roomType}, "roomName": {"Cassini D-810 integration test"}}
			if roomType == "1" {
				form.Set("invite", peer)
			}
			data, err := setup(http.MethodPost, "/apps/spreed/api/v4/room", form)
			if err != nil {
				t.Fatal(err)
			}
			var room struct {
				Token string `json:"token"`
			}
			if json.Unmarshal(data, &room) != nil || room.Token == "" {
				t.Fatal("fixture room has no token")
			}
			t.Cleanup(func() {
				path := "/apps/spreed/api/v4/room/" + room.Token
				if roomType == "1" {
					path += "/participants/self"
				}
				if _, err := setup(http.MethodDelete, path, nil); err != nil {
					t.Error(err)
				}
			})
			n := talkNotification{Owner: owner, RoomToken: room.Token, MeetingID: "d833-test", Link: cfg.NextcloudURL + "/index.php/apps/app_api/embedded/" + cfg.AppID + "/viewer#meeting=d833-test", Message: "Meeting recording available: Integration check · 13 Oct · 2 min · Speakers: Test"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			// Joining a call/chat session is deliberately unnecessary.
			id, err := client.send(ctx, n)
			if err != nil {
				t.Fatal(err)
			}
			data, err = setup(http.MethodGet, "/apps/spreed/api/v1/chat/"+room.Token+"?lookIntoFuture=0&limit=10&setReadMarker=0", nil)
			if err != nil {
				t.Fatal(err)
			}
			var messages []talkChatMessage
			if err := json.Unmarshal(data, &messages); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, msg := range messages {
				if msg.ID == id && msg.ActorType == "users" && msg.ActorID == owner {
					found = true
				}
			}
			if !found {
				t.Fatalf("moderator message %d missing from history", id)
			}
			data, err = setup(http.MethodGet, "/apps/spreed/api/v1/chat/"+room.Token+"/share/overview", nil)
			if err != nil {
				t.Fatal(err)
			}
			var overview map[string][]struct {
				ID         int64 `json:"id"`
				Parameters map[string]struct {
					Name string `json:"name"`
					Link string `json:"link"`
				} `json:"messageParameters"`
			}
			if err := json.Unmarshal(data, &overview); err != nil {
				t.Fatal(err)
			}
			found = false
			for _, item := range overview["other"] {
				if item.ID == id && item.Parameters["object"].Link == n.Link && item.Parameters["object"].Name == n.Message {
					found = true
				}
			}
			if !found {
				t.Fatal("rich meeting reference missing from Shared items / Other")
			}
			t.Logf("moderator message %d verified in chat and Shared items for room type %s", id, roomType)
		})
	}
}
