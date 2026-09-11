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
// │  {"access_control_enabled": true|false,               │
// │   "source": "user"|"env"|…,                           │
// │   "migration_clean": bool}          (or absent)       │
// └───────────────────────────┬───────────────────────────┘
//
//	│ read on the AppAPI enabled edge
//	▼
//
// ┌──────────────────── preflight ────────────────────────┐
// │  probe Nextcloud (read only), BOTH modes, BOTH roots   │
// │  resolve the mode WITHOUT looking at the probe:        │
// │      file ─▶ env ─▶ UNDECIDED                          │
// │  sanity-check the resolved mode AGAINST the probe      │
// │  refuse unless somebody CHOSE it                       │
// └───────────────────────────┬───────────────────────────┘
//
// The probe never decides the mode, only whether the decided mode is usable.
// Cassini used to infer it — "this instance has a Team folder, so it must want
// access control" — and that made who can read the archive a function of what
// Nextcloud happened to look like at one instant, which on a stack still being
// assembled is the wrong instant. Now nothing is inferred: an instance whose
// storage does not match its mode is reported as a mismatch and refuses to
// publish, rather than being quietly re-interpreted.
//
// D-708 removed the last thing that decided on its own. There used to be a third
// resolution branch — fall back to the deps-free model, and write THAT down on
// the first healthy enable — which is a quieter version of the same mistake,
// because `default` is the model in which every account can read every
// recording. There is no fallback now, and a recorded mode nobody CHOSE (a
// fallback from an older build, a mode an interrupted first switch wrote) is
// reported as a question rather than acted on. Both states refuse to publish and
// both are ended in the Setup tab.
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

	// storageModeSourceConfigured is the legacy stand-in for "read from disk".
	// Nothing writes it any more: the resolvers carry the RECORDED source
	// through instead, because flattening it here is what made a fallback and an
	// administrator's click indistinguishable on the wire (D-708). Files written
	// by earlier builds do not carry it either — it was only ever an in-memory
	// label — so it survives as the answer for a settings file whose provenance
	// is genuinely unknown.
	storageModeSourceConfigured = "configured"
	// storageModeSourceDefault means nothing said otherwise.
	//
	// Nothing writes this any more either. Until D-708 an install that recorded
	// no mode and declared none fell back to the deps-free model and wrote THAT
	// down, permanently, on its first healthy enable — which made who can read
	// an organisation's meetings a decision Cassini took on its own. There is no
	// fallback now; an undecided install stays undecided until somebody says.
	// The constant remains because installs from those builds carry the value,
	// and it is what marks their mode as unconfirmed.
	storageModeSourceDefault = "default"
	// storageModeSourceDerived is older still: a mode inferred from the shape of
	// the instance. Removed long before D-708 and, like the fallback, kept here
	// only so a file carrying it reads as unconfirmed rather than as a choice.
	storageModeSourceDerived = "derived"
	// storageModeSourceUser means an administrator chose it, by switching modes
	// in the Setup tab.
	storageModeSourceUser = "user"
	// storageModeSourceMigrating marks the mode a switch is carrying an archive
	// OUT of, on an install that had not chosen one.
	//
	// A switch marks the instance dirty before it writes a byte, naming the mode
	// the archive is currently under — that is what makes a crash recoverable.
	// Starting from undecided there is no such mode, and writing the origin down
	// as a CHOICE would confirm a decision nobody took, in the direction they
	// were switching away from. This says what is true instead: the archive is
	// at this model's root, and nobody has chosen anything. It never confirms.
	storageModeSourceMigrating = "migrating"
	// storageModeSourceEnv means the deployment declared it, through
	// envStorageMode. A deploy option is as explicit as a button.
	storageModeSourceEnv = "env"

	// envStorageMode declares the mode a FRESH install starts in, for a
	// DEVELOPMENT OR CI deployment. It is not a production affordance: a
	// production install is asked, in the Setup tab, and nothing else decides
	// (D-708).
	//
	// It seeds the flag and nothing more: once storage_settings.json records a
	// decision, the file is authoritative and changing this variable does not
	// move an archive that already exists.
	//
	// Since D-708 it is honoured only on an instance the declaration actually
	// fits. A declared mode whose prerequisites are missing, or that meets
	// recordings in both roots, or names that exist in both, is refused loudly
	// and NOT written down — because the whole reason a harness declares a mode
	// is that it knows what it built, and a declaration that disagrees with the
	// instance is a bug in the stack rather than an instruction to follow.
	envStorageMode = "CASSINI_STORAGE_MODE"

	storageSettingsFileName = "storage_settings.json"
)

