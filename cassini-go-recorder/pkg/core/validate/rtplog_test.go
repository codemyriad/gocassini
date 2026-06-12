package validate

import (
	"os"
	"path/filepath"
	"testing"

	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
)

func TestCheckLogDetectsNonMonotonicAndPayloadTypeMismatch(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "audio/opus",
		ClockRate:   48000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          111,
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	if err := writer.Write(store.Record{
		RecvMonoNS: 2_000_000_000,
		Kind:       store.KindRTP,
		WireBytes:  mustPacket(t, 111, 1000, 1, 77),
	}); err != nil {
		t.Fatalf("write first record: %v", err)
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 1_000_000_000,
		Kind:       store.KindRTP,
		WireBytes:  mustPacket(t, 96, 1010, 2, 77),
	}); err != nil {
		t.Fatalf("write second record: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	report, err := CheckLog(logPath)
	if err != nil {
		t.Fatalf("check log: %v", err)
	}
	if report.IssueCount == 0 {
		t.Fatalf("expected issues, got none")
	}

	foundNonMonotonic := false
	foundPTMismatch := false
	for _, issue := range report.Issues {
		if issue.Code == IssueRecvNonMonotonic {
			foundNonMonotonic = true
		}
		if issue.Code == IssuePayloadTypeChange {
			foundPTMismatch = true
		}
	}
	if !foundNonMonotonic {
		t.Fatalf("expected recv_non_monotonic issue, got=%v", report.Issues)
	}
	if !foundPTMismatch {
		t.Fatalf("expected payload_type_mismatch issue, got=%v", report.Issues)
	}
}

func TestCheckLogAcceptsWellFormedLog(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000002",
		Codec:       "video/vp8",
		ClockRate:   90000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          96,
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	if err := writer.Write(store.Record{
		RecvMonoNS: 1_100_000_000,
		Kind:       store.KindRTP,
		WireBytes:  mustPacket(t, 96, 2000, 11, 12345),
	}); err != nil {
		t.Fatalf("write first record: %v", err)
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 1_200_000_000,
		Kind:       store.KindRTP,
		WireBytes:  mustPacket(t, 96, 2030, 12, 12345),
	}); err != nil {
		t.Fatalf("write second record: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	report, err := CheckLog(logPath)
	if err != nil {
		t.Fatalf("check log: %v", err)
	}
	if report.IssueCount != 0 {
		t.Fatalf("expected no issues, got=%v", report.Issues)
	}
}

func TestCheckLogFlagsTruncatedTailWithoutFailing(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000003",
		Codec:       "video/vp8",
		ClockRate:   90000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          96,
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 1_100_000_000,
		Kind:       store.KindRTP,
		WireBytes:  mustPacket(t, 96, 2000, 11, 12345),
	}); err != nil {
		t.Fatalf("write first record: %v", err)
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 1_200_000_000,
		Kind:       store.KindRTP,
		WireBytes:  mustPacket(t, 96, 2030, 12, 12345),
	}); err != nil {
		t.Fatalf("write second record: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	// Cut into the final record to simulate a recorder killed mid-write.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if err := os.Truncate(logPath, info.Size()-5); err != nil {
		t.Fatalf("truncate log: %v", err)
	}

	report, err := CheckLog(logPath)
	if err != nil {
		t.Fatalf("check log on truncated tail: %v", err)
	}
	if report.RTP != 1 {
		t.Fatalf("rtp records = %d, want 1 complete record", report.RTP)
	}
	foundTruncated := false
	for _, issue := range report.Issues {
		if issue.Code == IssueTruncatedTail {
			foundTruncated = true
		}
	}
	if !foundTruncated {
		t.Fatalf("expected truncated_tail issue, got=%v", report.Issues)
	}
}

func mustPacket(t *testing.T, payloadType uint8, timestamp uint32, seq uint16, ssrc uint32) []byte {
	t.Helper()
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType,
			SequenceNumber: seq,
			Timestamp:      timestamp,
			SSRC:           ssrc,
		},
		Payload: []byte{0x01, 0x02, 0x03},
	}
	wire, err := packet.Marshal()
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	return wire
}
