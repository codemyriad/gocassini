package cassini

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	inspectpkg "gocassini/internal/inspect"
)

type portableWorkspace struct {
	RootDir    string
	RunDir     string
	MeetingDir string
}

func ensurePortableWorkspace(outPath string) (portableWorkspace, error) {
	resolvedOut, err := preparePortableMeetingOutput(outPath)
	if err != nil {
		return portableWorkspace{}, err
	}
	rootDir, err := createPortableWorkRoot(resolvedOut)
	if err != nil {
		return portableWorkspace{}, err
	}
	return portableWorkspace{
		RootDir:    rootDir,
		RunDir:     filepath.Join(rootDir, "capture.run"),
		MeetingDir: filepath.Join(rootDir, "meeting.meeting"),
	}, nil
}

func portableMeetingAlreadyReady(path string) (bool, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolve portable meeting path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat portable meeting path: %w", err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("portable meeting output is a directory: %s", resolved)
	}

	var out bytes.Buffer
	if err := inspectpkg.InspectPath(&out, resolved); err != nil {
		return false, nil
	}
	return strings.Contains(out.String(), "cassini=ok"), nil
}

func resetPortableWorkspaceDir(path string) error {
	if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reset workspace directory %s: %w", path, err)
	}
	return nil
}

func printPortableResumeHint(w io.Writer, workRoot string, outPath string) {
	fmt.Fprintf(w, "work_dir -> %s\n", workRoot)
	fmt.Fprintf(w, "resume -> rerun the same cassini command with --out %q after fixing the issue\n", outPath)
}

func reusableRunBundle(runDir string, recorderName string, stdout io.Writer) (RunBundle, bool, error) {
	if loaded, ok, err := LoadRunBundle(runDir); err != nil {
		return RunBundle{}, false, err
	} else if ok {
		bundle := runBundleFromLoaded(loaded)
		if runBundleRecordingExists(bundle) && bundleIsReady(loaded.Manifest.State) {
			fmt.Fprintf(stdout, "resume reusing recorded meeting at %s\n", bundle.RootDir)
			return bundle, true, nil
		}
	}

	if bundle, ok, err := adoptRunBundle(runDir, recorderName); err != nil {
		return RunBundle{}, false, err
	} else if ok {
		fmt.Fprintf(stdout, "resume finalized existing recording at %s\n", bundle.RootDir)
		return bundle, true, nil
	}

	if err := resetPortableWorkspaceDir(runDir); err != nil {
		return RunBundle{}, false, err
	}
	bundle, err := PrepareRunBundle(runDir, false)
	return bundle, false, err
}

func reusableMeetingBundle(meetingDir string, input buildInput, stdout io.Writer) (MeetingBundle, bool, error) {
	if loaded, ok, err := LoadMeetingBundle(meetingDir); err != nil {
		return MeetingBundle{}, false, err
	} else if ok {
		bundle := MeetingBundle{
			RootDir:      loaded.RootDir,
			ManifestPath: loaded.FoundPath,
		}
		if bundleIsReady(loaded.Manifest.State) && meetingBundleMatchesInput(loaded.Manifest, input) && meetingBundleHasPortableFiles(loaded.RootDir) {
			fmt.Fprintf(stdout, "resume reusing built meeting artifact at %s\n", loaded.RootDir)
			return bundle, true, nil
		}
	}

	if bundle, ok, err := adoptMeetingBundle(meetingDir, input); err != nil {
		return MeetingBundle{}, false, err
	} else if ok {
		fmt.Fprintf(stdout, "resume finalized existing meeting artifact at %s\n", bundle.RootDir)
		return bundle, true, nil
	}

	if err := resetPortableWorkspaceDir(meetingDir); err != nil {
		return MeetingBundle{}, false, err
	}
	bundle, err := PrepareMeetingBundle(meetingDir)
	return bundle, false, err
}

func runBundleFromLoaded(loaded LoadedRunBundle) RunBundle {
	recordingPath := filepath.Join(loaded.RootDir, "recording.mkv")
	if name := strings.TrimSpace(loaded.Manifest.Recording.Path); name != "" {
		recordingPath = filepath.Join(loaded.RootDir, name)
	}
	return RunBundle{
		RootDir:       loaded.RootDir,
		ManifestPath:  loaded.FoundPath,
		RecordingPath: recordingPath,
		Simulate:      strings.EqualFold(strings.TrimSpace(loaded.Manifest.SourceMode), "simulate"),
	}
}

func runBundleRecordingExists(bundle RunBundle) bool {
	if strings.TrimSpace(bundle.RecordingPath) == "" {
		return false
	}
	_, err := os.Stat(bundle.RecordingPath)
	return err == nil
}

func adoptRunBundle(runDir string, recorderName string) (RunBundle, bool, error) {
	rootDir, err := filepath.Abs(runDir)
	if err != nil {
		return RunBundle{}, false, fmt.Errorf("resolve run bundle path: %w", err)
	}

	meta := RunManifest{
		SourceMode:   "talk",
		RecorderName: recorderName,
	}
	recordingName := "recording.mkv"

	if loaded, ok, err := LoadRunBundle(rootDir); err != nil {
		return RunBundle{}, false, err
	} else if ok {
		meta = loaded.Manifest
		if name := strings.TrimSpace(loaded.Manifest.Recording.Path); name != "" {
			recordingName = name
		}
	}

	recordingPath := filepath.Join(rootDir, recordingName)
	if _, err := os.Stat(recordingPath); err != nil {
		return RunBundle{}, false, nil
	}

	bundle := RunBundle{
		RootDir:       rootDir,
		ManifestPath:  filepath.Join(rootDir, "cassini.json"),
		RecordingPath: recordingPath,
	}
	if err := FinalizeRunBundle(bundle, meta); err != nil {
		return RunBundle{}, false, err
	}
	return bundle, true, nil
}

func adoptMeetingBundle(meetingDir string, input buildInput) (MeetingBundle, bool, error) {
	rootDir, err := filepath.Abs(meetingDir)
	if err != nil {
		return MeetingBundle{}, false, fmt.Errorf("resolve meeting bundle path: %w", err)
	}
	if !meetingBundleHasPortableFiles(rootDir) {
		return MeetingBundle{}, false, nil
	}

	meta := MeetingBundleManifest{
		SourceKind: input.SourceKind,
		SourcePath: input.SourcePath,
	}
	if loaded, ok, err := LoadMeetingBundle(rootDir); err != nil {
		return MeetingBundle{}, false, err
	} else if ok {
		meta = loaded.Manifest
		if !meetingBundleMatchesInput(meta, input) {
			return MeetingBundle{}, false, nil
		}
	}

	bundle := MeetingBundle{
		RootDir:      rootDir,
		ManifestPath: filepath.Join(rootDir, "cassini.json"),
	}
	if err := FinalizeMeetingBundle(bundle, meta); err != nil {
		return MeetingBundle{}, false, err
	}
	return bundle, true, nil
}

func meetingBundleMatchesInput(meta MeetingBundleManifest, input buildInput) bool {
	return strings.TrimSpace(meta.SourceKind) == strings.TrimSpace(input.SourceKind) &&
		filepath.Clean(strings.TrimSpace(meta.SourcePath)) == filepath.Clean(strings.TrimSpace(input.SourcePath))
}

func meetingBundleHasPortableFiles(rootDir string) bool {
	required := []string{
		filepath.Join(rootDir, "meeting.webm"),
		filepath.Join(rootDir, "transcript.words.v1.json"),
		filepath.Join(rootDir, "manifest.json"),
	}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
