package transcribe

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"gocassini/internal/modelstore"
)

// InstalledModel and InstalledVAD never acquire files. Runtime processing uses
// only this store; the old archive helper remains for explicit benchmark tools.
func InstalledModel(root string, id ModelID, revision string, _ io.Writer) (ModelPaths, error) {
	s := modelstore.New(root)
	m, err := s.Catalogue.Model(string(id), revision)
	if err != nil {
		return ModelPaths{}, err
	}
	if err := s.Installed(m); err != nil {
		return ModelPaths{}, fmt.Errorf("model is not installed/verified; use Settings or cassini models import: %w", err)
	}
	spec, ok := knownModels[id]
	if !ok {
		return ModelPaths{}, fmt.Errorf("unknown model %s", id)
	}
	// Optional vocabulary is absent in the published int8 revision.
	hasVocab := false
	for _, f := range m.Files {
		if f.Path == spec.BpeVocabFile {
			hasVocab = true
		}
	}
	if !hasVocab {
		spec.BpeVocabFile = ""
	}
	return resolveModelPaths(s.Dir(m), spec, id), nil
}
func InstalledVAD(root string, id ModelID, revision string, _ io.Writer) (string, error) {
	s := modelstore.New(root)
	m, err := s.Catalogue.Model(string(id), revision)
	if err != nil {
		return "", err
	}
	v, err := s.VAD(m)
	if err != nil {
		return "", err
	}
	if err := s.Installed(v); err != nil {
		return "", fmt.Errorf("Silero VAD is not installed: %w", err)
	}
	return filepath.Join(s.Dir(v), v.Files[0].Path), nil
}
func ModelRuntimeFingerprint() string {
	// Every new executable or native runtime gets a local readiness recheck;
	// neither affects the identity/path of the weights.
	exe, _ := os.Executable()
	st, _ := os.Stat(exe)
	identity := fmt.Sprintf("%s/%s/%s/%t", runtime.GOOS, runtime.GOARCH, RuntimeVersion(), HasReferenceRuntime())
	if st != nil {
		identity += fmt.Sprintf("/%d/%d/%s", st.Size(), st.ModTime().UnixNano(), os.Getenv("CUDA_VISIBLE_DEVICES"))
	}
	// A moved volume, reboot/driver change, or native library replacement
	// rechecks locally too; none of these changes the weight identity.
	for _, p := range []string{"/proc/sys/kernel/random/boot_id", "/proc/driver/nvidia/version"} {
		if b, e := os.ReadFile(p); e == nil {
			identity += "/" + string(b)
		}
	}
	if dir := os.Getenv("CASSINI_LIB_DIR"); dir != "" {
		for _, pattern := range []string{"libsherpa*", "libonnxruntime*"} {
			paths, _ := filepath.Glob(filepath.Join(dir, pattern))
			for _, p := range paths {
				if st, e := os.Stat(p); e == nil {
					identity += fmt.Sprintf("/%s/%d/%d", filepath.Base(p), st.Size(), st.ModTime().UnixNano())
				}
			}
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}
func ProbeInstalledModel(ctx context.Context, s *modelstore.Store, m modelstore.Model, device string) error {
	unlock, err := lockModelRuntime(ctx, s.Root)
	if err != nil {
		return err
	}
	defer unlock()
	if device != "cpu" && device != "cuda" {
		return fmt.Errorf("device must be cpu or cuda")
	}
	if device == "cuda" && ModelID(m.ID) != ModelParakeet06BV3 {
		return fmt.Errorf("CUDA requires the fp32 Parakeet revision")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Complete(m); err != nil {
		return err
	}
	if err := admitModelProbe(ctx, ModelID(m.ID), device); err != nil {
		return err
	}
	spec, ok := knownModels[ModelID(m.ID)]
	if !ok {
		return fmt.Errorf("unknown model %s", m.ID)
	}
	hasVocab := false
	for _, f := range m.Files {
		if f.Path == spec.BpeVocabFile {
			hasVocab = true
		}
	}
	if !hasVocab {
		spec.BpeVocabFile = ""
	}
	paths := resolveModelPaths(s.Dir(m), spec, ModelID(m.ID))
	v, _ := s.VAD(m)
	rec, err := NewRecognizer(paths, filepath.Join(s.Dir(v), v.Files[0].Path), device, 1, nil)
	if err != nil {
		return err
	}
	defer rec.Close()
	// Feed silence too: loading the graph alone misses runtime execution errors.
	if _, err = rec.Transcribe(make([]float32, 16000), 16000, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.MarkReady(m, device, ModelRuntimeFingerprint())
}
