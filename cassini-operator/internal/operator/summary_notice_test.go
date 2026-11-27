package operator

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func jobSummarySent(t *testing.T, store *Store, jobID string) (host, reason string) {
	t.Helper()
	if err := store.db.QueryRow(`SELECT COALESCE(summary_sent_host, ''), COALESCE(summary_sent_reason, '') FROM jobs WHERE id = ?`, jobID).Scan(&host, &reason); err != nil {
		t.Fatal(err)
	}
	return host, reason
}

func statusServer(t *testing.T, status *atomic.Int32) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// The shared fixture is also read by the Webhooks panel's tests, so the
// panel's preview and the meeting name a receiver the same way, and an
// address the operator cannot name is refused when it is saved.
func TestNoticeHostFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "testdata", "webhook-notice-hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Cases []struct{ URL, Host string } }
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) == 0 {
		t.Fatalf("fixture: %v", err)
	}
	rt := eventWebhookRuntimeForTest(t)
	rt.startEventWebhook()
	for _, c := range fixture.Cases {
		if got := eventNoticeHost(c.URL); got != c.Host {
			t.Errorf("eventNoticeHost(%q) = %q, want %q", c.URL, got, c.Host)
		}
		body, _ := json.Marshal(map[string]any{"url": c.URL, "secret": testSecret, "events": []string{eventSummaryCompleted}})
		code, out := putEventWebhookSettings(t, rt, string(body))
		if refused := code == http.StatusBadRequest && out["error_code"] == "webhook_url_invalid"; refused != (c.Host == "") {
			t.Errorf("PUT %q = %d %v, want refused=%v", c.URL, code, out, c.Host == "")
		}
	}
}

// A summary.completed row records the host and reason when queued. Only a
// 2xx copies them onto the job, so a refused summary, a status event or a
// later change of the reason never shows on the meeting.
func TestASummaryMarksItsJobOnlyWhenAccepted(t *testing.T) {
	store := newTestEventStore(t)
	ctx := context.Background()
	insertJob(t, store.db, "job-1", nowUTCString())
	var status atomic.Int32
	status.Store(http.StatusUnauthorized)
	receiver := statusServer(t, &status) + "/hook"
	settings, err := normalizeEventWebhookSettings(EventWebhookSettings{URL: receiver, Secret: "s", Events: eventCatalog, SummaryNotice: " We keep   summaries. "})
	if err != nil {
		t.Fatal(err)
	}
	hook := newEventWebhook(store, log.New(io.Discard, "", 0), settings, "test")
	for _, eventType := range []string{eventRecordingStarted, eventSummaryCompleted} {
		if err := hook.enqueue(ctx, eventType, map[string]any{"job_id": "job-1"}); err != nil {
			t.Fatal(err)
		}
	}
	hook.deliverDue(ctx)
	if host, _ := jobSummarySent(t, store, "job-1"); host != "" {
		t.Fatalf("a refused summary marked the job: %q", host)
	}
	if notice, ok, _ := store.pendingSummaryNotice(ctx, "job-1", hook.cfg.receiver()); !ok || notice != (summaryNotice{Host: "127.0.0.1", Reason: "We keep summaries."}) {
		t.Fatalf("pending notice = %+v %v, want the waiting row's", notice, ok)
	}

	// A save changes the reason; the queued row keeps what it was queued with.
	settings.SummaryNotice = "Something else."
	later := newEventWebhook(store, log.New(io.Discard, "", 0), settings, "test")
	status.Store(http.StatusNoContent)
	if _, err := store.db.Exec(`UPDATE event_outbox SET next_attempt_at = ?`, nowUTCString()); err != nil {
		t.Fatal(err)
	}
	later.deliverDue(ctx)
	if host, reason := jobSummarySent(t, store, "job-1"); host != "127.0.0.1" || reason != "We keep summaries." {
		t.Fatalf("job = %q %q, want the host and the reason as queued", host, reason)
	}
}

