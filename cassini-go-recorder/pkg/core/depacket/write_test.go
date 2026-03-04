package depacket

import (
	"encoding/binary"
	"io"
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
	if err := writeSampleLog(logPath, 111, sampleOpusPayload); err != nil {
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
	if err := writeSampleLog(logPath, 96, sampleVP8Payload); err != nil {
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

func TestWriteElementaryFromRTPLogVP8PreservesRTPClockPTS(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "video-pts.rtplog")
	outPath := filepath.Join(tmp, "video-pts.ivf")
	timestamps := []uint32{90_000, 93_000, 99_000, 117_000}
	if err := writeSampleLogWithTimestamps(logPath, 96, sampleVP8Payload, timestamps); err != nil {
		t.Fatalf("write sample log: %v", err)
	}

	if _, err := WriteElementaryFromRTPLog(logPath, "video/vp8", 90_000, outPath); err != nil {
		t.Fatalf("write elementary: %v", err)
	}

	pts, timebaseNum, timebaseDen, err := readIVFFramePTS(outPath)
	if err != nil {
		t.Fatalf("read ivf pts: %v", err)
	}
	if len(pts) != len(timestamps) {
		t.Fatalf("frame count mismatch: got=%d want=%d", len(pts), len(timestamps))
	}
	if timebaseNum != 1 || timebaseDen != 90_000 {
		t.Fatalf("unexpected ivf timebase: got=%d/%d want=1/90000", timebaseNum, timebaseDen)
	}

	base := timestamps[0]
	for i, got := range pts {
		want := uint64(timestamps[i] - base)
		if got != want {
			t.Fatalf("frame %d pts mismatch: got=%d want=%d", i, got, want)
		}
	}
}

func TestWriteElementaryFromRTPLogVP9(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "video-vp9.rtplog")
	outPath := filepath.Join(tmp, "video-vp9.ivf")
	if err := writeSampleLog(logPath, 98, sampleVP9Payload); err != nil {
		t.Fatalf("write sample log: %v", err)
	}
	result, err := WriteElementaryFromRTPLog(logPath, "video/vp9", 90000, outPath)
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

func TestWriteElementaryFromRTPLogH264(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "video-h264.rtplog")
	outPath := filepath.Join(tmp, "video-h264.h264")
	if err := writeSampleLog(logPath, 102, sampleH264Payload); err != nil {
		t.Fatalf("write sample log: %v", err)
	}
	result, err := WriteElementaryFromRTPLog(logPath, "video/h264", 90000, outPath)
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
	if info.Size() == 0 {
		t.Fatalf("empty output")
	}
}

func TestWriteElementaryFromRTPLogUnsupportedCodec(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	if err := writeSampleLog(logPath, 96, sampleVP8Payload); err != nil {
		t.Fatalf("write sample log: %v", err)
	}
	_, err := WriteElementaryFromRTPLog(logPath, "audio/pcmu", 8000, filepath.Join(tmp, "out.pcmu"))
	if err == nil {
		t.Fatalf("expected unsupported codec error")
	}
}

func writeSampleLog(path string, payloadType uint8, payload func() []byte) error {
	return writeSampleLogWithTimestamps(path, payloadType, payload, []uint32{1000, 2000})
}

func writeSampleLogWithTimestamps(path string, payloadType uint8, payload func() []byte, timestamps []uint32) error {
	if len(timestamps) == 0 {
		return nil
	}
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

	recvNS := uint64(2000)
	seq := uint16(100)
	for _, ts := range timestamps {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    payloadType,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           7777,
				Marker:         true,
			},
			Payload: payload(),
		}
		seq++
		wire, err := packet.Marshal()
		if err != nil {
			return err
		}
		if err := writer.Write(store.Record{RecvMonoNS: recvNS, Kind: store.KindRTP, WireBytes: wire}); err != nil {
			return err
		}
		recvNS += 1000
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: recvNS + 500,
		Kind:       store.KindRTCP,
		WireBytes:  []byte{0x80, 0xc9, 0x00, 0x01},
	}); err != nil {
		return err
	}
	return writer.Close()
}

func readIVFFramePTS(path string) (pts []uint64, timebaseNum, timebaseDen uint32, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() {
		_ = f.Close()
	}()

	header := make([]byte, 32)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, 0, 0, err
	}
	if string(header[0:4]) != "DKIF" {
		return nil, 0, 0, io.ErrUnexpectedEOF
	}
	timebaseDen = binary.LittleEndian.Uint32(header[16:20])
	timebaseNum = binary.LittleEndian.Uint32(header[20:24])

	out := make([]uint64, 0, 16)
	frameHeader := make([]byte, 12)
	for {
		if _, err := io.ReadFull(f, frameHeader); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, 0, 0, err
		}
		size := binary.LittleEndian.Uint32(frameHeader[0:4])
		out = append(out, binary.LittleEndian.Uint64(frameHeader[4:12]))
		if size == 0 {
			continue
		}
		if _, err := io.CopyN(io.Discard, f, int64(size)); err != nil {
			return nil, 0, 0, err
		}
	}

	return out, timebaseNum, timebaseDen, nil
}

func sampleOpusPayload() []byte {
	return []byte{0xf8, 0xff, 0xfe}
}

func sampleVP8Payload() []byte {
	// VP8 payload descriptor (S=1, PID=0) + minimal keyframe header bytes.
	return []byte{0x10, 0x00, 0x00, 0x00, 0x9d, 0x01, 0x2a, 0x00, 0x00, 0x00}
}

func sampleVP9Payload() []byte {
	// VP9 payload descriptor with beginning-of-frame bit set.
	return []byte{0x08, 0x01, 0x20}
}

func sampleH264Payload() []byte {
	// SPS NALU (type 7) so h264 writer treats this as keyframe access unit.
	return []byte{0x67, 0x42, 0x00, 0x1f, 0xe5, 0x88, 0x68}
}
