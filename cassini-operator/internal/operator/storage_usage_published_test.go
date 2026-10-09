package operator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageMetadataDate(t *testing.T) {
	for _, tc := range []struct{ recorded, created, want string }{
		{"2026-03-05T23:38:29", "2026-09-29T12:00:00Z", "2026-03-05"},
		{"2026-03-05T23:38:29", "", "2026-03-05"},
		{"", "2026-03-05T23:38:29-02:00", "2026-03-06"},
		{"invalid", "2026-03-05T12:00:00Z", "2026-03-05"},
		{"2026-02-30T12:00:00", "", ""},
		{"", "", ""},
	} {
		if got := storageMetadataDate(tc.recorded, tc.created); got != tc.want {
			t.Errorf("%+v: got %q", tc, got)
		}
	}
}

func TestPublishedStoragePublicationAndMetadataDates(t *testing.T) {
	rt, close := newBareSealRuntime(t)
	defer close()
	seedRetentionJob(t, rt, "known")
	if _, err := rt.store.db.Exec(`UPDATE jobs SET artifact_site_path=? WHERE id='known'`, ncRecordingsRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',artifact_opus_path='known.json',publish_finished_at='2026-01-01T12:00:00Z' WHERE job_id='known'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.store.db.Exec(`INSERT INTO job_attempts(job_id,attempt_number,trigger_kind,request_json,stage,state,created_at,updated_at,artifact_opus_path,publish_finished_at) VALUES
 ('known',2,'manual','{}','done','succeeded','2026-02-02','2026-02-02','known.json','2026-02-02T23:30:00-02:00'),
 ('known',3,'manual','{}','done','failed','2026-03-02','2026-03-02','known.json','2026-03-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), "metadata.sqlite3"), rt.logger)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	rt.meetingMetadata = metadata
	for id, raw := range map[int64]string{
		1: `{"meetingPath":"./meetings/known.json","recordedAtLocal":"2000-01-01T00:00:00"}`,
		2: `{"audioPath":"./meetings/orphan.opus","recordedAtLocal":"2025-12-31T23:59:59"}`,
		3: `{"meetingPath":"./meetings/created.json","createdAtUtc":"2026-01-01T23:00:00-02:00"}`,
	} {
		name := map[int64]string{1: "known.json", 2: "orphan.opus", 3: "created.json"}[id]
		if err := metadata.Put(t.Context(), id, name, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	entries := []davSizeEntry{
		{relPath: ncRecordingsRoot + "/meetings/known.json", size: 10, fileID: 1},
		{relPath: ncRecordingsRoot + "/meetings/orphan.opus", size: 20, fileID: 2},
		{relPath: "Cassini/Recordings/meetings/created.json", size: 30, fileID: 3},
		{relPath: "Cassini/Recordings/meetings/known.json", size: 40, fileID: 4},
		{relPath: ncRecordingsRoot + "/catalog.json", size: 50},
		{relPath: ncRecordingsRoot + "/meetings/known.opus", size: 60},
	}
	category, problem := rt.publishedStorageCategory(t.Context(), ExAppConfig{}, entries)
	if problem != "" {
		t.Fatal(problem)
	}
	if category.Bytes != 210 || category.Files != 6 || category.UndatedBytes != 150 || category.UndatedFiles != 3 {
		t.Fatalf("%+v", category)
	}
	want := []storageUsageDay{{Date: "2025-12-31", Bytes: 20, Files: 1}, {Date: "2026-01-02", Bytes: 30, Files: 1}, {Date: "2026-02-03", Bytes: 10, Files: 1}}
	got, _ := json.Marshal(category.Days)
	expected, _ := json.Marshal(want)
	if string(got) != string(expected) {
		t.Fatalf("days %s, want %s", got, expected)
	}
}

func TestPublishedStorageInspectsOrphansAndCachesByIdentity(t *testing.T) {
	for _, extension := range []string{".opus", ".json"} {
		t.Run(extension, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			rt.cfg.CassiniBin = writeFakeCassini(t, "printf '%s' '{\"recordedAtLocal\":\"2026-03-05T23:59:59\"}'\n")
			gets := 0
			etag := `"original"`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("unexpected %s", r.Method)
				}
				if r.Header.Get("If-Match") != etag {
					t.Error("unconditional read")
				}
				gets++
				fmt.Fprint(w, "audio")
			}))
			defer server.Close()
			cfg := testExAppConfig(server.URL)
			entry := davSizeEntry{relPath: ncRecordingsRoot + "/meetings/orphan" + extension, size: 5, fileID: 42, etag: etag}
			for i := 0; i < 2; i++ {
				category, problem := rt.publishedStorageCategory(t.Context(), cfg, []davSizeEntry{entry})
				if problem != "" || len(category.Days) != 1 || category.Days[0].Date != "2026-03-05" {
					t.Fatal(category, problem)
				}
			}
			if gets != 1 {
				t.Fatalf("downloaded %d times", gets)
			}
			etag = `"changed"`
			entry.etag = etag
			if _, problem := rt.publishedStorageCategory(t.Context(), cfg, []davSizeEntry{entry}); problem != "" {
				t.Fatal(problem)
			}
			if gets != 2 {
				t.Fatal("changed file used cached dates")
			}
			entry.fileID = 43
			if _, problem := rt.publishedStorageCategory(t.Context(), cfg, []davSizeEntry{entry}); problem != "" {
				t.Fatal(problem)
			}
			if gets != 3 {
				t.Fatal("replaced file used cached dates")
			}
		})
	}
}

