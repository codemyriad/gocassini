package store

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type IndexEntry struct {
	RecvMonoNS uint64
	FileOffset uint64
}

// BuildIndex scans an rtplog and writes every record position to a sidecar index.
// The index contains uint64 recvMonoNS and uint64 offset pairs.
func BuildIndex(logPath, indexPath string) error {
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("open stream log: %w", err)
	}
	defer f.Close()

	headerMagic := make([]byte, len(Magic))
	if _, err := io.ReadFull(f, headerMagic); err != nil {
		return fmt.Errorf("read magic: %w", err)
	}
	if string(headerMagic) != Magic {
		return fmt.Errorf("bad magic: %q", string(headerMagic))
	}

	var ver uint16
	if err := binary.Read(f, binary.BigEndian, &ver); err != nil {
		return fmt.Errorf("read version: %w", err)
	}
	_ = ver
	var flags uint16
	if err := binary.Read(f, binary.BigEndian, &flags); err != nil {
		return fmt.Errorf("read flags: %w", err)
	}
	var hdrLen uint32
	if err := binary.Read(f, binary.BigEndian, &hdrLen); err != nil {
		return fmt.Errorf("read header length: %w", err)
	}

	hdrBuf := make([]byte, hdrLen)
	if _, err := io.ReadFull(f, hdrBuf); err != nil {
		return fmt.Errorf("read stream header: %w", err)
	}
	var hdr StreamHeader
	if err := json.Unmarshal(hdrBuf, &hdr); err != nil {
		return fmt.Errorf("unmarshal stream header: %w", err)
	}
	_ = hdr

	out, err := os.Create(indexPath)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	defer out.Close()

	offset := uint64(len(Magic) + 2 + 2 + 4 + len(hdrBuf))
	for {
		var recv uint64
		if err := binary.Read(f, binary.BigEndian, &recv); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read recv timestamp: %w", err)
		}
		recOffset := offset
		k, err := f.Read(make([]byte, 1))
		if err != nil {
			return fmt.Errorf("read record kind: %w", err)
		}
		_ = k
		var n uint32
		if err := binary.Read(f, binary.BigEndian, &n); err != nil {
			return fmt.Errorf("read record length: %w", err)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(f, payload); err != nil {
			return fmt.Errorf("read record payload: %w", err)
		}
		offset += 8 + 1 + 4 + uint64(n)

		if flags&FlagUseCRC != 0 {
			var crc uint32
			if err := binary.Read(f, binary.BigEndian, &crc); err != nil {
				return fmt.Errorf("read crc: %w", err)
			}
			_ = crc
			offset += 4
		}

		if err := binary.Write(out, binary.BigEndian, recv); err != nil {
			return fmt.Errorf("write index timestamp: %w", err)
		}
		if err := binary.Write(out, binary.BigEndian, recOffset); err != nil {
			return fmt.Errorf("write index offset: %w", err)
		}
	}
}
