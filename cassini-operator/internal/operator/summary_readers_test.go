package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeModelsAndSummaryCLI answers what the speakers fixture's routes ask the
// CLI: the model inventory, and `meetings summarize --estimate`, two words a
// meeting.
func fakeModelsAndSummaryCLI(t *testing.T) string {
	t.Helper()
	return writeFakeCassini(t, `echo "$*" >> "$0.calls"
case "$1 $2" in
"models list") cat "$0.inventory.json" ;;
"meetings summarize")
  shift 2; sep=""; printf '['
  for a in "$@"; do case "$a" in --*) ;; *) printf '%s{"path":"%s","status":"estimated","words":2,"estimatedInputTokens":900}' "$sep" "$a"; sep="," ;; esac; done
  printf ']\n' ;;
*) echo "unexpected command: $*" >&2; exit 1 ;;
esac
`)
}

// newSummaryReadersFixture is the speakers fixture for one published meeting
// with a summary endpoint set up.
func newSummaryReadersFixture(t *testing.T) *speakersFixture {
	t.Helper()
	f := newSpeakersFixture(t)
	f.rt.cfg.CassiniBin = fakeModelsAndSummaryCLI(t)
	writeDiarizerInventory(t, f.rt.cfg.CassiniBin, true, true, true)
	f.rt.setLLMSettings(LLMSettings{
		Providers: []LLMProvider{{ID: "p1", Name: "Hosted", BaseURL: "https://llm.example.test/v1", Model: "summary-model"}},
		Summary:   LLMStep{Enabled: true, Provider: "p1"},
	})
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	return f
}

func readerSummary(t *testing.T, f *speakersFixture, method, caller, body string) (int, readerSummaryState, string) {
	t.Helper()
	rec := annTestCall(f.h, method, "MEETING1/summary", caller, body)
	var state readerSummaryState
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
			t.Fatalf("%s summary: %v %s", method, err, rec.Body.String())
		}
	}
	return rec.Code, state, rec.Body.String()
}

func giveMeetingASummary(t *testing.T, f *speakersFixture) {
	t.Helper()
	meeting := canonicalMeetingPath(f.rt.cfg.WorkRoot, "MEETING1")
	if err := os.WriteFile(filepath.Join(meeting, "summary.md"), []byte("# Summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeArtifactManifest(t, meeting, func(m map[string]any) {
		m["files"] = map[string]any{"summary": "summary.md"}
	})
}

// Any reader may fill a missing summary, once, after seeing what it costs.
func TestReaderSummaryWritesAMissingOneForAnyReader(t *testing.T) {
	f := newSummaryReadersFixture(t)
	code, state, body := readerSummary(t, f, http.MethodGet, "alice", "")
	if code != http.StatusOK || !state.Available || state.HasSummary || !state.CanWrite || state.CanRegenerate ||
		state.Calls != 1 || state.Words != 2 || state.EstimatedInputTokens != 900 || state.Model != "summary-model" ||
		!state.External || state.State != readerSummaryIdle {
		t.Fatalf("GET = %d %+v %s", code, state, body)
	}

	code, state, body = readerSummary(t, f, http.MethodPost, "alice", `{}`)
	if code != http.StatusOK || state.State != readerSummaryWriting || state.CanWrite {
		t.Fatalf("POST = %d %+v %s", code, state, body)
	}
	attempt, err := f.rt.store.GetJobAttempt(context.Background(), "MEETING1", 2)
	if err != nil || attempt.TriggerKind != triggerKindSummarize {
		t.Fatalf("queued attempt = %+v %v", attempt, err)
	}
	// One at a time.
	if code, _, body := readerSummary(t, f, http.MethodPost, "bob", `{}`); code != http.StatusConflict || !strings.Contains(body, "busy") {
		t.Fatalf("second POST = %d %s", code, body)
	}
}

// Replacing a summary changes what every reader sees: only an administrator
// or a moderator of the room may, and only when they say they mean to.
func TestReaderSummaryReplaceNeedsAModerator(t *testing.T) {
	f := newSummaryReadersFixture(t)
	giveMeetingASummary(t, f)

	code, state, body := readerSummary(t, f, http.MethodGet, "alice", "")
	if code != http.StatusOK || !state.HasSummary || state.CanWrite || state.CanRegenerate {
		t.Fatalf("GET as a reader = %d %+v %s", code, state, body)
	}
	if code, _, body := readerSummary(t, f, http.MethodPost, "alice", `{"replace":true}`); code != http.StatusForbidden {
		t.Fatalf("replace by a reader = %d %s", code, body)
	}

	f.canRewriteSummary = true
	if code, _, body := readerSummary(t, f, http.MethodPost, "alice", `{}`); code != http.StatusConflict || !strings.Contains(body, "has-summary") {
		t.Fatalf("unasked replace = %d %s", code, body)
	}
	_, state, _ = readerSummary(t, f, http.MethodGet, "alice", "")
	if !state.CanRegenerate {
		t.Fatalf("GET as a moderator = %+v", state)
	}
	if code, state, body := readerSummary(t, f, http.MethodPost, "alice", `{"replace":true}`); code != http.StatusOK || state.State != readerSummaryWriting {
		t.Fatalf("replace by a moderator = %d %+v %s", code, state, body)
	}
}

func TestReaderSummaryRefusesWhatItCannotDo(t *testing.T) {
	f := newSummaryReadersFixture(t)
	f.rt.setLLMSettings(LLMSettings{})
	_, state, _ := readerSummary(t, f, http.MethodGet, "alice", "")
	if state.Available || state.Reason != summaryUnavailableOff || state.CanWrite {
		t.Fatalf("GET with summaries off = %+v", state)
	}
	if code, _, body := readerSummary(t, f, http.MethodPost, "alice", `{}`); code != http.StatusConflict || !strings.Contains(body, summaryUnavailableOff) {
		t.Fatalf("POST with summaries off = %d %s", code, body)
	}
	if code, _, _ := readerSummary(t, f, http.MethodPost, "alice", `{"replace":"yes"}`); code != http.StatusBadRequest {
		t.Fatalf("bad body = %d", code)
	}
}

