package store

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestWriterRoundTripAndIndex(t *testing.T) {
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
		t.Fatalf("write rtp: %v", err)
	}
	if err := w.Write(Record{
		RecvMonoNS: 200,
		Kind:       KindRTCP,
		WireBytes:  []byte{0x04, 0x05},
	}); err != nil {
		t.Fatalf("write rtcp: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	r, err := OpenReader(logPath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	h := r.Header()
	if h.Stream.StreamID != header.StreamID {
		t.Fatalf("stream id mismatch: got=%q want=%q", h.Stream.StreamID, header.StreamID)
	}
	if h.Stream.ClockRate != header.ClockRate {
		t.Fatalf("clock rate mismatch: got=%d want=%d", h.Stream.ClockRate, header.ClockRate)
	}

	rec1, err := r.Next()
	if err != nil {
		t.Fatalf("read first record: %v", err)
	}
	if rec1.Kind != KindRTP || rec1.RecvMonoNS != 100 || len(rec1.WireBytes) != 3 {
		t.Fatalf("unexpected first record: %#v", rec1)
	}
	rec2, err := r.Next()
	if err != nil {
		t.Fatalf("read second record: %v", err)
	}
	if rec2.Kind != KindRTCP || rec2.RecvMonoNS != 200 || len(rec2.WireBytes) != 2 {
		t.Fatalf("unexpected second record: %#v", rec2)
	}

	indexPath := logPath + ".idx"
	if err := BuildIndex(logPath, indexPath); err != nil {
		t.Fatalf("build index: %v", err)
	}
	idx, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("index file missing: %v", err)
	}
	if idx.Size()%16 != 0 {
		t.Fatalf("index entry size invalid: %d", idx.Size())
	}
	indexCount := idx.Size() / 16
	if indexCount != 2 {
		t.Fatalf("unexpected index entry count: %d", indexCount)
	}

	idxFile, err := os.Open(indexPath)
	if err != nil {
		t.Fatalf("open index file: %v", err)
	}
	defer idxFile.Close()

	var recv, offset uint64
	if err := binary.Read(idxFile, binary.BigEndian, &recv); err != nil {
		t.Fatalf("read idx recv: %v", err)
	}
	if err := binary.Read(idxFile, binary.BigEndian, &offset); err != nil {
		t.Fatalf("read idx offset: %v", err)
	}
	if recv != 100 || offset == 0 {
		t.Fatalf("unexpected first index values: recv=%d offset=%d", recv, offset)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
}
