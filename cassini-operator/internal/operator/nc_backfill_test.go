package operator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backfillDest is a Nextcloud Files stand-in for the backfill's destination:
// it records every mutating request and can be pre-loaded with an archive so
// the guard has something to refuse.
type backfillDest struct {
	files map[string][]byte
	ops   []ncFilesOp
	// catalogStatus overrides the GET status for catalog.json, so the guard can
	// be driven through the answers that are neither "there" nor "not there".
	catalogStatus int
	// failPUT names a remote path whose PUT returns 500.
	failPUT string
}

func newBackfillDest() *backfillDest {
	return &backfillDest{files: map[string][]byte{}}
}

func (d *backfillDest) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Path
		if idx := strings.Index(rel, "/Cassini"); idx >= 0 {
			rel = rel[idx+1:]
		}
		body, _ := io.ReadAll(r.Body)
		switch r.Method {
		case "MKCOL":
			d.ops = append(d.ops, ncFilesOp{method: "MKCOL", path: rel})
			w.WriteHeader(http.StatusCreated)
		case "PROPPATCH":
			d.ops = append(d.ops, ncFilesOp{method: "PROPPATCH", path: rel, body: string(body)})
			w.WriteHeader(http.StatusMultiStatus)
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			var b bytes.Buffer
			b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
			b.WriteString(`<d:response><d:href>/remote.php/dav/files/` + ncRecordingsOwner + `/` + rel + `/</d:href></d:response>`)
			for name := range d.files {
				if strings.HasPrefix(name, rel+"/") && strings.HasSuffix(name, ".opus") {
					b.WriteString(`<d:response><d:href>/remote.php/dav/files/` + ncRecordingsOwner + `/` + name + `</d:href></d:response>`)
				}
			}
			b.WriteString(`</d:multistatus>`)
			_, _ = w.Write(b.Bytes())
		case http.MethodPut:
			if d.failPUT != "" && rel == d.failPUT {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			d.ops = append(d.ops, ncFilesOp{method: http.MethodPut, path: rel, body: string(body)})
			_, existed := d.files[rel]
			d.files[rel] = body
			if existed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		case http.MethodGet:
			if strings.HasSuffix(rel, "catalog.json") && d.catalogStatus != 0 {
				w.WriteHeader(d.catalogStatus)
				return
			}
			raw, ok := d.files[rel]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(raw)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (d *backfillDest) op(method, path string) (ncFilesOp, bool) {
	for _, op := range d.ops {
		if op.method == method && op.path == path {
			return op, true
		}
	}
	return ncFilesOp{}, false
}

// writeLegacySite lays down the shape a pre-Nextcloud-Files install left on its
// volume: catalog.json plus meetings/<id>.opus.
func writeLegacySite(t *testing.T, ids ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "meetings"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := make([]string, 0, len(ids))
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(root, "meetings", id+".opus"), []byte("OPUS:"+id), 0o644); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, fmt.Sprintf(`{"id":%q,"audioPath":"meetings/%s.opus"}`, id, id))
	}
	catalog := fmt.Sprintf(`{"version":%q,"meetings":[%s]}`, catalogSchemaVersion, strings.Join(entries, ","))
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func newBackfill(t *testing.T, ncURL string) (*backfillNCFiles, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	return &backfillNCFiles{
		cfg:    testExAppConfig(ncURL),
		client: &http.Client{},
		out:    out,
	}, out
}

func TestBackfillDeliversTheArchiveAndIndexesItLast(t *testing.T) {
	dest := newBackfillDest()
	b, out := newBackfill(t, dest.server(t).URL)
	site := writeLegacySite(t, "meeting-a", "meeting-b")

	if err := b.run(context.Background(), site, false); err != nil {
		t.Fatalf("run() error = %v\n%s", err, out)
	}

	for _, id := range []string{"meeting-a", "meeting-b"} {
		got, ok := dest.files["Cassini/Recordings/meetings/"+id+".opus"]
		if !ok || string(got) != "OPUS:"+id {
			t.Errorf("%s: uploaded=%v body=%q", id, ok, got)
		}
	}

	raw, ok := dest.files["Cassini/Recordings/catalog.json"]
	if !ok {
		t.Fatal("catalog.json never reached Nextcloud")
	}
	var catalog siteCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("remote catalog is not JSON: %v", err)
	}
	if catalog.Version != catalogSchemaVersion || len(catalog.Meetings) != 2 {
		t.Fatalf("remote catalog = %+v, want both meetings at %s", catalog, catalogSchemaVersion)
	}

	// The index is written after every object it names, so an interrupted run
	// can only ever leave unreferenced files behind.
	var lastPut string
	for _, op := range dest.ops {
		if op.method == http.MethodPut {
			lastPut = op.path
		}
	}
	if lastPut != "Cassini/Recordings/catalog.json" {
		t.Fatalf("last PUT = %q, want the catalog", lastPut)
	}
}

