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
