package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// speakerEditsRateLimited decodes a 429 from the speakers route.
func speakerEditsRateLimited(t *testing.T, step string, code int, body string) int64 {
	t.Helper()
	var refusal struct {
		Error        string `json:"error"`
		RetryAfterMs *int64 `json:"retryAfterMs"`
	}
	if code != http.StatusTooManyRequests || json.Unmarshal([]byte(body), &refusal) != nil || refusal.Error != "rate-limited" || refusal.RetryAfterMs == nil {
		t.Fatalf("%s = %d %s, want 429 rate-limited with retryAfterMs", step, code, body)
	}
	return *refusal.RetryAfterMs
}

// One person's saves are counted across every meeting they can read, and
// only saves that queued a refine count. Another person, and reading, are
// not limited by them.
func TestSpeakersPostIsRateLimitedPerUserAcrossMeetings(t *testing.T) {
	t.Setenv(envSpeakerEditsPerHour, "2")
	f := newSpeakersFixture(t)
	f.installModel(t)
	f.nc.frontMu.Lock()
	f.nc.visible["SECRET.opus"] = true
	f.nc.frontMu.Unlock()
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "MEETING1")
	seedSpeakerJob(t, f.rt.store, f.rt.cfg.WorkRoot, "SECRET")
	clock := f.useClock(speakerProgressT0)
	idle := func(id string) {
		t.Helper()
		if _, err := f.rt.store.db.Exec(`UPDATE jobs SET stage = 'done', state = 'succeeded' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	post := func(caller, id string, expect int, label string) (int, string, http.Header) {
		t.Helper()
		body := fmt.Sprintf(`{"expectRevision":%d,"doc":{"labels":[{"speakerId":"spk_room","label":%q}]}}`, expect, label)
		rec := annTestCall(f.h, http.MethodPost, id+"/speakers", caller, body)
		return rec.Code, rec.Body.String(), rec.Header()
	}

	// A refused save is not counted.
	if code, body, _ := post("alice", "MEETING1", 3, "Stale"); code != http.StatusConflict {
		t.Fatalf("stale save = %d %s", code, body)
	}
	if code, body, _ := post("alice", "MEETING1", 0, "Room"); code != http.StatusOK {
		t.Fatalf("first save = %d %s", code, body)
	}
	clock.advance(10 * time.Minute)
	if code, body, _ := post("alice", "SECRET", 0, "Room"); code != http.StatusOK {
		t.Fatalf("second save, another meeting = %d %s", code, body)
	}
	idle("MEETING1")
	code, body, header := post("alice", "MEETING1", 1, "Room A")
	if got := speakerEditsRateLimited(t, "third save", code, body); got != (50 * time.Minute).Milliseconds() {
		t.Fatalf("retryAfterMs = %d, want 50 minutes, when the first save leaves the hour", got)
	}
	if got := header.Get("Retry-After"); got != "3000" {
		t.Fatalf("Retry-After = %q, want 3000", got)
	}
	if rec, err := f.rt.store.GetSpeakerEdits(context.Background(), "MEETING1"); err != nil || rec.Revision != 1 {
		t.Fatalf("a refused save was stored: %+v %v", rec, err)
	}

	// Reading is not limited.
	if resp := f.get(t, "MEETING1"); resp.Revision != 1 {
		t.Fatalf("GET while limited: %+v", resp)
	}
	// Nor is somebody else.
	if code, body, _ := post("bob", "MEETING1", 1, "Room B"); code != http.StatusOK {
		t.Fatalf("bob's save while alice is limited = %d %s", code, body)
	}

	// Once the first save is an hour old, alice saves again.
	clock.advance(50*time.Minute - time.Millisecond)
	idle("MEETING1")
	code, body, _ = post("alice", "MEETING1", 2, "Room C")
	if got := speakerEditsRateLimited(t, "a millisecond early", code, body); got != 1 {
		t.Fatalf("retryAfterMs = %d, want 1", got)
	}
	clock.advance(time.Millisecond)
	if code, body, _ := post("alice", "MEETING1", 2, "Room C"); code != http.StatusOK {
		t.Fatalf("save an hour after the first = %d %s", code, body)
	}
}

func TestSpeakerEditLimiter(t *testing.T) {
	t0 := speakerProgressT0
	off := newSpeakerEditLimiter(0, time.Hour)
	for i := 0; i < 100; i++ {
		if _, _, ok := off.reserve("alice", t0); !ok {
			t.Fatal("a limit of 0 limited")
		}
	}

	l := newSpeakerEditLimiter(1, time.Hour)
	release, _, ok := l.reserve("alice", t0)
	if !ok {
		t.Fatal("first save refused")
	}
	if _, retry, ok := l.reserve("alice", t0.Add(time.Minute)); ok || retry != 59*time.Minute {
		t.Fatalf("second save: ok=%v retry=%s", ok, retry)
	}
	// A save that queued nothing is given back.
	release()
	if _, _, ok := l.reserve("alice", t0.Add(time.Minute)); !ok {
		t.Fatal("a released save still counted")
	}
	// People whose saves all left the window are forgotten.
	l.reserve("bob", t0.Add(2*time.Minute))
	l.reserve("carol", t0.Add(3*time.Hour))
	if len(l.saves) != 1 || len(l.saves["carol"]) != 1 {
		t.Fatalf("saves kept = %v, want carol's only", l.saves)
	}
}
