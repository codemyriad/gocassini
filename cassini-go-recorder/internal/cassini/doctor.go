package cassini

import (
	"encoding/json"
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

// DoctorCheck is one thing doctor looked at.
//
// Exported, with JSON tags, because the operator consumes these (D-798). It did
// so already, by substring-matching the summary against a marker constant
// duplicated in both modules — which made a human sentence load-bearing across
// a boundary nothing enforces. An id is what a caller should key on instead.
type DoctorCheck struct {
	// ID is stable and survives rewording. It is the key a caller matches on,
	// a checklist row is identified by, and a repair step is attached to.
	ID string `json:"id"`
	// Status is ok / warn / fail.
	Status doctorStatus `json:"status"`
	// Summary is for a person to read, and may be reworded freely — that is
	// precisely why nothing may depend on its text.
	Summary string `json:"summary"`
	// Advice is what to do about it, empty when there is nothing to do.
	Advice string `json:"advice,omitempty"`
}

// doctorCheck is the internal alias the collectors build. Kept short because it
// appears at every construction site.
type doctorCheck = DoctorCheck

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cassini doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := "all"
	asJSON := false
	fs.StringVar(&target, "target", "all", "check set: all, record, build, media")
	fs.BoolVar(&asJSON, "json", false, "emit the checks as a JSON array instead of text")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini doctor
  cassini doctor --target record
  cassini doctor --target build
  cassini doctor --target media
  cassini doctor --json

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
	// The JSON document is for a caller; the text is for a person, and it is a
	// shipped contract (standalone `cassini doctor` must keep its text format).
	// So --json REPLACES the text rather than
	// decorating it, and the exit code is identical either way.
	if asJSON {
		out, err := json.Marshal(checks)
		if err != nil {
			fmt.Fprintf(stderr, "encode doctor checks: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "%s\n", out)
	}
	hasFail := false
	hasWarn := false
	for _, check := range checks {
		if !asJSON {
			fmt.Fprintf(stdout, "%s %s\n", check.Status, check.Summary)
			if check.Advice != "" {
				fmt.Fprintf(stdout, "fix %s\n", check.Advice)
			}
		}
		if check.Status == doctorFail {
			hasFail = true
		}
		if check.Status == doctorWarn {
			hasWarn = true
		}
	}
	// The verdict line belongs to the text rendering; under --json the document
	// must be the whole of stdout so a caller can unmarshal it without
	// stripping a trailer. The EXIT CODE is unaffected either way, which is what
	// the /healthz record probe depends on (record_runtime.go).
	switch {
	case hasFail:
		if !asJSON {
			fmt.Fprintln(stdout, "result fail")
		}
		return 1
	case hasWarn:
		if !asJSON {
			fmt.Fprintln(stdout, "result warn")
		}
		return 0
	default:
		if !asJSON {
			fmt.Fprintln(stdout, "result ok")
		}
		return 0
	}
}

func collectDoctorChecks(target string) []doctorCheck {
	if target != "all" && target != "record" && target != "build" && target != "media" {
		return []doctorCheck{{ID: "target", Status: doctorFail, Summary: fmt.Sprintf("unsupported doctor target %q", target)}}
	}

	checks := []doctorCheck{}
	if wd, err := os.Getwd(); err != nil {
		checks = append(checks, doctorCheck{ID: "workdir", Status: doctorFail, Summary: fmt.Sprintf("working directory unavailable: %v", err)})
	} else {
		checks = append(checks, doctorCheck{
			ID:      "workdir",
			Status:  doctorOK,
			Summary: fmt.Sprintf("working directory %s", wd),
		})
		checks = append(checks, writableDirCheck("workdir.writable", wd, "working directory"))
		checks = append(checks, freeSpaceCheck("workdir.space", wd, "working directory", minWorkFreeWarn, minWorkFreeFail))
	}

	tmp := os.TempDir()
	checks = append(checks, writableDirCheck("tmpdir.writable", tmp, "temporary directory"))
	checks = append(checks, freeSpaceCheck("tmpdir.space", tmp, "temporary directory", minTempFreeWarn, minTempFreeFail))

	if target == "all" || target == "build" || target == "media" {
		checks = append(checks, commandCheck("ffmpeg", "ffmpeg"))
		checks = append(checks, commandCheck("ffprobe", "ffprobe"))
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
//
// Prose for a person to read, NOT a contract. The operator's /status probe used
// to substring-match these across a module boundary, against its own second
// copy of the strings that nothing kept in agreement; it now reads the
// speech.runtime check by id, so these are free to be reworded (D-798). They
// stay named so the two summaries below phrase the same fact the same way —
// not because anything parses them.
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
			ID:      "speech.runtime",
			Status:  doctorOK,
			Summary: fmt.Sprintf("speech engine runtime %s", ver),
		}
	}
	if hasRef {
		return doctorCheck{
			ID:      "speech.runtime",
			Status:  doctorOK,
			Summary: fmt.Sprintf("speech engine runtime %s (%s)", ver, SpeechEngineRefActiveMarker),
		}
	}
	return doctorCheck{
		ID:      "speech.runtime",
		Status:  doctorWarn,
		Summary: fmt.Sprintf("speech engine runtime %s (%s; falling back to standard decode profile)", ver, SpeechEngineRefInactiveMarker),
		Advice:  "verify that Go module replace directives for github.com/codemyriad/sherpa-onnx-go are intact and no unpatched libsherpa-onnx-c-api.so is shadowing on LD_LIBRARY_PATH",
	}
}

func writableDirCheck(id string, path string, label string) doctorCheck {
	info, err := os.Stat(path)
	if err != nil {
		return doctorCheck{
			ID:      id,
			Status:  doctorFail,
			Summary: fmt.Sprintf("%s not accessible: %v", label, err),
			Advice:  fmt.Sprintf("create %s or choose a path you can write to", path),
		}
	}
	if !info.IsDir() {
		return doctorCheck{
			ID:      id,
			Status:  doctorFail,
			Summary: fmt.Sprintf("%s is not a directory: %s", label, path),
			Advice:  fmt.Sprintf("replace %s with a writable directory", path),
		}
	}
	probePath := filepath.Join(path, ".cassini-doctor-write-test")
	if err := os.WriteFile(probePath, []byte("ok\n"), 0o644); err != nil {
		return doctorCheck{
			ID:      id,
			Status:  doctorFail,
			Summary: fmt.Sprintf("%s is not writable: %s (%v)", label, path, err),
			Advice:  writableDirAdvice(label, path),
		}
	}
	_ = os.Remove(probePath)
	return doctorCheck{ID: id, Status: doctorOK, Summary: fmt.Sprintf("%s writable: %s", label, path)}
}

func freeSpaceCheck(id string, path string, label string, warnBytes int64, failBytes int64) doctorCheck {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return doctorCheck{ID: id, Status: doctorFail, Summary: fmt.Sprintf("could not read free space for %s: %v", path, err)}
	}
	freeBytes := int64(stat.Bavail) * int64(stat.Bsize)
	summary := fmt.Sprintf("%s free space: %s available at %s", label, formatBytes(freeBytes), path)
	switch {
	case freeBytes < failBytes:
		return doctorCheck{ID: id, Status: doctorFail, Summary: summary, Advice: fmt.Sprintf("free space in %s before running Cassini again", path)}
	case freeBytes < warnBytes:
		return doctorCheck{ID: id, Status: doctorWarn, Summary: summary, Advice: fmt.Sprintf("consider freeing space in %s to avoid mid-run failures", path)}
	default:
		return doctorCheck{ID: id, Status: doctorOK, Summary: summary}
	}
}

