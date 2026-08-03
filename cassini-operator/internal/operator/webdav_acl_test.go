package operator

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestACLListXML(t *testing.T) {
	got := string(aclListXML([]aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "group", ID: "team & co"},
	}, false))
	for _, want := range []string{
		`xmlns:nc="http://nextcloud.org/ns"`,
		`<nc:acl-mapping-type>group</nc:acl-mapping-type><nc:acl-mapping-id>everyone</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>0</nc:acl-permissions>`,
		`<nc:acl-mapping-type>user</nc:acl-mapping-type><nc:acl-mapping-id>` + ncRecordingsOwner + `</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>31</nc:acl-permissions>`,
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
		{Type: "group", ID: ncRecordingsEveryoneGroup},
	}, false)
	if len(rules) != 2 {
		t.Fatalf("rules = %+v, want the two built-in mappings without duplicates", rules)
	}
	if rules[0].Permissions != aclPermRead {
		t.Errorf("everyone permissions = %d, want read when the broad group itself is a participant", rules[0].Permissions)
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
	if hasEveryoneGroupDeny(got) == false {
		t.Fatalf("everyone not denied after ensureProtectedRules: %+v", got)
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
	protected := recordingACLRules([]aclMapping{{Type: "user", ID: "bob"}}, false)
	if !hasEveryoneGroupDeny(protected) {
		t.Error("recordingACLRules output should already deny everyone")
	}
	// A stale ALLOW passed directly to the private-repair helper is replaced by
	// a deny. The self-heal sweep preserves an explicit `everyone` allow because
	// it represents a deliberate public recording.
	fixed := ensureProtectedRules([]aclRule{
		{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: aclMaskAll},
	})
	if !hasEveryoneGroupDeny(fixed) {
		t.Errorf("stale everyone allow not turned into a deny: %+v", fixed)
	}
}

func TestNCFilesAccessApplierWritesOpusACLOnly(t *testing.T) {
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

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	apply := cfg.ncFilesAccessApplier(nil)
	if apply == nil {
		t.Fatal("applier nil with AccessControl=true")
	}
	mappings := []aclMapping{{Type: "user", ID: "alice"}, {Type: "user", ID: "bob"}}
	if err := apply(context.Background(), "JOB1", mappings, false); err != nil {
		t.Fatalf("apply: %v", err)
	}

	base := "/remote.php/dav/files/" + ncRecordingsOwner + "/" + ncRecordingsRoot + "/meetings/"
	wantAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":sekret"))
	var opusACL *davRequest
	for i := range got {
		if got[i].auth != wantAuth {
			t.Errorf("auth = %q, want owner %q (path %s)", got[i].auth, wantAuth, got[i].path)
		}
		if strings.HasSuffix(got[i].path, ".manifest.json") {
			t.Errorf("unexpected external manifest request: %s %s", got[i].method, got[i].path)
		}
		if got[i].method == "PROPPATCH" && strings.HasSuffix(got[i].path, "JOB1.opus") {
			opusACL = &got[i]
		}
	}
	if opusACL == nil {
		t.Fatalf("no PROPPATCH on the .opus (base %s); got %+v", base, got)
	}
	for _, want := range []string{"alice", "bob", "everyone", "<nc:acl-permissions>0</nc:acl-permissions>", "<nc:acl-mask>31</nc:acl-mask>"} {
		if !strings.Contains(string(opusACL.body), want) {
			t.Errorf("opus ACL body missing %q", want)
		}
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

// aclMultistatus builds a groupfolders PROPFIND (Depth 1) response for
// meetings/, reporting each named leaf's own nc:acl-list.
func aclMultistatus(leaves map[string][]aclRule) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">`)
	b.WriteString(`<d:response><d:href>/remote.php/dav/files/` + ncRecordingsOwner + `/Cassini/Recordings/meetings/</d:href>` +
		`<d:propstat><d:prop><nc:acl-list/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	for name, rules := range leaves {
		b.WriteString(`<d:response><d:href>/remote.php/dav/files/` + ncRecordingsOwner + `/Cassini/Recordings/meetings/` + name + `</d:href>`)
		b.WriteString(`<d:propstat><d:prop><nc:acl-list>`)
		for _, r := range rules {
			b.WriteString(`<nc:acl><nc:acl-mapping-type>` + r.Type + `</nc:acl-mapping-type>` +
				`<nc:acl-mapping-id>` + r.ID + `</nc:acl-mapping-id>` +
				`<nc:acl-mask>` + itoaTest(r.Mask) + `</nc:acl-mask>` +
				`<nc:acl-permissions>` + itoaTest(r.Permissions) + `</nc:acl-permissions></nc:acl>`)
		}
		b.WriteString(`</nc:acl-list></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	b.WriteString(`</d:multistatus>`)
	return b.String()
}

func itoaTest(i int) string { return strconv.Itoa(i) }

// Existing explicit `everyone` rules are authoritative, while legacy broad
// rules are translated without changing public/private polarity and an orphan
// is repaired as private.
func TestSelfHealPreservesCurrentRulesAndMigratesLegacyRules(t *testing.T) {
	var mu sync.Mutex
	proppatched := map[string]string{}
	legacyPublic := []aclRule{
		{Type: "group", ID: ncLegacyRecordingsViewerGroup, Mask: aclMaskAll, Permissions: aclPermRead},
		{Type: "user", ID: ncLegacyRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll},
	}
	legacyPrivate := []aclRule{
		{Type: "group", ID: ncLegacyRecordingsViewerGroup, Mask: aclMaskAll, Permissions: 0},
		{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead},
	}
	currentPublicOldOwner := []aclRule{
		{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: aclPermRead},
		{Type: "user", ID: ncLegacyRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll},
		{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead},
	}
	currentPrivateOldOwner := []aclRule{
		{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: 0},
		{Type: "user", ID: ncLegacyRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll},
		{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, aclMultistatus(map[string][]aclRule{
				"public.opus":                     recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, true),
				"private.opus":                    recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false),
				"legacy-public.opus":              legacyPublic,
				"legacy-private.opus":             legacyPrivate,
				"everyone-old-owner-public.opus":  currentPublicOldOwner,
				"everyone-old-owner-private.opus": currentPrivateOldOwner,
				"orphan.opus":                     {{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead}},
			}))
		case "PROPPATCH":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			proppatched[r.URL.Path] = string(body)
			mu.Unlock()
			w.WriteHeader(http.StatusMultiStatus)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	testExAppConfig(srv.URL).selfHealLeafProtection(context.Background(), srv.Client(), log.New(io.Discard, "", 0))

	mu.Lock()
	defer mu.Unlock()
	for path := range proppatched {
		if strings.HasSuffix(path, "/public.opus") || strings.HasSuffix(path, "/private.opus") {
			t.Fatalf("self-heal rewrote a current explicit ACL: %v", proppatched)
		}
	}
	assertMigrated := func(name, permission string) {
		t.Helper()
		var body string
		for path, candidate := range proppatched {
			if strings.HasSuffix(path, "/"+name) {
				body = candidate
			}
		}
		if body == "" || !strings.Contains(body, "<nc:acl-mapping-id>everyone</nc:acl-mapping-id>") ||
			!strings.Contains(body, "<nc:acl-permissions>"+permission+"</nc:acl-permissions>") ||
			!strings.Contains(body, "<nc:acl-mapping-id>"+ncRecordingsOwner+"</nc:acl-mapping-id>") ||
			strings.Contains(body, ncLegacyRecordingsViewerGroup) ||
			strings.Contains(body, "<nc:acl-mapping-id>"+ncLegacyRecordingsOwner+"</nc:acl-mapping-id>") {
			t.Fatalf("%s was not migrated with permissions=%s: %s", name, permission, body)
		}
	}
	assertMigrated("legacy-public.opus", "1")
	assertMigrated("legacy-private.opus", "0")
	assertMigrated("everyone-old-owner-public.opus", "1")
	assertMigrated("everyone-old-owner-private.opus", "0")
	assertMigrated("orphan.opus", "0")
}

