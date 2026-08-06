package operator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Zero-config Talk recording provisioning (D-447).
//
// Installing Cassini from the App Store is one click, but wiring it up as
// Talk's recording backend historically meant the admin had to (1) invent a
// shared secret, (2) pass it to Cassini as CASSINI_TALK_RECORDING_SECRET, and
// (3) register it in spreed's recording_servers by hand. This file removes the
// human from the secret: when no secret is supplied Cassini generates one on
// first start and persists it, and it derives + publishes exactly what to
// register in Talk.
//
// What it CANNOT do: write spreed's recording_servers itself. That config is
// admin-only and has no OCS API an ExApp may call (AppAPI's app-config write is
// hard-scoped to the ExApp's own app id; spreed writes recording_servers only
// from its admin settings / occ). So the registration stays a single admin
// action — but a secret-free one: the host reads the generated secret + backend
// URL from the ADMIN-only provisioning endpoint and sets recording_servers in
// one idempotent step. See docs/exapp-install.md and sandbox/wire-cassini.sh.

const (
	// talkProvisioningFile is the persisted secret store, kept next to the
	// operator DB so it lands on the AppAPI persistent volume and survives
	// container restart + app update (same rationale as settings.json).
	talkProvisioningFile = "talk-provisioning.json"

	// talkRecordingSecretBytes is the entropy of a generated recording secret;
	// 32 bytes -> 64 hex chars, matching the length Talk's own setup uses.
	talkRecordingSecretBytes = 32

	// AppAPI proxies an ExApp's PUBLIC routes under this prefix; Talk must reach
	// Cassini's recording-backend protocol here, so this is the server URL to
	// register in spreed's recording_servers.
	appAPIProxyPathPrefix = "/index.php/apps/app_api/proxy/"

	talkSecretSourceEnv       = "env"
	talkSecretSourceGenerated = "generated"
	talkSecretSourceUnset     = "unset"
)

// talkProvisioningState is the durable secret store. Only the generated secret
// is persisted; an env/flag-supplied secret is externally managed and never
// written here.
type talkProvisioningState struct {
	RecordingSecret string `json:"recording_secret"`
}

// talkProvisioning holds the resolved provisioning values for the process
// lifetime: the effective recording secret, where it came from, and the backend
// URL to register in Talk. It is built once at startup and read by the status
// and provisioning handlers.
type talkProvisioning struct {
	Secret       string
	SecretSource string
	// BackendURL is the recording_servers server value to register in spreed:
	// the AppAPI proxy base for this ExApp. Empty when NEXTCLOUD_URL / APP_ID
	// are unknown (standalone / dev), in which case it cannot be derived.
	BackendURL string
}

// talkSecretStore persists the generated recording secret atomically. A mutex
// guards concurrent generation on the (unlikely) racing first start.
type talkSecretStore struct {
	path string
	mu   sync.Mutex
}

func newTalkSecretStore(dataDir string) *talkSecretStore {
	return &talkSecretStore{path: filepath.Join(dataDir, talkProvisioningFile)}
}

// loadOrGenerate returns the persisted recording secret, generating and
// persisting a fresh one on first start. The file is written 0600: it holds a
// live credential, unlike the world-readable settings.json.
func (s *talkSecretStore) loadOrGenerate() (secret string, generated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err == nil {
		var st talkProvisioningState
		if err := json.Unmarshal(raw, &st); err != nil {
			return "", false, fmt.Errorf("parse %s: %w", s.path, err)
		}
		if secret := strings.TrimSpace(st.RecordingSecret); secret != "" {
			return secret, false, nil
		}
		// File exists but carries no secret (truncated/hand-edited): fall
		// through and regenerate so Talk recording is not left unusable.
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read %s: %w", s.path, err)
	}

	fresh, err := generateTalkRecordingSecret()
	if err != nil {
		return "", false, err
	}
	if err := s.save(talkProvisioningState{RecordingSecret: fresh}); err != nil {
		return "", false, err
	}
	return fresh, true, nil
}

// save writes the provisioning state atomically (temp file + rename) with 0600
// perms so a crash mid-write cannot truncate the persisted secret.
func (s *talkSecretStore) save(st talkProvisioningState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir provisioning dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal provisioning state: %w", err)
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write provisioning temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename provisioning state: %w", err)
	}
	return nil
}