// Every artefact the backfill creates must override the container's read grant
// to the virtual all-users group, and the recordings must do so before the
// catalog advertises them.
func TestBackfillProtectsEverythingItWrites(t *testing.T) {
	dest := newBackfillDest()
	b, out := newBackfill(t, dest.server(t).URL)

	if err := b.run(context.Background(), writeLegacySite(t, "meeting-a"), false); err != nil {
		t.Fatalf("run() error = %v\n%s", err, out)
	}

	const (
		opusPath    = "Cassini/Recordings/meetings/meeting-a.opus"
		catalogPath = "Cassini/Recordings/catalog.json"
		denyAll     = "<nc:acl-permissions>0</nc:acl-permissions>"
		grantAll    = "<nc:acl-permissions>31</nc:acl-permissions>"
	)
	for _, path := range []string{opusPath, catalogPath} {
		acl, ok := dest.op("PROPPATCH", path)
		if !ok {
			t.Fatalf("%s was never given an ACL: it inherits the container's read grant", path)
		}
		for _, want := range []string{
			"<nc:acl-mapping-id>" + ncRecordingsEveryoneGroup + "</nc:acl-mapping-id>",
			denyAll,
			"<nc:acl-mapping-id>" + ncRecordingsOwner + "</nc:acl-mapping-id>",
			grantAll,
		} {
			if !strings.Contains(acl.body, want) {
				t.Errorf("%s ACL missing %q: %s", path, want, acl.body)
			}
		}
	}

	var opusDeny, catalogPut = -1, -1
	for i, op := range dest.ops {
		if op.method == "PROPPATCH" && op.path == opusPath && opusDeny < 0 {
			opusDeny = i
		}
		if op.method == http.MethodPut && op.path == catalogPath && catalogPut < 0 {
			catalogPut = i
		}
	}
	if opusDeny < 0 || catalogPut < 0 || opusDeny > catalogPut {
		t.Fatalf("recording must be protected before it is advertised: deny=%d catalog PUT=%d", opusDeny, catalogPut)
	}
}

// --public is the deliberate escape hatch for a legacy archive that was
// org-wide readable before access control existed.
func TestBackfillPublicGrantsTheAllUsersGroupRead(t *testing.T) {
	dest := newBackfillDest()
	b, out := newBackfill(t, dest.server(t).URL)
	b.public = true

	if err := b.run(context.Background(), writeLegacySite(t, "meeting-a"), false); err != nil {
		t.Fatalf("run() error = %v\n%s", err, out)
	}

	acl, ok := dest.op("PROPPATCH", "Cassini/Recordings/meetings/meeting-a.opus")
	if !ok {
		t.Fatal("recording was never given an ACL")
	}
	if !strings.Contains(acl.body, "<nc:acl-permissions>1</nc:acl-permissions>") {
		t.Errorf("--public did not grant read: %s", acl.body)
	}

	// The catalog is never widened: it is the unfiltered index, and the read
	// proxy serves each caller a filtered view of it.
	catalogACL, ok := dest.op("PROPPATCH", "Cassini/Recordings/catalog.json")
	if !ok {
		t.Fatal("catalog was never given an ACL")
	}
	if !strings.Contains(catalogACL.body, "<nc:acl-permissions>0</nc:acl-permissions>") {
		t.Errorf("--public widened the authoritative catalog: %s", catalogACL.body)
	}
}

