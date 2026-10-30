package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// topicsPreviewStore gives rt an annotations store of its own and returns its
// topic side, locked as a new install is.
func topicsPreviewStore(t *testing.T, rt *Runtime) *topicsStore {
	t.Helper()
	annotations, err := openAnnotationStore(filepath.Join(t.TempDir(), annotationsStoreFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { annotations.Close() })
	rt.annotations = annotations
	return newTopicsStore(annotations)
}

func lockTopicsPreview(t *testing.T, s *topicsService) {
	t.Helper()
	if err := s.store.setTopicsPreviewUnlocked(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

// topicsPreviewCLI runs `cassini-operator topics-preview` against rt's own
// databases, as a shell in the container would beside the serving operator.
func topicsPreviewCLI(t *testing.T, rt *Runtime, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(envAppPersistentStorage, "")
	t.Setenv("CASSINI_OPERATOR_DB_PATH", rt.cfg.DBPath)
	t.Setenv("CASSINI_BIN", topicsPreviewCassiniBin(t, rt))
	var stdout, stderr bytes.Buffer
	code := runTopicsPreview(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// topicsPreviewCassiniBin is rt's CLI, or a stand-in: the commands load the
// operator's config, which wants one, and never run it.
func topicsPreviewCassiniBin(t *testing.T, rt *Runtime) string {
	t.Helper()
	if rt.cfg.CassiniBin != "" {
		return rt.cfg.CassiniBin
	}
	bin := filepath.Join(t.TempDir(), "cassini")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestTopicsPreviewIsLockedOnANewInstall(t *testing.T) {
	rt := &Runtime{}
	if rt.topicsUnlocked(context.Background()) {
		t.Fatal("no annotations store reads as unlocked")
	}
	store := topicsPreviewStore(t, rt)
	rt.setSettings(STTSettings{TopicSuggestions: topicSettings{Enabled: true}})
	if rt.topicsUnlocked(context.Background()) || rt.topicSuggestionsOn(context.Background()) {
		t.Fatal("a new install is unlocked")
	}
	if err := store.setTopicsPreviewUnlocked(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !rt.topicSuggestionsOn(context.Background()) {
		t.Fatal("unlocked and switched on is off")
	}
}

// Locked, every USER route answers exactly as it does while suggestions are
// switched off, even with the Settings switch on and data stored.
func TestTopicsLockedRoutesAnswerAsSwitchedOff(t *testing.T) {
	s, h, topics := topicLibraryService(t, "MEETING1.opus")
	seedSuggestion(t, topics, "tag_release", "Release planning", "MEETING1.opus")
	gets := []string{"", "library/tag_release/history", "library/tag_release/rooms", "meetings/MEETING1", "coverage", "review", "proposals"}
	posts := map[string]string{
		"library":                       createTopicBody("r1", "Budget", "Money."),
		"library/tag_release":           `{"requestId":"r2","expectRevision":1,"definition":"Releases."}`,
		"library/tag_release/history/1": `{"requestId":"r3","action":"redact"}`,
		"meetings/MEETING1/tag_release": `{"requestId":"r4","decision":"confirm"}`,
		"review/batch":                  `{"requestId":"r5","tagId":"tag_release","meetingIds":["MEETING1"],"decision":"reject"}`,
		"proposals/p1":                  `{"requestId":"r6","action":"dismiss"}`,
	}
	if rec := reviewCall(h, http.MethodGet, "meetings/MEETING1", "alice", ""); !strings.Contains(rec.Body.String(), "Release planning") {
		t.Fatalf("unlocked and on, the suggestion is served: %d %s", rec.Code, rec.Body)
	}
	answers := func() map[string]string {
		out := map[string]string{}
		for _, path := range gets {
			rec := reviewCall(h, http.MethodGet, path, "alice", "")
			out["GET "+path] = rec.Result().Status + " " + rec.Body.String()
		}
		for path, body := range posts {
			rec := reviewCall(h, http.MethodPost, path, "alice", body)
			out["POST "+path] = rec.Result().Status + " " + rec.Body.String()
		}
		return out
	}
	settings := s.rt.currentSettings()
	settings.TopicSuggestions.Enabled = false
	s.rt.setSettings(settings)
	off := answers()
	settings.TopicSuggestions.Enabled = true
	s.rt.setSettings(settings)
	lockTopicsPreview(t, topics)
	locked := answers()
	for route, answer := range locked {
		if strings.HasPrefix(route, "GET") && answer != "200 OK {\"enabled\":false}\n" {
			t.Errorf("%s locked: %s", route, answer)
		}
		if strings.HasPrefix(route, "POST") && answer != "409 Conflict {\"error\":\"disabled\"}\n" {
			t.Errorf("%s locked: %s", route, answer)
		}
		if off[route] != answer {
			t.Errorf("%s: switched off %q, locked %q", route, off[route], answer)
		}
	}
	if s.libraryOn(context.Background()) {
		t.Error("the library shapes tagging while locked")
	}
	var verdicts, library int
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_verdict`).Scan(&verdicts)
	topics.store.db.QueryRow(`SELECT COUNT(*) FROM topic_library`).Scan(&library)
	if verdicts != 0 || library != 1 {
		t.Fatalf("a locked route wrote: verdicts=%d library=%d", verdicts, library)
	}
}

// Locked, Settings finds no card: GET is 404, changes are refused, and
// operator/status has no topics block. Unlocking brings back what was there.
func TestTopicsLockedSettingsAreHiddenAndUnlockingRestoresThem(t *testing.T) {
	s, _ := newTopicsTestService(t)
	s.rt.settingsPath = filepath.Join(t.TempDir(), "settings.json")
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	if err := s.store.setMeta(context.Background(), topicMetaEnabledAt, "2026-10-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range operatorAPIRoutes(s.rt, ExAppConfig{}) {
		mux.Handle(route.pattern, route.handler)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	lockTopicsPreview(t, s)
	if rec := call(http.MethodGet, "/settings/topics", ""); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "enabled") {
		t.Fatalf("locked GET: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/settings/topics", "/settings/topics/backfill", "/settings/topics/retry"} {
		method := http.MethodPost
		if path == "/settings/topics" {
			method = http.MethodPut
		}
		if rec := call(method, path, `{"enabled":true}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "topics-preview on") {
			t.Fatalf("locked %s %s: %d %s", method, path, rec.Code, rec.Body)
		}
	}
	if block := s.rt.topicsStatusBlock(context.Background(), true); block != nil {
		t.Fatalf("operator/status has a topics block while locked: %+v", block)
	}
	if _, err := os.Stat(s.rt.settingsPath); err == nil {
		t.Fatal("a locked PUT saved settings")
	}

	unlockTopicsPreview(t, s)
	rec := call(http.MethodGet, "/settings/topics", "")
	var status topicsStatus
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &status) != nil {
		t.Fatalf("unlocked GET: %d %s", rec.Code, rec.Body)
	}
	if !status.Enabled || status.Counts[topicStateQueued] != 1 || status.EnabledAt != "2026-10-01T00:00:00Z" {
		t.Fatalf("unlocking lost the switch or the work: %+v", status)
	}
	if block := s.rt.topicsStatusBlock(context.Background(), true); block == nil || block.Counts[topicStateQueued] != 1 {
		t.Fatalf("operator/status topics block: %+v", block)
	}
	if got := topicRow(t, s, name); got.State != topicStateQueued {
		t.Fatalf("row: %+v", got)
	}
}

// Locked, the worker embeds nothing, the publish hook queues nothing and
// topics.sqlite3 gets no vectors; unlocking picks the queue up again.
func TestTopicsLockedWorkerAndHookDoNothing(t *testing.T) {
	s, cli := newTopicsTestService(t)
	ctx := context.Background()
	name := publishTopicTestMeeting(t, s, "job-a", "transcript A", 1)
	lockTopicsPreview(t, s)
	if s.runOnce(ctx) {
		t.Fatal("the worker ran while locked")
	}
	if calls := cli.calls(); len(calls) != 0 {
		t.Fatalf("the CLI embedded while locked: %v", calls)
	}
	if s.on(ctx) {
		t.Fatal("the loop's reconcile and proposals pass would run while locked")
	}
	s.statsMu.Lock()
	paused := s.stats.Paused
	s.statsMu.Unlock()
	if paused != "disabled" {
		t.Fatalf("paused = %q", paused)
	}
	if _, ok, _ := s.vectors.meeting(ctx, name); ok {
		t.Fatal("vectors were written while locked")
	}
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "catalog.json"), []byte(`{"meetings":[{"id":"job-h","audioPath":"meetings/job-h.opus"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.rt.queuePublishedTopics(publishTask{JobID: "job-h", AttemptNumber: 1, OpusSHA256: "digest"}, site)
	var hooked int
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_meeting WHERE opus_name='job-h.opus'`).Scan(&hooked)
	if hooked != 0 {
		t.Fatal("the publish hook queued a meeting while locked")
	}

	unlockTopicsPreview(t, s)
	s.rt.queuePublishedTopics(publishTask{JobID: "job-h", AttemptNumber: 1, OpusSHA256: "digest"}, site)
	s.store.db.QueryRow(`SELECT COUNT(*) FROM topic_meeting WHERE opus_name='job-h.opus'`).Scan(&hooked)
	if hooked != 1 {
		t.Fatal("unlocked, the publish hook queues")
	}
	if !s.runOnce(ctx) {
		t.Fatal("unlocked, the worker runs")
	}
	if got := topicRow(t, s, name); got.State != topicStateDone {
		t.Fatalf("after unlocking: %+v", got)
	}
}

// The command flips a serving operator without a restart, keeps the Settings
// switch, and says where things stand.
func TestTopicsPreviewCommandFlipsAServingOperator(t *testing.T) {
	s, _ := newTopicsTestService(t)
	settingsFile := settingsPath(s.rt.cfg)
	if err := os.WriteFile(settingsFile, []byte(`{"topic_suggestions":{"enabled":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range operatorAPIRoutes(s.rt, ExAppConfig{}) {
		mux.Handle(route.pattern, route.handler)
	}
	get := func() int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings/topics", nil))
		return rec.Code
	}
	if get() != http.StatusOK {
		t.Fatal("unlocked: no settings")
	}

	code, out, errOut := topicsPreviewCLI(t, s.rt, "off")
	if code != 0 || !strings.HasPrefix(out, "Locked.") {
		t.Fatalf("off: %d %q %q", code, out, errOut)
	}
	if get() != http.StatusNotFound || s.rt.topicSuggestionsOn(context.Background()) {
		t.Fatal("the serving operator did not see the lock")
	}
	if code, out, _ := topicsPreviewCLI(t, s.rt, "status"); code != 0 || out != "preview: locked\nswitch in Settings: on\n" {
		t.Fatalf("status: %d %q", code, out)
	}
	if !s.rt.currentSettings().TopicSuggestions.Enabled {
		t.Fatal("locking changed the Settings switch")
	}

	code, out, _ = topicsPreviewCLI(t, s.rt, "on")
	if code != 0 || !strings.Contains(out, "they run again now") {
		t.Fatalf("on: %d %q", code, out)
	}
	if get() != http.StatusOK || !s.rt.topicSuggestionsOn(context.Background()) {
		t.Fatal("the serving operator did not see the unlock")
	}
	if code, out, _ := topicsPreviewCLI(t, s.rt, "status"); code != 0 || out != "preview: unlocked\nswitch in Settings: on\n" {
		t.Fatalf("status: %d %q", code, out)
	}
	if raw, _ := os.ReadFile(settingsFile); string(raw) != `{"topic_suggestions":{"enabled":true}}` {
		t.Fatalf("the command rewrote settings.json: %s", raw)
	}

	for _, args := range [][]string{nil, {"maybe"}, {"on", "off"}} {
		if code, _, errOut := topicsPreviewCLI(t, s.rt, args...); code != 2 || !strings.Contains(errOut, "on, off or status") {
			t.Fatalf("%v: %d %q", args, code, errOut)
		}
	}
}

func TestTopicsPreviewCommandOnANewInstall(t *testing.T) {
	rt := &Runtime{cfg: Config{DBPath: filepath.Join(t.TempDir(), "jobs.sqlite3")}}
	if code, out, errOut := topicsPreviewCLI(t, rt, "status"); code != 0 || out != "preview: locked\nswitch in Settings: off\n" {
		t.Fatalf("status: %d %q %q", code, out, errOut)
	}
	if code, out, _ := topicsPreviewCLI(t, rt, "on"); code != 0 || !strings.Contains(out, "download the topic suggestions model") {
		t.Fatalf("on: %d %q", code, out)
	}
	if _, err := os.Stat(settingsPath(rt.cfg)); err == nil {
		t.Fatal("the command created settings.json")
	}
}

func TestBackfillTopicsRefusesWhileLocked(t *testing.T) {
	rt := &Runtime{cfg: Config{DBPath: filepath.Join(t.TempDir(), "jobs.sqlite3")}}
	t.Setenv(envNextcloudURL, "http://127.0.0.1:9")
	t.Setenv(envAppSecret, "secret")
	t.Setenv(envAppID, "gocassini")
	t.Setenv(envAppPersistentStorage, "")
	t.Setenv("CASSINI_OPERATOR_DB_PATH", rt.cfg.DBPath)
	t.Setenv("CASSINI_BIN", topicsPreviewCassiniBin(t, rt))
	var stdout, stderr bytes.Buffer
	if code := runBackfillTopics(context.Background(), nil, &stdout, &stderr); code != 3 || !strings.Contains(stderr.String(), "topics-preview on") || !strings.Contains(stderr.String(), "nothing was queued") {
		t.Fatalf("backfill while locked: %d %q", code, stderr.String())
	}
}

// The top-level help and the unknown-command error name no topic command
// while the preview is unannounced, yet the commands still dispatch.
func TestTopicCommandsUnlistedButDispatched(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"no-such-command"}} {
		var stdout, stderr bytes.Buffer
		Run(context.Background(), args, &stdout, &stderr)
		out := stdout.String() + stderr.String()
		if !strings.Contains(out, backfillSearchCommand) {
			t.Fatalf("%v lists no commands:\n%s", args, out)
		}
		for _, hidden := range []string{topicsPreviewCommand, backfillTopicsCommand, topicsExportCommand, topicsEvalCommand, "topic suggestions"} {
			if strings.Contains(out, hidden) {
				t.Fatalf("%v names %q:\n%s", args, hidden, out)
			}
		}
	}
	for _, command := range []string{topicsPreviewCommand, backfillTopicsCommand, topicsExportCommand, topicsEvalCommand} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{command, "--help"}, &stdout, &stderr); code != 0 || strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("%s --help: %d %q", command, code, stderr.String())
		}
	}
}
