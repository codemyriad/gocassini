package modelstore

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const PackFormat = "cassini.model-pack.v1"

type PackFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type PackManifest struct {
	Format      string     `json:"format"`
	Model       string     `json:"model"`
	Revision    string     `json:"revision"`
	VADRevision string     `json:"vad_revision"`
	Files       []PackFile `json:"files"`
}

func (s *Store) Pack(ctx context.Context, m Model, dest string) error {
	if err := s.Acquire(ctx, m); err != nil {
		return err
	}
	return s.locked(ctx, func() error {
		var size int64
		for _, c := range s.Components(m) {
			if e := s.Verify(ctx, c, s.Dir(c)); e != nil {
				return e
			}
			_, n := s.Catalogue.Sizes(c)
			size += n
		}
		if _, err := os.Stat(dest); err == nil {
			return fmt.Errorf("output already exists: %s", dest)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := checkSpace(filepath.Dir(dest), size+1<<20); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(dest), ".model-pack-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		tw := tar.NewWriter(f)
		v, _ := s.VAD(m)
		meta := PackManifest{Format: PackFormat, Model: m.ID, Revision: m.Revision, VADRevision: v.Revision}
		for _, c := range s.Components(m) {
			prefix := "model/"
			if c.ID == v.ID {
				prefix = "vad/"
			}
			for _, f := range c.Files {
				a, _ := s.Catalogue.Artifact(f.Artifact)
				meta.Files = append(meta.Files, PackFile{prefix + f.Path, a.UncompressedSize, a.UncompressedSHA256})
			}
		}
		manifest, _ := json.Marshal(meta)
		write := func(name string, r io.Reader, n int64) error {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: n, Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			_, err := io.CopyN(tw, &contextReader{ctx, r}, n)
			return err
		}
		if err := write("manifest.json", strings.NewReader(string(manifest)), int64(len(manifest))); err != nil {
			return err
		}
		var done int64
		for _, c := range s.Components(m) {
			prefix := "model/"
			if c.ID == "silero-vad" {
				prefix = "vad/"
			}
			for _, entry := range c.Files {
				a, _ := s.Catalogue.Artifact(entry.Artifact)
				in, err := os.Open(filepath.Join(s.Dir(c), entry.Path))
				if err != nil {
					return err
				}
				s.emit("packing", entry.Path, done, size, 0)
				err = write(prefix+entry.Path, in, a.UncompressedSize)
				in.Close()
				if err != nil {
					return err
				}
				done += a.UncompressedSize
			}
		}
		if err := write("NOTICE.txt", strings.NewReader(Notices), int64(len(Notices))); err != nil {
			return err
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		// Link is no-replace, unlike Rename: a concurrent pack cannot overwrite an
		// already completed transfer package with the same destination name.
		if err := os.Link(f.Name(), dest); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(dest)); err != nil {
			return err
		}
		s.emit("packed", dest, size, size, 0)
		return nil
	})
}

// PackIdentity reads only the first, bounded manifest. All identity and payload
// assertions are checked against the target's own catalogue during Import.
func PackIdentity(src string) (PackManifest, error) {
	f, err := os.Open(src)
	if err != nil {
		return PackManifest{}, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	h, err := tr.Next()
	if err != nil {
		return PackManifest{}, err
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > 64<<10 {
		return PackManifest{}, fmt.Errorf("not a Cassini model pack")
	}
	var p PackManifest
	if err := json.NewDecoder(io.LimitReader(tr, 64<<10)).Decode(&p); err != nil {
		return p, err
	}
	if p.Format != PackFormat {
		return p, fmt.Errorf("unsupported model pack format %q", p.Format)
	}
	return p, nil
}

type ImportOptions struct {
	From string
	VAD  string
	Pack bool
}

// Import is unconditionally local: there is no acquisition fallback, even when
// NoDownload is false. Source files and copied readiness receipts are untouched.
func (s *Store) Import(ctx context.Context, m Model, opts ImportOptions) error {
	return s.locked(ctx, func() error {
		var required int64
		for _, c := range s.Components(m) {
			_, n := s.Catalogue.Sizes(c)
			required += n
		}
		if err := checkSpace(s.Root, required); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(s.Root, ".import-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		md := filepath.Join(stage, "model")
		vd := filepath.Join(stage, "vad")
		for _, d := range []string{md, vd} {
			if err := os.MkdirAll(d, 0755); err != nil {
				return err
			}
		}
		v, _ := s.VAD(m)
		s.emit("verifying", filepath.Base(opts.From), 0, required, 0)
		info, err := os.Stat(opts.From)
		if err != nil {
			return err
		}
		switch {
		case opts.Pack:
			identity, err := PackIdentity(opts.From)
			if err != nil {
				return err
			}
			if identity.Model != m.ID || identity.Revision != m.Revision || identity.VADRevision != v.Revision {
				return fmt.Errorf("package identity does not match the shipped model/VAD catalogue")
			}
			f, err := os.Open(opts.From)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := s.unpack(ctx, m, f, stage, true, required); err != nil {
				return err
			}
		case info.IsDir():
			for _, entry := range m.Files {
				a, _ := s.Catalogue.Artifact(entry.Artifact)
				src := filepath.Join(opts.From, entry.Path)
				dest := filepath.Join(md, entry.Path)
				if _, err := os.Lstat(src); os.IsNotExist(err) {
					src += ".zst"
					if err := verifyFile(ctx, src, a.Size, a.SHA256); err != nil {
						return err
					}
					if err := decompress(ctx, src, dest, a); err != nil {
						return err
					}
				} else if err := copyVerified(ctx, src, dest, a.UncompressedSize, a.UncompressedSHA256); err != nil {
					return err
				}
			}
		default:
			// Original upstream tar.bz2 is supported for manual transfer only. It is
			// identified by the immutable source digest pinned in the shipped index.
			if err := verifyFile(ctx, opts.From, info.Size(), m.Revision); err != nil {
				return err
			}
			f, err := os.Open(opts.From)
			if err != nil {
				return err
			}
			defer f.Close()
			if err := s.unpack(ctx, m, bzip2.NewReader(f), stage, false, required); err != nil {
				return err
			}
		}
		if !opts.Pack {
			if opts.VAD != "" {
				a, _ := s.Catalogue.Artifact(v.Files[0].Artifact)
				if strings.HasSuffix(opts.VAD, ".zst") {
					if err := verifyFile(ctx, opts.VAD, a.Size, a.SHA256); err != nil {
						return err
					}
					if err := decompress(ctx, opts.VAD, filepath.Join(vd, v.Files[0].Path), a); err != nil {
						return err
					}
				} else if err := copyVerified(ctx, opts.VAD, filepath.Join(vd, v.Files[0].Path), a.UncompressedSize, a.UncompressedSHA256); err != nil {
					return err
				}
			} else if s.Verify(ctx, v, s.Dir(v)) != nil {
				return fmt.Errorf("missing Silero VAD dependency: supply --vad with the copied file")
			}
		}
		// Verify the entire input before publishing either component.
		if err := s.Verify(ctx, m, md); err != nil {
			return err
		}
		haveVAD := opts.Pack || opts.VAD != ""
		if haveVAD {
			if err := s.Verify(ctx, v, vd); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if haveVAD {
			if err := s.publish(ctx, v, vd); err != nil {
				return err
			}
		}
		if err := s.publish(ctx, m, md); err != nil {
			return err
		}
		s.emit("installed", "", required, required, 0)
		return nil
	})
}
func (s *Store) unpack(ctx context.Context, m Model, reader io.Reader, stage string, pack bool, limit int64) error {
	expected := map[string]Artifact{}
	for _, c := range s.Components(m) {
		prefix := "model/"
		if c.ID == "silero-vad" {
			if !pack {
				continue
			}
			prefix = "vad/"
		}
		for _, f := range c.Files {
			a, _ := s.Catalogue.Artifact(f.Artifact)
			expected[prefix+f.Path] = a
		}
	}
	tr := tar.NewReader(io.LimitReader(&contextReader{ctx, reader}, limit+8<<20))
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if name != strings.TrimSuffix(h.Name, "/") || path.IsAbs(name) || strings.Contains(name, "\\") || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return fmt.Errorf("unsupported archive entry %q", h.Name)
		}
		key := name
		if !pack {
			key = "model/" + path.Base(name)
		}
		a, ok := expected[key]
		if !ok {
			if pack && name != "manifest.json" && name != "NOTICE.txt" {
				return fmt.Errorf("unexpected package file %q", name)
			}
			continue
		}
		if seen[key] || h.Size != a.UncompressedSize {
			return fmt.Errorf("invalid/duplicate package file %q", name)
		}
		seen[key] = true
		s.emit("unpacking", key, 0, limit, 0)
		if err := writeVerified(ctx, tr, filepath.Join(stage, filepath.FromSlash(key)), a.UncompressedSize, a.UncompressedSHA256); err != nil {
			return err
		}
	}
	for key := range expected {
		if !seen[key] {
			return fmt.Errorf("missing model file %s", key)
		}
	}
	return nil
}
