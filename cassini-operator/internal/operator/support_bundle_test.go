package operator

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

const supportTestBody = `{"privacy":"scrubbed","surface":"browse","timezone_offset_minutes":-60,"online":true,"catalog_state":"failed","catalog_reason":"request_failed"}`

func supportTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, closeStore := newBarePublishRuntime(t, log.New(io.Discard, "", 0))
	t.Cleanup(closeStore)
	rt.cfg.APIToken = "test-support-token"
	rt.settings = STTSettings{Quality: sttQualityFast}
	rt.supportJournal = newSupportJournal(rt.cfg.WorkRoot)
	return rt
}

func supportTestDownload(t *testing.T, rt *Runtime, c ExAppConfig, admin bool) map[string][]byte {
	t.Helper()
	return supportTestDownloadMode(t, rt, c, admin, "scrubbed")
}

func supportTestDownloadMode(t *testing.T, rt *Runtime, c ExAppConfig, admin bool, privacy string) map[string][]byte {
	t.Helper()
	path := supportUserPath
	if admin {
		path = supportAdminPath
	}
	r := callerReq(http.MethodPost, path, "alice")
	r.Body = io.NopCloser(strings.NewReader(strings.Replace(supportTestBody, `"privacy":"scrubbed"`, `"privacy":"`+privacy+`"`, 1)))
	w := httptest.NewRecorder()
	rt.supportBundleHandler(c, admin).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), `attachment; filename="cassini-support-`) {
		t.Fatalf("unsafe download headers: %v", w.Header())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string][]byte{}
	total := uint64(0)
	for _, f := range z.File {
		if strings.Contains(f.Name, "..") || filepath.IsAbs(f.Name) {
			t.Fatalf("unsafe ZIP name %s", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[f.Name] = content
		total += f.UncompressedSize64
	}
	if total > supportMaxBytes {
		t.Fatalf("oversize archive: %d", total)
	}
	return entries
}

func TestSupportUserBundleContainsOnlySelfDiagnostics(t *testing.T) {
	rt := supportTestRuntime(t)
	insertJob(t, rt.store.db, "inaccessible-private-meeting", nowUTCString())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || (r.URL.Query().Get("shared_with_me") != "true" && r.URL.Path != "/ocs/v2.php/apps/files_sharing/api/v1/shares/pending") {
			t.Errorf("not a read-only self probe: %s %s", r.Method, r.URL)
		}
		auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
		if string(auth) != "alice:test-secret" {
			t.Errorf("probe acted as someone else: %q", auth)
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
	}))
	defer server.Close()
	c := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "test-secret"}
	entries := supportTestDownload(t, rt, c, false)
	if len(entries) != 5 || entries["jobs.json"] != nil || entries["logs/operator.jsonl"] != nil {
		t.Fatalf("user bundle has global diagnostics: %v", entries)
	}
	if !bytes.Contains(entries["visibility.json"], []byte(`"received_recording_shares": 0`)) || !bytes.Contains(entries["visibility.json"], []byte(`"ok": true`)) {
		t.Fatalf("visibility=%s", entries["visibility.json"])
	}
	for name, raw := range entries {
		if bytes.Contains(raw, []byte("inaccessible-private-meeting")) || bytes.Contains(raw, []byte("test-secret")) || bytes.Contains(raw, []byte("alice")) {
			t.Fatalf("private data in %s", name)
		}
	}
}

func TestSupportVisibilityIdentifiesPendingAcceptanceWithoutExportingShares(t *testing.T) {
	rt := supportTestRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
		if r.Method != http.MethodGet || string(auth) != "alice:test-secret" {
			t.Errorf("not a read-only caller probe: %s %s", r.Method, auth)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pending") {
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[{"id":"1","uid_file_owner":"cassini","permissions":0,"item_type":"file","path":"/SECRET.opus","share_with":"alice"},{"uid_file_owner":"someone-else","permissions":1,"item_type":"file"},{"uid_file_owner":"cassini","permissions":1,"item_type":"folder"} ]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
	}))
	defer server.Close()
	c := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "test-secret"}
	entries := supportTestDownload(t, rt, c, false)
	v := entries["visibility.json"]
	for _, want := range []string{`"received_recording_shares": 0`, `"pending_recording_shares": 1`, `"pending_check_ok": true`} {
		if !bytes.Contains(v, []byte(want)) {
			t.Errorf("missing %s: %s", want, v)
		}
	}
	for _, private := range []string{"SECRET", "alice", "someone-else", "test-secret"} {
		if bytes.Contains(v, []byte(private)) {
			t.Errorf("private share detail %s escaped: %s", private, v)
		}
	}
}