// StorageSettings is the persisted storage-mode decision: one flag, and whether
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
	// Source records HOW the flag got there — user, env, or a build that decided
	// on its own.
	//
	// It is read for exactly one question, and it is NOT "may this mode be
	// reconsidered against the live instance". That is what an earlier version
	// used it for, and it made the file non-authoritative: it could say `default`
	// while the app acted access-controlled, because Nextcloud had changed
	// underneath it. Nothing re-opens a recorded decision.
	//
	// What it decides is whether the mode was CHOSEN — see Confirmed. Until D-708
	// the field was written and displayed and never branched on at all, and both
	// resolvers flattened it to "configured" on the way out, so an administrator's
	// click, a deploy option and the old fallback were indistinguishable
	// downstream. The setup wizard's whole premise is being able to tell them
	// apart.
	//
	// Absent in files written before the field existed. Values written by the
	// removed derivation ("derived") still appear on installs from that build
	// and are simply displayed.
	Source string `json:"source,omitempty"`

	// MigrationClean records whether the LAST migration finished tidying up.
	//
	//	true / absent   settled. The root named by AccessControlEnabled holds the
	//	                archive and the other one holds nothing.
	//	false           a migration is in flight, or one stopped part way. The
	//	                root named by AccessControlEnabled STILL holds a complete
	//	                archive — that is the invariant the whole sequence exists
	//	                to keep — and the OTHER root holds leftovers that nothing
	//	                reads.
	//
	// The pointer is what makes "absent" mean settled. Every file written before
	// this field existed describes an install that is not mid-migration, and
	// reading those as dirty would offer every upgrading instance a cleanup it
	// does not need — one that DELETES from a root, which is not a button to
	// arm on a guess.
	//
	// One flag is enough because the recovery does not depend on which half
	// failed: whatever went wrong, the archive is at the recorded mode's root and
	// the leftovers are at the other one, so "clear the root the mode does not
	// name" finishes every case. See finishMigration.
	MigrationClean *bool `json:"migration_clean,omitempty"`

	// FirstRunAcknowledged records that an administrator has seen the dialog a
	// fresh install shows once — who will be able to read recordings, and the
	// service account that will own them (D-755).
	//
	// It lives here rather than in the browser because the question is about the
	// INSTALL, not about the person looking: a second administrator, or the same
	// one on another machine, must not be asked again. It is a plain bool
	// because absence carries nothing beyond "not yet" — unlike the two pointers
	// above, where a file written before the field existed describes a state
	// that is not the zero value.
	FirstRunAcknowledged bool `json:"first_run_acknowledged,omitempty"`
}

// storageModeFromEnv reads the declared initial mode.
//
// `ok` is false when the variable is unset, which is the ordinary case and
// means "take the default model". `ok` is also false for a value that is set but
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

// Clean reports whether the last migration finished. An absent flag is clean —
// see MigrationClean.
func (s StorageSettings) Clean() bool { return s.MigrationClean == nil || *s.MigrationClean }

// Confirmed reports whether the recorded mode is a DECISION rather than
// something Cassini arrived at on its own (D-708).
//
// This is the field the setup wizard turns on, and the reason `Source` stopped
// being decorative. Until D-708 a recorded mode was a recorded mode: an
// administrator's click, a deploy option and the old `default` fallback all read
// back as `configured`, so nothing could tell an install where somebody chose
// the open model from an install where nobody was asked.
//
//	"user"                an administrator chose it in the Setup tab
//	"env"                 the deployment declared it. A deploy option is as
//	                      explicit as a button — and since D-708 it is only
//	                      honoured on an instance it fits.
//	"default" / "derived" a build that decided on its own. NOT a choice.
//	absent                unknown provenance, from a build predating the field.
//	                      Read as unconfirmed: asking once is cheap, and
//	                      assuming consent is what this whole change removes.
func (s StorageSettings) Confirmed() bool {
	return s.Configured() && storageSourceConfirmed(s.Source)
}

