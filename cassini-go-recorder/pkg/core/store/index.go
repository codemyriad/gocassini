package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

type IndexEntry struct {
	RecvMonoNS uint64
	FileOffset uint64
}

// ReadIndex reads a sidecar index and returns entries in file order.
func ReadIndex(path string) ([]IndexEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	defer f.Close()

	entries := make([]IndexEntry, 0)
	for {
		var recv, offset uint64
		if err := binary.Read(f, binary.BigEndian, &recv); err != nil {
			if errors.Is(err, io.EOF) {
				return entries, nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("index truncated")
			}
			return nil, fmt.Errorf("read index recv timestamp: %w", err)
		}
		if err := binary.Read(f, binary.BigEndian, &offset); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("index truncated")
			}
			return nil, fmt.Errorf("read index file offset: %w", err)
		}
		entries = append(entries, IndexEntry{RecvMonoNS: recv, FileOffset: offset})
	}
}

// FindOffset returns the first index entry at or after recvMonoNS.
// If no entry satisfies the threshold, ok is false.
func FindOffset(entries []IndexEntry, recvMonoNS uint64) (IndexEntry, bool) {
	if len(entries) == 0 {
		return IndexEntry{}, false
	}

	sorted := make([]IndexEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].RecvMonoNS == sorted[j].RecvMonoNS {
			return sorted[i].FileOffset < sorted[j].FileOffset
		}
		return sorted[i].RecvMonoNS < sorted[j].RecvMonoNS
	})

	idx := sort.Search(len(sorted), func(i int) bool {
		return sorted[i].RecvMonoNS >= recvMonoNS
	})
	if idx >= len(sorted) {
		return IndexEntry{}, false
	}
	return sorted[idx], true
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