// The guard is the reason this command is safe to hand to an admin, so it is
// tested through every answer Nextcloud can give — not just present/absent.
func TestBackfillRefusesUnlessTheDestinationIsProvablyEmpty(t *testing.T) {
	populated := func(d *backfillDest) {
		d.files["Cassini/Recordings/catalog.json"] =
			[]byte(`{"version":"` + catalogSchemaVersion + `","meetings":[{"id":"live"}]}`)
	}
	tests := map[string]struct {
		setup      func(*backfillDest)
		wantRefuse bool
		wantIn     string
	}{
		"catalog lists a meeting": {
			setup: populated, wantRefuse: true, wantIn: "already lists 1 meeting",
		},
		"catalog is unreadable": {
			setup: func(d *backfillDest) {
				d.files["Cassini/Recordings/catalog.json"] = []byte("not json at all")
			},
			wantRefuse: true, wantIn: "not readable as a catalog",
		},
		"catalog is empty but recordings exist": {
			// The catalog is written last, so an interrupted delivery leaves
			// exactly this: objects with no index naming them.
			setup: func(d *backfillDest) {
				d.files["Cassini/Recordings/catalog.json"] = []byte(`{"meetings":[]}`)
				d.files["Cassini/Recordings/meetings/orphan.opus"] = []byte("OPUS")
			},
			wantRefuse: true, wantIn: "already holds 1 recording",
		},
		"catalog read is denied": {
			// davGetBytes returns a nil error for 403, so branching on err
			// instead of status would read this as "absent" and overwrite.
			setup:      func(d *backfillDest) { d.catalogStatus = http.StatusForbidden },
			wantRefuse: true, wantIn: "HTTP 403",
		},
		"catalog read fails server-side": {
			setup:      func(d *backfillDest) { d.catalogStatus = http.StatusInternalServerError },
			wantRefuse: true, wantIn: "HTTP 500",
		},
		"nothing there at all": {
			setup: func(*backfillDest) {}, wantRefuse: false,
		},
		"empty catalog and no recordings": {
			setup: func(d *backfillDest) {
				d.files["Cassini/Recordings/catalog.json"] = []byte(`{"meetings":[]}`)
			},
			wantRefuse: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dest := newBackfillDest()
			tc.setup(dest)
			b, _ := newBackfill(t, dest.server(t).URL)

			err := b.guardDestinationIsEmpty(context.Background())
			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("guard refused an empty destination: %v", err)
				}
				return
			}
			if !errors.Is(err, errBackfillRefused) {
				t.Fatalf("guard error = %v, want a refusal", err)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("refusal %q does not explain itself with %q", err, tc.wantIn)
			}
		})
	}
}

func TestBackfillRefusalChangesNothing(t *testing.T) {
	dest := newBackfillDest()
	dest.files["Cassini/Recordings/catalog.json"] =
		[]byte(`{"version":"` + catalogSchemaVersion + `","meetings":[{"id":"live"}]}`)
	b, _ := newBackfill(t, dest.server(t).URL)

	err := b.run(context.Background(), writeLegacySite(t, "meeting-a"), false)
	if !errors.Is(err, errBackfillRefused) {
		t.Fatalf("run() error = %v, want a refusal", err)
	}
	for _, op := range dest.ops {
		if op.method != http.MethodGet {
			t.Errorf("a refused backfill wrote to Nextcloud: %s %s", op.method, op.path)
		}
	}
}

func TestBackfillDryRunReportsWithoutWriting(t *testing.T) {
	dest := newBackfillDest()
	b, out := newBackfill(t, dest.server(t).URL)

	if err := b.run(context.Background(), writeLegacySite(t, "meeting-a"), true); err != nil {
		t.Fatalf("dry run error = %v", err)
	}
	if len(dest.ops) != 0 {
		t.Errorf("dry run mutated Nextcloud: %+v", dest.ops)
	}
	if !strings.Contains(out.String(), "meetings/meeting-a.opus") {
		t.Errorf("dry run did not report what it would upload: %s", out)
	}
}

// A failed upload must not be followed by a catalog that names it.
func TestBackfillDoesNotIndexAfterAFailedUpload(t *testing.T) {
	dest := newBackfillDest()
	dest.failPUT = "Cassini/Recordings/meetings/meeting-a.opus"
	b, _ := newBackfill(t, dest.server(t).URL)

	if err := b.run(context.Background(), writeLegacySite(t, "meeting-a"), false); err == nil {
		t.Fatal("run() succeeded despite a failed upload")
	}
	if _, ok := dest.files["Cassini/Recordings/catalog.json"]; ok {
		t.Fatal("catalog was written after an upload failed")
	}
}

