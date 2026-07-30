package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestACLListXML(t *testing.T) {
	got := string(aclListXML([]aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "group", ID: "team & co"},
	}))
	for _, want := range []string{
		`xmlns:nc="http://nextcloud.org/ns"`,
		`<nc:acl-mapping-type>group</nc:acl-mapping-type><nc:acl-mapping-id>recording-viewers</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>0</nc:acl-permissions>`,
		`<nc:acl-mapping-type>user</nc:acl-mapping-type><nc:acl-mapping-id>admin</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>31</nc:acl-permissions>`,
		`<nc:acl-mapping-type>user</nc:acl-mapping-type><nc:acl-mapping-id>alice</nc:acl-mapping-id>`,
		`<nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>1</nc:acl-permissions>`,
		`<nc:acl-mapping-type>group</nc:acl-mapping-type>`,
		`team &amp; co`, // XML-escaped
	} {
		if !strings.Contains(got, want) {
			t.Errorf("aclListXML missing %q\n got: %s", want, got)
		}
	}
}

func TestRecordingACLRulesCoalesceBuiltInMappings(t *testing.T) {
	rules := recordingACLRules([]aclMapping{
		{Type: "user", ID: ncRecordingsOwner},
		{Type: "group", ID: ncRecordingsViewerGroup},
	})
	if len(rules) != 2 {
		t.Fatalf("rules = %+v, want the two built-in mappings without duplicates", rules)
	}
	if rules[0].Permissions != aclPermRead {
		t.Errorf("viewer group permissions = %d, want read when the group itself is a participant", rules[0].Permissions)
	}
	if rules[1].Permissions != aclMaskAll {
		t.Errorf("owner permissions = %d, want full permissions", rules[1].Permissions)
	}
}

func TestEnsureProtectedRulesPreservesParticipantsAddsDeny(t *testing.T) {
	// A recording that somehow lacks its viewer-group deny but keeps a
	// participant allow: self-heal must add the deny and the owner, and keep the
	// participant.
	got := ensureProtectedRules([]aclRule{
		{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead},
	})
	if hasViewerGroupDeny(got) == false {
		t.Fatalf("viewer group not denied after ensureProtectedRules: %+v", got)
	}
	var haveAlice, haveOwner bool
	for _, r := range got {
		if r.Type == "user" && r.ID == "alice" && r.Permissions == aclPermRead {
			haveAlice = true
		}
		if r.Type == "user" && r.ID == ncRecordingsOwner && r.Permissions == aclMaskAll {
			haveOwner = true
		}
	}
	if !haveAlice {
		t.Error("participant allow (alice) was dropped")
	}
	if !haveOwner {
		t.Error("owner full-access rule was not ensured")
	}

	// An already-protected recording is detected as such (no-op needed).
	protected := recordingACLRules([]aclMapping{{Type: "user", ID: "bob"}})
	if !hasViewerGroupDeny(protected) {
		t.Error("recordingACLRules output should already be viewer-group-denied")
	}
	// A stale ALLOW on the viewer group is replaced by a deny.
	fixed := ensureProtectedRules([]aclRule{
		{Type: "group", ID: ncRecordingsViewerGroup, Mask: aclMaskAll, Permissions: aclMaskAll},
	})
	if !hasViewerGroupDeny(fixed) {
		t.Errorf("stale viewer-group allow not turned into a deny: %+v", fixed)
	}
}

func TestBuildSidecarExtractsEntry(t *testing.T) {
	siteRoot := t.TempDir()
	sc := 3
	cat := siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: []siteCatalogEntry{
		{ID: "mtg_a", Title: "Standup", DateLabel: "Apr 29", AudioPath: "./meetings/JOB1.opus", ArtifactPath: "./meetings/JOB1.opus", SpeakerCount: &sc},
		{ID: "mtg_b", Title: "Other", DateLabel: "Apr 30", AudioPath: "./meetings/JOB2.opus"},
	}}
	raw, _ := json.Marshal(cat)
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	body, err := buildSidecar(siteRoot, "JOB1")
	if err != nil {
		t.Fatalf("buildSidecar: %v", err)
	}
	var entry siteCatalogEntry
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("sidecar json: %v", err)
	}
	if entry.Title != "Standup" || entry.DateLabel != "Apr 29" {
		t.Errorf("wrong entry: %+v", entry)
	}
	if entry.AudioPath != "JOB1.opus" {
		t.Errorf("audioPath = %q, want sibling basename JOB1.opus", entry.AudioPath)
	}
	if entry.ArtifactPath != "" {
		t.Errorf("artifactPath should be cleared, got %q", entry.ArtifactPath)
	}
	if entry.SpeakerCount == nil || *entry.SpeakerCount != 3 {
		t.Errorf("speakerCount not preserved: %v", entry.SpeakerCount)
	}

	if _, err := buildSidecar(siteRoot, "NOPE"); err == nil {
		t.Error("expected error for a job with no catalog entry")
	}
}

