package transcribe

import (
	"archive/tar"
	"compress/bzip2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ModelID identifies a sherpa-onnx model bundle.
type ModelID string

const (
	// ModelParakeet110M is the NeMo Parakeet TDT CTC 110M int8 model.
	// Kept for reference; superseded by 0.6B.
	ModelParakeet110M ModelID = "parakeet-tdt-ctc-110m-en-int8"

	// ModelParakeet06B is the NeMo Parakeet TDT 0.6B v2 int8 transducer model.
	// Kept for reference; superseded by v3.
	ModelParakeet06B ModelID = "parakeet-tdt-0.6b-v2-int8"

	// ModelParakeet06BV3Int8 is the NeMo Parakeet TDT 0.6B v3 int8 transducer
	// model. CPU-bundled default in the gocassini ExApp image. Uses
	// encoder+decoder+joiner architecture with feature dim 128 (v3 quirk;
	// v2 used 80).
	ModelParakeet06BV3Int8 ModelID = "parakeet-tdt-0.6b-v3-int8"

	// ModelParakeet06BV3 is the NeMo Parakeet TDT 0.6B v3 fp32 transducer model.
	// CUDA-bundled default; same architecture as v3 int8. Exported from the
	// official .nemo via scripts/nemo/parakeet-tdt-0.6b-v3/export_onnx.py.
	// Runs ~2× faster than CPU at fp32 on CUDA; int8 fragments under CUDA EP.
	ModelParakeet06BV3 ModelID = "parakeet-tdt-0.6b-v3"

	defaultModelID = ModelParakeet06BV3Int8

	// sileroVADURL is the Silero VAD model (single .onnx file, ~630 KB).
	sileroVADURL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx"
)

type modelSpec struct {
	URL string

	// CTC models: single model file.
	ModelFile string

	// Transducer models: three separate files.
	EncoderFile string
	DecoderFile string
	JoinerFile  string

	// WeightsFile is an external-data sidecar the encoder graph references
	// (fp32 exports keep their weights outside the .onnx). sherpa cannot load
	// the encoder without it, so it counts as a required file even though it is
	// never named in the session config.
	WeightsFile string

	TokensFile string
	ModelType  string // sherpa-onnx model type hint
	SampleRate int
	FeatureDim int
}

var knownModels = map[ModelID]modelSpec{
	ModelParakeet110M: {
		URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" +
			"sherpa-onnx-nemo-parakeet_tdt_ctc_110m-en-36000-int8.tar.bz2",
		ModelFile:  "model.int8.onnx",
		TokensFile: "tokens.txt",
		ModelType:  "nemo_ctc",
		SampleRate: 16000,
		FeatureDim: 80,
	},
	ModelParakeet06B: {
		URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" +
			"sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2",
		EncoderFile: "encoder.int8.onnx",
		DecoderFile: "decoder.int8.onnx",
		JoinerFile:  "joiner.int8.onnx",
		TokensFile:  "tokens.txt",
		ModelType:   "nemo_transducer",
		SampleRate:  16000,
		FeatureDim:  80,
	},
	ModelParakeet06BV3Int8: {
		URL: "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" +
			"sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2",
		EncoderFile: "encoder.int8.onnx",
		DecoderFile: "decoder.int8.onnx",
		JoinerFile:  "joiner.int8.onnx",
		TokensFile:  "tokens.txt",
		ModelType:   "nemo_transducer",
		SampleRate:  16000,
		FeatureDim:  128,
	},
	ModelParakeet06BV3: {
		URL: "https://assets.gocassini.codemyriad.io/" +
			"sherpa-onnx-nemo-parakeet-tdt-0.6b-v3.tar.bz2",
		EncoderFile: "encoder.onnx",
		DecoderFile: "decoder.onnx",
		JoinerFile:  "joiner.onnx",
		WeightsFile: "encoder.weights",
		TokensFile:  "tokens.txt",
		ModelType:   "nemo_transducer",
		SampleRate:  16000,
		FeatureDim:  128,
	},
}

// DefaultModelID returns the model ID used when no model is explicitly configured.
func DefaultModelID() ModelID { return defaultModelID }

// ModelPaths holds resolved filesystem paths for a downloaded model.
type ModelPaths struct {
	// CTC models set ModelFile; transducer models set Encoder/Decoder/Joiner.
	ModelFile   string
	EncoderFile string
	DecoderFile string
	JoinerFile  string
	// WeightsFile is the encoder's external-data sidecar when the export has
	// one; empty otherwise. Required on disk, never passed to sherpa.
	WeightsFile string

	TokensFile string
	ModelType  string
	SampleRate int
	FeatureDim int
}

// envBundledModelRoot names the read-only directory where an image bakes its
// models, and envBundledModels lists which model ids that image claims to
// carry. An image serves the device it exists for: the portable image bundles
// the CPU default, the CUDA image bundles fp32. A tier outside that list is
// downloaded once into the writable cache, which the ExApp keeps on its
// persistent volume so the download survives a container recreate (D-704).
const (
	envBundledModelRoot = "CASSINI_BUNDLED_MODEL_ROOT"
	envBundledModels    = "CASSINI_BUNDLED_MODELS"
)

// bundledModelDeclared reports whether the running image says it carries this
// model. A declared model that is not on disk is a broken image, so the caller
// must fail loudly instead of downloading over the top of it.
func bundledModelDeclared(id ModelID) bool {
	for _, declared := range strings.Fields(os.Getenv(envBundledModels)) {
		if declared == string(id) {
			return true
		}
	}
	return false
}

// EnsureModel downloads and extracts the model if not already cached,
// returning the resolved file paths. cacheDir is the writable cache root
// (e.g. ~/.cache/cassini). Files baked into the image are found first, under
// CASSINI_BUNDLED_MODEL_ROOT, and are never written to.
func EnsureModel(cacheDir string, id ModelID, progress io.Writer) (ModelPaths, error) {
	spec, ok := knownModels[id]
	if !ok {
		return ModelPaths{}, fmt.Errorf("unknown model %q", id)
	}

	if root := strings.TrimSpace(os.Getenv(envBundledModelRoot)); root != "" {
		bundledDir := filepath.Join(root, "models", string(id))
		bundled := resolveModelPaths(bundledDir, spec)
		if allExist(requiredModelFiles(bundled, spec)) {
			return bundled, nil
		}
		if bundledModelDeclared(id) {
			missing := []string{}
			for _, f := range requiredModelFiles(bundled, spec) {
				if !fileExists(f) {
					missing = append(missing, f)
				}
			}
			return ModelPaths{}, fmt.Errorf(
				"image declares model %s in %s but these files are missing: %s; "+
					"rebuild the image, do not expect a download to repair a bundled model",
				id, envBundledModels, strings.Join(missing, ", "))
		}
	}

	modelDir := filepath.Join(cacheDir, "models", string(id))

	paths := resolveModelPaths(modelDir, spec)
	// A downloaded model counts only when it also carries the completion
	// marker. Files that merely exist can be the truncated remains of an
	// interrupted download, and this cache is persistent, so such a directory
	// would otherwise poison every later build (D-704).
	if modelDirComplete(modelDir) && allNonEmpty(requiredModelFiles(paths, spec)) {
		return paths, nil
	}

	// CASSINI_DISALLOW_MODEL_DOWNLOAD=1 forbids every download, for an
	// air-gapped install that wants a loud failure instead of a fetch. Images
	// no longer set it: they declare what they bake in CASSINI_BUNDLED_MODELS,
	// which fails loudly for a missing bundled model while still allowing a
	// tier the image does not carry to download once (D-704).
	if envBool("CASSINI_DISALLOW_MODEL_DOWNLOAD") {
		missing := []string{}
		for _, f := range requiredModelFiles(paths, spec) {
			if !fileExists(f) {
				missing = append(missing, f)
			}
		}
		return ModelPaths{}, fmt.Errorf(
			"model %s missing required files and CASSINI_DISALLOW_MODEL_DOWNLOAD=1; "+
				"missing: %s", id, strings.Join(missing, ", "))
	}

	required := RequiredModelFileNames(id)
	// One writer per model, across processes. Without the lock two downloaders
	// can both judge the destination invalid, and the loser removes the
	// directory the winner just promoted, while a third process loads from it.
	// This is a file lock, so the kernel releases it when a process dies, which
	// is what lets the next holder clean up an abandoned staging directory.
	if err := withModelLock(cacheDir, string(id), func() error {
		// Another process can have finished this model while this one waited.
		if modelDirValid(modelDir, required) {
			return nil
		}
		removeOrphanStaging(filepath.Join(cacheDir, "models"), string(id), progress)
		fmt.Fprintf(progress, "downloading model %s from %s\n", id, spec.URL)
		return downloadAndExtract(spec.URL, modelDir, required, progress)
	}); err != nil {
		return ModelPaths{}, fmt.Errorf("download model %s: %w", id, err)
	}

	if !modelDirValid(modelDir, required) {
		return ModelPaths{}, fmt.Errorf("model %s is still incomplete after download", id)
	}
	return paths, nil
}

// modelLockWait bounds how long one build waits for another process to finish
// the same model. It sits above the HTTP timeout, so a live download is never
// abandoned, and a wedged holder cannot occupy the build worker forever.
const modelLockWait = 35 * time.Minute

// withModelLock runs fn while holding an exclusive lock for one model. The lock
// covers staging cleanup, the free-space budget, extraction and promotion, so
// two processes cannot each reserve the same free space or race on the
// destination directory.
func withModelLock(cacheDir, id string, fn func() error) error {
	dir := filepath.Join(cacheDir, "models")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create model cache dir: %w", err)
	}
	lockPath := filepath.Join(dir, "."+id+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open model lock %s: %w", lockPath, err)
	}
	defer f.Close()

	deadline := time.Now().Add(modelLockWait)
	for {
		lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			break
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
			return fmt.Errorf("lock model %s: %w", id, lockErr)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"another process has been downloading model %s for over %s; "+
					"wait for it to finish, or select a quality tier this image bundles", id, modelLockWait)
		}
		time.Sleep(2 * time.Second)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	return fn()
}