func TestSupportPendingProbeFailurePreservesReceivedResult(t *testing.T) {
	rt := supportTestRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pending") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":403,"status":"failure","message":"SECRET"},"data":[]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[{"uid_file_owner":"cassini","permissions":1,"item_type":"file"}]}}`)
	}))
	defer server.Close()
	entries := supportTestDownload(t, rt, ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "test-secret"}, false)
	v := entries["visibility.json"]
	for _, want := range []string{`"ok": true`, `"received_recording_shares": 1`, `"pending_check_ok": false`, `"pending_http_status": 403`, `"pending_ocs_status": 403`} {
		if !bytes.Contains(v, []byte(want)) {
			t.Errorf("missing %s: %s", want, v)
		}
	}
	if bytes.Contains(v, []byte("SECRET")) {
		t.Fatal("raw pending refusal escaped scrubbing")
	}
}

func TestSupportVisibilityRefusalIsNotAnEmptySuccess(t *testing.T) {
	rt := supportTestRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":403,"status":"failure","message":"Sharing is only allowed with group members SECRET"},"data":[]}}`)
	}))
	defer server.Close()
	entries := supportTestDownload(t, rt, ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "secret"}, false)
	v := entries["visibility.json"]
	for _, want := range []string{`"ok": false`, `"http_status": 200`, `"ocs_status": 403`, `sharing_group_restriction`} {
		if !bytes.Contains(v, []byte(want)) {
			t.Errorf("missing %s: %s", want, v)
		}
	}
	if bytes.Contains(v, []byte("SECRET")) {
		t.Fatal("raw refusal escaped")
	}
}

func TestSupportAdminProjectionExcludesAllRawContent(t *testing.T) {
	rt := supportTestRuntime(t)
	const secret = "NEVER_EXPORT_THIS_SENTINEL"
	const jobID = "private-room-title"
	insertJob(t, rt.store.db, jobID, nowUTCString())
	_, err := rt.store.db.Exec(`UPDATE job_attempts SET error=?, request_json=?, transcription_detail=? WHERE job_id=?`, "HTTP 200, OCS 403: Sharing is only allowed with group members "+secret, secret, secret, jobID)
	if err != nil {
		t.Fatal(err)
	}
	path := attemptLogPath(rt.cfg.WorkRoot, jobID, 1, "build")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(secret+" transcript\nCUDA out of memory "+secret+" https://private/call/token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(path), "speaker-edits.json"), []byte(secret), 0600)
	rt.supportJournal.record(supportEvent{Event: "operator_failure", Reason: secret, Job: jobID})
	rt.cfg.TalkSharedSecret = secret
	rt.settings.TranscriptionTerms = []string{secret}
	rt.settings.SearchAliases = [][]string{{secret}}
	t.Setenv(envAppVersion, secret)
	entries := supportTestDownload(t, rt, ExAppConfig{}, true)
	if !bytes.Contains(entries["jobs.json"], []byte("sharing_group_restriction")) || !bytes.Contains(entries["jobs.json"], []byte(`"ocs_status": 403`)) {
		t.Fatalf("lost share failure: %s", entries["jobs.json"])
	}
	var stageName string
	for name, raw := range entries {
		if strings.HasSuffix(name, "/build.jsonl") {
			stageName = name
			if !bytes.Contains(raw, []byte("out_of_memory")) {
				t.Errorf("lost diagnostic: %s", raw)
			}
		}
		if strings.Contains(name, secret) || bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(jobID)) || bytes.Contains(raw, []byte("https://private")) {
			t.Fatalf("raw content in %s: %s", name, raw)
		}
	}
	if stageName == "" {
		t.Fatal("stage log missing")
	}
	if !bytes.Contains(entries["manifest.json"], []byte("source_missing_or_unsafe")) {
		t.Fatal("expired/missing logs not identified")
	}
}

