package store

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIndexAndFindOffset(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")

	header := StreamHeader{
		StreamID:    "stream-01",
		MID:         "1",
		Codec:       "audio/opus",
		ClockRate:   48000,
		Direction:   "recvonly",
		StartMonoNS: 123456789,
		PT:          111,
	}
	w, err := NewWriter(logPath, header)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	if err := w.Write(Record{
		RecvMonoNS: 100,
		Kind:       KindRTP,
		WireBytes:  []byte{0x01, 0x02, 0x03},
	}); err != nil {
		t.Fatalf("write first record: %v", err)
	}
	if err := w.Write(Record{
		RecvMonoNS: 50,
		Kind:       KindRTCP,
		WireBytes:  []byte{0x04},
	}); err != nil {
		t.Fatalf("write second record: %v", err)
	}
	if err := w.Write(Record{
		RecvMonoNS: 200,
		Kind:       KindRTP,
		WireBytes:  []byte{0x05, 0x06},
	}); err != nil {
		t.Fatalf("write third record: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	indexPath := logPath + ".idx"
	if err := BuildIndex(logPath, indexPath); err != nil {
		t.Fatalf("build index: %v", err)
	}

	entries, err := ReadIndex(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].RecvMonoNS != 100 || entries[1].RecvMonoNS != 50 || entries[2].RecvMonoNS != 200 {
		t.Fatalf("expected in-file order recv timestamps [100 50 200], got [%d %d %d]",
			entries[0].RecvMonoNS, entries[1].RecvMonoNS, entries[2].RecvMonoNS)
	}

	for i := 1; i < len(entries); i++ {
		if entries[i].FileOffset <= entries[i-1].FileOffset {
			t.Fatalf("index file offsets are not strictly increasing at %d: %d <= %d", i, entries[i].FileOffset, entries[i-1].FileOffset)
		}
	}

	near, ok := FindOffset(entries, 60)
	if !ok {
		t.Fatalf("expected a match for recv>=60")
	}
	if near.RecvMonoNS != 100 {
		t.Fatalf("expected sorted lower-bound recv>=60 to be 100, got %d", near.RecvMonoNS)
	}
	near, ok = FindOffset(entries, 200)
	if !ok {
		t.Fatalf("expected exact match for recv=200")
	}
	if near.RecvMonoNS != 200 {
		t.Fatalf("expected recv=200 match, got %d", near.RecvMonoNS)
	}
	if _, ok = FindOffset(entries, 201); ok {
		t.Fatal("expected no match for recv>max")
	}

	logPayloadByOffset := readPayloadsByOffset(t, logPath, entries)
	if got := string(logPayloadByOffset[entries[0].FileOffset]); got != "\x01\x02\x03" {
		t.Fatalf("expected first payload bytes, got %x", logPayloadByOffset[entries[0].FileOffset])
	}
	if got := string(logPayloadByOffset[entries[1].FileOffset]); got != "\x04" {
		t.Fatalf("expected second payload bytes, got %x", logPayloadByOffset[entries[1].FileOffset])
	}
	if got := string(logPayloadByOffset[entries[2].FileOffset]); got != "\x05\x06" {
		t.Fatalf("expected third payload bytes, got %x", logPayloadByOffset[entries[2].FileOffset])
	}
}

func TestReaderRejectsCorruptedCRC(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")

	header := StreamHeader{
		StreamID:    "stream-02",
		MID:         "audio",
		Codec:       "audio/opus",
		ClockRate:   48000,
		Direction:   "recvonly",
		StartMonoNS: 987654321,
		PT:          111,
	}
	w, err := NewWriter(logPath, header, WithCRC())
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	packet := []byte{0x10, 0x11, 0x12}
	if err := w.Write(Record{
		RecvMonoNS: 1000,
		Kind:       KindRTP,
		WireBytes:  packet,
	}); err != nil {
		t.Fatalf("write record: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	f, err := os.OpenFile(logPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open log for corruption: %v", err)
	}
	defer f.Close()
	headerSize := streamHeaderSize(t, logPath)
	payloadOffset := headerSize + 8 + 1 + 4
	if _, err := f.WriteAt([]byte{packet[0] ^ 0xFF}, int64(payloadOffset)); err != nil {
		t.Fatalf("inject corruption: %v", err)
	}

	r, err := OpenReader(logPath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	_, err = r.Next()
	if err == nil {
		t.Fatal("expected CRC mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "crc mismatch") {
		t.Fatalf("expected crc mismatch error, got %v", err)
	}
}

func TestReadIndexRejectsTruncatedFile(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "truncated.idx")
	if err := os.WriteFile(indexPath, []byte{0x00, 0x00, 0x00}, 0o644); err != nil {
		t.Fatalf("write truncated index: %v", err)
	}
	_, err := ReadIndex(indexPath)
	if err == nil {
		t.Fatal("expected truncated index read error")
	}
}

func readPayloadsByOffset(t *testing.T, path string, entries []IndexEntry) map[uint64][]byte {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log for payload decode: %v", err)
	}
	defer f.Close()

	out := make(map[uint64][]byte, len(entries))
	for _, entry := range entries {
		payload, err := readLogPayloadAtOffset(f, entry.FileOffset)
		if err != nil {
			t.Fatalf("read payload at offset %d: %v", entry.FileOffset, err)
		}
		out[entry.FileOffset] = payload
	}
	return out
}

func readLogPayloadAtOffset(f *os.File, offset uint64) ([]byte, error) {
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	var recv uint64
	if err := binary.Read(f, binary.BigEndian, &recv); err != nil {
		return nil, fmt.Errorf("read recv: %w", err)
	}
	_ = recv
	var kind [1]byte
	if _, err := io.ReadFull(f, kind[:]); err != nil {
		return nil, fmt.Errorf("read kind: %w", err)
	}
	if StreamKind(kind[0]) != KindRTP && StreamKind(kind[0]) != KindRTCP {
		return nil, fmt.Errorf("unknown kind: %d", kind[0])
	}
	var length uint32
	if err := binary.Read(f, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(f, payload); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	return payload, nil
}

func streamHeaderSize(t *testing.T, path string) int64 {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log for header parse: %v", err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)

	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(br, magic); err != nil {
		t.Fatalf("read magic: %v", err)
	}
	if string(magic) != Magic {
		t.Fatalf("bad magic while parsing header: %q", string(magic))
	}

	var version uint16
	if err := binary.Read(br, binary.BigEndian, &version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	_ = version
	var flags uint16
	if err := binary.Read(br, binary.BigEndian, &flags); err != nil {
		t.Fatalf("read flags: %v", err)
	}
	_ = flags
	var hdrLen uint32
	if err := binary.Read(br, binary.BigEndian, &hdrLen); err != nil {
		t.Fatalf("read header length: %v", err)
	}

	header := make([]byte, hdrLen)
	if _, err := io.ReadFull(br, header); err != nil {
		t.Fatalf("read header body: %v", err)
	}
	var streamHeader StreamHeader
	if err := json.Unmarshal(header, &streamHeader); err != nil {
		t.Fatalf("unmarshal stream header: %v", err)
	}
	_ = streamHeader

	return int64(len(Magic) + 2 + 2 + 4 + int(hdrLen))
}
