package cassini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const meetingManifestVersion = "cassini.meeting.v1"

type MeetingBundle struct {
	RootDir      string
	ManifestPath string
}

type MeetingBundleManifest struct {
	Kind         string `json:"kind"`
	Version      string `json:"version"`
	CreatedAtUTC string `json:"created_at_utc"`
	// Title is an optional human-readable meeting name (e.g. the Talk room
	// name the operator resolved at recording time). Packing prefers it over
	// names derived from file paths; empty means "no known name".
	Title string `json:"title,omitempty"`
	// RoomToken and RoomName name the conversation the recording came from —
	// for Talk, the room token and its display name. The operator stamps them
	// alongside Title after the build, and `cassini pack` derives the published
	// room id from them (D-622). Empty means "no known room", which is the
	// normal state for a bundle built from a file rather than a call.
	//
	// The bundle holds the raw token because it is the operator's own working
	// directory — the same exposure as the jobs table it came from. The token
	// goes no further: packing derives, and the artifact carries only the
	// derivation.
	RoomToken        string            `json:"room_token,omitempty"`
	RoomName         string            `json:"room_name,omitempty"`
	State            string            `json:"state,omitempty"`
	Stage            string            `json:"stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	SourceKind       string            `json:"source_kind"`
	SourcePath       string            `json:"source_path"`
	ArtifactManifest string            `json:"artifact_manifest,omitempty"`
	Files            map[string]string `json:"files,omitempty"`
}

type LoadedMeetingBundle struct {
	RootDir   string
	Manifest  MeetingBundleManifest
	FoundPath string
}

func PrepareMeetingBundle(outDir string) (MeetingBundle, error) {
	rootDir, err := filepath.Abs(outDir)
	if err != nil {
		return MeetingBundle{}, fmt.Errorf("resolve output path: %w", err)
	}
	if err := ensureEmptyDir(rootDir); err != nil {
		return MeetingBundle{}, err
	}
	bundle := MeetingBundle{
		RootDir:      rootDir,
		ManifestPath: filepath.Join(rootDir, "cassini.json"),
	}
	if err := writeMeetingManifest(bundle, MeetingBundleManifest{
		State: bundleStatePreparing,
		Stage: "prepare",
	}); err != nil {
		return MeetingBundle{}, err
	}
	return bundle, nil
}

func FinalizeMeetingBundle(bundle MeetingBundle, meta MeetingBundleManifest) error {
	if err := validateReadyMeetingBundleContents(bundle.RootDir); err != nil {
		return fmt.Errorf("validate ready meeting bundle: %w", err)
	}

	meta.State = bundleStateReady
	meta.Stage = "ready"
	meta.Error = ""
	meta.Files = meetingBundleFiles(bundle.RootDir)

	artifactManifest := filepath.Join(bundle.RootDir, "manifest.json")
	if _, err := os.Stat(artifactManifest); err == nil {
		meta.ArtifactManifest = "manifest.json"
	}

	return writeMeetingManifest(bundle, meta)
}

func LoadMeetingBundle(path string) (LoadedMeetingBundle, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadedMeetingBundle{}, false, nil
		}
		return LoadedMeetingBundle{}, false, fmt.Errorf("stat meeting bundle path: %w", err)
	}

	root := path
	if !info.IsDir() {
		if filepath.Base(path) != "cassini.json" {
			return LoadedMeetingBundle{}, false, nil
		}
		root = filepath.Dir(path)
	}

	manifestPath := filepath.Join(root, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadedMeetingBundle{}, false, nil
		}
		return LoadedMeetingBundle{}, false, fmt.Errorf("read meeting bundle manifest %s: %w", manifestPath, err)
	}

	var manifest MeetingBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return LoadedMeetingBundle{}, false, fmt.Errorf("parse meeting bundle manifest %s: %w", manifestPath, err)
	}
	if !strings.EqualFold(manifest.Kind, "meeting") {
		return LoadedMeetingBundle{}, false, nil
	}

	return LoadedMeetingBundle{
		RootDir:   root,
		Manifest:  manifest,
		FoundPath: manifestPath,
	}, true, nil
}

func meetingBundleFiles(rootDir string) map[string]string {
	files := map[string]string{}
	maybeAddMeetingFile(files, rootDir, "audio", "meeting.webm")
	maybeAddMeetingFile(files, rootDir, "transcript", "transcript.words.v1.json")
	maybeAddMeetingFile(files, rootDir, "readable_transcript", "transcript.readable.v1.json")
	maybeAddMeetingFile(files, rootDir, "captions", "captions.vtt")
	maybeAddMeetingFile(files, rootDir, "timeline", "timeline.map.v1.json")
	maybeAddMeetingFile(files, rootDir, "artifact_manifest", "manifest.json")
	return files
}

func maybeAddMeetingFile(files map[string]string, rootDir string, key string, name string) {
	if _, err := os.Stat(filepath.Join(rootDir, name)); err == nil {
		files[key] = name
	}
}

func validateReadyMeetingBundleContents(rootDir string) error {
	requiredFiles := []string{"meeting.webm", "transcript.words.v1.json", "manifest.json"}
	missing := make([]string, 0, len(requiredFiles))
	for _, name := range requiredFiles {
		if _, err := os.Stat(filepath.Join(rootDir, name)); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required files: %s", strings.Join(missing, ", "))
	}

	// The artifact manifest's `files` map carries both scalar paths (audio,
	// transcript, ...) and, in v2 multi-tx mode, a structured `transcripts`
	// array. Decode permissively: validate string entries; recurse into the
	// transcripts array's `path` fields; ignore any other shape.
	type transcriptRef struct {
		Path string `json:"path"`
	}
	manifestPath := filepath.Join(rootDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read artifact manifest: %w", err)
	}
	var envelope struct {
		Files map[string]json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("parse artifact manifest: %w", err)
	}
	for key, raw := range envelope.Files {
		if key == "transcripts" {
			var refs []transcriptRef
			if err := json.Unmarshal(raw, &refs); err != nil {
				return fmt.Errorf("artifact manifest files.transcripts: %w", err)
			}
			for _, ref := range refs {
				path := strings.TrimSpace(ref.Path)
				if path == "" {
					continue
				}
				if _, err := os.Stat(filepath.Join(rootDir, path)); err != nil {
					return fmt.Errorf("artifact manifest declares missing transcripts entry: %s", path)
				}
			}
			continue
		}
		var path string
		if err := json.Unmarshal(raw, &path); err != nil {
			// Non-string, non-transcripts entries are not paths; skip.
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(rootDir, path)); err != nil {
			return fmt.Errorf("artifact manifest declares missing %s file: %s", key, path)
		}
	}
	return nil
}

func UpdateMeetingBundleStatus(bundle MeetingBundle, state string, stage string, errText string) error {
	meta, err := readMeetingManifest(bundle.ManifestPath)
	if err != nil {
		return err
	}
	meta.State = state
	meta.Stage = stage
	meta.Error = strings.TrimSpace(errText)
	return writeMeetingManifest(bundle, meta)
}

func UpdateMeetingBundleSource(bundle MeetingBundle, sourceKind string, sourcePath string, stage string) error {
	meta, err := readMeetingManifest(bundle.ManifestPath)
	if err != nil {
		return err
	}
	meta.SourceKind = sourceKind
	meta.SourcePath = sourcePath
	if stage != "" {
		meta.Stage = stage
	}
	return writeMeetingManifest(bundle, meta)
}

func readMeetingManifest(path string) (MeetingBundleManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return MeetingBundleManifest{}, fmt.Errorf("read meeting manifest: %w", err)
	}
	var manifest MeetingBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return MeetingBundleManifest{}, fmt.Errorf("parse meeting manifest: %w", err)
	}
	return manifest, nil
}

func writeMeetingManifest(bundle MeetingBundle, meta MeetingBundleManifest) error {
	meta.Kind = "meeting"
	meta.Version = meetingManifestVersion
	if strings.TrimSpace(meta.CreatedAtUTC) == "" {
		meta.CreatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	}
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meeting manifest: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomic(bundle.ManifestPath, body, 0o644); err != nil {
		return fmt.Errorf("write meeting manifest: %w", err)
	}
	return nil
}
