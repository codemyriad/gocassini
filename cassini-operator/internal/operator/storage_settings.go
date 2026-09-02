package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Which storage model Cassini keeps recordings in, and where that decision
// lives (D-616 first pass).
//
// Cassini has had exactly one storage model since D-554: a Team folder with
// advanced ACLs, where each recording is readable only by the people who were
// in the meeting. That model needs two Nextcloud apps an ExApp cannot install
// for itself, so an instance without them records happily and then cannot
// publish at all — the app forces a choice it never asked the administrator to
// make. The opt-in turns that into a decision with a default:
//
//	default            recordings live in the `cassini` service account's own
//	                   private Cassini/Recordings, and everyone who can open the
//	                   Cassini app can read every recording. No third-party apps,
//	                   no ACLs, nothing to install.
//	access controlled  today's model, unchanged: the Team folder, the `everyone`
//	                   mount, and a per-recording audience frozen at publish.
//
// ┌──────────────── storage_settings.json ────────────────┐
// │  {"access_control_enabled": true|false}   (or absent) │
// └───────────────────────────┬───────────────────────────┘
//
//	│ read on the AppAPI enabled edge
//	▼
//
// ┌──────────────────── preflight ────────────────────────┐
// │  probe Nextcloud (read only)                          │
// │  flag absent ─▶ derive it from the probe, then persist│
// │  flag present ─▶ sanity-check it against the probe    │
// └───────────────────────────┬───────────────────────────┘
//
//	│
//	▼            ncStorage (process-wide)
//	           publish sink ── read proxy ── /status ── /storage
//
// This lives in its own file, apart from settings.json, on purpose.
// settings.json is the STT policy: a different lifecycle (hardware-derived,
// migrated on load, rewritten when the host changes) and a different owner.
// Merging the two would mean one loader that can fail for two unrelated reasons
// and one schema two features have to agree on. A separate file can be deleted,
// swapped for a Nextcloud app-config store, or moved under an ExApp settings
// API without touching anything the recorder depends on.

const (
	// storageModeDefault is the deps-free model: one private tree in the
	// service account's home, readable by everyone the app is readable by.
	storageModeDefault = "default"
	// storageModeAccessControlled is the Team-folder + per-recording-ACL model.
	storageModeAccessControlled = "access_controlled"

	// storageModeSourceConfigured means the flag was read from disk — either an
	// administrator's choice, or a default this operator derived and persisted
	// on an earlier run. Once written it is never re-derived: a derived default
	// that could flip when Nextcloud changes underneath it would silently
	// change who can read every recording.
	storageModeSourceConfigured = "configured"
	// storageModeSourceDerived means the flag was absent and this run derived
	// it. It is only ever reported for the run that also persisted it.
	storageModeSourceDerived = "derived"
	// storageModeSourceUser means an administrator chose it, by switching modes
	// in the Setup tab. Never reconsidered.
	storageModeSourceUser = "user"
	// storageModeSourceEnv means the deployment declared it, through
	// envStorageMode. Also never reconsidered: an operator who set it said what
	// they wanted, and a deploy option is as explicit as a button.
	storageModeSourceEnv = "env"

	// envStorageMode declares the mode a FRESH install starts in. It seeds the
	// flag and nothing more: once storage_settings.json records a decision, the
	// file is authoritative and changing this variable does not move an archive
	// that already exists.
	//
	// It exists because deriving the mode from the instance is a guess about
	// timing — it reads whatever Nextcloud looks like on the first enabled edge,
	// which on a stack still being built is the wrong moment. A deployment that
	// KNOWS which model it wants should be able to say so instead.
	envStorageMode = "CASSINI_STORAGE_MODE"

	storageSettingsFileName = "storage_settings.json"
)

// StorageSettings is the persisted storage-mode policy: one flag, and whether
// it was ever actually written.
//
// The pointer is the whole point. A bare bool cannot tell "an administrator
// chose the default model" from "nobody has decided yet", and those two must
// not behave the same: the first is a decision to honour, the second is a
// question to answer from the state of the instance. Every install that exists
// today is in the second case, and resolving it the wrong way turns an
// access-controlled archive into an org-wide one.
type StorageSettings struct {
	AccessControlEnabled *bool `json:"access_control_enabled"`
	// Source is how the flag got there: storageModeSourceDerived when Cassini
	// worked it out from the instance, storageModeSourceUser when somebody
	// chose it.
	//
	// The difference decides one thing only, and it is worth the extra field:
	// whether a `default` may be RECONSIDERED. A derived one may — it was a
	// guess made at whatever moment the enabled edge happened to fire, and on a
	// Nextcloud still being set up (or one whose occ-made changes had not yet
	// reached the web workers) that guess is wrong and permanent. A chosen one
	// may not: an administrator who picked default meant it.
	//
	// Absent in files written before this field existed, which reads as derived
	// — those were all written by the derivation.
	Source string `json:"source,omitempty"`
}

// Chosen reports whether somebody STATED this mode — an administrator in the
// Setup tab, or a deployment through CASSINI_STORAGE_MODE — as opposed to
// Cassini having derived it. A stated mode is never reconsidered.
func (s StorageSettings) Chosen() bool {
	return s.Source == storageModeSourceUser || s.Source == storageModeSourceEnv
}

