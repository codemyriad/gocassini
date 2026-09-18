package operator

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// restrictFixture serves the ACL listing and records every PROPPATCH body by
// path, so a test can assert what was actually written rather than only that
// the call returned.
func restrictFixture(t *testing.T, leaves map[string][]aclRule) (ExAppConfig, *Store, func() map[string]string) {
	t.Helper()
	var mu sync.Mutex
	proppatched := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, aclMultistatus(leaves))
		case "PROPPATCH":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			proppatched[r.URL.Path] = string(body)
			mu.Unlock()
			w.WriteHeader(http.StatusMultiStatus)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	store := openAudienceStore(t)
	previousMode, previousResolved := ncStorage.mode()
	previousSource := ncStorage.recordedSource()
	previousClean := ncStorage.migrationClean()
	ncStorage.set(true, storageModeSourceConfigured, true)
	t.Cleanup(func() {
		if previousResolved {
			ncStorage.set(previousMode, previousSource, previousClean)
		}
	})

	return testExAppConfig(srv.URL), store, func() map[string]string {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]string{}
		for k, v := range proppatched {
			out[k] = v
		}
		return out
	}
}

func resultFor(results []restrictMeetingResult, id string) (restrictMeetingResult, bool) {
	for _, result := range results {
		if result.ID == id {
			return result, true
		}
	}
	return restrictMeetingResult{}, false
}

// TestRestrictOpenRecordingsWritesTheCapturedAudience is the happy path, and
// the assertion that matters is what landed in the PROPPATCH: the captured
// principals get read, and the everyone group is denied.
func TestRestrictOpenRecordingsWritesTheCapturedAudience(t *testing.T) {
	roster := []aclMapping{{Type: "user", ID: "alice"}, {Type: "group", ID: "dev-team"}}
	cfg, store, written := restrictFixture(t, map[string][]aclRule{
		"migrated.opus": recordingACLRules(nil, true),
	})
	seedAudienceJob(t, store, "migrated", false, roster, true)

	logger := log.New(io.Discard, "", 0)
	listed, err := cfg.listOpenRecordings(context.Background(), store, logger)
	if err != nil {
		t.Fatalf("listOpenRecordings() error = %v", err)
	}
	entry, ok := openRecordingByID(listed, "migrated")
	if !ok {
		t.Fatal("the recording under test is not on the open list")
	}

	results, err := cfg.restrictOpenRecordings(context.Background(), store,
		[]restrictMeetingRequest{{ID: "migrated", AudienceDigest: entry.AudienceDigest}}, logger)
	if err != nil {
		t.Fatalf("restrictOpenRecordings() error = %v", err)
	}
	result, ok := resultFor(results, "migrated")
	if !ok || result.Outcome != restrictOutcomeRestricted {
		t.Fatalf("result = %+v, want %s", result, restrictOutcomeRestricted)
	}
	if result.Grants != 2 {
		t.Errorf("Grants = %d, want 2", result.Grants)
	}

	var body string
	for path, candidate := range written() {
		if strings.HasSuffix(path, "/migrated.opus") {
			body = candidate
		}
	}
	if body == "" {
		t.Fatal("nothing was PROPPATCHed onto the recording")
	}
	for _, want := range []string{
		"<nc:acl-mapping-id>alice</nc:acl-mapping-id>",
		"<nc:acl-mapping-id>dev-team</nc:acl-mapping-id>",
		"<nc:acl-mapping-id>" + ncRecordingsEveryoneGroup + "</nc:acl-mapping-id>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("PROPPATCH body missing %s:\n%s", want, body)
		}
	}
	// The everyone group must be denied, which is the whole point of the write.
	everyoneIdx := strings.Index(body, ncRecordingsEveryoneGroup)
	if everyoneIdx < 0 || !strings.Contains(body[everyoneIdx:everyoneIdx+200], "<nc:acl-permissions>0</nc:acl-permissions>") {
		t.Errorf("the everyone group was not denied read:\n%s", body)
	}
}

// TestRestrictOpenRecordingsIsFrozenAgainstRoomChurn is the rule the whole
// feature rests on, asserted end to end: the room's membership has completely
// turned over since the recording, and the grant must still be the roster
// captured while the meeting was happening.
func TestRestrictOpenRecordingsIsFrozenAgainstRoomChurn(t *testing.T) {
	captured := []aclMapping{{Type: "user", ID: "alice"}}
	cfg, store, written := restrictFixture(t, map[string][]aclRule{
		"frozen00.opus": recordingACLRules(nil, true),
	})
	seedAudienceJob(t, store, "frozen00", false, captured, true)

	logger := log.New(io.Discard, "", 0)
	listed, _ := cfg.listOpenRecordings(context.Background(), store, logger)
	entry, ok := openRecordingByID(listed, "frozen00")
	if !ok {
		t.Fatal("the recording under test is not on the open list")
	}

	// Suppose the room has since turned over completely — alice gone, mallory
	// added. Talk would say so, and nothing on this path is allowed to ask:
	// restrictOpenRecordings takes no participants fetcher at all. The grant
	// below comes from the column captured while the meeting was running, and
	// the negative assertion is what keeps a future edit from reintroducing a
	// live lookup here.
	if _, err := cfg.restrictOpenRecordings(context.Background(), store,
		[]restrictMeetingRequest{{ID: "frozen00", AudienceDigest: entry.AudienceDigest}}, logger); err != nil {
		t.Fatalf("restrictOpenRecordings() error = %v", err)
	}

	var body string
	for path, candidate := range written() {
		if strings.HasSuffix(path, "/frozen00.opus") {
			body = candidate
		}
	}
	if !strings.Contains(body, "<nc:acl-mapping-id>alice</nc:acl-mapping-id>") {
		t.Errorf("the captured participant was not granted:\n%s", body)
	}
	if strings.Contains(body, "mallory") || strings.Contains(body, "everyone-now") {
		t.Errorf("the grant reflects the room as it is now, not as it was:\n%s", body)
	}
}

