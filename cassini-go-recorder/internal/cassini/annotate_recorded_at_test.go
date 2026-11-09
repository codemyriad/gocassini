package cassini

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A file whose manifest knows when the recording started but whose plain tags
// do not say so the same way — no CASSINI_RECORDED_AT_UTC mirror, DATE carrying
// the start with a local offset. The shared rewrite re-derives both from the
// manifest; annotate's tag-preservation check must accept that rather than
// refuse the write.
func TestAnnotateApplyAcceptsTheRecordedAtMirrorsBeingRederived(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	const start = "2026-11-09T11:31:48Z"
	packed := packAnnotateFixture(t, tmp, "meeting")
	retagged := filepath.Join(tmp, "retagged.opus")
	runRetagJSON(t, packed, "--out", retagged, "--recorded-at-utc", start)

	tags, err := portableMeetingTags(retagged)
	if err != nil {
		t.Fatal(err)
	}
	deleteTagAnyCase(tags, "CASSINI_RECORDED_AT_UTC")
	setTagPreservingCase(tags, "DATE", "2026-11-09T12:31:48+01:00")
	path := filepath.Join(tmp, "minimal.opus")
	if err := writePortableMeetingFile(context.Background(), retagged, path, tags); err != nil {
		t.Fatal(err)
	}
	before, err := portableMeetingTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if portableTagValue(before, "CASSINI_RECORDED_AT_UTC") != "" || portableTagValue(before, "DATE") == start {
		t.Fatalf("fixture tags already agree with the manifest: %v", before)
	}
	if got := decodePortableManifestFromOpus(t, path).Meeting.RecordedAtUTC; got != start {
		t.Fatalf("fixture recordedAtUtc = %q, want %q", got, start)
	}

	applyInPlace(t, path, annotateTwoMarks)

	if doc := annotationsIn(t, path); doc == nil || len(doc.Items) != 2 {
		t.Fatalf("annotations after apply = %+v, want the two marks", doc)
	}
	after, err := portableMeetingTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := portableTagValue(after, "CASSINI_RECORDED_AT_UTC"); got != start {
		t.Errorf("CASSINI_RECORDED_AT_UTC = %q, want %q", got, start)
	}
	if got := portableTagValue(after, "DATE"); got != start {
		t.Errorf("DATE = %q, want %q", got, start)
	}
	// Everything else outside the payload still survived.
	for key, value := range before {
		if annotateTagMayMove(key, start) {
			continue
		}
		if got := portableTagValue(after, key); got != strings.TrimSpace(value) {
			t.Errorf("tag %s = %q, want %q", key, got, value)
		}
	}
}

// DATE is free to move only to the manifest's recordedAtUtc; without one, the
// rewrite must leave it exactly as it was.
func TestVerifyAnnotateTagsPreservedHoldsDATEToTheManifest(t *testing.T) {
	const start = "2026-11-09T11:31:48Z"
	const local = "2026-11-09T12:31:48+01:00"
	before := map[string]string{"DATE": local, "TITLE": "Sync"}
	cases := []struct {
		name          string
		after         map[string]string
		recordedAtUTC string
		wantErr       bool
	}{
		{"start re-derived", map[string]string{"DATE": start, "CASSINI_RECORDED_AT_UTC": start, "TITLE": "Sync"}, start, false},
		{"DATE elsewhere", map[string]string{"DATE": "2026-11-09T12:00:00Z", "CASSINI_RECORDED_AT_UTC": start, "TITLE": "Sync"}, start, true},
		{"mirror missing", map[string]string{"DATE": start, "TITLE": "Sync"}, start, true},
		{"no start, DATE kept", map[string]string{"DATE": local, "TITLE": "Sync"}, "", false},
		{"no start, DATE moved", map[string]string{"DATE": start, "TITLE": "Sync"}, "", true},
		{"no start, mirror appeared", map[string]string{"DATE": local, "CASSINI_RECORDED_AT_UTC": start, "TITLE": "Sync"}, "", true},
	}
	for _, tc := range cases {
		err := verifyAnnotateTagsPreserved(before, tc.after, tc.recordedAtUTC)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}
