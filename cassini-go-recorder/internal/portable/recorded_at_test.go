package portable

import "testing"

func TestRecordedAtUTCTagsAndDate(t *testing.T) {
	known := publishedFixtureTags(t, publishedManifestFixture(Meeting{
		Title: "Weekly Sync", RecordedAtUTC: "2026-03-13T09:58:12.250Z",
	}))
	for key, want := range map[string]string{
		"CASSINI_RECORDED_AT_UTC": "2026-03-13T09:58:12.250Z",
		// DATE is when the recording started once that is known...
		"DATE": "2026-03-13T09:58:12.250Z",
		// ...while CASSINI_CREATED_AT keeps meaning pack time.
		"CASSINI_CREATED_AT": "2026-03-13T10:00:00Z",
	} {
		if got := known[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	unknown := publishedFixtureTags(t, publishedManifestFixture(Meeting{Title: "Some File"}))
	if _, ok := unknown["CASSINI_RECORDED_AT_UTC"]; ok {
		t.Error("CASSINI_RECORDED_AT_UTC is present without a value")
	}
	if got := unknown["DATE"]; got != "2026-03-13T10:00:00Z" {
		t.Errorf("DATE without a start = %q, want createdAtUtc", got)
	}
}

func TestParseRecordedAtUTC(t *testing.T) {
	for value, want := range map[string]bool{
		"2026-11-09T11:31:48Z":           true,
		"2026-11-09T11:31:48.250Z":       true,
		"2026-11-09T11:31:48.123456789Z": true,
		"2026-11-09T12:31:48+01:00":      false, // an offset is not UTC Z
		"2026-11-09T11:31:48":            false, // a label, not an instant
		"2026-11-09T11:31Z":              false,
		"2026-11-09 11:31:48Z":           false,
		"2026-02-30T11:31:48Z":           false,
		"":                               false,
	} {
		if got := ValidRecordedAtUTC(value); got != want {
			t.Errorf("ValidRecordedAtUTC(%q) = %v, want %v", value, got, want)
		}
	}
}

// The format's "When the meeting happened": recordedAtUtc, else a DATE with an
// offset that is not the pack time, else the recordedAtLocal label.
func TestMeetingStartOf(t *testing.T) {
	const created = "2026-11-09T10:00:00Z"
	for _, tc := range []struct {
		name       string
		meeting    Meeting
		date       string
		wantSource string
		wantValue  string
	}{
		// Conformance vector 027.
		{"recordedAtUtc wins", Meeting{CreatedAtUTC: created, RecordedAtUTC: "2026-11-09T09:31:48.250Z", RecordedAtLocal: "2026-11-09T09:31:48"}, "2026-11-09T09:31:48.250Z", MeetingStartRecordedAtUTC, "2026-11-09T09:31:48.250Z"},
		// Conformance vector 028: DATE is only the pack stamp.
		{"DATE equal to createdAtUtc", Meeting{CreatedAtUTC: created, RecordedAtLocal: "2026-11-09T09:31:48"}, created, MeetingStartRecordedAtLocal, "2026-11-09T09:31:48"},
		{"DATE the same instant as createdAtUtc in another spelling", Meeting{CreatedAtUTC: created, RecordedAtLocal: "2026-11-09T09:31:48"}, "2026-11-09T11:00:00.000+01:00", MeetingStartRecordedAtLocal, "2026-11-09T09:31:48"},
		// Conformance vector 029: DATE with an offset outranks the label.
		{"DATE with an offset", Meeting{CreatedAtUTC: created, RecordedAtLocal: "2026-11-09T09:31:48"}, "2026-11-09T10:31:48+01:00", MeetingStartDATE, "2026-11-09T10:31:48+01:00"},
		{"DATE in Z", Meeting{CreatedAtUTC: created}, "2026-11-09T09:31:48Z", MeetingStartDATE, "2026-11-09T09:31:48Z"},
		{"DATE without an offset is not an instant", Meeting{CreatedAtUTC: created, RecordedAtLocal: "2026-11-09T09:31:48"}, "2026-11-09T09:31:48", MeetingStartRecordedAtLocal, "2026-11-09T09:31:48"},
		{"DATE only a day", Meeting{CreatedAtUTC: created}, "2026-11-09", "", ""},
		{"malformed recordedAtUtc falls through", Meeting{CreatedAtUTC: created, RecordedAtUTC: "2026-11-09T09:31:48+01:00"}, "2026-11-09T09:31:48+01:00", MeetingStartDATE, "2026-11-09T09:31:48+01:00"},
		{"createdAtUtc alone is unknown", Meeting{CreatedAtUTC: created}, created, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MeetingStartOf(tc.meeting, tc.date)
			if got.Source != tc.wantSource || got.Value != tc.wantValue {
				t.Fatalf("MeetingStartOf = %s %q, want %s %q", got.Source, got.Value, tc.wantSource, tc.wantValue)
			}
			if got.Instant != (tc.wantSource == MeetingStartRecordedAtUTC || tc.wantSource == MeetingStartDATE) {
				t.Fatalf("Instant = %v for source %q", got.Instant, got.Source)
			}
		})
	}
	at := MeetingStartOf(Meeting{CreatedAtUTC: created}, "2026-11-09T10:31:48+01:00").At
	if want := "2026-11-09T09:31:48Z"; at.Format("2006-01-02T15:04:05Z07:00") != want {
		t.Fatalf("DATE instant = %s, want %s", at, want)
	}
}