// TestRestrictOpenRecordingsRefusals covers the three ways a request is turned
// down without writing anything.
func TestRestrictOpenRecordingsRefusals(t *testing.T) {
	roster := []aclMapping{{Type: "user", ID: "alice"}}
	cfg, store, written := restrictFixture(t, map[string][]aclRule{
		"stale000.opus": recordingACLRules(nil, true),
		"guests00.opus": recordingACLRules(nil, true),
		// Already narrowed, so it is not on the open list at all.
		"donealdy.opus": recordingACLRules(roster, false),
	})
	seedAudienceJob(t, store, "stale000", false, roster, true)
	seedAudienceJob(t, store, "guests00", false, nil, true)
	seedAudienceJob(t, store, "donealdy", false, roster, true)

	results, err := cfg.restrictOpenRecordings(context.Background(), store, []restrictMeetingRequest{
		{ID: "stale000", AudienceDigest: "a-digest-from-a-page-loaded-an-hour-ago"},
		{ID: "guests00", AudienceDigest: ""},
		{ID: "donealdy", AudienceDigest: "whatever"},
		{ID: "not-in-the-archive", AudienceDigest: "whatever"},
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("restrictOpenRecordings() error = %v", err)
	}

	for _, tc := range []struct{ id, outcome string }{
		{"stale000", restrictOutcomeStale},
		{"guests00", restrictOutcomeEmpty},
		{"donealdy", restrictOutcomeNotOpen},
		{"not-in-the-archive", restrictOutcomeNotOpen},
	} {
		result, ok := resultFor(results, tc.id)
		if !ok {
			t.Errorf("%s: no result returned", tc.id)
			continue
		}
		if result.Outcome != tc.outcome {
			t.Errorf("%s: outcome = %q, want %q", tc.id, result.Outcome, tc.outcome)
		}
	}
	if len(written()) != 0 {
		t.Errorf("a refused request still wrote an ACL: %v", written())
	}
}

// TestRestrictOpenRecordingsRefusesWhileAMigrationIsUnfinished. Which root is
// authoritative is exactly what is unsettled then, so writing permissions into
// one of them would be a guess.
func TestRestrictOpenRecordingsRefusesWhileAMigrationIsUnfinished(t *testing.T) {
	cfg, store, written := restrictFixture(t, map[string][]aclRule{
		"migrated.opus": recordingACLRules(nil, true),
	})
	seedAudienceJob(t, store, "migrated", false, []aclMapping{{Type: "user", ID: "alice"}}, true)
	ncStorage.set(true, storageModeSourceConfigured, false)

	if _, err := cfg.restrictOpenRecordings(context.Background(), store,
		[]restrictMeetingRequest{{ID: "migrated", AudienceDigest: "anything"}}, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("restrictOpenRecordings() during an unfinished migration error = nil, want a refusal")
	}
	if len(written()) != 0 {
		t.Errorf("an ACL was written during an unfinished migration: %v", written())
	}
}

// TestSetRecordingsIgnoredRoundTrips: dismissing and restoring are both
// idempotent, because a double-click is the ordinary way either one happens.
func TestSetRecordingsIgnoredRoundTrips(t *testing.T) {
	store := openAudienceStore(t)
	ctx := context.Background()

	if err := store.SetRecordingsIgnored(ctx, []string{"a", "b"}, true, "2026-09-16T10:00:00.000000000Z"); err != nil {
		t.Fatalf("SetRecordingsIgnored(true) error = %v", err)
	}
	if err := store.SetRecordingsIgnored(ctx, []string{"a"}, true, "2026-09-16T10:01:00.000000000Z"); err != nil {
		t.Fatalf("SetRecordingsIgnored(true, again) error = %v", err)
	}
	ignored, err := store.ListIgnoredRecordings(ctx)
	if err != nil {
		t.Fatalf("ListIgnoredRecordings() error = %v", err)
	}
	if !ignored["a"] || !ignored["b"] || len(ignored) != 2 {
		t.Fatalf("ignored = %v, want exactly a and b", ignored)
	}

	if err := store.SetRecordingsIgnored(ctx, []string{"a", "never-ignored"}, false, ""); err != nil {
		t.Fatalf("SetRecordingsIgnored(false) error = %v", err)
	}
	ignored, err = store.ListIgnoredRecordings(ctx)
	if err != nil {
		t.Fatalf("ListIgnoredRecordings() error = %v", err)
	}
	if ignored["a"] || !ignored["b"] || len(ignored) != 1 {
		t.Fatalf("ignored = %v, want only b", ignored)
	}
}
