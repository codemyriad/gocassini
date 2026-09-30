package operator

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapturePolicySettingsPersistence(t *testing.T) {
	rt := newSettingsTestRuntime(t)
	for _, tc := range []struct {
		body   string
		want   bool
		status int
	}{
		{`{"quality":"balanced"}`, false, 200},
		{`{"quality":"balanced","retain_video":true}`, true, 200},
		{`{"quality":"fast"}`, true, 200},
		{`{"quality":"fast","retain_video":"yes"}`, true, 400},
		{`{"quality":"balanced","retain_video":false}`, false, 200},
	} {
		rec := httptest.NewRecorder()
		rt.handlePutSettings(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(tc.body)))
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.body, rec.Code, rec.Body.String())
		}
		if rt.currentSettings().RetainVideo != tc.want {
			t.Fatalf("%s: wrong runtime policy", tc.body)
		}
		loaded, err := LoadOrInitSettings(rt.settingsPath)
		if err != nil || loaded.RetainVideo != tc.want {
			t.Fatalf("%s: restart policy=%v err=%v", tc.body, loaded.RetainVideo, err)
		}
	}
}

func TestCapturePolicyFreshAndHardwareMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	fresh, err := LoadOrInitSettings(path)
	if err != nil || fresh.RetainVideo {
		t.Fatalf("fresh policy: %+v %v", fresh, err)
	}
	fresh.RetainVideo = true
	fresh.Source = sttSourceAuto
	fresh.HardwareFingerprint = "old-hardware"
	if err := Save(path, fresh); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOrInitSettings(path)
	if err != nil || !loaded.RetainVideo {
		t.Fatalf("migration lost consent: %+v %v", loaded, err)
	}
}

func TestCapturePolicyPersistFailureDoesNotChangeRuntime(t *testing.T) {
	rt := newSettingsTestRuntime(t)
	// Parent is a file, so atomic save cannot create its settings directory.
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	rt.settingsPath = filepath.Join(parent, "settings.json")
	rec := httptest.NewRecorder()
	rt.handlePutSettings(rec, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"balanced","retain_video":true}`)))
	if rec.Code != http.StatusInternalServerError || rt.currentSettings().RetainVideo {
		t.Fatalf("failed save applied: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRecordingAdmissionFreezesTrustedCapturePolicy(t *testing.T) {
	for _, video := range []bool{false, true} {
		t.Run(map[bool]string{false: "audio-only", true: "video-opt-in"}[video], func(t *testing.T) {
			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			rt.settingsMu.Lock()
			rt.settings.RetainVideo = video
			rt.settingsMu.Unlock()
			req := TriggerRequest{Platform: nextcloudTalkProvider, URL: "https://example.test/call/room", GuestName: defaultGuestName, TalkAuthMode: defaultTalkAuthMode, RetainVideo: !video, CaptureMode: "forged"}
			resp, start, err := rt.prepareRecordJob(context.Background(), nextcloudTalkProvider, `{"retain_video":true,"capture_mode":"forged"}`, req)
			if err != nil {
				t.Fatal(err)
			}
			job, err := rt.store.GetJob(context.Background(), resp.ID)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := decodeStoredTriggerRequest(job.RequestJSON)
			want := "audio-only"
			if video {
				want = "audio-video"
			}
			if err != nil || stored.RetainVideo != video || stored.CaptureMode != want {
				t.Fatalf("untrusted snapshot: %+v %v", stored, err)
			}
			// Change policy between admission and spawn. The accepted capture stays frozen.
			rt.settingsMu.Lock()
			rt.settings.RetainVideo = !video
			rt.settingsMu.Unlock()
			observed := make(chan TriggerRequest, 1)
			rt.recordJobFn = func(_ context.Context, _ Job, child TriggerRequest) (recordResult, error) {
				observed <- child
				return recordResult{}, errors.New("test stop")
			}
			start()
			select {
			case child := <-observed:
				if child.RetainVideo != video || child.CaptureMode != want {
					t.Fatalf("active policy changed: %+v", child)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child did not start")
			}
			rt.WaitForRecordJobs(5 * time.Second)
			// A new capture takes the changed trusted policy, including legacy requests.
			resp, _, err = rt.prepareRecordJob(context.Background(), nextcloudTalkProvider, "{}", req)
			if err != nil {
				t.Fatal(err)
			}
			job, err = rt.store.GetJob(context.Background(), resp.ID)
			if err != nil {
				t.Fatal(err)
			}
			var next TriggerRequest
			if err := json.Unmarshal([]byte(job.RequestJSON), &next); err != nil {
				t.Fatal(err)
			}
			if next.RetainVideo != !video {
				t.Fatal("new capture used stale consent")
			}
			rt.releaseRecordSlot()
		})
	}
}

func TestRecordChildGetsExplicitTrustedVideoFlag(t *testing.T) {
	for _, video := range []bool{false, true} {
		t.Run(map[bool]string{false: "audio-only", true: "opt-in"}[video], func(t *testing.T) {
			rt, cleanup, logPath, _ := newCLITestRuntime(t)
			defer cleanup()
			rt.settingsMu.Lock()
			rt.settings.RetainVideo = video
			rt.settingsMu.Unlock()
			rec := httptest.NewRecorder()
			rt.jobsHandler(rec, httptest.NewRequest(http.MethodPost, "/jobs?provider=nextcloud-talk", strings.NewReader(`{"platform":"nextcloud-talk","url":"https://example.test/call/room","retain_video":true}`)))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			var response createJobResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			waitForJobState(t, rt.store, response.ID, "succeeded")
			want := "--retain-video=false"
			if video {
				want = "--retain-video=true"
			}
			if !strings.Contains(readFileString(t, logPath), want) {
				t.Fatalf("child missing %s", want)
			}
		})
	}
}

func TestCapturePolicySettingsUseExistingAdminAuthorization(t *testing.T) {
	raw, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		External struct {
			Routes []struct {
				URL    string `xml:"url"`
				Access string `xml:"access_level"`
				Verb   string `xml:"verb"`
			} `xml:"routes>route"`
		} `xml:"external-app"`
	}
	if err := xml.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range manifest.External.Routes {
		if strings.Contains(route.URL, "settings(") {
			found = true
			if route.Access != "ADMIN" || !strings.Contains(route.Verb, "PUT") {
				t.Fatal(route)
			}
		}
	}
	if !found {
		t.Fatal("settings admin route missing")
	}
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.APIToken = "capture-test-token"
	handler := newHTTPHandler(rt.logger, rt, ExAppConfig{})
	for _, token := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"quality":"balanced","retain_video":true}`))
		req.Header.Set("Authorization", token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rt.currentSettings().RetainVideo {
			t.Fatalf("unauthorized settings write: %d", rec.Code)
		}
	}
}
