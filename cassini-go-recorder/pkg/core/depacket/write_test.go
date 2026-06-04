package depacket

import (
	"encoding/binary"
	"io"
	"math"
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

func TestWriteElementaryFromRTPLogVP8UsesRecvTimelinePTS(t *testing.T) {
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

	stepTicks := uint64(math.Round(0.02 * 90_000))
	for i, got := range pts {
		want := uint64(i) * stepTicks
		if got != want {
			t.Fatalf("frame %d pts mismatch: got=%d want=%d", i, got, want)
		}
	}
}

func TestWriteElementaryFromRTPLogVP8PreservesReceiveGaps(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "video-gap.rtplog")
	outPath := filepath.Join(tmp, "video-gap.ivf")
	timestamps := []uint32{90_000, 93_000, 96_000}
	recvNS := []uint64{
		2_000_000_000,
		12_000_000_000,
		22_000_000_000,
	}
	if err := writeSampleLogWithTimestampsAndRecv(logPath, 96, sampleVP8Payload, timestamps, recvNS); err != nil {
		t.Fatalf("write sample log: %v", err)
	}

	if _, err := WriteElementaryFromRTPLog(logPath, "video/vp8", 90_000, outPath); err != nil {
		t.Fatalf("write elementary: %v", err)
	}
	pts, _, _, err := readIVFFramePTS(outPath)
	if err != nil {
		t.Fatalf("read ivf frame pts: %v", err)
	}
	if len(pts) != 3 {
		t.Fatalf("frame count mismatch: got=%d want=3", len(pts))
	}
	// Two 10s gaps at 90kHz should produce ~900,000 tick jumps each.
	want := []uint64{0, 900_000, 1_800_000}
	for idx := range want {
		if pts[idx] != want[idx] {
			t.Fatalf("frame %d pts mismatch: got=%d want=%d", idx, pts[idx], want[idx])
		}
	}
}