func generateTalkRecordingSecret() (string, error) {
	buf := make([]byte, talkRecordingSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate talk recording secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// resolveTalkProvisioning decides the effective recording secret and backend
// URL. An explicit secret (CASSINI_TALK_RECORDING_SECRET via flag/env) always
// wins and is treated as externally managed — never persisted. Otherwise the
// operator loads or generates a persisted secret so Talk recording works with
// zero configuration. A generation/persist failure is returned so startup can
// fall back to "no secret" (Talk recording disabled) rather than crash.
func resolveTalkProvisioning(dataDir, explicitSecret, nextcloudURL, appID string, logger *log.Logger) talkProvisioning {
	prov := talkProvisioning{
		BackendURL: deriveRecordingBackendURL(nextcloudURL, appID),
	}

	if s := strings.TrimSpace(explicitSecret); s != "" {
		prov.Secret = s
		prov.SecretSource = talkSecretSourceEnv
		return prov
	}

	store := newTalkSecretStore(dataDir)
	secret, generated, err := store.loadOrGenerate()
	if err != nil {
		// Degrade, loudly: Talk recording stays off until a secret is supplied,
		// but the operator still serves everything else (D-447 must not turn a
		// disk hiccup into a crash loop).
		logger.Printf("ERROR: talk provisioning: could not load or generate recording secret: %v — Talk recording is disabled until CASSINI_TALK_RECORDING_SECRET is set or the store is writable", err)
		prov.SecretSource = talkSecretSourceUnset
		return prov
	}
	prov.Secret = secret
	prov.SecretSource = talkSecretSourceGenerated
	if generated {
		logger.Printf("talk provisioning: generated a Talk recording secret (persisted to %s); register it in spreed via the operator provisioning endpoint or wire-cassini.sh", filepath.Join(dataDir, talkProvisioningFile))
	}
	return prov
}

// talkRecordingServer mirrors one entry of spreed's recording_servers config
// (server URL + TLS verification), so the provisioning response carries a value
// the host can hand straight to `occ config:app:set spreed recording_servers`.
type talkRecordingServer struct {
	Server string `json:"server"`
	Verify bool   `json:"verify"`
}

// talkRecordingServers is the exact recording_servers JSON to register in
// spreed: the servers list plus the shared secret.
type talkRecordingServers struct {
	Servers []talkRecordingServer `json:"servers"`
	Secret  string                `json:"secret"`
}

// talkProvisioningResponse is the ADMIN-only provisioning bundle. It exposes
// the recording secret on purpose — that is the whole point: the host reads it
// here and registers it in spreed so no human ever handles the secret. The
// route is ADMIN-ACL'd at the AppAPI proxy (see appinfo/info.xml).
type talkProvisioningResponse struct {
	SecretConfigured    bool   `json:"secret_configured"`
	SecretSource        string `json:"secret_source"`
	RecordingSecret     string `json:"recording_secret,omitempty"`
	RecordingBackendURL string `json:"recording_backend_url,omitempty"`
	// CallRecording is the spreed call_recording value to enable ("yes"), given
	// so the host can turn recording on in the same step.
	CallRecording string `json:"call_recording"`
	// RecordingServers is the ready-to-apply spreed recording_servers value,
	// present only when both the secret and backend URL are known.
	RecordingServers *talkRecordingServers `json:"recording_servers,omitempty"`
	// Note explains any missing piece so the caller knows what to configure.
	Note string `json:"note,omitempty"`
}

// talkProvisioningHandler serves the ADMIN-only Talk provisioning bundle: the
// effective recording secret and the recording_servers value to register in
// spreed. This closes the loop for one-command, secret-free registration when
// the operator generated its own secret (D-447). Cassini cannot write spreed's
// config itself (no OCS API), so the host consumes this endpoint.
func (rt *Runtime) talkProvisioningHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/talk/provisioning" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	secret := strings.TrimSpace(rt.cfg.TalkSharedSecret)
	backendURL := strings.TrimSpace(rt.cfg.TalkRecordingBackendURL)
	resp := talkProvisioningResponse{
		SecretConfigured:    secret != "",
		SecretSource:        rt.cfg.TalkSecretSource,
		RecordingSecret:     secret,
		RecordingBackendURL: backendURL,
		CallRecording:       "yes",
	}
	switch {
	case secret == "":
		resp.Note = "no Talk recording secret is configured; set CASSINI_TALK_RECORDING_SECRET or make the provisioning store writable"
	case backendURL == "":
		resp.Note = "recording backend URL is unknown (NEXTCLOUD_URL / APP_ID unset); register recording_servers with this operator's AppAPI proxy URL and the secret above"
	default:
		resp.RecordingServers = &talkRecordingServers{
			Servers: []talkRecordingServer{{Server: backendURL, Verify: true}},
			Secret:  secret,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// deriveRecordingBackendURL builds the recording_servers server value to
// register in spreed: the AppAPI proxy base for this ExApp. Returns "" when
// either input is unknown (standalone / dev deploys), where the value cannot be
// derived and must be configured out of band.
func deriveRecordingBackendURL(nextcloudURL, appID string) string {
	base := strings.TrimRight(strings.TrimSpace(nextcloudURL), "/")
	appID = strings.TrimSpace(appID)
	if base == "" || appID == "" {
		return ""
	}
	return base + appAPIProxyPathPrefix + appID
}
