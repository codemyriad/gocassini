package cassini

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedPullRefreshesSameSizeChangesAndRejectsTampering(t *testing.T) {
	bodies := map[string][]byte{"MEETING1": []byte("original")}
	fake := newMeetingsFakeNextcloud(t, servePackArchive(oneMeetingCatalog, bodies, nil))
	out := t.TempDir()
	pull := func() {
		t.Helper()
		if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out); code != 0 {
			t.Fatal(stderr)
		}
	}
	pull()
	bodies["MEETING1"] = []byte("modified")
	pull()
	file := filepath.Join(out, "meetings", "MEETING1.opus")
	got, _ := os.ReadFile(file)
	if string(got) != "modified" {
		t.Fatalf("stale content: %s", got)
	}
	if _, err := resolveDevStackPublishedSeedDir(out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDevStackPublishedSeedDir(out); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("accepted tampering: %v", err)
	}
	pull()
	if _, err := resolveDevStackPublishedSeedDir(out); err != nil {
		t.Fatal(err)
	}
	manifest := readPackManifest(t, out)
	manifest.Complete = false
	if err := writeSeedPackManifest(out, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDevStackPublishedSeedDir(out); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("accepted incomplete pack: %v", err)
	}
}

func TestSeedPullCurrentAnnotationsEmbedsAcceptedSnapshot(t *testing.T) {
	requireFFMediaTools(t)
	file := packAnnotateFixture(t, t.TempDir(), "current")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	result := applyInPlace(t, file, annotateTwoMarks)
	archive := servePackArchive(oneMeetingCatalog, map[string][]byte{"MEETING1": original}, nil)
	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/annotations/meetings/") {
			json.NewEncoder(w).Encode(map[string]any{"meetingId": "MEETING1", "revision": result.Revision, "annotations": json.RawMessage(result.Annotations)})
			return
		}
		if r.URL.Path == meetingsTestListPath {
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			fmt.Fprint(w, oneMeetingCatalog)
			return
		}
		archive(w, r)
	})
	out := t.TempDir()
	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out, "--annotations", "current"); code != 0 {
		t.Fatal(stderr)
	}
	doc := annotationsIn(t, filepath.Join(out, "meetings", "MEETING1.opus"))
	if doc == nil || len(doc.Items) != 2 {
		t.Fatalf("current annotations were not embedded: %+v", doc)
	}
	if _, err := resolveDevStackPublishedSeedDir(out); err != nil {
		t.Fatal(err)
	}
}
