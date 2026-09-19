package operator

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedAudienceJob inserts a job row that looks like a finished Talk recording:
// a binding carrying the room's publicness, and optionally a captured roster.
func seedAudienceJob(t *testing.T, store *Store, id string, public bool, roster []aclMapping, captured bool) {
	t.Helper()
	seedJobRow(t, store.db, seededJobRow{ID: id, Stage: "publish", State: "succeeded", CreatedAt: "2026-09-1" + id[len(id)-1:] + "T10:00:00.000000000Z"})
	binding, err := encodeTalkBinding(&talkRoomState{
		BackendURL: "https://nc.test",
		RoomToken:  "tok-" + id,
		Owner:      "alice",
		RoomName:   "Room " + id,
		RoomPublic: public,
	})
	if err != nil {
		t.Fatalf("encodeTalkBinding() error = %v", err)
	}
	if err := store.SetJobTalkBinding(context.Background(), id, binding); err != nil {
		t.Fatalf("SetJobTalkBinding() error = %v", err)
	}
	if captured {
		if err := store.MergeJobRoomAudience(context.Background(), id, roster, "2026-09-16T10:00:00.000000000Z"); err != nil {
			t.Fatalf("MergeJobRoomAudience() error = %v", err)
		}
	}
}

// openRecordingsFixture stands up a Nextcloud that answers one ACL PROPFIND,
// and a store, and puts the operator in the access-controlled model.
func openRecordingsFixture(t *testing.T, leaves map[string][]aclRule) (ExAppConfig, *Store) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, aclMultistatus(leaves))
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
	return testExAppConfig(srv.URL), store
}

func openRecordingByID(result openRecordingsResult, id string) (openRecording, bool) {
	for _, entry := range result.Recordings {
		if entry.ID == id {
			return entry, true
		}
	}
	return openRecording{}, false
}

// TestListOpenRecordingsClassifiesEveryBucket is the whole decision tree in one
// archive: what is already restricted, what is open and fixable, what is open
// and must stay open, and what is open with nothing to fix it with.
func TestListOpenRecordingsClassifiesEveryBucket(t *testing.T) {
	roster := []aclMapping{{Type: "user", ID: "alice"}, {Type: "group", ID: "dev-team"}}
	cfg, store := openRecordingsFixture(t, map[string][]aclRule{
		// Already narrowed: states its own audience, so not a row.
		"restricted.opus": recordingACLRules(roster, false),
		// A migration left these readable by everyone.
		"migrated.opus": recordingACLRules(nil, true),
		"public.opus":   recordingACLRules(nil, true),
		"norostr.opus":  recordingACLRules(nil, true),
		"guests00.opus": recordingACLRules(nil, true),
		"nojobrow.opus": recordingACLRules(nil, true),
		// An `everyone` row whose mask does not cover READ decides nothing, so
		// the leaf still inherits the container's grant: world-readable behind
		// an ACL that looks like a restriction.
		"inherits.opus": {
			{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll &^ aclPermRead, Permissions: 0},
			{Type: "user", ID: ncRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll},
		},
	})

	seedAudienceJob(t, store, "restricted", false, roster, true)
	seedAudienceJob(t, store, "migrated", false, roster, true)
	seedAudienceJob(t, store, "public", true, roster, true)
	seedAudienceJob(t, store, "norostr", false, nil, false)
	seedAudienceJob(t, store, "guests00", false, nil, true)
	seedAudienceJob(t, store, "inherits", false, roster, true)
	// nojobrow deliberately has no job row at all.

	result, err := cfg.listOpenRecordings(context.Background(), store, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("listOpenRecordings() error = %v", err)
	}

	if _, listed := openRecordingByID(result, "restricted"); listed {
		t.Error("a recording that already states its own audience was listed")
	}
	if _, listed := openRecordingByID(result, "public"); listed {
		t.Error("a public conversation's recording was listed; it is readable by everyone on purpose")
	}

	migrated, ok := openRecordingByID(result, "migrated")
	if !ok {
		t.Fatal("the migrated recording is missing from the list")
	}
	if !migrated.Narrowable {
		t.Errorf("migrated: Narrowable = false, want true (reason %q)", migrated.Reason)
	}
	if len(migrated.Audience) != 2 {
		t.Errorf("migrated: audience = %+v, want 2 principals", migrated.Audience)
	}
	if migrated.AudienceDigest == "" {
		t.Error("migrated: no audience digest, so the apply step has nothing to compare")
	}
	if migrated.RoomName != "Room migrated" {
		t.Errorf("migrated: RoomName = %q, want the job's room name", migrated.RoomName)
	}

	inherits, ok := openRecordingByID(result, "inherits")
	if !ok {
		t.Fatal("a leaf whose everyone rule omits READ from its mask was treated as restricted")
	}
	if !inherits.Narrowable {
		t.Errorf("inherits: Narrowable = false, want true (reason %q)", inherits.Reason)
	}

	for _, tc := range []struct{ id, reason string }{
		{"norostr", openRecordingNoRoster},
		{"guests00", openRecordingNobodyGrantable},
		{"nojobrow", openRecordingNoJob},
	} {
		entry, ok := openRecordingByID(result, tc.id)
		if !ok {
			t.Errorf("%s: missing from the list", tc.id)
			continue
		}
		if entry.Narrowable {
			t.Errorf("%s: Narrowable = true, want false", tc.id)
		}
		if entry.Reason != tc.reason {
			t.Errorf("%s: Reason = %q, want %q", tc.id, entry.Reason, tc.reason)
		}
		if len(entry.Audience) != 0 {
			t.Errorf("%s: audience = %+v, want none", tc.id, entry.Audience)
		}
	}

	if result.Narrowable != 2 {
		t.Errorf("Narrowable = %d, want 2 (migrated and inherits)", result.Narrowable)
	}
}