// removeOrphanStaging deletes staging directories left by a download that the
// kernel killed before its cleanup ran. The caller holds the model lock, so a
// staging directory for this model is abandoned by definition. Each one holds
// up to gigabytes on the volume that also stores the recordings.
func removeOrphanStaging(modelsDir, id string, progress io.Writer) {
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		return
	}
	prefix := ".staging-" + id + "-"
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(modelsDir, entry.Name())); err == nil {
			fmt.Fprintf(progress, "  removed abandoned download %s\n", entry.Name())
		}
	}
}

// EnsureVAD returns the path of the Silero VAD model, downloading it only when
// neither the image nor the cache has it. Honors
// CASSINI_DISALLOW_MODEL_DOWNLOAD for an air-gapped install.
func EnsureVAD(cacheDir string, progress io.Writer) (string, error) {
	// Every image bakes the VAD, so look in the read-only bundled root first.
	// Without this the writable cache on the persistent volume would refetch a
	// file the image already has (D-704).
	if root := strings.TrimSpace(os.Getenv(envBundledModelRoot)); root != "" {
		bundled := filepath.Join(root, "vad", "silero_vad.onnx")
		if fileExists(bundled) {
			return bundled, nil
		}
	}
	vadPath := filepath.Join(cacheDir, "vad", "silero_vad.onnx")
	if fileExists(vadPath) {
		return vadPath, nil
	}
	if envBool("CASSINI_DISALLOW_MODEL_DOWNLOAD") {
		return "", fmt.Errorf(
			"silero VAD missing at %s and CASSINI_DISALLOW_MODEL_DOWNLOAD=1; "+
				"rebuild the image with the VAD model bundled", vadPath)
	}
	if err := os.MkdirAll(filepath.Dir(vadPath), 0o755); err != nil {
		return "", fmt.Errorf("create vad dir: %w", err)
	}
	fmt.Fprintf(progress, "downloading Silero VAD from %s\n", sileroVADURL)
	// Download beside the target and rename, so an interrupted fetch never
	// leaves a truncated VAD that later builds would load.
	tmp, err := os.CreateTemp(filepath.Dir(vadPath), ".silero_vad-*.onnx")
	if err != nil {
		return "", fmt.Errorf("create vad staging file: %w", err)
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)
	if err := downloadFile(sileroVADURL, tmpName); err != nil {
		return "", fmt.Errorf("download VAD: %w", err)
	}
	if info, err := os.Stat(tmpName); err != nil || info.Size() == 0 {
		return "", fmt.Errorf("downloaded VAD from %s is empty", sileroVADURL)
	}
	if err := os.Rename(tmpName, vadPath); err != nil {
		return "", fmt.Errorf("promote VAD into %s: %w", vadPath, err)
	}
	return vadPath, nil
}

