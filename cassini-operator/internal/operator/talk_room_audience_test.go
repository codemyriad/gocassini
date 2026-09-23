package operator

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func jobAudienceColumns(t *testing.T, db *sql.DB, id string) (audience, audienceAt sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT room_audience, room_audience_at FROM jobs WHERE id = ?`, id).Scan(&audience, &audienceAt); err != nil {
		t.Fatalf("read room audience columns for %s: %v", id, err)
	}
	return audience, audienceAt
}

func openAudienceStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("CASSINI_REPO_ROOT", filepath.Clean(filepath.Join("..", "..", "..")))
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestEncodeRoomAudienceIsCanonical is the property the apply step depends on:
// the same principals must produce the same bytes regardless of the order two
// captures happened to see them in, because D-769 hashes this value so the
// panel and the write can agree on what was shown.
func TestEncodeRoomAudienceIsCanonical(t *testing.T) {
	forward, err := encodeRoomAudience([]aclMapping{
		{Type: "user", ID: "bob"},
		{Type: "group", ID: "dev-team"},
		{Type: "user", ID: "alice"},
	})
	if err != nil {
		t.Fatalf("encodeRoomAudience() error = %v", err)
	}
	reversed, err := encodeRoomAudience([]aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "group", ID: "dev-team"},
		{Type: "user", ID: "bob"},
		// A duplicate must not survive: the two captures overlap almost
		// entirely, so without dedup every roster would double at stop.
		{Type: "user", ID: "bob"},
	})
	if err != nil {
		t.Fatalf("encodeRoomAudience() error = %v", err)
	}
	if forward != reversed {
		t.Errorf("encoding is order-dependent:\n  %s\n  %s", forward, reversed)
	}
	want := `[{"type":"group","id":"dev-team"},{"type":"user","id":"alice"},{"type":"user","id":"bob"}]`
	if forward != want {
		t.Errorf("encodeRoomAudience() = %s, want %s", forward, want)
	}
}

func TestRoomAudienceRoundTripsIntoACLRules(t *testing.T) {
	encoded, err := encodeRoomAudience([]aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "circle", ID: "circle-7"},
	})
	if err != nil {
		t.Fatalf("encodeRoomAudience() error = %v", err)
	}
	decoded, err := decodeRoomAudience(encoded)
	if err != nil {
		t.Fatalf("decodeRoomAudience() error = %v", err)
	}
	if len(decoded) != 2 || decoded[0].Type != "circle" || decoded[0].ID != "circle-7" || decoded[1].Type != "user" || decoded[1].ID != "alice" {
		t.Fatalf("audience round trip = %+v", decoded)
	}
}

func TestDecodeRoomAudienceEmptyAndInvalid(t *testing.T) {
	for _, raw := range []string{"", "   ", "[]"} {
		mappings, err := decodeRoomAudience(raw)
		if err != nil {
			t.Errorf("decodeRoomAudience(%q) error = %v", raw, err)
		}
		if len(mappings) != 0 {
			t.Errorf("decodeRoomAudience(%q) = %+v, want empty", raw, mappings)
		}
	}
	if _, err := decodeRoomAudience("not json"); err == nil {
		t.Error("decodeRoomAudience(\"not json\") error = nil, want an error")
	}
}

// TestMergeJobRoomAudienceUnionsBothCaptures covers the reason the roster is
// taken twice. The start capture cannot see somebody invited half way through
// the call, and the stop capture cannot see somebody removed before the end —
// who was nonetheless in the meeting. Only the union keeps both.
func TestMergeJobRoomAudienceUnionsBothCaptures(t *testing.T) {
	store := openAudienceStore(t)
	ctx := context.Background()
	seedJobRow(t, store.db, seededJobRow{ID: "job-1", Stage: "record", State: "queued", CreatedAt: "2026-09-16T10:00:00.000000000Z"})

	// Start: alice and bob are in the room.
	if err := store.MergeJobRoomAudience(ctx, "job-1", []aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "user", ID: "bob"},
	}, "2026-09-16T10:00:00.000000000Z"); err != nil {
		t.Fatalf("MergeJobRoomAudience(start) error = %v", err)
	}
	_, firstAt := jobAudienceColumns(t, store.db, "job-1")
	if !firstAt.Valid {
		t.Fatal("room_audience_at is NULL after the first capture")
	}

	// Stop: bob has been removed, carol joined during the call.
	if err := store.MergeJobRoomAudience(ctx, "job-1", []aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "user", ID: "carol"},
	}, "2026-09-16T11:00:00.000000000Z"); err != nil {
		t.Fatalf("MergeJobRoomAudience(stop) error = %v", err)
	}

	mappings, captured, err := store.JobRoomAudience(ctx, "job-1")
	if err != nil {
		t.Fatalf("JobRoomAudience() error = %v", err)
	}
	if !captured {
		t.Fatal("JobRoomAudience() captured = false after two captures")
	}
	got := map[string]bool{}
	for _, mapping := range mappings {
		got[mapping.Type+":"+mapping.ID] = true
	}
	for _, want := range []string{"user:alice", "user:bob", "user:carol"} {
		if !got[want] {
			t.Errorf("union is missing %s (got %+v)", want, mappings)
		}
	}
	if len(mappings) != 3 {
		t.Errorf("union has %d principals, want 3 (got %+v)", len(mappings), mappings)
	}

	// The timestamp marks that a capture happened, so the first one owns it.
	_, secondAt := jobAudienceColumns(t, store.db, "job-1")
	if secondAt.String != firstAt.String {
		t.Errorf("room_audience_at moved on the second capture: %q -> %q", firstAt.String, secondAt.String)
	}
}

// TestJobRoomAudienceDistinguishesEmptyFromNeverCaptured is the distinction the
// panel renders differently and the apply step branches on. An empty roster is
// a room whose attendees were all guests — leave it alone. A missing one is a
// recording nothing can narrow, ever.
func TestJobRoomAudienceDistinguishesEmptyFromNeverCaptured(t *testing.T) {
	store := openAudienceStore(t)
	ctx := context.Background()
	seedJobRow(t, store.db, seededJobRow{ID: "guests-only", Stage: "record", State: "queued", CreatedAt: "2026-09-16T10:00:00.000000000Z"})
	seedJobRow(t, store.db, seededJobRow{ID: "never-looked", Stage: "record", State: "queued", CreatedAt: "2026-09-16T10:00:00.000000000Z"})

	// A room of guests resolves to a real, empty answer — and still records
	// that it was asked.
	if err := store.MergeJobRoomAudience(ctx, "guests-only", nil, "2026-09-16T10:00:00.000000000Z"); err != nil {
		t.Fatalf("MergeJobRoomAudience() error = %v", err)
	}

	mappings, captured, err := store.JobRoomAudience(ctx, "guests-only")
	if err != nil {
		t.Fatalf("JobRoomAudience(guests-only) error = %v", err)
	}
	if !captured {
		t.Error("guests-only: captured = false, want true (the lookup succeeded and found nobody grantable)")
	}
	if len(mappings) != 0 {
		t.Errorf("guests-only: mappings = %+v, want empty", mappings)
	}

	_, captured, err = store.JobRoomAudience(ctx, "never-looked")
	if err != nil {
		t.Fatalf("JobRoomAudience(never-looked) error = %v", err)
	}
	if captured {
		t.Error("never-looked: captured = true, want false")
	}
}

// TestCaptureRoomAudienceStoresResolvedPrincipals drives the capture the way
// the start and stop hooks do, through the shared resolver, so the retry ladder
// and the store write are exercised together.
func TestCaptureRoomAudienceStoresResolvedPrincipals(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-audience")

	calls := 0
	rt.talkAudienceRetryGap = time.Millisecond
	rt.fetchTalkParticipants = func(_ context.Context, owner, roomToken string) ([]aclMapping, error) {
		calls++
		if roomToken != "tok123" {
			t.Errorf("fetch called with token=%q, want tok123", roomToken)
		}
		if calls == 1 {
			// A transient failure must not cost the roster: nothing repairs it
			// later, so the ladder is the only protection this value gets.
			return nil, errors.New("nextcloud briefly unreachable")
		}
		return []aclMapping{{Type: "user", ID: "alice"}, {Type: "group", ID: "dev-team"}}, nil
	}

	rt.captureRoomAudience("job-audience", "alice", "tok123", roomAudiencePhaseStart)

	mappings, captured, err := rt.store.JobRoomAudience(context.Background(), "job-audience")
	if err != nil {
		t.Fatalf("JobRoomAudience() error = %v", err)
	}
	if !captured {
		t.Fatal("captured = false after a successful capture")
	}
	if len(mappings) != 2 {
		t.Fatalf("mappings = %+v, want 2 principals", mappings)
	}
}

// TestCaptureRoomAudienceLeavesNothingWhenEveryTierFails is the failure this
// design accepts: the roster is lost and the recording becomes one the panel
// can only explain, never narrow. It must fail that way quietly rather than
// writing a half-answer that would later read as "captured".
func TestCaptureRoomAudienceLeavesNothingWhenEveryTierFails(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-doomed")

	rt.talkAudienceRetryGap = time.Millisecond
	rt.fetchTalkParticipants = func(_ context.Context, _, _ string) ([]aclMapping, error) {
		return nil, errors.New("nextcloud is down")
	}

	rt.captureRoomAudience("job-doomed", "alice", "tok123", roomAudiencePhaseStop)

	audience, audienceAt := jobAudienceColumns(t, rt.store.db, "job-doomed")
	if audience.Valid {
		t.Errorf("room_audience = %q, want NULL after a total lookup failure", audience.String)
	}
	if audienceAt.Valid {
		t.Errorf("room_audience_at = %q, want NULL after a total lookup failure", audienceAt.String)
	}
}

// TestCaptureRoomAudienceSkipsWithoutFetcher keeps a standalone (non-AppAPI)
// operator out of this path entirely: there is no Talk to ask.
func TestCaptureRoomAudienceSkipsWithoutFetcher(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	seedTalkJob(t, rt, "job-standalone")

	rt.fetchTalkParticipants = nil
	rt.captureRoomAudience("job-standalone", "alice", "tok123", roomAudiencePhaseStart)

	if _, audienceAt := jobAudienceColumns(t, rt.store.db, "job-standalone"); audienceAt.Valid {
		t.Error("a standalone operator recorded a room audience")
	}
}

// TestRoomAudienceMigrationLeavesExistingJobsUncaptured is the upgrade case.
// Migration 0011 deliberately has no backfill clause — nothing on the instance
// can say who was in a meeting that has already happened — so every job that
// predates it must read as "never captured" rather than as an empty roster.
func TestRoomAudienceMigrationLeavesExistingJobsUncaptured(t *testing.T) {
	store := openAudienceStore(t)
	seedJobRow(t, store.db, seededJobRow{ID: "pre-existing", Stage: "publish", State: "succeeded", CreatedAt: "2026-08-01T10:00:00.000000000Z"})

	if err := store.migrateDownTo(10); err != nil {
		t.Fatalf("migrateDownTo(10): %v", err)
	}
	if err := store.ensureSchema(); err != nil {
		t.Fatalf("ensureSchema(): %v", err)
	}

	_, captured, err := store.JobRoomAudience(context.Background(), "pre-existing")
	if err != nil {
		t.Fatalf("JobRoomAudience() error = %v", err)
	}
	if captured {
		t.Error("a job that predates the capture reads as captured")
	}
}
