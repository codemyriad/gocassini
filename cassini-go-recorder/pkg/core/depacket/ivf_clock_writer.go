package depacket

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

type ivfClockWriter struct {
	file *os.File

	codec     string
	clockRate uint32

	count              uint32
	firstFrameTS       uint32
	firstFrameTSSet    bool
	seenKeyFrame       bool
	currentFrameBuffer []byte
}

func newIVFClockWriter(path, codec string, clockRate uint32) (*ivfClockWriter, error) {
	fourcc := ""
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "vp8":
		fourcc = "VP80"
	case "vp9":
		fourcc = "VP90"
	default:
		return nil, fmt.Errorf("unsupported ivf codec %q", codec)
	}
	if clockRate == 0 {
		return nil, errors.New("clock rate must be > 0")
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	writer := &ivfClockWriter{
		file:      file,
		codec:     codec,
		clockRate: clockRate,
	}
	if err := writer.writeHeader(fourcc); err != nil {
		_ = file.Close()
		return nil, err
	}
	return writer, nil
}

func (w *ivfClockWriter) writeHeader(fourcc string) error {
	header := make([]byte, 32)
	copy(header[0:], "DKIF")
	binary.LittleEndian.PutUint16(header[4:], 0)
	binary.LittleEndian.PutUint16(header[6:], 32)
	copy(header[8:], fourcc)
	// width/height are optional hints for IVF demuxers; use defaults.
	binary.LittleEndian.PutUint16(header[12:], 640)
	binary.LittleEndian.PutUint16(header[14:], 480)
	// Preserve RTP clock directly: timebase = 1/clockRate.
	binary.LittleEndian.PutUint32(header[16:], w.clockRate)
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint32(header[24:], 0)
	binary.LittleEndian.PutUint32(header[28:], 0)
	_, err := w.file.Write(header)
	return err
}

func (w *ivfClockWriter) WriteRTP(packet *rtp.Packet) error {
	if w.file == nil {
		return errors.New("ivf writer closed")
	}
	if packet == nil || len(packet.Payload) == 0 {
		return nil
	}
	if !w.firstFrameTSSet {
		w.firstFrameTS = packet.Timestamp
		w.firstFrameTSSet = true
	}
	relativeTS := uint64(packet.Timestamp - w.firstFrameTS)

	switch strings.ToLower(strings.TrimSpace(w.codec)) {
	case "vp8":
		vp8Packet := codecs.VP8Packet{}
		if _, err := vp8Packet.Unmarshal(packet.Payload); err != nil {
			return err
		}
		if len(vp8Packet.Payload) == 0 {
			return nil
		}
		isKeyFrame := (vp8Packet.Payload[0] & 0x01) == 0
		switch {
		case !w.seenKeyFrame && !isKeyFrame:
			return nil
		case w.currentFrameBuffer == nil && vp8Packet.S != 1:
			return nil
		}
		w.seenKeyFrame = true
		w.currentFrameBuffer = append(w.currentFrameBuffer, vp8Packet.Payload...)
	case "vp9":
		vp9Packet := codecs.VP9Packet{}
		if _, err := vp9Packet.Unmarshal(packet.Payload); err != nil {
			return err
		}
		switch {
		case !w.seenKeyFrame && vp9Packet.P:
			return nil
		case w.currentFrameBuffer == nil && !vp9Packet.B:
			return nil
		}
		w.seenKeyFrame = true
		w.currentFrameBuffer = append(w.currentFrameBuffer, vp9Packet.Payload...)
	default:
		return fmt.Errorf("unsupported ivf codec %q", w.codec)
	}

	if !packet.Marker || len(w.currentFrameBuffer) == 0 {
		return nil
	}

	frameHeader := make([]byte, 12)
	binary.LittleEndian.PutUint32(frameHeader[0:], uint32(len(w.currentFrameBuffer)))
	binary.LittleEndian.PutUint64(frameHeader[4:], relativeTS)
	if _, err := w.file.Write(frameHeader); err != nil {
		return err
	}
	if _, err := w.file.Write(w.currentFrameBuffer); err != nil {
		return err
	}
	w.count++
	w.currentFrameBuffer = nil
	return nil
}

func (w *ivfClockWriter) Close() error {
	if w.file == nil {
		return nil
	}
	file := w.file
	w.file = nil

	if _, err := file.Seek(24, 0); err != nil {
		_ = file.Close()
		return err
	}
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, w.count)
	if _, err := file.Write(buf); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
