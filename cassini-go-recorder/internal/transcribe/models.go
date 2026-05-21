package transcribe

import (
	"archive/tar"
	"compress/bzip2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

	TokensFile string
	ModelType  string
	SampleRate int
	FeatureDim int
}

// EnsureModel downloads and extracts the model if not already cached,
// returning the resolved file paths. cacheDir is the root cache directory
// (e.g. ~/.cache/cassini).
func EnsureModel(cacheDir string, id ModelID, progress io.Writer) (ModelPaths, error) {
	spec, ok := knownModels[id]
	if !ok {
		return ModelPaths{}, fmt.Errorf("unknown model %q", id)
	}

	modelDir := filepath.Join(cacheDir, "models", string(id))

	paths := resolveModelPaths(modelDir, spec)
	if allExist(requiredModelFiles(paths, spec)) {
		return paths, nil
	}

	// Production images (Nextcloud ExApp, etc.) bundle the default model into
	// the image and set CASSINI_DISALLOW_MODEL_DOWNLOAD=1 so that a missing
	// bundled file fails loudly instead of silently triggering a multi-hundred-
	// MiB download from a container that may have no network egress.
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

	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return ModelPaths{}, fmt.Errorf("create model dir: %w", err)
	}

	fmt.Fprintf(progress, "downloading model %s from %s\n", id, spec.URL)
	if err := downloadAndExtract(spec.URL, modelDir, progress); err != nil {
		return ModelPaths{}, fmt.Errorf("download model %s: %w", id, err)
	}

	for _, f := range requiredModelFiles(paths, spec) {
		if !fileExists(f) {
			return ModelPaths{}, fmt.Errorf("model file not found after extraction: %s", f)
		}
	}
	return paths, nil
}

// EnsureVAD downloads the Silero VAD model if not already cached,
// returning its path. Honors CASSINI_DISALLOW_MODEL_DOWNLOAD: production
// images (where VAD is also bundled) get a clear error instead of a
// runtime download.
func EnsureVAD(cacheDir string, progress io.Writer) (string, error) {
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
	if err := downloadFile(sileroVADURL, vadPath); err != nil {
		return "", fmt.Errorf("download VAD: %w", err)
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
	} else {
		p.ModelFile = filepath.Join(modelDir, spec.ModelFile)
	}
	return p
}

func requiredModelFiles(paths ModelPaths, spec modelSpec) []string {
	if spec.EncoderFile != "" {
		return []string{paths.EncoderFile, paths.DecoderFile, paths.JoinerFile, paths.TokensFile}
	}
	return []string{paths.ModelFile, paths.TokensFile}
}

func allExist(files []string) bool {
	for _, f := range files {
		if !fileExists(f) {
			return false
		}
	}
	return true
}

func downloadAndExtract(url, destDir string, progress io.Writer) error {
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	bzr := bzip2.NewReader(resp.Body)
	tr := tar.NewReader(bzr)

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

		// Strip the top-level directory from the tar path.
		// Handles both "dir/file" and "./dir/file" tar conventions.
		name := strings.TrimPrefix(hdr.Name, "./")
		if idx := strings.Index(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if name == "" {
			continue
		}

		outPath := filepath.Join(destDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		f, err := os.Create(outPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		f.Close()
		fmt.Fprintf(progress, "  extracted: %s\n", name)
	}
	return nil
}

func downloadFile(url, destPath string) error {
	resp, err := http.Get(url) //nolint:gosec
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
