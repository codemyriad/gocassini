package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runDevMeetingsPullCLI drives the real entry point against a fake Nextcloud,
// the way runMeetingsCLI does for the read commands.
func runDevMeetingsPullCLI(t *testing.T, baseURL string, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"dev", "meetings", "pull"}, args...)
	full = append(full,
		"--nextcloud-url", baseURL,
		"--user", "alice",
		"--app-password", "app-pw-1234",
	)
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// servePackArchive answers the catalog route plus one asset route per body,
// keyed by the meeting id, and counts the GETs so a test can prove a second run
// downloaded nothing. HEAD is answered by net/http from the same handler, which
// is what the real proxy does too.
func servePackArchive(catalog string, bodies map[string][]byte, gets map[string]int) func(http.ResponseWriter, *http.Request) {
	const assetPrefix = "/index.php/apps/app_api/proxy/gocassini/published/meetings/"
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == meetingsTestCatalogPath {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
			fmt.Fprint(w, catalog)
			return
		}
		if !strings.HasPrefix(r.URL.Path, assetPrefix) {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, assetPrefix), ".opus")
		body, ok := bodies[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodGet && gets != nil {
			gets[id]++
		}
		w.Header().Set("Content-Type", "audio/ogg")
		w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
		// http.ServeContent-free on purpose: setting the length explicitly is
		// what the real proxy relays, and it is what the pull checks against.
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}
}

func readPackCatalog(t *testing.T, dir string) struct {
	Version  string            `json:"version"`
	Meetings []json.RawMessage `json:"meetings"`
} {
	t.Helper()
	var doc struct {
		Version  string            `json:"version"`
		Meetings []json.RawMessage `json:"meetings"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, seedPackCatalogName))
	if err != nil {
		t.Fatalf("read pack catalog: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse pack catalog: %v", err)
	}
	return doc
}

func readPackManifest(t *testing.T, dir string) seedPackManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, seedPackManifestName))
	if err != nil {
		t.Fatalf("read pack manifest: %v", err)
	}
	var manifest seedPackManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse pack manifest: %v", err)
	}
	return manifest
}

// A pack must be a site root: the catalog the server sent, entry for entry,
// beside the files those entries name. Anything less and the operator's
// backfill and the harness seeder both reject it.
func TestDevMeetingsPullWritesASiteRootShapedPack(t *testing.T) {
	older := []byte("OLDER-opus-bytes")
	newer := []byte("NEWER-opus-bytes-and-then-some")
	fake := newMeetingsFakeNextcloud(t, servePackArchive(twoMeetingCatalog, map[string][]byte{
		"OLDER": older,
		"NEWER": newer,
	}, nil))
	out := filepath.Join(t.TempDir(), "pack")

	code, stdout, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	for id, want := range map[string][]byte{"OLDER": older, "NEWER": newer} {
		got, err := os.ReadFile(filepath.Join(out, "meetings", id+".opus"))
		if err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: pack holds %q, server sent %q", id, got, want)
		}
	}

	catalog := readPackCatalog(t, out)
	if catalog.Version != meetingsCatalogVersion {
		t.Errorf("pack catalog version = %q, want %q", catalog.Version, meetingsCatalogVersion)
	}
	if len(catalog.Meetings) != 2 {
		t.Fatalf("pack catalog lists %d meetings, want 2", len(catalog.Meetings))
	}
	if !strings.Contains(stdout, "done: 2 meeting(s)") {
		t.Errorf("expected a done line, got %q", stdout)
	}
}

// The entries must survive the round trip byte for byte. A pack that
// re-normalised them would be a second dialect of a format that already has
// one, and would silently drop any field this build does not know about.
func TestDevMeetingsPullReEmitsCatalogEntriesVerbatim(t *testing.T) {
	const catalogWithUnknownField = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "MEETING1", "title": "Daily Standup", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/MEETING1.opus", "somethingThisBuildIgnores": {"a": [1, 2]}}
  ]
}`
	fake := newMeetingsFakeNextcloud(t, servePackArchive(catalogWithUnknownField, map[string][]byte{
		"MEETING1": []byte("body"),
	}, nil))
	out := filepath.Join(t.TempDir(), "pack")

	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	catalog := readPackCatalog(t, out)
	if len(catalog.Meetings) != 1 {
		t.Fatalf("pack catalog lists %d meetings, want 1", len(catalog.Meetings))
	}
	var entry map[string]any
	if err := json.Unmarshal(catalog.Meetings[0], &entry); err != nil {
		t.Fatalf("parse entry: %v", err)
	}
	if _, ok := entry["somethingThisBuildIgnores"]; !ok {
		t.Errorf("the unknown field was dropped; entry = %v", entry)
	}
}

// --limit takes the newest, because the catalog is newest-first and a caller
// asking for two meetings to develop against means the two most recent.
func TestDevMeetingsPullLimitKeepsTheNewest(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, servePackArchive(twoMeetingCatalog, map[string][]byte{
		"OLDER": []byte("older"),
		"NEWER": []byte("newer"),
	}, nil))
	out := filepath.Join(t.TempDir(), "pack")

	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out, "--limit", "1"); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(out, "meetings", "NEWER.opus")); err != nil {
		t.Errorf("the newest meeting was not pulled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "meetings", "OLDER.opus")); !os.IsNotExist(err) {
		t.Errorf("the older meeting should not have been pulled, stat err = %v", err)
	}
	if manifest := readPackManifest(t, out); manifest.Totals.Meetings != 1 {
		t.Errorf("manifest counts %d meetings, want 1", manifest.Totals.Meetings)
	}
}