func resolveModelPaths(modelDir string, spec modelSpec) ModelPaths {
	p := ModelPaths{
		TokensFile: filepath.Join(modelDir, spec.TokensFile),
		ModelType:  spec.ModelType,
		SampleRate: spec.SampleRate,
		FeatureDim: spec.FeatureDim,
	}
	if spec.EncoderFile != "" {
		p.EncoderFile = filepath.Join(modelDir, spec.EncoderFile)
		p.DecoderFile = filepath.Join(modelDir, spec.DecoderFile)
		p.JoinerFile = filepath.Join(modelDir, spec.JoinerFile)
		if spec.WeightsFile != "" {
			p.WeightsFile = filepath.Join(modelDir, spec.WeightsFile)
		}
	} else {
		p.ModelFile = filepath.Join(modelDir, spec.ModelFile)
	}
	return p
}

func requiredModelFiles(paths ModelPaths, spec modelSpec) []string {
	if spec.EncoderFile != "" {
		files := []string{paths.EncoderFile, paths.DecoderFile, paths.JoinerFile, paths.TokensFile}
		if paths.WeightsFile != "" {
			files = append(files, paths.WeightsFile)
		}
		return files
	}
	return []string{paths.ModelFile, paths.TokensFile}
}

// RequiredModelFileNames returns the base names a bundle of this model must
// contain, or nil for an unknown id. Callers that check a bundled model without
// loading it — doctor, image smoke tests — must derive the list from here
// rather than restating one architecture's file names: a CTC model ships a
// single model.int8.onnx where a transducer ships encoder/decoder/joiner, and
// asserting the wrong shape fails a model that is present and correct.
func RequiredModelFileNames(id ModelID) []string {
	spec, ok := knownModels[id]
	if !ok {
		return nil
	}
	names := requiredModelFiles(resolveModelPaths("", spec), spec)
	for i, name := range names {
		names[i] = filepath.Base(name)
	}
	return names
}