func TestNormalizeOwnerRulesPreservesAdminParticipantRead(t *testing.T) {
	rules := normalizeOwnerRules([]aclRule{
		{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: 0},
		{Type: "user", ID: ncLegacyRecordingsOwner, Mask: aclMaskAll, Permissions: aclPermRead},
	})
	var adminRead, ownerAll bool
	for _, rule := range rules {
		adminRead = adminRead || (rule.Type == "user" && rule.ID == ncLegacyRecordingsOwner && rule.Permissions == aclPermRead)
		ownerAll = ownerAll || (rule.Type == "user" && rule.ID == ncRecordingsOwner && rule.Permissions == aclMaskAll)
	}
	if !adminRead || !ownerAll {
		t.Fatalf("owner normalization dropped participant read or omitted service owner: %+v", rules)
	}
}

func TestHasExplicitEveryoneGroupRule(t *testing.T) {
	public := recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, true)
	private := recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false)

	if hasEveryoneGroupDeny(public) {
		t.Error("a public recording must not deny everyone")
	}
	if !hasExplicitEveryoneGroupRule(public) {
		t.Error("a public recording states an explicit everyone allow")
	}
	if !hasEveryoneGroupDeny(private) || !hasExplicitEveryoneGroupRule(private) {
		t.Error("a private recording states an explicit everyone deny")
	}
	orphan := []aclRule{{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead}}
	if hasExplicitEveryoneGroupRule(orphan) {
		t.Error("a leaf with no everyone rule must not read as explicit")
	}
}