func TestSupportAuthorizationAndRequestBounds(t *testing.T) {
	rt := supportTestRuntime(t)
	for _, test := range []struct {
		name                string
		admin               bool
		caller, token, body string
		want                int
	}{
		{"anonymous user", false, "", "", supportTestBody, 401},
		{"anonymous admin", true, "", "", supportTestBody, 401},
		{"bearer cannot impersonate user", false, "", "test-support-token", supportTestBody, 401},
		{"wrong bearer", true, "", "wrong", supportTestBody, 401},
		{"direct admin", true, "", "test-support-token", supportTestBody, 200},
		{"scope spoof", false, "alice", "", `{"surface":"browse","admin":true}`, 400},
		{"actor spoof", false, "alice", "", `{"surface":"browse","user_id":"bob"}`, 400},
		{"huge body", false, "alice", "", strings.Repeat("x", 4097), 400},
		{"extra JSON", false, "alice", "", supportTestBody + `{}`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := supportUserPath
			if test.admin {
				path = supportAdminPath
			}
			r := callerReq(http.MethodPost, path, test.caller)
			r.Body = io.NopCloser(strings.NewReader(test.body))
			if test.token != "" {
				r.Header.Set("Authorization", "Bearer "+test.token)
			}
			w := httptest.NewRecorder()
			rt.supportBundleHandler(ExAppConfig{}, test.admin).ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, test.want, w.Body.String())
			}
		})
	}
	if !rt.supportGate.acquire("user:alice") {
		t.Fatal("could not reserve export")
	}
	r := callerReq(http.MethodPost, supportUserPath, "alice")
	r.Body = io.NopCloser(strings.NewReader(supportTestBody))
	w := httptest.NewRecorder()
	rt.supportBundleHandler(ExAppConfig{}, false).ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatalf("duplicate export status %d", w.Code)
	}
	rt.supportGate.release("user:alice")
}

func TestSupportPartialBundleSurvivesDatabaseAndUnsafeLogs(t *testing.T) {
	rt := supportTestRuntime(t)
	if err := rt.store.Close(); err != nil {
		t.Fatal(err)
	}
	entries := supportTestDownload(t, rt, ExAppConfig{}, true)
	if entries["configuration.json"] == nil || !bytes.Contains(entries["manifest.json"], []byte("database_unavailable")) {
		t.Fatal("database failure prevented partial bundle")
	}
	// An external symlink and a FIFO must both be refused without reading or
	// blocking, regardless of whether their contents contain recognizable text.
	out := filepath.Join(t.TempDir(), "outside.log")
	_ = os.WriteFile(out, []byte("out of memory PRIVATE"), 0600)
	path := attemptLogPath(rt.cfg.WorkRoot, "job", 1, "record")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.Symlink(out, path); err != nil {
		t.Fatal(err)
	}
	b, _ := newSupportBundle(true)
	rt.collectSupportStageLog(b, "job", "ref-test", 1, "record")
	if len(b.entries) != 0 || b.manifest.Sources[0].State != "omitted" {
		t.Fatal("symlink was read")
	}
	_ = os.Remove(path)
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	b, _ = newSupportBundle(true)
	rt.collectSupportStageLog(b, "job", "ref-test", 1, "record")
	if len(b.entries) != 0 {
		t.Fatal("FIFO was read")
	}
}

func TestSupportBundleHasHardUncompressedLimit(t *testing.T) {
	b, _ := newSupportBundle(true)
	b.addBytes("large.txt", make([]byte, supportMaxBytes), false)
	if b.entries["large.txt"] != nil {
		t.Fatal("accepted oversized entry")
	}
	var out bytes.Buffer
	if err := b.writeZIP(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b.entries["manifest.json"], []byte("bundle_size_limit")) {
		t.Fatal("size omission was silent")
	}
}

