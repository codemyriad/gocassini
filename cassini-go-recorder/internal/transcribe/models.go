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
	// Best accuracy for English with real word-level timestamps.
	ModelParakeet110M ModelID = "parakeet-tdt-ctc-110m-en-int8"

	defaultModelID = ModelParakeet110M
)

type modelSpec struct {
	URL      string
	ModelFile  string // relative path inside tarball to the .onnx file
	TokensFile string // relative path inside tarball to tokens.txt
	ModelType  string // sherpa-onnx model type string
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
}

// ModelPaths holds resolved filesystem paths for a downloaded model.
type ModelPaths struct {
	ModelFile  string
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
	modelFile := filepath.Join(modelDir, spec.ModelFile)
	tokensFile := filepath.Join(modelDir, spec.TokensFile)

	if fileExists(modelFile) && fileExists(tokensFile) {
		return ModelPaths{
			ModelFile:  modelFile,
			TokensFile: tokensFile,
			ModelType:  spec.ModelType,
			SampleRate: spec.SampleRate,
			FeatureDim: spec.FeatureDim,
		}, nil
	}

	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		return ModelPaths{}, fmt.Errorf("create model dir: %w", err)
	}

	fmt.Fprintf(progress, "downloading model %s from %s\n", id, spec.URL)
	if err := downloadAndExtract(spec.URL, modelDir, progress); err != nil {
		return ModelPaths{}, fmt.Errorf("download model %s: %w", id, err)
	}

	if !fileExists(modelFile) {
		return ModelPaths{}, fmt.Errorf("model file not found after extraction: %s", modelFile)
	}
	if !fileExists(tokensFile) {
		return ModelPaths{}, fmt.Errorf("tokens file not found after extraction: %s", tokensFile)
	}

	return ModelPaths{
		ModelFile:  modelFile,
		TokensFile: tokensFile,
		ModelType:  spec.ModelType,
		SampleRate: spec.SampleRate,
		FeatureDim: spec.FeatureDim,
	}, nil
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
