package operator

import (
	"testing"
	"time"
)

func TestNextcloudPolicyOrderingAndDeadlines(t *testing.T) {
	forever := retentionPolicy{Forever: true}
	days := func(n int) retentionPolicy { return retentionPolicy{Count: n, Unit: "days"} }
	for _, tc := range []struct {
		audio, text retentionPolicy
		valid       bool
		action      string
	}{
		{forever, forever, true, "keep"}, {days(30), forever, true, "convert"}, {days(30), days(90), true, "convert"}, {days(30), days(30), true, "retire"}, {days(90), days(30), false, ""}, {forever, days(90), false, ""},
	} {
		p := nextcloudRetentionSettings{tc.audio, tc.text}
		err := p.validate()
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", p, err)
		}
		if !tc.valid {
			continue
		}
		m := meetingLifecycle{Name: "m.opus", Anchor: "2026-08-01T23:59:59Z", AnchorSource: "recording-completed", State: "active", Representation: "opus"}
		result := evaluateRemoteRetention(m, p, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
		if result.Action != tc.action {
			t.Fatalf("%+v: %+v", p, result)
		}
	}
}
