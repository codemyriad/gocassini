package cassette

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestWriterRoundTrip(t *testing.T) {
	out := filepath.Join(t.TempDir(), "test.csr")

	w, err := NewWriter(out)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	trackRef, err := w.RegisterTrack(TrackMetadata{
		ParticipantName: "Silvio",
		ParticipantID:   "user-1",
		RemoteSessionID: "session-abc",
		TrackID:         "track-video",
		StreamID:        "stream-main",
		Kind:            "video",
		Codec:           "video/VP8",
		ClockRate:       90000,
		SSRC:            12345,
		PayloadType:     96,
	})
	if err != nil {
		t.Fatalf("register track: %v", err)
	}

	pkt1 := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 100, Timestamp: 1000, SSRC: 12345, PayloadType: 96}, Payload: []byte{0x01, 0x02, 0x03}}
	pkt2 := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 101, Timestamp: 2000, SSRC: 12345, PayloadType: 96}, Payload: []byte{0x04, 0x05, 0x06}}

	at1 := time.Unix(100, 1)
	at2 := time.Unix(100, 2)

	if err := w.WriteRTP(trackRef, pkt1, at1); err != nil {
		t.Fatalf("write rtp pkt1: %v", err)
	}
	if err := w.WriteRTP(trackRef, pkt2, at2); err != nil {
		t.Fatalf("write rtp pkt2: %v", err)
	}

	if err := w.EndTrack(trackRef, "test-done", time.Unix(101, 0)); err != nil {
		t.Fatalf("end track: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	header, records, err := ReadAll(out)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if header.Version != Version {
		t.Fatalf("unexpected version: %d", header.Version)
	}

	if len(records) != 4 {
		t.Fatalf("expected 4 records, got %d", len(records))
	}

	if records[0].Type != RecordTrackStart || records[0].TrackStart == nil {
		t.Fatalf("first record is not track start")
	}
	if records[0].TrackStart.ParticipantName != "Silvio" {
		t.Fatalf("unexpected participant: %q", records[0].TrackStart.ParticipantName)
	}

	if records[1].RTP == nil || records[2].RTP == nil {
		t.Fatalf("missing RTP records")
	}
	if records[1].RTP.ParsedRTP.SequenceNumber != 100 {
		t.Fatalf("unexpected sequence pkt1: %d", records[1].RTP.ParsedRTP.SequenceNumber)
	}
	if records[2].RTP.ParsedRTP.SequenceNumber != 101 {
		t.Fatalf("unexpected sequence pkt2: %d", records[2].RTP.ParsedRTP.SequenceNumber)
	}

	if records[3].Type != RecordTrackEnd || records[3].TrackEnd == nil {
		t.Fatalf("last record is not track end")
	}
	if records[3].TrackEnd.Reason != "test-done" {
		t.Fatalf("unexpected end reason: %q", records[3].TrackEnd.Reason)
	}
}
