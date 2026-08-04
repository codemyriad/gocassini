package operator

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestResolveTalkProvisioningExplicitSecretWins(t *testing.T) {
	dir := t.TempDir()
	prov := resolveTalkProvisioning(dir, "  injected-secret  ", "https://nc.example/", "gocassini", discardLogger())

	if prov.Secret != "injected-secret" {
		t.Fatalf("secret = %q, want trimmed injected-secret", prov.Secret)
	}
	if prov.SecretSource != talkSecretSourceEnv {
		t.Fatalf("source = %q, want %q", prov.SecretSource, talkSecretSourceEnv)
	}
	if prov.BackendURL != "https://nc.example/index.php/apps/app_api/proxy/gocassini" {
		t.Fatalf("backend url = %q", prov.BackendURL)
	}
	// An explicit secret must never be persisted — it is externally managed.
	if _, err := os.Stat(filepath.Join(dir, talkProvisioningFile)); !os.IsNotExist(err) {
		t.Fatalf("provisioning file should not exist for an env secret, stat err = %v", err)
	}
}

func TestResolveTalkProvisioningGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	first := resolveTalkProvisioning(dir, "", "https://nc.example", "gocassini", discardLogger())

	if first.SecretSource != talkSecretSourceGenerated {
		t.Fatalf("source = %q, want %q", first.SecretSource, talkSecretSourceGenerated)
	}
	if len(first.Secret) != talkRecordingSecretBytes*2 {
		t.Fatalf("generated secret length = %d, want %d hex chars", len(first.Secret), talkRecordingSecretBytes*2)
	}

	path := filepath.Join(dir, talkProvisioningFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("provisioning file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("provisioning file perm = %o, want 600", perm)
	}

	// A second resolve reads the same persisted secret (stable across restart).
	second := resolveTalkProvisioning(dir, "", "https://nc.example", "gocassini", discardLogger())
	if second.Secret != first.Secret {
		t.Fatalf("secret changed across restart: %q -> %q", first.Secret, second.Secret)
	}
	if second.SecretSource != talkSecretSourceGenerated {
		t.Fatalf("second source = %q, want %q", second.SecretSource, talkSecretSourceGenerated)
	}
}

func TestResolveTalkProvisioningRegeneratesEmptyStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, talkProvisioningFile)
	if err := os.WriteFile(path, []byte(`{"recording_secret":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := resolveTalkProvisioning(dir, "", "", "", discardLogger())
	if prov.Secret == "" || prov.SecretSource != talkSecretSourceGenerated {
		t.Fatalf("expected a regenerated secret, got %#v", prov)
	}
}

func TestResolveTalkProvisioningStoreUnwritable(t *testing.T) {
	dir := t.TempDir()
	// Make the data dir itself a file so the store cannot create/write into it.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := resolveTalkProvisioning(blocked, "", "https://nc.example", "gocassini", discardLogger())
	if prov.Secret != "" || prov.SecretSource != talkSecretSourceUnset {
		t.Fatalf("expected unset secret on unwritable store, got %#v", prov)
	}
	// Backend URL is independent of the secret store and must still be derived.
	if prov.BackendURL == "" {
		t.Fatalf("backend url should still derive, got empty")
	}
}

func TestDeriveRecordingBackendURL(t *testing.T) {
	cases := []struct {
		nc, appID, want string
	}{
		{"https://nc.example", "gocassini", "https://nc.example/index.php/apps/app_api/proxy/gocassini"},
		{"https://nc.example/", "gocassini", "https://nc.example/index.php/apps/app_api/proxy/gocassini"},
		{"  https://nc.example//  ", " gocassini ", "https://nc.example/index.php/apps/app_api/proxy/gocassini"},
		{"", "gocassini", ""},
		{"https://nc.example", "", ""},
	}
	for _, c := range cases {
		if got := deriveRecordingBackendURL(c.nc, c.appID); got != c.want {
			t.Errorf("derive(%q,%q) = %q, want %q", c.nc, c.appID, got, c.want)
		}
	}
}

func TestTalkProvisioningHandlerFullBundle(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "the-secret"
	rt.cfg.TalkSecretSource = talkSecretSourceGenerated
	rt.cfg.TalkRecordingBackendURL = "https://nc.example/index.php/apps/app_api/proxy/gocassini"

	rec := httptest.NewRecorder()
	rt.talkProvisioningHandler(rec, httptest.NewRequest(http.MethodGet, "/talk/provisioning", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp talkProvisioningResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.SecretConfigured || resp.RecordingSecret != "the-secret" || resp.CallRecording != "yes" {
		t.Fatalf("unexpected bundle: %#v", resp)
	}
	if resp.RecordingServers == nil || resp.RecordingServers.Secret != "the-secret" ||
		len(resp.RecordingServers.Servers) != 1 || !resp.RecordingServers.Servers[0].Verify ||
		resp.RecordingServers.Servers[0].Server != rt.cfg.TalkRecordingBackendURL {
		t.Fatalf("recording_servers not ready-to-apply: %#v", resp.RecordingServers)
	}
	if resp.Note != "" {
		t.Fatalf("unexpected note: %q", resp.Note)
	}
}

func TestTalkProvisioningHandlerMissingPieces(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rt.cfg.TalkSharedSecret = "the-secret"
	rt.cfg.TalkSecretSource = talkSecretSourceGenerated
	rt.cfg.TalkRecordingBackendURL = "" // standalone/dev: URL not derivable

	rec := httptest.NewRecorder()
	rt.talkProvisioningHandler(rec, httptest.NewRequest(http.MethodGet, "/talk/provisioning", nil))
	var resp talkProvisioningResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RecordingServers != nil {
		t.Fatalf("recording_servers should be nil without a backend URL")
	}
	if !strings.Contains(resp.Note, "recording backend URL is unknown") {
		t.Fatalf("expected explanatory note, got %q", resp.Note)
	}
}

func TestTalkProvisioningHandlerRejectsNonGET(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	rec := httptest.NewRecorder()
	rt.talkProvisioningHandler(rec, httptest.NewRequest(http.MethodPost, "/talk/provisioning", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
