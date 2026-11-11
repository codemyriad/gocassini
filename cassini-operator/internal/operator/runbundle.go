package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	runManifestVersion   = "cassini.run.v1"
	bundleStatePreparing = "preparing"
	bundleStateReady     = "ready"
	bundleStateFailed    = "failed"
)

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
	if err := writeRunManifest(bundle, RunManifest{State: bundleStatePreparing, Stage: "prepare"}); err != nil {
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
	}
	return writeRunManifest(bundle, meta)
}

func UpdateRunBundleStatus(bundle RunBundle, state, stage, errText string) error {
	meta, err := readRunManifest(bundle.ManifestPath)
	if err != nil {
		return err
	}
	meta.State = state
	meta.Stage = stage
	meta.Error = strings.TrimSpace(errText)
	return writeRunManifest(bundle, meta)
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
	if err := os.WriteFile(bundle.ManifestPath, body, 0o644); err != nil {
		return fmt.Errorf("write run manifest: %w", err)
	}
	return nil
}