func TestAGivenUpSummaryNeverMarksItsJob(t *testing.T) {
	store := newTestEventStore(t)
	ctx := context.Background()
	insertJob(t, store.db, "job-1", nowUTCString())
	var status atomic.Int32
	status.Store(http.StatusNoContent)
	hook := newTestEventWebhook(t, store, statusServer(t, &status), "s")
	if err := hook.enqueue(ctx, eventSummaryCompleted, map[string]any{"job_id": "job-1"}); err != nil {
		t.Fatal(err)
	}
	old := formatUTCString(time.Now().Add(-eventWebhookRetryWindow - time.Minute))
	if _, err := store.db.Exec(`UPDATE event_outbox SET created_at = ?, next_attempt_at = ?`, old, old); err != nil {
		t.Fatal(err)
	}
	hook.deliverDue(ctx)
	if _, _, failed, _ := outboxRow(t, store); failed == "" {
		t.Fatal("the day-old event was not given up on")
	}
	if host, _ := jobSummarySent(t, store, "job-1"); host != "" {
		t.Fatalf("a given-up summary marked the job: %q", host)
	}
	if _, ok, _ := store.pendingSummaryNotice(ctx, "job-1", hook.cfg.receiver()); ok {
		t.Fatal("a given-up summary is still announced")
	}
}

