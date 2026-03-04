package depacket

import (
	"errors"
	"fmt"
	"io"
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
	for {
		record, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return WriteResult{}, fmt.Errorf("read rtplog record: %w", err)
		}

		if result.FirstRecvNS == 0 || record.RecvMonoNS < result.FirstRecvNS {
			result.FirstRecvNS = record.RecvMonoNS
		}
		if record.RecvMonoNS > result.LastRecvNS {
			result.LastRecvNS = record.RecvMonoNS
		}

		switch record.Kind {
		case store.KindRTCP:
			result.RTCPPackets++
		case store.KindRTP:
			var packet rtp.Packet
			if err := packet.Unmarshal(record.WireBytes); err != nil {
				return WriteResult{}, fmt.Errorf("unmarshal rtp packet: %w", err)
			}
			if err := w.WriteRTP(&packet); err != nil {
				return WriteResult{}, fmt.Errorf("write rtp packet: %w", err)
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
		w, err := ivfwriter.New(outputPath)
		if err != nil {
			return nil, fmt.Errorf("create ivf writer: %w", err)
		}
		return w, nil
	case strings.Contains(lc, "vp9"):
		w, err := ivfwriter.New(outputPath, ivfwriter.WithCodec("video/VP9"))
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
