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
	"strings"
	"syscall"

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
	fs.StringVar(&target, "target", "all", "check set: all, record, build")
	fs.BoolVar(&asJSON, "json", false, "emit the checks as a JSON array instead of text")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini doctor
  cassini doctor --target record
  cassini doctor --target build
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
	// shipped contract (standalone `cassini doctor` must keep printing exactly
	// what it printed before). So --json REPLACES the text rather than
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
	if target != "all" && target != "record" && target != "build" {
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

	if target == "all" || target == "build" {
		checks = append(checks, commandCheck("ffmpeg", "ffmpeg"))
		checks = append(checks, commandCheck("ffprobe", "ffprobe"))
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

	// Look in the image's read-only bundled root first, the same order
	// EnsureModel uses: a tier the image carries is never downloaded, and a
	// tier it does not carry is fetched once into the writable cache (D-704).
	modelDir := filepath.Join(cacheRoot, "models", string(modelID))
	if root := strings.TrimSpace(os.Getenv("CASSINI_BUNDLED_MODEL_ROOT")); root != "" {
		bundledDir := filepath.Join(root, "models", string(modelID))
		if check := modelFilesCheck(bundledDir, modelID); check.Status == doctorOK {
			checks = append(checks, check)
			return append(checks, doctorCheck{
				ID:      "model.bundled",
				Status:  doctorOK,
				Summary: fmt.Sprintf("STT model %s is bundled in this image", modelID),
			})
		}
		checks = append(checks, doctorCheck{
			ID:     "model.bundled",
			Status: doctorWarn,
			Summary: fmt.Sprintf("STT model %s is not bundled in this image; it downloads once into %s on first use",
				modelID, modelDir),
		})
	}
	checks = append(checks, modelFilesCheck(modelDir, modelID))

	if info, err := os.Stat(modelDir); err == nil && info.IsDir() {
		checks = append(checks, writableDirCheck("model.cache.writable", modelDir, "STT model cache"))
	} else {
		check := parentWritableCheck("model.cache.parent", modelDir, "STT model cache parent")
		if check.Status != doctorOK {
			check.Advice = fmt.Sprintf("ensure %s can be created, or set CASSINI_CACHE_ROOT to a writable cache directory", modelDir)
		}
		checks = append(checks, check)
	}
	return checks
}

// modelFilesCheck reports whether the required onnx + tokens files for the
// given model id are present inside modelDir. In bundled-image deployments
// (CASSINI_DISALLOW_MODEL_DOWNLOAD=1) a missing file is fatal at recorder
// startup, so doctor surfaces it up front.
func modelFilesCheck(modelDir string, modelID transcribe.ModelID) doctorCheck {
	// Ask the model registry which files this bundle needs rather than naming
	// one architecture's: the 110M "fast" tier is a CTC model shipping a single
	// model.int8.onnx, and demanding encoder/decoder/joiner of it failed a model
	// that was present and correct (D-702).
	required := transcribe.RequiredModelFileNames(modelID)
	if len(required) == 0 {
		return doctorCheck{
			ID:      "model.files",
			Status:  doctorWarn,
			Summary: fmt.Sprintf("unknown STT model %q; cannot verify its files in %s", modelID, modelDir),
			Advice:  "set CASSINI_STT_MODEL to a known model id, or leave it unset to use the quality tier's model",
		}
	}
	missing := []string{}
	for _, name := range required {
		if !fileExists(filepath.Join(modelDir, name)) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return doctorCheck{
			ID:      "model.files",
			Status:  doctorOK,
			Summary: fmt.Sprintf("STT model files present in %s", modelDir),
		}
	}
	disallow := os.Getenv("CASSINI_DISALLOW_MODEL_DOWNLOAD")
	if disallow == "1" || disallow == "true" {
		return doctorCheck{
			ID:     "model.files",
			Status: doctorFail,
			Summary: fmt.Sprintf("STT model files missing in %s: %s (CASSINI_DISALLOW_MODEL_DOWNLOAD set)",
				modelDir, strings.Join(missing, ", ")),
			Advice: fmt.Sprintf("rebuild the image with the model bundled at %s, or unset CASSINI_DISALLOW_MODEL_DOWNLOAD to allow runtime download",
				modelDir),
		}
	}
	return doctorCheck{
		ID:     "model.files",
		Status: doctorWarn,
		Summary: fmt.Sprintf("STT model files missing in %s: %s (will be downloaded on first build)",
			modelDir, strings.Join(missing, ", ")),
	}
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
