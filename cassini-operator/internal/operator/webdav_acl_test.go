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
		`<nc:acl-mapping-type>group</nc:acl-mapping-type><nc:acl-mapping-id>recording-viewers</nc:acl-mapping-id><nc:acl-mask>31</nc:acl-mask><nc:acl-permissions>0</nc:acl-permissions>`,
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
		{Type: "group", ID: ncRecordingsViewerGroup},
	}, false)
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
	protected := recordingACLRules([]aclMapping{{Type: "user", ID: "bob"}}, false)
	if !hasViewerGroupDeny(protected) {
		t.Error("recordingACLRules output should already be viewer-group-denied")
	}
	// A stale ALLOW on the viewer group is replaced by a deny.
	//
	// This is a property of the repair function in isolation, NOT of the sweep
	// that calls it: selfHealLeafProtection no longer hands it a leaf that
	// states any viewer-group rule, because an allow there is a deliberate
	// public grant rather than damage (D-552, see
	// TestSelfHealLeavesAPublicRecordingAlone). Kept because the function must
	// still normalise whatever it is given.
	fixed := ensureProtectedRules([]aclRule{
		{Type: "group", ID: ncRecordingsViewerGroup, Mask: aclMaskAll, Permissions: aclMaskAll},
	})
	if !hasViewerGroupDeny(fixed) {
		t.Errorf("stale viewer-group allow not turned into a deny: %+v", fixed)
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
	// Two identities, and which one is used where is the point (D-532): the
	// recording's own files are written as the service account that owns them,
	// while group membership is instance administration and needs the admin.
	wantAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":sekret"))
	wantAdminAuth := base64.StdEncoding.EncodeToString([]byte(defaultNextcloudAdminUser + ":sekret"))
	var opusACL *davRequest
	for i := range got {
		expected, role := wantAuth, "owner"
		if strings.Contains(got[i].path, "/ocs/v2.php/cloud/") {
			expected, role = wantAdminAuth, "administrator"
		}
		if got[i].auth != expected {
			t.Errorf("auth = %q, want %s %q (path %s)", got[i].auth, role, expected, got[i].path)
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
	for _, want := range []string{"alice", "bob", "recording-viewers", "<nc:acl-permissions>0</nc:acl-permissions>", "<nc:acl-mask>31</nc:acl-mask>"} {
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
	b.WriteString(`<d:response><d:href>/remote.php/dav/files/admin/Cassini/Recordings/meetings/</d:href>` +
		`<d:propstat><d:prop><nc:acl-list/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	for name, rules := range leaves {
		b.WriteString(`<d:response><d:href>/remote.php/dav/files/admin/Cassini/Recordings/meetings/` + name + `</d:href>`)
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

// TestSelfHealLeavesAPublicRecordingAlone pins the D-552 regression.
//
// A public meeting's leaf ACL *is* a viewer-group read ALLOW. The self-heal
// sweep used to classify "no viewer-group DENY" as damage, so it rewrote every
// public recording back to participant-private on the next enabled edge —
// silently undoing the one thing D-552 exists to do. A registered user who was
// not in the meeting could then no longer see the public recording.
func TestSelfHealLeavesAPublicRecordingAlone(t *testing.T) {
	var mu sync.Mutex
	var proppatched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PROPFIND":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, aclMultistatus(map[string][]aclRule{
				// A public recording: the viewer group is granted read.
				"public.opus": recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, true),
				// A private one: the viewer group is denied.
				"private.opus": recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false),
				// Genuinely unprotected: no viewer-group rule at all, so it
				// inherits the container's broad grant and MUST be healed.
				"orphan.opus": {{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead}},
			}))
		case "PROPPATCH":
			mu.Lock()
			proppatched = append(proppatched, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusMultiStatus)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.selfHealLeafProtection(context.Background(), srv.Client(), log.New(io.Discard, "", 0))

	mu.Lock()
	defer mu.Unlock()
	for _, path := range proppatched {
		if strings.HasSuffix(path, "/public.opus") {
			t.Fatalf("self-heal rewrote the public recording's ACL (D-552 regression); PROPPATCHed: %v", proppatched)
		}
		if strings.HasSuffix(path, "/private.opus") {
			t.Errorf("self-heal rewrote an already-protected private recording: %v", proppatched)
		}
	}
	// The safety net must still do its job for a leaf that states no rule.
	var healedOrphan bool
	for _, path := range proppatched {
		if strings.HasSuffix(path, "/orphan.opus") {
			healedOrphan = true
		}
	}
	if !healedOrphan {
		t.Fatalf("self-heal skipped a leaf with no viewer-group rule; PROPPATCHed: %v", proppatched)
	}
}

func TestHasExplicitViewerGroupRule(t *testing.T) {
	public := recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, true)
	private := recordingACLRules([]aclMapping{{Type: "user", ID: "alice"}}, false)

	// The distinction the self-heal turns on: a public leaf carries an explicit
	// viewer-group rule, it simply is not a deny.
	if hasViewerGroupDeny(public) {
		t.Error("a public recording must not read as viewer-group-denied")
	}
	if !hasExplicitViewerGroupRule(public) {
		t.Error("a public recording states an explicit viewer-group rule (an allow)")
	}
	if !hasViewerGroupDeny(private) || !hasExplicitViewerGroupRule(private) {
		t.Error("a private recording states an explicit viewer-group deny")
	}
	// A leaf relying on inheritance states nothing — the only real offender.
	orphan := []aclRule{{Type: "user", ID: "alice", Mask: aclMaskAll, Permissions: aclPermRead}}
	if hasExplicitViewerGroupRule(orphan) {
		t.Error("a leaf with no viewer-group rule must not read as explicit")
	}
}

// TestFilteredCatalogConvergesAnUnmountedCaller covers the other half of the
// same user-visible bug: a public recording is granted to the viewer GROUP, so
// an account that is not in that group yet has no mount of the recordings
// folder at all. Its PROPFIND 404s, which used to be folded into an empty-but-
// successful listing — a brand-new user saw an empty archive, public meetings
// included, until the next reconcile sweep (up to 15 minutes later).
func TestFilteredCatalogConvergesAnUnmountedCaller(t *testing.T) {
	const catalog = `{"version":"cassini.viewer.catalog.v1","meetings":[{"id":"pub","audioPath":"meetings/pub.opus"}]}`

	var mu sync.Mutex
	var propfinds int
	var addedToGroup []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/groups"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			addedToGroup = append(addedToGroup, r.URL.Path+"?"+string(body))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200}}}`)
		case r.Method == "PROPFIND":
			mu.Lock()
			propfinds++
			n := propfinds
			mu.Unlock()
			if n == 1 {
				// Not in the viewer group yet: the collection does not exist
				// for this caller.
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, aclMultistatus(map[string][]aclRule{"pub.opus": nil}))
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, catalog)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	w := httptest.NewRecorder()
	cfg.serveFilteredCatalog(context.Background(), w, srv.Client(), "bob", log.New(io.Discard, "", 0))

	mu.Lock()
	defer mu.Unlock()
	if len(addedToGroup) != 1 || !strings.Contains(addedToGroup[0], "bob") ||
		!strings.Contains(addedToGroup[0], ncRecordingsViewerGroup) {
		t.Fatalf("caller was not added to %q on first access: %v", ncRecordingsViewerGroup, addedToGroup)
	}
	if propfinds != 2 {
		t.Errorf("PROPFIND count = %d, want 2 (initial + rescan after the group add)", propfinds)
	}
	if !strings.Contains(w.Body.String(), `"pub"`) {
		t.Fatalf("the public meeting was not served to a freshly converged caller: %s", w.Body.String())
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
