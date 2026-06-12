package cassini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const runManifestVersion = "cassini.run.v1"

type RunBundle struct {
	RootDir       string
	ManifestPath  string
	RecordingPath string
	Simulate      bool
}

type RunManifest struct {
	Kind         string           `json:"kind"`
	Version      string           `json:"version"`
	CreatedAtUTC string           `json:"created_at_utc"`
	State        string           `json:"state,omitempty"`
	Stage        string           `json:"stage,omitempty"`
	Error        string           `json:"error,omitempty"`
	SourceMode   string           `json:"source_mode"`
	RecorderName string           `json:"recorder_name,omitempty"`
	Recording    RunRecordingFile `json:"recording"`
	Session      *RunSessionDir   `json:"session,omitempty"`
}

type LoadedRunBundle struct {
	RootDir   string
	Manifest  RunManifest
	FoundPath string
}

type RunRecordingFile struct {
	Path   string `json:"path"`
	Format string `json:"format"`
}

type RunSessionDir struct {
	Path string `json:"path"`
}

func PrepareRunBundle(outDir string, simulate bool) (RunBundle, error) {
	rootDir, err := filepath.Abs(outDir)
	if err != nil {
		return RunBundle{}, fmt.Errorf("resolve output path: %w", err)
	}
	if err := ensureEmptyDir(rootDir); err != nil {
		return RunBundle{}, err
	}

	recordingName := "recording.mkv"
	if simulate {
		recordingName = "recording.csr"
	}

	bundle := RunBundle{
		RootDir:       rootDir,
		ManifestPath:  filepath.Join(rootDir, "cassini.json"),
		RecordingPath: filepath.Join(rootDir, recordingName),
		Simulate:      simulate,
	}
	if err := writeRunManifest(bundle, RunManifest{
		State: bundleStatePreparing,
		Stage: "prepare",
	}); err != nil {
		return RunBundle{}, err
	}
	return bundle, nil
}

func FinalizeRunBundle(bundle RunBundle, meta RunManifest) error {
	meta.State = bundleStateReady
	meta.Stage = "ready"
	meta.Error = ""
	meta.Recording.Path = filepath.Base(bundle.RecordingPath)
	if bundle.Simulate {
		meta.Recording.Format = "csr"
	} else {
		meta.Recording.Format = "mkv"
		sessionPath, err := normalizeSessionDir(bundle.RootDir)
		if err != nil {
			return err
		}
		if sessionPath != "" {
			meta.Session = &RunSessionDir{Path: filepath.Base(sessionPath)}
		}
	}

	return writeRunManifest(bundle, meta)
}

func LoadRunBundle(path string) (LoadedRunBundle, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadedRunBundle{}, false, nil
		}
		return LoadedRunBundle{}, false, fmt.Errorf("stat run bundle path: %w", err)
	}

	root := path
	if !info.IsDir() {
		if filepath.Base(path) != "cassini.json" {
			return LoadedRunBundle{}, false, nil
		}
		root = filepath.Dir(path)
	}

	manifestPath := filepath.Join(root, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadedRunBundle{}, false, nil
		}
		return LoadedRunBundle{}, false, fmt.Errorf("read run bundle manifest: %w", err)
	}

	var manifest RunManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return LoadedRunBundle{}, false, fmt.Errorf("parse run bundle manifest: %w", err)
	}
	if !strings.EqualFold(manifest.Kind, "run") {
		return LoadedRunBundle{}, false, nil
	}

	return LoadedRunBundle{
		RootDir:   root,
		Manifest:  manifest,
		FoundPath: manifestPath,
	}, true, nil
}

func ensureEmptyDir(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("output path exists and is not a directory: %s", path)
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return fmt.Errorf("read output directory: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("output directory is not empty: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat output path: %w", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	return nil
}

func UpdateRunBundleStatus(bundle RunBundle, state string, stage string, errText string) error {
	meta, err := readRunManifest(bundle.ManifestPath)
	if err != nil {
		return err
	}
	meta.State = state
	meta.Stage = stage
	meta.Error = strings.TrimSpace(errText)
	return writeRunManifest(bundle, meta)
}

func readRunManifest(path string) (RunManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RunManifest{}, fmt.Errorf("read run manifest: %w", err)
	}
	var manifest RunManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return RunManifest{}, fmt.Errorf("parse run manifest: %w", err)
	}
	return manifest, nil
}

func writeRunManifest(bundle RunBundle, meta RunManifest) error {
	meta.Kind = "run"
	meta.Version = runManifestVersion
	if strings.TrimSpace(meta.CreatedAtUTC) == "" {
		meta.CreatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	}
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run manifest: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomic(bundle.ManifestPath, body, 0o644); err != nil {
		return fmt.Errorf("write run manifest: %w", err)
	}
	return nil
}

func normalizeSessionDir(rootDir string) (string, error) {
	sessionsRoot := filepath.Join(rootDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read sessions directory: %w", err)
	}

	sessionDirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			sessionDirs = append(sessionDirs, entry.Name())
		}
	}
	sort.Strings(sessionDirs)
	if len(sessionDirs) == 0 {
		return "", nil
	}
	if len(sessionDirs) != 1 {
		return "", fmt.Errorf("expected exactly one session artifact directory in %s, found %d", sessionsRoot, len(sessionDirs))
	}

	source := filepath.Join(sessionsRoot, sessionDirs[0])
	target := filepath.Join(rootDir, "session")
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("run bundle already contains %s", target)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat session target: %w", err)
	}

	if err := os.Rename(source, target); err != nil {
		return "", fmt.Errorf("move session artifact into bundle: %w", err)
	}
	if err := os.Remove(sessionsRoot); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove empty sessions directory: %w", err)
	}
	return target, nil
}
