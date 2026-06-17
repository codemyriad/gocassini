package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A recorder killed mid-write (SIGKILL, OOM, crash) leaves the final rtplog
// record partially flushed. Every complete record before it is intact, so the
// reader must treat the truncated tail as end-of-stream instead of failing
// the whole log.
func TestReaderTreatsTruncatedTailAsEOF(t *testing.T) {
	const lastPayloadLen = 24
	cases := []struct {
		name string
		// keepBytes is how many bytes of the final record survive the crash.
		keepBytes int64
		useCRC    bool
	}{
		{name: "mid recv timestamp", keepBytes: 4},
		{name: "missing kind", keepBytes: 8},
		{name: "mid length", keepBytes: 11},
		{name: "mid payload", keepBytes: 13 + lastPayloadLen/2},
		{name: "missing crc", keepBytes: 13 + lastPayloadLen, useCRC: true},
		{name: "mid crc", keepBytes: 13 + lastPayloadLen + 2, useCRC: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "stream.rtplog")
			writeTruncationFixtureLog(t, logPath, tc.useCRC, lastPayloadLen)

			lastRecordSize := int64(8 + 1 + 4 + lastPayloadLen)
			if tc.useCRC {
				lastRecordSize += 4
			}
			info, err := os.Stat(logPath)
			if err != nil {
				t.Fatalf("stat log: %v", err)
			}
			if err := os.Truncate(logPath, info.Size()-lastRecordSize+tc.keepBytes); err != nil {
				t.Fatalf("truncate log: %v", err)
			}

			r, err := OpenReader(logPath)
			if err != nil {
				t.Fatalf("open reader: %v", err)
			}
			defer r.Close()

			records := 0
			for {
				_, err := r.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("Next() error = %v, want clean EOF", err)
				}
				records++
			}
			if records != 2 {
				t.Fatalf("records = %d, want 2 complete records before the truncated tail", records)
			}
			if !r.Truncated() {
				t.Fatalf("Truncated() = false, want true")
			}
		})
	}
}

func TestReaderCompleteLogIsNotTruncated(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "stream.rtplog")
	writeTruncationFixtureLog(t, logPath, true, 24)

	r, err := OpenReader(logPath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer r.Close()

	records := 0
	for {
		_, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		records++
	}
	if records != 3 {
		t.Fatalf("records = %d, want 3", records)
	}
	if r.Truncated() {
		t.Fatalf("Truncated() = true, want false for a complete log")
	}
}

// writeTruncationFixtureLog writes two complete records followed by a final
// record with a payload of lastPayloadLen bytes, so tests can cut into the
// final record at known field offsets.
func writeTruncationFixtureLog(t *testing.T, path string, useCRC bool, lastPayloadLen int) {
	t.Helper()
	var opts []WriterOption
	if useCRC {
		opts = append(opts, WithCRC())
	}
	w, err := NewWriter(path, StreamHeader{
		StreamID:    "stream-01",
		Codec:       "audio/opus",
		ClockRate:   48000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          111,
	}, opts...)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	records := []Record{
		{RecvMonoNS: 100, Kind: KindRTP, WireBytes: []byte{0x01, 0x02, 0x03}},
		{RecvMonoNS: 200, Kind: KindRTCP, WireBytes: []byte{0x04, 0x05}},
		{RecvMonoNS: 300, Kind: KindRTP, WireBytes: make([]byte, lastPayloadLen)},
	}
	for idx, rec := range records {
		if err := w.Write(rec); err != nil {
			t.Fatalf("write record %d: %v", idx, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
}