func TestSupportSharingDiagnosticsPersistAcrossRestart(t *testing.T) {
	journal := newSupportJournal(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Sharing is only allowed with group members PRIVATE"},"data":[]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
	}))
	defer server.Close()
	c := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "secret", supportJournal: journal}
	ctx := context.WithValue(context.Background(), supportDeliveryKey{}, supportDeliveryContext{job: "job1", attempt: 2})
	_, err := c.reconcileRecordingShares(ctx, server.Client(), ncRecordingsRoot+"/meetings/job1.opus", []sharePrincipal{{Type: "user", ID: "alice"}}, false, sharePublish, "")
	if err == nil {
		t.Fatal("expected a sharing refusal")
	}
	journal = newSupportJournal(journal.workRoot)
	events, failed := journal.read(time.Time{})
	if failed {
		t.Fatal("journal unreadable after restart")
	}
	found := false
	for _, e := range events {
		if e.Event == "share_create" {
			found = true
			if e.Job != "job1" || e.Attempt != 2 || e.HTTP != 200 || e.OCS != 403 || e.Reason != "sharing_group_restriction" {
				t.Errorf("lost evidence: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("share event missing")
	}
	raw, _ := json.Marshal(events)
	if !bytes.Contains(raw, []byte("PRIVATE")) || !bytes.Contains(raw, []byte("alice")) {
		t.Fatal("original share detail discarded before export")
	}
	var projected []supportEvent
	for _, e := range events {
		projected = append(projected, safeSupportEvent(e, func(s string) string {
			if s != "" {
				return "ref-test"
			}
			return ""
		}))
	}
	raw, _ = json.Marshal(projected)
	if bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte("alice")) {
		t.Fatal("scrubbed projection leaked detail")
	}
}

func TestSupportOriginalAndScrubbedBundlesPreserveChoice(t *testing.T) {
	rt := supportTestRuntime(t)
	const detail = "Unfamiliar backend error: alice https://internal.example/call/private-token SECRET"
	insertJob(t, rt.store.db, "private-job", nowUTCString())
	_, err := rt.store.db.Exec(`UPDATE job_attempts SET error=?,transcription_detail=? WHERE job_id=?`, detail, detail, "private-job")
	if err != nil {
		t.Fatal(err)
	}
	path := attemptLogPath(rt.cfg.WorkRoot, "private-job", 1, "build")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(detail+"\n"), 0600)
	_, _ = rt.supportJournal.Write([]byte(detail + "\n"))
	original := supportTestDownloadMode(t, rt, ExAppConfig{}, true, "original")
	if !bytes.Contains(original["jobs.json"], []byte(detail)) || !bytes.Contains(original["logs/operator.jsonl"], []byte(detail)) {
		t.Fatal("original mode lost unfamiliar error")
	}
	found := false
	for name, raw := range original {
		if strings.HasSuffix(name, "/build.log") {
			found = true
			if string(raw) != detail+"\n" {
				t.Fatal("original stage log changed")
			}
		}
	}
	if !found {
		t.Fatal("original stage log absent")
	}
	if !bytes.Contains(original["manifest.json"], []byte(`"privacy": "original"`)) || !bytes.Contains(original["README.txt"], []byte("filtering is OFF")) {
		t.Fatal("original mode not declared")
	}
	scrubbed := supportTestDownloadMode(t, rt, ExAppConfig{}, true, "scrubbed")
	for name, raw := range scrubbed {
		if bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("private-job")) || bytes.Contains(raw, []byte("alice")) {
			t.Fatalf("sensitive data in scrubbed %s", name)
		}
	}
	if !bytes.Contains(scrubbed["README.txt"], []byte("filtering is ON")) {
		t.Fatal("scrubbed mode not declared")
	}
}

func TestSupportOriginalUserBundleStillExcludesGlobalDiagnostics(t *testing.T) {
	rt := supportTestRuntime(t)
	rt.supportJournal.record(supportEvent{Event: "operator_log", Detail: "ANOTHER_USERS_PRIVATE_DATA"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":403,"status":"failure","message":"Unfamiliar error for alice"},"data":[]}}`)
	}))
	defer server.Close()
	entries := supportTestDownloadMode(t, rt, ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "secret"}, false, "original")
	if !bytes.Contains(entries["visibility.json"], []byte("Unfamiliar error for alice")) {
		t.Fatal("original self-error lost")
	}
	for name, raw := range entries {
		if strings.HasPrefix(name, "logs/") || name == "jobs.json" || bytes.Contains(raw, []byte("ANOTHER_USERS_PRIVATE_DATA")) {
			t.Fatalf("original mode widened permissions: %s", name)
		}
	}
}

