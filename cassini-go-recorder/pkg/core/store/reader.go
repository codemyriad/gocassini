package store

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

type Reader struct {
	file      *os.File
	buffer    *bufio.Reader
	header    StreamHeader
	flags     uint16
	closed    bool
	useCRC    bool
	truncated bool
}

type Header struct {
	Version uint16
	Flags   uint16
	Stream  StreamHeader
}

func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open stream log: %w", err)
	}
	br := bufio.NewReaderSize(f, 1<<20)

	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(br, magic); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read magic: %w", err)
	}
	if string(magic) != Magic {
		_ = f.Close()
		return nil, fmt.Errorf("bad magic: %q", string(magic))
	}

	var version uint16
	if err := binary.Read(br, binary.BigEndian, &version); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read version: %w", err)
	}
	var flags uint16
	if err := binary.Read(br, binary.BigEndian, &flags); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read flags: %w", err)
	}

	var hdrLen uint32
	if err := binary.Read(br, binary.BigEndian, &hdrLen); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read header length: %w", err)
	}
	raw := make([]byte, hdrLen)
	if _, err := io.ReadFull(br, raw); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("read stream header: %w", err)
	}
	var header StreamHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("unmarshal stream header: %w", err)
	}
	header.Version = version

	return &Reader{
		file:   f,
		buffer: br,
		header: header,
		flags:  flags,
		useCRC: flags&FlagUseCRC != 0,
	}, nil
}

func (r *Reader) Header() Header {
	return Header{
		Version: r.header.Version,
		Flags:   r.flags,
		Stream:  r.header,
	}
}

func (r *Reader) Next() (Record, error) {
	var recv uint64
	if err := binary.Read(r.buffer, binary.BigEndian, &recv); err != nil {
		if errors.Is(err, io.EOF) {
			return Record{}, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Record{}, r.markTruncatedTail()
		}
		return Record{}, fmt.Errorf("read recv timestamp: %w", err)
	}

	k, err := r.buffer.ReadByte()
	if err != nil {
		if isTruncatedTail(err) {
			return Record{}, r.markTruncatedTail()
		}
		return Record{}, fmt.Errorf("read record kind: %w", err)
	}
	var n uint32
	if err := binary.Read(r.buffer, binary.BigEndian, &n); err != nil {
		if isTruncatedTail(err) {
			return Record{}, r.markTruncatedTail()
		}
		return Record{}, fmt.Errorf("read record length: %w", err)
	}
	if n == 0 {
		return Record{}, fmt.Errorf("record length is zero")
	}
	wire := make([]byte, n)
	if _, err := io.ReadFull(r.buffer, wire); err != nil {
		if isTruncatedTail(err) {
			return Record{}, r.markTruncatedTail()
		}
		return Record{}, fmt.Errorf("read record bytes: %w", err)
	}

	if r.useCRC {
		var c uint32
		if err := binary.Read(r.buffer, binary.BigEndian, &c); err != nil {
			if isTruncatedTail(err) {
				return Record{}, r.markTruncatedTail()
			}
			return Record{}, fmt.Errorf("read crc: %w", err)
		}
		if crc32.ChecksumIEEE(wire) != c {
			return Record{}, fmt.Errorf("crc mismatch")
		}
	}

	return Record{
		RecvMonoNS: recv,
		Kind:       StreamKind(k),
		WireBytes:  wire,
	}, nil
}

// Truncated reports whether the log ended in a partially written final
// record. A recorder killed mid-write (SIGKILL, OOM, crash) loses at most
// the buffered tail of each rtplog; every complete record before it is
// intact, so Next treats a truncated tail as end-of-stream instead of an
// error to keep crashed sessions rebuildable.
func (r *Reader) Truncated() bool {
	return r.truncated
}

// markTruncatedTail flags the partially written final record and reports
// clean end-of-stream to the caller.
func (r *Reader) markTruncatedTail() error {
	r.truncated = true
	return io.EOF
}

// isTruncatedTail matches the short-read errors a crash-truncated final
// record produces. io.EOF mid-record means a field is missing entirely;
// io.ErrUnexpectedEOF means a field was cut short.
func isTruncatedTail(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (r *Reader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}
