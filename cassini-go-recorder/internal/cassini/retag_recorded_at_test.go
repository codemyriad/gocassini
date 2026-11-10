package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func rawPortableManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	tags, err := portableMeetingTags(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func runRetagJSON(t *testing.T, args ...string) RetagSummary {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), append(append([]string{"retag"}, args...), "--json"), &stdout, &stderr); code != 0 {
		t.Fatalf("retag %v failed code=%d stderr=%q", args, code, stderr.String())
	}
	var summary RetagSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("parse --json summary %q: %v", stdout.String(), err)
	}
	return summary
}

func TestRetagRecordedAtUTCRoundTrip(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before",
		"--title", "Weekly Sync", "--room-token", "a7bc3k9x",
		"--job-id", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "--attempt-number", "2")
	before := decodePortableManifestFromOpus(t, inPath)
	beforeRaw := rawPortableManifest(t, inPath)
	beforeTags, err := portableMeetingTags(inPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Meeting.RecordedAtUTC != "" || portableTagValue(beforeTags, "DATE") != before.Meeting.CreatedAtUTC {
		t.Fatalf("fixture already knows its start: %+v DATE=%q", before.Meeting, portableTagValue(beforeTags, "DATE"))
	}

	const start = "2026-11-09T11:31:48.250Z"
	outPath := filepath.Join(tmp, "after.opus")
	summary := runRetagJSON(t, inPath, "--out", outPath, "--recorded-at-utc", start)
	if summary.Unchanged || len(summary.Changes) != 1 || summary.Changes[0].Field != "recordedAtUtc" ||
		summary.Changes[0].From != nil || summary.Changes[0].To != start {
		t.Fatalf("summary = %+v, want exactly recordedAtUtc null -> %s", summary, start)
	}

	after := decodePortableManifestFromOpus(t, outPath)
	if after.Meeting.ID != before.Meeting.ID || after.Integrity != before.Integrity || after.Audio != before.Audio {
		t.Fatalf("retag changed the meeting's identity or audio:\n before %+v\n after  %+v", before, after)
	}
	if after.Meeting.RecordedAtUTC != start {
		t.Fatalf("meeting.recordedAtUtc = %q, want %q", after.Meeting.RecordedAtUTC, start)
	}
	if after.Meeting.CreatedAtUTC != before.Meeting.CreatedAtUTC {
		t.Fatalf("createdAtUtc moved: %q -> %q", before.Meeting.CreatedAtUTC, after.Meeting.CreatedAtUTC)
	}
	// Every other manifest member carried forward unchanged.
	afterRaw := rawPortableManifest(t, outPath)
	afterMeeting := afterRaw["meeting"].(map[string]any)
	delete(afterMeeting, "recordedAtUtc")
	if !reflect.DeepEqual(beforeRaw, afterRaw) {
		t.Fatalf("retag changed members it was not asked to:\n before %v\n after  %v", beforeRaw, afterRaw)
	}

	afterTags, err := portableMeetingTags(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := portableTagValue(afterTags, "CASSINI_RECORDED_AT_UTC"); got != start {
		t.Errorf("CASSINI_RECORDED_AT_UTC = %q, want %q", got, start)
	}
	if got := portableTagValue(afterTags, "DATE"); got != start {
		t.Errorf("DATE = %q, want the recording start %q", got, start)
	}
	for key, want := range beforeTags {
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "CASSINI_PAYLOAD_") || upper == "DATE" {
			continue
		}
		if got := portableTagValue(afterTags, key); got != strings.TrimSpace(want) {
			t.Errorf("tag %s = %q after retag, want the original %q", key, got, want)
		}
	}

	// Idempotent: the same instant, in either spelling, changes nothing.
	for i, same := range []string{start, "2026-11-09T11:31:48.25Z"} {
		againPath := filepath.Join(tmp, "again"+string(rune('a'+i))+".opus")
		again := runRetagJSON(t, outPath, "--out", againPath, "--recorded-at-utc", same)
		if !again.Unchanged || len(again.Changes) != 0 {
			t.Fatalf("retag with the value the file has (%s) = %+v, want unchanged", same, again)
		}
		if !reflect.DeepEqual(rawPortableManifest(t, outPath), rawPortableManifest(t, againPath)) {
			t.Fatalf("an unchanged retag rewrote the manifest")
		}
	}

	// Another edit through the shared rewrite keeps DATE and the mirror.
	roomPath := filepath.Join(tmp, "room.opus")
	runRetagJSON(t, outPath, "--out", roomPath, "--room-id", "rm_1111111111111111")
	roomTags, err := portableMeetingTags(roomPath)
	if err != nil {
		t.Fatal(err)
	}
	if portableTagValue(roomTags, "DATE") != start || portableTagValue(roomTags, "CASSINI_RECORDED_AT_UTC") != start {
		t.Fatalf("a later retag lost the start: %v", roomTags)
	}

	// The text form says so too.
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"retag", outPath, "--out", filepath.Join(tmp, "text.opus"), "--recorded-at-utc", start}, &stdout, &stderr); code != 0 {
		t.Fatalf("retag failed code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "unchanged") {
		t.Fatalf("text output %q does not report unchanged", stdout.String())
	}
}

func TestRetagRecordedAtUTCRefusesNonUTCValues(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	inPath := packFixtureOpus(t, tmp, "before")
	for _, value := range []string{"", "2026-11-09T12:31:48+01:00", "2026-11-09T12:31:48", "2026-11-09", "yesterday"} {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"retag", inPath, "--out", filepath.Join(tmp, "after.opus"), "--recorded-at-utc", value}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("--recorded-at-utc %q: code=%d, want 2 (stderr %q)", value, code, stderr.String())
		}
	}
}
