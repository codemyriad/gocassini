package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in: exclusively new scratch files on a disposable installed harness.
func TestInstalledRetentionLifecycle(t *testing.T) {
	if os.Getenv("CASSINI_RETENTION_HARNESS") != "1" {
		t.Skip("requires disposable installed harness")
	}
	fixture := os.Getenv("CASSINI_RETENTION_FIXTURE")
	bin := os.Getenv("CASSINI_TEST_BIN")
	if fixture == "" || bin == "" {
		t.Fatal("fixture and freshly built CLI required")
	}
	container := os.Getenv("CASSINI_EXAPP_CONTAINER")
	if container == "" {
		container = "nc_app_gocassini"
	}
	raw, err := exec.Command("docker", "inspect", "--format", "{{json .Config.Env}}", container).Output()
	if err != nil {
		t.Fatal("installed ExApp unavailable")
	}
	var env []string
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, v := range env {
		k, v, _ := strings.Cut(v, "=")
		values[k] = v
	}
	base := os.Getenv("RETENTION_PROBE_URL")
	if base == "" {
		base = "http://127.0.0.1:28080"
	}
	rt, close := newBareSealRuntime(t)
	defer close()
	os.MkdirAll(rt.cfg.WorkRoot, 0700)
	rt.cfg.CassiniBin = bin
	rt.retention = newRetentionConfig(filepath.Join(t.TempDir(), "settings.json"))
	ann, err := openAnnotationStore(filepath.Join(t.TempDir(), "annotations.db"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	defer ann.Close()
	rt.annotations = ann
	cfg := ExAppConfig{NextcloudURL: base, AppID: values["APP_ID"], AppSecret: values["APP_SECRET"], AppVersion: values["APP_VERSION"], AAVersion: values["AA_VERSION"], PublishSink: publishSinkNextcloudFiles, lifecycle: rt.store}
	service := &annotationService{rt: rt, exapp: cfg, client: &http.Client{Timeout: time.Minute}, bin: bin, logger: rt.logger}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	name := fmt.Sprintf("retention-lifecycle-%d.opus", time.Now().UnixNano())
	rel := ncRecordingsRoot + "/meetings/" + name
	for _, dir := range recordingsTreeDirs(ncRecordingsRoot) {
		if err := cfg.davMkcol(ctx, service.client, ncRecordingsOwner, dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cfg.davPutFileStatus(ctx, service.client, ncRecordingsOwner, rel, fixture, ncRecordingsContentType); err != nil {
		t.Fatal(err)
	}
	dest := strings.TrimSuffix(rel, ".opus") + transcriptionSuffix
	t.Cleanup(func() {
		for _, p := range []string{rel, dest} {
			state, err := cfg.davRetentionLeaf(context.Background(), service.client, p)
			if err == nil && state.Exists {
				if err := cfg.davRetentionMutation(context.Background(), service.client, "DELETE", p, "", state.ETag); err != nil {
					t.Error("scratch cleanup failed", err)
				}
			}
		}
	})
	state, err := cfg.davRetentionLeaf(ctx, service.client, rel)
	if err != nil {
		t.Fatal(err)
	}
	m := meetingLifecycle{Name: name, FileID: state.FileID, Path: rel, Representation: "opus", State: "active", Anchor: "2026-01-01T00:00:00Z", AnchorSource: "synthetic-recording-completed"}
	if err := rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := service.prepareRemoteRetention(ctx, m, "convert", 0); err != nil {
		t.Fatal(err)
	}
	converted, _, err := rt.store.meetingLifecycle(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if converted.Path != dest || converted.Representation != "transcription" {
		t.Fatalf("conversion locator: %+v", converted)
	}
	after, err := cfg.davRetentionLeaf(ctx, service.client, dest)
	if err != nil || after.FileID != state.FileID {
		t.Fatal("identity changed", err)
	}
	local := filepath.Join(t.TempDir(), "retained.json")
	if _, _, _, err := cfg.davDownloadFile(ctx, service.client, ncRecordingsOwner, dest, local, 64<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command(bin, "inspect", "--transcript", local).Output(); err != nil {
		t.Fatal("retained transcript unreadable", err)
	}
	sink := &directSharesPublishSink{&nextcloudFilesPublishSink{cfg: cfg, client: service.client, cassiniBin: bin, rt: rt}}
	if _, err := sink.refreshRetained(ctx, converted, fixture, json.RawMessage(`{}`), ""); err != nil {
		t.Fatal("same-audio refresh", err)
	}
	refreshed, err := cfg.davRetentionLeaf(ctx, service.client, dest)
	if err != nil || refreshed.FileID != state.FileID {
		t.Fatal("refresh changed identity", err)
	}
	unchanged, _, _ := rt.store.meetingLifecycle(ctx, name)
	if unchanged.Anchor != m.Anchor || unchanged.DocumentID != converted.DocumentID {
		t.Fatal("refresh changed lifecycle boundary")
	}
	if err := service.prepareRemoteRetention(ctx, converted, "retire", 0); err != nil {
		t.Fatal(err)
	}
	absent, err := cfg.davRetentionLeaf(ctx, service.client, dest)
	if err != nil || absent.Exists {
		t.Fatal("retirement not verified", err)
	}
	if err := cfg.meetingNotRetired(ctx, name); err == nil {
		t.Fatal("tombstone did not guard publication")
	}
	t.Logf("AppAPI lifecycle passed: stable file ID %d, retained transcript, conditional retirement, tombstone", state.FileID)
}
