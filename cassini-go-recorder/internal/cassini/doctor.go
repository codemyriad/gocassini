package cassini

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"

	"gocassini/internal/modelstore"
	"gocassini/internal/transcribe"
)

const (
	minTempFreeWarn = int64(5 << 30)
	minTempFreeFail = int64(1 << 30)
	minWorkFreeWarn = int64(10 << 30)
	minWorkFreeFail = int64(2 << 30)
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorCheck struct {
	status  doctorStatus
	summary string
	advice  string
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := "all"
	fs.StringVar(&target, "target", "all", "check set: all, record, build")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini doctor
  cassini doctor --target record
  cassini doctor --target build

`+"\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "doctor does not accept positional arguments: %v\n", fs.Args())
		return 2
	}

	checks := collectDoctorChecks(target)
	hasFail := false
	hasWarn := false
	for _, check := range checks {
		fmt.Fprintf(stdout, "%s %s\n", check.status, check.summary)
		if check.advice != "" {
			fmt.Fprintf(stdout, "fix %s\n", check.advice)
		}
		if check.status == doctorFail {
			hasFail = true
		}
		if check.status == doctorWarn {
			hasWarn = true
		}
	}
	switch {
	case hasFail:
		fmt.Fprintln(stdout, "result fail")
		return 1
	case hasWarn:
		fmt.Fprintln(stdout, "result warn")
		return 0
	default:
		fmt.Fprintln(stdout, "result ok")
		return 0
	}
}

func collectDoctorChecks(target string) []doctorCheck {
	if target != "all" && target != "record" && target != "build" && target != "media" {
		return []doctorCheck{{status: doctorFail, summary: fmt.Sprintf("unsupported doctor target %q", target)}}
	}

	checks := []doctorCheck{}
	if wd, err := os.Getwd(); err != nil {
		checks = append(checks, doctorCheck{status: doctorFail, summary: fmt.Sprintf("working directory unavailable: %v", err)})
	} else {
		checks = append(checks, doctorCheck{
			status:  doctorOK,
			summary: fmt.Sprintf("working directory %s", wd),
		})
		checks = append(checks, writableDirCheck(wd, "working directory"))
		checks = append(checks, freeSpaceCheck(wd, "working directory", minWorkFreeWarn, minWorkFreeFail))
	}

	tmp := os.TempDir()
	checks = append(checks, writableDirCheck(tmp, "temporary directory"))
	checks = append(checks, freeSpaceCheck(tmp, "temporary directory", minTempFreeWarn, minTempFreeFail))

	if target == "all" || target == "build" || target == "media" {
		checks = append(checks, commandCheck("ffmpeg"))
		checks = append(checks, commandCheck("ffprobe"))
	}
	if (target == "all" || target == "build") && transcribe.DefaultBuildConfig().TranscriptionMode == "on" {
		device := transcribe.ResolveDevice(os.Getenv("CASSINI_STT_DEVICE"))
		modelID := transcribe.ResolveModelID(os.Getenv("CASSINI_STT_MODEL"), os.Getenv("CASSINI_STT_QUALITY"), device)
		checks = append(checks, nativeRuntimeCheck(modelID))
		checks = append(checks, sttModelCacheChecks()...)
	}

	return checks
}

// Diagnostic markers reported in doctor summary for speech engine runtime.
// These strings form the contract with external callers (e.g. cassini-operator's
// /status probe).
const (
	SpeechEngineRefActiveMarker   = "reference frontend active"
	SpeechEngineRefInactiveMarker = "reference frontend optimization inactive"
)

func nativeRuntimeCheck(modelID transcribe.ModelID) doctorCheck {
	return nativeRuntimeCheckWithState(modelID, transcribe.RuntimeVersion(), transcribe.HasReferenceRuntime())
}

func nativeRuntimeCheckWithState(modelID transcribe.ModelID, ver string, hasRef bool) doctorCheck {
	if !transcribe.UsesParakeetV3ReferencePolicy(modelID) {
		return doctorCheck{
			status:  doctorOK,
			summary: fmt.Sprintf("speech engine runtime %s", ver),
		}
	}
	if hasRef {
		return doctorCheck{
			status:  doctorOK,
			summary: fmt.Sprintf("speech engine runtime %s (%s)", ver, SpeechEngineRefActiveMarker),
		}
	}
	return doctorCheck{
		status:  doctorWarn,
		summary: fmt.Sprintf("speech engine runtime %s (%s; falling back to standard decode profile)", ver, SpeechEngineRefInactiveMarker),
		advice:  "verify that Go module replace directives for github.com/codemyriad/sherpa-onnx-go are intact and no unpatched libsherpa-onnx-c-api.so is shadowing on LD_LIBRARY_PATH",
	}
}

func writableDirCheck(path string, label string) doctorCheck {
	info, err := os.Stat(path)
	if err != nil {
		return doctorCheck{
			status:  doctorFail,
			summary: fmt.Sprintf("%s not accessible: %v", label, err),
			advice:  fmt.Sprintf("create %s or choose a path you can write to", path),
		}
	}
	if !info.IsDir() {
		return doctorCheck{
			status:  doctorFail,
			summary: fmt.Sprintf("%s is not a directory: %s", label, path),
			advice:  fmt.Sprintf("replace %s with a writable directory", path),
		}
	}
	probePath := filepath.Join(path, ".cassini-doctor-write-test")
	if err := os.WriteFile(probePath, []byte("ok\n"), 0o644); err != nil {
		return doctorCheck{
			status:  doctorFail,
			summary: fmt.Sprintf("%s is not writable: %s (%v)", label, path, err),
			advice:  writableDirAdvice(label, path),
		}
	}
	_ = os.Remove(probePath)
	return doctorCheck{status: doctorOK, summary: fmt.Sprintf("%s writable: %s", label, path)}
}

func freeSpaceCheck(path string, label string, warnBytes int64, failBytes int64) doctorCheck {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return doctorCheck{status: doctorFail, summary: fmt.Sprintf("could not read free space for %s: %v", path, err)}
	}
	freeBytes := int64(stat.Bavail) * int64(stat.Bsize)
	summary := fmt.Sprintf("%s free space: %s available at %s", label, formatBytes(freeBytes), path)
	switch {
	case freeBytes < failBytes:
		return doctorCheck{status: doctorFail, summary: summary, advice: fmt.Sprintf("free space in %s before running Cassini again", path)}
	case freeBytes < warnBytes:
		return doctorCheck{status: doctorWarn, summary: summary, advice: fmt.Sprintf("consider freeing space in %s to avoid mid-run failures", path)}
	default:
		return doctorCheck{status: doctorOK, summary: summary}
	}
}

func commandCheck(name string) doctorCheck {
	path, err := exec.LookPath(name)
	if err != nil {
		return doctorCheck{
			status:  doctorFail,
			summary: fmt.Sprintf("%s not found in PATH", name),
			advice:  fmt.Sprintf("install %s and ensure it is available on PATH", name),
		}
	}
	return doctorCheck{status: doctorOK, summary: fmt.Sprintf("%s available at %s", name, path)}
}

func sttModelCacheChecks() []doctorCheck {
	cacheRoot := defaultCassiniCacheRoot()
	// Report the *resolved* device+model the build will actually use, so doctor
	// reflects GPU auto-detection and the quality-tier model choice.
	device := transcribe.ResolveDevice(os.Getenv("CASSINI_STT_DEVICE"))
	modelID := transcribe.ResolveModelID(os.Getenv("CASSINI_STT_MODEL"), os.Getenv("CASSINI_STT_QUALITY"), device)

	checks := []doctorCheck{
		{status: doctorOK, summary: fmt.Sprintf("STT model id: %s", modelID)},
		{status: doctorOK, summary: fmt.Sprintf("STT device: %s (quality=%s)", device, transcribe.NormalizeQuality(os.Getenv("CASSINI_STT_QUALITY")))},
		parentWritableCheck(cacheRoot, "cassini cache root"),
	}

	s := modelstore.New(cacheRoot)
	m, err := s.Catalogue.Model(string(modelID), os.Getenv("CASSINI_STT_REVISION"))
	if err == nil {
		err = s.Complete(m)
	}
	if err != nil {
		checks = append(checks, doctorCheck{status: doctorWarn, summary: fmt.Sprintf("Transcription unavailable: %v; meetings retain audio", err), advice: "Install a model in Settings or use cassini models import; builds never download models"})
	} else if !s.Ready(m, device, transcribe.ModelRuntimeFingerprint()) {
		checks = append(checks, doctorCheck{status: doctorWarn, summary: "Model installed; runtime check needed", advice: "Run cassini models probe " + m.ID + " --device " + device})
	} else {
		checks = append(checks, doctorCheck{status: doctorOK, summary: "Pinned model and VAD installed and ready"})
	}
	return checks
}

func defaultSTTDevice() string {
	if v := os.Getenv("CASSINI_STT_DEVICE"); v != "" {
		return v
	}
	return "cpu"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func parentWritableCheck(path string, label string) doctorCheck {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return doctorCheck{status: doctorFail, summary: fmt.Sprintf("%s is not a directory: %s", label, path)}
		}
		return writableDirCheck(path, label)
	}

	parent := filepath.Dir(path)
	for parent != "" && parent != "." {
		if info, err := os.Stat(parent); err == nil && info.IsDir() {
			return writableDirCheck(parent, label+" parent")
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return doctorCheck{status: doctorFail, summary: fmt.Sprintf("%s parent path not accessible: %s", label, path)}
}

func writableDirAdvice(label string, path string) string {
	switch label {
	case "working directory":
		return "change into a writable working directory or fix directory permissions before running Cassini"
	case "temporary directory":
		return "free space or permissions in the system temp directory, or run with TMPDIR set to a writable location"
	case "cassini cache root", "STT model cache", "STT model cache parent":
		return fmt.Sprintf("make %s writable or set CASSINI_CACHE_ROOT to a writable cache directory", path)
	default:
		return fmt.Sprintf("make %s writable or choose a different writable path", path)
	}
}

func defaultCassiniCacheRoot() string {
	if value := os.Getenv("CASSINI_CACHE_ROOT"); value != "" {
		return value
	}
	currentUser, err := user.Current()
	if err == nil && currentUser.HomeDir != "" {
		return filepath.Join(currentUser.HomeDir, ".cache", "cassini")
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".cache", "cassini")
	}
	return filepath.Join(".", ".cache", "cassini")
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}
