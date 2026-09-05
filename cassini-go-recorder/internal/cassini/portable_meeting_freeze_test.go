package cassini

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The portable v1 format is frozen, and the mix splice must not thaw it.
//
// The build's own manifest records what the published audio carries — which
// windows came from whose upload, at what crossfade — because that is provenance
// somebody may want to audit. The published .opus carries none of it: its
// provenance block is a fixed set of fields, and anything the recorder adds
// beside them is dropped when pack decodes the build manifest into it. That
// holds by construction, and this is the test that says so, because "by
// construction" is exactly the kind of claim that stops being true quietly.
func TestTheMixSpliceProvenanceNeverReachesThePortableFile(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()
	meetingDir := filepath.Join(tmp, "spliced.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}

	// A build manifest shaped like one the spliced mix produces.
	manifestPath := filepath.Join(meetingDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	withProvenance := strings.Replace(string(raw), `"speakerCount": 1,`, `"provenance": {
    "sourceAudio": [
      {
        "speaker_id": "spk_host",
        "owner": "host",
        "segments": 1,
        "placed": 1,
        "spliced_ms": 200,
        "mix_spliced": true,
        "crossfade_ms": 15,
        "render_hz": 48000,
        "windows": [{"from_ms": 0, "to_ms": 200, "segment": 0}]
      }
    ]
  },
  "speakerCount": 1,`, 1)
	if withProvenance == string(raw) {
		t.Fatal("could not put source-audio provenance into the fixture manifest")
	}
	if err := os.WriteFile(manifestPath, []byte(withProvenance), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	opusPath := filepath.Join(tmp, "spliced.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, opusPath, portablePackOptions{Title: "Spliced"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}

	tags, err := portableMeetingTags(opusPath)
	if err != nil {
		t.Fatalf("read tags: %v", err)
	}
	payload, err := decodePortableMeetingPayload(tags)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, forbidden := range []string{"sourceAudio", "mix_spliced", "crossfade_ms", "render_hz", "from_ms"} {
		if bytes.Contains(payload, []byte(forbidden)) {
			t.Fatalf("the frozen portable manifest carries %q:\n%s", forbidden, payload)
		}
	}
	for name, value := range tags {
		if strings.Contains(strings.ToLower(value), "mix_spliced") {
			t.Fatalf("tag %s carries the mix splice provenance: %s", name, value)
		}
	}
	// And the file is still a valid portable meeting, splice or no splice.
	if _, _, err := readPortableMeetingManifest(opusPath); err != nil {
		t.Fatalf("the packed file no longer reads as a portable meeting: %v", err)
	}
}