func TestNCFilesAccessApplierWritesSidecarAndACL(t *testing.T) {
	var mu sync.Mutex
	var got []davRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, davRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("AUTHORIZATION-APP-API"), ctype: r.Header.Get("Content-Type"), body: body})
		mu.Unlock()
		if r.Method == "PROPPATCH" {
			w.WriteHeader(http.StatusMultiStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	siteRoot := t.TempDir()
	cat := siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: []siteCatalogEntry{
		{ID: "mtg_a", Title: "Standup", DateLabel: "Apr 29", AudioPath: "./meetings/JOB1.opus"},
	}}
	raw, _ := json.Marshal(cat)
	if err := os.WriteFile(filepath.Join(siteRoot, "catalog.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	apply := cfg.ncFilesAccessApplier(nil)
	if apply == nil {
		t.Fatal("applier nil with AccessControl=true")
	}
	mappings := []aclMapping{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}}
	if err := apply(context.Background(), "JOB1", siteRoot, mappings); err != nil {
		t.Fatalf("apply: %v", err)
	}

	base := "/remote.php/dav/files/" + ncRecordingsOwner + "/" + ncRecordingsRoot + "/meetings/"
	wantAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":sekret"))
	var opusACL, sidecarACL, sidecarPut *davRequest
	for i := range got {
		if got[i].auth != wantAuth {
			t.Errorf("auth = %q, want owner %q (path %s)", got[i].auth, wantAuth, got[i].path)
		}
		switch {
		case got[i].method == "PROPPATCH" && strings.HasSuffix(got[i].path, "JOB1.opus"):
			opusACL = &got[i]
		case got[i].method == "PROPPATCH" && strings.HasSuffix(got[i].path, "JOB1"+sidecarSuffix):
			sidecarACL = &got[i]
		case got[i].method == http.MethodPut && strings.HasSuffix(got[i].path, "JOB1"+sidecarSuffix):
			sidecarPut = &got[i]
		}
	}
	if opusACL == nil {
		t.Fatalf("no PROPPATCH on the .opus (base %s); got %+v", base, got)
	}
	for _, want := range []string{"alice", "bob", "recording-viewers", "<nc:acl-permissions>0</nc:acl-permissions>", "<nc:acl-mask>31</nc:acl-mask>"} {
		if !strings.Contains(string(opusACL.body), want) {
			t.Errorf("opus ACL body missing %q", want)
		}
	}
	if sidecarPut == nil {
		t.Fatal("no sidecar PUT")
	}
	if !strings.Contains(string(sidecarPut.body), `"title":"Standup"`) {
		t.Errorf("sidecar body = %s", sidecarPut.body)
	}
	if sidecarACL == nil {
		t.Error("no PROPPATCH on the sidecar")
	}
}

func TestNCFilesAccessApplierNilUnlessEnabled(t *testing.T) {
	cfg := testExAppConfig("https://nc.example.com")
	if cfg.ncFilesAccessApplier(nil) != nil {
		t.Error("applier should be nil when AccessControl is off")
	}
	cfg.AccessControl = true
	if cfg.ncFilesAccessApplier(nil) == nil {
		t.Error("applier should be non-nil when AccessControl is on")
	}
	// No NextcloudURL -> inactive regardless of the flag.
	off := ExAppConfig{AccessControl: true}
	if off.ncFilesAccessApplier(nil) != nil {
		t.Error("applier should be nil without ExApp env")
	}
}
