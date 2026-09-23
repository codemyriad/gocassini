package modelstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

type checkpoint struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	ETag   string `json:"etag"`
}

// Acquire installs bytes only. Readiness is a separate target-runtime probe;
// pack uses this operation without loading a recognizer or requiring a GPU.
func (s *Store) Acquire(ctx context.Context, m Model) error {
	return s.locked(ctx, func() error {
		var total, done, reused int64
		for _, c := range s.Components(m) {
			d, _ := s.Catalogue.Sizes(c)
			total += d
		}
		for _, c := range s.Components(m) {
			if s.Installed(c) == nil {
				d, _ := s.Catalogue.Sizes(c)
				done += d
				reused += d
				continue
			}
			stage := filepath.Join(s.Root, "downloads", c.Revision, "files")
			if err := os.MkdirAll(stage, 0755); err != nil {
				return err
			}
			for _, f := range c.Files {
				a, _ := s.Catalogue.Artifact(f.Artifact)
				dest := filepath.Join(stage, f.Path)
				if verifyFile(ctx, dest, a.UncompressedSize, a.UncompressedSHA256) == nil {
					done += a.Size
					reused += a.Size
					continue
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				// A different installed model can share e.g. the exact tokens file.
				reusedPath := s.existingFile(ctx, a)
				if reusedPath != "" {
					if err := copyVerified(ctx, reusedPath, dest, a.UncompressedSize, a.UncompressedSHA256); err != nil {
						return err
					}
					done += a.Size
					reused += a.Size
					continue
				}
				compressed := filepath.Join(filepath.Dir(stage), a.SHA256+".part")
				remaining := a.Size
				if st, e := os.Stat(compressed); e == nil && st.Size() <= a.Size {
					remaining -= st.Size()
				}
				if err := checkSpace(s.Root, remaining+a.UncompressedSize); err != nil {
					return err
				}
				if err := s.download(ctx, a, compressed, func(n int64) { s.emit("downloading", f.Path, done+n, total, reused) }); err != nil {
					return err
				}
				s.emit("verifying", f.Path, done+a.Size, total, reused)
				if err := verifyFile(ctx, compressed, a.Size, a.SHA256); err != nil {
					os.Remove(compressed)
					os.Remove(compressed + ".json")
					return err
				}
				s.emit("unpacking", f.Path, done+a.Size, total, reused)
				if err := decompress(ctx, compressed, dest, a); err != nil {
					return err
				}
				os.Remove(compressed)
				os.Remove(compressed + ".json")
				done += a.Size
			}
			s.emit("verifying", c.ID, done, total, reused)
			if err := s.publish(ctx, c, stage); err != nil {
				return err
			}
			// publish may have reused a completed directory after a prior crash.
			os.RemoveAll(filepath.Dir(stage))
		}
		s.emit("installed", "", total, total, reused)
		return nil
	})
}
func (s *Store) existingFile(ctx context.Context, a Artifact) string {
	for _, m := range s.Catalogue.Models {
		if s.Installed(m) != nil {
			continue
		}
		for _, f := range m.Files {
			other, _ := s.Catalogue.Artifact(f.Artifact)
			if other.UncompressedSHA256 == a.UncompressedSHA256 {
				path := filepath.Join(s.Dir(m), f.Path)
				if verifyFile(ctx, path, a.UncompressedSize, a.UncompressedSHA256) == nil {
					return path
				}
			}
		}
	}
	return ""
}

// downloadAttempts bounds the automatic retries after a dropped connection, a
// stalled transfer or a server error. With a strong ETag each attempt resumes
// from the bytes already on disk, so one reset no longer costs a whole model
// install; without one it safely starts the file again.
const downloadAttempts = 6

// downloadStallTimeout aborts an attempt that receives no bytes for this long.
// It replaces a cap on the whole transfer, which a slow link could never meet.
var downloadStallTimeout = 60 * time.Second

var downloadRetryDelay = func(attempt int) time.Duration { return time.Duration(attempt) * 2 * time.Second }

// retryableError marks a download failure that another attempt may cure.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

func (s *Store) download(ctx context.Context, a Artifact, path string, progress func(int64)) error {
	var err error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		err = s.downloadOnce(ctx, a, path, progress)
		var retry retryableError
		if err == nil || ctx.Err() != nil || !errors.As(err, &retry) || attempt == downloadAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(downloadRetryDelay(attempt)):
		}
	}
	return err
}

