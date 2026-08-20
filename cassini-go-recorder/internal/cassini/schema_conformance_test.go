package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestPackedMeetingKeysAreDeclaredBySchema is the guard against the one drift
// this format cannot absorb: adding a field to portable.Meeting and forgetting
// the schema.
//
// Both schemas declare the meeting object with "additionalProperties": false,
// so a key a producer emits and a schema does not declare makes every file that
// producer writes invalid — against a document nothing in CI validates, so it
// would be found by a consumer rather than by us.
//
// It reads the real schema file and a really-packed file, rather than comparing
// the struct against a list: a list would be a third thing to keep in step.
func TestPackedMeetingKeysAreDeclaredBySchema(t *testing.T) {
	requireFFMediaTools(t)

	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "conformance.meeting")
	if err := writeReadyMeetingBundleFixture(bundleDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	outPath := filepath.Join(tmp, "conformance.opus")
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"pack", bundleDir, "--out", outPath,
		// Everything optional, so the packed file exercises the widest key set
		// a producer can emit.
		"--title", "Weekly Sync", "--room-token", "a7bc3k9x", "--room-name", "Weekly Sync",
		"--job-id", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", "--attempt-number", "2",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}

	tags, err := portableMeetingTags(outPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var document struct {
		Meeting map[string]json.RawMessage `json:"meeting"`
	}
	if err := json.Unmarshal(rawJSON, &document); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if len(document.Meeting) == 0 {
		t.Fatal("the packed manifest has no meeting object")
	}

	// `cassini pack` writes v2, so that is the schema this file must satisfy.
	// v1 is checked too, because the same Meeting struct is what a v1 write
	// would serialise.
	for _, schema := range []struct {
		path string
		at   func(map[string]any) map[string]any
	}{
		{
			path: "../../../spec/cassini-portable-meeting-manifest-v2.schema.json",
			at:   func(doc map[string]any) map[string]any { return nested(doc, "$defs", "meeting") },
		},
		{
			path: "../../../spec/cassini-portable-meeting-manifest-v1.schema.json",
			at:   func(doc map[string]any) map[string]any { return nested(doc, "properties", "meeting") },
		},
	} {
		raw, err := os.ReadFile(schema.path)
		if err != nil {
			t.Fatalf("read %s: %v", schema.path, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", schema.path, err)
		}
		meeting := schema.at(doc)
		if meeting == nil {
			t.Fatalf("%s has no meeting object", schema.path)
		}
		// The check only means anything while this is false; if it is ever
		// relaxed, this test should be reconsidered rather than silently pass.
		if additional, ok := meeting["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s: meeting.additionalProperties is not false; this test assumes it is", schema.path)
		}
		declared, _ := meeting["properties"].(map[string]any)
		var undeclared []string
		for key := range document.Meeting {
			if _, ok := declared[key]; !ok {
				undeclared = append(undeclared, key)
			}
		}
		sort.Strings(undeclared)
		if len(undeclared) > 0 {
			t.Errorf("%s does not declare meeting field(s) a packed file emits: %v", schema.path, undeclared)
		}
	}
}

func nested(doc map[string]any, keys ...string) map[string]any {
	current := doc
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}
