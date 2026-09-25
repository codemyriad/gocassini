package operator

import "testing"

func TestMeetingIDAcceptsHistoricalClockNamesWithoutAcceptingPaths(t *testing.T) {
	for _, id := range []string{"daily--2026-03-09--12:32:04", "01M34AN9XP4ZPWJ3ZJDQ267VZW"} {
		if !isPlainMeetingID(id) {
			t.Fatalf("rejected historical meeting ID %q", id)
		}
	}
	for _, id := range []string{"", ".", "..", "../meeting", "room/meeting", `room\meeting`, "https://host/meeting", "meeting\nother", "%2f"} {
		if isPlainMeetingID(id) {
			t.Fatalf("accepted unsafe meeting ID %q", id)
		}
	}
}