func TestRecvTimelineRewriteUsesReceiveClock(t *testing.T) {
	var tl recvTimeline
	const (
		clockRate = uint32(48_000)
		baseRecv  = uint64(1_000_000_000)
	)

	first := tl.rewrite(120, baseRecv, clockRate)
	if first != 120 {
		t.Fatalf("first timestamp changed: got=%d want=120", first)
	}

	// 20ms later -> +960 ticks at 48kHz.
	second := tl.rewrite(121, baseRecv+20_000_000, clockRate)
	if second != 1_080 {
		t.Fatalf("second timestamp mismatch: got=%d want=1080", second)
	}

	// 10s later, even with a tiny RTP step, should preserve receive-time gap.
	third := tl.rewrite(122, baseRecv+10_020_000_000, clockRate)
	if third != 481_080 {
		t.Fatalf("third timestamp mismatch: got=%d want=481080", third)
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

func TestWriteElementaryFromRTPLogFirstRecvTracksRTPOnly(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "stream.rtplog")
	outPath := filepath.Join(tmp, "stream.ogg")

	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "audio/opus",
		ClockRate:   48000,
		Direction:   "recvonly",
		StartMonoNS: 1_000,
		PT:          111,
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 100,
		Kind:       store.KindRTCP,
		WireBytes:  []byte{0x80, 0xc9, 0x00, 0x01},
	}); err != nil {
		t.Fatalf("write rtcp: %v", err)
	}
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: 1,
			Timestamp:      960,
			SSRC:           7,
			Marker:         true,
		},
		Payload: sampleOpusPayload(),
	}
	raw, err := packet.Marshal()
	if err != nil {
		t.Fatalf("marshal rtp: %v", err)
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: 200,
		Kind:       store.KindRTP,
		WireBytes:  raw,
	}); err != nil {
		t.Fatalf("write rtp: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	result, err := WriteElementaryFromRTPLog(logPath, "audio/opus", 48000, outPath)
	if err != nil {
		t.Fatalf("write elementary: %v", err)
	}
	if result.FirstRecvNS != 200 {
		t.Fatalf("first recv mismatch: got=%d want=200", result.FirstRecvNS)
	}
	if result.LastRecvNS != 200 {
		t.Fatalf("last recv mismatch: got=%d want=200", result.LastRecvNS)
	}
}

func writeSampleLog(path string, payloadType uint8, payload func() []byte) error {
	return writeSampleLogWithTimestamps(path, payloadType, payload, []uint32{1000, 2000})
}

func writeSampleLogWithTimestamps(path string, payloadType uint8, payload func() []byte, timestamps []uint32) error {
	if len(timestamps) == 0 {
		return nil
	}
	recvNS := make([]uint64, 0, len(timestamps))
	next := uint64(2_000_000_000)
	for range timestamps {
		recvNS = append(recvNS, next)
		next += 20_000_000
	}
	return writeSampleLogWithTimestampsAndRecv(path, payloadType, payload, timestamps, recvNS)
}

func writeSampleLogWithTimestampsAndRecv(
	path string,
	payloadType uint8,
	payload func() []byte,
	timestamps []uint32,
	recvTimes []uint64,
) error {
	if len(timestamps) == 0 {
		return nil
	}
	if len(recvTimes) != len(timestamps) {
		return io.ErrShortBuffer
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

	seq := uint16(100)
	for idx, ts := range timestamps {
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
		if err := writer.Write(store.Record{RecvMonoNS: recvTimes[idx], Kind: store.KindRTP, WireBytes: wire}); err != nil {
			return err
		}
	}
	if err := writer.Write(store.Record{
		RecvMonoNS: recvTimes[len(recvTimes)-1] + 500,
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

// TestWriteElementaryFromRTPLogReordersRetransmissions reproduces the
// "smeared video" bug: rtplogs store packets in ARRIVAL order, so a
// NACK-recovered retransmission lands long after its sequence position.
// The depacketizer must see packets in sequence order with stable per-frame
// timestamps, or the frame containing the recovered packet (and everything
// until the next keyframe) is corrupted. The test writes the same multi-
// packet frames twice — once in order, once with one packet arriving 12
// packets late — and requires byte-identical elementary output.
func TestWriteElementaryFromRTPLogReordersRetransmissions(t *testing.T) {
	tmp := t.TempDir()

	type pkt struct {
		seq    uint16
		ts     uint32
		marker bool
		first  bool // first packet of its frame (VP8 S bit)
		recvNS uint64
	}

	// Six frames of three packets each, 3000 ticks (30 ms) apart.
	var ordered []pkt
	seq := uint16(100)
	recv := uint64(2_000_000_000)
	for frame := 0; frame < 6; frame++ {
		ts := uint32(90_000 + frame*3_000)
		for part := 0; part < 3; part++ {
			ordered = append(ordered, pkt{
				seq:    seq,
				ts:     ts,
				marker: part == 2,
				first:  part == 0,
				recvNS: recv,
			})
			seq++
			recv += 10_000_000
		}
	}

	// Arrival order: packet index 4 (middle of frame 2) is lost on first
	// transmission and arrives last, ~12 packets late, with a late recv time.
	late := ordered[4]
	late.recvNS = recv + 50_000_000
	arrival := append(append([]pkt{}, ordered[:4]...), ordered[5:]...)
	arrival = append(arrival, late)

	writeLog := func(path string, pkts []pkt) error {
		writer, err := store.NewWriter(path, store.StreamHeader{
			StreamID:    "s_000001",
			Codec:       "video/vp8",
			ClockRate:   90000,
			Direction:   "recvonly",
			StartMonoNS: 1000,
			PT:          96,
		})
		if err != nil {
			return err
		}
		defer func() { _ = writer.Close() }()
		for _, p := range pkts {
			payload := []byte{0x00, 0xaa, byte(p.seq), byte(p.seq >> 8)}
			if p.first {
				payload[0] = 0x10 // VP8 descriptor S=1, PID=0
				payload = append(payload, 0x9d, 0x01, 0x2a, 0x00, 0x00, 0x00)
			}
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: p.seq,
					Timestamp:      p.ts,
					SSRC:           7777,
					Marker:         p.marker,
				},
				Payload: payload,
			}
			wire, err := packet.Marshal()
			if err != nil {
				return err
			}
			if err := writer.Write(store.Record{
				RecvMonoNS: p.recvNS,
				Kind:       store.KindRTP,
				WireBytes:  wire,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	orderedLog := filepath.Join(tmp, "ordered.rtplog")
	arrivalLog := filepath.Join(tmp, "arrival.rtplog")
	if err := writeLog(orderedLog, ordered); err != nil {
		t.Fatalf("write ordered log: %v", err)
	}
	if err := writeLog(arrivalLog, arrival); err != nil {
		t.Fatalf("write arrival log: %v", err)
	}

	orderedOut := filepath.Join(tmp, "ordered.ivf")
	arrivalOut := filepath.Join(tmp, "arrival.ivf")
	orderedRes, err := WriteElementaryFromRTPLog(orderedLog, "video/vp8", 90000, orderedOut)
	if err != nil {
		t.Fatalf("write ordered elementary: %v", err)
	}
	arrivalRes, err := WriteElementaryFromRTPLog(arrivalLog, "video/vp8", 90000, arrivalOut)
	if err != nil {
		t.Fatalf("write arrival elementary: %v", err)
	}
	if orderedRes.RTPPackets != len(ordered) || arrivalRes.RTPPackets != len(ordered) {
		t.Fatalf("rtp packet counts: ordered=%d arrival=%d want=%d",
			orderedRes.RTPPackets, arrivalRes.RTPPackets, len(ordered))
	}

	orderedBytes, err := os.ReadFile(orderedOut)
	if err != nil {
		t.Fatalf("read ordered output: %v", err)
	}
	arrivalBytes, err := os.ReadFile(arrivalOut)
	if err != nil {
		t.Fatalf("read arrival output: %v", err)
	}
	if len(orderedBytes) <= 32 {
		t.Fatalf("ordered output too small: %d bytes", len(orderedBytes))
	}
	if string(orderedBytes) != string(arrivalBytes) {
		t.Fatalf("outputs differ: a late retransmission changed the elementary stream (ordered=%d bytes, arrival=%d bytes)",
			len(orderedBytes), len(arrivalBytes))
	}
}

// TestWriteElementaryFromRTPLogDropsDuplicateRetransmissions verifies that a
// duplicate of an already-delivered packet (retransmission that raced the
// original) is dropped instead of being fed to the depacketizer twice.
func TestWriteElementaryFromRTPLogDropsDuplicateRetransmissions(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "dup.rtplog")
	outPath := filepath.Join(tmp, "dup.ivf")

	writer, err := store.NewWriter(logPath, store.StreamHeader{
		StreamID:    "s_000001",
		Codec:       "video/vp8",
		ClockRate:   90000,
		Direction:   "recvonly",
		StartMonoNS: 1000,
		PT:          96,
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	seqs := []uint16{100, 101, 101, 102}
	recv := uint64(2_000_000_000)
	for _, s := range seqs {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: s,
				Timestamp:      90_000,
				SSRC:           7777,
				Marker:         s == 102,
			},
			Payload: sampleVP8Payload(),
		}
		wire, err := packet.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := writer.Write(store.Record{RecvMonoNS: recv, Kind: store.KindRTP, WireBytes: wire}); err != nil {
			t.Fatalf("write: %v", err)
		}
		recv += 10_000_000
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result, err := WriteElementaryFromRTPLog(logPath, "video/vp8", 90000, outPath)
	if err != nil {
		t.Fatalf("write elementary: %v", err)
	}
	if result.RTPPackets != 3 {
		t.Fatalf("rtp packets written: got=%d want=3 (duplicate must be dropped)", result.RTPPackets)
	}
}