// allNonEmpty reports whether every path is a regular file with at least one
// byte in it. A zero-length file is the signature of a download that died
// mid-write. The regular-file test matters as much: a malformed archive can
// create a directory or a symlink named model.int8.onnx, and that would
// otherwise promote as a valid model and poison the cache for good. Lstat, so
// a symlink is judged as itself rather than as its target.
func allNonEmpty(files []string) bool {
	for _, f := range files {
		info, err := os.Lstat(f)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	return true
}

func allExist(files []string) bool {
	for _, f := range files {
		if !fileExists(f) {
			return false
		}
	}
	return true
}

// modelHTTPClient bounds every model fetch. A blackholed endpoint would
// otherwise hold the only build worker forever, because the operator runs one
// build at a time.
var modelHTTPClient = &http.Client{
	Timeout: 30 * time.Minute,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

// maxExtractedBytes caps one archive. The largest model this repository ships
// unpacks to about 2.5GB, so this leaves room without letting a hostile or
// corrupt archive fill the volume that also holds the job database and the
// recordings.
const maxExtractedBytes = 8 << 30

// diskReserveBytes stays free for the job database and the recordings that
// share the persistent volume with this cache.
const diskReserveBytes = 1 << 30

// completionMarker marks a model directory that finished extraction and passed
// validation. Its presence is the only proof a directory is usable: a truncated
// file from an interrupted download still "exists", and the cache now lives on
// a persistent volume where such a directory would survive forever (D-704).
const completionMarker = ".cassini-model-complete"

// modelDirComplete reports whether a model directory holds a finished download.
func modelDirComplete(dir string) bool {
	return fileExists(filepath.Join(dir, completionMarker))
}

// safeExtractPath joins one tar entry onto destDir and refuses anything that
// escapes it. A tar entry controls its own name, so "a/../../etc/cron.d/x"
// would otherwise write outside the cache from a container that runs as root.
func safeExtractPath(destDir, name string) (string, error) {
	raw := strings.TrimPrefix(name, "./")
	// Test absoluteness before stripping the top-level directory. Stripping
	// first turns "/etc/passwd" into "etc/passwd", which reads as a safe
	// relative path.
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") {
		return "", fmt.Errorf("tar entry %q is an absolute path", name)
	}
	if idx := strings.Index(raw, "/"); idx >= 0 {
		raw = raw[idx+1:] // strip the archive's top-level directory
	}
	if raw == "" {
		return "", nil
	}
	name = raw
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar entry %q escapes the destination", name)
	}
	out := filepath.Join(destDir, clean)
	rel, err := filepath.Rel(destDir, out)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("tar entry %q escapes the destination", name)
	}
	return out, nil
}