// storageSourceConfirmed is Confirmed's rule on its own, so the resolvers can
// apply it to a source they are carrying rather than to a loaded file.
func storageSourceConfirmed(source string) bool {
	switch source {
	case storageModeSourceUser, storageModeSourceEnv:
		return true
	default:
		return false
	}
}

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
//
// It carries FirstRunAcknowledged forward from whatever is already on disk.
// That flag is about the administrator rather than about the archive, and every
// caller here is writing a step of the mode state machine — a migration that
// un-acknowledged the first-run dialog would put it back in front of somebody
// who has already answered it.
func SaveStorageSettings(path string, accessControlEnabled bool, source string, migrationClean bool) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("storage settings path must not be empty")
	}
	// Best effort: an unreadable file must not stop the mode being written. The
	// mode is the invariant the archive rests on; the acknowledgement is one
	// dialog shown once more.
	existing, _ := LoadStorageSettings(path)
	return writeStorageSettings(path, StorageSettings{
		AccessControlEnabled: &accessControlEnabled,
		Source:               source,
		MigrationClean:       &migrationClean,
		FirstRunAcknowledged: existing.FirstRunAcknowledged,
	})
}

// AcknowledgeStorageFirstRun records that the first-run dialog has been
// answered, leaving the mode record exactly as it is.
//
// Idempotent by construction: it writes `true` over whatever is there, so a
// double-click, a retry of a request whose response was lost, and a second
// administrator all produce the same file.
//
// A file it cannot parse IS an error here, and deliberately not the best-effort
// treatment SaveStorageSettings gives it: overwriting the mode record to record
// a dialog dismissal would trade the decision that governs who can read the
// archive for the one thing it is safe to ask again.
func AcknowledgeStorageFirstRun(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("storage settings path must not be empty")
	}
	settings, err := LoadStorageSettings(path)
	if err != nil {
		return err
	}
	settings.FirstRunAcknowledged = true
	return writeStorageSettings(path, settings)
}

// writeStorageSettings is the atomic write both of the above share.
func writeStorageSettings(path string, settings StorageSettings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir storage settings dir: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
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
	// confirmed is whether a PERSON (or a dev/CI deploy option) chose this mode,
	// as opposed to it being recorded by a build that decided on its own. An
	// unconfirmed mode still governs everything — the archive is where it says —
	// but the Setup tab asks for a decision rather than presenting one.
	confirmed bool
	// clean mirrors StorageSettings.MigrationClean for the readers that must not
	// touch the disk — /status, /storage, and the PUT that decides whether a
	// request for the mode already in force is a no-op or a repair.
	clean bool
	// firstRunAcknowledged mirrors StorageSettings.FirstRunAcknowledged, for the
	// same reason clean is mirrored: /storage answers it on every request and
	// must not read the volume to do it.
	//
	// It is deliberately NOT written by set(): every step of a mode switch calls
	// that, and none of them is evidence about a dialog somebody answered.
	firstRunAcknowledged bool
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

// set records the mode this process is operating under, where it came from, and
// whether the last migration finished tidying up.
//
// `confirmed` is derived from the source rather than passed, so there is one
// rule for "was this chosen" and every caller cannot help but agree with it.
func (s *ncStorageModeState) set(accessControlEnabled bool, source string, clean bool) {
	s.mu.Lock()
	s.resolved = true
	s.accessControlEnabled = accessControlEnabled
	s.source = source
	s.confirmed = storageSourceConfirmed(source)
	s.clean = clean
	s.mu.Unlock()
}

// setFirstRunAcknowledged mirrors the persisted acknowledgement into this
// process. Startup calls it with what the file said; the acknowledge action
// calls it with true once the write has landed.
func (s *ncStorageModeState) setFirstRunAcknowledged(acknowledged bool) {
	s.mu.Lock()
	s.firstRunAcknowledged = acknowledged
	s.mu.Unlock()
}

// acknowledgedFirstRun reports whether the first-run dialog has been answered
// on this install.
func (s *ncStorageModeState) acknowledgedFirstRun() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.firstRunAcknowledged
}

// recordedSource is the provenance of the mode in force, or "" when there is
// none. A switch reads it so that marking the instance dirty preserves what the
// current mode's provenance was, rather than promoting it to a choice.
func (s *ncStorageModeState) recordedSource() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.resolved {
		return ""
	}
	return s.source
}

// confirmedMode reports whether the resolved mode is a decision somebody took.
// False for an unresolved process: nothing has been decided, which is the state
// the setup wizard exists to end.
func (s *ncStorageModeState) confirmedMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolved && s.confirmed
}

// migrationClean reports the recorded cleanup state. It answers `true` for an
// unresolved process: nothing has migrated, so there is nothing to finish, and
// offering a cleanup on the strength of a mode nobody has decided would be a
// DELETE against a root chosen by default.
func (s *ncStorageModeState) migrationClean() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.resolved || s.clean
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
	s.confirmed = false
	s.clean = false
	s.firstRunAcknowledged = false
	s.mu.Unlock()
}