// storageModeFromEnv reads the declared initial mode.
//
// `ok` is false when the variable is unset, which is the ordinary case and
// means "derive it". `ok` is also false for a value that is set but
// unrecognised, and `raw` is then non-empty so the caller can say so loudly —
// silently ignoring a typo would start the instance in a mode nobody asked for.
//
// The spellings are deliberately generous. The harness flag says `acl-enabled`,
// the API and the config file say `access_controlled`, and a person setting a
// deploy option should not have to know which vocabulary they are in.
func storageModeFromEnv(lookup func(string) string) (accessControlled bool, ok bool, raw string) {
	raw = strings.TrimSpace(lookup(envStorageMode))
	switch strings.ToLower(raw) {
	case "":
		return false, false, ""
	case storageModeDefault, "off", "none":
		return false, true, raw
	case storageModeAccessControlled, "access-controlled", "acl-enabled", "acl", "on":
		return true, true, raw
	default:
		return false, false, raw
	}
}

// storageModeEnvValues is what an error message offers instead of the value it
// rejected.
const storageModeEnvValues = `"` + storageModeDefault + `" or "` + storageModeAccessControlled + `"`

// Configured reports whether a decision has been recorded.
func (s StorageSettings) Configured() bool { return s.AccessControlEnabled != nil }

// AccessControlled reports the recorded decision, defaulting to false for a
// file that carries none. Callers that care about the difference ask
// Configured first.
func (s StorageSettings) AccessControlled() bool {
	return s.AccessControlEnabled != nil && *s.AccessControlEnabled
}

// Mode names the recorded decision for humans and for the UI.
func (s StorageSettings) Mode() string { return storageModeName(s.AccessControlled()) }

func storageModeName(accessControlled bool) string {
	if accessControlled {
		return storageModeAccessControlled
	}
	return storageModeDefault
}

// storageSettingsPath puts the file beside settings.json and the job DB, so it
// survives restart and redeploy on the AppAPI persistent volume — the storage
// mode has to outlive the container that decided it, or every restart would
// re-derive it from whatever Nextcloud looks like at that moment.
func storageSettingsPath(cfg Config) string {
	return filepath.Join(filepath.Dir(cfg.DBPath), storageSettingsFileName)
}

// LoadStorageSettings reads the file. A missing file is not an error: it is the
// ordinary state of an install that has not been preflighted yet, and it is
// reported through Configured() rather than through err.
//
// A file that exists but cannot be parsed IS an error. Treating it as "no
// decision" would let one bad byte silently re-derive the mode, and the derived
// default of an install whose Team folder has since been removed is `default` —
// which would publish the next recording where everybody can read it.
func LoadStorageSettings(path string) (StorageSettings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StorageSettings{}, nil
		}
		return StorageSettings{}, fmt.Errorf("read storage settings: %w", err)
	}
	var s StorageSettings
	if err := json.Unmarshal(raw, &s); err != nil {
		return StorageSettings{}, fmt.Errorf("parse storage settings %s: %w", path, err)
	}
	return s, nil
}

// SaveStorageSettings records a decision atomically (temp file + rename), so a
// crash mid-write cannot leave a truncated file that the loader above would
// then refuse — which would take the operator's storage mode with it.
func SaveStorageSettings(path string, accessControlEnabled bool, source string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("storage settings path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir storage settings dir: %w", err)
	}
	data, err := json.MarshalIndent(StorageSettings{AccessControlEnabled: &accessControlEnabled, Source: source}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal storage settings: %w", err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write storage settings temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename storage settings: %w", err)
	}
	return nil
}

// ncStorageModeState is the process-wide resolved storage mode.
//
// It is a package-level singleton for exactly the reason ncAccessSubstrate and
// provisionMu are: the preflight that resolves it runs on the AppAPI enabled
// callback, which has no Runtime in scope, and the two readers that matter —
// the publish sink and the read proxy — are built from an ExAppConfig, not from
// the Runtime either.
type ncStorageModeState struct {
	mu sync.RWMutex
	// path is where a transition persists a new decision. Set once at startup.
	path string
	// resolved is false until something has actually decided. Everything that
	// branches on the mode must treat "not resolved" as "keep doing what the
	// access-controlled model does", because that is the branch that fails
	// closed: serving as the owner on an unresolved mode would hand every
	// account the whole archive for the window between a restart and the next
	// enable edge.
	resolved             bool
	accessControlEnabled bool
	source               string
}

var ncStorage ncStorageModeState

func (s *ncStorageModeState) setPath(path string) {
	s.mu.Lock()
	s.path = path
	s.mu.Unlock()
}

func (s *ncStorageModeState) settingsPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

// set records the mode this process is operating under. source is
// storageModeSourceConfigured or storageModeSourceDerived.
func (s *ncStorageModeState) set(accessControlEnabled bool, source string) {
	s.mu.Lock()
	s.resolved = true
	s.accessControlEnabled = accessControlEnabled
	s.source = source
	s.mu.Unlock()
}

// mode returns the resolved mode. resolved is false when nothing has decided
// yet; a caller that ignores it and reads accessControlled alone gets `false`,
// which is the open direction — so the second value is not optional.
func (s *ncStorageModeState) mode() (accessControlled bool, resolved bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accessControlEnabled, s.resolved
}

// accessControlled is the form for callers that already know the mode is
// resolved — everything downstream of the substrate gate, which cannot report
// `provisioned` before the preflight ran. It answers true for an unresolved
// mode so a caller that is wrong about that fails closed.
func (s *ncStorageModeState) accessControlled() bool {
	accessControlled, resolved := s.mode()
	return accessControlled || !resolved
}

// snapshot renders the mode for /status and /storage.
func (s *ncStorageModeState) snapshot() (mode, source string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.resolved {
		return "", ""
	}
	return storageModeName(s.accessControlEnabled), s.source
}

// reset returns the record to its zero state. Only tests need it; the fields
// are cleared individually rather than by assigning a zero struct, which would
// zero the mutex it holds.
func (s *ncStorageModeState) reset() {
	s.mu.Lock()
	s.path = ""
	s.resolved = false
	s.accessControlEnabled = false
	s.source = ""
	s.mu.Unlock()
}