func TestFilteredCatalogFailsClosedWhenUniversalMountIsMissing(t *testing.T) {
	const catalog = `{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"pub","audioPath":"meetings/pub.opus"}]}`
	var groupWrites, propfinds int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/groups"):
			groupWrites++
		case r.Method == "PROPFIND":
			propfinds++
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, catalog)
		}
	}))
	defer srv.Close()

	w := httptest.NewRecorder()
	testExAppConfig(srv.URL).serveFilteredCatalog(context.Background(), w, srv.Client(), "bob", log.New(io.Discard, "", 0))

	if groupWrites != 0 || propfinds != 1 {
		t.Fatalf("missing mount caused membership writes/rescans: writes=%d propfinds=%d", groupWrites, propfinds)
	}
	if strings.Contains(w.Body.String(), `"pub"`) || !strings.Contains(w.Body.String(), `"meetings":[]`) {
		t.Fatalf("missing universal mount must serve an empty catalog: %s", w.Body.String())
	}
}

// A caller who genuinely has a mount and simply cannot read anything must not
// trigger a group write on every catalog fetch.
func TestFilteredCatalogDoesNotTouchGroupsForAMountedCaller(t *testing.T) {
	var mu sync.Mutex
	var groupWrites int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/groups"):
			mu.Lock()
			groupWrites++
			mu.Unlock()
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200}}}`)
		case r.Method == "PROPFIND":
			// Mounted, but every recording is denied: the collection itself is
			// visible and lists no children.
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, aclMultistatus(nil))
		default:
			_, _ = io.WriteString(w, `{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"x","audioPath":"meetings/x.opus"}]}`)
		}
	}))
	defer srv.Close()

	w := httptest.NewRecorder()
	testExAppConfig(srv.URL).serveFilteredCatalog(context.Background(), w, srv.Client(), "carol", log.New(io.Discard, "", 0))

	mu.Lock()
	defer mu.Unlock()
	if groupWrites != 0 {
		t.Fatalf("a mounted caller triggered %d group write(s); membership must only converge on a missing mount", groupWrites)
	}
	if strings.Contains(w.Body.String(), `"x"`) {
		t.Fatalf("a denied meeting leaked to a mounted caller: %s", w.Body.String())
	}
}