// TestListOpenRecordingsHonoursTheIgnoreSet: an ignored recording leaves the
// actionable list but is still reported, so the panel can offer to undo it.
func TestListOpenRecordingsHonoursTheIgnoreSet(t *testing.T) {
	roster := []aclMapping{{Type: "user", ID: "alice"}}
	cfg, store := openRecordingsFixture(t, map[string][]aclRule{
		"keepopen.opus": recordingACLRules(nil, true),
		"fixmenow.opus": recordingACLRules(nil, true),
	})
	seedAudienceJob(t, store, "keepopen", false, roster, true)
	seedAudienceJob(t, store, "fixmenow", false, roster, true)

	if _, err := store.db.Exec(`INSERT INTO ignored_recordings (meeting_id, ignored_at) VALUES (?, ?)`,
		"keepopen", "2026-09-16T12:00:00.000000000Z"); err != nil {
		t.Fatalf("seed ignored recording: %v", err)
	}

	result, err := cfg.listOpenRecordings(context.Background(), store, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("listOpenRecordings() error = %v", err)
	}
	if _, listed := openRecordingByID(result, "keepopen"); listed {
		t.Error("an ignored recording is still on the actionable list")
	}
	if len(result.Ignored) != 1 || result.Ignored[0].ID != "keepopen" {
		t.Errorf("Ignored = %+v, want the one ignored recording", result.Ignored)
	}
	if result.Narrowable != 1 {
		t.Errorf("Narrowable = %d, want 1", result.Narrowable)
	}
}

// TestListOpenRecordingsRefusesInTheDefaultModel. There, every recording is
// readable by everyone by design and no leaf carries rules at all — answering
// "none are open" would be reassurance about a question that does not apply.
func TestListOpenRecordingsRefusesInTheDefaultModel(t *testing.T) {
	cfg, store := openRecordingsFixture(t, map[string][]aclRule{})
	ncStorage.set(false, storageModeSourceConfigured, true)

	if _, err := cfg.listOpenRecordings(context.Background(), store, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("listOpenRecordings() in the default model error = nil, want a refusal")
	}
}

// TestListOpenRecordingsSurfacesAnUnreadableArchive. A PROPFIND that fails must
// not read as an empty list: "no recordings are open" and "Cassini could not
// look" are opposite answers, and only one of them is reassuring.
func TestListOpenRecordingsSurfacesAnUnreadableArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := openAudienceStore(t)
	ncStorage.set(true, storageModeSourceConfigured, true)

	if _, err := testExAppConfig(srv.URL).listOpenRecordings(context.Background(), store, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("listOpenRecordings() with an unreadable archive error = nil, want an error")
	}
}