func commandCheck(id string, name string) doctorCheck {
	path, err := exec.LookPath(name)
	if err != nil {
		return doctorCheck{
			ID:      id,
			Status:  doctorFail,
			Summary: fmt.Sprintf("%s not found in PATH", name),
			Advice:  fmt.Sprintf("install %s and ensure it is available on PATH", name),
		}
	}
	return doctorCheck{ID: id, Status: doctorOK, Summary: fmt.Sprintf("%s available at %s", name, path)}
}

func sttModelCacheChecks() []doctorCheck {
	cacheRoot := defaultCassiniCacheRoot()
	// Report the *resolved* device+model the build will actually use, so doctor
	// reflects GPU auto-detection and the quality-tier model choice.
	device := transcribe.ResolveDevice(os.Getenv("CASSINI_STT_DEVICE"))
	modelID := transcribe.ResolveModelID(os.Getenv("CASSINI_STT_MODEL"), os.Getenv("CASSINI_STT_QUALITY"), device)

	checks := []doctorCheck{
		{ID: "model.id", Status: doctorOK, Summary: fmt.Sprintf("STT model id: %s", modelID)},
		{ID: "model.device", Status: doctorOK, Summary: fmt.Sprintf("STT device: %s (quality=%s)", device, transcribe.NormalizeQuality(os.Getenv("CASSINI_STT_QUALITY")))},
		parentWritableCheck("cache.root", cacheRoot, "cassini cache root"),
	}

	s := modelstore.New(cacheRoot)
	m, err := s.Catalogue.Model(string(modelID), os.Getenv("CASSINI_STT_REVISION"))
	if err == nil {
		err = s.Complete(m)
	}
	if err != nil {
		checks = append(checks, doctorCheck{
			ID:      "model.ready",
			Status:  doctorWarn,
			Summary: fmt.Sprintf("Transcription unavailable: %v; meetings retain audio", err),
			Advice:  "Install a model in Settings or use cassini models import; builds never download models",
		})
	} else if !s.Ready(m, device, transcribe.ModelRuntimeFingerprint()) {
		checks = append(checks, doctorCheck{
			ID:      "model.ready",
			Status:  doctorWarn,
			Summary: "Model installed; runtime check needed",
			Advice:  "Run cassini models probe " + m.ID + " --device " + device,
		})
	} else {
		checks = append(checks, doctorCheck{
			ID:      "model.ready",
			Status:  doctorOK,
			Summary: "Pinned model and VAD installed and ready",
		})
	}
	return checks
}

func defaultSTTDevice() string {
	if v := os.Getenv("CASSINI_STT_DEVICE"); v != "" {
		return v
	}
	return "cpu"
}

func parentWritableCheck(id string, path string, label string) doctorCheck {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return doctorCheck{ID: id, Status: doctorFail, Summary: fmt.Sprintf("%s is not a directory: %s", label, path)}
		}
		return writableDirCheck(id, path, label)
	}

	parent := filepath.Dir(path)
	for parent != "" && parent != "." {
		if info, err := os.Stat(parent); err == nil && info.IsDir() {
			return writableDirCheck(id, parent, label+" parent")
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	return doctorCheck{ID: id, Status: doctorFail, Summary: fmt.Sprintf("%s parent path not accessible: %s", label, path)}
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
