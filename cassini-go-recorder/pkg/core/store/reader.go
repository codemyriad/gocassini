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
	file   *os.File
	buffer *bufio.Reader
	header StreamHeader
	flags  uint16
	closed bool
	useCRC bool
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
		return Record{}, fmt.Errorf("read recv timestamp: %w", err)
	}

	k, err := r.buffer.ReadByte()
	if err != nil {
		return Record{}, fmt.Errorf("read record kind: %w", err)
	}
	var n uint32
	if err := binary.Read(r.buffer, binary.BigEndian, &n); err != nil {
		return Record{}, fmt.Errorf("read record length: %w", err)
	}
	if n == 0 {
		return Record{}, fmt.Errorf("record length is zero")
	}
	wire := make([]byte, n)
	if _, err := io.ReadFull(r.buffer, wire); err != nil {
		return Record{}, fmt.Errorf("read record bytes: %w", err)
	}

	if r.useCRC {
		var c uint32
		if err := binary.Read(r.buffer, binary.BigEndian, &c); err != nil {
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
