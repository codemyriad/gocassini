package portable

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRoomIDIsDeterministicAndDomainSeparated(t *testing.T) {
	// Determinism is the whole feature: a room that derived a different id on
	// each recording would silently split in the meeting list.
	first := RoomIDFromToken("", "a7bc3k9x")
	if first != RoomIDFromToken("", "a7bc3k9x") {
		t.Fatal("the same token derived two different ids")
	}
	if !strings.HasPrefix(first, "rm_") || len(first) != len("rm_")+16 {
		t.Errorf("id = %q, want an rm_ prefix and 16 hex characters", first)
	}

	// The token must not be recoverable by reading the id, and must not appear
	// in it — the point of the whole exercise.
	if strings.Contains(first, "a7bc3k9x") {
		t.Errorf("id %q contains the token", first)
	}

	// Domain separation: a room NAME that happens to equal another room's TOKEN
	// must not derive the same id, or the two rooms merge into one.
	if RoomIDFromName("", "a7bc3k9x") == first {
		t.Error("a name and a token with the same text derived the same id")
	}

	// The pepper changes every id, which is what makes it worth setting.
	if RoomIDFromToken("s3cret", "a7bc3k9x") == first {
		t.Error("the pepper did not change the derivation")
	}
}

func TestRoomIDIsEmptyForAnEmptyInput(t *testing.T) {
	// A meeting with no room must carry NO room id, not the id of the empty
	// string — which would be a real-looking value shared by every roomless
	// meeting, grouping them all into one phantom room.
	for _, blank := range []string{"", "   ", "\t"} {
		if got := RoomIDFromToken("p", blank); got != "" {
			t.Errorf("RoomIDFromToken(%q) = %q, want empty", blank, got)
		}
		if got := RoomIDFromName("p", blank); got != "" {
			t.Errorf("RoomIDFromName(%q) = %q, want empty", blank, got)
		}
	}
	if got := RoomIDForMeeting("p", "  ", "  "); got != "" {
		t.Errorf("RoomIDForMeeting with neither half = %q, want empty", got)
	}
}

func TestRoomIDForMeetingPrefersTheToken(t *testing.T) {
	// The token is the stronger identity: it survives a rename, and two rooms
	// cannot share one. The name is the fallback for files that never carried a
	// token, and the SAME rule has to run in every producer or a room gets one
	// id from the recorder and another from the backfill.
	both := RoomIDForMeeting("p", "a7bc3k9x", "Weekly Sync")
	if both != RoomIDFromToken("p", "a7bc3k9x") {
		t.Errorf("with both halves the token must win, got %q", both)
	}
	nameOnly := RoomIDForMeeting("p", "", "Weekly Sync")
	if nameOnly != RoomIDFromName("p", "Weekly Sync") {
		t.Errorf("with no token the name must be used, got %q", nameOnly)
	}
	if both == nameOnly {
		t.Error("a token-derived and a name-derived id for one room must differ; " +
			"they are reconciled by a human, not by collision")
	}
}

func TestRoomIDTrimsButDoesNotFold(t *testing.T) {
	if RoomIDFromName("p", "  Weekly Sync  ") != RoomIDFromName("p", "Weekly Sync") {
		t.Error("surrounding whitespace changed the derivation")
	}
	// Case folding would merge rooms someone named differently on purpose, and
	// the merge would be unreviewable. Reattribution is where that judgement is
	// made, deliberately and by a person.
	if RoomIDFromName("p", "weekly sync") == RoomIDFromName("p", "Weekly Sync") {
		t.Error("names differing only in case derived the same id; folding must not happen here")
	}
}

// The backfill and the reattribution script derive ids in Node, inside the app
// container. If the two implementations ever disagree, a backfilled room stops
// matching the same room's live recordings — silently, and only in production.
// Pin them against each other.
func TestRoomIDMatchesTheNodeImplementation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}

	// The Node side builds the domain itself, exactly as the scripts do — the
	// separator carries a NUL and could not cross an argv boundary anyway, and
	// reconstructing it here is what actually pins the two CONSTRUCTIONS
	// together rather than only the hashing.
	const script = `
const crypto = require("crypto");
const [, pepper, kind, value] = process.argv;
const domain = "cassini.room." + kind + ".v1\u0000";
const mac = crypto.createHmac("sha256", Buffer.from(pepper, "utf8"));
mac.update(domain, "utf8");
mac.update(value.trim(), "utf8");
process.stdout.write("rm_" + mac.digest("hex").slice(0, 16));
`
	cases := []struct {
		pepper, kind, value string
		got                 string
	}{
		{"", "token", "a7bc3k9x", RoomIDFromToken("", "a7bc3k9x")},
		{"s3cret", "token", "a7bc3k9x", RoomIDFromToken("s3cret", "a7bc3k9x")},
		{"", "name", "Weekly Sync", RoomIDFromName("", "Weekly Sync")},
		{"s3cret", "name", "Weekly Sync", RoomIDFromName("s3cret", "Weekly Sync")},
		// Non-ASCII, because both sides must agree on UTF-8 rather than on
		// whatever their default string encoding happens to be.
		{"pëpper", "name", "Café Sync ☕", RoomIDFromName("pëpper", "Café Sync ☕")},
	}
	for _, tc := range cases {
		out, err := exec.Command(node, "-e", script, tc.pepper, tc.kind, tc.value).Output()
		if err != nil {
			t.Fatalf("node derivation failed for %q: %v", tc.value, err)
		}
		if string(out) != tc.got {
			t.Errorf("node = %s, go = %s (pepper=%q kind=%s value=%q)", out, tc.got, tc.pepper, tc.kind, tc.value)
		}
	}
}
