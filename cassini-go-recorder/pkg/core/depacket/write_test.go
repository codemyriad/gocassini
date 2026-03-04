package depacket

import (
	"os"
	"path/filepath"
	"testing"

	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
)

func TestWriteElementaryFromRTPLogOpus(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "audio.rtplog")
	outPath := filepath.Join(tmp, "audio.ogg")
	if err := writeSampleLog(logPath, 111, true); err != nil {
		t.Fatalf("write sample log: %v", err)
	}

	result, err := WriteElementaryFromRTPLog(logPath, "audio/opus", 48000, outPath)
	if err != nil {
		t.Fatalf("write elementary: %v", err)
	}
	if result.RTPPackets != 2 {
		t.Fatalf("rtp packet count: got=%d want=2", result.RTPPackets)
	}
	if result.RTCPPackets != 1 {
		t.Fatalf("rtcp packet count: got=%d want=1", result.RTCPPackets)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() <= 0 {
		t.Fatalf("empty output file")
	}
}

func TestWriteElementaryFromRTPLogVP8(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "video.rtplog")
	outPath := filepath.Join(tmp, "video.ivf")
	if err := writeSampleLog(logPath, 96, false); err != nil {
		t.Fatalf("write sample log: %v", err)
	}

	result, err := WriteElementaryFromRTPLog(logPath, "video/vp8", 90000, outPath)
	if err != nil {
		t.Fatalf("write elementary: %v", err)
	}
	if result.RTPPackets != 2 {
		t.Fatalf("rtp packet count: got=%d want=2", result.RTPPackets)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() <= 32 {
		t.Fatalf("output too small: size=%d", info.Size())
	}
}

func TestWriteElementaryFromRTPLogUnsupportedCodec(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	if err := writeSampleLog(logPath, 96, false); err != nil {
		t.Fatalf("write sample log: %v", err)
	}
	_, err := WriteElementaryFromRTPLog(logPath, "video/h264", 90000, filepath.Join(tmp, "out.h264"))
	if err == nil {
		t.Fatalf("expected unsupported codec error")
	}
}

func writeSampleLog(path string, payloadType uint8, opus bool) error {
	writer, err := store.NewWriter(path, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "sample",
		ClockRate:   90000,
		Direction:   "recvonly",
		StartMonoNS: 1000,
		PT:          payloadType,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = writer.Close()
	}()

	packet1 := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType,
			SequenceNumber: 100,
			Timestamp:      1000,
			SSRC:           7777,
			Marker:         true,
		},
		Payload: samplePayload(opus),
	}
	packet2 := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType,
			SequenceNumber: 101,
			Timestamp:      2000,
			SSRC:           7777,
			Marker:         true,
		},
		Payload: samplePayload(opus),
	}
	wire1, err := packet1.Marshal()
	if err != nil {
		return err
	}
	wire2, err := packet2.Marshal()
	if err != nil {
		return err
	}
	if err := writer.Write(store.Record{RecvMonoNS: 2000, Kind: store.KindRTP, WireBytes: wire1}); err != nil {
		return err
	}
	if err := writer.Write(store.Record{RecvMonoNS: 3000, Kind: store.KindRTP, WireBytes: wire2}); err != nil {
		return err
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 3500,
		Kind:       store.KindRTCP,
		WireBytes:  []byte{0x80, 0xc9, 0x00, 0x01},
	}); err != nil {
		return err
	}
	return writer.Close()
}

func samplePayload(opus bool) []byte {
	if opus {
		return []byte{0xf8, 0xff, 0xfe}
	}
	// VP8 payload descriptor (S=1, PID=0) + minimal keyframe header bytes.
	return []byte{0x10, 0x00, 0x00, 0x00, 0x9d, 0x01, 0x2a, 0x00, 0x00, 0x00}
}