// The newest queued summary wins: an older row delivered late does not
// replace a newer one's host.
func TestAnOlderSummaryDeliveredLateDoesNotOverwrite(t *testing.T) {
	store := newTestEventStore(t)
	ctx := context.Background()
	insertJob(t, store.db, "job-1", nowUTCString())
	for _, row := range []struct{ id, host, at string }{
		{"evt_old", "old.example", "2026-11-27T10:00:00.000000000Z"},
		{"evt_new", "new.example", "2026-11-27T11:00:00.000000000Z"},
	} {
		if err := store.insertEventFor(ctx, row.id, "rcv_test", eventSummaryCompleted, `{}`, row.at, eventOutboxLink{JobID: "job-1", NoticeHost: row.host}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"evt_new", "evt_old"} {
		if err := store.markEventDelivered(ctx, id, nowUTCString()); err != nil {
			t.Fatal(err)
		}
	}
	if host, _ := jobSummarySent(t, store, "job-1"); host != "new.example" {
		t.Fatalf("host = %q, want the newer row's", host)
	}
}

// A summary rewritten later and not sent (here: queued, then dropped when
// delivery was turned off) leaves the chip: an earlier summary was sent.
func TestARewrittenSummaryThatIsNotSentKeepsTheChip(t *testing.T) {
	store := newTestEventStore(t)
	ctx := context.Background()
	insertJob(t, store.db, "job-1", nowUTCString())
	var status atomic.Int32
	status.Store(http.StatusNoContent)
	hook := newTestEventWebhook(t, store, statusServer(t, &status), "s")
	for i := 0; i < 2; i++ {
		if err := hook.enqueue(ctx, eventSummaryCompleted, map[string]any{"job_id": "job-1"}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			hook.deliverDue(ctx)
		}
	}
	if _, err := store.settleEventOutbox(ctx, EventWebhookSettings{}); err != nil {
		t.Fatal(err)
	}
	if host, _ := jobSummarySent(t, store, "job-1"); host != "127.0.0.1" {
		t.Fatalf("host = %q, want the earlier delivery kept", host)
	}
}

// Only meetings whose summary was accepted get summarySentTo, added when
// served; the stored entry is left as it is.
func TestProjectSummaryNotices(t *testing.T) {
	store := newTestEventStore(t)
	insertJob(t, store.db, "job-1", nowUTCString())
	insertJob(t, store.db, "job-2", nowUTCString())
	if _, err := store.db.Exec(`UPDATE jobs SET summary_sent_host = 'flows.example.org', summary_sent_reason = 'Wiki.' WHERE id = 'job-1'`); err != nil {
		t.Fatal(err)
	}
	entries := []json.RawMessage{
		json.RawMessage(`{"id":"job-1","title":"Planning"}`),
		json.RawMessage(`{"id":"job-2","title":"Standup"}`),
		json.RawMessage(`{"id":"imported","title":"Old call"}`),
	}
	projected, err := store.projectSummaryNotices(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(projected[0]); !strings.Contains(got, `"summarySentTo":{"host":"flows.example.org","reason":"Wiki."}`) || !strings.Contains(got, `"title":"Planning"`) {
		t.Fatalf("first entry = %s", got)
	}
	for _, raw := range projected[1:] {
		if strings.Contains(string(raw), "summarySentTo") {
			t.Fatalf("another meeting got the notice: %s", raw)
		}
	}
	if string(entries[0]) != `{"id":"job-1","title":"Planning"}` {
		t.Fatal("the stored entry was rewritten")
	}
}

func TestDirectShareSnapshotCarriesTheSummaryNotice(t *testing.T) {
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	store := newTestEventStore(t)
	insertJob(t, store.db, "JOB1", nowUTCString())
	if _, err := store.db.Exec(`UPDATE jobs SET summary_sent_host = 'flows.example.org' WHERE id = 'JOB1'`); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Put(context.Background(), 11, "JOB1.opus", json.RawMessage(`{"id":"JOB1","title":"Planning","audioPath":"./meetings/JOB1.opus"}`)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[{"uid_file_owner":"cassini","file_source":11,"permissions":1,"path":"/Shared/JOB1.opus","item_type":"file"}]}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", sharePaths: &recordingSharePathCache{}, lifecycle: store}
	snapshot, err := cfg.directShareSnapshot(context.Background(), server.Client(), "alice", metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.entries) != 1 || !strings.Contains(string(snapshot.entries[0]), `"summarySentTo":{"host":"flows.example.org"}`) {
		t.Fatalf("entries = %s", snapshot.entries)
	}
}

// The reason: one line of plain text, at most 200 characters, no mention;
// stored while summary.completed is unticked, cleared with the receiver.
func TestSummaryNoticeSetting(t *testing.T) {
	rt := eventWebhookRuntimeForTest(t)
	rt.startEventWebhook()
	base := `"url":"https://hooks.example/x","secret":"` + testSecret + `","events":["summary.completed"]`
	for notice, want := range map[string]string{
		`line one\nline two`:                               "webhook_summary_notice_invalid",
		`rtl \u202eoverride`:                               "webhook_summary_notice_invalid",
		`zero\u200bwidth`:                                  "webhook_summary_notice_invalid",
		`\ufeffbom`:                                        "webhook_summary_notice_invalid",
		`ask @alice`:                                       "webhook_summary_notice_mention",
		strings.Repeat("a", 201):                           "webhook_summary_notice_too_long",
		strings.Repeat("\u00e9", 201):                      "webhook_summary_notice_too_long",
		`  We keep   summaries.  `:                         "",
		`Write to team@example.org.`:                       "",
		strings.Repeat("\u00e9", 200):                      "",
		`\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645`: "", // ZWNJ in Persian
		`Family \ud83d\udc68\u200d\ud83d\udc69\u200d\ud83d\udc67`: "", // ZWJ emoji
	} {
		code, out := putEventWebhookSettings(t, rt, `{`+base+`,"summary_notice":"`+notice+`"}`)
		if want == "" && code != http.StatusOK || want != "" && (code != http.StatusBadRequest || out["error_code"] != want) {
			t.Errorf("PUT %q = %d %v, want %q", notice, code, out, want)
		}
	}
	putEventWebhookSettings(t, rt, `{`+base+`,"summary_notice":"  We keep   summaries.  "}`)
	if code, out := putEventWebhookSettings(t, rt, `{"events":["recording.started"]}`); code != http.StatusOK || out["summary_notice"] != "We keep summaries." {
		t.Fatalf("after unticking = %d %v, want the reason trimmed, collapsed and kept", code, out)
	}
	if _, out := putEventWebhookSettings(t, rt, `{"url":""}`); out["summary_notice"] != "" {
		t.Fatalf("after removing the receiver = %v, want no reason", out)
	}
}
