package depacket

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/h264writer"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
)

type WriteResult struct {
	RTPPackets  int
	RTCPPackets int
	FirstRecvNS uint64
	LastRecvNS  uint64
}

type rtpWriter interface {
	WriteRTP(packet *rtp.Packet) error
	Close() error
}

func WriteElementaryFromRTPLog(logPath, codec string, clockRate uint32, outputPath string) (WriteResult, error) {
	w, err := newRTPWriter(codec, clockRate, outputPath)
	if err != nil {
		return WriteResult{}, err
	}
	defer func() {
		_ = w.Close()
	}()

	reader, err := store.OpenReader(logPath)
	if err != nil {
		return WriteResult{}, fmt.Errorf("open rtplog: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()

	var result WriteResult
	emitter := newOrderedEmitter(w, effectiveClockRate(codec, clockRate), &result)
	for {
		record, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return WriteResult{}, fmt.Errorf("read rtplog record: %w", err)
		}

		switch record.Kind {
		case store.KindRTCP:
			result.RTCPPackets++
		case store.KindRTP:
			var packet rtp.Packet
			if err := packet.Unmarshal(record.WireBytes); err != nil {
				return WriteResult{}, fmt.Errorf("unmarshal rtp packet: %w", err)
			}
			if err := emitter.push(&packet, record.RecvMonoNS); err != nil {
				return WriteResult{}, fmt.Errorf("write rtp packet: %w", err)
			}
			if result.FirstRecvNS == 0 || record.RecvMonoNS < result.FirstRecvNS {
				result.FirstRecvNS = record.RecvMonoNS
			}
			if record.RecvMonoNS > result.LastRecvNS {
				result.LastRecvNS = record.RecvMonoNS
			}
		}
	}
	if err := emitter.flush(); err != nil {
		return WriteResult{}, fmt.Errorf("write rtp packet: %w", err)
	}

	if err := w.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("close writer: %w", err)
	}
	return result, nil
}

// reorderWindowPackets is how many packets the emitter buffers before
// releasing the lowest-sequence one. Captured rtplogs store packets in
// ARRIVAL order, and NACK-recovered retransmissions arrive hundreds of
// packets after their sequence position. Feeding them to the depacketizer
// in arrival order tears the frame they belong to — and every frame after
// it until the next keyframe (which WebRTC senders emit ~100 s apart),
// which is exactly the long "smeared face" corruption observed on real
// Talk recordings. 512 packets ≈ several seconds of video, comfortably
// above observed NACK round-trips.
const reorderWindowPackets = 512

// orderedEmitter restores RTP sequence order with a bounded look-ahead
// window, drops duplicate retransmissions, and rewrites timestamps once per
// frame (per distinct original RTP timestamp) so all packets of a frame stay
// on one timestamp. The previous per-packet rewrite stamped each packet with
// its own arrival time, which (a) spread one frame across several
// timestamps and (b) pushed late retransmissions onto a *newer* monotonic
// timestamp than the frame they belong to, corrupting reassembly.
type orderedEmitter struct {
	w         rtpWriter
	clockRate uint32
	result    *WriteResult

	buf      []seqPacket // kept sorted by ext ascending
	haveSeq  bool
	highest  uint16
	highExt  int64
	lastEmit int64

	timeline   recvTimeline
	haveFrame  bool
	frameTS    uint32 // original RTP timestamp of the current frame
	frameNewTS uint32 // rewritten timestamp for that frame
}

type seqPacket struct {
	ext    int64
	recvNS uint64
	pkt    *rtp.Packet
}

func newOrderedEmitter(w rtpWriter, clockRate uint32, result *WriteResult) *orderedEmitter {
	return &orderedEmitter{w: w, clockRate: clockRate, result: result, lastEmit: math.MinInt64}
}

func (e *orderedEmitter) push(pkt *rtp.Packet, recvNS uint64) error {
	var ext int64
	if !e.haveSeq {
		e.haveSeq = true
		e.highest = pkt.SequenceNumber
		e.highExt = int64(pkt.SequenceNumber)
		ext = e.highExt
	} else {
		delta := int16(pkt.SequenceNumber - e.highest) // wrap-aware
		ext = e.highExt + int64(delta)
		if delta > 0 {
			e.highest = pkt.SequenceNumber
			e.highExt = ext
		}
	}

	// Insert sorted by extended sequence (binary search; the buffer is
	// almost always already ordered, so this is effectively an append).
	idx := sort.Search(len(e.buf), func(i int) bool { return e.buf[i].ext > ext })
	e.buf = append(e.buf, seqPacket{})
	copy(e.buf[idx+1:], e.buf[idx:])
	e.buf[idx] = seqPacket{ext: ext, recvNS: recvNS, pkt: pkt}

	for len(e.buf) > reorderWindowPackets {
		if err := e.emit(e.buf[0]); err != nil {
			return err
		}
		e.buf = e.buf[1:]
	}
	return nil
}