func TestSupportPrivacyModeDefaultsToOriginalAndRejectsUnknown(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, supportUserPath, strings.NewReader(strings.Replace(supportTestBody, `"privacy":"scrubbed",`, "", 1)))
	b, err := readSupportBrowser(httptest.NewRecorder(), r)
	if err != nil || b.Privacy != "original" {
		t.Fatalf("default mode: %+v %v", b, err)
	}
	r = httptest.NewRequest(http.MethodPost, supportUserPath, strings.NewReader(strings.Replace(supportTestBody, "scrubbed", "bad-mode", 1)))
	if _, err = readSupportBrowser(httptest.NewRecorder(), r); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestSupportOriginalLogsCannotCrowdOutMetadata(t *testing.T) {
	rt := supportTestRuntime(t)
	insertJob(t, rt.store.db, "budget-job", nowUTCString())
	path := attemptLogPath(rt.cfg.WorkRoot, "budget-job", 1, "build")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, bytes.Repeat([]byte("x"), 4096), 0600)
	b, _ := newSupportBundle(true)
	b.remaining = 2048
	rt.collectSupportJobs(context.Background(), b)
	if b.entries["jobs.json"] == nil {
		t.Fatal("original log starved job metadata")
	}
	b.remaining = 0
	var out bytes.Buffer
	if err := b.writeZIP(&out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b.entries["README.txt"], []byte("filtering is OFF")) {
		t.Fatal("README starved by log budget")
	}
	for _, source := range b.manifest.Sources {
		if strings.Contains(source.Name, "/build.") && !strings.HasSuffix(source.Name, ".log") {
			t.Fatalf("wrong original source name: %s", source.Name)
		}
	}
}

func TestSupportOriginalShareListFailureKeepsFullMessage(t *testing.T) {
	message := strings.Repeat("context ", 30) + "DISTINCT_ERROR_AFTER_80_CHARACTERS"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{"meta": map[string]any{"statuscode": 403, "status": "failure", "message": message}, "data": []any{}}})
	}))
	defer server.Close()
	journal := newSupportJournal(t.TempDir())
	c := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "secret", supportJournal: journal}
	_, _ = c.reconcileRecordingShares(context.Background(), server.Client(), ncRecordingsRoot+"/meetings/job.opus", []sharePrincipal{{Type: "user", ID: "alice"}}, false, sharePublish, "")
	events, _ := journal.read(time.Time{})
	for _, event := range events {
		if event.Event == "share_list" && strings.Contains(event.Detail, message) {
			return
		}
	}
	t.Fatal("full share-list failure was discarded")
}

func TestSupportJournalBoundsUnicodeAndRecognizesFailures(t *testing.T) {
	j := newSupportJournal(t.TempDir())
	j.record(supportEvent{Event: "operator_log", Detail: strings.Repeat("你", 6000)})
	_, _ = j.Write([]byte("context deadline exceeded\n"))
	events, failed := j.read(time.Time{})
	if failed {
		t.Fatal("journal unreadable")
	}
	found := false
	for _, event := range events {
		if len(event.Detail) > 16<<10 || !utf8.ValidString(event.Detail) {
			t.Fatal("invalid bounded diagnostic text")
		}
		if event.Reason == "timeout" {
			found = true
		}
	}
	if !found {
		t.Fatal("recognized error lost without the word failed")
	}
}

func TestSupportScrubbingRemovesSubmittedBrowserError(t *testing.T) {
	for _, mode := range []string{"original", "scrubbed"} {
		body := strings.Replace(supportTestBody, `"privacy":"scrubbed"`, `"privacy":"`+mode+`","catalog_error":"PRIVATE_ERROR"`, 1)
		r := httptest.NewRequest(http.MethodPost, supportUserPath, strings.NewReader(body))
		b, err := readSupportBrowser(httptest.NewRecorder(), r)
		if err != nil {
			t.Fatal(err)
		}
		if (b.CatalogError != "") != (mode == "original") {
			t.Fatalf("wrong browser filtering: %+v", b)
		}
	}
}

