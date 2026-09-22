package modelstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const receiptName = ".cassini-model.json"
const reserveBytes = int64(256 << 20)

type Progress struct {
	Version   int    `json:"version"`
	Phase     string `json:"phase"`
	File      string `json:"file,omitempty"`
	Completed int64  `json:"completed_bytes"`
	Total     int64  `json:"total_bytes"`
	Reused    int64  `json:"reused_bytes"`
}
type Store struct {
	Root      string
	Catalogue Catalogue
	Progress  func(Progress)
	// URL is a test seam. Production uses only the shipped immutable URLs.
	URL        func(Artifact) string
	NoDownload bool
}
type fileStamp struct {
	Size  int64 `json:"size"`
	Mtime int64 `json:"mtime_ns"`
}
type receipt struct {
	Model    string               `json:"model"`
	Revision string               `json:"revision"`
	Files    map[string]fileStamp `json:"files"`
}

func New(root string) *Store { return &Store{Root: root, Catalogue: Shipped()} }
func (s *Store) emit(phase, file string, done, total, reused int64) {
	if s.Progress != nil {
		s.Progress(Progress{1, phase, file, done, total, reused})
	}
}
func (s *Store) Dir(m Model) string {
	if m.ID == "silero-vad" {
		return filepath.Join(s.Root, "vad", m.Revision)
	}
	return filepath.Join(s.Root, "models", m.ID, m.Revision)
}
func (s *Store) VAD(m Model) (Model, error) { return s.Catalogue.Model("silero-vad", m.VADRevision) }
func (s *Store) Components(m Model) []Model {
	v, _ := s.VAD(m)
	if m.ID == v.ID {
		return []Model{m}
	}
	return []Model{m, v}
}

// Installed checks the verified publication receipt and file metadata. All bytes
// were hashed before publication. Changed files require explicit revalidation.
func (s *Store) Installed(m Model) error {
	data, err := os.ReadFile(filepath.Join(s.Dir(m), receiptName))
	if err != nil {
		return err
	}
	var r receipt
	if json.Unmarshal(data, &r) != nil || r.Model != m.ID || r.Revision != m.Revision {
		return errors.New("invalid installation receipt")
	}
	for _, f := range m.Files {
		a, _ := s.Catalogue.Artifact(f.Artifact)
		st, err := os.Lstat(filepath.Join(s.Dir(m), f.Path))
		if err != nil {
			return err
		}
		stamp, ok := r.Files[f.Path]
		if !ok || !st.Mode().IsRegular() || st.Size() != a.UncompressedSize || stamp.Size != st.Size() || stamp.Mtime != st.ModTime().UnixNano() {
			return fmt.Errorf("installed file changed: %s", f.Path)
		}
	}
	return nil
}
func (s *Store) Complete(m Model) error {
	for _, c := range s.Components(m) {
		if e := s.Installed(c); e != nil {
			return fmt.Errorf("%s: %w", c.ID, e)
		}
	}
	return nil
}
func (s *Store) Verify(ctx context.Context, m Model, dir string) error {
	for _, f := range m.Files {
		a, _ := s.Catalogue.Artifact(f.Artifact)
		if err := verifyFile(ctx, filepath.Join(dir, f.Path), a.UncompressedSize, a.UncompressedSHA256); err != nil {
			return err
		}
	}
	return nil
}
func verifyFile(ctx context.Context, path string, size int64, hash string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != size {
		return fmt.Errorf("invalid size/type for %s: expected %d bytes", filepath.Base(path), size)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, &contextReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return fmt.Errorf("checksum mismatch: %s", filepath.Base(path))
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func (s *Store) locked(ctx context.Context, fn func() error) error {
	if s.Root == "" {
		return errors.New("model cache root is required")
	}
	if err := os.MkdirAll(s.Root, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Root, ".models.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func checkSpace(root string, needed int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return err
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < needed+reserveBytes {
		return fmt.Errorf("not enough disk space: need %d bytes plus %d reserve, have %d", needed, reserveBytes, free)
	}
	return nil
}
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".metadata-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func (s *Store) publish(ctx context.Context, m Model, staging string) error {
	if err := s.Verify(ctx, m, staging); err != nil {
		return err
	}
	// An attempt killed mid-write leaves its temporary file behind. Staging is
	// renamed into place whole, so clear those first or they are installed
	// with the revision for good.
	if err := removeTempFiles(staging); err != nil {
		return err
	}
	r := receipt{m.ID, m.Revision, map[string]fileStamp{}}
	for _, f := range m.Files {
		st, err := os.Stat(filepath.Join(staging, f.Path))
		if err != nil {
			return err
		}
		r.Files[f.Path] = fileStamp{st.Size(), st.ModTime().UnixNano()}
	}
	if err := os.WriteFile(filepath.Join(staging, "NOTICE.txt"), []byte(Notices), 0644); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(staging, receiptName), r); err != nil {
		return err
	}
	dest := s.Dir(m)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		if s.Installed(m) == nil {
			return nil
		}
		return fmt.Errorf("existing revision is damaged: %s; move it aside before reinstalling", dest)
	}
	if err := os.Rename(staging, dest); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dest))
}

// removeTempFiles deletes the CreateTemp leftovers of unpack and writeJSON.
func removeTempFiles(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if !d.IsDir() && (strings.HasPrefix(name, ".file-") || strings.HasPrefix(name, ".metadata-")) {
			return os.Remove(path)
		}
		return nil
	})
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