// Re-running must not re-download. This is what makes an interrupted 2 GB pull
// resumable by simply repeating the command.
func TestDevMeetingsPullSkipsMeetingsAlreadyAtTheServerSize(t *testing.T) {
	gets := map[string]int{}
	fake := newMeetingsFakeNextcloud(t, servePackArchive(twoMeetingCatalog, map[string][]byte{
		"OLDER": []byte("older-body"),
		"NEWER": []byte("newer-body"),
	}, gets))
	out := filepath.Join(t.TempDir(), "pack")

	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out); code != 0 {
		t.Fatalf("first pull exit=%d stderr=%q", code, stderr)
	}
	if gets["NEWER"] != 1 || gets["OLDER"] != 1 {
		t.Fatalf("first pull GETs = %v, want one each", gets)
	}

	code, stdout, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
	if code != 0 {
		t.Fatalf("second pull exit=%d stderr=%q", code, stderr)
	}
	if gets["NEWER"] != 1 || gets["OLDER"] != 1 {
		t.Errorf("second pull re-downloaded: GETs = %v", gets)
	}
	if !strings.Contains(stdout, "2 cached") {
		t.Errorf("expected the second run to report 2 cached, got %q", stdout)
	}
}

// --force is the way back to a fresh copy when the local file is the right
// size but the wrong bytes.
func TestDevMeetingsPullForceRedownloads(t *testing.T) {
	gets := map[string]int{}
	fake := newMeetingsFakeNextcloud(t, servePackArchive(oneMeetingCatalog, map[string][]byte{
		"MEETING1": []byte("published"),
	}, gets))
	out := filepath.Join(t.TempDir(), "pack")

	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out); code != 0 {
		t.Fatalf("first pull exit=%d stderr=%q", code, stderr)
	}
	// Same length, different content: only --force can tell the difference.
	if err := os.WriteFile(filepath.Join(out, "meetings", "MEETING1.opus"), []byte("corrupted"), 0o600); err != nil {
		t.Fatalf("corrupt the local copy: %v", err)
	}

	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out, "--force"); code != 0 {
		t.Fatalf("forced pull exit=%d stderr=%q", code, stderr)
	}
	if gets["MEETING1"] != 2 {
		t.Errorf("--force did not re-download: GETs = %d", gets["MEETING1"])
	}
	got, err := os.ReadFile(filepath.Join(out, "meetings", "MEETING1.opus"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "published" {
		t.Errorf("after --force the file is %q, want the published bytes", got)
	}
}

// A body cut short must not survive as a plausible-looking .opus. This is the
// failure D-714 describes in the sibling puller. Two layers catch it, and both
// are exercised because only the second one is ours.
func TestDevMeetingsPullRejectsATruncatedTransfer(t *testing.T) {
	const assetPath = "/index.php/apps/app_api/proxy/gocassini/published/meetings/MEETING1.opus"

	// Declared length on the GET: net/http itself refuses the short body.
	t.Run("declared length", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case meetingsTestCatalogPath:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
				fmt.Fprint(w, oneMeetingCatalog)
			case assetPath:
				w.Header().Set("Content-Length", "100")
				if r.Method == http.MethodHead {
					return
				}
				_, _ = w.Write([]byte("short"))
			default:
				http.NotFound(w, r)
			}
		})
		out := filepath.Join(t.TempDir(), "pack")

		code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
		if code == 0 {
			t.Fatalf("a truncated transfer exited 0; stderr=%q", stderr)
		}
		if _, err := os.Stat(filepath.Join(out, "meetings", "MEETING1.opus")); !os.IsNotExist(err) {
			t.Errorf("the truncated file was left behind, stat err = %v", err)
		}
	})

	// Chunked GET, so the transport has no length to check against and the
	// short body arrives as a clean success. Only the size the HEAD promised
	// separates it from a genuinely small meeting.
	t.Run("chunked, length known only from HEAD", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case meetingsTestCatalogPath:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cassini-Meeting-Source", "nextcloud-files")
				fmt.Fprint(w, oneMeetingCatalog)
			case assetPath:
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", "100")
					return
				}
				// No Content-Length: Go chunks it, and a short write looks clean.
				_, _ = w.Write([]byte("short"))
			default:
				http.NotFound(w, r)
			}
		})
		out := filepath.Join(t.TempDir(), "pack")

		code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
		if code == 0 {
			t.Fatalf("a truncated transfer exited 0; stderr=%q", stderr)
		}
		if !strings.Contains(stderr, "cut short") {
			t.Errorf("expected the truncation to be named, got %q", stderr)
		}
		if _, err := os.Stat(filepath.Join(out, "meetings", "MEETING1.opus")); !os.IsNotExist(err) {
			t.Errorf("the truncated file was left behind, stat err = %v", err)
		}
	})
}