// A speaker edit keeps the summary unless the save asks to rewrite it, and
// only someone who may replace the summary can ask.
func TestSpeakersSaveRewritesTheSummaryOnlyWhenAllowed(t *testing.T) {
	f := newSummaryReadersFixture(t)
	rename := func(revision int, summary string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"expectRevision":%d,"doc":{"splits":[],"merges":[],"labels":[{"speakerId":"spk_room","label":"Room %d"}]}%s}`, revision, revision, summary)
		return annTestCall(f.h, http.MethodPost, "MEETING1/speakers", "alice", body)
	}

	if got := f.get(t, "MEETING1"); got.CanRewriteSummary {
		t.Fatalf("a reader was offered the rewrite: %+v", got)
	}
	if rec := rename(0, `,"summary":"rewrite"`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "summary-rewrite") {
		t.Fatalf("rewrite by a reader = %d %s", rec.Code, rec.Body.String())
	}
	if rec := rename(0, `,"summary":"maybe"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown summary choice = %d %s", rec.Code, rec.Body.String())
	}

	// The default keeps it.
	if rec := rename(0, ""); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body.String())
	}
	if rewrite, err := f.rt.store.AttemptRewritesSummary(context.Background(), "MEETING1", 2); err != nil || rewrite {
		t.Fatalf("default save rewrites = %v %v, want keep", rewrite, err)
	}

	// A moderator may ask, and the attempt records it.
	f.canRewriteSummary = true
	if got := f.get(t, "MEETING1"); !got.CanRewriteSummary {
		t.Fatalf("a moderator was not offered the rewrite: %+v", got)
	}
	finishQueuedAttempt(t, f.rt.store, "MEETING1")
	if rec := rename(1, `,"summary":"rewrite"`); rec.Code != http.StatusOK {
		t.Fatalf("rewrite by a moderator = %d %s", rec.Code, rec.Body.String())
	}
	if rewrite, err := f.rt.store.AttemptRewritesSummary(context.Background(), "MEETING1", 3); err != nil || !rewrite {
		t.Fatalf("moderator's save rewrites = %v %v, want rewrite", rewrite, err)
	}
}

// finishQueuedAttempt marks the job's queued attempt done, as a worker would,
// so the next save is not refused as busy.
func finishQueuedAttempt(t *testing.T, store *Store, jobID string) {
	t.Helper()
	now := nowUTCString()
	if _, err := store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'succeeded', updated_at = ? WHERE id = ?`, now, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE job_attempts SET stage = 'done', state = 'succeeded', updated_at = ? WHERE job_id = ? AND state = 'queued'`, now, jobID); err != nil {
		t.Fatal(err)
	}
}

func TestSummaryRewritePermissionsCacheAndRefuseOnError(t *testing.T) {
	calls := 0
	answer, fail := true, false
	p := newSummaryRewritePermissions(func(context.Context, string, string) (bool, error) {
		calls++
		if fail {
			return false, errors.New("nextcloud down")
		}
		return answer, nil
	})
	at := time.Unix(1000, 0)
	p.now = func() time.Time { return at }
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	if !p.allowed(context.Background(), "alice", "JOB", logf) || !p.allowed(context.Background(), "alice", "JOB", logf) || calls != 1 {
		t.Fatalf("calls = %d, want one cached answer", calls)
	}
	at = at.Add(2 * time.Minute)
	fail = true
	if p.allowed(context.Background(), "alice", "JOB", logf) || len(logged) != 1 {
		t.Fatalf("a failed check allowed, or was not logged: %v", logged)
	}
	if p.allowed(context.Background(), "", "JOB", logf) {
		t.Fatal("no caller allowed")
	}
}

// The check asks Nextcloud as the caller: the admin group listing, then the
// meeting's Talk room for their participant type.
func TestNextcloudSummaryRewriteCheck(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "TALKJOB")
	insertJob(t, rt.store.db, "NOROOM", nowUTCString())

	participant := map[string]int{"owner": talkParticipantOwner, "mod": talkParticipantModerator, "member": 3}
	nc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
		user, _, _ := strings.Cut(string(raw), ":")
		switch {
		case strings.HasSuffix(r.URL.Path, "/cloud/groups/admin"):
			if user == "root" {
				_, _ = w.Write([]byte(`{"ocs":{"data":{"users":["root"]}}}`))
				return
			}
			w.WriteHeader(http.StatusForbidden)
		case strings.HasSuffix(r.URL.Path, "/apps/spreed/api/v4/room/tok123"):
			typ, ok := participant[user]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"ocs":{"data":{"participantType":%d}}}`, typ)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer nc.Close()
	s := &annotationService{rt: rt, exapp: testExAppConfig(nc.URL), client: nc.Client()}

	for _, tc := range []struct {
		caller, job string
		want        bool
	}{
		{"root", "TALKJOB", true},
		{"root", "NOROOM", true},
		{"owner", "TALKJOB", true},
		{"mod", "TALKJOB", true},
		{"member", "TALKJOB", false},
		{"stranger", "TALKJOB", false},
		{"mod", "NOROOM", false},
	} {
		got, err := s.nextcloudSummaryRewriteCheck(context.Background(), tc.caller, tc.job)
		if err != nil || got != tc.want {
			t.Errorf("%s on %s = %v %v, want %v", tc.caller, tc.job, got, err, tc.want)
		}
	}
}
