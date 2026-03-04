package depacket

import (
	"errors"
	"fmt"
	"io"
	"math"
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
	var timeline recvTimeline
	effectiveClockRate := effectiveClockRate(codec, clockRate)
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
			packet.Timestamp = timeline.rewrite(packet.Timestamp, record.RecvMonoNS, effectiveClockRate)
			if err := w.WriteRTP(&packet); err != nil {
				return WriteResult{}, fmt.Errorf("write rtp packet: %w", err)
			}
			if result.FirstRecvNS == 0 || record.RecvMonoNS < result.FirstRecvNS {
				result.FirstRecvNS = record.RecvMonoNS
			}
			if record.RecvMonoNS > result.LastRecvNS {
				result.LastRecvNS = record.RecvMonoNS
			}
			result.RTPPackets++
		}
	}

	if err := w.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("close writer: %w", err)
	}
	return result, nil
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
