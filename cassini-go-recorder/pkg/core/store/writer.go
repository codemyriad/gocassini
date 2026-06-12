package store

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"sync"
)

// ErrWriterClosed is returned by Write once the writer has been closed.
// The writer rejects the record before touching the file, so callers that
// race a deliberate Close (e.g. stream rotation) can match it with
// errors.Is to tell a benign drop from a media-losing I/O failure.
var ErrWriterClosed = errors.New("writer is closed")

type Writer struct {
	mu     sync.Mutex
	bw     *bufio.Writer
	file   *os.File
	closed bool
	useCRC bool
	flags  uint16
	offset int64
}

type WriterOption func(*Writer)

func WithCRC() WriterOption {
	return func(w *Writer) {
		w.useCRC = true
		w.flags |= FlagUseCRC
	}
}

func NewWriter(path string, header StreamHeader, opts ...WriterOption) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create stream log: %w", err)
	}
	bw := bufio.NewWriter(f)

	w := &Writer{
		bw:    bw,
		file:  f,
		flags: 0,
	}
	for _, opt := range opts {
		opt(w)
	}

	header.Version = CurrentVer
	headerBytes, err := json.Marshal(header)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("marshal stream header: %w", err)
	}
	if _, err := bw.WriteString(Magic); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write magic: %w", err)
	}
	if err := binary.Write(bw, binary.BigEndian, uint16(CurrentVer)); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write header version: %w", err)
	}
	if err := binary.Write(bw, binary.BigEndian, w.flags); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write header flags: %w", err)
	}
	if err := binary.Write(bw, binary.BigEndian, uint32(len(headerBytes))); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write header length: %w", err)
	}
	if _, err := bw.Write(headerBytes); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write header payload: %w", err)
	}

	// Track stream end offset; used for optional indexing.
	w.offset = int64(len(Magic) + 2 + 2 + 4 + len(headerBytes))
	return w, nil
}

func (w *Writer) Write(rec Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrWriterClosed
	}

	if rec.Kind != KindRTP && rec.Kind != KindRTCP {
		return fmt.Errorf("unsupported record kind: %d", rec.Kind)
	}

	if err := binary.Write(w.bw, binary.BigEndian, rec.RecvMonoNS); err != nil {
		return fmt.Errorf("write record timestamp: %w", err)
	}
	if err := w.bw.WriteByte(byte(rec.Kind)); err != nil {
		return fmt.Errorf("write record kind: %w", err)
	}
	if err := binary.Write(w.bw, binary.BigEndian, uint32(len(rec.WireBytes))); err != nil {
		return fmt.Errorf("write record length: %w", err)
	}
	if _, err := w.bw.Write(rec.WireBytes); err != nil {
		return fmt.Errorf("write record bytes: %w", err)
	}

	if w.useCRC {
		crc := crc32.ChecksumIEEE(rec.WireBytes)
		if err := binary.Write(w.bw, binary.BigEndian, crc); err != nil {
			return fmt.Errorf("write record crc: %w", err)
		}
	}

	w.offset += 8 + 1 + 4 + int64(len(rec.WireBytes))
	if w.useCRC {
		w.offset += 4
	}
	return nil
}

func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bw.Flush()
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.bw.Flush(); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}
