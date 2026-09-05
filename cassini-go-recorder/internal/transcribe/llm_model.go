package transcribe

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The image fetcher consumes this same descriptor.
//
//go:embed ling-model.json
var lingModelJSON []byte

type summaryModelSpec struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func bundledSummarySpec() summaryModelSpec {
	var spec summaryModelSpec
	if err := json.Unmarshal(lingModelJSON, &spec); err != nil {
		panic(err)
	}
	return spec
}

func verifySummaryModel(path string, spec summaryModelSpec) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != spec.Size {
		return fmt.Errorf("summary model %s has wrong size (want %d bytes)", path, spec.Size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != spec.SHA256 {
		return fmt.Errorf("summary model %s failed SHA-256 verification", path)
	}
	return nil
}

func ensureSummaryModel(cacheDir string, progress io.Writer) (string, error) {
	return ensureSummaryModelSpec(cacheDir, bundledSummarySpec(), progress)
}

func ensureSummaryModelSpec(cacheDir string, spec summaryModelSpec, progress io.Writer) (string, error) {
	if root := strings.TrimSpace(os.Getenv(envBundledModelRoot)); root != "" {
		path := filepath.Join(root, "models", spec.ID, spec.File)
		_, statErr := os.Stat(path)
		if statErr == nil || os.Getenv("CASSINI_BUNDLED_SUMMARY_MODEL") == spec.ID {
			if err := verifySummaryModel(path, spec); err != nil {
				return "", fmt.Errorf("invalid bundled summary model; rebuild image: %w", err)
			}
			return path, nil
		}
	}
	path := filepath.Join(cacheDir, "models", spec.ID, spec.File)
	if err := verifySummaryModel(path, spec); err == nil {
		return path, nil
	}
	if envBool("CASSINI_DISALLOW_MODEL_DOWNLOAD") {
		return "", fmt.Errorf("summary model missing or invalid and CASSINI_DISALLOW_MODEL_DOWNLOAD=1")
	}
	err := withCacheLock(cacheDir, func() error {
		if err := verifySummaryModel(path, spec); err == nil {
			return nil
		}
		parent := filepath.Join(cacheDir, "models")
		removeOrphanStaging(parent, progress)
		var stat syscall.Statfs_t
		if err := syscall.Statfs(parent, &stat); err != nil {
			return err
		}
		if uint64(stat.Bavail)*uint64(stat.Bsize) < uint64(spec.Size)+(1<<30) {
			return fmt.Errorf("summary model needs %d bytes plus 1 GiB free disk headroom", spec.Size)
		}
		staging, err := os.MkdirTemp(parent, ".staging-ling-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(staging)
		download := filepath.Join(staging, spec.File)
		fmt.Fprintf(progress, "  downloading summary model %s (%d bytes)\n", spec.ID, spec.Size)
		if err := downloadSummaryModel(spec, download); err != nil {
			return err
		}
		if err := verifySummaryModel(download, spec); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.Rename(download, path)
	})
	return path, err
}

func downloadSummaryModel(spec summaryModelSpec, path string) error {
	resp, err := modelHTTPClient.Get(spec.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("summary model download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, spec.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != spec.Size {
		return fmt.Errorf("summary model download has %d bytes, want %d", n, spec.Size)
	}
	return nil
}