func (e *orderedEmitter) flush() error {
	for _, sp := range e.buf {
		if err := e.emit(sp); err != nil {
			return err
		}
	}
	e.buf = nil
	return nil
}

func (e *orderedEmitter) emit(sp seqPacket) error {
	if sp.ext <= e.lastEmit && e.lastEmit != math.MinInt64 {
		// Duplicate retransmission, or a packet that arrived too late for
		// the reorder window. Either way the depacketizer already moved on.
		return nil
	}
	e.lastEmit = sp.ext

	if !e.haveFrame || sp.pkt.Timestamp != e.frameTS {
		e.haveFrame = true
		e.frameTS = sp.pkt.Timestamp
		e.frameNewTS = e.timeline.rewrite(sp.pkt.Timestamp, sp.recvNS, e.clockRate)
	}
	sp.pkt.Timestamp = e.frameNewTS
	if err := e.w.WriteRTP(sp.pkt); err != nil {
		return err
	}
	e.result.RTPPackets++
	return nil
}

func newRTPWriter(codec string, clockRate uint32, outputPath string) (rtpWriter, error) {
	lc := strings.ToLower(strings.TrimSpace(codec))
	switch {
	case strings.Contains(lc, "opus"):
		if clockRate == 0 {
			clockRate = 48000
		}
		w, err := oggwriter.New(outputPath, clockRate, 2)
		if err != nil {
			return nil, fmt.Errorf("create ogg writer: %w", err)
		}
		return w, nil
	case strings.Contains(lc, "vp8"):
		w, err := newIVFClockWriter(outputPath, "vp8", safeClockRate(clockRate, 90000))
		if err != nil {
			return nil, fmt.Errorf("create ivf writer: %w", err)
		}
		return w, nil
	case strings.Contains(lc, "vp9"):
		w, err := newIVFClockWriter(outputPath, "vp9", safeClockRate(clockRate, 90000))
		if err != nil {
			return nil, fmt.Errorf("create vp9 ivf writer: %w", err)
		}
		return w, nil
	case strings.Contains(lc, "av1"):
		w, err := ivfwriter.New(outputPath, ivfwriter.WithCodec("video/AV1"))
		if err != nil {
			return nil, fmt.Errorf("create av1 ivf writer: %w", err)
		}
		return w, nil
	case strings.Contains(lc, "h264"):
		w, err := h264writer.New(outputPath)
		if err != nil {
			return nil, fmt.Errorf("create h264 writer: %w", err)
		}
		return w, nil
	default:
		return nil, fmt.Errorf("unsupported codec for elementary write: %s", codec)
	}
}

func effectiveClockRate(codec string, clockRate uint32) uint32 {
	if clockRate > 0 {
		return clockRate
	}
	lc := strings.ToLower(strings.TrimSpace(codec))
	switch {
	case strings.Contains(lc, "opus"):
		return 48000
	case strings.Contains(lc, "vp8"), strings.Contains(lc, "vp9"), strings.Contains(lc, "h264"), strings.Contains(lc, "av1"):
		return 90000
	default:
		return 90000
	}
}

func safeClockRate(clockRate, fallback uint32) uint32 {
	if clockRate == 0 {
		return fallback
	}
	return clockRate
}

type recvTimeline struct {
	initialized bool
	baseRecvNS  uint64
	baseTS      uint32
	lastTS      int64
}

func (t *recvTimeline) rewrite(originalTS uint32, recvNS uint64, clockRate uint32) uint32 {
	if !t.initialized {
		t.initialized = true
		t.baseRecvNS = recvNS
		t.baseTS = originalTS
		t.lastTS = int64(originalTS)
		return originalTS
	}

	if recvNS < t.baseRecvNS {
		recvNS = t.baseRecvNS
	}
	deltaSeconds := float64(recvNS-t.baseRecvNS) / 1e9
	recvTicks := uint64(math.Round(deltaSeconds * float64(clockRate)))
	candidate := uint32(uint64(t.baseTS) + recvTicks)
	unwrapped := unwrap32(t.lastTS, candidate)
	if unwrapped < t.lastTS {
		unwrapped = t.lastTS
		candidate = uint32(unwrapped)
	}
	t.lastTS = unwrapped
	return candidate
}

func unwrap32(last int64, raw uint32) int64 {
	raw64 := int64(raw)
	delta := raw64 - (last & 0xffffffff)
	for delta > (1 << 31) {
		delta -= 1 << 32
	}
	for delta < -(1 << 31) {
		delta += 1 << 32
	}
	return last + delta
}