func (s *Store) downloadOnce(ctx context.Context, a Artifact, path string, progress func(int64)) error {
	if verifyFile(ctx, path, a.Size, a.SHA256) == nil {
		progress(a.Size)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.NoDownload {
		return fmt.Errorf("model downloads are disabled; import local files instead (missing %s)", filepath.Base(a.Key))
	}
	url := a.URL
	if s.URL != nil {
		url = s.URL(a)
	}
	var cp checkpoint
	b, _ := os.ReadFile(path + ".json")
	_ = json.Unmarshal(b, &cp)
	var offset int64
	if st, err := os.Stat(path); err == nil && cp.URL == url && cp.SHA256 == a.SHA256 && cp.ETag != "" && !strings.HasPrefix(cp.ETag, "W/") && st.Size() < a.Size {
		offset = st.Size()
	}
	// A full-size but corrupt prefix, missing validator, or changed catalogue
	// never participates in a range request.
	if offset == 0 {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		attemptCtx, cancel := context.WithCancel(ctx)
		stall := time.AfterFunc(downloadStallTimeout, cancel)
		stalled := func() bool { return attemptCtx.Err() != nil && ctx.Err() == nil }
		release := func() { stall.Stop(); cancel() }
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
		if err != nil {
			release()
			return err
		}
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			req.Header.Set("If-Range", cp.ETag)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			release()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return retryableError{fmt.Errorf("download %s: %w", filepath.Base(a.Key), err)}
		}
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
			resp.Body.Close()
			release()
			offset = 0
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			release()
			err := fmt.Errorf("download %s: HTTP %d", filepath.Base(a.Key), resp.StatusCode)
			if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
				return retryableError{err}
			}
			return err
		}
		defer release()
		if resp.StatusCode == http.StatusPartialContent {
			expected := fmt.Sprintf("bytes %d-%d/%d", offset, a.Size-1, a.Size)
			if resp.Header.Get("Content-Range") != expected || (cp.ETag != "" && offset > 0 && resp.Header.Get("ETag") != cp.ETag) {
				resp.Body.Close()
				return fmt.Errorf("invalid resume response for %s", filepath.Base(a.Key))
			}
		} else {
			offset = 0
		}
		if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
			resp.Body.Close()
			return fmt.Errorf("unexpected content encoding for %s", filepath.Base(a.Key))
		}
		if resp.ContentLength >= 0 && resp.ContentLength != a.Size-offset {
			resp.Body.Close()
			return fmt.Errorf("unexpected download length: got %d, want %d", resp.ContentLength, a.Size-offset)
		}
		cp = checkpoint{url, a.SHA256, resp.Header.Get("ETag")}
		if err := writeJSON(path+".json", cp); err != nil {
			resp.Body.Close()
			return err
		}
		flags := os.O_WRONLY | os.O_CREATE
		if offset == 0 {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		f, err := os.OpenFile(path, flags, 0600)
		if err != nil {
			resp.Body.Close()
			return err
		}
		count := offset
		last := time.Time{}
		progress(count)
		buf := make([]byte, 128<<10)
		var readErr error
		reader := io.LimitReader(resp.Body, a.Size-offset+1)
		for {
			n, e := reader.Read(buf)
			if n > 0 {
				if count+int64(n) > a.Size {
					readErr = fmt.Errorf("download exceeds expected size %s", strconv.FormatInt(a.Size, 10))
					break
				}
				stall.Reset(downloadStallTimeout)
				wn, we := f.Write(buf[:n])
				count += int64(wn)
				if we != nil {
					readErr = we
					break
				}
				if time.Since(last) >= 250*time.Millisecond {
					progress(count)
					last = time.Now()
				}
			}
			if e != nil {
				if e != io.EOF {
					// The connection dropped or stalled; the bytes so far are kept.
					readErr = retryableError{fmt.Errorf("download %s: %w", filepath.Base(a.Key), e)}
					if stalled() {
						readErr = retryableError{fmt.Errorf("download %s: no data for %s", filepath.Base(a.Key), downloadStallTimeout)}
					}
					if ctx.Err() != nil {
						readErr = ctx.Err()
					}
				}
				break
			}
		}
		syncErr := f.Sync()
		closeErr := f.Close()
		resp.Body.Close()
		progress(count)
		if readErr != nil {
			return readErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if count != a.Size {
			return retryableError{fmt.Errorf("incomplete download: %d of %d bytes; retry to resume", count, a.Size)}
		}
		return nil
	}
	return fmt.Errorf("could not resume %s", filepath.Base(a.Key))
}
func decompress(ctx context.Context, src, dest string, a Artifact) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	decoder, err := zstd.NewReader(in, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return err
	}
	defer decoder.Close()
	return writeVerified(ctx, decoder, dest, a.UncompressedSize, a.UncompressedSHA256)
}
func copyVerified(ctx context.Context, src, dest string, size int64, hash string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeVerified(ctx, in, dest, size, hash)
}
func writeVerified(ctx context.Context, r io.Reader, dest string, size int64, hash string) error {
	// Recheck immediately before expansion/copy, including reuse of a file
	// from another revision that needs no network transfer.
	if err := checkSpace(filepath.Dir(dest), size); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(dest), ".file-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	n, err := io.Copy(out, io.LimitReader(&contextReader{ctx, r}, size+1))
	if err == nil && n != size {
		err = fmt.Errorf("invalid size for %s: got %d, expected %d", filepath.Base(dest), n, size)
	}
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := verifyFile(ctx, tmp, size, hash); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(dest), err)
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