// One failure must not cost the whole pack, and the catalog must then name only
// what actually landed — otherwise the pack is internally inconsistent and no
// seeder will take it.
func TestDevMeetingsPullKeepsWhatLandedWhenOneMeetingFails(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, servePackArchive(twoMeetingCatalog, map[string][]byte{
		// OLDER is absent from the server, so it 404s.
		"NEWER": []byte("newer-body"),
	}, nil))
	out := filepath.Join(t.TempDir(), "pack")

	code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
	if code != 1 {
		t.Fatalf("exit=%d, want 1 for a partial pull; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "OLDER") {
		t.Errorf("expected the failing meeting to be named, got %q", stderr)
	}

	catalog := readPackCatalog(t, out)
	if len(catalog.Meetings) != 1 {
		t.Fatalf("pack catalog lists %d meetings, want only the one that landed", len(catalog.Meetings))
	}
	if !strings.Contains(string(catalog.Meetings[0]), "NEWER") {
		t.Errorf("pack catalog kept the wrong entry: %s", catalog.Meetings[0])
	}
	if manifest := readPackManifest(t, out); manifest.Totals.Meetings != 1 {
		t.Errorf("manifest counts %d meetings, want 1", manifest.Totals.Meetings)
	}
}

// The catalog is network data, and its audioPath decides which local file is
// written. An entry climbing out of --out must be refused, not cleaned into
// something plausible.
func TestDevMeetingsPullRefusesAnAssetPathOutsideThePack(t *testing.T) {
	for name, audioPath := range map[string]string{
		"traversal": "../../escape.opus",
		"absolute":  "/etc/passwd",
		"url":       "https://elsewhere.example.com/x.opus",
	} {
		t.Run(name, func(t *testing.T) {
			catalog := fmt.Sprintf(`{"version":"cassini.viewer.catalog.v1","meetings":[
              {"id":"EVIL","title":"t","dateLabel":"2026-08-11 10:32","audioPath":%q}]}`, audioPath)
			fake := newMeetingsFakeNextcloud(t, servePackArchive(catalog, nil, nil))
			out := filepath.Join(t.TempDir(), "pack")

			code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
			if code == 0 {
				t.Fatalf("accepted audioPath %q; stderr=%q", audioPath, stderr)
			}
			if _, err := os.Stat(filepath.Join(out, seedPackCatalogName)); err == nil {
				t.Errorf("a pack was written for a refused archive")
			}
		})
	}
}

// Two entries naming one file would make the second silently overwrite the
// first. Nothing promises catalog ids are unique, so it is refused.
func TestDevMeetingsPullRefusesTwoEntriesNamingOneFile(t *testing.T) {
	const duplicate = `{"version":"cassini.viewer.catalog.v1","meetings":[
      {"id":"A","title":"a","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/SAME.opus"},
      {"id":"B","title":"b","dateLabel":"2026-08-10 10:32","audioPath":"./meetings/SAME.opus"}]}`
	fake := newMeetingsFakeNextcloud(t, servePackArchive(duplicate, map[string][]byte{"SAME": []byte("x")}, nil))
	out := filepath.Join(t.TempDir(), "pack")

	code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
	if code == 0 {
		t.Fatalf("accepted a duplicate asset path; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "one meeting over another") {
		t.Errorf("expected the overwrite to be named, got %q", stderr)
	}
}

// --dry-run answers "how big is this" before committing to the transfer, and
// must write nothing at all while doing it.
func TestDevMeetingsPullDryRunReportsSizeAndWritesNothing(t *testing.T) {
	gets := map[string]int{}
	fake := newMeetingsFakeNextcloud(t, servePackArchive(twoMeetingCatalog, map[string][]byte{
		"OLDER": bytes.Repeat([]byte("o"), 1024),
		"NEWER": bytes.Repeat([]byte("n"), 2048),
	}, gets))
	out := filepath.Join(t.TempDir(), "pack")

	code, stdout, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out, "--dry-run")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "dry run: 2 meeting(s), 3.0 KiB") {
		t.Errorf("expected the total, got %q", stdout)
	}
	if len(gets) != 0 {
		t.Errorf("a dry run downloaded something: %v", gets)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a dry run created %s, stat err = %v", out, err)
	}
}

