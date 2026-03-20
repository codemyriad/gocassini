package meetingtime

import "testing"

func TestInferRecordedAtLocal(t *testing.T) {
	tests := map[string]string{
		"daily-meeting-2026-03-10--12:30.mkv":         "2026-03-10T12:30:00",
		"daily-meeting--2026-03-05--12:38:29.opus":    "2026-03-05T12:38:29",
		"demo-meeting--20260310T123045.meeting":       "2026-03-10T12:30:45",
		"notes-without-a-stamp.txt":                   "",
		"/tmp/path/daily-meeting-2026-03-18--12:30":   "2026-03-18T12:30:00",
	}

	for input, want := range tests {
		if got := InferRecordedAtLocal(input); got != want {
			t.Fatalf("%s: got %q want %q", input, got, want)
		}
	}
}