func TestPublishedStorageMissingDatesAndReadFailures(t *testing.T) {
	for _, scenario := range []string{"missing", "changed", "inspect failure"} {
		t.Run(scenario, func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			script := "printf '%s' '{}'\n"
			if scenario == "inspect failure" {
				script = "exit 1\n"
			}
			rt.cfg.CassiniBin = writeFakeCassini(t, script)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "changed" {
					w.WriteHeader(412)
					return
				}
				fmt.Fprint(w, "audio")
			}))
			defer server.Close()
			category, problem := rt.publishedStorageCategory(t.Context(), testExAppConfig(server.URL), []davSizeEntry{{relPath: ncRecordingsRoot + "/meetings/orphan.opus", size: 5, fileID: 42, etag: `"original"`}})
			if category.Bytes != 5 || category.UndatedBytes != 5 || category.UndatedFiles != 1 || len(category.Days) != 0 {
				t.Fatal(category)
			}
			if (problem != "") != (scenario != "missing") {
				t.Fatal(problem)
			}
		})
	}
}

func TestPublishedStorageRefreshReconcilesSuccessfulRoots(t *testing.T) {
	for _, failLegacy := range []bool{false, true} {
		t.Run(fmt.Sprint(failLegacy), func(t *testing.T) {
			rt, close := newBareSealRuntime(t)
			defer close()
			seedRetentionJob(t, rt, "known")
			if _, err := rt.store.db.Exec(`UPDATE jobs SET artifact_site_path=? WHERE id='known'`, ncRecordingsRoot); err != nil {
				t.Fatal(err)
			}
			if _, err := rt.store.db.Exec(`UPDATE job_attempts SET state='succeeded',artifact_opus_path='known.opus',publish_finished_at='2026-03-01T00:00:00Z' WHERE job_id='known'`); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				root := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/files/cassini/")
				if r.Method != "PROPFIND" {
					t.Errorf("unexpected %s", r.Method)
				}
				switch root {
				case ncRecordingsRoot:
					w.WriteHeader(207)
					fmt.Fprint(w, davSizesXML(root, true, 0, root+"/meetings", true, 0))
				case ncRecordingsRoot + "/meetings":
					w.WriteHeader(207)
					fmt.Fprint(w, davSizesXML(root, true, 0, root+"/known.opus", false, 10))
				case "Cassini/Recordings":
					if failLegacy {
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(207)
					fmt.Fprint(w, davSizesXML(root, true, 0, root+"/catalog.json", false, 7))
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			report := rt.refreshDetailedStorageUsage(t.Context(), testExAppConfig(server.URL))
			var sum int64
			for _, source := range report.Published {
				sum += source.Bytes
			}
			c := report.PublishedCategory
			if c == nil || c.Bytes != sum || c.Bytes != 10+c.UndatedBytes || c.Days[0].Bytes != 10 || report.PublishedCategoryError != "" {
				t.Fatal(report)
			}
			if (report.Published[2].Error != "") != failLegacy {
				t.Fatal(report.Published)
			}
			if rt.cachedDetailedStorageUsage().PublishedCategory.Bytes != sum {
				t.Fatal("cache omitted published dates")
			}
		})
	}
}

func TestStorageDAVReadsIdentityAndRejectsMissingSizes(t *testing.T) {
	for _, badSize := range []bool{false, true} {
		t.Run(fmt.Sprint(badSize), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(207)
				status := "HTTP/1.1 200 OK"
				if badSize {
					status = "HTTP/1.1 404 Not Found"
				}
				fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
   <d:response><d:href>%s/orphan.opus</d:href><d:propstat><d:status>%s</d:status><d:prop><d:getcontentlength>5</d:getcontentlength><oc:fileid>42</oc:fileid><d:getetag>"v1"</d:getetag></d:prop></d:propstat></d:response>
   <d:response><d:href>%s/orphan.opus</d:href><d:propstat><d:prop><d:getcontentlength>5</d:getcontentlength></d:prop></d:propstat></d:response>
   <d:response><d:href>/remote.php/dav/files/cassini/elsewhere.opus</d:href><d:propstat><d:prop><d:getcontentlength>9</d:getcontentlength></d:prop></d:propstat></d:response>
   </d:multistatus>`, r.URL.Path, status, r.URL.Path)
			}))
			defer server.Close()
			entries, _, err := testExAppConfig(server.URL).davListSizes(t.Context(), server.Client(), ncRecordingsOwner, ncRecordingsRoot+"/meetings")
			if badSize {
				if err == nil {
					t.Fatal("accepted failed size property")
				}
				return
			}
			if err != nil || len(entries) != 1 || entries[0].size != 5 || entries[0].fileID != 42 || entries[0].etag != `"v1"` {
				t.Fatal(entries, err)
			}
		})
	}
}