// The manifest is the pack's provenance, and it must never carry the
// credential that fetched it.
func TestDevMeetingsPullManifestRecordsProvenanceWithoutTheCredential(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, servePackArchive(oneMeetingCatalog, map[string][]byte{
		"MEETING1": []byte("body-bytes"),
	}, nil))
	out := filepath.Join(t.TempDir(), "pack")

	if code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	manifest := readPackManifest(t, out)
	if manifest.Version != seedPackManifestVersion {
		t.Errorf("manifest version = %q, want %q", manifest.Version, seedPackManifestVersion)
	}
	if manifest.Source.Account != "alice" {
		t.Errorf("manifest account = %q, want alice", manifest.Source.Account)
	}
	if manifest.Source.Origin != "nextcloud-files" {
		t.Errorf("manifest origin = %q, want nextcloud-files", manifest.Source.Origin)
	}
	if manifest.Totals.Bytes != int64(len("body-bytes")) {
		t.Errorf("manifest bytes = %d, want %d", manifest.Totals.Bytes, len("body-bytes"))
	}
	raw, err := os.ReadFile(filepath.Join(out, seedPackManifestName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(raw), "app-pw-1234") {
		t.Error("the manifest carries the app password")
	}
}

// An empty selection is not a pack. Which of the three causes it was decides
// what the caller should do next, so they are not reported alike.
func TestDevMeetingsPullExplainsAnEmptySelection(t *testing.T) {
	t.Run("filter removed everything", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, serveCatalog(twoMeetingCatalog))
		out := filepath.Join(t.TempDir(), "pack")
		code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out, "--room", "rm_nothing")
		if code == 0 {
			t.Fatalf("exit 0 with nothing pulled; stderr=%q", stderr)
		}
		if !strings.Contains(stderr, "your filter excluded all 2") {
			t.Errorf("expected the filter explanation, got %q", stderr)
		}
	})
	t.Run("account reads nothing", func(t *testing.T) {
		fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[]}`))
		out := filepath.Join(t.TempDir(), "pack")
		code, _, stderr := runDevMeetingsPullCLI(t, fake.server.URL, "--out", out)
		if code == 0 {
			t.Fatalf("exit 0 with nothing pulled; stderr=%q", stderr)
		}
		if !strings.Contains(stderr, "never provisioned") {
			t.Errorf("expected the provisioning ambiguity to be named, got %q", stderr)
		}
	})
}

func TestDevMeetingsPullRequiresOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"dev", "meetings", "pull"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2 for a usage error; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--out is required") {
		t.Errorf("expected --out to be named, got %q", stderr.String())
	}
}

func TestPackRelativeAsset(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"./meetings/x.opus", "meetings/x.opus", true},
		{"meetings/x.opus", "meetings/x.opus", true},
		{"./meetings/../meetings/x.opus", "meetings/x.opus", true},
		{"", "", false},
		{"   ", "", false},
		{"/meetings/x.opus", "", false},
		{"../x.opus", "", false},
		{"./../x.opus", "", false},
		{"https://example.com/x.opus", "", false},
		{".", "", false},
	} {
		got, err := packRelativeAsset(tc.in)
		if tc.ok && err != nil {
			t.Errorf("packRelativeAsset(%q) errored: %v", tc.in, err)
			continue
		}
		if !tc.ok && err == nil {
			t.Errorf("packRelativeAsset(%q) = %q, want an error", tc.in, got)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("packRelativeAsset(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{
		-1:      "size unknown",
		0:       "0 B",
		999:     "999 B",
		1024:    "1.0 KiB",
		1536:    "1.5 KiB",
		1 << 20: "1.0 MiB",
		1 << 30: "1.0 GiB",
	} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