func TestSupportJournalRotationRetentionAndFailure(t *testing.T) {
	j := newSupportJournal(t.TempDir())
	j.limit = 512
	for i := 0; i < 40; i++ {
		j.record(supportEvent{Event: "operator_failure", Reason: "out_of_memory"})
	}
	for _, name := range []string{"operator.jsonl", "operator.previous.jsonl"} {
		info, err := os.Stat(filepath.Join(j.workRoot, "diagnostics", name))
		if err != nil || info.Size() > j.limit {
			t.Fatalf("unbounded journal %s: %v", name, err)
		}
	}
	if err := j.expire(retentionPolicy{Count: 1, Unit: "days"}, time.Now().AddDate(0, 0, 2)); err != nil {
		t.Fatal(err)
	}
	events, failed := j.read(time.Time{})
	if failed || len(events) != 0 {
		t.Fatalf("old diagnostic events retained: %+v %v", events, failed)
	}
	bad := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(bad, []byte("x"), 0600)
	j = newSupportJournal(bad)
	_, _ = j.Write([]byte("operator failed PRIVATE")) // failure is absorbed, not sent to the pipeline
	if !j.failed {
		t.Fatal("unavailable journal not reported")
	}
}

func TestSupportDeliveryFailureIsCapturedOutsideStageLogs(t *testing.T) {
	rt := supportTestRuntime(t)
	insertJob(t, rt.store.db, "delivery", nowUTCString())
	rt.publishJobFn = func(context.Context, publishTask) (string, error) { return "/no-stage-log", nil }
	rt.publishSink = errPublishSink{err: &ncShareError{HTTPStatus: 200, OCSStatus: 403, Message: "Sharing is only allowed with group members PRIVATE"}}
	if err := rt.store.MarkPublishQueued(context.Background(), "delivery", "/meeting", "/meeting", nowUTCString()); err != nil {
		t.Fatal(err)
	}
	rt.runPublishJob(publishTask{JobID: "delivery", AttemptNumber: 1})
	events, failed := rt.supportJournal.read(time.Time{})
	if failed {
		t.Fatal("journal failed")
	}
	for _, e := range events {
		if e.Event == "delivery_failed" && e.HTTP == 200 && e.OCS == 403 && e.Job == "delivery" {
			return
		}
	}
	t.Fatal("sink failure was not captured")
}

func TestSupportManifestKeepsUserAndAdminRoutesSeparate(t *testing.T) {
	raw, err := os.ReadFile("../../../appinfo/info.xml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Routes []struct {
			URL    string `xml:"url"`
			Verb   string `xml:"verb"`
			Access string `xml:"access_level"`
		} `xml:"external-app>routes>route"`
	}
	if err = xml.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, route := range manifest.Routes {
		if strings.Contains(route.URL, "support") {
			seen[route.URL] = route.Access + ":" + route.Verb
		}
	}
	if seen[`^support\/bundle$`] != "USER:POST" || seen[`^operator\/support-bundle$`] != "ADMIN:POST" || len(seen) != 2 {
		t.Fatalf("unexpected support ACLs: %v", seen)
	}
}