// downloadAndExtract fetches an archive into a staging directory beside the
// target, validates it, and only then moves it into place. Nothing ever writes
// into the directory the recorder loads from, so an interrupted download leaves
// no half-model behind.
func downloadAndExtract(url, destDir string, required []string, progress io.Writer) error {
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create model cache dir: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".staging-"+filepath.Base(destDir)+"-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := extractInto(url, staging, progress); err != nil {
		return err
	}
	for _, name := range required {
		info, err := os.Lstat(filepath.Join(staging, name))
		if err != nil {
			return fmt.Errorf("downloaded archive from %s has no %s", url, name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("downloaded archive from %s has %s as %s, not a file", url, name, info.Mode().Type())
		}
		if info.Size() == 0 {
			return fmt.Errorf("downloaded file %s from %s is empty", name, url)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, completionMarker), []byte(url+"\n"), 0o644); err != nil {
		return fmt.Errorf("write completion marker: %w", err)
	}

	// Rename refuses a destination that exists and is not empty, so the remains
	// of an interrupted download would block every later attempt forever.
	// Remove such a directory first. A valid one is left alone: another build
	// finished this model while this one ran.
	if dirExists(destDir) && !modelDirValid(destDir, required) {
		if err := os.RemoveAll(destDir); err != nil {
			return fmt.Errorf("remove incomplete model directory %s: %w", destDir, err)
		}
	}
	if err := os.Rename(staging, destDir); err != nil {
		// The loser of a race uses the winner's directory, but only after it
		// passes the same validation this copy just passed.
		if modelDirValid(destDir, required) {
			return nil
		}
		return fmt.Errorf("promote model into %s: %w", destDir, err)
	}
	return nil
}

// modelDirValid reports whether a directory holds a finished, usable model: the
// completion marker and every required file with at least one byte in it.
func modelDirValid(dir string, required []string) bool {
	if !modelDirComplete(dir) {
		return false
	}
	paths := make([]string, 0, len(required))
	for _, name := range required {
		paths = append(paths, filepath.Join(dir, name))
	}
	return allNonEmpty(paths)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func extractInto(url, destDir string, progress io.Writer) error {
	resp, err := modelHTTPClient.Get(url) //nolint:gosec
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	// Bound the extraction by the space actually available, not by the size of
	// the archive: a compressed file says nothing reliable about what it
	// expands to. This volume also holds the job database and the recordings,
	// so 1GiB stays free for them.
	budget, err := extractionBudget(destDir)
	if err != nil {
		return err
	}

	bzr := bzip2.NewReader(io.LimitReader(resp.Body, maxExtractedBytes))
	tr := tar.NewReader(bzr)
	var written int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		outPath, err := safeExtractPath(destDir, hdr.Name)
		if err != nil {
			return fmt.Errorf("refusing archive from %s: %w", url, err)
		}
		if outPath == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		f, err := os.Create(outPath)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(tr, budget-written+1))
		f.Close()
		if err != nil {
			return err
		}
		written += n
		if written > budget {
			return fmt.Errorf(
				"archive from %s expands beyond the %d MB this volume can spare; "+
					"free space, or select a quality tier this image bundles",
				url, budget/(1<<20))
		}
		fmt.Fprintf(progress, "  extracted: %s\n", filepath.Base(outPath))
	}
	return nil
}

func downloadFile(url, destPath string) error {
	resp, err := modelHTTPClient.Get(url) //nolint:gosec
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// extractionBudget is the number of bytes an extraction may write: the space
// free on this filesystem, less a reserve for the job database and the
// recordings that share the volume, and never more than maxExtractedBytes.
// Enforcing a budget while writing is the only bound that holds, because the
// size of a compressed archive does not limit what it expands to.
func extractionBudget(dir string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		// An unreadable filesystem must not stop a download that would have
		// worked, so fall back to the absolute cap.
		return maxExtractedBytes, nil
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	budget := free - diskReserveBytes
	if budget <= 0 {
		return 0, fmt.Errorf(
			"only %d MB is free in %s, and %d MB stays reserved for the recordings and the job database; "+
				"free space, or select a quality tier this image bundles",
			free/(1<<20), dir, int64(diskReserveBytes)/(1<<20))
	}
	if budget > maxExtractedBytes {
		budget = maxExtractedBytes
	}
	return budget, nil
}
