package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	initial, err := runAnnotateShow(ctx, bin, fixture)
	if err != nil {
		t.Fatal(err)
	}
	recordMarks(t, ann, name, initial)
	if err = cfg.reconcileRecordingShares(ctx, service.client, rel, []sharePrincipal{{Type: "user", ID: "admin"}}, false); err != nil {
		t.Fatal(err)
	}
	commit := func(label, key, token string) annotateResult {
		var request annotateWriteRequest
		if err := json.Unmarshal([]byte(markRequest(label, key)), &request); err != nil {
			t.Fatal(err)
		}
		request.StateToken = token
		result, err := service.commitDocument(ctx, strings.TrimSuffix(name, ".opus"), name, rel, []string{name}, "admin", request)
		if err != nil {
			t.Fatalf("commit %s: %v", key, err)
		}
		return result
	}
	first := commit("before conversion", "before", "")
	var latest annotateResult
	transport := http.DefaultTransport
	service.client.Transport = annotationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			latest = commit("during conversion", "during", first.StateToken)
		}
		return transport.RoundTrip(r)
	})
	jobID := strings.TrimSuffix(name, ".opus")
	insertJob(t, rt.store.db, jobID, m.Anchor)
	if _, err = rt.store.db.Exec(`UPDATE jobs SET stage='done',state='succeeded',record_finished_at=? WHERE id=?`, m.Anchor, jobID); err != nil {
		t.Fatal(err)
	}
	rt.remoteRetention = service
	rt.retention.settings.Nextcloud.Recordings = retentionPolicy{Count: 30, Unit: "days"}
	proposal, _ := json.Marshal(rt.retention.settings)
	preview := httptest.NewRecorder()
	rt.remoteRetentionPreviewHandler(preview, httptest.NewRequest("POST", "/", bytes.NewReader(proposal)))
	var impact remoteRetentionPreview
	if err = json.Unmarshal(preview.Body.Bytes(), &impact); err != nil || preview.Code != 200 || impact.Convert != 1 || !impact.Capability {
		t.Fatalf("preview: %d %s %v", preview.Code, preview.Body.String(), err)
	}
	pending, err := rt.store.pendingRemoteOperations(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("preview wrote intent", err)
	}
	if err := service.runRemoteRetention(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	service.client.Transport = transport
	head, err := ann.document(ctx, name)
	if err != nil || latest.Sync == nil || head.Sync.Desired != latest.Sync.Desired || head.Sync.Confirmed != first.Sync.Desired || head.StateToken != latest.StateToken {
		t.Fatalf("conversion acknowledged wrong snapshot: %+v latest=%+v err=%v", head, latest, err)
	}
	if err := ann.Close(); err != nil {
		t.Fatal(err)
	}
	ann, err = openAnnotationStore(ann.path, rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	defer ann.Close()
	rt.annotations = ann
	if err = service.syncAnnotation(ctx, name); err != nil {
		t.Fatal("post-restart pending edit", err)
	}
	head, err = ann.document(ctx, name)
	if err != nil || head.Sync.State != "saved" || head.StateToken != latest.StateToken {
		t.Fatal("pending snapshot not saved", err)
	}
	replay := commit("during conversion", "during", first.StateToken)
	if replay.StateToken != latest.StateToken {
		t.Fatal("receipt replay changed")
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
	rt.retention.settings.Nextcloud.Transcriptions = retentionPolicy{Count: 90, Unit: "days"}
	if err := service.runRemoteRetention(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}

	if _, err := service.readDocument(ctx, "admin", strings.TrimSuffix(name, ".opus"), name, dest); err == nil {
		t.Fatal("expired annotations served")
	}
	var receipts int
	if err := ann.db.QueryRow(`SELECT COUNT(*) FROM annotation_receipt WHERE opus_name=? AND response!=?`, name, []byte(`{"expired":true}`)).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("expired receipt content retained", err)
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
