package main

import (
	"os"
	"path/filepath"
	"testing"

	"gocassini/pkg/core/session"
	"gocassini/pkg/core/store"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestSummarizeSegmentChurn(t *testing.T) {
	entries := []inspectedSessionStream{
		{
			packet: session.PacketStream{
				StreamID:    "s_1",
				LTID:        "p:alice:video:mid1",
				PrimarySSRC: 1001,
				PT:          96,
				StartMonoNS: 1_000_000_000,
			},
			summary: streamSummary{first: 1_000_000_000, last: 2_000_000_000},
		},
		{
			packet: session.PacketStream{
				StreamID:    "s_2",
				LTID:        "p:alice:video:mid1",
				PrimarySSRC: 1002,
				PT:          96,
				StartMonoNS: 2_200_000_000,
			},
			summary: streamSummary{first: 2_200_000_000, last: 3_200_000_000},
		},
		{
			packet: session.PacketStream{
				StreamID:    "s_3",
				LTID:        "p:alice:video:mid1",
				PrimarySSRC: 1002,
				PT:          97,
				StartMonoNS: 3_300_000_000,
			},
			summary: streamSummary{first: 3_300_000_000, last: 3_900_000_000},
		},
	}

	got := summarizeSegmentChurn(entries)
	if got.Segments != 3 {
		t.Fatalf("segments: got=%d want=3", got.Segments)
	}
	if got.SSRCChanges != 1 {
		t.Fatalf("ssrc changes: got=%d want=1", got.SSRCChanges)
	}
	if got.PTChanges != 1 {
		t.Fatalf("pt changes: got=%d want=1", got.PTChanges)
	}
	if got.MaxGapNS != 200_000_000 {
		t.Fatalf("max gap: got=%d want=%d", got.MaxGapNS, uint64(200_000_000))
	}
	if got.FirstNS != 1_000_000_000 || got.LastNS != 3_900_000_000 {
		t.Fatalf("time bounds mismatch: first=%d last=%d", got.FirstNS, got.LastNS)
	}
}

func TestStreamCloseReasons(t *testing.T) {
	tmp := t.TempDir()
	eventsPath := filepath.Join(tmp, "events.ndjson")
	body := []byte(
		`{"type":"stream_opened","stream_id":"s_1"}` + "\n" +
			`{"type":"stream_closed","stream_id":"s_1","reason":"segment-rotate:ssrc:1->2"}` + "\n" +
			`{"type":"stream_closed","stream_id":"s_2","reason":"eof"}` + "\n" +
			`{"type":"stream_closed","stream_id":"s_3","reason":"eof"}` + "\n",
	)
	if err := os.WriteFile(eventsPath, body, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	reasons, err := streamCloseReasons(eventsPath)
	if err != nil {
		t.Fatalf("streamCloseReasons: %v", err)
	}
	if reasons["eof"] != 2 {
		t.Fatalf("expected eof=2, got=%d", reasons["eof"])
	}
	if reasons["segment-rotate:ssrc:1->2"] != 1 {
		t.Fatalf("expected rotate=1, got=%d", reasons["segment-rotate:ssrc:1->2"])
	}
}

func TestInspectStreamLogCountsRTCPAndSRRate(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "video/vp8",
		ClockRate:   90000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          96,
	})
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}

	sr1 := &rtcp.SenderReport{SSRC: 1, RTPTime: 1000}
	sr2 := &rtcp.SenderReport{SSRC: 1, RTPTime: 91000}
	rr := &rtcp.ReceiverReport{SSRC: 2}
	wireSR1, err := sr1.Marshal()
	if err != nil {
		t.Fatalf("marshal sr1: %v", err)
	}
	wireSR2, err := sr2.Marshal()
	if err != nil {
		t.Fatalf("marshal sr2: %v", err)
	}
	wireRR, err := rr.Marshal()
	if err != nil {
		t.Fatalf("marshal rr: %v", err)
	}

	if err := writer.Write(store.Record{RecvMonoNS: 1_000_000_000, Kind: store.KindRTCP, WireBytes: wireSR1}); err != nil {
		t.Fatalf("write sr1: %v", err)
	}
	if err := writer.Write(store.Record{RecvMonoNS: 2_000_000_000, Kind: store.KindRTCP, WireBytes: wireSR2}); err != nil {
		t.Fatalf("write sr2: %v", err)
	}
	if err := writer.Write(store.Record{RecvMonoNS: 2_100_000_000, Kind: store.KindRTCP, WireBytes: wireRR}); err != nil {
		t.Fatalf("write rr: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	summary, err := inspectStreamLog(logPath, 1, 90000)
	if err != nil {
		t.Fatalf("inspect stream: %v", err)
	}
	if summary.rtcp != 3 {
		t.Fatalf("expected rtcp=3, got=%d", summary.rtcp)
	}
	if summary.rtcpSR != 2 {
		t.Fatalf("expected sr=2, got=%d", summary.rtcpSR)
	}
	if summary.rtcpRR != 1 {
		t.Fatalf("expected rr=1, got=%d", summary.rtcpRR)
	}
	// 90000 RTP ticks across 1 second should estimate roughly 90 kHz.
	if summary.srClockRateEstimate < 89000 || summary.srClockRateEstimate > 91000 {
		t.Fatalf("unexpected sr clock rate estimate: %.2f", summary.srClockRateEstimate)
	}
}

func TestInspectStreamLogTimelineDeltaStats(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "timeline.rtplog")
	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000010",
		Codec:       "video/vp8",
		ClockRate:   90000,
		Direction:   "recvonly",
		StartMonoNS: 1_000_000_000,
		PT:          96,
	})
	if err != nil {
		t.Fatalf("create writer: %v", err)
	}

	const ssrc = uint32(1234)
	for sec := 0; sec <= 10; sec++ {
		recv := uint64(1_000_000_000 + sec*1_000_000_000)
		timestamp := uint32(sec * 90090)
		packet := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(100 + sec),
				Timestamp:      timestamp,
				SSRC:           ssrc,
				Marker:         true,
			},
			Payload: []byte{0x10, 0x00, 0x00, 0x00, 0x9d, 0x01, 0x2a},
		}
		wire, err := packet.Marshal()
		if err != nil {
			t.Fatalf("marshal rtp packet: %v", err)
		}
		if err := writer.Write(store.Record{
			RecvMonoNS: recv,
			Kind:       store.KindRTP,
			WireBytes:  wire,
		}); err != nil {
			t.Fatalf("write rtp: %v", err)
		}
		if sec%5 == 0 {
			sr := &rtcp.SenderReport{SSRC: ssrc, RTPTime: timestamp}
			raw, err := sr.Marshal()
			if err != nil {
				t.Fatalf("marshal sr: %v", err)
			}
			if err := writer.Write(store.Record{
				RecvMonoNS: recv,
				Kind:       store.KindRTCP,
				WireBytes:  raw,
			}); err != nil {
				t.Fatalf("write sr: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	summary, err := inspectStreamLog(logPath, ssrc, 90000)
	if err != nil {
		t.Fatalf("inspect stream: %v", err)
	}
	if summary.timelineSamples == 0 {
		t.Fatalf("expected timeline samples")
	}
	if summary.timelineMeanAbsDeltaMS <= 0 {
		t.Fatalf("expected timeline mean abs delta to be positive")
	}
	if summary.timelineMaxAbsDeltaMS <= 0 {
		t.Fatalf("expected timeline max abs delta to be positive")
	}
}

func TestUnwrap32(t *testing.T) {
	last := int64(0xfffffff0)
	got := unwrap32(last, 0x00000020)
	if got <= last {
		t.Fatalf("expected unwrap to increase, got=%d last=%d", got, last)
	}
}
