package operator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func probeServe(t *testing.T, status int, body string) (ExAppConfig, *http.Client, []string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return testExAppConfig(srv.URL), &http.Client{}, seen
}

func TestProbeRealNextcloudLeafShape(t *testing.T) {
	// A real Nextcloud 34 leaf response: full namespace soup, an s: prefixed
	// sabre namespace, a percent-encoded href, and a separate 404 propstat for
	// the property the resource does not carry.
	const body = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/cassini/Cassini/Recordings/meetings/job%20a%2Fb.opus</d:href>
    <d:propstat>
      <d:prop>
        <d:getcontentlength>5242880</d:getcontentlength>
        <nc:acl-list>
          <nc:acl>
            <nc:acl-mapping-type>group</nc:acl-mapping-type>
            <nc:acl-mapping-id>everyone</nc:acl-mapping-id>
            <nc:acl-mask>31</nc:acl-mask>
            <nc:acl-permissions>0</nc:acl-permissions>
          </nc:acl>
          <nc:acl>
            <nc:acl-mapping-type>user</nc:acl-mapping-type>
            <nc:acl-mapping-id>cassini</nc:acl-mapping-id>
            <nc:acl-mask>31</nc:acl-mask>
            <nc:acl-permissions>31</nc:acl-permissions>
          </nc:acl>
        </nc:acl-list>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
	cfg, client, _ := probeServe(t, http.StatusMultiStatus, body)
	st, err := cfg.davPropfindLeafState(context.Background(), client, "cassini", "Cassini/Recordings/meetings/x.opus")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	fmt.Printf("REAL-SHAPE: exists=%v size=%d rules=%+v\n", st.Exists, st.Size, st.Rules)
}

func TestProbeCollectionShape(t *testing.T) {
	const body = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/cassini/Cassini/Recordings/meetings/</d:href>
    <d:propstat>
      <d:prop><nc:acl-list/></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
    <d:propstat>
      <d:prop><d:getcontentlength/></d:prop>
      <d:status>HTTP/1.1 404 Not Found</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
	cfg, client, _ := probeServe(t, http.StatusMultiStatus, body)
	st, err := cfg.davPropfindLeafState(context.Background(), client, "cassini", "Cassini/Recordings/meetings")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	fmt.Printf("COLLECTION: exists=%v size=%d rules=%+v\n", st.Exists, st.Size, st.Rules)
}

func TestProbeMultipleResponses(t *testing.T) {
	// A server that ignores Depth:0 and answers with the collection plus its
	// children — the first response is the collection, not the leaf.
	const body = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/cassini/Cassini/Recordings/meetings/</d:href>
    <d:propstat><d:prop><d:getcontentlength/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/cassini/Cassini/Recordings/meetings/a.opus</d:href>
    <d:propstat><d:prop><d:getcontentlength>17</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`
	cfg, client, _ := probeServe(t, http.StatusMultiStatus, body)
	st, err := cfg.davPropfindLeafState(context.Background(), client, "cassini", "Cassini/Recordings/meetings/a.opus")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	fmt.Printf("MULTI: exists=%v size=%d rules=%+v\n", st.Exists, st.Size, st.Rules)
}

func TestProbeEmptyBody200(t *testing.T) {
	cfg, client, _ := probeServe(t, http.StatusOK, "")
	st, err := cfg.davPropfindLeafState(context.Background(), client, "cassini", "Cassini/Recordings/meetings/a.opus")
	fmt.Printf("EMPTY200: state=%+v err=%v\n", st, err)
}

func TestProbeAudienceApplied(t *testing.T) {
	base := recordingACLRules(nil, false)
	fmt.Printf("BASELINE rules=%+v audienceApplied=%v hasEveryone=%v\n", base, audienceApplied(base), hasExplicitEveryoneGroupRule(base))
	pub := recordingACLRules(nil, true)
	fmt.Printf("PUBLIC   rules=%+v audienceApplied=%v\n", pub, audienceApplied(pub))
	cat := catalogProtectionACLRules()
	fmt.Printf("CATALOG  rules=%+v audienceApplied=%v\n", cat, audienceApplied(cat))
}

func TestProbeDavFileURLEscaping(t *testing.T) {
	cfg := testExAppConfig("https://nc.example.com")
	fmt.Printf("URL=%s\n", cfg.davFileURL("cassini", ncRecordingsRoot+"/meetings/"+"job id+#?&.opus"))
}
