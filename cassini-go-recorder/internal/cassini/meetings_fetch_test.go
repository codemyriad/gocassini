package cassini

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inspectpkg "gocassini/internal/inspect"
)

const oneMeetingCatalog = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "MEETING1", "title": "Daily Standup", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/MEETING1.opus", "speakerCount": 1, "segmentCount": 2}
  ]
}`

// serveCatalogAndOpus answers the catalog route from the given JSON and the
// meeting asset route with opusBody, 404ing everything else — the same shape the
// app presents.
func serveCatalogAndOpus(catalog string, opusBody []byte) func(w http.ResponseWriter, r *http.Request) {
	const assetPath = "/index.php/apps/app_api/proxy/gocassini/published/meetings/MEETING1.opus"
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case meetingsTestCatalogPath:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			fmt.Fprint(w, catalog)
		case assetPath:
			if opusBody == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "audio/ogg")
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			_, _ = w.Write(opusBody)
		default:
			http.NotFound(w, r)
		}
	}
}

// fetchedMeetingIsByteIdenticalAndParseable is the property that matters: what
// lands on disk is the published file, not a re-encoding, so the local extractor
// accepts it exactly as the viewer would.
func TestMeetingsFetchWritesAByteIdenticalParseableOpus(t *testing.T) {
	requireFFMediaTools(t)
	tmp := t.TempDir()

	meetingDir := filepath.Join(tmp, "good.meeting")
	if err := writeReadyMeetingBundleFixture(meetingDir, "/tmp/source.mkv"); err != nil {
		t.Fatalf("write ready meeting bundle: %v", err)
	}
	sourceOpus := filepath.Join(tmp, "published.opus")
	if err := packMeetingBundle(context.Background(), meetingDir, sourceOpus, portablePackOptions{Title: "Daily Standup"}); err != nil {
		t.Fatalf("pack meeting bundle: %v", err)
	}
	published, err := os.ReadFile(sourceOpus)
	if err != nil {
		t.Fatalf("read packed opus: %v", err)
	}

	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, published))
	outPath := filepath.Join(tmp, "downloaded", "Daily Standup.opus")

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", outPath)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "portable_meeting -> "+outPath) {
		t.Errorf("expected the portable_meeting arrow line, got %q", stdout)
	}

	downloaded, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read downloaded opus: %v", err)
	}
	if !bytes.Equal(downloaded, published) {
		t.Fatalf("downloaded %d bytes, want the %d published bytes byte-for-byte", len(downloaded), len(published))
	}
	if _, err := inspectpkg.ExtractMeeting(outPath); err != nil {
		t.Fatalf("the downloaded file should parse as a portable meeting: %v", err)
	}
}

// The id may come before every flag or after every flag. Both forms have to
// work, and the leading form in particular must not swallow --out.
//
// A positional wedged between flags is not supported and cannot be: Go's flag
// package stops parsing at the first non-flag argument. build, pack and publish
// have the same constraint.
func TestMeetingsFetchAcceptsTheIDBeforeOrAfterFlags(t *testing.T) {
	body := []byte("opus-bytes")
	for _, order := range []struct {
		name   string
		layout func(outPath, baseURL string) []string
	}{
		{"id before every flag", func(outPath, baseURL string) []string {
			return []string{"meetings", "fetch", "MEETING1",
				"--out", outPath, "--nextcloud-url", baseURL, "--user", "alice", "--app-password", "pw"}
		}},
		{"id after every flag", func(outPath, baseURL string) []string {
			return []string{"meetings", "fetch",
				"--out", outPath, "--nextcloud-url", baseURL, "--user", "alice", "--app-password", "pw",
				"MEETING1"}
		}},
	} {
		t.Run(order.name, func(t *testing.T) {
			tmp := t.TempDir()
			outPath := filepath.Join(tmp, "m.opus")
			fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, body))

			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), order.layout(outPath, fake.server.URL), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Errorf("wrote %q, want %q", got, body)
			}
		})
	}
}

// A flag placed after the meeting id cannot be parsed, so the error has to point
// at the ordering rather than leaving the caller to guess.
func TestMeetingsFetchExplainsFlagsAfterTheID(t *testing.T) {
	tmp := t.TempDir()
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("opus")))

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"meetings", "fetch",
		"--out", filepath.Join(tmp, "m.opus"), "MEETING1",
		"--nextcloud-url", fake.server.URL, "--user", "alice", "--app-password", "pw"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flags must come before the meeting id") {
		t.Errorf("stderr=%q, want it to explain the argument ordering", stderr.String())
	}
}

// An id absent from the caller's catalog — which is also how an id the caller
// may not read presents — must be reported without claiming either.
func TestMeetingsFetchUnknownIDReportsNotReadable(t *testing.T) {
	tmp := t.TempDir()
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("opus")))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "SOMEONE-ELSES", "--out", filepath.Join(tmp, "x.opus"))

	if code != 1 {
		t.Fatalf("exit=%d, want 1", code)
	}
	if !strings.Contains(stderr, `no recording "SOMEONE-ELSES" you can read`) {
		t.Errorf("stderr=%q, want it to say the recording is not readable", stderr)
	}
	for _, banned := range []string{"forbidden", "permission denied", "does not exist"} {
		if strings.Contains(strings.ToLower(stderr), banned) {
			t.Errorf("stderr must not say %q: %q", banned, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "x.opus")); !os.IsNotExist(err) {
		t.Errorf("no output file should be created on failure, stat err=%v", err)
	}
}

// A meeting listed in the catalog whose asset 404s is the race where a recording
// was deleted between listing and fetching. It must exit 1 and leave nothing
// behind.
func TestMeetingsFetchAssetGoneLeavesNoPartialFile(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "m.opus")
	fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, nil))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", outPath)

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stderr, "no recording you can read") {
		t.Errorf("stderr=%q", stderr)
	}
	assertNoStrayFiles(t, tmp)
}

// A transfer that dies mid-body must not leave a truncated .opus at the
// destination: a later extract would report it as a corrupt meeting rather than
// an interrupted download.
func TestMeetingsFetchTruncatedTransferLeavesNoFileAtDestination(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "m.opus")

	fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == meetingsTestCatalogPath {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			fmt.Fprint(w, oneMeetingCatalog)
			return
		}
		// Promise more than we send, then hang up: io.Copy sees an unexpected EOF.
		w.Header().Set("Content-Length", "4096")
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("truncated"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		panic(http.ErrAbortHandler)
	})

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "fetch", "MEETING1", "--out", outPath)

	if code != 1 {
		t.Fatalf("exit=%d, want 1 (stderr=%q)", code, stderr)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("a truncated download must not land at the destination, stat err=%v", err)
	}
	assertNoStrayFiles(t, tmp)
}

func TestMeetingsFetchUsageErrors(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"no id", []string{"fetch", "--out", "/tmp/x.opus"}, "exactly one meeting id"},
		{"two ids", []string{"fetch", "A", "B", "--out", "/tmp/x.opus"}, "exactly one meeting id"},
		{"no out", []string{"fetch", "MEETING1"}, "--out is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newMeetingsFakeNextcloud(t, serveCatalogAndOpus(oneMeetingCatalog, []byte("opus")))
			code, _, stderr := runMeetingsCLI(t, fake.server.URL, tc.args...)
			if code != 2 {
				t.Fatalf("exit=%d, want 2 (stderr=%q)", code, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Errorf("stderr=%q, want it to contain %q", stderr, tc.wantStderr)
			}
		})
	}
}

// assertNoStrayFiles fails if dir holds anything, so a leaked ".partial-*" temp
// file is caught rather than merely not asserted.
func assertNoStrayFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 0 {
		t.Errorf("expected no leftover files in %s, found %v", dir, names)
	}
}

func TestMeetingsParseArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"id first moves to the end", []string{"ID", "--out", "x"}, []string{"--out", "x", "ID"}},
		{"id already last is untouched", []string{"--out", "x", "ID"}, []string{"--out", "x", "ID"}},
		// The value of a flag must never be mistaken for the positional.
		{"flag value is not the positional", []string{"--out", "ID-LOOKING-VALUE"}, []string{"--out", "ID-LOOKING-VALUE"}},
		{"empty", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := meetingsParseArgs(tc.in)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("meetingsParseArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