func TestSupportRoutesMountWithEveryOperatorBase(t *testing.T) {
	for _, base := range []string{"/", "/operator", "/custom"} {
		t.Run(base, func(t *testing.T) {
			rt := supportTestRuntime(t)
			rt.cfg.BasePath = base
			handler := newHTTPHandler(rt.logger, rt, ExAppConfig{})
			prefix := strings.TrimRight(base, "/")
			r := httptest.NewRequest(http.MethodPost, prefix+supportAdminPath, strings.NewReader(supportTestBody))
			r.Header.Set("Authorization", "Bearer test-support-token")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("admin status %d: %s", w.Code, w.Body.String())
			}
			// A user endpoint is a sibling of the operator base and never inherits
			// its standalone bearer token. An anonymous request is still refused.
			r = httptest.NewRequest(http.MethodPost, supportUserPath, strings.NewReader(supportTestBody))
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("user status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSupportTemporaryFilesFromACrashAreRemovedConservatively(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, "cassini-support-old.zip")
	fresh := filepath.Join(dir, "cassini-support-fresh.zip")
	other := filepath.Join(dir, "another-file.zip")
	for _, path := range []string{old, fresh, other} {
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{old, other} {
		_ = os.Chtimes(path, now.Add(-48*time.Hour), now.Add(-48*time.Hour))
	}
	cleanupSupportTemps(dir, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale support temp retained")
	}
	for _, path := range []string{fresh, other} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated or fresh file removed: %s", path)
		}
	}
}

func TestSupportRetentionDiscardsCrashFragmentAndExpiresValidEntries(t *testing.T) {
	j := newSupportJournal(t.TempDir())
	f, err := os.OpenFile(filepath.Join(j.workRoot, "diagnostics", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"at":`)
	f.Close()
	if err = j.expire(retentionPolicy{Count: 1, Unit: "days"}, time.Now().AddDate(0, 0, 2)); err == nil {
		t.Fatal("corruption not reported")
	}
	events, _ := j.read(time.Time{})
	if len(events) != 0 {
		t.Fatalf("corruption wedged expiration: %+v", events)
	}
	j.record(supportEvent{Event: "operator_started"})
	events, _ = j.read(time.Time{})
	if len(events) != 1 {
		t.Fatal("journal unusable after recovery")
	}
}

func TestSupportDiagnosticStorageIsCountedAsLogs(t *testing.T) {
	rt := supportTestRuntime(t)
	index, err := rt.storageCategoryIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response := ExAppConfig{}.scanDetailedStorageUsage(context.Background(), rt.cfg.WorkRoot, index.add)
	var size int64
	files := 0
	for _, name := range supportJournalPaths() {
		info, err := os.Stat(filepath.Join(rt.cfg.WorkRoot, filepath.FromSlash(name)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
		files++
	}
	if files == 0 {
		t.Fatal("no journal file written")
	}
	assertArtifactRoot(t, response, "diagnostics", size, files)
	for _, category := range index.result() {
		if category.ID == "logs" {
			if category.Bytes != size {
				t.Fatalf("journal missing from logs: %d", category.Bytes)
			}
			return
		}
	}
	t.Fatal("Logs category missing")
}

func TestSupportOversizedNextcloudResponseIsExplicit(t *testing.T) {
	rt := supportTestRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1))
	}))
	defer server.Close()
	entries := supportTestDownload(t, rt, ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppSecret: "secret"}, false)
	if !bytes.Contains(entries["visibility.json"], []byte("response_too_large")) || !bytes.Contains(entries["visibility.json"], []byte(`"ok": false`)) {
		t.Fatalf("response bound was silent: %s", entries["visibility.json"])
	}
	e := safeSupportEvent(supportEvent{Reason: "response_too_large"}, func(s string) string { return s })
	if e.Reason != "response_too_large" {
		t.Fatal("journal projection lost response-size failure")
	}
}

// Free-text operator output is frequent; share and delivery events are rare
// and are what a support bundle is for. Log volume must not rotate them away.
func TestSupportJournalLogVolumeKeepsShareEvidence(t *testing.T) {
	j := newSupportJournal(t.TempDir())
	j.limit = 4096
	j.record(supportEvent{Event: "share_create", Job: "job-1", Recipient: "user", Reason: "sharing_group_restriction", OCS: 403})
	logger := log.New(j, "", log.LstdFlags)
	for i := 0; i < 500; i++ { // ~25 rotations of the operator stream
		logger.Printf("publish deliver failed id=job-%d: upstream timeout", i)
	}
	events, failed := j.read(time.Time{})
	if failed {
		t.Fatal("journal I/O failed")
	}
	var share, newest bool
	for i, e := range events {
		if e.Event == "share_create" && e.Job == "job-1" && e.OCS == 403 {
			share = true
		}
		if i > 0 && e.At < events[i-1].At {
			t.Fatalf("events not in time order at %d", i)
		}
		newest = newest || strings.Contains(e.Detail, "id=job-499:")
	}
	if !share {
		t.Fatal("operator log volume rotated the share evidence away")
	}
	if !newest {
		t.Fatal("latest operator line missing")
	}
	for _, name := range supportJournalPaths() {
		info, err := os.Stat(filepath.Join(j.workRoot, filepath.FromSlash(name)))
		if err == nil && info.Size() > j.limit {
			t.Fatalf("%s exceeds its generation limit: %d", name, info.Size())
		}
	}
}

// Every request is logged (heartbeats, the Operator page's 2-second poll).
// Those lines still reach stderr but must not fill the support journal.
func TestSupportJournalExcludesRequestLines(t *testing.T) {
	stderr := &syncBuffer{}
	rt, cleanup := newTestRuntimeWithLogger(t, log.New(stderr, "", 0))
	defer cleanup()
	handler := newHTTPHandler(rt.logger, rt, ExAppConfig{})
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz?probe=support-journal", nil))
	rt.logger.Printf("publish deliver failed id=job-1: boom")
	if !strings.Contains(stderr.String(), "/healthz?probe=support-journal") {
		t.Fatalf("request line missing from stderr: %q", stderr.String())
	}
	events, _ := rt.supportJournal.read(time.Time{})
	var operatorLine bool
	for _, e := range events {
		if strings.Contains(e.Detail, "support-journal") {
			t.Fatalf("request line journaled: %+v", e)
		}
		operatorLine = operatorLine || strings.Contains(e.Detail, "id=job-1: boom")
	}
	if !operatorLine {
		t.Fatal("operator output no longer journaled")
	}
}

func TestSupportJobStateDetailIsBounded(t *testing.T) {
	rt := supportTestRuntime(t)
	long := strings.Repeat("x", 16<<10)
	rt.publishStateChangeEvent(StateChangeEvent{JobID: "job-1", Job: Job{Error: &long}})
	events, _ := rt.supportJournal.read(time.Time{})
	for _, e := range events {
		if e.Event == "job_state" {
			if len(e.Detail) > supportJobStateDetailMax || !e.DetailTruncated {
				t.Fatalf("job_state detail not bounded: %d bytes, truncated=%v", len(e.Detail), e.DetailTruncated)
			}
			return
		}
	}
	t.Fatal("job_state not recorded")
}

// A journal file that exists but cannot be opened must not be deleted by the
// rewrite: its events were never read.
func TestSupportRetentionSkipsWhenAFileIsUnreadable(t *testing.T) {
	j := newSupportJournal(t.TempDir())
	j.record(supportEvent{Event: "share_create", Job: "job-1", OCS: 403})
	_, previous := supportJournalFiles(supportStreamOperator)
	if err := os.Mkdir(filepath.Join(j.workRoot, filepath.FromSlash(previous)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := j.expire(retentionPolicy{Count: 1, Unit: "days"}, time.Now().AddDate(0, 0, 2)); err == nil {
		t.Fatal("unreadable journal not reported")
	}
	active, _ := supportJournalFiles(supportStreamEvents)
	raw, err := os.ReadFile(filepath.Join(j.workRoot, filepath.FromSlash(active)))
	if err != nil || !strings.Contains(string(raw), `"share_create"`) {
		t.Fatalf("retention deleted unread history: %v", err)
	}
}

// Readers are offered no scrub choice (D-910): their README must not send
// them back for one, and must say what their bundle does hold.
func TestSupportUserReadmeOffersNoScrubbing(t *testing.T) {
	b, err := newSupportBundle(false)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := b.writeZIP(&out); err != nil {
		t.Fatal(err)
	}
	readme := string(b.entries["README.txt"])
	if strings.Contains(readme, "scrubbing enabled") || strings.Contains(readme, "filtering is OFF") {
		t.Fatalf("reader README offers a choice readers do not have:\n%s", readme)
	}
	if !strings.Contains(readme, "Nextcloud user ID") || !strings.Contains(readme, "no server logs") {
		t.Fatalf("reader README does not say what the bundle holds:\n%s", readme)
	}
}