// An absent or empty local archive means "nothing to migrate", not "the
// migration failed". It is the ordinary state of every installation created
// after recordings moved to Nextcloud Files, so an admin who runs this to check
// must not be told their migration broke.
func TestBackfillTreatsAnAbsentLocalArchiveAsNothingToDo(t *testing.T) {
	t.Run("no catalog", func(t *testing.T) {
		_, err := loadBackfillSource(t.TempDir())
		if !errors.Is(err, errBackfillRefused) {
			t.Fatalf("error = %v, want a 'nothing to migrate' refusal", err)
		}
		if !strings.Contains(err.Error(), "no catalog.json") {
			t.Errorf("refusal does not say why: %v", err)
		}
	})

	t.Run("catalog with no meetings", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(`{"meetings":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := func() error { _, err := loadBackfillSource(root); return err }(); !errors.Is(err, errBackfillRefused) {
			t.Fatalf("error = %v, want a 'nothing to migrate' refusal", err)
		}
	})
}

func TestBackfillSourceRejectsAnArchiveItCannotDeliver(t *testing.T) {
	t.Run("asset missing from disk", func(t *testing.T) {
		root := writeLegacySite(t, "meeting-a")
		if err := os.Remove(filepath.Join(root, "meetings", "meeting-a.opus")); err != nil {
			t.Fatal(err)
		}
		// Caught before anything is uploaded, so a half-delivered archive is
		// never the outcome of a catalog that lies.
		if _, err := loadBackfillSource(root); err == nil || !strings.Contains(err.Error(), "not on disk") {
			t.Fatalf("error = %v, want the missing asset named", err)
		}
	})

	t.Run("legacy directory export", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "meetings", "old"), 0o755); err != nil {
			t.Fatal(err)
		}
		catalog := `{"meetings":[{"id":"old","artifactPath":"meetings/old"}]}`
		if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(catalog), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadBackfillSource(root); err == nil || !strings.Contains(err.Error(), "predates portable meetings") {
			t.Fatalf("error = %v, want a directory export to be refused explicitly", err)
		}
	})

	t.Run("remote recordings base url", func(t *testing.T) {
		root := t.TempDir()
		catalog := `{"meetings":[{"id":"remote","audioPath":"https://elsewhere.example.com/x.opus"}]}`
		if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(catalog), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadBackfillSource(root); err == nil || !strings.Contains(err.Error(), "names no local asset") {
			t.Fatalf("error = %v, want a remote asset to be refused", err)
		}
	})
}

// The backfill writes as the recordings owner over the AppAPI act-as-user
// scheme, which is the only credential available inside the container. DAV
// requests carry no OCS content negotiation.
func TestBackfillAuthenticatesAsTheRecordingsOwner(t *testing.T) {
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(r.Context()))
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"/>`)
		case "PROPPATCH":
			w.WriteHeader(http.StatusMultiStatus)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	b, _ := newBackfill(t, srv.URL)
	if err := b.run(context.Background(), writeLegacySite(t, "meeting-a"), false); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	wantAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":sekret"))
	if len(seen) == 0 {
		t.Fatal("no requests reached Nextcloud")
	}
	for _, r := range seen {
		if got := r.Header.Get("AUTHORIZATION-APP-API"); got != wantAuth {
			t.Errorf("%s %s: auth = %q, want the recordings owner", r.Method, r.URL.Path, got)
		}
		if got := r.Header.Get("EX-APP-ID"); got != "gocassini" {
			t.Errorf("%s %s: EX-APP-ID = %q", r.Method, r.URL.Path, got)
		}
		if got := r.Header.Get("AA-VERSION"); got != "34.0.0" {
			t.Errorf("%s %s: AA-VERSION = %q", r.Method, r.URL.Path, got)
		}
		if got := r.Header.Get("OCS-APIRequest"); got != "" {
			t.Errorf("%s %s: DAV request carried OCS-APIRequest = %q", r.Method, r.URL.Path, got)
		}
	}
}

func TestRunRejectsAnUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"definitely-not-a-command"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), backfillNCFilesCommand) {
		t.Errorf("unknown command did not name the known ones: %s", stderr.String())
	}
}

func TestBackfillCommandRefusesOutsideAnExApp(t *testing.T) {
	for _, name := range []string{"NEXTCLOUD_URL", "APP_ID", "APP_SECRET"} {
		t.Setenv(name, "")
	}
	var stdout, stderr bytes.Buffer
	code := runBackfillNCFiles(context.Background(), []string{"--dry-run"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 outside an ExApp", code)
	}
	if !strings.Contains(stderr.String(), "inside the Cassini app container") {
		t.Errorf("error does not say where to run it: %s", stderr.String())
	}
}

// A malformed ExApp environment is rejected before the guard runs, so nothing
// has been written and the runner must say so. This path used to return the
// generic failure code, which the runner reads as "may be half-written" and
// answers with "remove the recordings this run uploaded" — pointing an admin at
// a live archive this run never touched.
func TestBackfillConfigFailureReportsNothingWasWritten(t *testing.T) {
	t.Setenv("CASSINI_APPAPI_REQUIRED", "true")
	t.Setenv("NEXTCLOUD_URL", "http://nextcloud.invalid")
	t.Setenv("APP_ID", "gocassini")
	t.Setenv("APP_SECRET", "")

	var stdout, stderr bytes.Buffer
	code := runBackfillNCFiles(context.Background(), []string{"--dry-run"}, &stdout, &stderr)
	if code != backfillExitNotStarted {
		t.Fatalf("exit = %d, want %d (nothing was written)\nstderr: %s", code, backfillExitNotStarted, &stderr)
	}
}

// The exit code says whether anything was WRITTEN, because the runner turns it
// into opposite instructions: "re-run, nothing to clean up" versus "do not
// re-run, remove what this uploaded". Giving the second answer for a run that
// wrote nothing points an admin at their live archive.
func TestBackfillExitCodesDistinguishWrittenFromNotWritten(t *testing.T) {
	legacy := writeLegacySite(t, "meeting-a")
	empty := t.TempDir()

	tests := map[string]struct {
		siteRoot string
		nc       func(*backfillDest)
		// unreachable points the command at a dead server.
		unreachable bool
		want        int
	}{
		"migrated": {
			siteRoot: legacy, nc: func(*backfillDest) {}, want: 0,
		},
		"no legacy archive on disk": {
			// The ordinary state of a current install. Nothing was written, and
			// nothing is wrong — this is the case that used to report a partial
			// migration and send admins to delete live recordings.
			siteRoot: empty, nc: func(*backfillDest) {}, want: backfillExitNothingToDo,
		},
		"destination already populated": {
			siteRoot: legacy,
			nc: func(d *backfillDest) {
				d.files["Cassini/Recordings/catalog.json"] =
					[]byte(`{"version":"` + catalogSchemaVersion + `","meetings":[{"id":"live"}]}`)
			},
			want: backfillExitNothingToDo,
		},
		"guard could not reach Nextcloud": {
			// A failure, but strictly before any write: retry is safe.
			siteRoot: legacy, nc: func(*backfillDest) {}, unreachable: true,
			want: backfillExitNotStarted,
		},
		"upload failed part-way": {
			siteRoot: legacy,
			nc: func(d *backfillDest) {
				d.failPUT = "Cassini/Recordings/meetings/meeting-a.opus"
			},
			want: backfillExitPartial,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			dest := newBackfillDest()
			tc.nc(dest)
			srv := dest.server(t)
			url := srv.URL
			if tc.unreachable {
				srv.Close()
			}

			t.Setenv("NEXTCLOUD_URL", url)
			t.Setenv("APP_ID", "gocassini")
			t.Setenv("APP_SECRET", "sekret")
			t.Setenv("APP_VERSION", "1.2.3")

			var stdout, stderr bytes.Buffer
			got := runBackfillNCFiles(context.Background(),
				[]string{"--site-root", tc.siteRoot}, &stdout, &stderr)
			if got != tc.want {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", got, tc.want, &stdout, &stderr)
			}
		})
	}
}
